package runtime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ataidesorg/friday/internal/core"
	"github.com/ataidesorg/friday/internal/tools"
)

// failure carries the category a phase wants recorded on the outcome.
type failure struct {
	cat core.FailureCategory
	err error
}

func (f failure) Error() string { return f.err.Error() }
func (f failure) Unwrap() error { return f.err }

func fail(cat core.FailureCategory, err error) error { return failure{cat: cat, err: err} }

// escalation aborts the run with outcome escalated: the block is one only
// a human can lift (a session or day budget), not a task failure.
type escalation struct{ reason string }

func (e escalation) Error() string { return e.reason }

func escalate(reason string) error { return escalation{reason: reason} }

type state struct {
	d  Deps
	in Input

	run         core.Run
	posture     core.PolicyPosture
	root        string
	sandbox     core.Sandbox
	tools       *tools.Registry
	msgs        []core.Message
	last        core.CompletionResponse
	usage       core.Usage
	cost        core.CostReport
	costUnknown bool
	costWarned  bool
	spendWarned bool
	dayLoaded   bool
	dayBroken   bool
	dayBase     core.USDMicros
	calls       int
	maxCalls    int
	verified    bool
	verify      string
	summary     string
	memories    []core.MemoryCandidate
	seq         uint64
	events      int
	goal        *core.Goal
	denied      map[string]bool
	wrote       map[string]bool
	ranOK       bool
}

// Run executes in to a terminal state and returns the result. The error is
// non-nil only when the run could not be recorded at all (invalid Deps or a
// failing Sink); every other failure is a terminal Outcome in the Result.
func Run(ctx context.Context, d Deps, in Input) (Result, error) {
	if err := d.validate(); err != nil {
		return Result{}, err
	}
	d = d.withDefaults()
	s := &state{d: d, in: in, root: in.Workspace.Root, run: core.NewRun(in.Task.ID, 1, d.Clock()), maxCalls: in.Task.Budget.MaxToolCalls}
	if in.Goal != nil {
		g := *in.Goal
		s.goal = &g
		s.d.Tools = s.d.Tools.WithGoal(s.loadGoal, s.saveGoal)
		s.bindGoalProof()
	}
	if s.maxCalls <= 0 {
		s.maxCalls = DefaultMaxToolCalls
	}
	if in.ResumeFrom != "" {
		c, err := LoadCheckpoint(in.ResumeFrom)
		if err != nil {
			return Result{}, err
		}
		s.hydrate(c)
	}
	if wall := in.Task.Budget.MaxWallClock; wall > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, wall)
		defer cancel()
	}
	err := s.loop(ctx)
	if ferr := s.finish(context.WithoutCancel(ctx)); err == nil {
		err = ferr
	}
	return s.result(), err
}

func (s *state) loop(ctx context.Context) error {
	if !s.run.State.Terminal() && s.run.State.Phase.Index() > core.PhaseIntake.Index() {
		if err := s.ensureSandbox(ctx); err != nil {
			t := s.failTransition(ctx, err)
			return s.apply(ctx, t)
		}
	}
	for !s.run.State.Terminal() {
		phase := s.run.State.Phase
		s.d.Observer.OnPhase(phase)
		t, err := s.step(ctx, phase)
		if err != nil {
			t = s.failTransition(ctx, err)
		}
		if err := s.apply(ctx, t); err != nil {
			return err
		}
	}
	return nil
}

func (s *state) step(ctx context.Context, p core.Phase) (core.Transition, error) {
	switch p {
	case core.PhaseIntake:
		return s.intake(ctx)
	case core.PhasePreflight:
		return s.preflight(ctx)
	case core.PhaseContextAssembly:
		return s.assemble(ctx)
	case core.PhaseModelSelection:
		return s.selectModel(ctx)
	case core.PhasePlanning:
		return s.plan(ctx)
	case core.PhaseToolExecution:
		return s.execute(ctx)
	case core.PhaseValidation:
		return s.validate(ctx)
	case core.PhaseSynthesis:
		return s.synthesise(ctx)
	case core.PhaseMemoryExtraction:
		return s.extract(ctx)
	case core.PhaseTelemetryCapture:
		return core.Transition{Kind: core.TransitionComplete, Verified: s.verified, Reason: s.verify}, nil
	}
	return core.Transition{}, fmt.Errorf("%w: phase %q", core.ErrInvalidInput, p)
}

