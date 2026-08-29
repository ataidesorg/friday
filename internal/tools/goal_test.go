package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ataidesorg/friday/internal/core"
)

type memGoal struct {
	g  core.Goal
	ok bool
}

func (m *memGoal) load() (core.Goal, bool) { return m.g, m.ok }
func (m *memGoal) save(g core.Goal) error {
	m.g, m.ok = g, true
	return nil
}

func TestGoalCompleteRequiresEvidenceAndCurrentID(t *testing.T) {
	g, err := core.NewGoal("ship", time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	mem := &memGoal{g: g, ok: true}
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	tool := &GoalComplete{Load: mem.load, Save: mem.save, Now: func() time.Time { return now }}
	_, tc := ws(t)

	_, err = call(t, tool, tc, `{"goal_id":"`+string(g.ID)+`","kind":"test","summary":""}`)
	if err == nil {
		t.Fatal("empty evidence must fail")
	}
	_, err = call(t, &GoalComplete{Load: mem.load, Save: mem.save, Now: tool.Now}, tc, `{"goal_id":"`+string(g.ID)+`","kind":"test"}`)
	if err == nil {
		t.Fatal("missing summary must fail schema")
	}
	_, err = call(t, tool, tc, `{"goal_id":"not-the-id","kind":"test","summary":"go test ./..."}`)
	if err == nil || !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("stale id: %v", err)
	}
	out, err := call(t, tool, tc, `{"goal_id":"`+string(g.ID)+`","kind":"test","summary":"go test ./... exit 0"}`)
	if err != nil {
		t.Fatal(err)
	}
	if mem.g.Status != core.GoalComplete {
		t.Fatalf("status %s", mem.g.Status)
	}
	if !strings.Contains(out.Content, "goal complete") {
		t.Fatalf("content %q", out.Content)
	}
	var got core.Goal
	if err := json.Unmarshal(out.Structured, &got); err != nil || got.EvidenceKind != core.GoalEvidenceTest {
		t.Fatalf("structured %+v %v", got, err)
	}
}

func TestGoalCompleteUnavailableWithoutSession(t *testing.T) {
	_, tc := ws(t)
	_, err := call(t, &GoalComplete{}, tc, `{"goal_id":"x","kind":"test","summary":"ok"}`)
	if err == nil || !errors.Is(err, core.ErrUnavailable) {
		t.Fatalf("want unavailable, got %v", err)
	}
}

func TestGoalBlockedAndWait(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	g, _ := core.NewGoal("ship", now)
	mem := &memGoal{g: g, ok: true}
	_, tc := ws(t)
	blocked := &GoalBlocked{Load: mem.load, Save: mem.save, Now: func() time.Time { return now }}
	_, err := call(t, blocked, tc, `{"goal_id":"`+string(g.ID)+`","reason":"need login","evidence":"401 three times","repeated_turns":2}`)
	if err == nil {
		t.Fatal("repeated_turns 2 must fail")
	}
	if _, err := call(t, blocked, tc, `{"goal_id":"`+string(g.ID)+`","reason":"need login","evidence":"401 three times","repeated_turns":3}`); err != nil {
		t.Fatal(err)
	}
	if mem.g.Status != core.GoalBlocked {
		t.Fatalf("blocked status %s", mem.g.Status)
	}

	g2, _ := core.NewGoal("wait", now)
	mem2 := &memGoal{g: g2, ok: true}
	wait := &GoalWait{Load: mem2.load, Save: mem2.save, Now: func() time.Time { return now }}
	if _, err := call(t, wait, tc, `{"goal_id":"`+string(g2.ID)+`","reason":"CI","resume_after_ms":5000}`); err != nil {
		t.Fatal(err)
	}
	if mem2.g.Status != core.GoalWaiting || mem2.g.WaitUntil.Sub(now) < core.MinGoalWait {
		t.Fatalf("wait %+v", mem2.g)
	}

	_, err = call(t, &GoalComplete{Load: mem.load, Save: mem.save, Now: func() time.Time { return now }}, tc,
		`{"goal_id":"`+string(g.ID)+`","kind":"test","summary":"go test"}`)
	if err == nil {
		t.Fatal("blocked goal must not complete")
	}
	_, err = call(t, &GoalBlocked{Load: mem.load, Save: mem.save, Now: func() time.Time { return now }}, tc,
		`{"goal_id":"stale","reason":"x","evidence":"y","repeated_turns":3}`)
	if err == nil || !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("stale blocked id: %v", err)
	}
}

func TestGoalCompleteFileNeedsPathAndExistingFile(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	g, _ := core.NewGoal("say hi", now)
	mem := &memGoal{g: g, ok: true}
	root, tc := ws(t)
	tool := &GoalComplete{Load: mem.load, Save: mem.save, Now: func() time.Time { return now }}
	_, err := call(t, tool, tc, `{"goal_id":"`+string(g.ID)+`","kind":"file","summary":"hello.txt exists"}`)
	if err == nil {
		t.Fatal("file kind without path must fail")
	}
	_, err = call(t, tool, tc, `{"goal_id":"`+string(g.ID)+`","kind":"file","path":"missing.txt","summary":"wrote missing.txt"}`)
	if err == nil {
		t.Fatal("missing file must fail")
	}
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, tool, tc, `{"goal_id":"`+string(g.ID)+`","kind":"file","path":"hello.txt","summary":"hello.txt exists"}`); err != nil {
		t.Fatal(err)
	}
	if mem.g.Status != core.GoalComplete {
		t.Fatalf("status %s", mem.g.Status)
	}
}

func TestGoalCompleteProofRejectsUnwrittenFile(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	g, _ := core.NewGoal("say hi", now)
	mem := &memGoal{g: g, ok: true}
	_, tc := ws(t)
	tool := &GoalComplete{
		Load: mem.load, Save: mem.save, Now: func() time.Time { return now },
		Proof: func(core.GoalEvidenceKind, string, string) error {
			return fmt.Errorf("%w: file hello.txt was not written this run", core.ErrInvalidInput)
		},
	}
	_, err := call(t, tool, tc, `{"goal_id":"`+string(g.ID)+`","kind":"file","path":"hello.txt","summary":"hello.txt exists"}`)
	if err == nil {
		t.Fatal("proof must reject an unwritten file")
	}
	if mem.g.Status == core.GoalComplete {
		t.Fatal("goal completed despite proof failure")
	}
}

func TestWithGoalRebinds(t *testing.T) {
	g, _ := core.NewGoal("x", time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC))
	mem := &memGoal{g: g, ok: true}
	r := Default(nil, nil).WithGoal(mem.load, mem.save)
	t1, ok := r.Get("goal_complete")
	if !ok {
		t.Fatal("missing goal_complete")
	}
	if t2, _ := Default(nil, nil).Get("goal_complete"); t2 == t1 {
		t.Fatal("WithGoal must copy")
	}
}
