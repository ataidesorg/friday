package evals

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ataidesorg/ink/internal/core"
	"github.com/ataidesorg/ink/internal/observability"
	"github.com/ataidesorg/ink/internal/redact"
	"github.com/ataidesorg/ink/internal/runtime"
	"github.com/ataidesorg/ink/internal/workspace"
)

// Runner executes a scenario on a private copy of its fixture and evaluates
// the expectations against the copy and the run's trail. Deps and Input are
// the caller's run graph; the runner replaces the task, project root,
// workspace, sandbox work dir, and event sink per scenario. A scripted
// provider is consumed by one run, so build a Runner per scenario.
type Runner struct {
	Deps     runtime.Deps
	Input    runtime.Input
	Redactor *redact.Redactor
	Privacy  core.PrivacyMode
	Clock    func() time.Time
	Version  string
	Commit   string
}

// Run executes one scenario. The fixture is never modified: the
// copy is removed when Run returns. Passed is true only when every check
// passed and the run itself did not fail.
func (r *Runner) Run(ctx context.Context, s core.EvaluationScenario) (core.EvaluationResult, error) {
	var out core.EvaluationResult
	if err := validate(s); err != nil {
		return out, err
	}
	for _, e := range s.Expectations {
		if e.Kind == core.ExpectMemoryWritten {
			return out, core.NotImplementedError{Feature: "memory_written expectation"}
		}
	}
	now := r.Clock
	if now == nil {
		now = time.Now
	}
	red := r.Redactor
	if red == nil {
		red = redact.New()
	}
	started := now()
	project := core.NewProjectID()
	ws, cleanup, err := workspace.Prepare(ctx, workspace.Options{Root: s.Fixture, Mode: workspace.ModeEphemeral, Project: project})
	if err != nil {
		return out, fmt.Errorf("scenario %s: %w", s.ID, err)
	}
	defer func() { _ = cleanup(context.WithoutCancel(ctx), false) }()

	res, events, lines, err := r.execute(ctx, s, ws, project, red)
	if err != nil {
		return out, fmt.Errorf("scenario %s: %w", s.ID, err)
	}
	checks, err := r.check(ctx, s, ws.Root, events, lines, red)
	if err != nil {
		return out, fmt.Errorf("scenario %s: %w", s.ID, err)
	}
	passed := res.Outcome.Kind != core.OutcomeFailed
	for _, c := range checks {
		passed = passed && c.Passed
	}
	return core.EvaluationResult{
		Scenario:       s.ID,
		Run:            res.Run.ID,
		Passed:         passed,
		Checks:         checks,
		Usage:          res.Usage,
		Cost:           res.Cost,
		Elapsed:        now().Sub(started),
		Failure:        res.Outcome.Category,
		HarnessVersion: r.Version,
		Commit:         r.Commit,
		Provider:       r.Deps.Provider.Descriptor().ID,
		Model:          r.Input.Model,
		Route:          "single",
	}, nil
}

func (r *Runner) execute(ctx context.Context, s core.EvaluationScenario, ws core.Workspace, project core.ProjectID, red *redact.Redactor) (runtime.Result, []core.Event, []string, error) {
	task, err := core.NewTask(s.Task, core.HarnessCode, core.NewProfileID(), core.NewSessionID(), core.Principal{Kind: core.PrincipalSystem, Name: "evals"})
	if err != nil {
		return runtime.Result{}, nil, nil, err
	}
	task.Budget = r.Input.Task.Budget
	in := r.Input
	in.Task, in.Workspace = task, ws
	in.Project.ID, in.Project.Root = project, ws.Root
	if in.Spec.WorkDir == "" { // zero spec: the caller left the sandbox to defaults
		in.Spec = core.NewSandboxSpec(ws.Root)
	}
	in.Spec.WorkDir = ws.Root
	trail := observability.NewLazyTrail(ws.Root, red, r.Privacy)
	d := r.Deps
	d.Sink = trail
	res, runErr := runtime.Run(ctx, d, in)
	if err := trail.Close(); err != nil && runErr == nil {
		runErr = err
	}
	if runErr != nil {
		return res, nil, nil, fmt.Errorf("run: %w", runErr)
	}
	if trail.Path() == "" {
		return res, nil, nil, nil
	}
	events, err := observability.ReadTrail(trail.Path())
	if err != nil {
		return res, nil, nil, err
	}
	raw, err := os.ReadFile(trail.Path()) //nolint:gosec // path minted by the trail under the workspace
	if err != nil {
		return res, nil, nil, err
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	return res, events, lines, nil
}

func (r *Runner) check(ctx context.Context, s core.EvaluationScenario, root string, events []core.Event, lines []string, red *redact.Redactor) ([]core.CheckResult, error) {
	spec := core.NewSandboxSpec(root)
	spec.Source = core.SandboxSource{Kind: core.SourceInPlace}
	sb, err := r.Deps.Sandbox.Create(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("check sandbox: %w", err)
	}
	defer func() { _ = sb.Destroy(context.WithoutCancel(ctx)) }()
	env := CheckEnv{Root: root, Events: events, Trail: lines, Exec: sb, Redactor: red, Goal: r.Input.Goal}
	checks := make([]core.CheckResult, 0, len(s.Expectations))
	for _, e := range s.Expectations {
		c, err := Check(ctx, e, env)
		if err != nil {
			return nil, fmt.Errorf("expectation %s: %w", e.Kind, err)
		}
		checks = append(checks, c)
	}
	return checks, nil
}
