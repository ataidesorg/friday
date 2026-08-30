package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ataidesorg/ink/internal/core"
)

const customToolTimeout = 120 * time.Second

// permissiveSchema accepts any JSON object when a custom tool declares none.
var permissiveSchema = json.RawMessage(`{"type":"object"}`)

// CustomTool is a config-declared argv tool. The argv is fixed at
// declaration; the model's arguments JSON is piped to the command's stdin.
// Policy gates it like any built-in — by name and by its command scope.
type CustomTool struct {
	name, description string
	risk              core.RiskClass
	schema            json.RawMessage
	argv              []string
	exec              Executor
}

// NewCustomTool builds a custom tool; validation upstream guarantees a
// non-empty argv and a known risk class.
func NewCustomTool(name, description string, risk core.RiskClass, schema json.RawMessage, argv []string) *CustomTool {
	if len(schema) == 0 {
		schema = permissiveSchema
	}
	if risk == "" {
		risk = core.RiskExecuteLocal
	}
	return &CustomTool{name: name, description: description, risk: risk, schema: schema, argv: slices.Clone(argv)}
}

// Spec describes the tool to the model.
func (t *CustomTool) Spec() core.ToolSpec {
	return core.ToolSpec{Name: t.name, Description: t.description, Risk: t.risk, InputSchema: t.schema}
}

// Capability scopes every call to the declared argv.
func (t *CustomTool) Capability(core.ToolInput) core.Capability {
	return core.Capability{Risk: t.risk, Scope: core.ResourceScope{Kind: core.ScopeCommand, Argv: slices.Clone(t.argv)}}
}

func (t *CustomTool) bindExec(exec Executor) core.Tool {
	c := *t
	c.exec = exec
	return &c
}

// Invoke runs the declared argv in the sandbox with the arguments on stdin.
func (t *CustomTool) Invoke(ctx context.Context, in core.ToolInput, tc core.ToolContext) (core.ToolOutput, error) {
	if err := requireRoot(tc); err != nil {
		return core.ToolOutput{}, err
	}
	if t.exec == nil {
		return core.ToolOutput{}, fmt.Errorf("%w: custom tool %s has no sandbox bound", core.ErrUnavailable, t.name)
	}
	r, err := t.exec.Exec(ctx, core.ExecRequest{Argv: slices.Clone(t.argv), Stdin: string(in.Arguments), Timeout: customToolTimeout})
	if err != nil {
		return core.ToolOutput{}, fmt.Errorf("run %q: %w", t.argv[0], err)
	}
	res := struct {
		ExitCode  int    `json:"exit_code"`
		Stdout    string `json:"stdout"`
		Stderr    string `json:"stderr"`
		TimedOut  bool   `json:"timed_out"`
		Truncated bool   `json:"truncated"`
	}{r.ExitCode, r.Stdout, r.Stderr, r.TimedOut, r.Truncated}
	var sb strings.Builder
	fmt.Fprintf(&sb, "exit %d", r.ExitCode)
	if r.TimedOut {
		sb.WriteString(" (timed out)")
	}
	sb.WriteString("\n")
	sb.WriteString(r.Stdout)
	if r.Stderr != "" {
		sb.WriteString("\nstderr:\n")
		sb.WriteString(r.Stderr)
	}
	return output(sb.String(), res, t.Capability(in))
}
