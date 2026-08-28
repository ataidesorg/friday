// Package container is the isolating sandbox provider: commands run inside a
// Docker or Podman container with --network none, cgroup memory/cpu/pid
// limits, bind-mounted workspace, and Snapshot as an image commit.
//
// Tests inject a Runner; they never talk to a real daemon. The process
// provider remains the shipped default for hosts without a runtime.
package container

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/ataidesorg/friday/internal/core"
	"github.com/ataidesorg/friday/internal/fsutil"
	"github.com/ataidesorg/friday/internal/sandbox"
)

// Name is the provider's registry key (`sandbox.provider = "container"`).
const Name = "container"

// DefaultImage is the keep-alive image when Settings.Image is empty.
const DefaultImage = "alpine:3.20"

// guestRoot is the workspace mount inside the container.
const guestRoot = "/workspace"

// Factory builds the provider for a sandbox.Registry.
var Factory sandbox.Factory = func(o sandbox.Options) core.SandboxProvider { return New(o) }

// Settings selects the runtime binary, image, and CLI runner.
type Settings struct {
	Runtime string
	Image   string
	Run     Runner
}

// Provider creates container sandboxes.
type Provider struct {
	o       sandbox.Options
	runtime string
	image   string
	run     Runner
}

// New returns a provider that shells out to docker or podman.
func New(o sandbox.Options) *Provider {
	return NewWith(o, Settings{})
}

// NewWith injects the CLI. Tests pass a fake Runner so no daemon is required.
func NewWith(o sandbox.Options, s Settings) *Provider {
	if s.Runtime == "" {
		s.Runtime = detectRuntime()
	}
	if s.Image == "" {
		s.Image = DefaultImage
	}
	if s.Run == nil {
		s.Run = CLIRunner(s.Runtime)
	}
	return &Provider{o: o.WithDefaults(), runtime: s.Runtime, image: s.Image, run: s.Run}
}

// Name implements core.SandboxProvider.
func (p *Provider) Name() string { return Name }

// Create validates the spec, copies or bind-mounts the workspace, and
// starts a keep-alive container with network disabled.
func (p *Provider) Create(ctx context.Context, spec core.SandboxSpec) (core.Sandbox, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := spec.Validate(p.o.Redactor); err != nil {
		return nil, err
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
	home, err := os.MkdirTemp("", "friday-sbx-*")
	if err != nil {
		return nil, err
	}
	sb, err := p.start(ctx, spec, scrubbed, home)
	if err != nil {
		_ = os.RemoveAll(home)
		return nil, err
	}
	return sb, nil
}

func (p *Provider) start(ctx context.Context, spec core.SandboxSpec, env map[string]string, home string) (*Sandbox, error) {
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
	id := core.NewSandboxID()
	argv := p.runArgv(id, spec, env, resolved)
	res, err := p.run(ctx, argv, "")
	if err != nil {
		return nil, fmt.Errorf("%w: %s run: %w", core.ErrUnavailable, p.runtime, err)
	}
	if res.ExitCode != 0 {
		msg := strings.TrimSpace(res.Stderr)
		if msg == "" {
			msg = strings.TrimSpace(res.Stdout)
		}
		return nil, fmt.Errorf("%w: %s run exited %d: %s", core.ErrUnavailable, p.runtime, res.ExitCode, msg)
	}
	cid := strings.TrimSpace(res.Stdout)
	if cid == "" {
		return nil, fmt.Errorf("%w: %s run produced no container id", core.ErrUnavailable, p.runtime)
	}
	return &Sandbox{
		id:        id,
		spec:      spec,
		o:         p.o,
		run:       p.run,
		runtime:   p.runtime,
		dir:       resolved,
		home:      home,
		cid:       cid,
		createdAt: p.o.Clock(),
	}, nil
}

func (p *Provider) runArgv(id core.SandboxID, spec core.SandboxSpec, env map[string]string, host string) []string {
	argv := []string{
		"run", "-d",
		"--name", "friday-sbx-" + string(id),
		"--network", "none",
		"--memory", fmt.Sprintf("%dm", spec.Limits.MemoryMB),
		"--cpus", strconv.Itoa(spec.Limits.CPUCores),
		"--pids-limit", strconv.Itoa(spec.Limits.MaxProcesses),
		"-v", host + ":" + guestRoot,
		"-w", guestRoot,
	}
	for _, k := range slices.Sorted(maps.Keys(env)) {
		argv = append(argv, "-e", k+"="+env[k])
	}
	for _, m := range spec.Mounts {
		specStr := m.Host + ":" + m.Guest
		if !m.Writable {
			specStr += ":ro"
		}
		argv = append(argv, "-v", specStr)
	}
	return append(argv, p.image, "sleep", "infinity")
}

var (
	_ core.SandboxProvider = (*Provider)(nil)
	_ core.Sandbox         = (*Sandbox)(nil)
	_ core.FileAccess      = (*Sandbox)(nil)
	_ core.Snapshotter     = (*Sandbox)(nil)
)
