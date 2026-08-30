package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fake is an in-process language server over io.Pipe: answers initialize and
// publishes one error diagnostic for every didOpen/didChange it sees.
type fake struct {
	mu      sync.Mutex
	methods []string
	silent  bool // swallow didOpen/didChange without publishing
	dieOn   string
}

func (f *fake) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.methods...)
}

func (f *fake) serve(t *testing.T, in io.ReadCloser, out io.WriteCloser) {
	t.Helper()
	defer out.Close() //nolint:errcheck // test pipe teardown
	br := bufio.NewReader(in)
	send := func(msg any) {
		b, _ := json.Marshal(msg)
		fmt.Fprintf(out, "Content-Length: %d\r\n\r\n%s", len(b), b)
	}
	for {
		body, err := readFrame(br)
		if err != nil {
			return
		}
		var msg rpcMessage
		if json.Unmarshal(body, &msg) != nil {
			continue
		}
		f.mu.Lock()
		f.methods = append(f.methods, msg.Method)
		f.mu.Unlock()
		if msg.Method == f.dieOn {
			return
		}
		switch msg.Method {
		case "initialize":
			send(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(msg.ID), "result": map[string]any{"capabilities": map[string]any{}}})
		case "textDocument/didOpen", "textDocument/didChange":
			if f.silent {
				continue
			}
			var p struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			send(map[string]any{"jsonrpc": "2.0", "method": "textDocument/publishDiagnostics", "params": map[string]any{
				"uri": p.TextDocument.URI,
				"diagnostics": []map[string]any{{
					"range":    map[string]any{"start": map[string]any{"line": 1, "character": 2}},
					"severity": 1,
					"message":  "undefined: x",
				}},
			}})
		}
	}
}

func fakeManager(t *testing.T, root string, f *fake) *Manager {
	t.Helper()
	m := NewManager(root, []Server{{Name: "fake", Command: []string{"unused"}, Extensions: []string{".go"}}})
	m.initTimeout, m.diagTimeout = 2*time.Second, 2*time.Second
	m.start = func(Server) (*client, error) {
		clientRead, serverWrite := io.Pipe()
		serverRead, clientWrite := io.Pipe()
		go f.serve(t, serverRead, serverWrite)
		stop := func() { _ = clientWrite.Close(); _ = serverWrite.Close() }
		return newClient(clientWrite, clientRead, stop), nil
	}
	t.Cleanup(m.Close)
	return m
}

func writeGo(t *testing.T, root string) string {
	t.Helper()
	p := filepath.Join(root, "main.go")
	if err := os.WriteFile(p, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDiagnoseOpenThenChange(t *testing.T) {
	root := t.TempDir()
	f := &fake{}
	m := fakeManager(t, root, f)
	p := writeGo(t, root)

	got := m.Diagnose(context.Background(), p)
	if want := "error main.go:2:3 undefined: x"; got != want {
		t.Fatalf("first diagnose = %q, want %q", got, want)
	}
	if got := m.Diagnose(context.Background(), p); !strings.Contains(got, "undefined: x") {
		t.Fatalf("second diagnose = %q", got)
	}
	seen := f.seen()
	want := []string{"initialize", "initialized", "textDocument/didOpen", "textDocument/didChange"}
	if len(seen) != len(want) {
		t.Fatalf("methods %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("methods %v, want %v", seen, want)
		}
	}
}

func TestDiagnoseNoServerForExtension(t *testing.T) {
	root := t.TempDir()
	m := fakeManager(t, root, &fake{})
	p := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(p, []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := m.Diagnose(context.Background(), p); got != "" {
		t.Fatalf("txt diagnose = %q, want empty", got)
	}
}

func TestDiagnoseSilentServerIsNotBroken(t *testing.T) {
	root := t.TempDir()
	m := fakeManager(t, root, &fake{silent: true})
	m.diagTimeout = 50 * time.Millisecond
	p := writeGo(t, root)
	for i := 0; i < 2; i++ {
		if got := m.Diagnose(context.Background(), p); got != "" {
			t.Fatalf("silent diagnose %d = %q, want empty", i, got)
		}
	}
}

func TestDiagnoseCrashWarnsOnce(t *testing.T) {
	root := t.TempDir()
	m := fakeManager(t, root, &fake{dieOn: "initialize"})
	p := writeGo(t, root)
	got := m.Diagnose(context.Background(), p)
	if !strings.Contains(got, "lsp fake: diagnostics disabled") {
		t.Fatalf("crash diagnose = %q, want disabled warning", got)
	}
	if got := m.Diagnose(context.Background(), p); got != "" {
		t.Fatalf("post-crash diagnose = %q, want silence", got)
	}
}

func TestDiagnoseCrashAfterInitWarnsOnce(t *testing.T) {
	root := t.TempDir()
	m := fakeManager(t, root, &fake{dieOn: "textDocument/didOpen"})
	m.diagTimeout = 5 * time.Second // dead must win the race, not the timeout
	p := writeGo(t, root)
	got := m.Diagnose(context.Background(), p)
	if !strings.Contains(got, "diagnostics disabled") {
		t.Fatalf("crash diagnose = %q, want disabled warning", got)
	}
	if got := m.Diagnose(context.Background(), p); got != "" {
		t.Fatalf("post-crash diagnose = %q, want silence", got)
	}
}

func TestDiagnoseMissingBinaryWarnsOnce(t *testing.T) {
	root := t.TempDir()
	m := NewManager(root, []Server{{Name: "ghost", Command: []string{"/nonexistent/ink-lsp"}, Extensions: []string{".go"}}})
	t.Cleanup(m.Close)
	p := writeGo(t, root)
	got := m.Diagnose(context.Background(), p)
	if !strings.Contains(got, "lsp ghost: diagnostics disabled") {
		t.Fatalf("missing binary diagnose = %q", got)
	}
	if got := m.Diagnose(context.Background(), p); got != "" {
		t.Fatalf("second diagnose = %q, want silence", got)
	}
}

func TestRenderCapsAndSeverity(t *testing.T) {
	mk := func(sev, line int, msg string) diagnostic {
		var d diagnostic
		d.Severity, d.Range.Start.Line = sev, line
		d.Message = msg
		return d
	}
	diags := []diagnostic{mk(3, 0, "hint stays out")}
	for i := 0; i < maxDiagLines+2; i++ {
		diags = append(diags, mk(1, i, "boom"))
	}
	got := render("/r", "/r/a.go", diags)
	if strings.Contains(got, "hint") {
		t.Fatalf("hint leaked: %q", got)
	}
	if !strings.Contains(got, "…and 2 more") {
		t.Fatalf("cap missing: %q", got)
	}
	if lines := strings.Count(got, "\n") + 1; lines != maxDiagLines+1 {
		t.Fatalf("%d lines, want %d", lines, maxDiagLines+1)
	}
}

func TestNilManagerIsInert(t *testing.T) {
	var m *Manager
	if got := m.Diagnose(context.Background(), "/x/a.go"); got != "" {
		t.Fatalf("nil manager diagnose = %q", got)
	}
	m.Close()
}
