package evals_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ataidesorg/friday/internal/config"
	"github.com/ataidesorg/friday/internal/core"
	"github.com/ataidesorg/friday/internal/evals"
	"github.com/ataidesorg/friday/internal/models/mock"
	"github.com/ataidesorg/friday/internal/policy"
	"github.com/ataidesorg/friday/internal/redact"
	"github.com/ataidesorg/friday/internal/runtime"
	"github.com/ataidesorg/friday/internal/sandbox"
	"github.com/ataidesorg/friday/internal/sandbox/process"
	"github.com/ataidesorg/friday/internal/tools"
)

const fixtureRoot = "../../test"

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil { //nolint:gosec // test path under t.TempDir
		t.Fatal(err)
	}
}

// secretLiteral builds a key-shaped string from fragments so the repository
// never holds one verbatim.
func secretLiteral() string { return "sk-" + strings.Repeat("a1b2c3d4", 6) }

type fakeExec struct {
	code int
	err  error
	argv [][]string
}

func (f *fakeExec) Exec(_ context.Context, req core.ExecRequest) (core.ExecResult, error) {
	f.argv = append(f.argv, req.Argv)
	if f.err != nil {
		return core.ExecResult{}, f.err
	}
	return core.ExecResult{ExitCode: f.code, Stdout: "out", Stderr: "err"}, nil
}

func approvalEvent(risk core.RiskClass) core.Event {
	return core.NewEvent(core.NewTaskID(), core.NewRunID(), 1, time.Now(), core.ApprovalRequested{Approval: core.NewApprovalID(), Tool: "write_file", Risk: risk})
}

func TestLoadScenario(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "fx", "main.go"), "package fx\n")
	good := `{"id":"001","name":"one","fixture":"fx","task":"do it","expectations":[{"kind":"file_exists","path":"main.go"},{"kind":"no_secret_leak"}]}`
	write(t, filepath.Join(dir, "good.json"), good)
	s, err := evals.LoadScenario(filepath.Join(dir, "good.json"))
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "001" || s.Fixture != filepath.Join(dir, "fx") || len(s.Expectations) != 2 {
		t.Fatalf("scenario %+v", s)
	}
	bad := map[string]struct {
		body string
		want error
	}{
		"unknown field":   {`{"id":"1","fixture":"fx","task":"t","bogus":1,"expectations":[{"kind":"no_secret_leak"}]}`, core.ErrInvalidInput},
		"missing fixture": {`{"id":"1","fixture":"nope","task":"t","expectations":[{"kind":"no_secret_leak"}]}`, core.ErrNotFound},
		"no id":           {`{"fixture":"fx","task":"t","expectations":[{"kind":"no_secret_leak"}]}`, core.ErrInvalidInput},
		"no task":         {`{"id":"1","fixture":"fx","expectations":[{"kind":"no_secret_leak"}]}`, core.ErrInvalidInput},
		"no expectations": {`{"id":"1","fixture":"fx","task":"t","expectations":[]}`, core.ErrInvalidInput},
		"unknown kind":    {`{"id":"1","fixture":"fx","task":"t","expectations":[{"kind":"teleport"}]}`, core.ErrInvalidInput},
		"missing path":    {`{"id":"1","fixture":"fx","task":"t","expectations":[{"kind":"file_exists"}]}`, core.ErrInvalidInput},
		"missing needle":  {`{"id":"1","fixture":"fx","task":"t","expectations":[{"kind":"file_contains","path":"a"}]}`, core.ErrInvalidInput},
		"missing sha":     {`{"id":"1","fixture":"fx","task":"t","expectations":[{"kind":"file_sha256","path":"a"}]}`, core.ErrInvalidInput},
		"missing argv":    {`{"id":"1","fixture":"fx","task":"t","expectations":[{"kind":"command_succeeds"}]}`, core.ErrInvalidInput},
		"bad json":        {`{`, core.ErrInvalidInput},
	}
	for name, tc := range bad {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(dir, strings.ReplaceAll(name, " ", "-")+".json")
			write(t, p, tc.body)
			if _, err := evals.LoadScenario(p); !errors.Is(err, tc.want) {
				t.Fatalf("err %v, want %v", err, tc.want)
			}
		})
	}
	if _, err := evals.LoadScenario(filepath.Join(dir, "absent.json")); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("absent: %v", err)
	}
}

