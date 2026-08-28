package tools

import (
	"context"
	"maps"
	"path/filepath"

	"github.com/ataidesorg/friday/internal/core"
)

// Diagnoser reports post-edit diagnostics for one absolute path; empty means
// nothing to say. Implemented by lsp.Manager.
type Diagnoser interface {
	Diagnose(ctx context.Context, path string) string
}

// WrapDiagnostics decorates write_file and apply_patch so language-server
// diagnostics for each written file land in the tool output. Runs outside
// WrapFormatters so diagnostics see the formatted file. Best-effort: a
// silent or broken server never blocks the turn.
func WrapDiagnostics(r *Registry, d Diagnoser) *Registry {
	if r == nil || d == nil {
		return r
	}
	m := maps.Clone(r.tools)
	for _, name := range []string{"write_file", "apply_patch"} {
		if t, ok := m[name]; ok {
			m[name] = &diagnosed{inner: t, d: d}
		}
	}
	return &Registry{tools: m}
}

// diagnosed forwards the inner tool's spec, capability, and executor binding
// so policy and the sandbox see the real action.
type diagnosed struct {
	inner core.Tool
	d     Diagnoser
}

func (g *diagnosed) Spec() core.ToolSpec { return g.inner.Spec() }

func (g *diagnosed) Capability(in core.ToolInput) core.Capability {
	return CapabilityOf(g.inner, in)
}

func (g *diagnosed) bindExec(exec Executor) core.Tool {
	inner := g.inner
	if b, ok := inner.(execBinder); ok {
		inner = b.bindExec(exec)
	}
	return &diagnosed{inner: inner, d: g.d}
}

func (g *diagnosed) Invoke(ctx context.Context, in core.ToolInput, tc core.ToolContext) (core.ToolOutput, error) {
	out, err := g.inner.Invoke(ctx, in, tc)
	if err != nil {
		return out, err
	}
	for _, p := range writtenPaths(g.inner.Spec().Name, in, out) {
		abs := p
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(tc.WorkspaceRoot, p)
		}
		if text := g.d.Diagnose(ctx, abs); text != "" {
			out.Content += "\n" + text
		}
	}
	return out, nil
}
