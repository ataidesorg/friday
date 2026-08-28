package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ataidesorg/friday/internal/core"
)

type fakeExec struct {
	calls []core.ExecRequest
	res   core.ExecResult
	err   error
}

func (f *fakeExec) Exec(_ context.Context, req core.ExecRequest) (core.ExecResult, error) {
	f.calls = append(f.calls, req)
	return f.res, f.err
}

func ws(t *testing.T) (string, core.ToolContext) {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"greet.go":      "package sample\n\nfunc Greet(name string) string {\n\treturn \"Hello, \" + name\n}\n",
		"README.md":     "# sample\nhello world\nHELLO again\n",
		"sub/notes.txt": "alpha\nbeta\n",
		"bin.dat":       "abc\x00def",
	}
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root, core.ToolContext{Run: core.NewRunID(), WorkspaceRoot: root}
}

func call(t *testing.T, tool core.Tool, tc core.ToolContext, args string) (core.ToolOutput, error) {
	t.Helper()
	return tool.Invoke(context.Background(), core.ToolInput{Call: core.NewToolCallID(), Arguments: json.RawMessage(args)}, tc)
}

func structured(t *testing.T, out core.ToolOutput, v any) {
	t.Helper()
	if err := json.Unmarshal(out.Structured, v); err != nil {
		t.Fatalf("structured output: %v (%s)", err, out.Structured)
	}
}

func TestRegistry(t *testing.T) {
	r := Default(nil, [][]string{{"go", "test"}})
	specs := r.Specs()
	want := []string{"apply_patch", "ask_user_question", "list_dir", "read_file", "run_command", "search", "todo_write", "write_file"}
	if len(specs) != len(want) {
		t.Fatalf("specs: %d", len(specs))
	}
	for i, s := range specs {
		if s.Name != want[i] {
			t.Errorf("spec %d = %q, want %q", i, s.Name, want[i])
		}
		var sch struct {
			Required []string `json:"required"`
		}
		if err := json.Unmarshal(s.InputSchema, &sch); err != nil || len(sch.Required) == 0 {
			t.Errorf("%s: schema must be valid JSON naming required fields: %v", s.Name, err)
		}
		if s.Risk == "" || s.Description == "" {
			t.Errorf("%s: risk and description required", s.Name)
		}
	}
	if _, ok := r.Get("read_file"); !ok {
		t.Fatal("read_file missing")
	}
	if _, ok := r.Get("shell"); ok {
		t.Fatal("shell must not exist")
	}
	if _, err := NewRegistry(&ReadFile{}, &ReadFile{}); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("duplicate: %v", err)
	}
	var nilReg *Registry
	if nilReg.Specs() != nil || nilReg.WithExecutor(nil) != nil {
		t.Fatal("nil registry must be inert")
	}
	if _, ok := nilReg.Get("x"); ok {
		t.Fatal("nil registry Get")
	}
}

func TestReadFile(t *testing.T) {
	_, tc := ws(t)
	tool := &ReadFile{}
	out, err := call(t, tool, tc, `{"path":"greet.go","max_bytes":7}`)
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Content   string `json:"content"`
		Truncated bool   `json:"truncated"`
	}
	structured(t, out, &res)
	if res.Content != "package" || !res.Truncated || out.Content != "package" {
		t.Fatalf("truncate: %+v", res)
	}
	if len(out.CapabilitiesUsed) != 1 || out.CapabilitiesUsed[0].Scope.Path != "greet.go" || out.CapabilitiesUsed[0].Risk != core.RiskReadOnly {
		t.Fatalf("capabilities: %+v", out.CapabilitiesUsed)
	}
	if _, err := call(t, tool, tc, `{"path":"nope.go"}`); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if _, err := call(t, tool, tc, `{"path":"sub"}`); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("dir: %v", err)
	}
	if _, err := call(t, tool, tc, `{"path":"../x"}`); !errors.Is(err, ErrOutsideWorkspace) {
		t.Fatalf("escape: %v", err)
	}
	if _, err := call(t, tool, tc, `{"path":"greet.go","bogus":1}`); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("unknown field: %v", err)
	}
	if _, err := call(t, tool, tc, `{}`); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("missing required: %v", err)
	}
	if _, err := call(t, tool, tc, `{"path":"greet.go","max_bytes":99999999}`); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("max_bytes cap: %v", err)
	}
	if _, err := call(t, tool, core.ToolContext{}, `{"path":"greet.go"}`); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("no root: %v", err)
	}
	if _, err := call(t, tool, tc, `[1]`); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("non-object: %v", err)
	}
	if c := CapabilityOf(tool, core.ToolInput{Arguments: json.RawMessage(`{"path":"sub/../greet.go"}`)}); c.Scope.Path != "greet.go" || c.Scope.Kind != core.ScopePath {
		t.Fatalf("CapabilityOf: %+v", c)
	}
}