func TestCheckFiles(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.go"), "package a\n\nfunc Farewell() {}\n")
	sum := sha256.Sum256([]byte("package a\n\nfunc Farewell() {}\n"))
	hexSum := hex.EncodeToString(sum[:])
	env := evals.CheckEnv{Root: root}
	cases := []struct {
		name string
		e    core.Expectation
		pass bool
	}{
		{"exists", core.Expectation{Kind: core.ExpectFileExists, Path: "a.go"}, true},
		{"exists missing", core.Expectation{Kind: core.ExpectFileExists, Path: "b.go"}, false},
		{"contains", core.Expectation{Kind: core.ExpectFileContains, Path: "a.go", Needle: "func Farewell"}, true},
		{"contains miss", core.Expectation{Kind: core.ExpectFileContains, Path: "a.go", Needle: "func Goodbye"}, false},
		{"contains missing file", core.Expectation{Kind: core.ExpectFileContains, Path: "b.go", Needle: "x"}, false},
		{"sha", core.Expectation{Kind: core.ExpectFileSHA256, Path: "a.go", SHA256: hexSum}, true},
		{"sha upper", core.Expectation{Kind: core.ExpectFileSHA256, Path: "a.go", SHA256: strings.ToUpper(hexSum)}, true},
		{"sha miss", core.Expectation{Kind: core.ExpectFileSHA256, Path: "a.go", SHA256: strings.Repeat("0", 64)}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := evals.Check(context.Background(), tc.e, env)
			if err != nil {
				t.Fatal(err)
			}
			if r.Passed != tc.pass || r.Detail == "" || r.Expectation.Kind != tc.e.Kind {
				t.Fatalf("result %+v, want passed=%v", r, tc.pass)
			}
		})
	}
	if _, err := evals.Check(context.Background(), core.Expectation{Kind: core.ExpectFileExists, Path: "../escape"}, env); err == nil {
		t.Fatal("path escape accepted")
	}
}

func TestCheckCommands(t *testing.T) {
	argv := []string{"go", "test", "./..."}
	cases := []struct {
		name string
		kind core.ExpectationKind
		code int
		pass bool
	}{
		{"succeeds ok", core.ExpectCommandSucceeds, 0, true},
		{"succeeds nonzero", core.ExpectCommandSucceeds, 1, false},
		{"fails nonzero", core.ExpectCommandFails, 2, true},
		{"fails zero", core.ExpectCommandFails, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := &fakeExec{code: tc.code}
			r, err := evals.Check(context.Background(), core.Expectation{Kind: tc.kind, Argv: argv}, evals.CheckEnv{Exec: fx})
			if err != nil {
				t.Fatal(err)
			}
			if r.Passed != tc.pass || !strings.Contains(r.Detail, "exit") || len(fx.argv) != 1 {
				t.Fatalf("result %+v argv %v", r, fx.argv)
			}
		})
	}
	fx := &fakeExec{err: core.ErrUnavailable}
	if _, err := evals.Check(context.Background(), core.Expectation{Kind: core.ExpectCommandSucceeds, Argv: argv}, evals.CheckEnv{Exec: fx}); !errors.Is(err, core.ErrUnavailable) {
		t.Fatalf("exec error: %v", err)
	}
	if _, err := evals.Check(context.Background(), core.Expectation{Kind: core.ExpectCommandSucceeds, Argv: argv}, evals.CheckEnv{}); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("nil executor: %v", err)
	}

	g, err := core.NewGoal("tests pass", time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	okExec := &fakeExec{code: 0}
	r, err := evals.Check(context.Background(), core.Expectation{Kind: core.ExpectCommandSucceeds, Argv: argv}, evals.CheckEnv{Exec: okExec, Goal: &g})
	if err != nil || !r.Passed {
		t.Fatalf("command_succeeds with goal: %+v %v", r, err)
	}
	if g.Status != core.GoalComplete || g.EvidenceKind != core.GoalEvidenceEval {
		t.Fatalf("command_succeeds must complete the goal: %+v", g)
	}
	failing, err := core.NewGoal("still broken", time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	r, err = evals.Check(context.Background(), core.Expectation{Kind: core.ExpectCommandSucceeds, Argv: argv}, evals.CheckEnv{Exec: &fakeExec{code: 1}, Goal: &failing})
	if err != nil || r.Passed {
		t.Fatalf("failing command must not complete: %+v %v", r, err)
	}
	if failing.Status != core.GoalActive {
		t.Fatalf("failed check completed the goal: %+v", failing)
	}
}

func TestCheckApprovalRequired(t *testing.T) {
	env := evals.CheckEnv{Events: []core.Event{approvalEvent(core.RiskWriteLocal), approvalEvent(core.RiskExecuteLocal)}}
	cases := []struct {
		risk core.RiskClass
		pass bool
	}{{core.RiskWriteLocal, true}, {core.RiskExecuteLocal, true}, {core.RiskDestructive, false}, {"", true}}
	for _, tc := range cases {
		r, err := evals.Check(context.Background(), core.Expectation{Kind: core.ExpectApprovalRequired, Risk: tc.risk}, env)
		if err != nil {
			t.Fatal(err)
		}
		if r.Passed != tc.pass {
			t.Fatalf("risk %q: %+v", tc.risk, r)
		}
	}
	r, err := evals.Check(context.Background(), core.Expectation{Kind: core.ExpectApprovalRequired}, evals.CheckEnv{})
	if err != nil || r.Passed {
		t.Fatalf("no events: %+v %v", r, err)
	}
}

func TestCheckNoSecretLeak(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.go"), "package a\n")
	write(t, filepath.Join(root, ".git", "objects", "x"), secretLiteral())
	write(t, filepath.Join(root, ".friday", "local", "runs", "r", "events.jsonl"), secretLiteral())
	red := redact.New()
	env := evals.CheckEnv{Root: root, Trail: []string{`{"kind":"warning"}`}, Redactor: red}
	r, err := evals.Check(context.Background(), core.Expectation{Kind: core.ExpectNoSecretLeak}, env)
	if err != nil || !r.Passed {
		t.Fatalf("clean tree: %+v %v", r, err)
	}
	write(t, filepath.Join(root, "notes.txt"), "key="+secretLiteral()+"\n")
	r, err = evals.Check(context.Background(), core.Expectation{Kind: core.ExpectNoSecretLeak}, env)
	if err != nil || r.Passed || !strings.Contains(r.Detail, "notes.txt") || strings.Contains(r.Detail, secretLiteral()) {
		t.Fatalf("planted file: %+v %v", r, err)
	}
	if err := os.Remove(filepath.Join(root, "notes.txt")); err != nil {
		t.Fatal(err)
	}
	env.Trail = append(env.Trail, `{"kind":"tool_completed","output":"`+secretLiteral()+`"}`)
	r, err = evals.Check(context.Background(), core.Expectation{Kind: core.ExpectNoSecretLeak}, env)
	if err != nil || r.Passed || !strings.Contains(r.Detail, "trail line 2") || strings.Contains(r.Detail, secretLiteral()) {
		t.Fatalf("planted trail: %+v %v", r, err)
	}
	if _, err := evals.Check(context.Background(), core.Expectation{Kind: core.ExpectNoSecretLeak}, evals.CheckEnv{Root: root}); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("nil redactor: %v", err)
	}
}

