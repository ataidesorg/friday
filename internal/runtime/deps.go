// Package runtime drives one task through the core lifecycle: every phase
// is deterministic code, the model is consulted only in planning and the
// tool loop, every tool call is policy-checked and recorded, budgets fail
// closed, and the trail always ends with a task_finished event.
package runtime

import (
	"context"
	"fmt"
	"time"

	"github.com/ataidesorg/ink/internal/core"
	"github.com/ataidesorg/ink/internal/policy"
	"github.com/ataidesorg/ink/internal/tools"
)

// DefaultMaxToolCalls bounds the tool loop when the task budget leaves
// MaxToolCalls unset; a run never loops unbounded.
const DefaultMaxToolCalls = 200

// ApprovalFunc asks a human to resolve an approval. A nil ApprovalFunc
// denies every request with the note "no approver (non-interactive)".
type ApprovalFunc func(ctx context.Context, a core.Approval) (core.ApprovalResolution, error)

// PriceFunc returns the cost of one model call, or nil when the price is
// unknown. Unknown costs are reported as nil and cannot enforce max_cost.
type PriceFunc func(provider, model string, u core.Usage) *core.USDMicros

// Observer receives live progress for a display. OnEvent sees each event as
// emitted, before any redaction the Sink applies, so it is for local
// display only.
type Observer interface {
	OnEvent(core.Event)
	OnPhase(core.Phase)
	OnModelDelta(string)
}

// Deps are the collaborators a run needs. Provider, Tools, Policy, Sandbox
// and Sink are required; the rest have safe defaults.
type Deps struct {
	Provider  core.ModelProvider
	Tools     *tools.Registry
	Policy    core.PolicyEngine
	Approvals *policy.Approvals
	Sandbox   core.SandboxProvider
	Sink      core.EventSink
	Approve   ApprovalFunc
	Observer  Observer
	Clock     func() time.Time
	Price     PriceFunc
	Spend     *Spend
	// Route, when set, is the routing decision that picked Provider; the
	// model_selected event then reports it instead of "single provider".
	Route *core.RouteDecision
	// Streamed means the provider forwards deltas to the Observer itself
	// (wire OnDelta); the runtime then skips replaying the full content.
	Streamed bool
}

// Input describes the task and where it runs.
type Input struct {
	Task      core.Task
	Project   core.Project
	Workspace core.Workspace
	Model     string
	Spec      core.SandboxSpec
	Posture   core.PolicyPosture
	TestCmd   []string
	// History is prior conversation turns injected between the system prompt
	// and this task's user message, so a chat run sees earlier turns. The
	// caller trims it to the context budget; the runtime uses it verbatim.
	History []core.Message

	// Images are attached to this turn's user message. Wire layers that
	// cannot carry them refuse honestly rather than dropping them.
	Images []core.ImagePart

	// Skills are the discovered agent skills, name+description only; the
	// skill tool loads full content on demand.
	Skills []SkillInfo

	// AgentPrompt is the active agent profile's extra instructions, appended
	// to the system prompt; empty means no agent is active.
	AgentPrompt string

	// CheckpointPath, when set, is rewritten after every lifecycle transition
	// so a crash can be inspected or resumed. Empty disables persistence.
	CheckpointPath string

	// ResumeFrom, when set, hydrates run state from that checkpoint and
	// continues at the saved phase. Intake/preflight still create a fresh
	// sandbox because the previous one is gone.
	ResumeFrom string

	// Agent is the active profile: style, posture, memory namespace, and
	// sensitivity cap. Zero means the code-harness defaults.
	Agent core.AgentProfile

	// Goal is the session goal for this turn; nil means none.
	Goal *core.Goal
	// SaveGoal persists mutations from tools and caps. Nil makes goal tools unavailable.
	SaveGoal func(core.Goal) error
}

// Result is what a finished run reports; Events counts the events emitted.
type Result struct {
	Run      core.Run
	Outcome  core.Outcome
	Usage    core.Usage
	Cost     core.CostReport
	Summary  string
	Memories []core.MemoryCandidate
	Events   int
	// Goal is the session goal after the turn, if one was in play.
	Goal *core.Goal
	// ContinueGoal is true when automatic work should keep going.
	ContinueGoal bool
}

func (d Deps) validate() error {
	missing := ""
	switch {
	case d.Provider == nil:
		missing = "Provider"
	case d.Tools == nil:
		missing = "Tools"
	case d.Policy == nil:
		missing = "Policy"
	case d.Sandbox == nil:
		missing = "Sandbox"
	case d.Sink == nil:
		missing = "Sink"
	}
	if missing != "" {
		return fmt.Errorf("%w: runtime.Deps.%s is nil", core.ErrInvalidInput, missing)
	}
	return nil
}

func (d Deps) withDefaults() Deps {
	if d.Clock == nil {
		d.Clock = time.Now
	}
	if d.Observer == nil {
		d.Observer = nopObserver{}
	}
	if d.Price == nil {
		d.Price = func(string, string, core.Usage) *core.USDMicros { return nil }
	}
	return d
}

type nopObserver struct{}

func (nopObserver) OnEvent(core.Event)  {}
func (nopObserver) OnPhase(core.Phase)  {}
func (nopObserver) OnModelDelta(string) {}

// SkillInfo is the prompt-visible surface of one agent skill.
type SkillInfo struct {
	Name        string
	Description string
}
