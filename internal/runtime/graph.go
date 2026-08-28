package runtime

import (
	"context"
	"fmt"
	"sync"

	"github.com/ataidesorg/friday/internal/core"
)

// NodeFunc runs one subtask. The caller owns worktrees, budgets, and Run.
type NodeFunc func(ctx context.Context, n core.Subtask) error

// RunGraph walks g in waves: nodes in a wave run concurrently, the next
// wave starts only after the previous wave has finished. The first node
// error cancels the rest of that wave and skips later waves.
func RunGraph(ctx context.Context, g core.TaskGraph, run NodeFunc) error {
	if run == nil {
		return fmt.Errorf("%w: graph runner is nil", core.ErrInvalidInput)
	}
	waves, err := g.Waves()
	if err != nil {
		return err
	}
	for i, wave := range waves {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := runWave(ctx, wave, run); err != nil {
			return fmt.Errorf("wave %d: %w", i, err)
		}
	}
	return nil
}

func runWave(ctx context.Context, wave []core.Subtask, run NodeFunc) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var mu sync.Mutex
	var first error
	var wg sync.WaitGroup
	for _, n := range wave {
		n := n
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := run(ctx, n); err != nil {
				mu.Lock()
				if first == nil {
					first = fmt.Errorf("subtask %s: %w", n.ID, err)
					cancel()
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return first
}
