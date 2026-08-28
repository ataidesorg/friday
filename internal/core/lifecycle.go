package core

import (
	"errors"
	"fmt"
)

// Phase is one step of the task lifecycle, in execution order.
type Phase string

// Lifecycle phases in order.
const (
	PhaseIntake           Phase = "intake"
	PhasePreflight        Phase = "preflight"
	PhaseContextAssembly  Phase = "context_assembly"
	PhaseModelSelection   Phase = "model_selection"
	PhasePlanning         Phase = "planning"
	PhaseToolExecution    Phase = "tool_execution"
	PhaseValidation       Phase = "validation"
	PhaseSynthesis        Phase = "synthesis"
	PhaseMemoryExtraction Phase = "memory_extraction"
	PhaseTelemetryCapture Phase = "telemetry_capture"
)

// Phases lists every phase in lifecycle order.
var Phases = [10]Phase{
	PhaseIntake, PhasePreflight, PhaseContextAssembly, PhaseModelSelection, PhasePlanning,
	PhaseToolExecution, PhaseValidation, PhaseSynthesis, PhaseMemoryExtraction, PhaseTelemetryCapture,
}

// Index returns the phase's position in Phases, or -1 if p is unknown.
func (p Phase) Index() int {
	for i, q := range Phases {
		if q == p {
			return i
		}
	}
	return -1
}

// Next returns the following phase; ok is false for the last or an unknown phase.
func (p Phase) Next() (Phase, bool) {
	i := p.Index()
	if i < 0 || i == len(Phases)-1 {
		return "", false
	}
	return Phases[i+1], true
}

// Status is the coarse task state.
type Status string

// Task statuses.
const (
	StatusActive           Status = "active"
	StatusAwaitingApproval Status = "awaiting_approval"
	StatusDone             Status = "done"
)

// OutcomeKind classifies how a task ended.
type OutcomeKind string

// Outcome kinds.
const (
	OutcomeCompletedVerified   OutcomeKind = "completed_verified"
	OutcomeCompletedUnverified OutcomeKind = "completed_unverified"
	OutcomeEscalated           OutcomeKind = "escalated"
	OutcomeRolledBack          OutcomeKind = "rolled_back"
	OutcomeFailed              OutcomeKind = "failed"
)

// FailureCategory classifies a failure for telemetry and policy.
type FailureCategory string

// Failure categories.
const (
	FailurePolicyDenied     FailureCategory = "policy_denied"
	FailureBudgetExceeded   FailureCategory = "budget_exceeded"
	FailureProviderError    FailureCategory = "provider_error"
	FailureToolError        FailureCategory = "tool_error"
	FailureSandboxError     FailureCategory = "sandbox_error"
	FailureValidationFailed FailureCategory = "validation_failed"
	FailureTimeout          FailureCategory = "timeout"
	FailureUserAborted      FailureCategory = "user_aborted"
	FailureInternal         FailureCategory = "internal"
)

// Outcome records how a task ended.
type Outcome struct {
	Kind     OutcomeKind     `json:"kind"`
	Reason   string          `json:"reason,omitempty"`
	Category FailureCategory `json:"category,omitempty"`
}

// TaskState is the immutable lifecycle state of a task.
type TaskState struct {
	Status   Status     `json:"status"`
	Phase    Phase      `json:"phase"`
	Resume   Phase      `json:"resume,omitempty"`
	Approval ApprovalID `json:"approval,omitempty"`
	Outcome  *Outcome   `json:"outcome,omitempty"`
}

// InitialState returns the state of a freshly created task.
func InitialState() TaskState { return TaskState{Status: StatusActive, Phase: PhaseIntake} }

// Terminal reports whether the state can no longer change.
func (s TaskState) Terminal() bool { return s.Status == StatusDone }

// TransitionKind names a lifecycle transition.
type TransitionKind string

// Transition kinds.
const (
	TransitionAdvance         TransitionKind = "advance"
	TransitionRevise          TransitionKind = "revise"
	TransitionRequestApproval TransitionKind = "request_approval"
	TransitionResolveApproval TransitionKind = "resolve_approval"
	TransitionComplete        TransitionKind = "complete"
	TransitionEscalate        TransitionKind = "escalate"
	TransitionRollback        TransitionKind = "rollback"
	TransitionFail            TransitionKind = "fail"
)

