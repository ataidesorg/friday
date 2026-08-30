package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ataidesorg/ink/internal/core"
)

const formatTimeout = 60 * time.Second

// FormatRule is one post-write formatter: the command (the written file's
// path is appended) and the extensions it covers.
type FormatRule struct {
	Command    []string
	Extensions []string
}

// WrapFormatters decorates write_file and apply_patch so a matching
// formatter runs in the sandbox after each successful write. A formatter
// failure appends a warning to the tool output and never blocks the turn.
func WrapFormatters(r *Registry, rules []FormatRule) *Registry {
	if r == nil || len(rules) == 0 {
		return r
	}
	m := maps.Clone(r.tools)
	for _, name := range []string{"write_file", "apply_patch"} {
		if t, ok := m[name]; ok {
			m[name] = &formatted{inner: t, rules: rules}
		}
	}
	return &Registry{tools: m}
}

// formatted decorates a writing tool with post-write formatting. It forwards
// the inner tool's spec and capability so policy sees the real action.
type formatted struct {
	inner core.Tool
	rules []FormatRule
	exec  Executor
}

func (f *formatted) Spec() core.ToolSpec { return f.inner.Spec() }

func (f *formatted) Capability(in core.ToolInput) core.Capability {
	return CapabilityOf(f.inner, in)
}

func (f *formatted) bindExec(exec Executor) core.Tool {
	inner := f.inner
	if b, ok := inner.(execBinder); ok {
		inner = b.bindExec(exec)
	}
	return &formatted{inner: inner, rules: f.rules, exec: exec}
}

// Invoke runs the inner tool, then formats each written file that matches a
// rule. Formatting is best-effort: any failure becomes a warning line.
func (f *formatted) Invoke(ctx context.Context, in core.ToolInput, tc core.ToolContext) (core.ToolOutput, error) {
	out, err := f.inner.Invoke(ctx, in, tc)
	if err != nil || f.exec == nil {
		return out, err
	}
	for _, p := range writtenPaths(f.inner.Spec().Name, in, out) {
		rule, ok := ruleFor(f.rules, p)
		if !ok {
			continue
		}
		r, ferr := f.exec.Exec(ctx, core.ExecRequest{Argv: append(slices.Clone(rule.Command), p), Timeout: formatTimeout})
		switch {
		case ferr != nil:
			out.Content += fmt.Sprintf("\nformatter warning: %s: %v", p, ferr)
		case r.ExitCode != 0:
			out.Content += fmt.Sprintf("\nformatter warning: %s exited %d: %s", p, r.ExitCode, clipFormatterOutput(r))
		}
	}
	return out, nil
}

// writtenPaths names the workspace-relative files a successful call wrote:
// write_file's path argument, or apply_patch's files_changed result.
func writtenPaths(tool string, in core.ToolInput, out core.ToolOutput) []string {
	switch tool {
	case "write_file":
		var a struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(in.Arguments, &a) == nil && a.Path != "" {
			return []string{a.Path}
		}
	case "apply_patch":
		var s struct {
			FilesChanged []string `json:"files_changed"`
		}
		if json.Unmarshal(out.Structured, &s) == nil {
			return s.FilesChanged
		}
	}
	return nil
}

func ruleFor(rules []FormatRule, path string) (FormatRule, bool) {
	ext := filepath.Ext(path)
	if ext == "" {
		return FormatRule{}, false
	}
	for _, r := range rules {
		if len(r.Command) > 0 && slices.Contains(r.Extensions, ext) {
			return r, true
		}
	}
	return FormatRule{}, false
}

func clipFormatterOutput(r core.ExecResult) string {
	msg := strings.TrimSpace(r.Stderr)
	if msg == "" {
		msg = strings.TrimSpace(r.Stdout)
	}
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	return msg
}
