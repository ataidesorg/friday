package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ataidesorg/ink/internal/core"
)

func TestCustomTool(t *testing.T) {
	_, tc := ws(t)
	ct := NewCustomTool("lint", "run the linter", "", nil, []string{"mylint", "--json"})
	if sp := ct.Spec(); sp.Risk != core.RiskExecuteLocal || string(sp.InputSchema) != `{"type":"object"}` {
		t.Fatalf("spec defaults: %+v", sp)
	}
	r, err := Default(nil, nil).With(ct)
	if err != nil {
		t.Fatal(err)
	}
	unbound, _ := r.Get("lint")
	if _, err := call(t, unbound, tc, `{"level":"strict"}`); !errors.Is(err, core.ErrUnavailable) {
		t.Fatalf("unbound: %v", err)
	}
	exec := &fakeExec{res: core.ExecResult{ExitCode: 0, Stdout: "clean\n"}}
	tool, _ := r.WithExecutor(exec).Get("lint")
	out, err := call(t, tool, tc, `{"level":"strict"}`)
	if err != nil {
		t.Fatal(err)
	}
	req := exec.calls[0]
	if req.Argv[0] != "mylint" || req.Argv[1] != "--json" || req.Stdin != `{"level":"strict"}` {
		t.Fatalf("exec request: %+v", req)
	}
	if !strings.HasPrefix(out.Content, "exit 0\nclean") {
		t.Fatalf("content: %q", out.Content)
	}
	c := out.CapabilitiesUsed[0]
	if c.Risk != core.RiskExecuteLocal || c.Scope.Kind != core.ScopeCommand || c.Scope.Argv[0] != "mylint" {
		t.Fatalf("capability: %+v", c)
	}
}

func TestFormatWriteFile(t *testing.T) {
	_, tc := ws(t)
	rules := []FormatRule{{Command: []string{"gofmt", "-w"}, Extensions: []string{".go"}}}
	exec := &fakeExec{res: core.ExecResult{ExitCode: 0}}
	r := WrapFormatters(Default(nil, nil), rules).WithExecutor(exec)
	tool, _ := r.Get("write_file")
	if sp := tool.Spec(); sp.Name != "write_file" || sp.Risk != core.RiskWriteLocal {
		t.Fatalf("wrapped spec: %+v", sp)
	}
	out, err := call(t, tool, tc, `{"path":"fresh.go","content":"package x\n"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(exec.calls) != 1 || strings.Join(exec.calls[0].Argv, " ") != "gofmt -w fresh.go" {
		t.Fatalf("formatter calls: %+v", exec.calls)
	}
	if strings.Contains(out.Content, "formatter warning") {
		t.Fatalf("clean run warned: %q", out.Content)
	}
	// A failing formatter warns and never blocks.
	exec.res = core.ExecResult{ExitCode: 1, Stderr: "syntax error"}
	out, err = call(t, tool, tc, `{"path":"bad.go","content":"package x\n"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Content, "formatter warning: bad.go exited 1: syntax error") {
		t.Fatalf("no warning: %q", out.Content)
	}
	// Non-matching extensions never invoke the formatter.
	before := len(exec.calls)
	if _, err := call(t, tool, tc, `{"path":"notes.txt","content":"hi"}`); err != nil {
		t.Fatal(err)
	}
	if len(exec.calls) != before {
		t.Fatalf("formatter ran for .txt: %+v", exec.calls)
	}
	// The wrapped tool still names the written path for policy.
	c := CapabilityOf(tool, core.ToolInput{Arguments: json.RawMessage(`{"path":"fresh.go","content":"x"}`)})
	if c.Scope.Kind != core.ScopePath || c.Scope.Path != "fresh.go" {
		t.Fatalf("capability lost by wrap: %+v", c)
	}
}

// stubPatcher stands in for apply_patch: it reports files_changed without
// touching the filesystem.
type stubPatcher struct{}

func (stubPatcher) Spec() core.ToolSpec {
	return core.ToolSpec{Name: "apply_patch", Risk: core.RiskWriteLocal}
}

func (stubPatcher) Invoke(context.Context, core.ToolInput, core.ToolContext) (core.ToolOutput, error) {
	return output("patched", struct {
		FilesChanged []string `json:"files_changed"`
	}{[]string{"a.go", "b.txt"}})
}

func TestFormatApplyPatchPaths(t *testing.T) {
	_, tc := ws(t)
	base, err := NewRegistry(stubPatcher{})
	if err != nil {
		t.Fatal(err)
	}
	exec := &fakeExec{res: core.ExecResult{ExitCode: 0}}
	tool, _ := WrapFormatters(base, []FormatRule{{Command: []string{"gofmt", "-w"}, Extensions: []string{".go"}}}).WithExecutor(exec).Get("apply_patch")
	if _, err := call(t, tool, tc, `{}`); err != nil {
		t.Fatal(err)
	}
	if len(exec.calls) != 1 || exec.calls[0].Argv[2] != "a.go" {
		t.Fatalf("want one format of a.go, got %+v", exec.calls)
	}
}