// Transition is a request to change a TaskState.
type Transition struct {
	Kind     TransitionKind  `json:"kind"`
	Reason   string          `json:"reason,omitempty"`
	Approval ApprovalID      `json:"approval,omitempty"`
	Verified bool            `json:"verified,omitempty"`
	Category FailureCategory `json:"category,omitempty"`
	Message  string          `json:"message,omitempty"`
}

// Lifecycle errors.
var (
	ErrAlreadyDone          = errors.New("task already done")
	ErrTransitionNotAllowed = errors.New("transition not allowed")
	ErrNoPendingApproval    = errors.New("no pending approval")
	ErrApprovalPending      = errors.New("approval pending")
)

// Apply returns the state after t, never mutating s.
func (s TaskState) Apply(t Transition) (TaskState, error) {
	switch s.Status {
	case StatusDone:
		return s, fmt.Errorf("%s: %w", t.Kind, ErrAlreadyDone)
	case StatusAwaitingApproval:
		return s.applyAwaiting(t)
	default:
		return s.applyActive(t)
	}
}

func (s TaskState) applyAwaiting(t Transition) (TaskState, error) {
	switch t.Kind {
	case TransitionResolveApproval:
		return TaskState{Status: StatusActive, Phase: s.Resume}, nil
	case TransitionEscalate:
		return s.done(OutcomeEscalated, t.Reason, ""), nil
	case TransitionFail:
		return s.done(OutcomeFailed, t.Message, t.Category), nil
	default:
		return s, fmt.Errorf("%s while awaiting approval: %w", t.Kind, ErrApprovalPending)
	}
}

func (s TaskState) applyActive(t Transition) (TaskState, error) {
	switch t.Kind {
	case TransitionAdvance:
		next, ok := s.Phase.Next()
		if !ok {
			return s, notAllowed(t, s.Phase)
		}
		return TaskState{Status: StatusActive, Phase: next}, nil
	case TransitionRevise:
		if s.Phase != PhaseToolExecution && s.Phase != PhaseValidation {
			return s, notAllowed(t, s.Phase)
		}
		return TaskState{Status: StatusActive, Phase: PhasePlanning}, nil
	case TransitionRequestApproval:
		if s.Phase != PhasePreflight && s.Phase != PhaseToolExecution {
			return s, notAllowed(t, s.Phase)
		}
		return TaskState{Status: StatusAwaitingApproval, Phase: s.Phase, Resume: s.Phase, Approval: t.Approval}, nil
	case TransitionResolveApproval:
		return s, fmt.Errorf("%s at %s: %w", t.Kind, s.Phase, ErrNoPendingApproval)
	case TransitionComplete:
		return s.complete(t)
	case TransitionEscalate:
		return s.done(OutcomeEscalated, t.Reason, ""), nil
	case TransitionRollback:
		if s.Phase.Index() < PhaseToolExecution.Index() {
			return s, notAllowed(t, s.Phase)
		}
		return s.done(OutcomeRolledBack, t.Reason, ""), nil
	case TransitionFail:
		return s.done(OutcomeFailed, t.Message, t.Category), nil
	default:
		return s, fmt.Errorf("unknown transition %q: %w", t.Kind, ErrTransitionNotAllowed)
	}
}

func (s TaskState) complete(t Transition) (TaskState, error) {
	if s.Phase != PhaseTelemetryCapture {
		return s, notAllowed(t, s.Phase)
	}
	if t.Verified {
		return s.done(OutcomeCompletedVerified, "", ""), nil
	}
	return s.done(OutcomeCompletedUnverified, t.Reason, ""), nil
}

func (s TaskState) done(kind OutcomeKind, reason string, cat FailureCategory) TaskState {
	return TaskState{Status: StatusDone, Phase: s.Phase, Outcome: &Outcome{Kind: kind, Reason: reason, Category: cat}}
}

func notAllowed(t Transition, p Phase) error {
	return fmt.Errorf("%s from %s: %w", t.Kind, p, ErrTransitionNotAllowed)
}
