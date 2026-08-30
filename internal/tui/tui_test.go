package tui_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ataidesorg/ink/internal/core"
	"github.com/ataidesorg/ink/internal/runtime"
	"github.com/ataidesorg/ink/internal/tui"
)

var fixedNow = time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)

func event(t *testing.T, seq uint64, data core.EventData) core.Event {
	t.Helper()
	return core.NewEvent(core.NewTaskID(), core.NewRunID(), seq, fixedNow, data)
}

func key(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }

func update(t *testing.T, m tui.Model, msg tea.Msg) (tui.Model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(msg)
	got, ok := next.(tui.Model)
	if !ok {
		t.Fatalf("Update returned %T", next)
	}
	return got, cmd
}

func approval() core.Approval {
	return core.Approval{
		ID: core.NewApprovalID(), Task: core.NewTaskID(), Run: core.NewRunID(), RequestedAt: fixedNow,
		Request: core.CapabilityRequest{Call: core.NewToolCallID(), Tool: "write_file", Capability: core.Capability{Risk: core.RiskWriteLocal, Scope: core.ResourceScope{Kind: core.ScopePath, Path: "farewell.go"}}},
	}
}

func result(kind core.OutcomeKind) runtime.Result {
	cost := core.USDMicros(1234)
	return runtime.Result{
		Outcome: core.Outcome{Kind: kind, Reason: "validation: go test exit 0"},
		Usage:   core.Usage{InputTokens: 1200, OutputTokens: 300},
		Cost:    core.CostReport{Actual: &cost},
		Summary: "Added Farewell(name) with a test.",
	}
}

func TestDenyLineIsTagged(t *testing.T) {
	m := tui.NewModel(tui.Options{Width: 200})
	m2, _ := update(t, m, tui.EventMsg{E: event(t, 1, core.PolicyDecided{Tool: "run_command", Risk: core.RiskExecuteLocal, Effect: core.EffectDeny, Rule: "tools.commands.allowed", Reason: "rm is not allowed"})})
	if len(m.Lines) != 0 {
		t.Fatalf("Update mutated the receiver: %v", m.Lines)
	}
	if len(m2.Lines) != 1 || !strings.HasPrefix(m2.Lines[0], "[policy] deny run_command") || !strings.Contains(m2.Lines[0], "rm is not allowed") {
		t.Fatalf("lines = %q", m2.Lines)
	}
	if !strings.Contains(m2.View(), m2.Lines[0]) {
		t.Fatalf("view lacks the deny line:\n%s", m2.View())
	}
}

func TestApprovalKeys(t *testing.T) {
	cases := []struct {
		key      rune
		decision core.ApprovalDecision
		scope    core.ApprovalScope
	}{{'y', core.ApprovalApproved, core.ApprovalOnce}, {'s', core.ApprovalApproved, core.ApprovalSession}, {'n', core.ApprovalDenied, core.ApprovalOnce}}
	for _, c := range cases {
		reply := make(chan core.ApprovalResolution, 1)
		m, _ := update(t, tui.NewModel(tui.Options{}), tui.ApprovalMsg{A: approval(), Reply: reply})
		if m.Pending == nil || !strings.Contains(m.View(), "[y] once") || !strings.Contains(m.View(), "farewell.go") {
			t.Fatalf("%c: pending=%v view=%q", c.key, m.Pending, m.View())
		}
		m, _ = update(t, m, key('x'))
		if m.Pending == nil || len(reply) != 0 {
			t.Fatalf("%c: unknown key resolved the approval", c.key)
		}
		m, _ = update(t, m, key(c.key))
		if m.Pending != nil {
			t.Fatalf("%c: pending not cleared", c.key)
		}
		select {
		case r := <-reply:
			if r.Decision != c.decision || r.Scope != c.scope || r.By != (core.Principal{Kind: core.PrincipalUser, Name: "owner"}) || r.At.IsZero() {
				t.Fatalf("%c: %+v", c.key, r)
			}
		default:
			t.Fatalf("%c: no reply", c.key)
		}
	}
}

