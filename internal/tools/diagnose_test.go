package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/ataidesorg/ink/internal/core"
)

type fakeDiagnoser struct {
	byPath map[string]string
	calls  []string
}

func (f *fakeDiagnoser) Diagnose(_ context.Context, path string) string {
	f.calls = append(f.calls, path)
	return f.byPath[path]
}

func TestDiagnoseWriteFile(t *testing.T) {
	root, tc := ws(t)
	d := &fakeDiagnoser{byPath: map[string]string{root + "/fresh.go": "error fresh.go:1:1 boom"}}
	r := WrapDiagnostics(Default(nil, nil), d)
	tool, _ := r.Get("write_file")
	if sp := tool.Spec(); sp.Name != "write_file" || sp.Risk != core.RiskWriteLocal {
		t.Fatalf("wrapped spec: %+v", sp)
	}
	out, err := call(t, tool, tc, `{"path":"fresh.go","content":"package x\n"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out.Content, "\nerror fresh.go:1:1 boom") {
		t.Fatalf("diagnostics missing: %q", out.Content)
	}
	// A quiet file appends nothing.
	out, err = call(t, tool, tc, `{"path":"clean.go","content":"package x\n"}`)
	if err != nil || strings.Contains(out.Content, "error") {
		t.Fatalf("quiet write: %q err %v", out.Content, err)
	}
	// A failed write never reaches the diagnoser.
	before := len(d.calls)
	if _, err := call(t, tool, tc, `{"path":"../escape.go","content":"x"}`); err == nil {
		t.Fatal("escape accepted")
	}
	if len(d.calls) != before {
		t.Fatalf("diagnoser ran on failure: %v", d.calls)
	}
}

func TestDiagnoseApplyPatchAndFilterSurvival(t *testing.T) {
	root, tc := ws(t)
	base, err := NewRegistry(stubPatcher{})
	if err != nil {
		t.Fatal(err)
	}
	d := &fakeDiagnoser{byPath: map[string]string{root + "/a.go": "warning a.go:2:1 unused"}}
	r := WrapDiagnostics(base, d).Filter([]string{"apply_patch"})
	tool, ok := r.Get("apply_patch")
	if !ok {
		t.Fatal("apply_patch filtered away")
	}
	out, err := call(t, tool, tc, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Content, "warning a.go:2:1 unused") {
		t.Fatalf("patch diagnostics missing: %q", out.Content)
	}
	if len(d.calls) != 2 { // a.go and b.txt both offered; only a.go had text
		t.Fatalf("calls: %v", d.calls)
	}
}

func TestWrapDiagnosticsNil(t *testing.T) {
	r := Default(nil, nil)
	if WrapDiagnostics(r, nil) != r {
		t.Fatal("nil diagnoser must be a no-op")
	}
	if WrapDiagnostics(nil, &fakeDiagnoser{}) != nil {
		t.Fatal("nil registry must stay nil")
	}
}
