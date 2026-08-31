package tui

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ataidesorg/ink/internal/core"
	"github.com/ataidesorg/ink/internal/runtime"
)

func enter() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyEnter} }

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }

func typeText(m ChatModel, s string) ChatModel {
	// textarea holds its buffer behind a shared pointer, so a model reused
	// across probes carries residual text; reset first, as the real submit
	// path does after every turn, so each probe types into an empty prompt.
	m.ta.Reset()
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
	return next.(ChatModel)
}

// runTurn drives one submitted turn to completion by executing the model's
// commands the way Bubble Tea would, and returns the final model.
func runTurn(t *testing.T, m ChatModel) ChatModel {
	t.Helper()
	next, cmd := m.Update(enter())
	return drainTurn(t, next.(ChatModel), cmd)
}

func drainTurn(t *testing.T, m ChatModel, cmd tea.Cmd) ChatModel {
	t.Helper()
	for cmd != nil {
		msg := cmd()
		var next tea.Model
		next, cmd = m.Update(msg)
		m = next.(ChatModel)
	}
	return m
}

func okResult(summary string) runtime.Result {
	cost := core.USDMicros(4200)
	return runtime.Result{
		Outcome: core.Outcome{Kind: core.OutcomeCompletedVerified},
		Usage:   core.Usage{InputTokens: 10, OutputTokens: 5},
		Cost:    core.CostReport{Actual: &cost},
		Summary: summary,
	}
}

func TestChatEmptyPromptIsNoop(t *testing.T) {
	m := NewChat(Options{Width: 120}, func(context.Context, string, runtime.Observer) (runtime.Result, error) {
		t.Fatal("runner must not run for an empty prompt")
		return runtime.Result{}, nil
	})
	next, cmd := m.Update(enter())
	got := next.(ChatModel)
	if cmd != nil || got.Running || len(got.Lines) != 0 {
		t.Fatalf("empty enter changed state: running=%v cmd=%v lines=%v", got.Running, cmd != nil, got.Lines)
	}
}

func TestChatTurnRendersReplyAndStatus(t *testing.T) {
	runner := func(_ context.Context, _ string, obs runtime.Observer) (runtime.Result, error) {
		obs.OnEvent(core.NewEvent(core.NewTaskID(), core.NewRunID(), 1, time.Unix(0, 0).UTC(),
			core.ToolCalled{Tool: "read_file", Risk: core.RiskReadOnly, InputSummary: "main.go"}))
		return okResult("Fixed the bug."), nil
	}
	m := typeText(NewChat(Options{Width: 120}, runner), "fix the bug")
	m = runTurn(t, m)

	if m.Running {
		t.Fatal("model still running after turn")
	}
	joined := strings.Join(m.Lines, "\n")
	for _, want := range []string{"[you] fix the bug", "[tool] read_file", "[ink] Fixed the bug."} {
		if !strings.Contains(joined, want) {
			t.Fatalf("scrollback missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "[cost]") {
		t.Fatal("cost line belongs in the status bar, not the transcript")
	}
	if m.Usage.OutputTokens != 5 {
		t.Fatalf("usage not captured from result: %+v", m.Usage)
	}
}

func TestChatStreamingAccumulatesIntoOneReply(t *testing.T) {
	// Deltas arrive word by word; they must accumulate into one committed
	// reply, never one scrollback line per token.
	runner := func(_ context.Context, _ string, obs runtime.Observer) (runtime.Result, error) {
		for _, w := range []string{"streamed", " ", "answer"} {
			obs.OnModelDelta(w)
		}
		return okResult("streamed answer"), nil
	}
	m := runTurn(t, typeText(NewChat(Options{Width: 120}, runner), "hi"))
	joined := strings.Join(m.Lines, "\n")
	if strings.Contains(joined, "[model]") {
		t.Fatalf("raw delta lines leaked into the scrollback:\n%s", joined)
	}
	if n := strings.Count(joined, "[ink] streamed answer"); n != 1 {
		t.Fatalf("want exactly one committed reply line, got %d:\n%s", n, joined)
	}
	if m.reply != "" {
		t.Fatalf("reply buffer not cleared after finish: %q", m.reply)
	}
}

func TestChatQuietTraceUnlessVerbose(t *testing.T) {
	// Usage/route accounting events and phases stay out of the chat unless
	// /verbose is on; tool lines always show.
	cost := core.USDMicros(100)
	noisy := func(_ context.Context, _ string, obs runtime.Observer) (runtime.Result, error) {
		obs.OnPhase(core.PhasePlanning)
		obs.OnEvent(core.NewEvent(core.NewTaskID(), core.NewRunID(), 1, time.Unix(0, 0).UTC(),
			core.ModelUsage{Provider: "fireworks", Model: "m", Usage: core.Usage{InputTokens: 1}, Cost: core.CostReport{Actual: &cost}}))
		obs.OnEvent(core.NewEvent(core.NewTaskID(), core.NewRunID(), 2, time.Unix(0, 0).UTC(),
			core.ToolCalled{Tool: "read_file", Risk: core.RiskReadOnly, InputSummary: "main.go"}))
		return okResult("done"), nil
	}
	m := runTurn(t, typeText(NewChat(Options{Width: 120}, noisy), "go"))
	joined := strings.Join(m.Lines, "\n")
	for _, banned := range []string{"[usage]", "[phase]"} {
		if strings.Contains(joined, banned) {
			t.Fatalf("trace line %s leaked into quiet chat:\n%s", banned, joined)
		}
	}
	if !strings.Contains(joined, "[tool] read_file") {
		t.Fatalf("tool line missing from quiet chat:\n%s", joined)
	}

	m = runTurn(t, typeText(m, "/verbose")) // toggle, then replay the noisy turn
	m = runTurn(t, typeText(m, "again"))
	joined = strings.Join(m.Lines, "\n")
	if !strings.Contains(joined, "[usage]") || !strings.Contains(joined, "[phase]") {
		t.Fatalf("verbose mode did not restore the trace:\n%s", joined)
	}
}

func TestChatToolAndThinkingDisplayToggles(t *testing.T) {
	toolTurn := func(_ context.Context, _ string, obs runtime.Observer) (runtime.Result, error) {
		obs.OnEvent(core.NewEvent(core.NewTaskID(), core.NewRunID(), 1, time.Unix(0, 0).UTC(),
			core.ToolCalled{Tool: "read_file", Risk: core.RiskReadOnly, InputSummary: "main.go"}))
		return okResult("done"), nil
	}
	m := slash(t, NewChat(Options{Width: 120}, toolTurn), "/tools")
	if m.showTools {
		t.Fatal("/tools should hide tool activity")
	}
	m = runTurn(t, typeText(m, "go"))
	if joined := strings.Join(m.Lines, "\n"); strings.Contains(joined, "[tool] read_file") {
		t.Fatalf("tool line leaked while hidden:\n%s", joined)
	}

	m = slash(t, m, "/thinking")
	if m.showThinking {
		t.Fatal("/thinking should hide thinking indicator")
	}
	m.Running, m.reply = true, ""
	if strings.Contains(m.layout().View(), "thinking…") {
		t.Fatal("thinking indicator should be hidden")
	}
}

func TestChatFailedOutcomeShowsReason(t *testing.T) {
	runner := func(context.Context, string, runtime.Observer) (runtime.Result, error) {
		return runtime.Result{
			Outcome: core.Outcome{Kind: core.OutcomeFailed, Reason: "no credential in env/secret_store", Category: core.FailureProviderError},
		}, nil
	}
	m := runTurn(t, typeText(NewChat(Options{Width: 120}, runner), "hi"))
	joined := strings.Join(m.Lines, "\n")
	if !strings.Contains(joined, "no credential in env/secret_store") {
		t.Fatalf("failed turn hid the reason:\n%s", joined)
	}
	if strings.Contains(joined, "[cost]") {
		t.Fatalf("failed turn must not dump a cost line into the transcript:\n%s", joined)
	}
}

func TestChatRunnerErrorShown(t *testing.T) {
	runner := func(context.Context, string, runtime.Observer) (runtime.Result, error) {
		return runtime.Result{}, context.DeadlineExceeded
	}
	m := runTurn(t, typeText(NewChat(Options{Width: 120}, runner), "go"))
	if m.Running {
		t.Fatal("still running after failed turn")
	}
	if !strings.Contains(strings.Join(m.Lines, "\n"), "[warn] turn failed") {
		t.Fatalf("error line missing:\n%s", strings.Join(m.Lines, "\n"))
	}
}

func TestChatSubmitClearsPromptAndDoesNotMutateReceiver(t *testing.T) {
	m := typeText(NewChat(Options{Width: 120}, func(context.Context, string, runtime.Observer) (runtime.Result, error) {
		return okResult("ok"), nil
	}), "hello")
	before := len(m.Lines)
	next, _ := m.Update(enter())
	got := next.(ChatModel)
	if len(m.Lines) != before {
		t.Fatalf("Update mutated receiver Lines: %v", m.Lines)
	}
	if got.ta.Value() != "" {
		t.Fatalf("prompt not cleared after submit: %q", got.ta.Value())
	}
	if !got.Running {
		t.Fatal("model not marked running after submit")
	}
}

func TestChatCtrlCRequiresConfirmation(t *testing.T) {
	m := NewChat(Options{Width: 120}, nil)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd != nil {
		t.Fatal("first ctrl+c must only arm quit confirmation")
	}
	m = next.(ChatModel)
	if !m.quitHint || m.quitKey != "ctrl+c" {
		t.Fatalf("quit hint not armed: hint=%v key=%q", m.quitHint, m.quitKey)
	}
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("second ctrl+c returned no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("ctrl+c did not quit, got %T", cmd())
	}
}

func TestChatCtrlQRequiresConfirmation(t *testing.T) {
	m := NewChat(Options{Width: 80, NoColor: true}, nil)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlQ})
	if cmd != nil {
		t.Fatal("first ctrl+q must only arm quit confirmation")
	}
	m = next.(ChatModel)
	if !m.quitHint || m.quitKey != "ctrl+q" {
		t.Fatalf("quit hint not armed: hint=%v key=%q", m.quitHint, m.quitKey)
	}
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlQ})
	if cmd == nil {
		t.Fatal("second ctrl+q returned no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("ctrl+q did not quit, got %T", cmd())
	}
}

