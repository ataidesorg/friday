package core

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/ataidesorg/friday/internal/redact"
)

// Mount maps a host path into the sandbox.
type Mount struct {
	Host     string `json:"host"`
	Guest    string `json:"guest"`
	Writable bool   `json:"writable"`
}

// ResourceLimits caps what a sandbox may consume.
type ResourceLimits struct {
	CPUCores     int           `json:"cpu_cores"`
	MemoryMB     int64         `json:"memory_mb"`
	DiskMB       int64         `json:"disk_mb"`
	MaxProcesses int           `json:"max_processes"`
	WallClock    time.Duration `json:"wall_clock"`
}

// DefaultResourceLimits returns the conservative defaults.
func DefaultResourceLimits() ResourceLimits {
	return ResourceLimits{CPUCores: 1, MemoryMB: 2048, DiskMB: 1024, MaxProcesses: 64, WallClock: 10 * time.Minute}
}

// SourceKind says how the sandbox obtains its working tree.
type SourceKind string

// Source kinds. An empty kind means SourceCopy.
const (
	// SourceCopy runs commands in a private copy of WorkDir (the default).
	SourceCopy SourceKind = "copy"
	// SourceInPlace runs commands directly in WorkDir; callers must have
	// prepared WorkDir through the workspace guard first.
	SourceInPlace SourceKind = "in_place"
)

// SandboxSource selects the working tree and what a copy leaves out.
// Exclude holds slash-separated glob patterns relative to WorkDir; a pattern
// ending in "/**" excludes a whole subtree. Nothing is excluded by default,
// so `.git` is copied and tests can run git inside the sandbox.
type SandboxSource struct {
	Kind    SourceKind `json:"kind,omitempty"`
	Exclude []string   `json:"exclude,omitempty"`
}

// SandboxSpec fully describes an isolated execution environment.
// WallClock is the whole sandbox's lifetime; Limits.WallClock caps one command.
type SandboxSpec struct {
	WorkDir           string            `json:"work_dir"`
	Source            SandboxSource     `json:"source"`
	Mounts            []Mount           `json:"mounts,omitempty"`
	Env               map[string]string `json:"env,omitempty"`
	Limits            ResourceLimits    `json:"limits"`
	WallClock         time.Duration     `json:"wall_clock,omitempty"`
	AllowSecretAccess bool              `json:"allow_secret_access"`
}

// NewSandboxSpec returns a closed-by-default spec: private copy, no secret
// access, default limits. Egress is the provider's to enforce, not the
// spec's: container runs --network none, process cannot isolate at all.
func NewSandboxSpec(workdir string) SandboxSpec {
	return SandboxSpec{
		WorkDir: workdir,
		Source:  SandboxSource{Kind: SourceCopy},
		Limits:  DefaultResourceLimits(),
	}
}

// SnapshotRef names a captured sandbox state.
type SnapshotRef struct {
	ID        string    `json:"id"`
	Provider  string    `json:"provider"`
	CreatedAt time.Time `json:"created_at"`
}

// Validate fails closed: absolute paths, positive limits, and no
// secret-shaped env values.
func (s SandboxSpec) Validate(r *redact.Redactor) error {
	if !filepath.IsAbs(s.WorkDir) {
		return fmt.Errorf("%w: sandbox work_dir must be absolute", ErrInvalidInput)
	}
	for _, m := range s.Mounts {
		if !filepath.IsAbs(m.Host) || !filepath.IsAbs(m.Guest) {
			return fmt.Errorf("%w: mount paths must be absolute (%s -> %s)", ErrInvalidInput, m.Host, m.Guest)
		}
	}
	if err := s.Limits.validate(); err != nil {
		return err
	}
	switch s.Source.Kind {
	case "", SourceCopy, SourceInPlace:
	default:
		return fmt.Errorf("%w: unknown source kind %q", ErrInvalidInput, s.Source.Kind)
	}
	if s.WallClock < 0 {
		return fmt.Errorf("%w: sandbox wall_clock must not be negative", ErrInvalidInput)
	}
	if r == nil {
		r = redact.New()
	}
	for k, v := range s.Env {
		if r.ContainsSecret(v) {
			return fmt.Errorf("%w: env %s looks like a secret", ErrSecretContent, k)
		}
	}
	return nil
}

func (l ResourceLimits) validate() error {
	if l.CPUCores <= 0 || l.MemoryMB <= 0 || l.DiskMB <= 0 || l.MaxProcesses <= 0 || l.WallClock <= 0 {
		return fmt.Errorf("%w: every sandbox limit must be positive", ErrInvalidInput)
	}
	return nil
}

// SandboxInfo describes a created sandbox.
type SandboxInfo struct {
	ID       SandboxID   `json:"id"`
	Provider string      `json:"provider"`
	Spec     SandboxSpec `json:"spec"`
}

// ExecRequest is one command to run inside a sandbox.
type ExecRequest struct {
	Argv    []string      `json:"argv"`
	Dir     string        `json:"dir,omitempty"`
	Stdin   string        `json:"stdin,omitempty"`
	Timeout time.Duration `json:"timeout,omitempty"`
}

// ExecResult is the structured outcome of an ExecRequest.
type ExecResult struct {
	ExitCode  int           `json:"exit_code"`
	Stdout    string        `json:"stdout"`
	Stderr    string        `json:"stderr"`
	Elapsed   time.Duration `json:"elapsed"`
	TimedOut  bool          `json:"timed_out"`
	Truncated bool          `json:"truncated"`
}
