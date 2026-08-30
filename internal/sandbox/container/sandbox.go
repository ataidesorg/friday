package container

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ataidesorg/ink/internal/core"
	"github.com/ataidesorg/ink/internal/fsutil"
	"github.com/ataidesorg/ink/internal/sandbox"
)

// Sandbox is one running container plus its bind-mounted tree.
type Sandbox struct {
	id        core.SandboxID
	spec      core.SandboxSpec
	o         sandbox.Options
	run       Runner
	runtime   string
	dir       string
	home      string
	cid       string
	createdAt time.Time

	mu        sync.Mutex
	destroyed bool
}

// Info implements core.Sandbox.
func (s *Sandbox) Info() core.SandboxInfo {
	return core.SandboxInfo{ID: s.id, Provider: Name, Spec: s.spec}
}

// Dir is the host path bind-mounted at /workspace.
func (s *Sandbox) Dir() string { return s.dir }

// Enforced names the limits this provider actually applies.
func (s *Sandbox) Enforced() []string {
	return []string{"network", "memory", "cpus", "pids", "wall_clock", "output_bytes"}
}

// Exec runs argv inside the container. A non-zero exit is a result, not an error.
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
	workdir, err := s.guestDir(req.Dir)
	if err != nil {
		return res, err
	}
	timeout, err := s.timeout(req.Timeout)
	if err != nil {
		return res, err
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	argv := append([]string{"exec", "-i", "-w", workdir, s.cid}, req.Argv...)
	start := s.o.Clock()
	out, runErr := s.run(cctx, argv, req.Stdin)
	stdout, outTrunc := capRedact(s.o, out.Stdout)
	stderr, errTrunc := capRedact(s.o, out.Stderr)
	res = core.ExecResult{
		ExitCode:  out.ExitCode,
		Stdout:    stdout,
		Stderr:    stderr,
		Elapsed:   s.o.Clock().Sub(start),
		TimedOut:  errors.Is(cctx.Err(), context.DeadlineExceeded),
		Truncated: outTrunc || errTrunc,
	}
	if res.TimedOut {
		return res, nil
	}
	if err := ctx.Err(); err != nil {
		return res, err
	}
	if runErr != nil {
		return res, fmt.Errorf("%w: %s exec %s: %w", core.ErrUnavailable, s.runtime, req.Argv[0], runErr)
	}
	return res, nil
}

// ReadFile implements core.FileAccess against the bind-mounted host tree.
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

// Snapshot commits the running container to an image.
func (s *Sandbox) Snapshot(ctx context.Context) (core.SnapshotRef, error) {
	if err := ctx.Err(); err != nil {
		return core.SnapshotRef{}, err
	}
	if s.isDestroyed() {
		return core.SnapshotRef{}, fmt.Errorf("%w: sandbox %s destroyed", core.ErrUnavailable, s.id)
	}
	tag := "ink-snap-" + string(core.NewSandboxID())
	res, err := s.run(ctx, []string{"commit", s.cid, tag}, "")
	if err != nil {
		return core.SnapshotRef{}, fmt.Errorf("%w: %s commit: %w", core.ErrUnavailable, s.runtime, err)
	}
	if res.ExitCode != 0 {
		return core.SnapshotRef{}, fmt.Errorf("%w: %s commit exited %d: %s", core.ErrUnavailable, s.runtime, res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return core.SnapshotRef{ID: tag, Provider: Name, CreatedAt: s.o.Clock()}, nil
}

// Destroy stops the container. The copied tree is removed unless KeepWorkspace
// is set. An in-place WorkDir is never removed. Idempotent.
func (s *Sandbox) Destroy(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.destroyed {
		return nil
	}
	s.destroyed = true
	if s.cid != "" {
		if _, err := s.run(ctx, []string{"rm", "-f", s.cid}, ""); err != nil && ctx.Err() == nil {
			s.destroyed = false
			return fmt.Errorf("%w: %s rm: %w", core.ErrUnavailable, s.runtime, err)
		}
	}
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

func (s *Sandbox) guestDir(rel string) (string, error) {
	if rel == "" {
		return guestRoot, nil
	}
	clean, err := fsutil.CleanRel(rel)
	if err != nil {
		return "", err
	}
	if clean == "." {
		return guestRoot, nil
	}
	return guestRoot + "/" + filepath.ToSlash(clean), nil
}

func (s *Sandbox) confine(rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("%w: path must be non-empty", core.ErrInvalidInput)
	}
	return fsutil.Confine(s.dir, rel)
}

func capRedact(o sandbox.Options, s string) (string, bool) {
	cut := o.MaxOutputBytes > 0 && len(s) > o.MaxOutputBytes
	s = o.Redactor.Redact(s)
	if o.MaxOutputBytes > 0 && len(s) > o.MaxOutputBytes {
		s = s[:o.MaxOutputBytes]
	}
	return s, cut
}