func TestChatEscClearsQuitConfirmation(t *testing.T) {
	m := NewChat(Options{Width: 80, NoColor: true}, nil)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = next.(ChatModel)
	if !m.quitHint || !strings.Contains(m.footerView(), "again to quit") {
		t.Fatalf("quit hint not armed: hint=%v footer=%q", m.quitHint, m.footerView())
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(ChatModel)
	if cmd != nil {
		t.Fatal("esc clearing quit confirmation must not quit")
	}
	if m.quitHint || strings.Contains(m.footerView(), "again to quit") {
		t.Fatalf("esc left quit hint armed: hint=%v footer=%q", m.quitHint, m.footerView())
	}
}

// slash types a slash-command line and submits it; commands resolve
// synchronously (no turn goroutine), so the returned model is final.
func slash(t *testing.T, m ChatModel, line string) ChatModel {
	t.Helper()
	next, _ := typeText(m, line).Update(enter())
	return next.(ChatModel)
}

func TestChatSlashHelpModelClear(t *testing.T) {
	m := NewChat(Options{Width: 120, Route: "openai/gpt-4o"}, nil)

	m = slash(t, m, "/help")
	if m.Running {
		t.Fatal("/help must not start a turn")
	}
	help := strings.Join(m.Lines, "\n")
	for _, want := range []string{"INK(1)", "SESSION", "/model", "/agent NAME", "ctrl+c, ctrl+q"} {
		if !strings.Contains(help, want) {
			t.Fatalf("/help missing %q:\n%s", want, help)
		}
	}

	m = slash(t, m, "/model")
	if !strings.Contains(strings.Join(m.Lines, "\n"), "route: openai/gpt-4o") {
		t.Fatalf("/model did not show the route:\n%s", strings.Join(m.Lines, "\n"))
	}

	m = slash(t, m, "/clear")
	if len(m.Lines) != 0 {
		t.Fatalf("/clear left %d lines: %v", len(m.Lines), m.Lines)
	}
}

func TestChatHomeShowsWelcomeWithoutDroppingHistory(t *testing.T) {
	m := runTurn(t, typeText(NewChat(Options{Width: 80, NoColor: true, Route: "fireworks/deepseek-v4-flash"},
		func(context.Context, string, runtime.Observer) (runtime.Result, error) {
			return okResult("answer"), nil
		}), "hello"))
	before := len(m.Lines)
	m = slash(t, m, "/home")
	if !strings.Contains(m.View(), "Ask anything") {
		t.Fatalf("/home did not show welcome:\n%s", m.View())
	}
	if len(m.Lines) != before {
		t.Fatal("/home must not erase the transcript")
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if strings.Contains(next.(ChatModel).View(), "Ask anything") {
		t.Fatal("esc should close the home surface")
	}
}

func TestChatDeleteRequiresConfirmation(t *testing.T) {
	var deleted string
	m := NewChat(Options{
		Width:     80,
		NoColor:   true,
		SessionID: "s1",
		DeleteSession: func() (string, error) {
			deleted = "s1"
			return "s2", nil
		},
	}, nil)
	warn := slash(t, m, "/delete")
	if deleted != "" {
		t.Fatal("/delete without confirm must not delete")
	}
	if !strings.Contains(strings.Join(warn.Lines, "\n"), "/delete confirm") {
		t.Fatalf("delete warning missing confirmation:\n%s", strings.Join(warn.Lines, "\n"))
	}
	done := slash(t, m, "/delete confirm")
	if deleted != "s1" {
		t.Fatalf("deleted %q, want s1", deleted)
	}
	if done.sessionID != "s2" || len(done.Lines) == 0 {
		t.Fatalf("delete did not rotate to a clean session: id=%q lines=%v", done.sessionID, done.Lines)
	}
}

func TestChatUnknownCommandWarns(t *testing.T) {
	m := slash(t, NewChat(Options{Width: 120}, func(context.Context, string, runtime.Observer) (runtime.Result, error) {
		t.Fatal("unknown command must not run as a turn")
		return runtime.Result{}, nil
	}), "/bogus")
	if !strings.Contains(strings.Join(m.Lines, "\n"), "unknown command") {
		t.Fatalf("no warning for unknown command:\n%s", strings.Join(m.Lines, "\n"))
	}
}

func TestChatQuitCommand(t *testing.T) {
	_, cmd := typeText(NewChat(Options{Width: 120}, nil), "/quit").Update(enter())
	if cmd == nil {
		t.Fatal("/quit returned no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("/quit did not quit, got %T", cmd())
	}
}

func TestChatCostAccumulatesAcrossTurns(t *testing.T) {
	runner := func(context.Context, string, runtime.Observer) (runtime.Result, error) {
		return okResult("done"), nil
	}
	m := NewChat(Options{Width: 120}, runner)
	m = runTurn(t, typeText(m, "one"))
	m = runTurn(t, typeText(m, "two"))
	if m.Usage.OutputTokens != 10 || m.Usage.InputTokens != 20 {
		t.Fatalf("usage not summed across turns: %+v", m.Usage)
	}
	if m.Cost.Actual == nil || *m.Cost.Actual != core.USDMicros(8400) {
		t.Fatalf("cost not summed across turns: %v", m.Cost.Actual)
	}
	m = slash(t, m, "/cost")
	if !strings.Contains(strings.Join(m.Lines, "\n"), "out 10") {
		t.Fatalf("/cost did not report totals:\n%s", strings.Join(m.Lines, "\n"))
	}
}

func TestChatNewSessionRotatesAndClears(t *testing.T) {
	rotated := false
	m := NewChat(Options{Width: 120, NewSession: func() (string, error) { rotated = true; return "new-id", nil }},
		func(context.Context, string, runtime.Observer) (runtime.Result, error) { return okResult("hi"), nil })
	m = runTurn(t, typeText(m, "hello"))
	m = slash(t, m, "/new")
	if !rotated {
		t.Fatal("/new did not call NewSession")
	}
	if m.Usage.OutputTokens != 0 || m.Cost.Actual != nil {
		t.Fatalf("/new did not reset totals: usage=%+v cost=%v", m.Usage, m.Cost.Actual)
	}
	joined := strings.Join(m.Lines, "\n")
	if strings.Contains(joined, "[you] hello") {
		t.Fatalf("/new did not clear scrollback:\n%s", joined)
	}
	if !strings.Contains(joined, "new session") {
		t.Fatalf("/new gave no confirmation:\n%s", joined)
	}
}

func TestChatModelSwitch(t *testing.T) {
	var got string
	opts := Options{
		Width: 120, Route: "openai/gpt-4o", Active: "smart",
		Routes: []RouteInfo{
			{Name: "smart", Provider: "openai", Model: "gpt-4o"},
			{Name: "fast", Provider: "fireworks", Model: "kimi"},
		},
		SwitchModel: func(name string) error { got = name; return nil },
	}
	m := NewChat(opts, nil)

	// /model with no argument opens the picker with the cursor on the
	// active route.
	picker := slash(t, m, "/model")
	if picker.ov == nil || picker.ov.kind != overlayModels {
		t.Fatal("/model with routes must open the picker overlay")
	}
	if got := picker.ov.items[picker.ov.cursor].id; got != "smart" {
		t.Fatalf("picker cursor on %q, want the active route", got)
	}
	view := picker.ov.view(newChatStyles(false), 120, 8)
	if !strings.Contains(view, "smart") || !strings.Contains(view, "fast") || !strings.Contains(view, "active") {
		t.Fatalf("picker view missing routes or active marker:\n%s", view)
	}

	// /model NAME switches: it calls the callback and updates the header route.
	sw := slash(t, m, "/model fast")
	if got != "fast" {
		t.Fatalf("SwitchModel called with %q, want fast", got)
	}
	if sw.active != "fast" || sw.route != "fireworks/kimi" {
		t.Fatalf("switch left active=%q route=%q", sw.active, sw.route)
	}
	if !strings.Contains(strings.Join(sw.Lines, "\n"), "switched to fast") {
		t.Fatalf("no switch confirmation:\n%s", strings.Join(sw.Lines, "\n"))
	}

	// An unknown route warns and never calls the callback.
	got = ""
	bad := slash(t, m, "/model nope")
	if got != "" {
		t.Fatal("unknown route must not call SwitchModel")
	}
	if !strings.Contains(strings.Join(bad.Lines, "\n"), "unknown route") {
		t.Fatalf("no unknown-route warning:\n%s", strings.Join(bad.Lines, "\n"))
	}

	// Refused mid-turn (defensive: the submit path already blocks input).
	running := m
	running.Running = true
	refused, _ := running.switchModel("fast")
	if !strings.Contains(strings.Join(refused.(ChatModel).Lines, "\n"), "before /model") {
		t.Fatalf("mid-turn switch not refused:\n%v", refused.(ChatModel).Lines)
	}
}

func TestShortRoute(t *testing.T) {
	cases := map[string]string{
		"fireworks/deepseek-v4-flash":                                "deepseek-v4-flash",
		"fireworks/accounts/fireworks/models/deepseek-v4-flash-0731": "deepseek-v4-flash-0731",
		"kimi": "",
	}
	if shortRoute("kimi") != "kimi" {
		t.Fatalf("bare name: %q", shortRoute("kimi"))
	}
	if shortRoute("fireworks/deepseek-v4-flash") != "deepseek-v4-flash" {
		t.Fatalf("provider/model: %q", shortRoute("fireworks/deepseek-v4-flash"))
	}
	got := shortRoute("fireworks/accounts/fireworks/models/deepseek-v4-flash-0731")
	if got != "deepseek-v4-flash-0731" {
		t.Fatalf("accounts path: %q", got)
	}
	_ = cases
}

func TestChatStyleConversationPlain(t *testing.T) {
	cs := newChatStyles(false) // NO_COLOR: plain text, structural rail only
	out := cs.conversation([]string{
		"[you] fix the bug",
		"[ink] on it",
		"[tool] read_file ok in 3ms",
	}, 40, -1)
	for _, want := range []string{"fix the bug", "on it", "read_file ok in 3ms"} {
		if !strings.Contains(out, want) {
			t.Fatalf("conversation missing %q:\n%s", want, out)
		}
	}
	for _, want := range []string{"You", "Ink", "Tool"} {
		if !strings.Contains(out, want) {
			t.Fatalf("conversation missing label %q:\n%s", want, out)
		}
	}
	for _, want := range []string{"╭ You", "╭ Ink"} {
		if !strings.Contains(out, want) {
			t.Fatalf("conversation missing message box %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "▍") {
		t.Fatalf("selection chrome leaked into the transcript:\n%s", out)
	}
	if strings.Contains(out, "[you]") || strings.Contains(out, "[tool]") {
		t.Fatalf("raw tags leaked into rendered body:\n%s", out)
	}
	if strings.Contains(out, "\x1b[") {
		t.Fatalf("NO_COLOR render emitted ANSI escapes:\n%q", out)
	}
}

func TestChatMarkdownAssistantView(t *testing.T) {
	cs := newChatStyles(false)
	out := cs.conversation([]string{
		"[ink] ## Heading",
		"[ink] intro",
		"[ink] ```",
		"[ink] func main() {}",
		"[ink] ```",
		"[ink] after",
	}, 40, -1)
	if strings.Contains(out, "[ink]") {
		t.Fatalf("tag leaked into painted view:\n%s", out)
	}
	for _, want := range []string{"Heading", "intro", "func main() {}", "after"} {
		if !strings.Contains(out, want) {
			t.Fatalf("markdown view missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "## ") {
		t.Fatalf("raw heading marks survived:\n%s", out)
	}
	if strings.Contains(out, "```") {
		t.Fatalf("fence markers survived:\n%s", out)
	}
}

func TestChatStyleGroupsAssistantResponseInOneBox(t *testing.T) {
	cs := newChatStyles(false)
	out := cs.conversation([]string{
		"[ink] Hi! I'm Ink, a coding agent for this project.",
		"[ink] ",
		"[ink] What can I help you with?",
		"[ink] - Look around the codebase",
		"[ink] - Make changes",
	}, 80, -1)
	if got := strings.Count(out, "╭ Ink"); got != 1 {
		t.Fatalf("assistant response rendered %d boxes, want 1:\n%s", got, out)
	}
	for _, want := range []string{"Hi! I'm Ink", "What can I help", "Look around", "Make changes"} {
		if !strings.Contains(out, want) {
			t.Fatalf("grouped response missing %q:\n%s", want, out)
		}
	}
}

func TestChatCopyHelpersReturnMessageBodyWithoutRails(t *testing.T) {
	cs := newChatStyles(false)
	out := cs.conversation([]string{
		"[ink] Hi! I'm Ink.",
		"[ink] ",
		"[ink] - Build and test",
	}, 80, -1)
	if !strings.Contains(out, "│ Hi!") {
		t.Fatalf("rendered message should keep side rails:\n%s", out)
	}
	copied := lastAssistantReply([]string{
		"[ink] Hi! I'm Ink.",
		"[ink] ",
		"[ink] - Build and test",
	})
	if strings.Contains(copied, "│") || strings.Contains(copied, "╭") || strings.Contains(copied, "╰") {
		t.Fatalf("copy helper leaked frame glyphs: %q", copied)
	}
	for _, want := range []string{"Hi! I'm Ink.", "- Build and test"} {
		if !strings.Contains(copied, want) {
			t.Fatalf("copy helper missing %q: %q", want, copied)
		}
	}
}

func TestChatCopySuccessUsesFooterNotice(t *testing.T) {
	var got string
	m := NewChat(Options{
		Width: 80, NoColor: true,
		Copy: func(s string) error { got = s; return nil },
	}, nil)
	m.Lines = []string{tagReply + " hello"}
	m = slash(t, m, "/copy")
	if got != "hello" {
		t.Fatalf("/copy got %q", got)
	}
	if strings.Contains(strings.Join(m.Lines, "\n"), tagWarn+" copied") {
		t.Fatalf("successful copy rendered as warning:\n%s", strings.Join(m.Lines, "\n"))
	}
	if !strings.Contains(m.footerView(), "copied to clipboard") {
		t.Fatalf("footer missing copy notice: %s", m.footerView())
	}
}

func TestWrapText(t *testing.T) {
	for _, r := range wrapText("alpha beta gamma delta epsilon", 11) {
		if runeLen(r) > 11 {
			t.Fatalf("row exceeds width 11: %q", r)
		}
	}
	// A single word longer than width is hard-broken, losing no characters.
	if got := strings.Join(wrapText("supercalifragilistic", 5), ""); got != "supercalifragilistic" {
		t.Fatalf("hard-break altered text: %q", got)
	}
}

func TestChatCtrlCCancelsTurnThenQuits(t *testing.T) {
	runner := func(ctx context.Context, _ string, _ runtime.Observer) (runtime.Result, error) {
		<-ctx.Done() // block until Ctrl-C cancels the turn
		return runtime.Result{}, ctx.Err()
	}
	next, wait := typeText(NewChat(Options{Width: 120}, runner), "slow").Update(enter())
	m := next.(ChatModel)
	if !m.Running || wait == nil {
		t.Fatalf("turn did not start: running=%v", m.Running)
	}

	// First Ctrl-C: cancel, not quit. Still running until the result lands.
	next, c := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = next.(ChatModel)
	if c != nil {
		if _, ok := c().(tea.QuitMsg); ok {
			t.Fatal("first Ctrl-C quit instead of cancelling")
		}
	}
	if !m.Running {
		t.Fatal("model stopped running before the cancelled result arrived")
	}

	// Second Ctrl-C while cancelling: quits.
	if _, q := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC}); q == nil {
		t.Fatal("second Ctrl-C returned no command")
	} else if _, ok := q().(tea.QuitMsg); !ok {
		t.Fatalf("second Ctrl-C did not quit, got %T", q())
	}

	// The outstanding waiter delivers the cancelled turn result.
	next, _ = m.Update(wait())
	m = next.(ChatModel)
	if m.Running {
		t.Fatal("still running after cancelled result")
	}
	if !strings.Contains(strings.Join(m.Lines, "\n"), "turn cancelled") {
		t.Fatalf("no cancellation notice:\n%s", strings.Join(m.Lines, "\n"))
	}
}

func TestChatStyleDoesNotClipStyledLineStarts(t *testing.T) {
	cs := newChatStyles(true)
	out := cs.conversation([]string{
		"[ink] hi",
		"[ink] What would you like to work on?",
	}, 64, -1)
	plain := stripANSI(out)
	for _, want := range []string{"hi", "What would you like"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("styled conversation lost %q:\n%s", want, plain)
		}
	}
}

func TestChatEscCancelsTurn(t *testing.T) {
	runner := func(ctx context.Context, _ string, _ runtime.Observer) (runtime.Result, error) {
		<-ctx.Done()
		return runtime.Result{}, ctx.Err()
	}
	next, wait := typeText(NewChat(Options{Width: 120}, runner), "slow").Update(enter())
	m := next.(ChatModel)
	if !m.Running || wait == nil {
		t.Fatalf("turn did not start: running=%v", m.Running)
	}

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(ChatModel)
	if cmd != nil {
		t.Fatal("esc cancel must not quit")
	}
	if m.cancel != nil {
		t.Fatal("esc did not cancel the turn context")
	}
	if !strings.Contains(strings.Join(m.Lines, "\n"), "cancelling turn") {
		t.Fatalf("missing cancellation notice:\n%s", strings.Join(m.Lines, "\n"))
	}

	next, _ = m.Update(wait())
	m = next.(ChatModel)
	if m.Running {
		t.Fatal("still running after cancelled result")
	}
}

// TestChatFrameCopiesClean guards the copy contract: terminal
// selection must grab text, not decoration — no rendered row may end in
// whitespace, because trailing padding travels with every copied line.
func TestChatFrameCopiesClean(t *testing.T) {
	noisy := func(_ context.Context, _ string, obs runtime.Observer) (runtime.Result, error) {
		obs.OnEvent(core.NewEvent(core.NewTaskID(), core.NewRunID(), 1, time.Unix(0, 0).UTC(),
			core.ToolCalled{Tool: "read_file", InputSummary: "greet.go"}))
		for _, w := range []string{"a long ", "answer ", "that surely ", "wraps across ", "several rendered rows ", "when the pane is narrow enough to force it"} {
			obs.OnModelDelta(w)
		}
		return okResult("a long answer that surely wraps across several rendered rows when the pane is narrow enough to force it"), nil
	}
	m := NewChat(Options{Width: 48}, noisy)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 48, Height: 20})
	m = next.(ChatModel)
	m = runTurn(t, typeText(m, "please wrap"))

	for i, line := range strings.Split(m.View(), "\n") {
		if line != strings.TrimRight(line, " \t") {
			t.Fatalf("row %d ends in whitespace: %q", i, line)
		}
	}
}

func TestChatCtrlPPaletteRunsAction(t *testing.T) {
	m := NewChat(Options{Width: 80}, nil)
	next, _ := m.Update(keyType(tea.KeyCtrlP))
	cm := next.(ChatModel)
	if cm.ov == nil || cm.ov.kind != overlayPalette {
		t.Fatal("ctrl+p must open the command palette")
	}
	next, _ = cm.Update(keyRunes("verb"))
	next, _ = next.(ChatModel).Update(enter())
	cm = next.(ChatModel)
	if cm.ov != nil {
		t.Fatal("commit must close the palette")
	}
	if !cm.verbose {
		t.Fatal("palette verbose action did not run")
	}
	if got := cm.ta.Value(); got != "" {
		t.Fatalf("palette typing leaked into the prompt: %q", got)
	}

	// Esc closes without running anything.
	next, _ = cm.Update(keyType(tea.KeyCtrlP))
	next, _ = next.(ChatModel).Update(keyType(tea.KeyEsc))
	if next.(ChatModel).ov != nil {
		t.Fatal("esc must close the palette")
	}
}

func TestChatPaletteChainsToModels(t *testing.T) {
	var got string
	m := NewChat(Options{
		Width: 80, Active: "smart",
		Routes: []RouteInfo{
			{Name: "smart", Provider: "openai", Model: "gpt-4o"},
			{Name: "fast", Provider: "fireworks", Model: "kimi"},
		},
		SwitchModel: func(name string) error { got = name; return nil },
	}, nil)
	next, _ := m.Update(keyType(tea.KeyCtrlP))
	cm := next.(ChatModel)
	if cm.ov == nil || cm.ov.kind != overlayPalette {
		t.Fatal("ctrl+p must open the palette")
	}
	next, _ = cm.Update(keyRunes("switch model"))
	next, _ = next.(ChatModel).Update(enter())
	cm = next.(ChatModel)
	if cm.ov == nil || cm.ov.kind != overlayModels {
		t.Fatal("palette Switch model must chain into the model picker")
	}
	// Cursor starts on smart; down + enter picks fast and switches live.
	next, _ = cm.Update(keyType(tea.KeyDown))
	next, _ = next.(ChatModel).Update(enter())
	cm = next.(ChatModel)
	if cm.ov != nil {
		t.Fatal("picking a route must close the picker")
	}
	if got != "fast" || cm.active != "fast" {
		t.Fatalf("picker switch got callback=%q active=%q, want fast", got, cm.active)
	}
}

func TestChatOverlayFrameCopiesClean(t *testing.T) {
	m := NewChat(Options{Width: 48, NoColor: true}, nil)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 48, Height: 20})
	next, _ = next.(ChatModel).Update(keyType(tea.KeyCtrlP))
	next, _ = next.(ChatModel).Update(keyRunes("mo"))
	for i, row := range strings.Split(next.(ChatModel).View(), "\n") {
		if row != strings.TrimRight(row, " \t") {
			t.Fatalf("overlay frame row %d ends in whitespace: %q", i, row)
		}
	}
}

