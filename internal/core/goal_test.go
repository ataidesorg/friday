package core

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNewGoal(t *testing.T) {
	g, err := NewGoal(" ship it ", t0)
	if err != nil {
		t.Fatal(err)
	}
	if g.Status != GoalActive || g.Objective != "ship it" || !ValidID(string(g.ID)) {
		t.Fatalf("got %+v", g)
	}
	if g.Continues() != true || !g.Open() {
		t.Fatal("new goal must continue")
	}
	if _, err := NewGoal("  ", t0); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty: %v", err)
	}
	if _, err := NewGoal(strings.Repeat("a", MaxGoalObjectiveBytes+1), t0); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("oversized: %v", err)
	}
}

func TestGoalEditKeepsUsage(t *testing.T) {
	g, _ := NewGoal("first", t0)
	g.Usage = Usage{InputTokens: 9}
	g.AutomaticTurns = 3
	next, err := g.Edit("second", t0.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if next.Objective != "second" || next.Usage.InputTokens != 9 || next.AutomaticTurns != 3 || next.ID != g.ID {
		t.Fatalf("edit mutated counters: %+v", next)
	}
}

func TestGoalCompleteRequiresEvidence(t *testing.T) {
	g, _ := NewGoal("done when tests pass", t0)
	if _, err := g.Complete(GoalEvidenceCommand, "  ", t0); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty evidence: %v", err)
	}
	if _, err := g.Complete("prose", "looks good", t0); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("prose kind: %v", err)
	}
	done, err := g.Complete(GoalEvidenceTest, "go test ./... exit 0", t0)
	if err != nil || done.Status != GoalComplete || done.EvidenceKind != GoalEvidenceTest {
		t.Fatalf("complete: %+v %v", done, err)
	}
	if _, err := done.Complete(GoalEvidenceTest, "again", t0); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("complete twice: %v", err)
	}
}

func TestGoalPauseResumeAndBlockWait(t *testing.T) {
	g, _ := NewGoal("x", t0)
	paused, err := g.Pause(GoalCauseUser, t0)
	if err != nil || paused.Status != GoalPaused {
		t.Fatalf("pause: %+v %v", paused, err)
	}
	if paused.Continues() {
		t.Fatal("paused must not auto-continue")
	}
	resumed, err := paused.Resume(t0)
	if err != nil || resumed.Status != GoalActive || resumed.PauseCause != "" {
		t.Fatalf("resume: %+v %v", resumed, err)
	}
	if _, err := g.Block("need creds", "tried 3 times", 2, t0); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("short block: %v", err)
	}
	blocked, err := g.Block("need creds", "tried 3 times", 3, t0)
	if err != nil || blocked.Status != GoalBlocked {
		t.Fatalf("block: %+v %v", blocked, err)
	}
	waiting, err := g.Wait("CI", t0.Add(time.Minute), t0)
	if err != nil || waiting.Status != GoalWaiting || waiting.Continues() {
		t.Fatalf("wait: %+v %v", waiting, err)
	}
}

func TestGoalRecordTurnCaps(t *testing.T) {
	g, _ := NewGoal("x", t0)
	g.MaxAutomaticTurns = 2
	g.MaxNoProgress = 2
	g = g.RecordTurn(Usage{OutputTokens: 1}, "hello there", false, t0)
	if g.Status != GoalActive || g.AutomaticTurns != 1 {
		t.Fatalf("first turn: %+v", g)
	}
	g = g.RecordTurn(Usage{}, "Hello, there!", false, t0)
	if g.Status != GoalPaused || g.PauseCause != GoalCauseNoProgress {
		t.Fatalf("no-progress: %+v", g)
	}

	g, _ = NewGoal("y", t0)
	g.MaxAutomaticTurns = 1
	g = g.RecordTurn(Usage{}, "a", true, t0)
	if g.Status != GoalPaused || g.PauseCause != GoalCauseContinuationLimit {
		t.Fatalf("turn cap: %+v", g)
	}

	g, _ = NewGoal("z", t0)
	g.TokenBudget = 10
	g = g.RecordTurn(Usage{InputTokens: 6, OutputTokens: 5}, "x", true, t0)
	if g.Status != GoalPaused || g.PauseCause != GoalCauseTokenBudget {
		t.Fatalf("budget: %+v", g)
	}
}

func TestGoalJSONHasNoImageFields(t *testing.T) {
	g, _ := NewGoal("no pictures", t0)
	raw, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(raw)), "image") {
		t.Fatalf("goal record must not mention images: %s", raw)
	}
}

func TestGoalContract(t *testing.T) {
	g, _ := NewGoal("add tests", t0)
	c := g.Contract()
	if !strings.Contains(c, string(g.ID)) || !strings.Contains(c, "add tests") || !strings.Contains(c, "goal_complete") {
		t.Fatalf("contract:\n%s", c)
	}
	done, _ := g.Complete(GoalEvidenceFile, "foo_test.go exists", t0)
	if done.Contract() != "" {
		t.Fatal("complete goal must not inject a contract")
	}
}

func TestParseTokenBudget(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"100k", 100_000, true},
		{"1.5m", 1_500_000, true},
		{"300000", 300_000, true},
		{"0", 0, false},
		{"", 0, false},
		{"nope", 0, false},
	}
	for _, c := range cases {
		got, err := ParseTokenBudget(c.in)
		if (err == nil) != c.ok || got != c.want && c.ok {
			t.Errorf("%q: got %d %v want %d ok=%v", c.in, got, err, c.want, c.ok)
		}
	}
}
