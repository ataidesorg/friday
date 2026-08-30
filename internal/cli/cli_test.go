package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ataidesorg/ink/internal/config"
	"github.com/ataidesorg/ink/internal/core"
	"github.com/ataidesorg/ink/internal/observability"
	"github.com/ataidesorg/ink/internal/redact"
)

func exec(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	environ := []string{"HOME=" + t.TempDir(), "PATH=/usr/bin"}
	code := Run(args, &stdout, &stderr, strings.NewReader(""), environ, func() (string, error) { return t.TempDir(), nil })
	return code, stdout.String(), stderr.String()
}

var (
	sample  = filepath.Join("..", "..", "test", "sample-project")
	invalid = filepath.Join("..", "..", "test", "invalid-project")
)

func TestVersionAndUsage(t *testing.T) {
	if code, out, _ := exec(t, "version"); code != 0 || !strings.HasPrefix(out, "ink dev (commit ") {
		t.Fatalf("version: %d %q", code, out)
	}
	if code, _, errOut := exec(t); code != exitUsage || !strings.Contains(errOut, "usage:") {
		t.Fatalf("no args: %d %q", code, errOut)
	}
	if code, out, _ := exec(t, "-h"); code != 0 || !strings.Contains(out, "usage:") {
		t.Fatalf("-h: %d", code)
	}
	if code, _, errOut := exec(t, "bogus"); code != exitUsage || !strings.Contains(errOut, `unknown command "bogus"`) {
		t.Fatalf("unknown: %d %q", code, errOut)
	}
	if code, _, _ := exec(t, "config"); code != exitUsage {
		t.Fatalf("config without subcommand: %d", code)
	}
	if code, _, _ := exec(t, "config", "frobnicate"); code != exitUsage {
		t.Fatalf("config unknown sub: %d", code)
	}
	if code, _, _ := exec(t, "config", "show", "--set", "novalue"); code != exitUsage {
		t.Fatalf("bad --set: %d", code)
	}
	if code, _, _ := exec(t, "config", "explain"); code != exitUsage {
		t.Fatalf("explain without key: %d", code)
	}
}

func TestConfigShowValidateExplain(t *testing.T) {
	code, out, errOut := exec(t, "config", "show", "--project", sample)
	if code != 0 || !strings.Contains(out, `name = "sample"`) || !strings.Contains(out, `instructions = ["AGENTS.md"]`) {
		t.Fatalf("show: %d %q %q", code, out, errOut)
	}
	if code, out, _ := exec(t, "config", "validate", "--project", sample); code != 0 || out != "ok\n" {
		t.Fatalf("validate sample: %d %q", code, out)
	}
	code, out, errOut = exec(t, "config", "validate", "--project", invalid)
	if code != exitError || out != "" || !strings.Contains(errOut, "evals.gate: must be one of") || !strings.Contains(errOut, "dropped sandbox.provider (untrusted)") {
		t.Fatalf("validate invalid: %d %q %q", code, out, errOut)
	}
	code, out, errOut = exec(t, "config", "show", "--project", invalid)
	if code != exitError || out != "" || !strings.Contains(errOut, "invalid configuration") {
		t.Fatalf("show invalid must not print: %d %q %q", code, out, errOut)
	}
	code, out, _ = exec(t, "config", "explain", "sandbox.provider", "--project", invalid)
	if code != 0 || !strings.Contains(out, `sandbox.provider = "process"`) || !strings.Contains(out, "[rejected: untrusted]") {
		t.Fatalf("explain rejected: %d %q", code, out)
	}
	code, out, _ = exec(t, "config", "explain", "--set", "evals.min_pass_rate=50", "evals.min_pass_rate", "--project", sample)
	if code != 0 || !strings.Contains(out, "cli → 50") {
		t.Fatalf("explain cli layer: %d %q", code, out)
	}
	if code, _, errOut := exec(t, "config", "explain", "nope.nope"); code != exitError || !strings.Contains(errOut, "unknown key") {
		t.Fatalf("explain unknown: %d %q", code, errOut)
	}
}

func TestSecretOverrideNeverEchoed(t *testing.T) {
	fake := "sk-" + strings.Repeat("b", 24)
	for _, sub := range []string{"show", "validate"} {
		code, out, errOut := exec(t, "config", sub, "--project", sample, "--set", "project.name="+fake)
		if code != exitError || out != "" || !strings.Contains(errOut, "project.name: secret literal in config") || strings.Contains(errOut, fake) {
			t.Fatalf("%s: %d %q %q", sub, code, out, errOut)
		}
	}
	code, out, errOut := exec(t, "config", "validate", "--project", sample, "--set", "telemetry.privacy="+fake)
	if code != exitError || out != "" || !strings.Contains(errOut, "telemetry.privacy: must be one of") || strings.Contains(errOut, fake) {
		t.Fatalf("validate enum: %d %q %q", code, out, errOut)
	}
	code, out, errOut = exec(t, "config", "explain", "project.name", "--project", sample, "--set", "project.name="+fake)
	if code != 0 || strings.Contains(out, fake) || !strings.Contains(out, "[REDACTED:openai_key]") || !strings.Contains(errOut, "configuration is invalid") {
		t.Fatalf("explain: %d %q %q", code, out, errOut)
	}
}