func TestChatRebindHonored(t *testing.T) {
	km, err := ParseKeymap(map[string]string{"palette": "ctrl+o", "scroll_up": "ctrl+k"})
	if err != nil {
		t.Fatal(err)
	}
	m := NewChat(Options{Width: 120, Keys: km}, nil)
	r, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	if r.(ChatModel).ov != nil {
		t.Fatal("default key still opens the palette after rebind")
	}
	r, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	if got := r.(ChatModel).ov; got == nil || got.kind != overlayPalette {
		t.Fatal("rebound key did not open the palette")
	}
}

func TestChatHelpListsBindings(t *testing.T) {
	km, _ := ParseKeymap(map[string]string{"palette": "ctrl+o"})
	m := NewChat(Options{Width: 120, Keys: km}, nil)
	r, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = typeText(r.(ChatModel), "/help")
	r, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	joined := strings.Join(r.(ChatModel).Lines, "\n")
	if !strings.Contains(joined, "ctrl+o          command palette") {
		t.Fatalf("help does not show the effective binding:\n%s", joined)
	}
	if !strings.Contains(r.(ChatModel).footerView(), "ctrl+o palette") {
		t.Fatalf("footer hint not rebound: %s", r.(ChatModel).footerView())
	}
}

func TestChatCompactCommand(t *testing.T) {
	m := NewChat(Options{Width: 120, Compact: func(context.Context, runtime.Observer) (string, error) {
		return "compacted 6 turns into a summary", nil
	}}, nil)
	r, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = typeText(r.(ChatModel), "/compact")
	r, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = r.(ChatModel)
	if !m.Running || cmd == nil {
		t.Fatal("/compact did not start a running compact")
	}
	// The background goroutine posts compactDoneMsg; feed it directly.
	r, _ = m.Update(compactDoneMsg{note: "compacted 6 turns into a summary"})
	m = r.(ChatModel)
	if m.Running {
		t.Fatal("still running after compactDoneMsg")
	}
	joined := strings.Join(m.Lines, "\n")
	if !strings.Contains(joined, "compacted 6 turns") {
		t.Fatalf("note not shown:\n%s", joined)
	}
}

