package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ataidesorg/friday/internal/core"
	"github.com/ataidesorg/friday/internal/runtime"
)

func TestChatGoalStartPauseResumeEditClear(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	var saved *core.Goal
	m := NewChat(Options{
		Width: 120,
		SetGoal: func(g core.Goal) error {
			cp := g
			saved = &cp
			return nil
		},
		ClearGoal: func() error { saved = nil; return nil },
	}, nil)
	m.now = func() time.Time { return now }

	m = slash(t, m, "/goal")
	if !strings.Contains(strings.Join(m.Lines, "\n"), "no goal") {
		t.Fatalf("empty status:\n%s", strings.Join(m.Lines, "\n"))
	}

	m = slash(t, m, "/goal start --tokens 100k ship the tests")
	if m.goal == nil || m.goal.Status != core.GoalActive || m.goal.Objective != "ship the tests" || m.goal.TokenBudget != 100_000 {
		t.Fatalf("start: %+v", m.goal)
	}
	if saved == nil || saved.ID != m.goal.ID {
		t.Fatal("start did not persist")
	}
	if !strings.Contains(strings.Join(m.Lines, "\n"), "goal active") {
		t.Fatalf("start status:\n%s", strings.Join(m.Lines, "\n"))
	}

	usage := core.Usage{InputTokens: 9}
	m.goal.Usage = usage
	m.goal.AutomaticTurns = 3
	m = slash(t, m, "/goal edit keep going")
	if m.goal.Objective != "keep going" || m.goal.Usage != usage || m.goal.AutomaticTurns != 3 {
		t.Fatalf("edit mutated counters: %+v", m.goal)
	}

	blocked := slash(t, m, "/goal ship something else")
	if !strings.Contains(strings.Join(blocked.Lines, "\n"), "already") {
		t.Fatalf("replace without clear:\n%s", strings.Join(blocked.Lines, "\n"))
	}

	m = slash(t, m, "/goal pause")
	if m.goal.Status != core.GoalPaused || m.goal.PauseCause != core.GoalCauseUser || m.goal.Continues() {
		t.Fatalf("pause: %+v", m.goal)
	}

	m = slash(t, m, "/goal resume")
	if m.goal.Status != core.GoalActive || m.goal.PauseCause != "" {
		t.Fatalf("resume: %+v", m.goal)
	}

	m = slash(t, m, "/goal clear")
	if m.goal != nil || saved != nil {
		t.Fatalf("clear left %+v saved=%v", m.goal, saved)
	}
	if !strings.Contains(strings.Join(m.Lines, "\n"), "goal cleared") {
		t.Fatalf("clear:\n%s", strings.Join(m.Lines, "\n"))
	}
}

func TestChatGoalDoesNotInventCompletion(t *testing.T) {
	g, err := core.NewGoal("prove it", time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	m := NewChat(Options{Width: 120}, func(context.Context, string, runtime.Observer) (runtime.Result, error) {
		return runtime.Result{
			Outcome: core.Outcome{Kind: core.OutcomeCompletedUnverified},
			Summary: "All done.",
			Goal:    &g,
		}, nil
	})
	m.goal = &g
	m = runTurn(t, typeText(m, "please finish"))
	if m.goal == nil || m.goal.Status != core.GoalActive {
		t.Fatalf("prose done completed the goal: %+v", m.goal)
	}
}

func TestChatGoalAutoContinuesWhenActive(t *testing.T) {
	g, err := core.NewGoal("keep going", time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	var prompts []string
	n := 0
	m := NewChat(Options{Width: 120}, func(_ context.Context, prompt string, _ runtime.Observer) (runtime.Result, error) {
		prompts = append(prompts, prompt)
		n++
		cp := g
		if n == 1 {
			return runtime.Result{Summary: "working", Goal: &cp, ContinueGoal: true}, nil
		}
		cp.Status = core.GoalPaused
		cp.PauseCause = core.GoalCauseUser
		return runtime.Result{Summary: "paused", Goal: &cp, ContinueGoal: false}, nil
	})
	m = runTurn(t, typeText(m, "start the work"))
	if len(prompts) != 2 {
		t.Fatalf("want 2 turns, got %v", prompts)
	}
	if prompts[1] != core.GoalContinuePrompt {
		t.Fatalf("continue prompt: %q", prompts[1])
	}
	if m.goal == nil || m.goal.Status != core.GoalPaused {
		t.Fatalf("second turn: %+v", m.goal)
	}
}

func TestChatAdvisoriesToggle(t *testing.T) {
	m := NewChat(Options{Width: 80, NoColor: true}, nil)
	if m.hideAdvis {
		t.Fatal("advisories show by default")
	}
	m = slash(t, m, "/advisories")
	if !m.hideAdvis {
		t.Fatal("/advisories did not hide")
	}
	if !strings.Contains(strings.Join(m.Lines, "\n"), "advisories off") {
		t.Fatalf("toggle status:\n%s", strings.Join(m.Lines, "\n"))
	}
}

func TestChatGoalPausedDoesNotAutoContinue(t *testing.T) {
	g, err := core.NewGoal("wait", time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	g.Status = core.GoalPaused
	g.PauseCause = core.GoalCauseUser
	calls := 0
	m := NewChat(Options{Width: 120}, func(context.Context, string, runtime.Observer) (runtime.Result, error) {
		calls++
		cp := g
		return runtime.Result{Summary: "idle", Goal: &cp, ContinueGoal: false}, nil
	})
	m = runTurn(t, typeText(m, "hello"))
	if calls != 1 {
		t.Fatalf("paused goal auto-continued: calls=%d", calls)
	}
	if m.goal == nil || m.goal.Status != core.GoalPaused {
		t.Fatalf("paused goal mutated: %+v", m.goal)
	}
}
