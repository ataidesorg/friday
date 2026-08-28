package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ataidesorg/friday/internal/runtime"
)

func TestChatDashboardOpenAttachDispatch(t *testing.T) {
	var attached string
	var created int
	var ran []string
	m := NewChat(Options{
		Width: 80, NoColor: true, SessionID: "live",
		ListAgents: func() []DashAgent {
			return []DashAgent{
				{ID: "live", Title: "live", Detail: "1 turns", State: "idle"},
				{ID: "other", Title: "other", Detail: "2 turns", Peek: "hello there", State: "idle"},
			}
		},
		CreateAgent: func() (string, error) {
			created++
			return "new-1", nil
		},
		AttachAgent: func(id string) (string, []HistoryTurn, error) {
			attached = id
			if id == "other" {
				return "other", []HistoryTurn{{Role: "user", Text: "hi"}, {Role: "assistant", Text: "hello there"}}, nil
			}
			return id, nil, nil
		},
		RunOn: func(_ context.Context, id, prompt string, _ runtime.Observer) (runtime.Result, error) {
			ran = append(ran, id+":"+prompt)
			return okResult("done"), nil
		},
	}, nil)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlBackslash})
	m = next.(ChatModel)
	if !m.dash {
		t.Fatal("ctrl+\\ should open the dashboard")
	}
	view := m.View()
	for _, want := range []string{"Dashboard", "live", "other", "IDLE", "Dispatch a new agent"} {
		if !strings.Contains(view, want) {
			t.Fatalf("dashboard missing %q:\n%s", want, view)
		}
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(ChatModel)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(ChatModel)
	if m.dash {
		t.Fatal("enter should attach and leave the dashboard")
	}
	if attached != "other" {
		t.Fatalf("attached %q", attached)
	}
	if !strings.Contains(strings.Join(m.Lines, "\n"), "hello there") {
		t.Fatalf("attach did not replay transcript:\n%s", strings.Join(m.Lines, "\n"))
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlBackslash})
	m = next.(ChatModel)
	m.dashDraft = "review the auth"
	next, cmd := m.dashDispatch("review the auth", false)
	m = next.(ChatModel)
	if created != 1 {
		t.Fatalf("created %d", created)
	}
	if !m.dash {
		t.Fatal("dispatch without attach should stay on the dashboard")
	}
	if cmd == nil {
		t.Fatal("dispatch should start a background turn")
	}
}

func TestChatTodosPane(t *testing.T) {
	m := NewChat(Options{
		Width: 80, NoColor: true,
		Todos: func() []TodoItem {
			return []TodoItem{{ID: "1", Content: "Ship dashboard", Status: "in_progress"}}
		},
	}, nil)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	m = next.(ChatModel)
	if !m.todosOpen {
		t.Fatal("ctrl+t should open todos")
	}
	if !strings.Contains(m.View(), "Ship dashboard") {
		t.Fatalf("todos pane missing item:\n%s", m.View())
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(ChatModel)
	if m.todosOpen {
		t.Fatal("esc should close todos")
	}
}

func TestChatEditPromptSeam(t *testing.T) {
	m := NewChat(Options{
		Width: 80, NoColor: true,
		EditPrompt: func(draft string) (string, error) {
			return draft + " edited", nil
		},
	}, nil)
	m.ta.SetValue("hello")
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlG})
	m = next.(ChatModel)
	if m.ta.Value() != "hello edited" {
		t.Fatalf("editor = %q", m.ta.Value())
	}
}