func TestCtrlCDeniesAndQuits(t *testing.T) {
	reply := make(chan core.ApprovalResolution, 1)
	m, _ := update(t, tui.NewModel(tui.Options{}), tui.ApprovalMsg{A: approval(), Reply: reply})
	m, cmd := update(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if m.Pending != nil || cmd == nil {
		t.Fatalf("pending=%v cmd=%v", m.Pending, cmd)
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("ctrl+c did not quit")
	}
	if r := <-reply; r.Decision != core.ApprovalDenied {
		t.Fatalf("ctrl+c resolution %+v", r)
	}
}

func TestDoneShowsOutcomeAndCost(t *testing.T) {
	m := tui.NewModel(tui.Options{Budget: core.TaskBudget{MaxCost: 1_000_000, MaxToolCalls: 50}})
	m, _ = update(t, m, tui.EventMsg{E: event(t, 1, core.ToolCalled{Tool: "read_file", Risk: core.RiskReadOnly, InputSummary: `{"path":"go.mod"}`})})
	m, cmd := update(t, m, tui.DoneMsg{Result: result(core.OutcomeCompletedVerified), Diff: "+func Farewell()"})
	if !m.Done || m.Outcome == nil || m.Outcome.Kind != core.OutcomeCompletedVerified || m.Diff == "" {
		t.Fatalf("model after done: %+v", m)
	}
	if cmd == nil {
		t.Fatal("done did not quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("done did not quit")
	}
	v := m.View()
	for _, want := range []string{"completed_verified", "$0.001234", "$1.000000", "tools 1/50", "in 1200", "out 300", "+func Farewell()", "Added Farewell(name) with a test."} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}
}

func TestResizeKeepsViewWithinWidth(t *testing.T) {
	m := tui.NewModel(tui.Options{Width: 120})
	long := strings.Repeat("x", 300)
	m, _ = update(t, m, tui.EventMsg{E: event(t, 1, core.Warning{Message: long})})
	m, _ = update(t, m, tui.DeltaMsg(long))
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 40, Height: 12})
	for _, line := range strings.Split(m.View(), "\n") {
		if w := lipgloss.Width(line); w > 40 {
			t.Errorf("line width %d > 40: %q", w, line)
		}
	}
	if n := strings.Count(m.View(), "\n"); n > 12 {
		t.Errorf("view has %d lines for height 12", n+1)
	}
}

func TestLongToolArgumentsAreTruncated(t *testing.T) {
	m := tui.NewModel(tui.Options{})
	m, _ = update(t, m, tui.EventMsg{E: event(t, 1, core.ToolCalled{Tool: "write_file", Risk: core.RiskWriteLocal, InputSummary: strings.Repeat("a", 500)})})
	line := m.Lines[len(m.Lines)-1]
	if !strings.HasSuffix(line, "(… truncated)") || len([]rune(line)) > 260 {
		t.Fatalf("line not truncated: %d runes, %q", len([]rune(line)), line[:60])
	}
}

// drive pushes the same sequence into any observer/approver pair.
func drive(t *testing.T, obs runtime.Observer, approve runtime.ApprovalFunc) core.ApprovalResolution {
	t.Helper()
	obs.OnPhase(core.PhaseIntake)
	obs.OnEvent(event(t, 1, core.TaskCreated{Description: "add Farewell(name) with a test", Harness: core.HarnessCode}))
	obs.OnEvent(event(t, 2, core.ModelSelected{Route: "single", Provider: "mock", Model: "scripted", Reason: "single provider configured"}))
	obs.OnEvent(event(t, 3, core.ContextAssembled{BudgetTokens: 8000, UsedTokens: 120, Items: 2, Excluded: 1}))
	obs.OnPhase(core.PhaseToolExecution)
	obs.OnModelDelta("I will add the function.\nThen test it.")
	obs.OnEvent(event(t, 4, core.ToolCalled{Call: core.NewToolCallID(), Tool: "write_file", Risk: core.RiskWriteLocal, InputSummary: `{"path":"farewell.go"}`}))
	obs.OnEvent(event(t, 5, core.PolicyDecided{Tool: "write_file", Risk: core.RiskWriteLocal, Effect: core.EffectRequireApproval, Rule: "posture.strict", Reason: "strict posture"}))
	obs.OnEvent(event(t, 6, core.ApprovalRequested{Approval: core.NewApprovalID(), Tool: "write_file", Risk: core.RiskWriteLocal, Justification: "strict posture"}))
	res, err := approve(context.Background(), approval())
	if err != nil {
		t.Fatal(err)
	}
	obs.OnEvent(event(t, 7, core.ApprovalResolved{Decision: res.Decision, By: res.By, Scope: res.Scope}))
	obs.OnEvent(event(t, 8, core.ToolCompleted{Tool: "write_file", Success: true, Elapsed: 3 * time.Millisecond, OutputSummary: "wrote 42 bytes"}))
	obs.OnEvent(event(t, 9, core.ModelUsage{Provider: "mock", Model: "scripted", Usage: core.Usage{InputTokens: 1200, OutputTokens: 300}}))
	obs.OnEvent(event(t, 10, core.ValidationResult{Command: "go test ./...", Passed: true, ExitCode: 0, Summary: "ok"}))
	obs.OnEvent(event(t, 11, core.SandboxDestroyed{Sandbox: "sb-1"}))
	obs.OnEvent(event(t, 12, core.Warning{Message: "model returned no summary"}))
	obs.OnEvent(event(t, 13, core.MemoryCandidateEvent{Candidate: "c-1", Category: core.MemoryProject, Status: core.CandidatePending}))
	obs.OnEvent(event(t, 14, core.SandboxCreated{Sandbox: "sb-1", Provider: "process"}))
	obs.OnEvent(event(t, 15, core.StateChanged{Transition: core.TransitionAdvance}))
	obs.OnEvent(event(t, 16, core.TaskFinished{Outcome: core.Outcome{Kind: core.OutcomeCompletedVerified}}))
	return res
}

type modelObserver struct{ m *tui.Model }

