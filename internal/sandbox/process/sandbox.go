package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ataidesorg/ink/internal/core"
	"github.com/ataidesorg/ink/internal/fsutil"
	"github.com/ataidesorg/ink/internal/sandbox"
)

// waitDelay bounds how long Wait blocks on pipes held open by orphaned
// grandchildren after the process group is killed.
const waitDelay = 2 * time.Second

// Sandbox is one materialised working tree. It implements core.Sandbox,
// core.FileAccess, and core.Snapshotter (the latter unavailable here).
type Sandbox struct {
	id        core.SandboxID
	spec      core.SandboxSpec
	o         sandbox.Options
	dir       string // symlink-resolved tree root commands run in
	home      string // private HOME/TMPDIR; holds the copy when Source is copy
	path      []string
	env       []string
	createdAt time.Time

	mu        sync.Mutex
	destroyed bool
}

// Info implements core.Sandbox.
func (s *Sandbox) Info() core.SandboxInfo {
	return core.SandboxInfo{ID: s.id, Provider: Name, Spec: s.spec}
}

// Dir is the tree commands run in and file access is confined to.
func (s *Sandbox) Dir() string { return s.dir }

// Enforced names the limits this provider enforces on this OS.
func (s *Sandbox) Enforced() []string { return enforcedLimits() }

// Exec runs one command with no shell, the scrubbed environment, a fixed
// PATH, and a timeout of min(req.Timeout, Limits.WallClock, remaining
// sandbox lifetime). A non-zero exit is a result, not an error.
func (s *Sandbox) Exec(ctx context.Context, req core.ExecRequest) (core.ExecResult, error) {
	var res core.ExecResult
	if err := ctx.Err(); err != nil {
		return res, err
	}
	if len(req.Argv) == 0 || req.Argv[0] == "" || req.Timeout < 0 {
		return res, fmt.Errorf("%w: exec needs argv and a non-negative timeout", core.ErrInvalidInput)
	}
	if s.isDestroyed() {
		return res, fmt.Errorf("%w: sandbox %s destroyed", core.ErrUnavailable, s.id)
	}
	dir, err := s.execDir(req.Dir)
	if err != nil {
		return res, err
	}
	bin, err := s.lookPath(req.Argv[0], dir)
	if err != nil {
		return res, err
	}
	timeout, err := s.timeout(req.Timeout)
	if err != nil {
		return res, err
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, req.Argv[1:]...) //nolint:gosec // argv is policy-checked by the caller; no shell
	stdout, stderr := newCap(s.o.MaxOutputBytes), newCap(s.o.MaxOutputBytes)
	cmd.Dir, cmd.Env, cmd.Stdin, cmd.Stdout, cmd.Stderr = dir, s.env, strings.NewReader(req.Stdin), stdout, stderr
	cmd.WaitDelay = waitDelay
	setProcAttr(cmd)

	start := s.o.Clock()
	runErr := cmd.Run()
	res = core.ExecResult{
		Stdout:    s.o.Redactor.Redact(stdout.String()),
		Stderr:    s.o.Redactor.Redact(stderr.String()),
		Elapsed:   s.o.Clock().Sub(start),
		TimedOut:  errors.Is(cctx.Err(), context.DeadlineExceeded),
		Truncated: stdout.truncated || stderr.truncated,
	}
	if err := ctx.Err(); err != nil && !res.TimedOut {
		return res, err
	}
	var exit *exec.ExitError
	switch {
	case runErr == nil:
	case errors.As(runErr, &exit):
		res.ExitCode = exit.ExitCode()
	default:
		return res, fmt.Errorf("%w: start %s: %w", core.ErrUnavailable, req.Argv[0], runErr)
	}
	return res, nil
}

// ReadFile implements core.FileAccess.
func (s *Sandbox) ReadFile(_ context.Context, path string) ([]byte, error) {
	if s.isDestroyed() {
		return nil, fmt.Errorf("%w: sandbox %s destroyed", core.ErrUnavailable, s.id)
	}
	abs, err := s.confine(path)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("%w: %s", core.ErrNotFound, path)
	case err != nil:
		return nil, err
	case st.IsDir():
		return nil, fmt.Errorf("%w: %s is a directory", core.ErrInvalidInput, path)
	}
	return os.ReadFile(abs) //nolint:gosec // confined to the sandbox tree above
}