func TestRunUsage(t *testing.T) {
	if code, _, _ := exec(t, "run"); code != exitUsage {
		t.Fatalf("run without task: %d", code)
	}
	if code, _, _ := exec(t, "run", "--bogus"); code != exitUsage {
		t.Fatalf("run bad flag: %d", code)
	}
	if code, _, _ := exec(t, "run", "a", "b"); code != exitUsage {
		t.Fatalf("run two tasks: %d", code)
	}
}

func TestTrace(t *testing.T) {
	root := t.TempDir()
	run := core.NewRunID()
	sink, closer, err := observability.NewRedactingJSONL(observability.TrailPath(root, run), redact.New(), core.PrivacyStandard)
	if err != nil {
		t.Fatal(err)
	}
	e := core.NewEvent(core.NewTaskID(), run, 1, time.Now(), core.Warning{Message: "hello"})
	if err := sink.Emit(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	code, out, _ := exec(t, "trace", "--project", root, string(run))
	if code != 0 || !strings.Contains(out, "warning") || !strings.Contains(out, "hello") {
		t.Fatalf("trace: code %d out %q", code, out)
	}
	code, out, _ = exec(t, "trace", "--project", root, "--json", "--kind", "warning", string(run))
	var got core.Event
	if code != 0 || got.UnmarshalJSON([]byte(strings.TrimSpace(out))) != nil || got.ID != e.ID {
		t.Fatalf("trace --json: code %d out %q", code, out)
	}
	for _, tc := range []struct {
		args []string
		code int
		msg  string
	}{
		{[]string{"trace"}, 2, "usage"},
		{[]string{"trace", "--project", root, "not-an-id"}, 2, "run not found"},
		{[]string{"trace", "--project", root, string(core.NewRunID())}, 2, "run not found"},
		{[]string{"trace", "--project", root, "--kind", "bogus", string(run)}, 2, "unknown event kind"},
		{[]string{"trace", "--bogus"}, 2, "flag provided"},
	} {
		code, _, errOut := exec(t, tc.args...)
		if code != tc.code || !strings.Contains(errOut, tc.msg) {
			t.Errorf("%v: code %d stderr %q", tc.args, code, errOut)
		}
	}
}

// execEnv runs the CLI with extra environment entries (HOME is always a temp dir).
func execEnv(t *testing.T, extra []string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	environ := append([]string{"HOME=" + t.TempDir(), "PATH=/usr/bin"}, extra...)
	code := Run(args, &stdout, &stderr, strings.NewReader(""), environ, func() (string, error) { return t.TempDir(), nil })
	return code, stdout.String(), stderr.String()
}

func TestTrustCommand(t *testing.T) {
	state := t.TempDir()
	env := []string{"INK_STATE_DIR=" + state}
	proj := t.TempDir()
	cfgPath := filepath.Join(proj, ".ink", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte("[sandbox]\nprovider = \"container\"\n[project]\nname = \"p\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Untrusted: validate passes but warns, naming the key, the reason, and the remedy.
	code, out, errOut := execEnv(t, env, "config", "validate", "--project", proj)
	if code != 0 || out != "ok\n" || !strings.Contains(errOut, "warning: "+cfgPath+": dropped sandbox.provider (untrusted)") || !strings.Contains(errOut, "ink trust") {
		t.Fatalf("untrusted validate: %d %q %q", code, out, errOut)
	}
	if code, out, _ := execEnv(t, env, "config", "show", "--project", proj); code != 0 || !strings.Contains(out, `provider = "process"`) {
		t.Fatalf("untrusted show: %d %q", code, out)
	}
	// --list on an empty store.
	if code, out, _ := execEnv(t, env, "trust", "--list"); code != 0 || out != "" {
		t.Fatalf("empty list: %d %q", code, out)
	}
	// Trust the file explicitly: prints the gated keys and the hash prefix.
	code, out, errOut = execEnv(t, env, "trust", "--project", proj)
	if code != 0 || !strings.Contains(out, "trusted "+cfgPath) || !strings.Contains(out, "sandbox.provider") {
		t.Fatalf("trust: %d %q %q", code, out, errOut)
	}
	code, out, errOut = execEnv(t, env, "config", "show", "--project", proj)
	if code != 0 || !strings.Contains(out, `provider = "container"`) || strings.Contains(errOut, "warning") {
		t.Fatalf("trusted show: %d %q %q", code, out, errOut)
	}
	code, out, _ = execEnv(t, env, "trust", "--list")
	fields := strings.Fields(out)
	if code != 0 || len(fields) != 3 || fields[0] != "trusted" || len(fields[1]) != 8 || fields[2] != cfgPath {
		t.Fatalf("list: %d %q", code, out)
	}
	// Edit after trust → hash_changed warning; revoke → untrusted again.
	if err := os.WriteFile(cfgPath, []byte("[sandbox]\nprovider = \"container\"\n[project]\nname = \"edited\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, errOut := execEnv(t, env, "config", "validate", "--project", proj); !strings.Contains(errOut, "(hash_changed)") {
		t.Fatalf("hash_changed: %q", errOut)
	}
	if code, out, _ := execEnv(t, env, "trust", "--revoke", "--project", proj); code != 0 || !strings.Contains(out, "revoked "+cfgPath) {
		t.Fatalf("revoke: %d %q", code, out)
	}
	if code, out, _ := execEnv(t, env, "trust", "--list"); code != 0 || out != "" {
		t.Fatalf("list after revoke: %d %q", code, out)
	}
	// Explicit path argument, missing file, usage errors.
	if code, _, errOut := execEnv(t, env, "trust", filepath.Join(proj, "nope.toml")); code != exitError || !strings.Contains(errOut, "nope.toml") {
		t.Fatalf("missing file: %d %q", code, errOut)
	}
	if code, _, _ := execEnv(t, env, "trust", "--bogus"); code != exitUsage {
		t.Fatalf("bad flag: %d", code)
	}
	if code, _, _ := execEnv(t, env, "trust", "a", "b"); code != exitUsage {
		t.Fatalf("two paths: %d", code)
	}
	if code, _, _ := execEnv(t, env, "trust", "--list", "--revoke"); code != exitUsage {
		t.Fatalf("list with revoke: %d", code)
	}
	// A file that sets no gated key needs no trust; say so, record nothing.
	if err := os.WriteFile(cfgPath, []byte("[project]\nname = \"quiet\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := execEnv(t, env, "trust", "--project", proj); code != 0 || !strings.Contains(out, "no gated keys") {
		t.Fatalf("ungated trust: %d %q", code, out)
	}
	if code, out, _ := execEnv(t, env, "trust", "--list"); code != 0 || out != "" {
		t.Fatalf("ungated file recorded: %q", out)
	}
}

func TestInitCommand(t *testing.T) {
	proj := t.TempDir()
	code, out, errOut := exec(t, "init", "--project", proj)
	if code != 0 || errOut != "" {
		t.Fatalf("init: %d %q %q", code, out, errOut)
	}
	ignore, err := os.ReadFile(filepath.Join(proj, ".gitignore")) //nolint:gosec // test-owned temp dir
	if err != nil || !strings.Contains(string(ignore), ".ink/config.local.toml\n") || !strings.Contains(string(ignore), ".ink/local/\n") {
		t.Fatalf("gitignore: %q %v", ignore, err)
	}
	cfg, err := os.ReadFile(filepath.Join(proj, ".ink", "config.toml")) //nolint:gosec // test-owned temp dir
	if err != nil || !strings.Contains(string(cfg), "name = "+strconv.Quote(filepath.Base(proj))) {
		t.Fatalf("config: %q %v", cfg, err)
	}
	// Second run changes nothing: lines are added only when absent, files never overwritten.
	if err := os.WriteFile(filepath.Join(proj, ".ink", "config.toml"), []byte("[project]\nname = \"mine\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := exec(t, "init", "--project", proj); code != 0 {
		t.Fatalf("second init: %d", code)
	}
	again, _ := os.ReadFile(filepath.Join(proj, ".gitignore")) //nolint:gosec // test-owned temp dir
	if string(again) != string(ignore) {
		t.Fatalf("gitignore changed on second run:\n%s", again)
	}
	if cfg, _ := os.ReadFile(filepath.Join(proj, ".ink", "config.toml")); string(cfg) != "[project]\nname = \"mine\"\n" { //nolint:gosec // test-owned temp dir
		t.Fatalf("config overwritten: %q", cfg)
	}
	if code, _, _ := exec(t, "init", "extra"); code != exitUsage {
		t.Fatalf("init with positional: %d", code)
	}
}

// TestWarnDroppedRiskFlag: a repository file that sets an opt-in risk flag
// is warned about by path, with the never-mergeable remedy text.
func TestWarnDroppedRiskFlag(t *testing.T) {
	r := &config.Resolved{Provenance: config.Provenance{
		"providers.codex.accept_third_party_oauth_risk": {{
			Source:   config.Source{Layer: config.LayerProject, Path: ".ink/config.toml"},
			Value:    true,
			Rejected: true,
			Reason:   config.RejectAllowlist,
		}},
	}}
	var buf bytes.Buffer
	warnDropped(&buf, r)
	out := buf.String()
	for _, want := range []string{".ink/config.toml", "accept_third_party_oauth_risk", "repository files may never set them"} {
		if !strings.Contains(out, want) {
			t.Fatalf("warning %q must contain %q", out, want)
		}
	}
}