func TestChatCompactUnavailable(t *testing.T) {
	m := typeText(NewChat(Options{Width: 120}, nil), "/compact")
	r, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = r.(ChatModel)
	if m.Running {
		t.Fatal("compact ran with no callback")
	}
	if !strings.Contains(strings.Join(m.Lines, "\n"), "not available") {
		t.Fatalf("missing warn: %v", m.Lines)
	}
}

func TestChatCompactFailureWarns(t *testing.T) {
	m := NewChat(Options{Width: 120, Compact: func(context.Context, runtime.Observer) (string, error) {
		return "", errors.New("boom")
	}}, nil)
	m = typeText(m, "/compact")
	r, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	r, _ = r.(ChatModel).Update(compactDoneMsg{err: errors.New("boom")})
	if !strings.Contains(strings.Join(r.(ChatModel).Lines, "\n"), "compact failed: boom") {
		t.Fatalf("missing failure warn: %v", r.(ChatModel).Lines)
	}
}

func TestChatCompactDoneDisarmsWaiter(t *testing.T) {
	m := NewChat(Options{Width: 120}, nil)
	m.Running = true
	_, cmd := m.Update(compactDoneMsg{note: "done"})
	if cmd != nil {
		t.Fatal("compactDoneMsg armed a waiter with no producer — leaks a channel reader")
	}
}

func TestChatQuitMidTurnCancels(t *testing.T) {
	m := NewChat(Options{Width: 120}, nil)
	cancelled := false
	m.Running, m.cancel = true, func() { cancelled = true }
	_, cmd := m.command("/quit")
	if !cancelled {
		t.Fatal("/quit left the in-flight turn running")
	}
	if cmd == nil {
		t.Fatal("/quit did not quit")
	}
}

func TestChatCompactMidTurnWarns(t *testing.T) {
	m := NewChat(Options{Width: 120, Compact: func(context.Context, runtime.Observer) (string, error) { return "", nil }}, nil)
	m.Running = true
	r, _ := m.command("/compact")
	if !strings.Contains(strings.Join(r.(ChatModel).Lines, "\n"), "before /compact") {
		t.Fatal("mid-turn /compact gave no feedback")
	}
}

func TestChatCustomCommand(t *testing.T) {
	var prompt string
	runner := func(_ context.Context, p string, _ runtime.Observer) (runtime.Result, error) {
		prompt = p
		return okResult("done"), nil
	}
	opts := Options{Width: 120, Commands: []CommandInfo{
		{Name: "deploy", Description: "ship it", Body: "Deploy $ARGUMENTS now."},
	}}
	m := NewChat(opts, runner)
	m = runTurn(t, typeText(m, "/deploy prod"))
	if prompt != "Deploy prod now." {
		t.Fatalf("runner got %q", prompt)
	}
	out := strings.Join(m.Lines, "\n")
	if !strings.Contains(out, "/deploy prod") {
		t.Fatalf("typed form missing from scrollback:\n%s", out)
	}
	if strings.Contains(out, "Deploy prod now.") {
		t.Fatalf("expanded prompt leaked into scrollback:\n%s", out)
	}

	found := false
	for _, it := range m.paletteItems() {
		if it.id == "cmd:deploy" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("custom commands must appear in Ctrl+P")
	}
}

func TestSkillsPicker(t *testing.T) {
	var prompt string
	m := NewChat(Options{
		Width: 80, NoColor: true,
		Skills: []SkillInfo{{Name: "taste", Description: "have taste"}},
	}, func(_ context.Context, p string, _ runtime.Observer) (runtime.Result, error) {
		prompt = p
		return okResult("ok"), nil
	})

	next, _ := m.command("/skills")
	cm := next.(ChatModel)
	if cm.ov == nil || cm.ov.kind != overlaySkills || cm.ov.items[0].id != "taste" {
		t.Fatalf("skills overlay = %+v", cm.ov)
	}
	next, cmd := cm.overlayCommit(overlaySkills, "taste")
	cm = drainTurn(t, next.(ChatModel), cmd)
	if !strings.Contains(prompt, "taste") {
		t.Fatalf("skill invoke prompt = %q", prompt)
	}

	next, _ = m.command("/skills")
	cm = next.(ChatModel)
	if !strings.Contains(cm.ov.view(newChatStyles(false), 72, 20), "How to manage") {
		t.Fatalf("skills overlay missing how-to:\n%s", cm.ov.view(newChatStyles(false), 72, 20))
	}
	next, _ = cm.overlayCommit(overlaySkills, "help:add-skill")
	cm = next.(ChatModel)
	if !strings.Contains(strings.Join(cm.Lines, "\n"), "reusable instruction pack") {
		t.Fatalf("skill how-to missing:\n%s", strings.Join(cm.Lines, "\n"))
	}
}

