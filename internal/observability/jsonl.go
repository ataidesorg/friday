// Package observability persists a run's event trail as JSON lines under
// .friday/local/runs/<run>/events.jsonl and replays it for `friday trace`.
// Everything stays on the local machine; the only writer is a
// core.RedactingSink, so a raw event never reaches disk.
package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/ataidesorg/friday/internal/core"
	"github.com/ataidesorg/friday/internal/redact"
)

// Layout of the local run store.
const (
	LocalDir  = ".friday/local"
	TrailFile = "events.jsonl"
	dirPerm   = 0o700
	filePerm  = 0o600
	syncEvery = 16
)

// RunDir is <root>/.friday/local/runs/<run>.
func RunDir(projectRoot string, run core.RunID) string {
	return filepath.Join(projectRoot, filepath.FromSlash(LocalDir), "runs", string(run))
}

// TrailPath is the events.jsonl inside RunDir.
func TrailPath(projectRoot string, run core.RunID) string {
	return filepath.Join(RunDir(projectRoot, run), TrailFile)
}

// JSONLSink appends one JSON line per event and fsyncs every syncEvery
// events and on Close. Safe for concurrent use.
type JSONLSink struct {
	mu     sync.Mutex
	f      *os.File
	n      int
	closed bool
}

// OpenJSONL opens path for append, creating it with perm and its parent
// directories with 0700.
func OpenJSONL(path string, perm fs.FileMode) (*JSONLSink, error) {
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return nil, fmt.Errorf("create run dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, perm) //nolint:gosec // path is the run's own trail under .friday/local
	if err != nil {
		return nil, fmt.Errorf("open trail: %w", err)
	}
	return &JSONLSink{f: f}, nil
}

// Emit implements core.EventSink.
func (s *JSONLSink) Emit(ctx context.Context, e core.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("%w: trail is closed", core.ErrUnavailable)
	}
	if _, err := s.f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write trail: %w", err)
	}
	s.n++
	if s.n%syncEvery == 0 {
		return s.f.Sync()
	}
	return nil
}

// Close syncs and closes the file; a second Close is a no-op.
func (s *JSONLSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if err := s.f.Sync(); err != nil {
		_ = s.f.Close()
		return fmt.Errorf("sync trail: %w", err)
	}
	return s.f.Close()
}

// NewRedactingJSONL is the constructor the runtime uses: a JSONLSink wrapped
// in core.RedactingSink so nothing unredacted is written. The redactor is
// required and the privacy mode must be valid; both fail closed.
func NewRedactingJSONL(path string, r *redact.Redactor, mode core.PrivacyMode) (core.EventSink, io.Closer, error) {
	if r == nil {
		return nil, nil, fmt.Errorf("%w: trail requires a redactor", core.ErrInvalidInput)
	}
	if mode != core.PrivacyStandard && mode != core.PrivacyMinimal {
		return nil, nil, fmt.Errorf("%w: unknown privacy mode %q", core.ErrInvalidInput, mode)
	}
	s, err := OpenJSONL(path, filePerm)
	if err != nil {
		return nil, nil, err
	}
	return core.RedactingSink{Inner: s, Redactor: r, Mode: mode}, s, nil
}