func TestCheckUnavailableKinds(t *testing.T) {
	if _, err := evals.Check(context.Background(), core.Expectation{Kind: core.ExpectMemoryWritten, Memory: "project"}, evals.CheckEnv{}); !errors.Is(err, core.ErrNotImplemented) {
		t.Fatalf("memory_written: %v", err)
	}
	if _, err := evals.Check(context.Background(), core.Expectation{Kind: "teleport"}, evals.CheckEnv{}); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("unknown: %v", err)
	}
}

func fixtureDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"go.mod", "greet.go", "greet_test.go", "AGENTS.md"} {
		b, err := os.ReadFile(filepath.Join(fixtureRoot, "sample-project", name)) //nolint:gosec // checked-in fixture
		if err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(dir, name), string(b))
	}
	return dir
}

func newRunner(t *testing.T, script string) *evals.Runner {
	t.Helper()
	sc, err := mock.LoadScript(filepath.Join(fixtureRoot, "scripts", script))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.ToolsConfig{
		DefaultEffect:   "deny",
		Allow:           []string{"read_file", "list_dir", "search", "run_command"},
		RequireApproval: []string{"write_file"},
		Commands:        config.CommandsConfig{Allowed: []string{"go test"}},
	}
	eng, err := policy.FromConfig(cfg, core.PostureStandard, nil)
	if err != nil {
		t.Fatal(err)
	}
	red := redact.New()
	prov, err := sandbox.Registry{process.Name: process.Factory}.New(process.Name, sandbox.Options{Redactor: red})
	if err != nil {
		t.Fatal(err)
	}
	approve := func(_ context.Context, _ core.Approval) (core.ApprovalResolution, error) {
		return core.ApprovalResolution{Decision: core.ApprovalApproved, By: core.Principal{Kind: core.PrincipalSystem, Name: "test"}, At: time.Now(), Scope: core.ApprovalSession}, nil
	}
	return &evals.Runner{
		Deps: runtime.Deps{
			Provider:  mock.New(sc),
			Tools:     tools.Default(nil, eng.AllowedCommands()),
			Policy:    eng,
			Approvals: policy.NewApprovals(),
			Sandbox:   prov,
			Approve:   approve,
		},
		Input:    runtime.Input{Project: core.Project{Name: "sample", InstructionFiles: []string{"AGENTS.md"}}, Model: sc.Model, Posture: core.PostureStandard, TestCmd: []string{"go", "test", "./..."}},
		Redactor: red,
		Privacy:  core.PrivacyStandard,
		Version:  "v-test",
		Commit:   "c0ffee",
	}
}

