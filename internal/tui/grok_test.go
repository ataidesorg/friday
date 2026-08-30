package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ataidesorg/ink/internal/core"
)

func TestChatStatusBuiltin(t *testing.T) {
	m := slash(t, NewChat(Options{
		Width: 80, NoColor: true, SessionID: "sess-9", Route: "fw/deepseek", Mode: "plan",
	}, nil), "/status")
	got := strings.Join(m.Lines, "\n")
	for _, want := range []string{"session", "sess-9", "fw/deepseek", "plan"} {
		if !strings.Contains(got, want) {
			t.Fatalf("/status missing %q:\n%s", want, got)
		}
	}
}

func TestChatCopyCommandAndYank(t *testing.T) {
	var got string
	m := NewChat(Options{
		Width: 80, NoColor: true,
		Copy:    func(s string) error { got = s; return nil },
		VimMode: true,
	}, nil)
	m.Lines = []string{tagReply + " hello", tagReply + " world"}
	m = slash(t, m, "/copy")
	if got != "hello\nworld" {
		t.Fatalf("/copy got %q", got)
	}
	got = ""
	m.promptFocus = false
	m.sel = 0
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if got != "hello\nworld" {
		t.Fatalf("vim y copied %q", got)
	}
	_ = next
}

func TestChatCopyNthAndExport(t *testing.T) {
	var got string
	m := NewChat(Options{
		Width: 80, NoColor: true,
		Copy: func(s string) error { got = s; return nil },
	}, nil)
	m.Lines = []string{
		tagUser + " q1", tagReply + " first",
		tagUser + " q2", tagReply + " second",
	}
	m = slash(t, m, "/copy 2")
	if got != "first" {
		t.Fatalf("/copy 2 = %q", got)
	}
	got = ""
	m = slash(t, m, "/export")
	if !strings.Contains(got, "q1") || !strings.Contains(got, "second") {
		t.Fatalf("/export = %q", got)
	}
}