func TestPaletteStampsToggleState(t *testing.T) {
	m := NewChat(Options{Width: 80, NoColor: true, VimMode: true}, nil)
	m.verbose, m.multiline = true, true
	var found string
	for _, it := range m.paletteItems() {
		if it.id == "vim-mode" {
			found = it.state
		}
		if it.id == "cycle-mode" && it.state != "normal" {
			t.Fatalf("cycle-mode state = %q", it.state)
		}
		if it.id == "verbose" && it.state != "on" {
			t.Fatalf("verbose state = %q", it.state)
		}
	}
	if found != "on" {
		t.Fatalf("vim-mode state = %q, want on", found)
	}
}

func TestChatCustomCommandModelSwitch(t *testing.T) {
	var switched, prompt string
	runner := func(_ context.Context, p string, _ runtime.Observer) (runtime.Result, error) {
		prompt = p
		return okResult("done"), nil
	}
	opts := Options{
		Width: 120, Active: "smart",
		Routes: []RouteInfo{
			{Name: "smart", Provider: "openai", Model: "gpt-4o"},
			{Name: "fast", Provider: "fireworks", Model: "kimi"},
		},
		SwitchModel: func(name string) error { switched = name; return nil },
		Commands: []CommandInfo{
			{Name: "quick", Model: "fast", Body: "Be quick."},
			{Name: "ghost", Model: "nope", Body: "Never runs."},
		},
	}
	m := NewChat(opts, runner)
	got := runTurn(t, typeText(m, "/quick"))
	if switched != "fast" || got.active != "fast" {
		t.Fatalf("switched=%q active=%q", switched, got.active)
	}
	if prompt != "Be quick." {
		t.Fatalf("runner got %q", prompt)
	}

	// A command naming an unconfigured route warns and never runs.
	prompt = ""
	bad := slash(t, m, "/ghost")
	if prompt != "" {
		t.Fatal("ghost must not run a turn")
	}
	if !strings.Contains(strings.Join(bad.Lines, "\n"), "not configured") {
		t.Fatalf("no route warning:\n%v", bad.Lines)
	}
}

func TestChatAgentSwitch(t *testing.T) {
	var got string
	opts := Options{
		Width:    120,
		Agents:   []AgentInfo{{Name: "reader", Description: "read-only helper"}},
		SetAgent: func(name string) error { got = name; return nil },
	}
	m := NewChat(opts, nil)

	// /agent with no argument opens the picker with a "none" entry first.
	picker := slash(t, m, "/agent")
	if picker.ov == nil || picker.ov.kind != overlayAgents {
		t.Fatal("/agent must open the agent overlay")
	}
	if picker.ov.items[0].id != "none" || picker.ov.items[1].id != "reader" {
		t.Fatalf("picker items %+v", picker.ov.items)
	}

	// /agent NAME activates and the status line shows it.
	on := slash(t, m, "/agent reader")
	if got != "reader" || on.agentName != "reader" {
		t.Fatalf("set=%q agentName=%q", got, on.agentName)
	}
	if !strings.Contains(on.View(), "reader") {
		t.Fatal("header must show the active agent")
	}

	// /agent none resets.
	off := slash(t, on, "/agent none")
	if got != "" || off.agentName != "" {
		t.Fatalf("reset set=%q agentName=%q", got, off.agentName)
	}

	// Unknown agent warns without calling the callback.
	got = "sentinel"
	bad := slash(t, m, "/agent nope")
	if got != "sentinel" {
		t.Fatal("unknown agent must not call SetAgent")
	}
	if !strings.Contains(strings.Join(bad.Lines, "\n"), "unknown agent") {
		t.Fatalf("no unknown-agent warning:\n%v", bad.Lines)
	}

	// Refused mid-turn.
	running := m
	running.Running = true
	refused, _ := running.applyAgent("reader")
	if !strings.Contains(strings.Join(refused.(ChatModel).Lines, "\n"), "before /agent") {
		t.Fatalf("mid-turn switch not refused:\n%v", refused.(ChatModel).Lines)
	}
}

