package mock_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ataidesorg/friday/internal/core"
	"github.com/ataidesorg/friday/internal/models/mock"
)

func writeScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "script.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const twoTurns = `{
  "model": "mock-1",
  "turns": [
    {"content": "reading", "finish": "tool_calls", "usage": {"input_tokens": 10, "output_tokens": 5},
     "tool_calls": [{"id": "c1", "name": "read_file", "arguments": {"path": "greet.go"}}]},
    {"content": "done", "finish": "stop", "match": "greet"}
  ]
}`

func TestLoadScriptStrict(t *testing.T) {
	if _, err := mock.LoadScript(writeScript(t, `{"model":"m","turns":[{"content":"x","finish":"stop","bogus":1}]}`)); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("unknown field: got %v, want ErrInvalidInput", err)
	}
	if _, err := mock.LoadScript(writeScript(t, `{"model":"m","turns":[]}`)); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("empty turns: got %v", err)
	}
	if _, err := mock.LoadScript(writeScript(t, `{"model":"m","turns":[{"content":"x","finish":"maybe"}]}`)); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("bad finish: got %v", err)
	}
	if _, err := mock.LoadScript(writeScript(t, `{"model":"m","turns":[{"finish":"tool_calls"}]}`)); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("tool_calls finish without calls: got %v", err)
	}
	if _, err := mock.LoadScript(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("missing file: want error")
	}
}

func TestCompletePlaysTurnsInOrder(t *testing.T) {
	s, err := mock.LoadScript(writeScript(t, twoTurns))
	if err != nil {
		t.Fatal(err)
	}
	p := mock.New(s)
	d := p.Descriptor()
	if d.Kind != core.ProviderMock || d.Privacy != core.PrivacyLocal || !d.Capabilities.ToolCalling || d.ID != "mock" {
		t.Fatalf("descriptor: %+v", d)
	}
	ctx := context.Background()
	r1, err := p.Complete(ctx, core.CompletionRequest{Model: "mock-1"})
	if err != nil {
		t.Fatal(err)
	}
	if r1.Finish != core.FinishToolCalls || len(r1.ToolCalls) != 1 || r1.ToolCalls[0].Name != "read_file" || r1.ToolCalls[0].ID != "c1" {
		t.Fatalf("turn 1: %+v", r1)
	}
	if string(r1.ToolCalls[0].Arguments) != `{"path":"greet.go"}` {
		t.Fatalf("arguments not compacted: %s", r1.ToolCalls[0].Arguments)
	}
	if r1.Usage != (core.Usage{InputTokens: 10, OutputTokens: 5}) {
		t.Fatalf("usage: %+v", r1.Usage)
	}
	r2, err := p.Complete(ctx, core.CompletionRequest{Model: "mock-1", Messages: []core.Message{{Role: core.RoleTool, Content: "package greet"}}})
	if err != nil {
		t.Fatal(err)
	}
	if r2.Finish != core.FinishStop || r2.Content != "done" {
		t.Fatalf("turn 2: %+v", r2)
	}
	if _, err := p.Complete(ctx, core.CompletionRequest{Model: "mock-1"}); !errors.Is(err, mock.ErrScriptExhausted) {
		t.Fatalf("third call: got %v", err)
	}
}

func TestCompleteMatchAndCancel(t *testing.T) {
	s, err := mock.LoadScript(writeScript(t, twoTurns))
	if err != nil {
		t.Fatal(err)
	}
	p := mock.New(s)
	if _, err := p.Complete(context.Background(), core.CompletionRequest{Model: "mock-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Complete(context.Background(), core.CompletionRequest{Model: "mock-1", Messages: []core.Message{{Role: core.RoleTool, Content: "nothing here"}}}); !errors.Is(err, mock.ErrScriptMismatch) {
		t.Fatalf("match miss: got %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Complete(ctx, core.CompletionRequest{Model: "mock-1"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled ctx: got %v", err)
	}
	if _, err := p.Complete(context.Background(), core.CompletionRequest{Model: "other"}); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("wrong model: got %v", err)
	}
}

func TestFixtureScriptsLoad(t *testing.T) {
	for _, name := range []string{"add-farewell.json", "forbidden-rm.json"} {
		s, err := mock.LoadScript(filepath.Join("..", "..", "..", "test", "scripts", name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(s.Turns) < 3 {
			t.Fatalf("%s: only %d turns", name, len(s.Turns))
		}
		last := s.Turns[len(s.Turns)-1]
		if last.Finish != core.FinishStop || last.Content == "" {
			t.Fatalf("%s: last turn must be a stop with a summary", name)
		}
	}
	s, _ := mock.LoadScript(filepath.Join("..", "..", "..", "test", "scripts", "forbidden-rm.json"))
	found := false
	for _, turn := range s.Turns {
		for _, c := range turn.ToolCalls {
			var a struct{ Argv []string }
			_ = json.Unmarshal(c.Arguments, &a)
			if c.Name == "run_command" && len(a.Argv) > 0 && a.Argv[0] == "rm" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("forbidden-rm.json must contain an rm run_command turn")
	}
}

func TestCompleteConcurrentCallsConsumeEachTurnOnce(t *testing.T) {
	s, err := mock.LoadScript(writeScript(t, twoTurns))
	if err != nil {
		t.Fatal(err)
	}
	p := mock.New(s)
	results := make(chan error, 4)
	for range 4 {
		go func() {
			_, err := p.Complete(context.Background(), core.CompletionRequest{Messages: []core.Message{{Role: core.RoleUser, Content: "greet"}}})
			results <- err
		}()
	}
	ok, exhausted := 0, 0
	for range 4 {
		switch err := <-results; {
		case err == nil:
			ok++
		case errors.Is(err, mock.ErrScriptExhausted):
			exhausted++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 2 || exhausted != 2 {
		t.Fatalf("ok=%d exhausted=%d, want 2/2", ok, exhausted)
	}
}
