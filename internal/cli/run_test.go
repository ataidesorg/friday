package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	gitexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ataidesorg/ink/internal/config"
	"github.com/ataidesorg/ink/internal/core"
	"github.com/ataidesorg/ink/internal/observability"
	"github.com/ataidesorg/ink/internal/runtime"
	"github.com/ataidesorg/ink/internal/sandbox"
)

var scripts = filepath.Join("..", "..", "test", "scripts")

// copyFixture duplicates the sample project into a temp dir so runs never
// dirty the repository; a plain directory gets an ephemeral workspace.
func copyFixture(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.WalkDir(sample, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(sample, p)
		if rel == filepath.Join(".ink", "local") {
			return filepath.SkipDir // trails left by manual runs are not fixture data
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		b, err := os.ReadFile(p) //nolint:gosec // fixture path
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o600) //nolint:gosec // target stays under t.TempDir()
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

// execRunHome invokes the CLI with a caller-owned HOME so state written by
// one invocation (the trust store) is visible to the next.
func execRunHome(t *testing.T, home, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	environ := []string{"HOME=" + home, "PATH=/usr/bin"}
	code := Run(args, &stdout, &stderr, strings.NewReader(stdin), environ, func() (string, error) { return t.TempDir(), nil })
	return code, stdout.String(), stderr.String()
}

func execRun(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	return execRunHome(t, t.TempDir(), stdin, args...)
}

// trustProject records root's .ink/config.toml as trusted in HOME's
// state store, the way an owner runs `ink trust` before a run.
func trustProject(t *testing.T, home, root string) {
	t.Helper()
	cfg, err := filepath.Abs(filepath.Join(root, ".ink", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if code, out, errOut := execRunHome(t, home, "", "trust", cfg); code != exitOK {
		t.Fatalf("trust: %d\nstdout: %s\nstderr: %s", code, out, errOut)
	}
}

func trailEvents(t *testing.T, root string) []core.Event {
	t.Helper()
	dirs, err := filepath.Glob(filepath.Join(root, observability.LocalDir, "runs", "*", "events.jsonl"))
	if err != nil || len(dirs) != 1 {
		t.Fatalf("trail files: %v %v", dirs, err)
	}
	events, err := observability.ReadTrail(dirs[0])
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func TestRunAddFarewellVerified(t *testing.T) {
	root := copyFixture(t)
	home := t.TempDir()
	trustProject(t, home, root)
	code, out, errOut := execRunHome(t, home, "", "run", "--project", root, "--script", filepath.Join(scripts, "add-farewell.json"), "--no-tui", "--yes", "add Farewell(name) with a test")
	if code != exitOK || !strings.Contains(out, "completed_verified") {
		t.Fatalf("run: %d\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(root, "farewell.go")); err == nil {
		t.Fatal("ephemeral workspace wrote into the project root")
	}
	events := trailEvents(t, root)
	var completed bool
	for _, e := range events {
		if e.Kind == core.EventTaskFinished {
			completed = true
		}
	}
	if !completed {
		t.Fatalf("trail lacks run completion (%d events)", len(events))
	}
}

func TestRunGoalProseDoesNotComplete(t *testing.T) {
	root := copyFixture(t)
	home := t.TempDir()
	trustProject(t, home, root)
	code, out, errOut := execRunHome(t, home, "", "run", "--project", root, "--script", filepath.Join(scripts, "prose-done.json"), "--no-tui", "--yes", "--goal", "ship the feature", "say you are done")
	if code != exitOK {
		t.Fatalf("run: %d\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	if strings.Contains(errOut, "goal complete") {
		t.Fatalf("ink run invented a completion:\n%s", errOut)
	}
	if !strings.Contains(errOut, "goal active") {
		t.Fatalf("want goal active on stderr:\n%s", errOut)
	}
}

func TestRunForbiddenRmDenied(t *testing.T) {
	root := copyFixture(t)
	home := t.TempDir()
	trustProject(t, home, root)
	code, out, errOut := execRunHome(t, home, "", "run", "--project", root, "--script", filepath.Join(scripts, "forbidden-rm.json"), "--no-tui", "--yes", "wipe things")
	if code != exitOK {
		t.Fatalf("run: %d\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	var denied bool
	for _, e := range trailEvents(t, root) {
		if d, ok := e.Data.(core.PolicyDecided); ok && d.Tool == "run_command" && d.Effect == core.EffectDeny {
			denied = true
		}
	}
	if !denied {
		t.Fatal("trail has no policy denial for run_command")
	}
}

// TestRunUntrustedCommandsDropped is the regression test for the trust-gate
// bypass: an untrusted repository config must not feed the command allowlist,
// so the scripted `go test ./...` is denied (the script then mismatches and
// the run fails — it never completes verified).
func TestRunUntrustedCommandsDropped(t *testing.T) {
	root := copyFixture(t)
	code, out, errOut := execRun(t, "", "run", "--project", root, "--script", filepath.Join(scripts, "add-farewell.json"), "--no-tui", "--yes", "add Farewell(name) with a test")
	if code != exitFailed || strings.Contains(out, "completed_verified") {
		t.Fatalf("untrusted run: %d\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	if !strings.Contains(out, `command "go test ./..." is not in tools.commands.allowed`) {
		t.Fatalf("go test not denied:\n%s", out)
	}
	if !strings.Contains(errOut, "project.commands.test") || !strings.Contains(errOut, "ink trust") {
		t.Fatalf("no dropped-keys warning: %q", errOut)
	}
	var denied bool
	for _, e := range trailEvents(t, root) {
		if d, ok := e.Data.(core.PolicyDecided); ok && d.Tool == "run_command" && d.Effect == core.EffectDeny {
			denied = true
		}
	}
	if !denied {
		t.Fatal("untrusted repo command was not denied")
	}
}

func TestRunWithoutYesDeniesWrites(t *testing.T) {
	root := copyFixture(t)
	code, out, _ := execRun(t, "", "run", "--project", root, "--script", filepath.Join(scripts, "add-farewell.json"), "--no-tui", "add Farewell(name) with a test")
	if code == exitOK || strings.Contains(out, "completed_verified") {
		t.Fatalf("writes approved without --yes or an answer: %d %s", code, out)
	}
	var denied bool
	for _, e := range trailEvents(t, root) {
		if d, ok := e.Data.(core.ApprovalResolved); ok && d.Decision == core.ApprovalDenied {
			denied = true
		}
	}
	if !denied {
		t.Fatal("trail has no denied approval")
	}
}

func TestRunExitCodes(t *testing.T) {
	root := copyFixture(t)
	if code, _, errOut := execRun(t, "", "run", "--project", root, "--no-tui", "do it"); code != exitError || !strings.Contains(errOut, "no model routes configured") {
		t.Fatalf("no script, no routes: %d %q", code, errOut)
	}
	if code, _, errOut := execRun(t, "", "run", "--project", invalid, "--script", filepath.Join(scripts, "add-farewell.json"), "--no-tui", "do it"); code != exitConfigInvalid || !strings.Contains(errOut, "configuration is invalid") {
		t.Fatalf("invalid config: %d %q", code, errOut)
	}
	if code, _, _ := execRun(t, "", "run"); code != exitUsage {
		t.Fatalf("no task: %d", code)
	}
	if code, _, _ := execRun(t, "", "run", "--bogus", "x"); code != exitUsage {
		t.Fatalf("bad flag: %d", code)
	}
	if code, _, errOut := execRun(t, "", "run", "--project", root, "--script", filepath.Join(scripts, "missing.json"), "--no-tui", "x"); code != exitFailed || !strings.Contains(errOut, "missing.json") {
		t.Fatalf("missing script: %d %q", code, errOut)
	}
	if code, _, errOut := execRun(t, "", "run", "--project", root, "--script", filepath.Join(scripts, "add-farewell.json"), "--no-tui", "--set", "sandbox.provider=unavailable", "x"); code != exitNotImplemented || !strings.Contains(errOut, "sandbox") {
		t.Fatalf("unavailable sandbox: %d %q", code, errOut)
	}
}

func TestExitFor(t *testing.T) {
	cases := map[core.Outcome]int{
		{Kind: core.OutcomeCompletedVerified}:                           exitOK,
		{Kind: core.OutcomeCompletedUnverified}:                         exitUnverified,
		{Kind: core.OutcomeEscalated}:                                   exitEscalated,
		{Kind: core.OutcomeRolledBack}:                                  exitRolledBack,
		{Kind: core.OutcomeFailed, Category: core.FailureProviderError}: exitFailed,
		{Kind: core.OutcomeFailed, Category: core.FailurePolicyDenied}:  exitPolicyDenied,
		{Kind: core.OutcomeKind("weird")}:                               exitFailed,
	}
	for o, want := range cases {
		if got := exitFor(o); got != want {
			t.Errorf("%+v: got %d want %d", o, got, want)
		}
	}
}

func TestSplitCommand(t *testing.T) {
	good := map[string][]string{
		"go test ./...":          {"go", "test", "./..."},
		`go test -run 'A B' ./…`: {"go", "test", "-run", "A B", "./…"},
		`echo "x y"  z`:          {"echo", "x y", "z"},
		"  go   build  ":         {"go", "build"},
	}
	for in, want := range good {
		got, err := splitCommand(in)
		if err != nil || strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	for _, bad := range []string{"go test ./... && rm -rf /", "a | b", "a; b", "a > b", "a < b", "a `b`", "a $(b)", "echo 'unterminated", "", "   "} {
		if _, err := splitCommand(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestYesApprover(t *testing.T) {
	fallback := func(context.Context, core.Approval) (core.ApprovalResolution, error) {
		return core.ApprovalResolution{Decision: core.ApprovalDenied, Note: "fallback"}, nil
	}
	approve := yesApprover(fallback)
	for risk, want := range map[core.RiskClass]core.ApprovalDecision{
		core.RiskWriteLocal:    core.ApprovalApproved,
		core.RiskExecuteLocal:  core.ApprovalApproved,
		core.RiskDestructive:   core.ApprovalDenied,
		core.RiskPrivileged:    core.ApprovalDenied,
		core.RiskSecretBearing: core.ApprovalDenied,
		core.RiskNetworkWrite:  core.ApprovalDenied,
	} {
		res, err := approve(context.Background(), core.Approval{Request: core.CapabilityRequest{Capability: core.Capability{Risk: risk}}})
		if err != nil || res.Decision != want {
			t.Errorf("%s: %v %v", risk, res, err)
		}
		if want == core.ApprovalApproved && (res.Scope != core.ApprovalSession || res.By.Name != "--yes") {
			t.Errorf("%s: %+v", risk, res)
		}
	}
	res, err := yesApprover(nil)(context.Background(), core.Approval{Request: core.CapabilityRequest{Capability: core.Capability{Risk: core.RiskDestructive}}})
	if err != nil || res.Decision != core.ApprovalDenied {
		t.Fatalf("nil fallback: %v %v", res, err)
	}
}

func TestTrustPromptAndReadLine(t *testing.T) {
	var out bytes.Buffer
	in := strings.NewReader("y\nrest")
	dec, err := trustPrompt(in, &out)("/p/.ink/config.toml", []string{"sandbox.provider"})
	if err != nil || dec != config.TrustTrusted || !strings.Contains(out.String(), "sandbox.provider") {
		t.Fatalf("yes: %v %v %q", dec, err, out.String())
	}
	if rest, _ := io.ReadAll(in); string(rest) != "rest" {
		t.Fatalf("prompt consumed past its line: %q", rest)
	}
	for _, answer := range []string{"n\n", "\n", "", "yes\n"} {
		dec, err := trustPrompt(strings.NewReader(answer), io.Discard)("/p", nil)
		if err != nil || dec != config.TrustUntrusted {
			t.Errorf("%q: %v %v", answer, dec, err)
		}
	}
	if line, err := readLine(strings.NewReader("abc")); line != "abc" || err == nil {
		t.Fatalf("eof line: %q %v", line, err)
	}
	f, err := os.CreateTemp(t.TempDir(), "plain")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if isTerminal(strings.NewReader("")) || isTerminal(f) {
		t.Fatal("non-terminals reported as terminals")
	}
}

func TestRunHelpers(t *testing.T) {
	if userName(nil) != "owner" || userName([]string{"USER=lu"}) != "lu" {
		t.Fatal("userName")
	}
	if b, err := budgetFrom(config.BudgetsConfig{PerTaskUSD: 1.5}); err != nil || b.MaxCost.Float() != 1.5 {
		t.Fatalf("budget: %v %v", b, err)
	}
	if _, err := budgetFrom(config.BudgetsConfig{PerTaskUSD: 1e15}); err == nil {
		t.Fatal("huge budget accepted")
	}
	if cmd, err := testCommand(map[string]string{"test": " "}); err != nil || cmd != nil {
		t.Fatalf("blank test command: %v %v", cmd, err)
	}
	if _, err := testCommand(map[string]string{"test": "a | b"}); err == nil {
		t.Fatal("shell test command accepted")
	}
	tc, err := withProjectCommands(config.ToolsConfig{Commands: config.CommandsConfig{Allowed: []string{"go vet"}}}, map[string]string{"test": "go test ./...", "build": "go build"})
	if err != nil || strings.Join(tc.Commands.Allowed, ",") != "go vet,go build,go test ./..." {
		t.Fatalf("allowlist: %v %v", tc.Commands.Allowed, err)
	}
	if _, err := withProjectCommands(config.ToolsConfig{}, map[string]string{"x": "a && b"}); err == nil || !strings.Contains(err.Error(), "project.commands.x") {
		t.Fatalf("shell project command: %v", err)
	}
	spec := sandboxSpec(config.SandboxConfig{Limits: config.LimitsConfig{CPUCores: 2, MemoryMB: 3, DiskMB: 4, MaxProcesses: 5, WallClockSecs: 6}}, "/w")
	if spec.WorkDir != "/w" || spec.Limits.CPUCores != 2 || spec.Limits.WallClock != 6*time.Second {
		t.Fatalf("spec: %+v", spec)
	}
}

func TestSandboxProviders(t *testing.T) {
	for _, name := range []string{"process", "container"} {
		p, err := sandboxProviders.New(name, sandbox.Options{})
		if err != nil || p.Name() != name {
			t.Errorf("%s: %v %v", name, p, err)
		}
	}
}

func TestSessionDiffAndClose(t *testing.T) {
	var stderr bytes.Buffer
	s := &session{sink: observability.NewLazyTrail(t.TempDir(), nil, core.PrivacyStandard), input: runtime.Input{Workspace: core.Workspace{Root: t.TempDir()}}, cleanup: func(context.Context, bool) error { return errors.New("boom") }}
	if d := s.diff(); !strings.HasPrefix(d, "no diff available") {
		t.Fatalf("non-git diff: %q", d)
	}
	if tr := s.trail(core.NewRunID()); tr != "no events recorded" {
		t.Fatalf("trail: %q", tr)
	}
	s.close(&stderr)
	if !strings.Contains(stderr.String(), "workspace cleanup: boom") {
		t.Fatalf("cleanup warning missing: %q", stderr.String())
	}
	if _, err := gitexec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := copyFixture(t)
	for _, argv := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "init"}} {
		cmd := gitexec.Command("git", argv...) //nolint:gosec // test constants
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", argv, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s = &session{input: runtime.Input{Workspace: core.Workspace{Root: root, Kind: core.WorkspacePrimary}}}
	if d := s.diff(); !strings.Contains(d, "changed files:") || !strings.Contains(d, "new.txt") {
		t.Fatalf("git diff: %q", d)
	}
}

func TestLoadTaskGraph(t *testing.T) {
	p := filepath.Join(t.TempDir(), "g.json")
	body := `{"nodes":[{"id":"api","title":"API"},{"id":"ui","title":"UI","deps":["api"]}]}`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	g, err := loadTaskGraph(p)
	if err != nil {
		t.Fatal(err)
	}
	waves, err := g.Waves()
	if err != nil || len(waves) != 2 {
		t.Fatalf("waves %v %v", waves, err)
	}
	if _, err := loadTaskGraph(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("missing")
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte(`{"nodes":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadTaskGraph(bad); err == nil {
		t.Fatal("empty graph must fail validate")
	}
}

func TestRunGraphFlagRejectsMissingFile(t *testing.T) {
	root := copyFixture(t)
	home := t.TempDir()
	trustProject(t, home, root)
	code, _, errOut := execRunHome(t, home, "", "run", "--project", root, "--graph", filepath.Join(root, "nope.json"), "--script", filepath.Join(scripts, "add-farewell.json"), "--no-tui", "--yes", "parent")
	if code == exitOK || (!strings.Contains(errOut, "nope.json") && !strings.Contains(errOut, "graph")) {
		t.Fatalf("code %d stderr %s", code, errOut)
	}
}