func TestChatApprovalKeys(t *testing.T) {
	m := NewChat(Options{Width: 120}, nil)
	m.Running = true
	reply := make(chan core.ApprovalResolution, 1)
	a := core.Approval{ID: core.NewApprovalID(), Request: core.CapabilityRequest{Tool: "write_file", Capability: core.Capability{Risk: core.RiskWriteLocal, Scope: core.ResourceScope{Kind: core.ScopePath, Path: "a.txt"}}}, Preview: "a.txt\n+ hi"}
	next, _ := m.Update(ApprovalMsg{A: a, Reply: reply})
	m = next.(ChatModel)
	if m.pending == nil {
		t.Fatal("approval not pending")
	}
	joined := strings.Join(m.Lines, "\n")
	if !strings.Contains(joined, "write_file") {
		t.Fatalf("prompt missing from scrollback: %q", joined)
	}
	view := m.View()
	for _, want := range []string{"Write a.txt", "+ hi", "Allow once", "Allow this session", "Reject"} {
		if !strings.Contains(view, want) {
			t.Fatalf("approval card missing %q:\n%s", want, view)
		}
	}
	if !strings.Contains(m.footerView(), "choose") {
		t.Fatalf("footer misses approval keys: %q", m.footerView())
	}
	// Draft keys are swallowed while pending.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m = next.(ChatModel)
	if m.pending == nil || len(reply) != 0 {
		t.Fatal("typed rune answered the approval")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(ChatModel)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(ChatModel)
	if m.approvalSel != 2 {
		t.Fatalf("down to reject: sel=%d", m.approvalSel)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(ChatModel)
	if m.pending != nil {
		t.Fatal("enter left the approval pending")
	}
	if r := <-reply; r.Decision != core.ApprovalDenied || r.Scope != core.ApprovalOnce {
		t.Fatalf("enter on reject resolved %s/%s", r.Decision, r.Scope)
	}

	replyS := make(chan core.ApprovalResolution, 1)
	next, _ = m.Update(ApprovalMsg{A: a, Reply: replyS})
	m = next.(ChatModel)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	m = next.(ChatModel)
	if m.pending != nil {
		t.Fatal("s left the approval pending")
	}
	r := <-replyS
	if r.Decision != core.ApprovalApproved || r.Scope != core.ApprovalSession {
		t.Fatalf("s resolved %s/%s", r.Decision, r.Scope)
	}

	// esc denies once.
	reply2 := make(chan core.ApprovalResolution, 1)
	next, _ = m.Update(ApprovalMsg{A: a, Reply: reply2})
	m = next.(ChatModel)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(ChatModel)
	if r := <-reply2; r.Decision != core.ApprovalDenied || r.Scope != core.ApprovalOnce {
		t.Fatalf("esc resolved %s/%s", r.Decision, r.Scope)
	}

	// A turn ending mid-prompt clears it without a reply.
	reply3 := make(chan core.ApprovalResolution, 1)
	next, _ = m.Update(ApprovalMsg{A: a, Reply: reply3})
	m = next.(ChatModel)
	next, _ = m.Update(turnDoneMsg{res: okResult("done")})
	m = next.(ChatModel)
	if m.pending != nil || len(reply3) != 0 {
		t.Fatal("turn end left the approval pending or answered it")
	}
}

func TestChatQuestionKeys(t *testing.T) {
	m := NewChat(Options{Width: 120}, nil)
	m.Running = true
	reply := make(chan QuestionResult, 1)
	qs := []core.UserQuestion{{
		Question: "Ship it?",
		Options:  []core.UserOption{{Label: "yes", Description: "go"}, {Label: "no", Description: "stop"}},
	}}
	next, _ := m.Update(QuestionMsg{Questions: qs, Reply: reply})
	m = next.(ChatModel)
	if m.question == nil {
		t.Fatal("question not pending")
	}
	if !strings.Contains(m.question.view(false, func(s string) string { return s }, func(s string) string { return s }), "Ship it?") {
		t.Fatal("composer missing question")
	}
	if !strings.Contains(m.footerView(), "1–9") {
		t.Fatalf("footer %q", m.footerView())
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	m = next.(ChatModel)
	if m.question != nil {
		t.Fatal("digit should finish a single question")
	}
	r := <-reply
	if r.Stop || len(r.Answers) != 1 || r.Answers[0].Selected[0] != "yes" {
		t.Fatalf("result %+v", r)
	}

	reply2 := make(chan QuestionResult, 1)
	next, _ = m.Update(QuestionMsg{Questions: qs, Reply: reply2})
	m = next.(ChatModel)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(ChatModel)
	if r := <-reply2; !r.Stop {
		t.Fatalf("esc %+v", r)
	}
}

func TestAskerFromChan(t *testing.T) {
	ch := make(chan tea.Msg, 1)
	fn := askerFromChan(ch)
	go func() {
		msg := (<-ch).(QuestionMsg)
		msg.Reply <- QuestionResult{Answers: []core.UserAnswer{{Question: "Q", Selected: []string{"a"}}}}
	}()
	got, err := fn(t.Context(), []core.UserQuestion{{Question: "Q", Options: []core.UserOption{{Label: "a"}, {Label: "b"}}}})
	if err != nil || len(got) != 1 || got[0].Selected[0] != "a" {
		t.Fatalf("asker: %v %v", got, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := fn(ctx, []core.UserQuestion{{Question: "Q", Options: []core.UserOption{{Label: "a"}, {Label: "b"}}}}); err == nil {
		t.Fatal("cancelled ctx must error")
	}
}

func TestChanApprover(t *testing.T) {
	ch := make(chan tea.Msg, 1)
	fn := chanApprover(ch)
	go func() {
		msg := (<-ch).(ApprovalMsg)
		msg.Reply <- core.ApprovalResolution{Decision: core.ApprovalApproved, Scope: core.ApprovalOnce}
	}()
	r, err := fn(t.Context(), core.Approval{})
	if err != nil || r.Decision != core.ApprovalApproved {
		t.Fatalf("approver: %v %v", r, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := fn(ctx, core.Approval{}); err == nil {
		t.Fatal("cancelled ctx must error, not hang")
	}
}

func sizedChat(t *testing.T, runner Runner) ChatModel {
	t.Helper()
	m := NewChat(Options{Width: 80}, runner)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 16})
	m = next.(ChatModel)
	for i := 0; i < 40; i++ {
		m = m.append(fmt.Sprintf("[you] line-%02d", i))
	}
	if !m.vp.AtBottom() {
		t.Fatal("fresh fill must follow the tail")
	}
	if m.vp.YOffset == 0 {
		t.Fatal("expected overflow so the viewport can scroll up")
	}
	return m
}

func TestChatStickyScrollKeepsOffsetWhenScrolledUp(t *testing.T) {
	m := sizedChat(t, nil)
	bottom := m.vp.YOffset
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	m = next.(ChatModel)
	if m.vp.YOffset >= bottom {
		t.Fatalf("pgup did not scroll up: y=%d bottom=%d", m.vp.YOffset, bottom)
	}
	held := m.vp.YOffset
	m = m.append("[ink] streamed token")
	if m.vp.AtBottom() {
		t.Fatal("layout jumped to the tail after the user scrolled up")
	}
	if m.vp.YOffset != held {
		t.Fatalf("offset moved from %d to %d while the user was reading", held, m.vp.YOffset)
	}
}

func TestChatSubmitRefollowsTail(t *testing.T) {
	m := sizedChat(t, func(context.Context, string, runtime.Observer) (runtime.Result, error) {
		return okResult("ok"), nil
	})
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	m = next.(ChatModel)
	if m.vp.AtBottom() {
		t.Fatal("setup: expected a scrolled-up viewport")
	}
	m = runTurn(t, typeText(m, "hello"))
	if !m.vp.AtBottom() {
		t.Fatalf("sending a prompt must re-follow the tail: y=%d", m.vp.YOffset)
	}
}

func TestChatQueuesPromptWhileRunning(t *testing.T) {
	started := make(chan string, 4)
	block := make(chan struct{})
	runner := func(_ context.Context, prompt string, _ runtime.Observer) (runtime.Result, error) {
		started <- prompt
		<-block
		return okResult(prompt), nil
	}
	next, wait := typeText(NewChat(Options{Width: 80}, runner), "first").Update(enter())
	m := next.(ChatModel)
	if got := <-started; got != "first" {
		t.Fatalf("first turn started with %q", got)
	}

	m = typeText(m, "second")
	next, cmd := m.Update(enter())
	m = next.(ChatModel)
	if cmd != nil {
		t.Fatal("queueing must not start a second runner")
	}
	if !m.Running {
		t.Fatal("first turn must still be running")
	}
	if len(m.queue) != 1 || m.queue[0].text != "second" || m.queue[0].display != "second" {
		t.Fatalf("queue = %v, want [second]", m.queue)
	}
	if m.ta.Value() != "" {
		t.Fatalf("queued draft still in the prompt: %q", m.ta.Value())
	}

	close(block)
	next, cmd = m.Update(wait())
	m = next.(ChatModel)
	if cmd == nil {
		t.Fatal("queued prompt must start a turn after the first ends")
	}
	if got := <-started; got != "second" {
		t.Fatalf("queued turn started with %q, want second", got)
	}
	next, _ = m.Update(cmd())
	m = next.(ChatModel)
	if m.Running {
		t.Fatal("queued turn did not finish")
	}
	joined := strings.Join(m.Lines, "\n")
	if !strings.Contains(joined, "[you] first") || !strings.Contains(joined, "[you] second") {
		t.Fatalf("scrollback missing queued prompts:\n%s", joined)
	}
}

func TestChatEscDropsQueuedPrompt(t *testing.T) {
	m := NewChat(Options{Width: 80}, nil)
	m.queue = []queuedPrompt{{text: "held", display: "held"}}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(ChatModel)
	if cmd != nil {
		t.Fatal("esc dropping a queued item must not quit")
	}
	if len(m.queue) != 0 {
		t.Fatalf("esc left queue %v", m.queue)
	}
}

func TestChatQueuePane(t *testing.T) {
	m := NewChat(Options{Width: 80, NoColor: true}, nil)
	m.queue = []queuedPrompt{
		{text: "first queued prompt", display: "first queued prompt"},
		{text: "second queued prompt", display: "second queued prompt"},
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlB})
	m = next.(ChatModel)
	if !m.queueOpen {
		t.Fatal("ctrl+b should expand the queue strip")
	}
	for _, want := range []string{"Next prompt", "first queued prompt", "second queued prompt"} {
		if !strings.Contains(m.View(), want) {
			t.Fatalf("queue strip missing %q:\n%s", want, m.View())
		}
	}
	if strings.Index(m.View(), "Next prompt") > strings.Index(m.View(), "╭") {
		t.Fatalf("queue should render above the composer:\n%s", m.View())
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("draft")})
	m = next.(ChatModel)
	if m.ta.Value() != "draft" {
		t.Fatalf("queue overlay blocked typing: %q", m.ta.Value())
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if next.(ChatModel).queueOpen {
		t.Fatal("esc should close the queue pane")
	}
}

func TestChatQueuePaneEmptyDoesNotPanic(t *testing.T) {
	m := NewChat(Options{Width: 80, NoColor: true}, nil)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlB})
	m = next.(ChatModel)
	if !m.queueOpen {
		t.Fatal("ctrl+b should open the empty queue strip")
	}
	v := m.View()
	if !strings.Contains(v, "Next prompt  empty") {
		t.Fatalf("empty queue strip missing:\n%s", v)
	}
}

func TestChatShortPaneSitsOnComposer(t *testing.T) {
	m := runTurn(t, typeText(NewChat(Options{Width: 80, NoColor: true},
		func(context.Context, string, runtime.Observer) (runtime.Result, error) {
			return okResult("short reply"), nil
		}), "hi"))
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	assertPaneSitsOnComposer(t, next.(ChatModel).View())
}

// TestChatHomeIsCentered pins the idle layout: the welcome block and the
// composer under it are one group in the middle of the screen, with about as
// much blank space above the group as below it.
func TestChatHomeIsCentered(t *testing.T) {
	m := NewChat(Options{Width: 100, NoColor: true}, nil)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	rows := strings.Split(next.(ChatModel).View(), "\n")
	if len(rows) != 40 {
		t.Fatalf("frame is %d rows, want 40", len(rows))
	}

	// Row 0 is the header bar; the group runs from the wordmark to the hint.
	first, last := -1, -1
	for i, l := range rows[1:] {
		if strings.TrimSpace(l) != "" {
			if first < 0 {
				first = i + 1
			}
			last = i + 1
		}
	}
	if first < 0 {
		t.Fatalf("home frame is empty:\n%s", strings.Join(rows, "\n"))
	}
	above, below := first-1, len(rows)-1-last
	if above == 0 || below == 0 || above-below > 1 || below-above > 1 {
		t.Fatalf("home group not centered: %d rows above, %d below:\n%s",
			above, below, strings.Join(rows, "\n"))
	}
	if !strings.Contains(rows[last], "enter send") {
		t.Fatalf("the composer hint should close the group, got %q", rows[last])
	}
}

func assertPaneSitsOnComposer(t *testing.T, v string) {
	t.Helper()
	lines := strings.Split(v, "\n")
	rule := -1
	for i := len(lines) - 1; i >= 0; i-- {
		l := lines[i]
		if strings.Contains(l, "╭") {
			rule = i
			break
		}
	}
	if rule < 1 {
		t.Fatalf("missing composer box:\n%s", v)
	}
	if strings.TrimSpace(lines[rule-1]) == "" {
		t.Fatalf("blank row above composer copies as an empty line:\n%s", v)
	}
}

func TestChatHeaderFolderBranchContext(t *testing.T) {
	m := NewChat(Options{
		Width: 80, NoColor: true, Folder: "/tmp/work/my-repo", Branch: "main", Dirty: true,
		ContextWindow: 128000,
	}, nil)
	m.ctxUsed = 1200
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	v := next.(ChatModel).View()
	if !strings.Contains(v, "my-repo") || !strings.Contains(v, "main*") {
		t.Fatalf("header missing folder/branch:\n%s", v)
	}
	if !strings.Contains(v, "1.2k") || !strings.Contains(v, "128k") {
		t.Fatalf("header missing context:\n%s", v)
	}
}

func TestDisplayCwdKeepsPath(t *testing.T) {
	if got := displayCwd(""); got != "ink" {
		t.Fatalf("empty = %q", got)
	}
	got := displayCwd("/tmp/work/studio/projects/ink")
	if !strings.Contains(got, "ink") || got == "ink" {
		t.Fatalf("long cwd collapsed: %q", got)
	}
}

func TestSlashOffersExit(t *testing.T) {
	m := typeText(NewChat(Options{Width: 80, NoColor: true}, nil), "/ex")
	menu := m.slashMatches()
	found := false
	for _, e := range menu {
		if e.name == "exit" {
			found = true
		}
	}
	if !found {
		t.Fatalf("slash menu missing /exit: %+v", menu)
	}
}

func TestComposerLabelSitsOnTheRight(t *testing.T) {
	m := NewChat(Options{Width: 80, NoColor: true, Route: "fw/deepseek-v4-flash", Mode: "code"}, nil)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	v := next.(ChatModel).View()
	var bot string
	for _, l := range strings.Split(v, "\n") {
		if strings.Contains(l, "╰") {
			bot = strings.TrimSpace(l)
			break
		}
	}
	if bot == "" {
		t.Fatalf("no composer bottom:\n%s", v)
	}
	if !strings.HasPrefix(strings.TrimLeft(bot, " "), "╰─") && !strings.HasPrefix(bot, "╰") {
		t.Fatalf("bottom lost its corner: %q", bot)
	}
	iDash := strings.Index(bot, "╰")
	iModel := strings.Index(bot, "deepseek")
	iEnd := strings.LastIndex(bot, "╯")
	if iModel < 0 || iEnd < 0 || iDash < 0 {
		t.Fatalf("bottom missing pieces: %q", bot)
	}
	if iDash >= iModel || iModel >= iEnd {
		t.Fatalf("model should sit on the right of the box line: %q", bot)
	}
	left := bot[iDash+len("╰") : iModel]
	if !strings.Contains(left, "─") {
		t.Fatalf("left of the model should be the line, not the label: %q", bot)
	}
}

func TestChatComposerIsBoxed(t *testing.T) {
	m := NewChat(Options{Width: 80, NoColor: true}, nil)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	v := next.(ChatModel).View()
	if !strings.Contains(v, "╭") || !strings.Contains(v, "╰") || !strings.Contains(v, "│") {
		t.Fatalf("composer must be a rounded box:\n%s", v)
	}
	if !strings.Contains(v, ">") {
		t.Fatalf("composer missing prompt marker:\n%s", v)
	}
}

func TestInkMarkGeometry(t *testing.T) {
	for _, mark := range []string{inkMarkLarge, inkMarkSmall} {
		lines := strings.Split(strings.TrimSuffix(mark, "\n"), "\n")
		if len(lines) < 4 {
			t.Fatalf("mark too short:\n%s", mark)
		}
		var left, right []int
		for _, l := range lines {
			i, j := -1, -1
			for k, r := range []rune(l) {
				if r != ' ' {
					if i < 0 {
						i = k
					}
					j = k
				}
			}
			if i < 0 {
				t.Fatalf("blank line in mark:\n%s", mark)
			}
			left = append(left, i)
			right = append(right, j)
		}
		if right[0]-left[0] != 1 {
			t.Fatalf("tip should be two cells, got %d-%d\n%s", left[0], right[0], mark)
		}
		tip := left[0] + right[0]
		bot := left[len(left)-1] + right[len(right)-1]
		if tip != bot {
			t.Fatalf("tip center %d != bottom center %d\n%s", tip, bot, mark)
		}
		widening := true
		for i := 1; i < len(left); i++ {
			span, prev := right[i]-left[i], right[i-1]-left[i-1]
			if widening {
				if span < prev {
					widening = false
				}
				continue
			}
			if span > prev {
				t.Fatalf("mark is not a drop (widens after taper at line %d)\n%s", i+1, mark)
			}
		}
		if widening {
			t.Fatalf("mark never tapers:\n%s", mark)
		}
	}
}

func TestChatWelcomeEmptyState(t *testing.T) {
	m := NewChat(Options{Width: 80, Route: "fireworks/deepseek-v4-flash"}, nil)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(ChatModel)
	v := m.View()
	for _, want := range []string{"╱╲", "Ink", "Ask anything", "what does this repo do?", "deepseek-v4-flash"} {
		if !strings.Contains(v, want) {
			t.Fatalf("empty chat missing %q:\n%s", want, v)
		}
	}
	for _, dead := range []string{"◉", "╱_____╱"} {
		if strings.Contains(v, dead) {
			t.Fatalf("welcome still shows the Friday mark %q:\n%s", dead, v)
		}
	}
	if strings.Contains(v, "[you]") || strings.Contains(v, "[ink]") {
		t.Fatalf("welcome painted log tags:\n%s", v)
	}

	m = runTurn(t, typeText(NewChat(Options{Width: 80, Route: "fireworks/deepseek-v4-flash"},
		func(context.Context, string, runtime.Observer) (runtime.Result, error) {
			return okResult("hi"), nil
		}), "hello"))
	after := m.View()
	if strings.Contains(after, "Ask anything") {
		t.Fatalf("welcome stayed after the first turn:\n%s", after)
	}
}

func TestChatTimestampsPaintTranscriptRows(t *testing.T) {
	runner := func(context.Context, string, runtime.Observer) (runtime.Result, error) {
		return okResult("answer"), nil
	}
	m := NewChat(Options{Width: 80, NoColor: true}, runner)
	m.now = func() time.Time { return time.Date(2026, 8, 26, 9, 4, 0, 0, time.UTC) }
	m = runTurn(t, typeText(m, "hello"))
	m = slash(t, m, "/timestamps")
	view := m.View()
	if !strings.Contains(view, "09:04") {
		t.Fatalf("timestamps toggle did not paint transcript rows:\n%s", view)
	}
}

func TestChatFooterRewritesByState(t *testing.T) {
	m := NewChat(Options{Width: 80, NoColor: true}, nil)
	idle := m.footerView()
	for _, want := range []string{"enter send", "palette", "quit"} {
		if !strings.Contains(idle, want) {
			t.Fatalf("idle footer missing %q: %s", want, idle)
		}
	}
	if strings.Contains(idle, "cancel") {
		t.Fatalf("idle footer showed working keys: %s", idle)
	}

	m.Running = true
	work := m.footerView()
	if !strings.Contains(work, "esc/ctrl+c cancel") {
		t.Fatalf("working footer missing cancel: %s", work)
	}
	if strings.Contains(work, "enter send") {
		t.Fatalf("working footer kept idle keys: %s", work)
	}

	m.queue = []queuedPrompt{{text: "held", display: "held"}}
	if q := m.footerView(); !strings.Contains(q, "1 queued") {
		t.Fatalf("working footer missing queue count: %s", q)
	}

	m.Running, m.queue = false, nil
	m.pending = &core.Approval{Request: core.CapabilityRequest{Tool: "write_file"}}
	perm := m.footerView()
	for _, want := range []string{"y once", "s session", "n reject"} {
		if !strings.Contains(perm, want) {
			t.Fatalf("permission footer missing %q: %s", want, perm)
		}
	}
	if strings.Contains(perm, "enter send") {
		t.Fatalf("permission footer kept idle keys: %s", perm)
	}

	m.pending = nil
	ov := overlay{kind: overlayPalette, title: "Commands", items: chatActions()}
	m.ov = &ov
	ovf := m.footerView()
	for _, want := range []string{"enter selects", "esc closes"} {
		if !strings.Contains(ovf, want) {
			t.Fatalf("overlay footer missing %q: %s", want, ovf)
		}
	}
}

func TestChatUsageToggleShowsContextNearModel(t *testing.T) {
	limit := core.USDMicros(1_000_000)
	actual := core.USDMicros(250_000)
	m := NewChat(Options{
		Width: 140, NoColor: true, Route: "fw/deepseek-v4-flash",
		ContextWindow: 128000, Budget: core.TaskBudget{MaxCost: limit},
		UsageLimits: "session cap $2.00 day cap $5.00",
	}, nil)
	m.ctxUsed = 64000
	m.Cost = core.CostReport{Actual: &actual}
	m = slash(t, m, "/usage")
	v := m.View()
	for _, want := range []string{"ctx 50%", "cost $0.250000", "task cap $1.00", "configured caps session cap $2.00 day cap $5.00"} {
		if !strings.Contains(v, want) {
			t.Fatalf("usage label missing %q:\n%s", want, v)
		}
	}
}

func TestChatMouseWheelScrollsTranscript(t *testing.T) {
	m := sizedChat(t, nil)
	start := m.vp.YOffset
	next, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	m = next.(ChatModel)
	if m.vp.YOffset >= start {
		t.Fatalf("wheel up did not scroll up: before=%d after=%d", start, m.vp.YOffset)
	}
	if m.followTail {
		t.Fatal("wheel up must release sticky tail")
	}
}

func TestChatMouseDragCopiesFramedMessageBody(t *testing.T) {
	var copied string
	m := NewChat(Options{Width: 90, NoColor: true, Copy: func(s string) error {
		copied = s
		return nil
	}}, nil)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 90, Height: 18})
	m = next.(ChatModel)
	m = m.append(
		tagReply+" Hi! I'm Ink.",
		tagReply+" ",
		tagReply+" - Build and test",
	)
	rows := strings.Split(m.paneView(), "\n")
	start, end := rowContaining(t, rows, "Hi!"), rowContaining(t, rows, "Build and test")
	endCol := colAfter(t, rows[end], "Build and test")

	next, _ = m.Update(tea.MouseMsg{
		Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 4, Y: chatChrome + start,
	})
	m = next.(ChatModel)
	next, _ = m.Update(tea.MouseMsg{
		Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: framePad + endCol, Y: chatChrome + end,
	})
	m = next.(ChatModel)
	next, _ = m.Update(tea.MouseMsg{
		Action: tea.MouseActionRelease, Button: tea.MouseButtonNone, X: framePad + endCol, Y: chatChrome + end,
	})
	m = next.(ChatModel)

	want := "Hi! I'm Ink.\n\n• Build and test"
	if copied != want {
		t.Fatalf("mouse copied = %q, want %q\npane:\n%s", copied, want, m.paneView())
	}
	for _, bad := range []string{"│", "╭", "╰"} {
		if strings.Contains(copied, bad) {
			t.Fatalf("mouse copy leaked frame glyph %q: %q", bad, copied)
		}
	}
}

