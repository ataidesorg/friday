// Package tools holds the typed tools the model may call. Every tool
// validates its arguments against an embedded JSON schema, confines paths to
// the workspace root, and never spawns a shell.
package tools

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/ataidesorg/ink/internal/core"
)

// Executor runs a command. core.Sandbox satisfies it.
type Executor interface {
	Exec(ctx context.Context, req core.ExecRequest) (core.ExecResult, error)
}

// execBinder is implemented by tools that run through the sandbox executor;
// WithExecutor rebinds each one when the sandbox comes up.
type execBinder interface {
	bindExec(exec Executor) core.Tool
}

// Scoper is implemented by tools that can name the resource a call touches
// before it runs, so policy can judge the exact scope.
type Scoper interface {
	Capability(in core.ToolInput) core.Capability
}

// Registry maps tool names to implementations. It is immutable after construction.
type Registry struct {
	tools map[string]core.Tool
}

// NewRegistry builds a registry; a duplicate name is an error.
func NewRegistry(ts ...core.Tool) (*Registry, error) {
	m := make(map[string]core.Tool, len(ts))
	for _, t := range ts {
		name := t.Spec().Name
		if name == "" {
			return nil, fmt.Errorf("%w: tool with empty name", core.ErrInvalidInput)
		}
		if _, dup := m[name]; dup {
			return nil, fmt.Errorf("%w: duplicate tool %q", core.ErrConflict, name)
		}
		m[name] = t
	}
	return &Registry{tools: m}, nil
}

// Default returns the built-in tools. exec may be nil until WithExecutor
// binds a sandbox; AskUser has no AskFunc until WithAskUser.
func Default(exec Executor, allowedArgv [][]string) *Registry {
	r, _ := NewRegistry( //nolint:errcheck // fixed distinct names cannot collide
		&ReadFile{}, &ListDir{}, &Search{}, &WriteFile{}, &ApplyPatch{},
		&RunCommand{exec: exec, allowed: cloneArgv(allowedArgv)},
		&AskUser{},
		&TodoWrite{},
		&GoalComplete{}, &GoalBlocked{}, &GoalWait{},
	)
	return r
}

// With returns a copy of the registry extended with more tools; a name
// already registered is a conflict, never a silent override.
func (r *Registry) With(ts ...core.Tool) (*Registry, error) {
	m := make(map[string]core.Tool, len(r.tools)+len(ts))
	for k, v := range r.tools {
		m[k] = v
	}
	for _, t := range ts {
		name := t.Spec().Name
		if name == "" {
			return nil, fmt.Errorf("%w: tool with empty name", core.ErrInvalidInput)
		}
		if _, dup := m[name]; dup {
			return nil, fmt.Errorf("%w: duplicate tool %q", core.ErrConflict, name)
		}
		m[name] = t
	}
	return &Registry{tools: m}, nil
}

// Filter returns a copy holding only the named tools; an empty list keeps
// everything. An unknown name is skipped — the agent simply lacks it.
func (r *Registry) Filter(names []string) *Registry {
	if len(names) == 0 {
		return r
	}
	m := make(map[string]core.Tool, len(names))
	for _, n := range names {
		if t, ok := r.tools[n]; ok {
			m[n] = t
		}
	}
	return &Registry{tools: m}
}

// Get looks a tool up by name.
func (r *Registry) Get(name string) (core.Tool, bool) {
	if r == nil {
		return nil, false
	}
	t, ok := r.tools[name]
	return t, ok
}

// Specs lists every tool spec sorted by name.
func (r *Registry) Specs() []core.ToolSpec {
	if r == nil {
		return nil
	}
	out := make([]core.ToolSpec, 0, len(r.tools))
	for _, name := range slices.Sorted(maps.Keys(r.tools)) {
		out = append(out, r.tools[name].Spec())
	}
	return out
}

type askBinder interface {
	bindAsk(ask core.AskFunc) core.Tool
}

// WithAskUser returns a copy whose ask_user_question tool calls ask.
func (r *Registry) WithAskUser(ask core.AskFunc) *Registry {
	if r == nil {
		return nil
	}
	m := maps.Clone(r.tools)
	for name, t := range m {
		if b, ok := t.(askBinder); ok {
			m[name] = b.bindAsk(ask)
		}
	}
	return &Registry{tools: m}
}

type todoBinder interface {
	bindTodos(load func() []TodoItem, save func([]TodoItem) error) core.Tool
}

type goalBinder interface {
	bindGoal(load func() (core.Goal, bool), save func(core.Goal) error) core.Tool
}

// WithGoal returns a copy whose goal_* tools read and write the session goal.
func (r *Registry) WithGoal(load func() (core.Goal, bool), save func(core.Goal) error) *Registry {
	if r == nil {
		return nil
	}
	m := maps.Clone(r.tools)
	for name, t := range m {
		if b, ok := t.(goalBinder); ok {
			m[name] = b.bindGoal(load, save)
		}
	}
	return &Registry{tools: m}
}

// WithTodos returns a copy whose todo_write tool reads and writes the session list.
func (r *Registry) WithTodos(load func() []TodoItem, save func([]TodoItem) error) *Registry {
	if r == nil {
		return nil
	}
	m := maps.Clone(r.tools)
	for name, t := range m {
		if b, ok := t.(todoBinder); ok {
			m[name] = b.bindTodos(load, save)
		}
	}
	return &Registry{tools: m}
}

// WithExecutor returns a copy of the registry whose command tool runs through exec.
func (r *Registry) WithExecutor(exec Executor) *Registry {
	if r == nil {
		return nil
	}
	m := maps.Clone(r.tools)
	for name, t := range m {
		if b, ok := t.(execBinder); ok {
			m[name] = b.bindExec(exec)
		}
	}
	return &Registry{tools: m}
}

// CapabilityOf names the capability a call needs: the tool's own scope when it
// can tell, otherwise its risk class over any resource.
func CapabilityOf(t core.Tool, in core.ToolInput) core.Capability {
	if s, ok := t.(Scoper); ok {
		return s.Capability(in)
	}
	return core.Capability{Risk: t.Spec().Risk, Scope: core.ResourceScope{Kind: core.ScopeAny}}
}

func cloneArgv(in [][]string) [][]string {
	out := make([][]string, 0, len(in))
	for _, a := range in {
		if len(a) > 0 {
			out = append(out, slices.Clone(a))
		}
	}
	return out
}

func pathScope(risk core.RiskClass, rel string) core.Capability {
	clean, err := cleanRel(rel)
	if err != nil {
		clean = rel
	}
	return core.Capability{Risk: risk, Scope: core.ResourceScope{Kind: core.ScopePath, Path: clean}}
}

func requireRoot(tc core.ToolContext) error {
	if tc.WorkspaceRoot == "" {
		return fmt.Errorf("%w: tool context has no workspace root", core.ErrInvalidInput)
	}
	return nil
}