func (o modelObserver) OnEvent(e core.Event)  { o.apply(tui.EventMsg{E: e}) }
func (o modelObserver) OnPhase(p core.Phase)  { o.apply(tui.PhaseMsg(p)) }
func (o modelObserver) OnModelDelta(s string) { o.apply(tui.DeltaMsg(s)) }
func (o modelObserver) apply(msg tea.Msg) {
	next, _ := o.m.Update(msg)
	*o.m = next.(tui.Model)
}

func TestPlainWritesTheSameLinesAsTheModel(t *testing.T) {
	model := tui.NewModel(tui.Options{})
	obs := modelObserver{m: &model}
	drive(t, obs, func(_ context.Context, a core.Approval) (core.ApprovalResolution, error) {
		reply := make(chan core.ApprovalResolution, 1)
		obs.apply(tui.ApprovalMsg{A: a, Reply: reply})
		obs.apply(key('s'))
		return <-reply, nil
	})
	obs.apply(tui.DoneMsg{Result: result(core.OutcomeCompletedVerified), Diff: "+func Farewell()\n-old"})

	var out strings.Builder
	p := tui.New(&out, strings.NewReader("s\n"), tui.Options{IsTTY: false})
	errc := make(chan error, 1)
	go func() { errc <- p.Start(context.Background()) }()
	res := drive(t, p.Observer(), p.Approver())
	if res.Decision != core.ApprovalApproved || res.Scope != core.ApprovalSession {
		t.Fatalf("plain approval: %+v", res)
	}
	p.Done(result(core.OutcomeCompletedVerified), "+func Farewell()\n-old")
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	want := strings.Join(model.Lines, "\n") + "\n"
	if out.String() != want {
		t.Fatalf("plain output differs from model lines:\n--- plain ---\n%s\n--- model ---\n%s", out.String(), want)
	}
	for _, s := range []string{"[phase] intake", "[model] I will add the function.", "[model] Then test it.", "[approval] write_file", "[validate] passed go test ./...", "[outcome] completed_verified", "[diff] +func Farewell()", "[warn] model returned no summary"} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("plain output lacks %q", s)
		}
	}
	if strings.Contains(out.String(), "[state]") || strings.Contains(out.String(), "task_finished") {
		t.Errorf("state transitions must not be printed: %s", out.String())
	}
}

func TestPlainDeniesOnEOFAndUnknownAnswer(t *testing.T) {
	for _, in := range []string{"", "maybe\n"} {
		var out strings.Builder
		p := tui.New(&out, strings.NewReader(in), tui.Options{})
		go func() { _ = p.Start(context.Background()) }()
		res, err := p.Approver()(context.Background(), approval())
		if err != nil || res.Decision != core.ApprovalDenied || res.Note == "" {
			t.Fatalf("input %q: %+v %v", in, res, err)
		}
		p.Done(result(core.OutcomeFailed), "")
	}
}

func TestPlainStartStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := tui.New(&strings.Builder{}, strings.NewReader(""), tui.Options{})
	if err := p.Start(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start = %v", err)
	}
	if _, err := p.Approver()(ctx, approval()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Approver = %v", err)
	}
}

func TestTeaProgramRunsToDone(t *testing.T) {
	var out strings.Builder
	p := tui.New(&out, strings.NewReader(""), tui.Options{IsTTY: true, NoColor: true, Width: 80})
	errc := make(chan error, 1)
	go func() { errc <- p.Start(context.Background()) }()
	p.Observer().OnPhase(core.PhaseIntake)
	p.Observer().OnEvent(event(t, 1, core.TaskCreated{Description: "add Farewell", Harness: core.HarnessCode}))
	p.Done(result(core.OutcomeCompletedVerified), "")
	select {
	case err := <-errc:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after Done")
	}
	if !strings.Contains(out.String(), "completed_verified") || !strings.Contains(out.String(), "add Farewell") {
		t.Fatalf("output: %q", out.String())
	}
	// After the program ended, observers and approvers must not block.
	p.Observer().OnPhase(core.PhaseToolExecution)
	if res, err := p.Approver()(context.Background(), approval()); err != nil || res.Decision != core.ApprovalDenied {
		t.Fatalf("approver after quit: %+v %v", res, err)
	}
}

func TestModelQuestionKeys(t *testing.T) {
	reply := make(chan tui.QuestionResult, 1)
	qs := []core.UserQuestion{{
		Question: "Ship?",
		Options:  []core.UserOption{{Label: "yes"}, {Label: "no"}},
	}}
	m, _ := update(t, tui.NewModel(tui.Options{}), tui.QuestionMsg{Questions: qs, Reply: reply})
	joined := strings.Join(m.Lines, "\n")
	if !strings.Contains(joined, "Ship?") {
		t.Fatalf("lines %q", joined)
	}
	m, _ = update(t, m, key('1'))
	r := <-reply
	if r.Stop || len(r.Answers) != 1 || r.Answers[0].Selected[0] != "yes" {
		t.Fatalf("result %+v", r)
	}
}

func TestTeaApproverHonoursContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := tui.New(&strings.Builder{}, strings.NewReader(""), tui.Options{IsTTY: true})
	if _, err := p.Approver()(ctx, approval()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Approver = %v", err)
	}
}