func TestChatMouseDragCopiesPartialWordRange(t *testing.T) {
	var copied string
	m := NewChat(Options{Width: 90, NoColor: true, Copy: func(s string) error {
		copied = s
		return nil
	}}, nil)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 90, Height: 18})
	m = next.(ChatModel)
	m = m.append(tagReply + " copy just this word")
	rows := strings.Split(m.paneView(), "\n")
	row := rowContaining(t, rows, "copy just this word")
	startCol := colAt(t, rows[row], "just")
	endCol := colAfter(t, rows[row], "just")

	next, _ = m.Update(tea.MouseMsg{
		Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: framePad + startCol, Y: chatChrome + row,
	})
	m = next.(ChatModel)
	next, _ = m.Update(tea.MouseMsg{
		Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: framePad + endCol, Y: chatChrome + row,
	})
	m = next.(ChatModel)
	next, _ = m.Update(tea.MouseMsg{
		Action: tea.MouseActionRelease, Button: tea.MouseButtonNone, X: framePad + endCol, Y: chatChrome + row,
	})
	_ = next

	if copied != "just" {
		t.Fatalf("partial mouse copy = %q, want just", copied)
	}
}

func TestChatMouseDragHighlightsSelection(t *testing.T) {
	m := NewChat(Options{Width: 90}, nil)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 90, Height: 18})
	m = next.(ChatModel)
	m = m.append(
		tagReply+" first line",
		tagReply+" second line",
	)
	rows := strings.Split(m.paneView(), "\n")
	start, end := rowContaining(t, rows, "first line"), rowContaining(t, rows, "second line")
	endCol := colAfter(t, rows[end], "second line")
	before := m.View()

	next, _ = m.Update(tea.MouseMsg{
		Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 4, Y: chatChrome + start,
	})
	m = next.(ChatModel)
	next, _ = m.Update(tea.MouseMsg{
		Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: framePad + endCol, Y: chatChrome + end,
	})
	m = next.(ChatModel)
	after := m.View()

	if before == after {
		t.Fatal("dragging did not change the rendered view")
	}
	if !strings.Contains(after, "\x1b[7m") {
		t.Fatalf("drag selection should use reverse-video highlight:\n%q", after)
	}
}

