package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ataidesorg/friday/internal/core"
	"github.com/ataidesorg/friday/internal/runtime"
	sessionstore "github.com/ataidesorg/friday/internal/session"
)

func seededSession(t *testing.T) (*sessionstore.Store, time.Time) {
	t.Helper()
	store := newTestStore(t)
	now := time.Unix(0, 0).UTC()
	if _, err := store.Create("s1", now); err != nil {
		t.Fatal(err)
	}
	for _, tn := range []sessionstore.Turn{
		{Role: core.RoleUser, Text: "q1", TS: now},
		{Role: core.RoleAssistant, Text: "a1", TS: now},
		{Role: core.RoleUser, Text: "q2", TS: now},
		{Role: core.RoleAssistant, Text: "a2", TS: now},
	} {
		if _, err := store.Append("s1", tn, now); err != nil {
			t.Fatal(err)
		}
	}
	return store, now
}

func compactSession(store *sessionstore.Store, now time.Time, run func(context.Context, runtime.Deps, runtime.Input) (runtime.Result, error)) *chatSession {
	return &chatSession{
		store: store, id: "s1", model: "m1", clock: func() time.Time { return now },
		profile:   core.NewProfileID(),
		sess:      core.SessionID("s1"),
		principal: core.Principal{Kind: core.PrincipalUser, Name: "tester"},
		run:       run,
	}
}

func TestChatCompactAppendsSummaryTurn(t *testing.T) {
	store, now := seededSession(t)
	var sawTask string
	var sawHistory []core.Message
	cs := compactSession(store, now, func(_ context.Context, _ runtime.Deps, in runtime.Input) (runtime.Result, error) {
		sawTask, sawHistory = in.Task.Description, in.History
		return runtime.Result{Summary: "the gist", Outcome: core.Outcome{Kind: core.OutcomeCompletedVerified}}, nil
	})
	note, err := cs.compact(context.Background(), nopObs{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(note, "compacted 4 turns") {
		t.Fatalf("note = %q", note)
	}
	if sawTask != compactPrompt || len(sawHistory) != 4 {
		t.Fatalf("summariser saw task %q with %d messages", sawTask, len(sawHistory))
	}
	_, turns, err := store.Load("s1")
	if err != nil {
		t.Fatal(err)
	}
	last := turns[len(turns)-1]
	if last.Kind != sessionstore.KindSummary || last.Text != "the gist" || last.Model != "m1" {
		t.Fatalf("summary turn = %+v", last)
	}
	// The next turn's history is now just the summary barrier.
	if h := sessionstore.History(turns, 0); len(h) != 1 || h[0].Role != core.RoleSystem {
		t.Fatalf("post-compact history = %+v", h)
	}
}

func TestChatCompactRefusesTinyTranscript(t *testing.T) {
	store := newTestStore(t)
	now := time.Unix(0, 0).UTC()
	if _, err := store.Create("s1", now); err != nil {
		t.Fatal(err)
	}
	cs := compactSession(store, now, func(context.Context, runtime.Deps, runtime.Input) (runtime.Result, error) {
		t.Fatal("summariser must not run on a tiny transcript")
		return runtime.Result{}, nil
	})
	if _, err := cs.compact(context.Background(), nopObs{}); err == nil {
		t.Fatal("want nothing-to-compact error")
	}
}

func TestChatAutoCompactNearBudget(t *testing.T) {
	store, now := seededSession(t)
	var tasks []string
	var mainHistory []core.Message
	cs := compactSession(store, now, func(_ context.Context, _ runtime.Deps, in runtime.Input) (runtime.Result, error) {
		tasks = append(tasks, in.Task.Description)
		if in.Task.Description == compactPrompt {
			return runtime.Result{Summary: "the gist", Outcome: core.Outcome{Kind: core.OutcomeCompletedVerified}}, nil
		}
		mainHistory = in.History
		return runtime.Result{Summary: "reply", Outcome: core.Outcome{Kind: core.OutcomeCompletedVerified}}, nil
	})
	cs.histChars = 8 // tail is 8 chars (q1a1q2a2) > 90% of 8
	if _, err := cs.turn(context.Background(), "next", nopObs{}); err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 || tasks[0] != compactPrompt || tasks[1] != "next" {
		t.Fatalf("run order = %v, want summarise then turn", tasks)
	}
	if len(mainHistory) == 0 || mainHistory[0].Role != core.RoleSystem || !strings.Contains(mainHistory[0].Content, "the gist") {
		t.Fatalf("main turn history not compacted: %+v", mainHistory)
	}
}

func TestChatTurnSkipsAutoCompactUnderBudget(t *testing.T) {
	store, now := seededSession(t)
	var tasks []string
	cs := compactSession(store, now, func(_ context.Context, _ runtime.Deps, in runtime.Input) (runtime.Result, error) {
		tasks = append(tasks, in.Task.Description)
		return runtime.Result{Summary: "reply", Outcome: core.Outcome{Kind: core.OutcomeCompletedVerified}}, nil
	})
	cs.histChars = 1000
	if _, err := cs.turn(context.Background(), "next", nopObs{}); err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("auto-compact fired under budget: %v", tasks)
	}
}
