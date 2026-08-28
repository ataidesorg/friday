package runtime_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ataidesorg/friday/internal/core"
	"github.com/ataidesorg/friday/internal/runtime"
)

func TestCheckpointRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoint.json")
	run := core.NewRun(core.NewTaskID(), 1, time.Unix(1, 0).UTC())
	c := runtime.Checkpoint{
		Run: run,
		Messages: []core.Message{{
			Role:    core.RoleUser,
			Content: "hi",
			Images:  []core.ImagePart{{MediaType: "image/png", Data: "abc"}},
		}},
		Usage:    core.Usage{InputTokens: 4, OutputTokens: 2},
		Seq:      3,
		Calls:    2,
		Verified: true,
		Verify:   "ok",
	}
	if err := runtime.SaveCheckpoint(path, c); err != nil {
		t.Fatal(err)
	}
	got, err := runtime.LoadCheckpoint(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Schema != runtime.CheckpointSchema || got.Run.ID != run.ID || got.Seq != 3 || !got.Verified {
		t.Fatalf("got %+v", got)
	}
	if len(got.Messages) != 1 || got.Messages[0].Content != "hi" {
		t.Fatalf("messages %+v", got.Messages)
	}
	if got.Messages[0].Images != nil {
		t.Fatal("images must not be persisted")
	}
	if _, err := runtime.LoadCheckpoint(filepath.Join(t.TempDir(), "missing.json")); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
	if err := runtime.SaveCheckpoint("", c); !errors.Is(err, core.ErrInvalidInput) {
		t.Errorf("empty path: %v", err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.LoadCheckpoint(path); !errors.Is(err, core.ErrInvalidInput) {
		t.Errorf("garbage: %v", err)
	}
}

func TestResumeFromSynthesisSkipsModel(t *testing.T) {
	h := newHarness(t, "add-farewell.json", toolsCfg())
	path := filepath.Join(h.root, "resume.json")
	st := core.InitialState()
	for i := 0; i < 7; i++ {
		next, err := st.Apply(core.Transition{Kind: core.TransitionAdvance})
		if err != nil {
			t.Fatal(err)
		}
		st = next
	}
	if st.Phase != core.PhaseSynthesis {
		t.Fatalf("phase %s", st.Phase)
	}
	run := core.NewRun(h.in.Task.ID, 1, time.Unix(1, 0).UTC())
	run.State = st
	c := runtime.Checkpoint{
		Run:      run,
		Last:     core.CompletionResponse{Content: "Resumed summary\nLearned: resume works."},
		Verified: true,
		Verify:   "ok",
	}
	if err := runtime.SaveCheckpoint(path, c); err != nil {
		t.Fatal(err)
	}
	h.in.ResumeFrom = path
	h.deps.Provider = failComplete{ModelProvider: h.deps.Provider, t: t}
	res := h.run(context.Background(), t)
	if res.Outcome.Kind != core.OutcomeCompletedVerified {
		t.Fatalf("outcome %+v", res.Outcome)
	}
	if res.Summary != "Resumed summary\nLearned: resume works." {
		t.Fatalf("summary %q", res.Summary)
	}
}

type failComplete struct {
	core.ModelProvider
	t *testing.T
}

func (f failComplete) Complete(context.Context, core.CompletionRequest) (core.CompletionResponse, error) {
	f.t.Fatal("Complete must not run when resuming at synthesis")
	return core.CompletionResponse{}, nil
}

func TestResumeMissingCheckpoint(t *testing.T) {
	h := newHarness(t, "add-farewell.json", toolsCfg())
	h.in.ResumeFrom = filepath.Join(h.root, "missing.json")
	_, err := runtime.Run(context.Background(), h.deps, h.in)
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestRunWritesCheckpoint(t *testing.T) {
	h := newHarness(t, "add-farewell.json", toolsCfg())
	h.in.CheckpointPath = filepath.Join(h.root, "checkpoint.json")
	res := h.run(context.Background(), t)
	got, err := runtime.LoadCheckpoint(h.in.CheckpointPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.Run.ID != res.Run.ID || !got.Run.State.Terminal() {
		t.Fatalf("checkpoint run %+v, result %+v", got.Run, res.Run)
	}
	if runtime.CheckpointPath(h.root, res.Run.ID) == "" {
		t.Fatal("CheckpointPath empty")
	}
}
