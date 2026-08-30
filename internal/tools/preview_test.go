package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ataidesorg/ink/internal/core"
)

func pin(args string) core.ToolInput {
	return core.ToolInput{Call: core.NewToolCallID(), Arguments: json.RawMessage(args)}
}

func TestPreviews(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tc := core.ToolContext{WorkspaceRoot: root}

	p := Preview(&WriteFile{}, pin(`{"path":"a.txt","content":"one\nthree\n"}`), tc)
	if !strings.Contains(p, "a.txt") || !strings.Contains(p, "- two") || !strings.Contains(p, "+ three") {
		t.Fatalf("write diff preview: %q", p)
	}
	if p := Preview(&WriteFile{}, pin(`{"path":"new.txt","content":"hi"}`), tc); !strings.Contains(p, "+ hi") {
		t.Fatalf("fresh file preview: %q", p)
	}
	if p := Preview(&ApplyPatch{}, pin(`{"patch":"--- a\n+++ b\n@@ -1 +1 @@\n-x\n+y\n"}`), tc); !strings.Contains(p, "+y") {
		t.Fatalf("patch preview: %q", p)
	}
	if p := Preview(&RunCommand{}, pin(`{"argv":["go","test","./..."]}`), tc); p != "$ go test ./..." {
		t.Fatalf("command preview: %q", p)
	}
	// A tool without a preview yields "".
	if p := Preview(&ReadFile{}, pin(`{"path":"a.txt"}`), tc); p != "" {
		t.Fatalf("read_file preview: %q", p)
	}
	// The formatter wrapper forwards to the wrapped write tool.
	f := &formatted{inner: &WriteFile{}}
	if p := Preview(f, pin(`{"path":"a.txt","content":"one\nthree\n"}`), tc); !strings.Contains(p, "+ three") {
		t.Fatalf("formatted preview: %q", p)
	}
	long, _ := json.Marshal(map[string]string{"patch": strings.Repeat("line\n", 60)})
	if p := Preview(&ApplyPatch{}, pin(string(long)), tc); !strings.Contains(p, "more lines)") {
		t.Fatalf("clip: %q", p[len(p)-40:])
	}
}

func TestPreviewStripsControlBytes(t *testing.T) {
	tool := &RunCommand{}
	in := pin(`{"argv":["echo","\u001b[2Khidden"]}`)
	got := tool.Preview(in, core.ToolContext{})
	if strings.ContainsRune(got, 0x1b) {
		t.Fatalf("escape byte survived: %q", got)
	}
	if !strings.Contains(got, "\uFFFD[2Khidden") {
		t.Fatalf("control byte not replaced: %q", got)
	}
}