// WriteFile implements core.FileAccess; parent directories are created.
func (s *Sandbox) WriteFile(_ context.Context, path string, data []byte, mode fs.FileMode) error {
	if s.isDestroyed() {
		return fmt.Errorf("%w: sandbox %s destroyed", core.ErrUnavailable, s.id)
	}
	abs, err := s.confine(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
		return err
	}
	return os.WriteFile(abs, data, mode)
}

// Snapshot implements core.Snapshotter; not available in this provider.
func (s *Sandbox) Snapshot(context.Context) (core.SnapshotRef, error) {
	return core.SnapshotRef{}, core.NotImplementedError{Feature: "process snapshot"}
}

// Destroy removes the private area (copy, HOME, TMPDIR) unless
// KeepWorkspace is set. An in-place WorkDir is never removed. Idempotent.
func (s *Sandbox) Destroy(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.destroyed {
		return nil
	}
	s.destroyed = true
	if s.o.KeepWorkspace {
		return nil
	}
	return os.RemoveAll(s.home)
}

func (s *Sandbox) isDestroyed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.destroyed
}

// timeout is min(requested, Limits.WallClock, remaining lifetime).
func (s *Sandbox) timeout(requested time.Duration) (time.Duration, error) {
	limit := s.spec.Limits.WallClock
	if requested > 0 && requested < limit {
		limit = requested
	}
	if s.spec.WallClock > 0 {
		remaining := s.createdAt.Add(s.spec.WallClock).Sub(s.o.Clock())
		if remaining <= 0 {
			return 0, fmt.Errorf("%w: sandbox lifetime of %s exhausted", core.ErrTimeout, s.spec.WallClock)
		}
		limit = min(limit, remaining)
	}
	return limit, nil
}

// execDir resolves the requested working directory inside the tree.
func (s *Sandbox) execDir(rel string) (string, error) {
	if rel == "" {
		return s.dir, nil
	}
	abs, err := s.confine(rel)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(abs)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("%w: dir %s", core.ErrNotFound, rel)
	case err != nil:
		return "", err
	case !st.IsDir():
		return "", fmt.Errorf("%w: dir %s is not a directory", core.ErrInvalidInput, rel)
	}
	return abs, nil
}

// lookPath resolves argv[0]: a bare name against the fixed PATH, a path with
// a separator relative to dir and confined to the tree. The host PATH is
// never consulted.
func (s *Sandbox) lookPath(name, dir string) (string, error) {
	if strings.Contains(name, string(filepath.Separator)) {
		abs := name
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(dir, name)
		}
		rel, err := filepath.Rel(s.dir, abs)
		if err != nil {
			return "", fmt.Errorf("%w: %s is outside the sandbox", core.ErrInvalidInput, name)
		}
		if _, err := s.confine(rel); err != nil {
			return "", err
		}
		if isExecutable(abs) {
			return abs, nil
		}
		return "", fmt.Errorf("%w: %s is not an executable in the sandbox", core.ErrNotFound, name)
	}
	for _, d := range s.path {
		if p := filepath.Join(d, name); isExecutable(p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("%w: command %q not on the sandbox PATH", core.ErrNotFound, name)
}

func isExecutable(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0
}

// confine maps a relative path onto the tree and refuses anything that
// resolves outside it, including through symlinks.
func (s *Sandbox) confine(rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("%w: path must be non-empty", core.ErrInvalidInput)
	}
	return fsutil.Confine(s.dir, rel)
}

// capWriter keeps the first max bytes and remembers that more arrived.
type capWriter struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func newCap(limit int) *capWriter { return &capWriter{max: limit} }

func (w *capWriter) Write(p []byte) (int, error) {
	n := len(p)
	if room := w.max - w.buf.Len(); n > room {
		w.truncated = true
		p = p[:max(room, 0)]
	}
	w.buf.Write(p)
	return n, nil
}

func (w *capWriter) String() string { return w.buf.String() }
