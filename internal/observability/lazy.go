package observability

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/ataidesorg/ink/internal/core"
	"github.com/ataidesorg/ink/internal/redact"
)

// LazyTrail opens the JSONL trail under root on the first event, once the
// runtime has minted the run ID. It never writes without an event, so an
// aborted start leaves nothing behind. Safe for concurrent use.
type LazyTrail struct {
	root string
	mode core.PrivacyMode
	red  *redact.Redactor

	mu     sync.Mutex
	sink   core.EventSink
	closer io.Closer
	path   string
}

// NewLazyTrail records events at TrailPath(root, run) through a redacting
// sink; a nil redactor falls back to the builtin patterns.
func NewLazyTrail(root string, r *redact.Redactor, mode core.PrivacyMode) *LazyTrail {
	if r == nil {
		r = redact.New()
	}
	return &LazyTrail{root: root, mode: mode, red: r}
}

// Emit implements core.EventSink.
func (t *LazyTrail) Emit(ctx context.Context, e core.Event) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.sink == nil {
		path := TrailPath(t.root, e.Run)
		sink, closer, err := NewRedactingJSONL(path, t.red, t.mode)
		if err != nil {
			return fmt.Errorf("open trail: %w", err)
		}
		t.sink, t.closer, t.path = sink, closer, path
	}
	return t.sink.Emit(ctx, e)
}

// Close flushes the trail; safe to call without events and more than once.
func (t *LazyTrail) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closer == nil {
		return nil
	}
	err := t.closer.Close()
	t.closer = nil
	return err
}

// Path is the trail file, or "" when no event was written.
func (t *LazyTrail) Path() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.path
}
