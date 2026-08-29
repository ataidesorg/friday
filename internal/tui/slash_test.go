package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ataidesorg/friday/internal/core"
)

// A bare "/" offers every built-in plus custom commands; a prefix filters
// it, and arguments or overlays dismiss it.
func TestSlashTypeaheadFiltersAndCaps(t *testing.T) {
	m := NewChat(Options{
		Width: 80, NoColor: true,
		Commands: []CommandInfo{{Name: "deploy", Description: "ship it"}},
	}, nil)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(ChatModel)

	m = typeText(m, "/")
	menu := m.slashMatches()
	got := map[string]bool{}
	for _, e := range menu {
		got[e.name] = true
	}
	for _, want := range []string{"help", "fork", "export", "timestamps", "advisories", "always-approve", "deploy", "quit"} {
		if !got[want] {
			t.Fatalf("bare / missing %q (%d matches)", want, len(menu))
		}
	}
	if len(menu) < len(builtinSlash()) {
		t.Fatalf("bare / shows %d, want at least the %d built-ins", len(menu), len(builtinSlash()))
	}
	m = typeText(m, "/mod")
	if menu := m.slashMatches(); len(menu) != 1 || menu[0].name != "model" {
		t.Fatalf("filter /mod got %+v, want model only", menu)
	}
	m = typeText(m, "/verb")
	if menu := m.slashMatches(); len(menu) != 1 || menu[0].name != "verbose" {
		t.Fatalf("filter /verb got %+v, want verbose", menu)
	}
	if v := m.View(); !strings.Contains(v, "/verbose") {
		t.Fatalf("menu row missing from the frame:\n%s", v)
	}
	if menu := typeText(m, "/dep").slashMatches(); len(menu) != 1 || menu[0].name != "deploy" {
		t.Fatalf("custom command not offered: %+v", menu)
	}
	if menu := typeText(m, "/model fast").slashMatches(); menu != nil {
		t.Fatalf("draft with an argument still shows the menu: %+v", menu)
	}
}

// Down moves the selection, tab completes the draft to the selected command.
func TestSlashTypeaheadTabCompletes(t *testing.T) {
	m := typeText(NewChat(Options{Width: 80}, nil), "/cos") // cost
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	next, _ = next.(ChatModel).Update(tea.KeyMsg{Type: tea.KeyTab})
	if got := next.(ChatModel).ta.Value(); got != "/cost " {
		t.Fatalf("tab completed to %q, want %q", got, "/cost ")
	}
}

// Enter on a partial draft runs the selected command and clears the prompt.
func TestSlashTypeaheadEnterRuns(t *testing.T) {
	m := typeText(NewChat(Options{Width: 80}, nil), "/he")
	next, _ := m.Update(enter())
	cm := next.(ChatModel)
	if joined := strings.Join(cm.Lines, "\n"); !strings.Contains(joined, "/model") {
		t.Fatalf("partial /he did not run /help:\n%s", joined)
	}
	if got := cm.ta.Value(); got != "" {
		t.Fatalf("draft %q survived the dispatch", got)
	}
}

func TestSlashTypeaheadEscClears(t *testing.T) {
	m := typeText(NewChat(Options{Width: 80}, nil), "/mo")
	next, _ := m.Update(keyType(tea.KeyEsc))
	if got := next.(ChatModel).ta.Value(); got != "" {
		t.Fatalf("esc left draft %q", got)
	}
}

// The menu borrows its rows from the pane so the frame never overflows.
func TestSlashMenuTakesPaneRows(t *testing.T) {
	m := NewChat(Options{Width: 80}, nil)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(ChatModel)
	base := m.vp.Height
	m = typeText(m, "/verbose")
	want := base - m.slashMenuRows(m.slashMatches())
	if m.vp.Height != want {
		t.Fatalf("pane %d rows with slash menu, want %d", m.vp.Height, want)
	}
}

// The footer is the persistent hint bar: bindings shown, last frame row,
// clipped to the terminal width.
func TestFooterShowsBindings(t *testing.T) {
	m := NewChat(Options{Width: 120, NoColor: true}, nil)
	f := m.footerView()
	for _, want := range []string{"enter", "send", "commands", "palette", "ctrl+c", "quit"} {
		if !strings.Contains(f, want) {
			t.Fatalf("footer missing %q: %s", want, f)
		}
	}
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 20})
	lines := strings.Split(next.(ChatModel).View(), "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1] // home parks blank rows under the group
	}
	if last := lines[len(lines)-1]; !strings.Contains(last, "palette") {
		t.Fatalf("footer not the last frame row: %q", last)
	}
	narrow := NewChat(Options{Width: 24, NoColor: true}, nil)
	if got := narrow.footerView(); len([]rune(got)) > 24 {
		t.Fatalf("footer wider than the terminal: %d runes %q", len([]rune(got)), got)
	}
}

