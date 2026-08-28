package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ataidesorg/friday/internal/core"
)

func fileStub(prefix string) []string {
	var out []string
	for _, f := range []string{"docs/shot.png", "main.go"} {
		if strings.Contains(f, prefix) {
			out = append(out, f)
		}
	}
	return out
}

// An @-token offers file matches, tab or enter completes it in place, and a
// draft without an @-token shows nothing.
func TestAtTypeaheadFiltersAndCompletes(t *testing.T) {
	m := NewChat(Options{Width: 80, NoColor: true, CompleteFiles: fileStub}, nil)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(ChatModel)

	if menu := typeText(m, "look at main.go").atMatches(); menu != nil {
		t.Fatalf("bare word offered files: %v", menu)
	}
	m = typeText(m, "what is @sho")
	if menu := m.atMatches(); len(menu) != 1 || menu[0] != "docs/shot.png" {
		t.Fatalf("@sho got %v", menu)
	}
	if v := m.View(); !strings.Contains(v, "@docs/shot.png") {
		t.Fatalf("menu row missing from the frame:\n%s", v)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(ChatModel)
	if got := m.ta.Value(); got != "what is @docs/shot.png " {
		t.Fatalf("tab completed to %q", got)
	}
	if menu := m.atMatches(); menu != nil {
		t.Fatalf("menu survived completion: %v", menu)
	}
}

// Enter also completes (it never submits while the menu is up), and down
// moves the selection first.
func TestAtTypeaheadEnterCompletesSelection(t *testing.T) {
	m := typeText(NewChat(Options{Width: 80, CompleteFiles: fileStub}, nil), "@")
	if menu := m.atMatches(); len(menu) != 2 {
		t.Fatalf("bare @ got %v", menu)
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	next, _ = next.(ChatModel).Update(enter())
	if got := next.(ChatModel).ta.Value(); got != "@main.go " {
		t.Fatalf("enter completed to %q", got)
	}
}

// Esc dismisses the menu for the current token; editing the token brings it
// back.
func TestAtTypeaheadEscDismissesUntilTokenChanges(t *testing.T) {
	m := typeText(NewChat(Options{Width: 80, CompleteFiles: fileStub}, nil), "@ma")
	next, _ := m.Update(keyType(tea.KeyEsc))
	m = next.(ChatModel)
	if got := m.ta.Value(); got != "@ma" {
		t.Fatalf("esc under the menu cleared the draft to %q", got)
	}
	if menu := m.atMatches(); menu != nil {
		t.Fatalf("menu survived esc: %v", menu)
	}
	m = typeText(m, "@mai")
	if menu := m.atMatches(); len(menu) != 1 || menu[0] != "main.go" {
		t.Fatalf("edited token did not revive the menu: %v", menu)
	}
}

// No completer, an approval prompt, or an overlay hides the menu.
func TestAtTypeaheadGuards(t *testing.T) {
	if menu := typeText(NewChat(Options{Width: 80}, nil), "@ma").atMatches(); menu != nil {
		t.Fatalf("nil completer offered %v", menu)
	}
	m := typeText(NewChat(Options{Width: 80, CompleteFiles: fileStub}, nil), "@ma")
	m.pending = new(core.Approval)
	if menu := m.atMatches(); menu != nil {
		t.Fatalf("approval prompt left the menu up: %v", menu)
	}
}
