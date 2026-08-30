// Package process is the default sandbox provider: commands run through
// os/exec in a private copy of the workspace with a scrubbed environment,
// a fixed PATH, wall-clock timeouts, and capped output.
//
// It is defence in depth against accidents, not containment of an
// adversary: on macOS there is no namespace, cgroup, or network isolation,
// and resource limits beyond wall clock and output size are not enforced.
// The container provider (`internal/sandbox/container`) is what makes an
// isolation claim (`--network none` plus cgroup limits).
package process

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ataidesorg/ink/internal/core"
	"github.com/ataidesorg/ink/internal/fsutil"
	"github.com/ataidesorg/ink/internal/sandbox"
)

// Name is the provider's registry key (`sandbox.provider = "process"`).
const Name = "process"

// Factory builds the provider for a sandbox.Registry.
var Factory sandbox.Factory = func(o sandbox.Options) core.SandboxProvider { return New(o) }

// Provider creates process sandboxes.
type Provider struct {
	o    sandbox.Options
	path []string
}

// New returns a provider; zero options take sandbox.Options defaults. The
// sandbox PATH is fixed at construction from the host's Go installation and
// the standard system directories, never from the host PATH itself.
func New(o sandbox.Options) *Provider {
	return &Provider{o: o.WithDefaults(), path: pathDirs()}
}

// Name implements core.SandboxProvider.
func (p *Provider) Name() string { return Name }

// Create validates the spec, refuses what this provider cannot enforce
// (network allowlists, mounts), scrubs the environment, and materialises
// the working tree: a private copy by default, or WorkDir itself for
// core.SourceInPlace.
func (p *Provider) Create(ctx context.Context, spec core.SandboxSpec) (core.Sandbox, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := spec.Validate(p.o.Redactor); err != nil {
		return nil, err
	}
	if len(spec.Mounts) > 0 {
		return nil, core.NotImplementedError{Feature: "process sandbox mounts"}
	}
	scrubbed, err := scrubEnv(spec.Env, p.o.Redactor)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(spec.WorkDir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("%w: work_dir %s", core.ErrNotFound, spec.WorkDir)
	case err != nil:
		return nil, err
	case !st.IsDir():
		return nil, fmt.Errorf("%w: work_dir %s is not a directory", core.ErrInvalidInput, spec.WorkDir)
	}
	home, err := os.MkdirTemp("", "ink-sbx-*")
	if err != nil {
		return nil, err
	}
	sb, err := p.materialise(ctx, spec, scrubbed, home)
	if err != nil {
		_ = os.RemoveAll(home)
		return nil, err
	}
	return sb, nil
}

func (p *Provider) materialise(ctx context.Context, spec core.SandboxSpec, env map[string]string, home string) (*Sandbox, error) {
	if err := os.Mkdir(filepath.Join(home, "tmp"), 0o750); err != nil {
		return nil, err
	}
	dir := spec.WorkDir
	if spec.Source.Kind != core.SourceInPlace {
		dir = filepath.Join(home, "work")
		if err := fsutil.CopyTree(ctx, spec.WorkDir, dir, spec.Source.Exclude); err != nil {
			return nil, fmt.Errorf("copy workspace: %w", err)
		}
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	return &Sandbox{
		id:        core.NewSandboxID(),
		spec:      spec,
		o:         p.o,
		dir:       resolved,
		home:      home,
		path:      p.path,
		env:       buildEnv(env, home, p.path),
		createdAt: p.o.Clock(),
	}, nil
}
