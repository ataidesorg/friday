package core

import (
	"context"
	"fmt"
	"sync"

	"github.com/ataidesorg/friday/internal/redact"
)

// EventSink receives events. Implementations must be safe for concurrent use.
type EventSink interface {
	Emit(ctx context.Context, e Event) error
}

// MemorySink keeps events in memory, mainly for tests and replay.
type MemorySink struct {
	mu     sync.Mutex
	events []Event
}

// Emit appends e.
func (s *MemorySink) Emit(_ context.Context, e Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
	return nil
}

// Events returns a copy of everything emitted so far.
func (s *MemorySink) Events() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Event, len(s.events))
	copy(out, s.events)
	return out
}

// RedactingSink scrubs every event before forwarding it. An event that
// cannot be redacted is dropped and replaced by a Warning; the raw event is
// never forwarded.
type RedactingSink struct {
	Inner    EventSink
	Redactor *redact.Redactor
	Mode     PrivacyMode
}

// Emit forwards the redacted event, or a Warning when redaction fails. The
// redaction error is returned so callers know the event was dropped.
func (s RedactingSink) Emit(ctx context.Context, e Event) error {
	red, err := e.Redacted(s.Redactor, s.Mode)
	if err != nil {
		w := NewEvent(e.Task, e.Run, e.Seq, e.At, Warning{Message: fmt.Sprintf("event %s dropped: redaction failed", e.ID)})
		if emitErr := s.Inner.Emit(ctx, w); emitErr != nil {
			return fmt.Errorf("emit warning for dropped event %s: %w", e.ID, emitErr)
		}
		return fmt.Errorf("event %s dropped: %w", e.ID, err)
	}
	return s.Inner.Emit(ctx, red)
}

var (
	_ EventSink = (*MemorySink)(nil)
	_ EventSink = RedactingSink{}
)
