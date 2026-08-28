package tools

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ataidesorg/friday/internal/core"
)

type memTodos struct {
	items []TodoItem
}

func (m *memTodos) load() []TodoItem { return append([]TodoItem(nil), m.items...) }
func (m *memTodos) save(items []TodoItem) error {
	m.items = append([]TodoItem(nil), items...)
	return nil
}

func TestTodoWriteMergeAndReplace(t *testing.T) {
	mem := &memTodos{items: []TodoItem{{ID: "1", Content: "Build it", Status: TodoInProgress}}}
	tool := &TodoWrite{Load: mem.load, Save: mem.save}
	_, ws := ws(t)

	out, err := call(t, tool, ws, `{"todos":[{"id":"1","status":"completed"},{"id":"2","content":"Ship it"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(mem.items) != 2 || mem.items[0].Status != TodoCompleted || mem.items[0].Content != "Build it" {
		t.Fatalf("merge = %+v", mem.items)
	}
	if mem.items[1].ID != "2" || mem.items[1].Status != TodoPending {
		t.Fatalf("new item = %+v", mem.items[1])
	}
	for _, want := range []string{"todos:", "[x] 1 Build it", "[ ] 2 Ship it"} {
		if !strings.Contains(out.Content, want) {
			t.Fatalf("content missing %q: %q", want, out.Content)
		}
	}

	_, err = call(t, tool, ws, `{"merge":false,"todos":[{"id":"a","content":"only"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(mem.items) != 1 || mem.items[0].ID != "a" {
		t.Fatalf("replace = %+v", mem.items)
	}
}

func TestTodoWriteRejects(t *testing.T) {
	tool := &TodoWrite{Load: func() []TodoItem { return nil }, Save: func([]TodoItem) error { return nil }}
	_, ws := ws(t)
	if _, err := call(t, tool, ws, `{"todos":[{"id":"x","status":"nope"}]}`); err == nil || !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("bad status: %v", err)
	}
	if _, err := call(t, tool, ws, `{"todos":[{"id":"x"}]}`); err == nil {
		t.Fatal("new item without content must fail")
	}
	bare := &TodoWrite{}
	if _, err := call(t, bare, ws, `{"todos":[]}`); err == nil || !errors.Is(err, core.ErrUnavailable) {
		t.Fatalf("unbound: %v", err)
	}
}

func TestTodoWriteWithBinder(t *testing.T) {
	mem := &memTodos{}
	r := Default(nil, nil)
	bound := r.WithTodos(mem.load, mem.save)
	t1, ok := bound.Get("todo_write")
	if !ok {
		t.Fatal("missing todo_write")
	}
	if t2, _ := r.Get("todo_write"); t2 == t1 {
		t.Fatal("WithTodos must copy")
	}
	_, ws := ws(t)
	if _, err := t1.Invoke(t.Context(), core.ToolInput{Arguments: json.RawMessage(`{"todos":[{"id":"1","content":"x"}]}`)}, ws); err != nil {
		t.Fatal(err)
	}
	if len(mem.items) != 1 {
		t.Fatalf("saved %+v", mem.items)
	}
}
