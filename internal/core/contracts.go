package core

import (
	"context"
	"io/fs"
)

// ModelProvider is a model backend. Credentials live in the adapter, never
// in the descriptor.
type ModelProvider interface {
	Descriptor() ProviderDescriptor
	Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error)
}

// Tool is a capability the model may request; every Invoke is policy-checked
// by the caller before it runs.
type Tool interface {
	Spec() ToolSpec
	Invoke(ctx context.Context, in ToolInput, tc ToolContext) (ToolOutput, error)
}

// SandboxProvider creates isolated execution environments.
type SandboxProvider interface {
	Name() string
	Create(ctx context.Context, spec SandboxSpec) (Sandbox, error)
}

// Sandbox runs commands under a validated SandboxSpec.
type Sandbox interface {
	Info() SandboxInfo
	Exec(ctx context.Context, req ExecRequest) (ExecResult, error)
	Destroy(ctx context.Context) error
}

// FileAccess is an optional Sandbox capability: direct file I/O inside the
// sandbox tree. Paths are relative to the sandbox root and confined to it.
type FileAccess interface {
	ReadFile(ctx context.Context, path string) ([]byte, error)
	WriteFile(ctx context.Context, path string, data []byte, mode fs.FileMode) error
}

// Snapshotter is an optional Sandbox capability: capture the current tree.
type Snapshotter interface {
	Snapshot(ctx context.Context) (SnapshotRef, error)
}

// PolicyEngine decides whether a capability request may proceed.
type PolicyEngine interface {
	Evaluate(req CapabilityRequest, pc PolicyContext) PolicyDecision
}
