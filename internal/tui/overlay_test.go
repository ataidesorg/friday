package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func keyRunes(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }
func keyType(t tea.KeyType) tea.KeyMsg {
	return tea.KeyMsg{Type: t}
}

func TestFuzzyRank(t *testing.T) {
	cases := []struct {
		q, s  string
		match bool
	}{
		{"", "anything", true},
		{"mod", "Switch model", true},
		{"sm", "Switch model", true},
		{"xyz", "Switch model", false},
		{"MOD", "switch model", true},
	}
	for _, c := range cases {
		if _, ok := fuzzyRank(c.q, c.s); ok != c.match {
			t.Fatalf("fuzzyRank(%q, %q) match=%v want %v", c.q, c.s, ok, c.match)
		}
	}
	pre, _ := fuzzyRank("mo", "model picker")
	scattered, _ := fuzzyRank("mo", "make cost flow")
	if pre >= scattered {
		t.Fatalf("prefix rank %d must beat scattered %d", pre, scattered)
	}
}

func TestOverlayFilterCommitClose(t *testing.T) {
	o := overlay{kind: overlayPalette, title: "Commands", items: chatActions()}
	o, done := o.update(keyRunes("verbo"))
	if done.closed || done.commit != "" {
		t.Fatalf("typing must neither close nor commit: %+v", done)
	}
	m := o.matches()
	if len(m) != 1 || m[0].id != "verbose" {
		t.Fatalf("filter kept %+v, want just verbose", m)
	}
	if _, done = o.update(keyType(tea.KeyEnter)); done.commit != "verbose" {
		t.Fatalf("enter committed %q, want verbose", done.commit)
	}
	if _, done = o.update(keyType(tea.KeyEsc)); !done.closed {
		t.Fatal("esc must close")
	}
	if _, done = o.update(keyType(tea.KeyCtrlC)); !done.closed {
		t.Fatal("ctrl+c must close")
	}
	// Backspace widens the filter again.
	o, _ = o.update(keyType(tea.KeyBackspace))
	if o.query != "verb" {
		t.Fatalf("backspace left query %q", o.query)
	}
}

func TestOverlayCursorBounds(t *testing.T) {
	o := overlay{kind: overlayModels, items: []overlayItem{{id: "a"}, {id: "b"}}}
	o, _ = o.update(keyType(tea.KeyUp))
	if o.cursor != 0 {
		t.Fatalf("up at top moved cursor to %d", o.cursor)
	}
	o, _ = o.update(keyType(tea.KeyDown))
	o, _ = o.update(keyType(tea.KeyDown))
	if o.cursor != 1 {
		t.Fatalf("down past end left cursor %d, want 1", o.cursor)
	}
	if _, done := o.update(keyType(tea.KeyEnter)); done.commit != "b" {
		t.Fatalf("enter committed %q, want b", done.commit)
	}
}

func TestOverlayEnterWithNoMatchesCloses(t *testing.T) {
	o := overlay{kind: overlayPalette, items: chatActions(), query: "zzzz"}
	if _, done := o.update(keyType(tea.KeyEnter)); !done.closed {
		t.Fatal("enter on empty matches must close, not commit")
	}
}

func TestOverlayViewCleanAndSized(t *testing.T) {
	o := overlay{kind: overlayPalette, title: "Commands", items: chatActions(), query: "mo"}
	v := o.view(newChatStyles(false), 60, 10)
	rows := strings.Split(v, "\n")
	if len(rows) != 10 {
		t.Fatalf("view height %d, want 10", len(rows))
	}
	for i, r := range rows {
		if r != strings.TrimRight(r, " \t") {
			t.Fatalf("row %d ends in whitespace: %q", i, r)
		}
	}
	if !strings.Contains(v, "Modules") && !strings.Contains(v, "Switch Model") {
		t.Fatalf("filtered view lost the match:\n%s", v)
	}
}

func TestOverlayScrollFollowsCursor(t *testing.T) {
	items := make([]overlayItem, 20)
	for i := range items {
		items[i] = overlayItem{id: string(rune('a' + i)), title: strings.Repeat(string(rune('a'+i)), 3)}
	}
	o := overlay{kind: overlayModels, title: "T", items: items, cursor: 19}
	v := o.view(newChatStyles(false), 40, 8)
	if !strings.Contains(v, "ttt") {
		t.Fatalf("view window does not follow the cursor:\n%s", v)
	}
}

func TestPaletteCursorWraps(t *testing.T) {
	o := overlay{kind: overlayPalette, items: chatActions()}
	n := len(o.matches())
	o, _ = o.update(keyType(tea.KeyUp))
	if o.cursor != n-1 {
		t.Fatalf("up at top = %d, want last %d", o.cursor, n-1)
	}
	o, _ = o.update(keyType(tea.KeyDown))
	if o.cursor != 0 {
		t.Fatalf("down at bottom = %d, want 0", o.cursor)
	}
}

func TestPaletteGroupsAndToggleState(t *testing.T) {
	items := chatActions()
	items[0].state = "on"
	o := overlay{kind: overlayPalette, title: "Commands", items: items}
	v := o.view(newChatStyles(false), 72, 28)
	for _, want := range []string{"Commands", "search:", "Session", "Context", "Model & Input", "New Session", "Ctrl+N", "[x]"} {
		if !strings.Contains(v, want) {
			t.Fatalf("palette missing %q:\n%s", want, v)
		}
	}
	if !strings.Contains(v, "on") {
		t.Fatalf("toggle state missing:\n%s", v)
	}
	if !strings.Contains(v, "◆") {
		t.Fatalf("rows lost their markers:\n%s", v)
	}
}

func TestPrettyKey(t *testing.T) {
	if got := prettyKey("ctrl+n"); got != "Ctrl+N" {
		t.Fatalf("prettyKey ctrl+n = %q", got)
	}
	if got := prettyKey("/resume"); got != "/resume" {
		t.Fatalf("slash key rewritten: %q", got)
	}
}
