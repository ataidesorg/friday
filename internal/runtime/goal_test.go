package runtime_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ataidesorg/ink/internal/core"
)

type captureProvider struct {
	inner  core.ModelProvider
	system string
}

func (p *captureProvider) Descriptor() core.ProviderDescriptor { return p.inner.Descriptor() }

func (p *captureProvider) Complete(ctx context.Context, req core.CompletionRequest) (core.CompletionResponse, error) {
	for _, m := range req.Messages {
		if m.Role == core.RoleSystem {
			p.system = m.Content
		}
	}
	return p.inner.Complete(ctx, req)
}

func TestGoalProseDoneStaysActiveAndInjectsContract(t *testing.T) {
	h := newHarness(t, "prose-done.json", toolsCfg())
	g, err := core.NewGoal("ship the feature", time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	h.in.Goal = &g
	h.in.History = []core.Message{{Role: core.RoleAssistant, Content: "earlier work folded into a summary"}}
	captured := &captureProvider{inner: h.deps.Provider}
	h.deps.Provider = captured
	res := h.run(context.Background(), t)
	if res.Goal == nil || res.Goal.Status != core.GoalActive {
		t.Fatalf("prose done must leave the goal active: %+v", res.Goal)
	}
	if !res.ContinueGoal {
		t.Fatal("active goal must continue")
	}
	if !strings.Contains(captured.system, string(g.ID)) || !strings.Contains(captured.system, "goal_complete") {
		t.Fatalf("assemble dropped the goal contract:\n%s", captured.system)
	}
	if !strings.Contains(captured.system, "ship the feature") {
		t.Fatalf("assemble dropped the objective:\n%s", captured.system)
	}
}

func TestPausedGoalDoesNotContinue(t *testing.T) {
	h := newHarness(t, "prose-done.json", toolsCfg())
	g, err := core.NewGoal("paused work", time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	g, err = g.Pause(core.GoalCauseUser, time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	h.in.Goal = &g
	res := h.run(context.Background(), t)
	if res.ContinueGoal {
		t.Fatal("paused goal must not auto-continue")
	}
	if res.Goal == nil || res.Goal.Status != core.GoalPaused || res.Goal.PauseCause != core.GoalCauseUser {
		t.Fatalf("paused goal mutated: %+v", res.Goal)
	}
}

func TestGoalTurnCapPauses(t *testing.T) {
	h := newHarness(t, "prose-done.json", toolsCfg())
	g, err := core.NewGoal("one turn", time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	g.MaxAutomaticTurns = 1
	h.in.Goal = &g
	res := h.run(context.Background(), t)
	if res.ContinueGoal {
		t.Fatal("turn cap must stop continuation")
	}
	if res.Goal == nil || res.Goal.Status != core.GoalPaused || res.Goal.PauseCause != core.GoalCauseContinuationLimit {
		t.Fatalf("turn cap: %+v", res.Goal)
	}
}

func TestGoalTokenBudgetPauses(t *testing.T) {
	h := newHarness(t, "prose-done.json", toolsCfg())
	g, err := core.NewGoal("cheap", time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	g.TokenBudget = 5
	h.in.Goal = &g
	res := h.run(context.Background(), t)
	if res.ContinueGoal {
		t.Fatal("token budget must stop continuation")
	}
	if res.Goal == nil || res.Goal.Status != core.GoalPaused || res.Goal.PauseCause != core.GoalCauseTokenBudget {
		t.Fatalf("token budget: %+v", res.Goal)
	}
}

func TestGoalCompleteToolFinishes(t *testing.T) {
	g, err := core.NewGoal("prove it", time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	body := `{
  "model": "mock-1",
  "turns": [
    {
      "finish": "tool_calls",
      "usage": {"input_tokens": 8, "output_tokens": 6},
      "tool_calls": [{"id": "call-1", "name": "goal_complete", "arguments": {"goal_id": ` + jsonQuote(string(g.ID)) + `, "kind": "eval", "summary": "command_succeeds"}}]
    },
    {
      "content": "Marked complete.",
      "finish": "stop",
      "match": "goal complete",
      "usage": {"input_tokens": 12, "output_tokens": 4}
    }
  ]
}`
	path := filepath.Join(t.TempDir(), "goal-complete.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newHarnessAt(t, path, toolsCfg())
	h.in.Goal = &g
	var saved core.Goal
	h.in.SaveGoal = func(next core.Goal) error { saved = next; return nil }
	res := h.run(context.Background(), t)
	if res.Goal == nil || res.Goal.Status != core.GoalComplete || res.Goal.EvidenceKind != core.GoalEvidenceEval {
		t.Fatalf("complete: %+v", res.Goal)
	}
	if res.ContinueGoal {
		t.Fatal("complete goal must not continue")
	}
	if saved.Status != core.GoalComplete {
		t.Fatalf("SaveGoal not called: %+v", saved)
	}
}

func jsonQuote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func TestGoalResultWithoutGoal(t *testing.T) {
	h := newHarness(t, "prose-done.json", toolsCfg())
	res := h.run(context.Background(), t)
	if res.Goal != nil || res.ContinueGoal {
		t.Fatalf("no goal: %+v continue=%v", res.Goal, res.ContinueGoal)
	}
}
