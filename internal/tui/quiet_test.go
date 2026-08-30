package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ataidesorg/ink/internal/core"
)

// TestSlashCommandOutputLeavesHome guards the bug where /help printed into a
// transcript the home surface was painting over: the command ran, the lines
// landed, and the frame showed the wordmark.
func TestSlashCommandOutputLeavesHome(t *testing.T) {
	m := NewChat(Options{Width: 80, NoColor: true}, nil)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	next, _ = typeText(next.(ChatModel), "/home").Update(enter())
	m = next.(ChatModel)
	if !m.homeOpen {
		t.Fatal("/home did not open the home surface")
	}

	next, _ = typeText(m, "/help").Update(enter())
	m = next.(ChatModel)
	if m.homeOpen {
		t.Fatal("/help left the home surface up")
	}
	if v := stripANSI(m.View()); !strings.Contains(v, "/doctor") {
		t.Fatalf("/help output is not on screen:\n%s", v)
	}

	// /home is the way back.
	next, _ = typeText(m, "/home").Update(enter())
	if !next.(ChatModel).homeOpen {
		t.Fatal("/home did not reopen the home surface")
	}
}

// TestCtrlJInsertsNewline: ctrl+j is the second newline key, next to alt+enter.
func TestCtrlJInsertsNewline(t *testing.T) {
	m := typeText(NewChat(Options{Width: 80, NoColor: true}, nil), "one")
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	m = next.(ChatModel)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("two")})
	if got := next.(ChatModel).ta.Value(); got != "one\ntwo" {
		t.Fatalf("ctrl+j draft = %q, want %q", got, "one\ntwo")
	}
}

func TestChatEventNoise(t *testing.T) {
	advisory := core.Event{Data: core.Warning{Message: "cost of model x is unknown", Advisory: true}}
	failure := core.Event{Data: core.Warning{Message: "spend ledger append failed"}}
	allow := core.Event{Data: core.PolicyDecided{Effect: core.EffectAllow, Tool: "read_file", Rule: "default"}}
	deny := core.Event{Data: core.PolicyDecided{Effect: core.EffectDeny, Tool: "bash", Rule: "deny-net"}}

	for _, tc := range []struct {
		name          string
		e             core.Event
		verbose, hide bool
		want          bool
	}{
		{"allow is noise", allow, false, false, false},
		{"deny is not", deny, false, false, true},
		{"allow shows in verbose", allow, true, false, true},
		{"advisory kept by default", advisory, false, false, true},
		{"advisory hidden on request", advisory, false, true, false},
		{"hiding beats verbose", advisory, true, true, false},
		{"real warning always shows", failure, false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := chatEventLines(tc.e, tc.verbose, true, tc.hide) != nil
			if got != tc.want {
				t.Fatalf("rendered = %v, want %v", got, tc.want)
			}
		})
	}
}