// While a turn runs with nothing streamed, the pane shows a live thinking
// line; the first delta replaces it.
func TestThinkingLineWhileRunning(t *testing.T) {
	m := NewChat(Options{Width: 80, NoColor: true}, nil)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m = next.(ChatModel)
	m.Running, m.reply = true, ""
	m = m.layout()
	if !strings.Contains(m.View(), "thinking…") {
		t.Fatal("no thinking line while a turn runs with no output yet")
	}
	m.reply = "words"
	m = m.layout()
	if strings.Contains(m.View(), "thinking…") {
		t.Fatal("thinking line must vanish once deltas stream")
	}
}

// /resume replays the previous transcript and restarts the totals.
func TestSlashResumeReplaysTranscript(t *testing.T) {
	m := NewChat(Options{Width: 80, Resume: func() (string, []HistoryTurn, error) {
		return "prev", []HistoryTurn{
			{Role: "user", Text: "old question"},
			{Role: "assistant", Text: "old answer"},
		}, nil
	}}, nil)
	m = slash(t, m, "/resume")
	joined := strings.Join(m.Lines, "\n")
	for _, want := range []string{"old question", "old answer", "resumed session prev (2 turns)"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("resume replay missing %q:\n%s", want, joined)
		}
	}
	if m.Usage != (core.Usage{}) {
		t.Fatalf("usage must restart at zero, got %+v", m.Usage)
	}
}

// /resume warns when unwired, refuses mid-turn, and surfaces callback errors.
func TestSlashResumeGuards(t *testing.T) {
	m := slash(t, NewChat(Options{Width: 80}, nil), "/resume")
	if joined := strings.Join(m.Lines, "\n"); !strings.Contains(joined, "resume is not available") {
		t.Fatalf("nil resume did not warn:\n%s", joined)
	}

	busy := NewChat(Options{Width: 80, Resume: func() (string, []HistoryTurn, error) {
		t.Fatal("resume must not fire mid-turn")
		return "", nil, nil
	}}, nil)
	busy.Running = true
	next, _ := busy.resumeSession()
	if joined := strings.Join(next.(ChatModel).Lines, "\n"); !strings.Contains(joined, "finish or cancel") {
		t.Fatalf("mid-turn resume not refused:\n%s", joined)
	}

	bad := NewChat(Options{Width: 80, Resume: func() (string, []HistoryTurn, error) {
		return "", nil, errors.New("no previous session to resume")
	}}, nil)
	bad = slash(t, bad, "/resume")
	if joined := strings.Join(bad.Lines, "\n"); !strings.Contains(joined, "/resume failed") {
		t.Fatalf("resume error not surfaced:\n%s", joined)
	}
}

func TestSlashResumeOpensSessionPicker(t *testing.T) {
	var got string
	m := NewChat(Options{
		Width: 80,
		Sessions: []SessionInfo{
			{ID: "aaa", Title: "fix auth", Detail: "4 turns"},
			{ID: "bbb", Title: "docs pass", Detail: "2 turns"},
		},
		ResumeByID: func(id string) (string, []HistoryTurn, error) {
			got = id
			return id, []HistoryTurn{{Role: "user", Text: "from " + id}}, nil
		},
		Resume: func() (string, []HistoryTurn, error) {
			t.Fatal("picker must not fall back to resumeLatest")
			return "", nil, nil
		},
	}, nil)
	m = slash(t, m, "/resume")
	if m.ov == nil || m.ov.kind != overlaySessions {
		t.Fatal("/resume with a session list must open the picker")
	}
	if !strings.Contains(m.ov.view(newChatStyles(false), 80, 8), "fix auth") {
		t.Fatalf("picker missing session titles:\n%s", m.ov.view(newChatStyles(false), 80, 8))
	}
	next, _ := m.Update(enter())
	m = next.(ChatModel)
	if got != "aaa" {
		t.Fatalf("ResumeByID got %q, want aaa", got)
	}
	if !strings.Contains(strings.Join(m.Lines, "\n"), "from aaa") {
		t.Fatalf("picked session not replayed:\n%s", strings.Join(m.Lines, "\n"))
	}
}

// The overlay renders as a framed box centered in the pane region: bare
// newlines above and below, a left indent, never trailing whitespace.
func TestOverlayModalCentered(t *testing.T) {
	o := overlay{kind: overlayModels, title: "Model", items: []overlayItem{
		{id: "a", title: "alpha"}, {id: "b", title: "beta"},
	}}
	view := o.view(newChatStyles(false), 60, 12)
	lines := strings.Split(view, "\n")
	if len(lines) != 12 {
		t.Fatalf("modal region %d rows, want 12:\n%s", len(lines), view)
	}
	if !strings.Contains(view, "╭") || !strings.Contains(view, "╰") {
		t.Fatalf("modal lost its frame:\n%s", view)
	}
	first := 0
	for first < len(lines) && lines[first] == "" {
		first++
	}
	if first == 0 || first >= len(lines) {
		t.Fatalf("no vertical padding above the box (first content row %d):\n%s", first, view)
	}
	if !strings.HasPrefix(lines[first], " ") {
		t.Fatalf("box not horizontally centered: %q", lines[first])
	}
	for i, l := range lines {
		if l != strings.TrimRight(l, " \t") {
			t.Fatalf("modal row %d ends in whitespace: %q", i, l)
		}
	}
}
