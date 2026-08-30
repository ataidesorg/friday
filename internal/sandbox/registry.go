// Package sandbox holds the provider-independent pieces of the sandbox
// layer: construction options, the provider registry, and the optional
// capability probes. Providers live in sub-packages (process, container)
// and are the only code allowed to import os/exec.
package sandbox

import (
	"fmt"
	"time"

	"github.com/ataidesorg/ink/internal/core"
	"github.com/ataidesorg/ink/internal/redact"
)

// DefaultMaxOutputBytes caps each of stdout and stderr per command.
const DefaultMaxOutputBytes = 1 << 20

// Options configures a provider. Zero values mean: a redactor with no
// literals, workspaces removed on Destroy, 1 MiB output cap, time.Now.
type Options struct {
	Redactor       *redact.Redactor
	KeepWorkspace  bool
	MaxOutputBytes int
	Clock          func() time.Time
}

// WithDefaults returns a copy with every zero field filled in.
func (o Options) WithDefaults() Options {
	if o.Redactor == nil {
		o.Redactor = redact.New()
	}
	if o.MaxOutputBytes <= 0 {
		o.MaxOutputBytes = DefaultMaxOutputBytes
	}
	if o.Clock == nil {
		o.Clock = time.Now
	}
	return o
}

// Factory builds a provider from options.
type Factory func(Options) core.SandboxProvider

// Registry maps `sandbox.provider` config values to factories. The CLI
// composes it from the provider packages so this package never imports them.
type Registry map[string]Factory

// New builds the named provider; an unknown name is core.ErrNotFound.
func (r Registry) New(name string, o Options) (core.SandboxProvider, error) {
	f, ok := r[name]
	if !ok || f == nil {
		return nil, fmt.Errorf("%w: sandbox provider %q", core.ErrNotFound, name)
	}
	return f(o), nil
}
