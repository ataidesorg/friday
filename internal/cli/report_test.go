package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var scenarios = filepath.Join("..", "..", "test", "scenarios")

// writeScenario points a scenario at a private fixture copy.
func writeScenario(t *testing.T, fixture, expectations string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s.json")
	body := `{"id":"t","fixture":"` + fixture + `","task":"add Farewell(name) with a test","expectations":[` + expectations + `]}`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEvalRunShippedScenario(t *testing.T) {
	home := t.TempDir()
	trustProject(t, home, sample)
	code, out, errOut := execRunHome(t, home, "", "eval", "run", filepath.Join(scenarios, "001-add-farewell.json"), "--script", filepath.Join(scripts, "add-farewell.json"))
	if code != exitOK {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	for _, want := range []string{"scenario 001-add-farewell: passed (3 checks", "pass file_contains farewell.go", "pass command_succeeds go test ./...", "pass no_secret_leak"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(sample, "farewell.go")); err == nil {
		t.Fatal("eval modified the shipped fixture")
	}
}

func TestEvalBenchShippedScenario(t *testing.T) {
	home := t.TempDir()
	trustProject(t, home, sample)
	code, out, errOut := execRunHome(t, home, "", "eval", "bench", filepath.Join(scenarios, "001-add-farewell.json"), "--script", filepath.Join(scripts, "add-farewell.json"))
	if code != exitOK {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	if !strings.Contains(out, "bench MET") || !strings.Contains(out, "tokens in=") {
		t.Fatalf("bench report:\n%s%s", out, errOut)
	}
}

func TestEvalRunFailedCheck(t *testing.T) {
	root := copyFixture(t)
	p := writeScenario(t, root, `{"kind":"file_contains","path":"farewell.go","needle":"func Goodbye"},{"kind":"command_fails","argv":["go","test","./..."]},{"kind":"file_exists","path":"farewell_test.go"}`)
	code, out, errOut := execRun(t, "", "eval", "run", p, "--script", filepath.Join(scripts, "add-farewell.json"))
	if code != exitError {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	for _, want := range []string{"scenario t: failed (3 checks", `FAIL file_contains farewell.go "func Goodbye" — farewell.go does not contain`, "FAIL command_fails go test ./... — go test ./... exit 0", "pass file_exists farewell_test.go"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
}

func TestEvalRunExitCodes(t *testing.T) {
	root := copyFixture(t)
	script := filepath.Join(scripts, "add-farewell.json")
	cases := []struct {
		name string
		args []string
		code int
		err  string
	}{
		{"no subcommand", []string{"eval"}, exitUsage, "usage: ink eval run"},
		{"wrong subcommand", []string{"eval", "list"}, exitUsage, "usage"},
		{"no scenario", []string{"eval", "run"}, exitUsage, "usage"},
		{"bad flag", []string{"eval", "run", "--bogus", "x.json"}, exitUsage, ""},
		{"missing scenario", []string{"eval", "run", filepath.Join(root, "nope.json"), "--script", script}, exitUsage, "not found"},
		{"invalid scenario", []string{"eval", "run", writeScenario(t, root, `{"kind":"teleport"}`), "--script", script}, exitUsage, "unknown kind"},
		{"no script", []string{"eval", "run", writeScenario(t, root, `{"kind":"no_secret_leak"}`)}, exitFailed, "no provider configured"},
		{"missing script", []string{"eval", "run", writeScenario(t, root, `{"kind":"no_secret_leak"}`), "--script", filepath.Join(root, "nope.json")}, exitFailed, ""},
		{"memory_written", []string{"eval", "run", writeScenario(t, root, `{"kind":"memory_written","memory":"project"}`), "--script", script}, exitNotImplemented, "memory_written expectation: not implemented"},
		{"invalid config", []string{"eval", "run", writeScenario(t, root, `{"kind":"no_secret_leak"}`), "--script", script, "--set", "evals.gate=loud"}, exitConfigInvalid, "configuration is invalid"},
		{"unavailable sandbox", []string{"eval", "run", writeScenario(t, root, `{"kind":"no_secret_leak"}`), "--script", script, "--set", "sandbox.provider=unavailable"}, exitNotImplemented, "unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errOut := execRun(t, "", tc.args...)
			if code != tc.code || !strings.Contains(errOut, tc.err) {
				t.Fatalf("exit %d (want %d)\n%s%s", code, tc.code, out, errOut)
			}
		})
	}
}