func TestChatMouseClickDoesNotCopy(t *testing.T) {
	var copied string
	m := NewChat(Options{Width: 90, NoColor: true, Copy: func(s string) error {
		copied = s
		return nil
	}}, nil)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 90, Height: 18})
	m = next.(ChatModel)
	m = m.append(tagReply + " Hi! I'm Ink.")
	row := rowContaining(t, strings.Split(m.paneView(), "\n"), "Hi!")

	next, _ = m.Update(tea.MouseMsg{
		Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 4, Y: chatChrome + row,
	})
	m = next.(ChatModel)
	_, _ = m.Update(tea.MouseMsg{
		Action: tea.MouseActionRelease, Button: tea.MouseButtonNone, X: 4, Y: chatChrome + row,
	})

	if copied != "" {
		t.Fatalf("plain click copied %q", copied)
	}
}

func TestChatPasteMarkerExpandsOnSubmit(t *testing.T) {
	got := make(chan string, 1)
	runner := func(_ context.Context, prompt string, _ runtime.Observer) (runtime.Result, error) {
		got <- prompt
		return okResult("ok"), nil
	}
	m := NewChat(Options{Width: 100, NoColor: true}, runner)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("alpha\nbeta\ngamma"), Paste: true})
	m = next.(ChatModel)
	if m.ta.Value() != "[Pasted +3 lines]" {
		t.Fatalf("paste marker = %q", m.ta.Value())
	}
	m = runTurn(t, m)
	if prompt := <-got; prompt != "alpha\nbeta\ngamma" {
		t.Fatalf("runner prompt = %q", prompt)
	}
	if joined := strings.Join(m.Lines, "\n"); !strings.Contains(joined, "[Pasted +3 lines]") {
		t.Fatalf("scrollback should keep compact paste marker:\n%s", joined)
	}
}

func rowContaining(t *testing.T, rows []string, needle string) int {
	t.Helper()
	for i, row := range rows {
		if strings.Contains(row, needle) {
			return i
		}
	}
	t.Fatalf("row containing %q not found in:\n%s", needle, strings.Join(rows, "\n"))
	return -1
}

func colAfter(t *testing.T, row, needle string) int {
	t.Helper()
	plain := stripANSI(row)
	idx := strings.Index(plain, needle)
	if idx < 0 {
		t.Fatalf("column containing %q not found in %q", needle, plain)
	}
	return len([]rune(plain[:idx])) + len([]rune(needle)) - 1
}

func colAt(t *testing.T, row, needle string) int {
	t.Helper()
	plain := stripANSI(row)
	idx := strings.Index(plain, needle)
	if idx < 0 {
		t.Fatalf("column containing %q not found in %q", needle, plain)
	}
	return len([]rune(plain[:idx]))
}

func TestChatApprovalRendersInComposer(t *testing.T) {
	m := NewChat(Options{Width: 80, NoColor: true}, nil)
	m.Running = true
	a := core.Approval{ID: core.NewApprovalID(), Request: core.CapabilityRequest{Tool: "write_file"}}
	next, _ := m.Update(ApprovalMsg{A: a, Reply: make(chan core.ApprovalResolution, 1)})
	v := next.(ChatModel).View()
	for _, want := range []string{"Allow once", "Allow this session", "Reject", "write_file"} {
		if !strings.Contains(v, want) {
			t.Fatalf("composer missing %q:\n%s", want, v)
		}
	}
}

func TestChatToolCardsFoldOutput(t *testing.T) {
	payload := strings.Repeat("Z", 240)
	runner := func(_ context.Context, _ string, obs runtime.Observer) (runtime.Result, error) {
		obs.OnEvent(core.NewEvent(core.NewTaskID(), core.NewRunID(), 1, time.Unix(0, 0).UTC(),
			core.ToolCalled{Tool: "read_file", Risk: core.RiskReadOnly, InputSummary: "main.go"}))
		obs.OnEvent(core.NewEvent(core.NewTaskID(), core.NewRunID(), 2, time.Unix(0, 0).UTC(),
			core.ToolCompleted{Tool: "read_file", Success: true, Elapsed: 3 * time.Millisecond, OutputSummary: payload}))
		return okResult("done"), nil
	}
	m := runTurn(t, typeText(NewChat(Options{Width: 80, NoColor: true}, runner), "go"))
	v := m.View()
	for _, want := range []string{"read_file", "3ms"} {
		if !strings.Contains(v, want) {
			t.Fatalf("tool card missing %q:\n%s", want, v)
		}
	}
	if strings.Count(v, "Z") >= len(payload) {
		t.Fatal("collapsed card dumped the full tool output")
	}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
	m = next.(ChatModel)
	open := m.View()
	if strings.Count(open, "Z") < len(payload) {
		t.Fatalf("expanded card missing full output:\n%s", open)
	}
}

func TestChatShiftTabCyclesModes(t *testing.T) {
	var got []string
	m := NewChat(Options{Width: 80, NoColor: true, SetMode: func(name string) error {
		got = append(got, name)
		return nil
	}}, nil)
	if m.mode != "code" || m.permLabel() != "normal" {
		t.Fatalf("default mode=%q label=%q", m.mode, m.permLabel())
	}
	if !strings.Contains(m.View(), "normal") {
		t.Fatalf("composer missing default mode:\n%s", m.View())
	}
	want := []struct{ label, mode string }{
		{"plan", "plan"},
		{"auto", "code"},
		{"always-approve", "code"},
		{"always-ask", "ask"},
		{"normal", "code"},
	}
	for _, w := range want {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
		m = next.(ChatModel)
		if m.permLabel() != w.label || m.mode != w.mode {
			t.Fatalf("shift+tab: label=%q mode=%q, want %q/%q", m.permLabel(), m.mode, w.label, w.mode)
		}
		if !strings.Contains(m.View(), w.label) {
			t.Fatalf("composer missing %q:\n%s", w.label, m.View())
		}
	}
	if strings.Join(got, ",") != "plan,code,ask,code" {
		t.Fatalf("SetMode calls = %v", got)
	}
	m.Running = true
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	if next.(ChatModel).permLabel() != "normal" {
		t.Fatal("shift+tab must not cycle mid-turn")
	}
}

func TestChatTabFocusesScrollback(t *testing.T) {
	m := runTurn(t, typeText(NewChat(Options{Width: 80, NoColor: true},
		func(context.Context, string, runtime.Observer) (runtime.Result, error) {
			return okResult("reply"), nil
		}), "hello"))
	if !m.promptFocus {
		t.Fatal("chat must start on the prompt")
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(ChatModel)
	if m.promptFocus {
		t.Fatal("tab must move focus to the scrollback")
	}
	if !strings.Contains(m.footerView(), "tab/space prompt") {
		t.Fatalf("scrollback footer missing tab hint: %s", m.footerView())
	}
	before := m.sel
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = next.(ChatModel)
	if m.sel == before && len(m.Lines) > 1 {
		t.Fatal("up must move the selected entry")
	}
	if strings.Contains(m.View(), "▶") == false {
		t.Fatalf("selected entry has no marker:\n%s", m.View())
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(ChatModel)
	if !m.promptFocus {
		t.Fatal("tab must return focus to the prompt")
	}
}
