package runtime_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ataidesorg/friday/internal/core"
	"github.com/ataidesorg/friday/internal/runtime"
)

func TestRunGraphWavesAndParallel(t *testing.T) {
	g := core.TaskGraph{Nodes: []core.Subtask{
		{ID: "api", Title: "API"},
		{ID: "docs", Title: "Docs"},
		{ID: "ui", Title: "UI", Deps: []string{"api"}},
	}}
	var mu sync.Mutex
	var order []string
	var live atomic.Int32
	var sawParallel atomic.Bool
	err := runtime.RunGraph(context.Background(), g, func(_ context.Context, n core.Subtask) error {
		live.Add(1)
		if live.Load() >= 2 {
			sawParallel.Store(true)
		}
		time.Sleep(20 * time.Millisecond)
		live.Add(-1)
		mu.Lock()
		order = append(order, n.ID)
		mu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(order) != 3 || order[2] != "ui" {
		t.Fatalf("order %v, ui must be last", order)
	}
	if !sawParallel.Load() {
		t.Fatal("wave 0 nodes must overlap")
	}
}

func TestRunGraphCancelsWaveOnError(t *testing.T) {
	g := core.TaskGraph{Nodes: []core.Subtask{
		{ID: "a"}, {ID: "b"},
		{ID: "c", Deps: []string{"a", "b"}},
	}}
	var cRan atomic.Bool
	err := runtime.RunGraph(context.Background(), g, func(ctx context.Context, n core.Subtask) error {
		if n.ID == "a" {
			return errors.New("boom")
		}
		if n.ID == "c" {
			cRan.Store(true)
		}
		<-ctx.Done()
		return ctx.Err()
	})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("want boom wrapped, got %v", err)
	}
	if cRan.Load() {
		t.Fatal("later wave must not run")
	}
}

func TestRunGraphRejectsNilAndCycles(t *testing.T) {
	g := core.TaskGraph{Nodes: []core.Subtask{{ID: "a"}}}
	if err := runtime.RunGraph(context.Background(), g, nil); !errors.Is(err, core.ErrInvalidInput) {
		t.Errorf("nil run: %v", err)
	}
	cyc := core.TaskGraph{Nodes: []core.Subtask{
		{ID: "a", Deps: []string{"b"}},
		{ID: "b", Deps: []string{"a"}},
	}}
	if err := runtime.RunGraph(context.Background(), cyc, func(context.Context, core.Subtask) error { return nil }); !errors.Is(err, core.ErrInvalidInput) {
		t.Errorf("cycle: %v", err)
	}
}
