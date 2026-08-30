package observability

import (
	"context"
	"io"
	"os"
	"testing"
	"time"

	"github.com/ataidesorg/ink/internal/core"
)

func TestLazyTrail(t *testing.T) {
	root := t.TempDir()
	s := NewLazyTrail(root, nil, core.PrivacyStandard)
	if err := s.Close(); err != nil || s.Path() != "" {
		t.Fatalf("close without events: %v %q", err, s.Path())
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatalf("close without events created %v", entries)
	}
	run := core.NewRunID()
	e := core.NewEvent(core.NewTaskID(), run, 1, time.Now(), core.Warning{Message: "hi"})
	if err := s.Emit(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if s.Path() != TrailPath(root, run) {
		t.Fatalf("path %q", s.Path())
	}
	events, err := ReadTrail(s.Path())
	if err != nil || len(events) != 1 {
		t.Fatalf("trail: %v %v", events, err)
	}
	var _ io.Closer = s
	var _ core.EventSink = s
}
