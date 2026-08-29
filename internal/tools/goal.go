package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ataidesorg/friday/internal/core"
)

// GoalComplete records evidence-backed done for the session goal.
type GoalComplete struct {
	Load func() (core.Goal, bool)
	Save func(core.Goal) error
	Now  func() time.Time
}

// GoalBlocked records a repeated impasse.
type GoalBlocked struct {
	Load func() (core.Goal, bool)
	Save func(core.Goal) error
	Now  func() time.Time
}

// GoalWait pauses automatic continuation until an external wake.
type GoalWait struct {
	Load func() (core.Goal, bool)
	Save func(core.Goal) error
	Now  func() time.Time
}

func (t *GoalComplete) bindGoal(load func() (core.Goal, bool), save func(core.Goal) error) core.Tool {
	return &GoalComplete{Load: load, Save: save, Now: t.Now}
}
func (t *GoalBlocked) bindGoal(load func() (core.Goal, bool), save func(core.Goal) error) core.Tool {
	return &GoalBlocked{Load: load, Save: save, Now: t.Now}
}
func (t *GoalWait) bindGoal(load func() (core.Goal, bool), save func(core.Goal) error) core.Tool {
	return &GoalWait{Load: load, Save: save, Now: t.Now}
}

func goalNow(now func() time.Time) time.Time {
	if now != nil {
		return now()
	}
	return time.Now()
}

func loadOpenGoal(load func() (core.Goal, bool), want string) (core.Goal, error) {
	if load == nil {
		return core.Goal{}, fmt.Errorf("%w: goal tools need a session", core.ErrUnavailable)
	}
	g, ok := load()
	if !ok || g.ID == "" {
		return core.Goal{}, fmt.Errorf("%w: no active goal", core.ErrNotFound)
	}
	if want != "" && string(g.ID) != strings.TrimSpace(want) {
		return core.Goal{}, fmt.Errorf("%w: stale goal id", core.ErrInvalidInput)
	}
	return g, nil
}

// Spec describes the tool.
func (*GoalComplete) Spec() core.ToolSpec {
	return core.ToolSpec{
		Name:        "goal_complete",
		Description: "Mark the session goal complete with evidence. Use a command, test, file, or eval check — never prose that the work looks done.",
		Risk:        core.RiskReadOnly,
		InputSchema: schema("goal_complete"),
	}
}

type goalCompleteArgs struct {
	GoalID  string `json:"goal_id"`
	Kind    string `json:"kind"`
	Summary string `json:"summary"`
}

// Invoke completes the current goal.
func (t *GoalComplete) Invoke(_ context.Context, in core.ToolInput, _ core.ToolContext) (core.ToolOutput, error) {
	var a goalCompleteArgs
	if err := decodeArgs("goal_complete", in.Arguments, &a); err != nil {
		return core.ToolOutput{}, err
	}
	if t == nil || t.Save == nil {
		return core.ToolOutput{}, fmt.Errorf("%w: goal_complete needs a session", core.ErrUnavailable)
	}
	g, err := loadOpenGoal(t.Load, a.GoalID)
	if err != nil {
		return core.ToolOutput{}, err
	}
	next, err := g.Complete(core.GoalEvidenceKind(a.Kind), a.Summary, goalNow(t.Now))
	if err != nil {
		return core.ToolOutput{}, err
	}
	if err := t.Save(next); err != nil {
		return core.ToolOutput{}, err
	}
	return output("goal complete: "+string(next.EvidenceKind)+" — "+next.Evidence, next, core.Capability{Risk: core.RiskReadOnly, Scope: core.ResourceScope{Kind: core.ScopeAny}})
}

// Spec describes the tool.
func (*GoalBlocked) Spec() core.ToolSpec {
	return core.ToolSpec{
		Name:        "goal_blocked",
		Description: "Stop the session goal because a user or external action is required after repeated failed attempts. Do not use this merely because the work is hard.",
		Risk:        core.RiskReadOnly,
		InputSchema: schema("goal_blocked"),
	}
}

type goalBlockedArgs struct {
	GoalID        string `json:"goal_id"`
	Reason        string `json:"reason"`
	Evidence      string `json:"evidence"`
	RepeatedTurns int    `json:"repeated_turns"`
}

// Invoke blocks the current goal.
func (t *GoalBlocked) Invoke(_ context.Context, in core.ToolInput, _ core.ToolContext) (core.ToolOutput, error) {
	var a goalBlockedArgs
	if err := decodeArgs("goal_blocked", in.Arguments, &a); err != nil {
		return core.ToolOutput{}, err
	}
	if t == nil || t.Save == nil {
		return core.ToolOutput{}, fmt.Errorf("%w: goal_blocked needs a session", core.ErrUnavailable)
	}
	g, err := loadOpenGoal(t.Load, a.GoalID)
	if err != nil {
		return core.ToolOutput{}, err
	}
	next, err := g.Block(a.Reason, a.Evidence, a.RepeatedTurns, goalNow(t.Now))
	if err != nil {
		return core.ToolOutput{}, err
	}
	if err := t.Save(next); err != nil {
		return core.ToolOutput{}, err
	}
	return output("goal blocked: "+next.BlockReason, next, core.Capability{Risk: core.RiskReadOnly, Scope: core.ResourceScope{Kind: core.ScopeAny}})
}

// Spec describes the tool.
func (*GoalWait) Spec() core.ToolSpec {
	return core.ToolSpec{
		Name:        "goal_wait",
		Description: "Pause automatic goal continuation until an external event. Arrange a wake source first. Optional resume_after_ms is a safety deadline, not a poll interval.",
		Risk:        core.RiskReadOnly,
		InputSchema: schema("goal_wait"),
	}
}

type goalWaitArgs struct {
	GoalID        string `json:"goal_id"`
	Reason        string `json:"reason"`
	ResumeAfterMS int64  `json:"resume_after_ms"`
}

// Invoke waits on the current goal.
func (t *GoalWait) Invoke(_ context.Context, in core.ToolInput, _ core.ToolContext) (core.ToolOutput, error) {
	var a goalWaitArgs
	if err := decodeArgs("goal_wait", in.Arguments, &a); err != nil {
		return core.ToolOutput{}, err
	}
	if t == nil || t.Save == nil {
		return core.ToolOutput{}, fmt.Errorf("%w: goal_wait needs a session", core.ErrUnavailable)
	}
	g, err := loadOpenGoal(t.Load, a.GoalID)
	if err != nil {
		return core.ToolOutput{}, err
	}
	now := goalNow(t.Now)
	var until time.Time
	if a.ResumeAfterMS > 0 {
		d := time.Duration(a.ResumeAfterMS) * time.Millisecond
		if d < core.MinGoalWait {
			d = core.MinGoalWait
		}
		until = now.Add(d)
	}
	next, err := g.Wait(a.Reason, until, now)
	if err != nil {
		return core.ToolOutput{}, err
	}
	if err := t.Save(next); err != nil {
		return core.ToolOutput{}, err
	}
	return output("goal waiting: "+next.WaitReason, next, core.Capability{Risk: core.RiskReadOnly, Scope: core.ResourceScope{Kind: core.ScopeAny}})
}