func TestChatHistoryArrows(t *testing.T) {
	m := NewChat(Options{Width: 80, NoColor: true}, nil)
	m.hist = []string{"one", "two"}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = next.(ChatModel)
	if m.ta.Value() != "two" {
		t.Fatalf("up recalled %q, want two", m.ta.Value())
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = next.(ChatModel)
	if m.ta.Value() != "one" {
		t.Fatalf("up again recalled %q, want one", m.ta.Value())
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(ChatModel)
	if m.ta.Value() != "two" {
		t.Fatalf("down recalled %q, want two", m.ta.Value())
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(ChatModel)
	if m.ta.Value() != "" {
		t.Fatalf("down past newest should clear, got %q", m.ta.Value())
	}
}

func TestChatEscEscClearsDraft(t *testing.T) {
	m := NewChat(Options{Width: 80, NoColor: true}, nil)
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	m.ta.SetValue("draft")
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(ChatModel)
	if !m.escHint || m.ta.Value() != "draft" {
		t.Fatalf("first esc should hint, keep draft; hint=%v val=%q", m.escHint, m.ta.Value())
	}
	if !strings.Contains(m.footerView(), "esc again") {
		t.Fatalf("footer missing clear hint: %s", m.footerView())
	}
	now = now.Add(100 * time.Millisecond)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(ChatModel)
	if m.ta.Value() != "" || m.escHint {
		t.Fatalf("second esc should clear; val=%q hint=%v", m.ta.Value(), m.escHint)
	}
}

func TestChatQuestionMarkOpensPalette(t *testing.T) {
	m := NewChat(Options{Width: 80, NoColor: true}, nil)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	m = next.(ChatModel)
	if m.ov == nil || m.ov.kind != overlayPalette {
		t.Fatal("? on empty prompt must open the palette")
	}
}

func TestChatSpaceFocusesPrompt(t *testing.T) {
	m := NewChat(Options{Width: 80, NoColor: true}, nil)
	m.Lines = []string{tagReply + " hi"}
	m.promptFocus = false
	m.sel = 0
	m.ta.Blur()
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = next.(ChatModel)
	if !m.promptFocus {
		t.Fatal("space in scrollback must focus the prompt")
	}
	if m.ta.Value() != "" {
		t.Fatalf("space must not insert a space, got %q", m.ta.Value())
	}
}

func TestChatAlwaysApproveAutoResolves(t *testing.T) {
	m := NewChat(Options{Width: 80, NoColor: true}, nil)
	m = slash(t, m, "/always-approve")
	if !m.yolo {
		t.Fatal("/always-approve should turn yolo on")
	}
	reply := make(chan core.ApprovalResolution, 1)
	next, _ := m.Update(ApprovalMsg{
		A:     core.Approval{Request: core.CapabilityRequest{Tool: "write_file"}},
		Reply: reply,
	})
	m = next.(ChatModel)
	if m.pending != nil {
		t.Fatal("yolo must auto-resolve the approval")
	}
	select {
	case r := <-reply:
		if r.Decision != core.ApprovalApproved || r.Scope != core.ApprovalSession {
			t.Fatalf("resolution = %+v", r)
		}
	default:
		t.Fatal("no resolution sent")
	}
}

func TestChatVimModeRequiredForYank(t *testing.T) {
	var got string
	m := NewChat(Options{
		Width: 80, NoColor: true,
		Copy: func(s string) error { got = s; return nil },
	}, nil)
	m.Lines = []string{tagReply + " secret"}
	m.promptFocus = false
	m.sel = 0
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = next.(ChatModel)
	if got != "" {
		t.Fatalf("simple mode y must type, not yank; copied %q", got)
	}
	if !m.promptFocus || m.ta.Value() != "y" {
		t.Fatalf("simple mode y should focus and type; focus=%v val=%q", m.promptFocus, m.ta.Value())
	}
}

func TestChatDoctorAndPlan(t *testing.T) {
	m := slash(t, NewChat(Options{Width: 80, NoColor: true, Doctor: []string{"term xterm-256color"}}, nil), "/doctor")
	got := strings.Join(m.Lines, "\n")
	for _, want := range []string{"doctor", "term xterm-256color", "osc52"} {
		if !strings.Contains(got, want) {
			t.Fatalf("/doctor missing %q:\n%s", want, got)
		}
	}
	m = slash(t, NewChat(Options{Width: 80, NoColor: true, SetMode: func(string) error { return nil }}, nil), "/plan")
	if m.mode != "plan" {
		t.Fatalf("/plan mode = %q", m.mode)
	}
}

func TestChatRewindKeepsEarlierPrompts(t *testing.T) {
	var kept int
	m := NewChat(Options{
		Width: 80, NoColor: true,
		Rewind: func(n int) error { kept = n; return nil },
	}, nil)
	m.Lines = []string{
		tagUser + " one", tagReply + " a1",
		tagUser + " two", tagReply + " a2",
	}
	next, _ := m.command("/rewind")
	m = next.(ChatModel)
	if m.ov == nil || m.ov.kind != overlayRewind || len(m.ov.items) != 2 {
		t.Fatalf("rewind overlay = %+v", m.ov)
	}
	next, _ = m.overlayCommit(overlayRewind, "1")
	m = next.(ChatModel)
	if kept != 1 {
		t.Fatalf("Rewind keep = %d", kept)
	}
	joined := strings.Join(m.Lines, "\n")
	if !strings.Contains(joined, "one") || strings.Contains(joined, "[you] two") {
		t.Fatalf("rewind lines:\n%s", joined)
	}
}

func TestChatMultilineAltEnter(t *testing.T) {
	m := NewChat(Options{Width: 80, NoColor: true}, nil)
	m.ta.SetValue("hello")
	m.ta.CursorEnd()
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	m = next.(ChatModel)
	if !strings.Contains(m.ta.Value(), "\n") {
		t.Fatalf("alt+enter should insert a newline, got %q", m.ta.Value())
	}
}