// failTransition maps a phase error to a terminal fail transition: an
// explicit failure keeps its category, a cancelled or expired context is
// user_aborted or timeout, anything else is internal.
func (s *state) failTransition(ctx context.Context, err error) core.Transition {
	var esc escalation
	if errors.As(err, &esc) {
		return core.Transition{Kind: core.TransitionEscalate, Reason: esc.reason}
	}
	cat := core.FailureInternal
	var f failure
	switch {
	case errors.As(err, &f):
		cat = f.cat
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		cat = core.FailureTimeout
	case ctx.Err() != nil:
		cat = core.FailureUserAborted
	}
	return core.Transition{Kind: core.TransitionFail, Category: cat, Message: err.Error()}
}

func (s *state) apply(ctx context.Context, t core.Transition) error {
	next, err := s.run.Transition(t, s.now())
	if err != nil {
		if t.Kind == core.TransitionFail {
			return fmt.Errorf("apply %s: %w", t.Kind, err)
		}
		return s.apply(ctx, core.Transition{Kind: core.TransitionFail, Category: core.FailureInternal, Message: err.Error()})
	}
	from := s.run.State
	s.run = next
	if err := s.emit(ctx, core.StateChanged{From: from, To: next.State, Transition: t.Kind}); err != nil {
		return err
	}
	return s.persistCheckpoint()
}

// finish destroys the sandbox and emits task_finished; it runs on every
// path so the trail always closes.
func (s *state) finish(ctx context.Context) error {
	if s.sandbox != nil {
		id := s.sandbox.Info().ID
		err := s.sandbox.Destroy(ctx)
		s.sandbox = nil
		if err != nil {
			if err := s.emit(ctx, core.Warning{Message: fmt.Sprintf("sandbox %s destroy: %v", id, err)}); err != nil {
				return err
			}
		} else if err := s.emit(ctx, core.SandboxDestroyed{Sandbox: id}); err != nil {
			return err
		}
	}
	if sp := s.d.Spend; sp != nil && s.cost.Actual != nil {
		entry := SpendEntry{Date: s.now().Format(dayLayout), Run: string(s.run.ID), USDMicros: *s.cost.Actual}
		if err := sp.Commit(entry); err != nil {
			if err := s.emit(ctx, core.Warning{Message: fmt.Sprintf("spend ledger append: %v", err)}); err != nil {
				return err
			}
		}
	}
	out := s.outcome()
	return s.emit(ctx, core.TaskFinished{Outcome: out, Elapsed: s.run.Elapsed(s.now()), Usage: s.usage, Cost: s.cost, Failure: out.Category})
}

func (s *state) outcome() core.Outcome {
	if o := s.run.State.Outcome; o != nil {
		return *o
	}
	return core.Outcome{Kind: core.OutcomeFailed, Reason: "run did not reach a terminal state", Category: core.FailureInternal}
}

func (s *state) result() Result {
	res := Result{Run: s.run, Outcome: s.outcome(), Usage: s.usage, Cost: s.cost, Summary: s.summary, Memories: s.memories, Events: s.events}
	if s.goal != nil {
		g := *s.goal
		res.Goal = &g
		res.ContinueGoal = g.Continues()
	}
	return res
}

func (s *state) loadGoal() (core.Goal, bool) {
	if s.goal == nil {
		return core.Goal{}, false
	}
	return *s.goal, true
}

func (s *state) bindGoalProof() {
	t, ok := s.d.Tools.Get("goal_complete")
	if !ok {
		return
	}
	g, ok := t.(*tools.GoalComplete)
	if !ok {
		return
	}
	g.Proof = s.proveGoal
}

func (s *state) proveGoal(kind core.GoalEvidenceKind, path, _ string) error {
	switch kind {
	case core.GoalEvidenceFile:
		if path == "" {
			return fmt.Errorf("%w: file evidence needs a path", core.ErrInvalidInput)
		}
		if !s.wrote[path] {
			return fmt.Errorf("%w: file %s was not written this run", core.ErrInvalidInput, path)
		}
	case core.GoalEvidenceCommand, core.GoalEvidenceTest:
		if !s.ranOK {
			return fmt.Errorf("%w: no successful command ran this run", core.ErrInvalidInput)
		}
	}
	return nil
}

func (s *state) saveGoal(g core.Goal) error {
	cp := g
	s.goal = &cp
	if s.in.SaveGoal != nil {
		return s.in.SaveGoal(g)
	}
	return nil
}

func (s *state) now() time.Time { return s.d.Clock() }

func (s *state) emit(ctx context.Context, data core.EventData) error {
	s.seq++
	e := core.NewEvent(s.in.Task.ID, s.run.ID, s.seq, s.now(), data)
	if err := s.d.Sink.Emit(ctx, e); err != nil {
		return fmt.Errorf("emit %s: %w", e.Kind, err)
	}
	s.events++
	s.d.Observer.OnEvent(e)
	return nil
}
