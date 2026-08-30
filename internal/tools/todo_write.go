package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/ataidesorg/ink/internal/core"
)

// Todo statuses the model may set. Unknown values are rejected.
const (
	TodoPending    = "pending"
	TodoInProgress = "in_progress"
	TodoCompleted  = "completed"
	TodoCancelled  = "cancelled"
)

// TodoItem is one task in the session list.
type TodoItem struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Status  string `json:"status"`
}

// TodoWrite is the model's scratch task list. It never touches the workspace;
// state lives in the session store the CLI binds.
type TodoWrite struct {
	Load func() []TodoItem
	Save func([]TodoItem) error
}

type todoWriteArgs struct {
	Merge *bool         `json:"merge"`
	Todos []todoWriteIn `json:"todos"`
}

type todoWriteIn struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Status  string `json:"status"`
}

// Spec describes the tool.
func (*TodoWrite) Spec() core.ToolSpec {
	return core.ToolSpec{
		Name:        "todo_write",
		Description: "Create and update the session task list. Use it to plan multi-step work, mark progress, and keep the current goal visible. merge=true (default) updates by id; send only id+status to flip a task without repeating its content. merge=false replaces the whole list.",
		Risk:        core.RiskReadOnly,
		InputSchema: schema("todo_write"),
	}
}

func (t *TodoWrite) bindTodos(load func() []TodoItem, save func([]TodoItem) error) core.Tool {
	return &TodoWrite{Load: load, Save: save}
}

// Invoke merges or replaces the session todo list.
func (t *TodoWrite) Invoke(_ context.Context, in core.ToolInput, _ core.ToolContext) (core.ToolOutput, error) {
	var a todoWriteArgs
	if err := decodeArgs("todo_write", in.Arguments, &a); err != nil {
		return core.ToolOutput{}, err
	}
	if t == nil || t.Save == nil {
		return core.ToolOutput{}, fmt.Errorf("%w: todo_write needs a session", core.ErrUnavailable)
	}
	merge := true
	if a.Merge != nil {
		merge = *a.Merge
	}
	cur := []TodoItem{}
	if t.Load != nil {
		cur = t.Load()
	}
	next, err := applyTodos(cur, a.Todos, merge)
	if err != nil {
		return core.ToolOutput{}, err
	}
	if err := t.Save(next); err != nil {
		return core.ToolOutput{}, err
	}
	return output(FormatTodos(next), next, core.Capability{Risk: core.RiskReadOnly, Scope: core.ResourceScope{Kind: core.ScopeAny}})
}

func applyTodos(cur []TodoItem, updates []todoWriteIn, merge bool) ([]TodoItem, error) {
	if !merge {
		out := make([]TodoItem, 0, len(updates))
		for _, u := range updates {
			item, err := newTodo(u, true)
			if err != nil {
				return nil, err
			}
			out = append(out, item)
		}
		return out, nil
	}
	byID := make(map[string]int, len(cur))
	out := append([]TodoItem(nil), cur...)
	for i, it := range out {
		byID[it.ID] = i
	}
	for _, u := range updates {
		id := strings.TrimSpace(u.ID)
		if id == "" {
			return nil, fmt.Errorf("%w: todo id is empty", core.ErrInvalidInput)
		}
		if i, ok := byID[id]; ok {
			if u.Content != "" {
				out[i].Content = u.Content
			}
			if u.Status != "" {
				st, err := normalizeTodoStatus(u.Status)
				if err != nil {
					return nil, err
				}
				out[i].Status = st
			}
			continue
		}
		item, err := newTodo(u, true)
		if err != nil {
			return nil, err
		}
		byID[item.ID] = len(out)
		out = append(out, item)
	}
	return out, nil
}

func newTodo(u todoWriteIn, requireContent bool) (TodoItem, error) {
	id := strings.TrimSpace(u.ID)
	if id == "" {
		return TodoItem{}, fmt.Errorf("%w: todo id is empty", core.ErrInvalidInput)
	}
	content := strings.TrimSpace(u.Content)
	if content == "" {
		if requireContent {
			return TodoItem{}, fmt.Errorf("%w: todo %q needs content", core.ErrInvalidInput, id)
		}
		content = id
	}
	st := u.Status
	if st == "" {
		st = TodoPending
	}
	st, err := normalizeTodoStatus(st)
	if err != nil {
		return TodoItem{}, err
	}
	return TodoItem{ID: id, Content: content, Status: st}, nil
}

func normalizeTodoStatus(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case TodoPending, TodoInProgress, TodoCompleted, TodoCancelled:
		return strings.ToLower(strings.TrimSpace(s)), nil
	}
	return "", fmt.Errorf("%w: todo status %q (want pending, in_progress, completed, cancelled)", core.ErrInvalidInput, s)
}

// FormatTodos is the model-facing list; the TUI pane parses the same marks.
func FormatTodos(items []TodoItem) string {
	if len(items) == 0 {
		return "todos: (none)"
	}
	var b strings.Builder
	b.WriteString("todos:")
	for _, it := range items {
		mark := " "
		switch it.Status {
		case TodoInProgress:
			mark = "~"
		case TodoCompleted:
			mark = "x"
		case TodoCancelled:
			mark = "-"
		}
		fmt.Fprintf(&b, "\n- [%s] %s %s", mark, it.ID, it.Content)
	}
	return b.String()
}