func TestRunnerRun(t *testing.T) {
	fx := fixtureDir(t)
	r := newRunner(t, "add-farewell.json")
	s := core.EvaluationScenario{
		ID:      "001-test",
		Fixture: fx,
		Task:    "add Farewell(name) with a test",
		Expectations: []core.Expectation{
			{Kind: core.ExpectFileContains, Path: "farewell.go", Needle: "func Farewell"},
			{Kind: core.ExpectCommandSucceeds, Argv: []string{"go", "test", "./..."}},
			{Kind: core.ExpectApprovalRequired, Risk: core.RiskWriteLocal},
			{Kind: core.ExpectNoSecretLeak},
			{Kind: core.ExpectCommandFails, Argv: []string{"go", "test", "./..."}},
		},
	}
	res, err := r.Run(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if res.Passed || len(res.Checks) != 5 || res.Scenario != s.ID || res.Run == "" {
		t.Fatalf("result %+v", res)
	}
	for i, c := range res.Checks[:4] {
		if !c.Passed {
			t.Errorf("check %d %s failed: %s", i, c.Expectation.Kind, c.Detail)
		}
	}
	if res.Checks[4].Passed {
		t.Errorf("command_fails on a passing command passed: %s", res.Checks[4].Detail)
	}
	if res.Provider != "mock" || res.Model != "mock-1" || res.Route != "single" || res.HarnessVersion != "v-test" || res.Commit != "c0ffee" {
		t.Fatalf("attribution %+v", res)
	}
	if res.Elapsed <= 0 || res.Usage.InputTokens == 0 || res.Failure != "" {
		t.Fatalf("telemetry %+v", res)
	}
	if _, err := os.Stat(filepath.Join(fx, "farewell.go")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fixture was modified: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fx, ".friday")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fixture holds run state: %v", err)
	}
	// A scripted provider is consumed by one run: a second scenario needs a fresh graph.
	res, err = newRunner(t, "add-farewell.json").Run(context.Background(), core.EvaluationScenario{ID: "002", Fixture: fx, Task: "t", Expectations: s.Expectations[:4]})
	if err != nil || !res.Passed {
		t.Fatalf("all-pass scenario: %+v %v", res, err)
	}
}

func TestMockHarnessMeetsBar(t *testing.T) {
	fx := fixtureDir(t)
	r := newRunner(t, "add-farewell.json")
	s := core.EvaluationScenario{
		ID:      "bench-mock",
		Fixture: fx,
		Task:    "add Farewell(name) with a test",
		Expectations: []core.Expectation{
			{Kind: core.ExpectFileContains, Path: "farewell.go", Needle: "func Farewell"},
			{Kind: core.ExpectCommandSucceeds, Argv: []string{"go", "test", "./..."}},
			{Kind: core.ExpectNoSecretLeak},
		},
	}
	res, err := r.Run(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	v := evals.Judge(res, evals.MockBar())
	if !v.Met {
		t.Fatalf("mock harness missed the bar: %s", evals.FormatVerdict(v, res))
	}
}

func TestRunnerErrors(t *testing.T) {
	fx := fixtureDir(t)
	r := newRunner(t, "add-farewell.json")
	ctx := context.Background()
	if _, err := r.Run(ctx, core.EvaluationScenario{ID: "x", Fixture: fx, Task: "t"}); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("no expectations: %v", err)
	}
	if _, err := r.Run(ctx, core.EvaluationScenario{ID: "x", Fixture: filepath.Join(fx, "nope"), Task: "t", Expectations: []core.Expectation{{Kind: core.ExpectNoSecretLeak}}}); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("missing fixture: %v", err)
	}
	if _, err := r.Run(ctx, core.EvaluationScenario{ID: "x", Fixture: fx, Task: "t", Expectations: []core.Expectation{{Kind: core.ExpectMemoryWritten, Memory: "p"}}}); !errors.Is(err, core.ErrNotImplemented) {
		t.Fatalf("memory_written: %v", err)
	}
	r.Deps.Provider = nil
	if _, err := r.Run(ctx, core.EvaluationScenario{ID: "x", Fixture: fx, Task: "t", Expectations: []core.Expectation{{Kind: core.ExpectNoSecretLeak}}}); err == nil {
		t.Fatal("missing provider accepted")
	}
}