func TestListDir(t *testing.T) {
	root, tc := ws(t)
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	tool := &ListDir{}
	out, err := call(t, tool, tc, `{"path":"."}`)
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Entries []struct{ Name, Kind string } `json:"entries"`
	}
	structured(t, out, &res)
	names := map[string]string{}
	for _, e := range res.Entries {
		names[e.Name] = e.Kind
	}
	if names["sub"] != "dir" || names["greet.go"] != "file" || names[".git"] != "" || names["sub/notes.txt"] != "" {
		t.Fatalf("depth 1: %v", names)
	}
	out, err = call(t, tool, tc, `{"path":".","depth":2}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Content, "file\tsub/notes.txt") {
		t.Fatalf("depth 2: %s", out.Content)
	}
	if _, err := call(t, tool, tc, `{"path":"missing"}`); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if _, err := call(t, tool, tc, `{"path":".","depth":99}`); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("depth cap: %v", err)
	}
}

func TestSearch(t *testing.T) {
	_, tc := ws(t)
	tool := &Search{}
	out, err := call(t, tool, tc, `{"pattern":"(?i)hello","max_results":2}`)
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Matches []struct {
			Path string
			Line int
			Text string
		} `json:"matches"`
	}
	structured(t, out, &res)
	if len(res.Matches) != 2 {
		t.Fatalf("cap: %+v", res.Matches)
	}
	out, err = call(t, tool, tc, `{"pattern":"beta","path":"sub"}`)
	if err != nil {
		t.Fatal(err)
	}
	if out.Content != "sub/notes.txt:2: beta\n" {
		t.Fatalf("scoped: %q", out.Content)
	}
	out, err = call(t, tool, tc, `{"pattern":"abc"}`)
	if err != nil || out.Content != "" {
		t.Fatalf("binary skipped: %q %v", out.Content, err)
	}
	if _, err := call(t, tool, tc, `{"pattern":"("}`); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("bad regexp: %v", err)
	}
	if _, err := call(t, tool, tc, `{"pattern":"x","max_results":5000}`); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("max_results cap: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tool.Invoke(ctx, core.ToolInput{Arguments: json.RawMessage(`{"pattern":"x"}`)}, tc); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestWriteFile(t *testing.T) {
	root, tc := ws(t)
	tool := &WriteFile{}
	out, err := call(t, tool, tc, `{"path":"new/dir/farewell.go","content":"package sample\n"}`)
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		BytesWritten int `json:"bytes_written"`
	}
	structured(t, out, &res)
	if res.BytesWritten != 15 {
		t.Fatalf("bytes: %d", res.BytesWritten)
	}
	if b := readFile(t, root, "new/dir/farewell.go"); b != "package sample\n" {
		t.Fatalf("content: %q", b)
	}
	if _, err := call(t, tool, tc, `{"path":"greet.go","content":"x","create_only":true}`); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("create_only: %v", err)
	}
	if _, err := call(t, tool, tc, `{"path":"greet.go","content":"overwritten\n"}`); err != nil {
		t.Fatal(err)
	}
	if b := readFile(t, root, "greet.go"); b != "overwritten\n" {
		t.Fatalf("overwrite: %q", b)
	}
	if _, err := call(t, tool, tc, `{"path":"../escape.txt","content":"x"}`); !errors.Is(err, ErrOutsideWorkspace) {
		t.Fatalf("escape: %v", err)
	}
	if _, err := call(t, tool, tc, `{"path":"greet.go"}`); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("missing content: %v", err)
	}
	if _, err := call(t, tool, tc, `{"path":"sub","content":"x"}`); err == nil {
		t.Fatal("writing over a directory must fail")
	}
	if c := tool.Capability(core.ToolInput{Arguments: json.RawMessage(`{"path":"a.go","content":""}`)}); c.Risk != core.RiskWriteLocal || c.Scope.Path != "a.go" {
		t.Fatalf("capability: %+v", c)
	}
}

const goodPatch = `diff --git a/greet.go b/greet.go
index 1..2 100644
--- a/greet.go
+++ b/greet.go
@@ -1,5 +1,5 @@
 package sample
 
 func Greet(name string) string {
-	return "Hello, " + name
+	return "Hello, " + name + "!"
 }
--- /dev/null
+++ b/farewell.go
@@ -0,0 +1,3 @@
+package sample
+
+func Farewell() string { return "bye" }
`

func TestApplyPatch(t *testing.T) {
	root, tc := ws(t)
	tool := &ApplyPatch{}
	out, err := call(t, tool, tc, mustJSON(t, map[string]string{"patch": goodPatch}))
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		FilesChanged []string `json:"files_changed"`
	}
	structured(t, out, &res)
	if strings.Join(res.FilesChanged, ",") != "greet.go,farewell.go" {
		t.Fatalf("files_changed: %v", res.FilesChanged)
	}
	b := readFile(t, root, "greet.go")
	if !strings.Contains(b, `name + "!"`) {
		t.Fatalf("greet.go not patched: %s", b)
	}
	b = readFile(t, root, "farewell.go")
	if b != "package sample\n\nfunc Farewell() string { return \"bye\" }\n" {
		t.Fatalf("farewell.go: %q", b)
	}
	if out.CapabilitiesUsed[0].Scope.Path != "greet.go,farewell.go" {
		t.Fatalf("scope: %+v", out.CapabilitiesUsed)
	}
	del := "--- a/sub/notes.txt\n+++ /dev/null\n@@ -1,2 +0,0 @@\n-alpha\n-beta\n"
	if _, err := call(t, tool, tc, mustJSON(t, map[string]string{"patch": del})); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "sub/notes.txt")); !os.IsNotExist(err) {
		t.Fatal("delete did not remove the file")
	}
	noNL := "--- a/README.md\n+++ b/README.md\n@@ -3 +3 @@\n-HELLO again\n+HELLO there\n\\ No newline at end of file\n"
	if _, err := call(t, tool, tc, mustJSON(t, map[string]string{"patch": noNL})); err != nil {
		t.Fatal(err)
	}
	if b := readFile(t, root, "README.md"); b != "# sample\nhello world\nHELLO there" {
		t.Fatalf("no-newline: %q", b)
	}
}

func TestApplyPatchAtomicAndConfined(t *testing.T) {
	root, tc := ws(t)
	tool := &ApplyPatch{}
	before := readFile(t, root, "greet.go")
	twoHunks := "--- a/greet.go\n+++ b/greet.go\n@@ -1 +1 @@\n-package sample\n+package changed\n--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-not the real line\n+x\n"
	if _, err := call(t, tool, tc, mustJSON(t, map[string]string{"patch": twoHunks})); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("later hunk failure: %v", err)
	}
	if after := readFile(t, root, "greet.go"); after != before {
		t.Fatal("first file written despite later hunk failure")
	}
	escape := "--- a/../outside.txt\n+++ b/../outside.txt\n@@ -0,0 +1 @@\n+x\n"
	if _, err := call(t, tool, tc, mustJSON(t, map[string]string{"patch": escape})); !errors.Is(err, ErrOutsideWorkspace) {
		t.Fatalf("escape: %v", err)
	}
	bad := map[string]string{
		"binary":     "--- a/x\n+++ b/x\nGIT binary patch\n",
		"empty":      "",
		"no headers": "just text\n",
		"no plus":    "--- a/greet.go\n@@ -1 +1 @@\n",
		"short hunk": "--- a/greet.go\n+++ b/greet.go\n@@ -1,3 +1,3 @@\n package sample\n",
		"bad range":  "--- a/greet.go\n+++ b/greet.go\n@@ -x +1 @@\n",
		"no hunks":   "--- a/greet.go\n+++ b/greet.go\n",
		"rename":     "--- a/greet.go\n+++ b/other.go\n@@ -1 +1 @@\n-package sample\n+package x\n",
		"exists":     "--- /dev/null\n+++ b/greet.go\n@@ -0,0 +1 @@\n+x\n",
		"missing":    "--- a/nope.go\n+++ b/nope.go\n@@ -1 +1 @@\n-a\n+b\n",
		"junk line":  "--- a/greet.go\n+++ b/greet.go\n@@ -1 +1 @@\n?what\n",
		"both null":  "--- /dev/null\n+++ /dev/null\n@@ -0,0 +1 @@\n+x\n",
	}
	for name, p := range bad {
		if _, err := call(t, tool, tc, mustJSON(t, map[string]string{"patch": p})); !errors.Is(err, core.ErrInvalidInput) && !errors.Is(err, core.ErrConflict) && !errors.Is(err, core.ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if c := tool.Capability(core.ToolInput{Arguments: json.RawMessage(`{"patch":"garbage"}`)}); c.Scope.Kind != core.ScopePath {
		t.Fatalf("garbage capability: %+v", c)
	}
}

func TestRunCommand(t *testing.T) {
	_, tc := ws(t)
	exec := &fakeExec{res: core.ExecResult{ExitCode: 0, Stdout: "ok\n", Stderr: "warn"}}
	r := Default(nil, [][]string{{"go", "test"}, {"go", "build"}})
	unbound, _ := r.Get("run_command")
	if _, err := call(t, unbound, tc, `{"argv":["go","test","./..."]}`); !errors.Is(err, core.ErrUnavailable) {
		t.Fatalf("unbound: %v", err)
	}
	tool, _ := r.WithExecutor(exec).Get("run_command")
	out, err := call(t, tool, tc, `{"argv":["go","test","./..."],"dir":"sub","timeout_secs":5}`)
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		ExitCode int    `json:"exit_code"`
		Stdout   string `json:"stdout"`
	}
	structured(t, out, &res)
	if res.ExitCode != 0 || res.Stdout != "ok\n" || !strings.Contains(out.Content, "stderr:\nwarn") {
		t.Fatalf("result: %+v %q", res, out.Content)
	}
	if len(exec.calls) != 1 || exec.calls[0].Dir != "sub" || exec.calls[0].Timeout.Seconds() != 5 {
		t.Fatalf("exec request: %+v", exec.calls)
	}
	if out.CapabilitiesUsed[0].Scope.Kind != core.ScopeCommand || out.CapabilitiesUsed[0].Scope.Argv[0] != "go" {
		t.Fatalf("capability: %+v", out.CapabilitiesUsed)
	}
	if _, err := call(t, tool, tc, `{"argv":["rm","-rf","."]}`); !errors.Is(err, core.ErrPolicyDenied) {
		t.Fatalf("rm: %v", err)
	}
	if _, err := call(t, tool, tc, `{"argv":["go"]}`); !errors.Is(err, core.ErrPolicyDenied) {
		t.Fatalf("shorter than prefix: %v", err)
	}
	if _, err := call(t, tool, tc, `{"argv":["go","test","./..."],"dir":"../x"}`); !errors.Is(err, ErrOutsideWorkspace) {
		t.Fatalf("dir escape: %v", err)
	}
	if _, err := call(t, tool, tc, `{"argv":["go","test"],"timeout_secs":9999}`); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("timeout cap: %v", err)
	}
	if _, err := call(t, tool, tc, `{"argv":[]}`); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("empty argv: %v", err)
	}
	if len(exec.calls) != 1 {
		t.Fatalf("denied commands must never reach the executor: %d calls", len(exec.calls))
	}
	exec.err = errors.New("boom")
	if _, err := call(t, tool, tc, `{"argv":["go","build"]}`); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("exec error: %v", err)
	}
	exec.err = nil
	exec.res = core.ExecResult{ExitCode: 1, TimedOut: true}
	out, _ = call(t, tool, tc, `{"argv":["go","build"]}`)
	if !strings.HasPrefix(out.Content, "exit 1 (timed out)") {
		t.Fatalf("timed out text: %q", out.Content)
	}
}

func TestSchemaMissing(t *testing.T) {
	var v struct{}
	if err := decodeArgs("no_such_tool", json.RawMessage(`{}`), &v); !errors.Is(err, core.ErrUnavailable) {
		t.Fatalf("missing schema: %v", err)
	}
	if err := decodeArgs("read_file", nil, &struct {
		Path string `json:"path"`
	}{}); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("empty args: %v", err)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

type anyTool struct{}

func (anyTool) Spec() core.ToolSpec { return core.ToolSpec{Name: "any", Risk: core.RiskPrivileged} }
func (anyTool) Invoke(context.Context, core.ToolInput, core.ToolContext) (core.ToolOutput, error) {
	return core.ToolOutput{}, core.ErrNotImplemented
}

func TestCapabilityOfFallbackAndScopes(t *testing.T) {
	c := CapabilityOf(anyTool{}, core.ToolInput{})
	if c.Risk != core.RiskPrivileged || c.Scope.Kind != core.ScopeAny {
		t.Fatalf("fallback: %+v", c)
	}
	if c := pathScope(core.RiskReadOnly, "/abs"); c.Scope.Path != "/abs" {
		t.Fatalf("unclean path kept verbatim for the trail: %+v", c)
	}
	if got := displayPath("/nonexistent-root", "/elsewhere/x"); got != "../elsewhere/x" && got != "/elsewhere/x" {
		t.Fatalf("displayPath fallback: %q", got)
	}
	if _, err := output("", make(chan int)); err == nil {
		t.Fatal("unmarshalable output must error")
	}
	root, tc := ws(t)
	if err := os.Symlink("greet.go", filepath.Join(root, "link.go")); err != nil {
		t.Fatal(err)
	}
	out, err := call(t, &ListDir{}, tc, `{"path":"."}`)
	if err != nil || !strings.Contains(out.Content, "symlink\tlink.go") {
		t.Fatalf("symlink kind: %v %q", err, out.Content)
	}
	if _, err := call(t, &WriteFile{}, tc, `{"path":"sub/notes.txt/child","content":"x"}`); err == nil {
		t.Fatal("file used as directory must fail")
	}
	if _, err := call(t, &ApplyPatch{}, tc, mustJSON(t, map[string]string{"patch": "--- /dev/null\n+++ b/sub/notes.txt/child\n@@ -0,0 +1 @@\n+x\n"})); err == nil {
		t.Fatal("patch under a file must fail")
	}
}

func readFile(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel))) //nolint:gosec // test fixture under t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRegistryFilter(t *testing.T) {
	r := Default(nil, nil)
	f := r.Filter([]string{"read_file", "nope"})
	if _, ok := f.Get("read_file"); !ok {
		t.Fatal("filter must keep a named tool")
	}
	if _, ok := f.Get("write_file"); ok {
		t.Fatal("filter must drop an unnamed tool")
	}
	if got := len(f.Specs()); got != 1 {
		t.Fatalf("unknown names must be skipped, got %d tools", got)
	}
	if r.Filter(nil) != r {
		t.Fatal("empty filter must keep the full registry")
	}
	if _, ok := r.Get("write_file"); !ok {
		t.Fatal("filter must not mutate the receiver")
	}
}
