package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ataidesorg/friday/internal/core"
)

const maxCommandTimeout = 600 * time.Second

// RunCommand executes an allow-listed argv through the bound sandbox executor.
type RunCommand struct {
	exec    Executor
	allowed [][]string
}

type runCommandArgs struct {
	Argv        []string `json:"argv"`
	Dir         string   `json:"dir,omitempty"`
	TimeoutSecs int      `json:"timeout_secs,omitempty"`
}

// Spec describes the tool.
func (*RunCommand) Spec() core.ToolSpec {
	return core.ToolSpec{Name: "run_command", Description: "Run an allow-listed command in the sandbox. No shell.", Risk: core.RiskExecuteLocal, InputSchema: schema("run_command")}
}

// Capability scopes the call to the exact argv.
func (*RunCommand) Capability(in core.ToolInput) core.Capability {
	var a runCommandArgs
	_ = json.Unmarshal(in.Arguments, &a)
	return core.Capability{Risk: core.RiskExecuteLocal, Scope: core.ResourceScope{Kind: core.ScopeCommand, Argv: slices.Clone(a.Argv)}}
}

func (t *RunCommand) bind(exec Executor) *RunCommand {
	return &RunCommand{exec: exec, allowed: t.allowed}
}

func (t *RunCommand) bindExec(exec Executor) core.Tool { return t.bind(exec) }

// Allowed reports whether argv starts with one of the allowed prefixes (exact elements).
func (t *RunCommand) Allowed(argv []string) bool {
	for _, prefix := range t.allowed {
		if len(prefix) <= len(argv) && slices.Equal(prefix, argv[:len(prefix)]) {
			return true
		}
	}
	return false
}

// Invoke checks the allow-list, then runs argv in the sandbox.
func (t *RunCommand) Invoke(ctx context.Context, in core.ToolInput, tc core.ToolContext) (core.ToolOutput, error) {
	if err := requireRoot(tc); err != nil {
		return core.ToolOutput{}, err
	}
	var a runCommandArgs
	if err := decodeArgs("run_command", in.Arguments, &a); err != nil {
		return core.ToolOutput{}, err
	}
	if len(a.Argv) == 0 || a.Argv[0] == "" {
		return core.ToolOutput{}, fmt.Errorf("%w: argv is empty", core.ErrInvalidInput)
	}
	if !t.Allowed(a.Argv) {
		return core.ToolOutput{}, fmt.Errorf("%w: command %q is not in tools.commands.allowed", core.ErrPolicyDenied, strings.Join(a.Argv, " "))
	}
	dir, err := cleanRel(a.Dir)
	if err != nil {
		return core.ToolOutput{}, err
	}
	if _, err := confine(tc.WorkspaceRoot, dir); err != nil {
		return core.ToolOutput{}, err
	}
	if t.exec == nil {
		return core.ToolOutput{}, fmt.Errorf("%w: run_command has no sandbox bound", core.ErrUnavailable)
	}
	timeout := time.Duration(a.TimeoutSecs) * time.Second
	if timeout > maxCommandTimeout {
		return core.ToolOutput{}, fmt.Errorf("%w: timeout_secs %d exceeds %d", core.ErrInvalidInput, a.TimeoutSecs, int(maxCommandTimeout.Seconds()))
	}
	r, err := t.exec.Exec(ctx, core.ExecRequest{Argv: slices.Clone(a.Argv), Dir: dir, Timeout: timeout})
	if err != nil {
		return core.ToolOutput{}, fmt.Errorf("run %q: %w", a.Argv[0], err)
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
