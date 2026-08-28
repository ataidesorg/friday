package core

import (
	"fmt"

	"github.com/google/uuid"
)

// Typed identifiers. Each is a UUIDv7 string so IDs sort by creation time;
// distinct types stop a TaskID from being passed where a RunID is expected.
type (
	// TaskID identifies a task.
	TaskID string
	// RunID identifies a run.
	RunID string
	// SessionID identifies a session.
	SessionID string
	// ProfileID identifies a profile.
	ProfileID string
	// ProjectID identifies a project.
	ProjectID string
	// WorkspaceID identifies a workspace.
	WorkspaceID string
	// SandboxID identifies a sandbox.
	SandboxID string
	// ToolCallID identifies a toolcall.
	ToolCallID string
	// ApprovalID identifies a approval.
	ApprovalID string
	// CandidateID identifies a candidate.
	CandidateID string
	// ScenarioID identifies a scenario.
	ScenarioID string
	// EventID identifies a event.
	EventID string
	// GateID identifies a gate.
	GateID string
	// PolicyID identifies a policy.
	PolicyID string
)

// ValidID reports whether s is a canonical UUID string.
func ValidID(s string) bool {
	u, err := uuid.Parse(s)
	return err == nil && u.String() == s
}

func newID() (string, error) {
	u, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate uuidv7: %w", err)
	}
	return u.String(), nil
}

// mustID is the single accepted panic in core: uuid.NewV7 fails only when
// crypto/rand is unavailable, which no caller can recover from.
func mustID() string {
	id, err := newID()
	if err != nil {
		panic(err)
	}
	return id
}

// NewTaskID returns a fresh TaskID.
func NewTaskID() TaskID { return TaskID(mustID()) }

// NewRunID returns a fresh RunID.
func NewRunID() RunID { return RunID(mustID()) }

// NewSessionID returns a fresh SessionID.
func NewSessionID() SessionID { return SessionID(mustID()) }

// NewProfileID returns a fresh ProfileID.
func NewProfileID() ProfileID { return ProfileID(mustID()) }

// NewProjectID returns a fresh ProjectID.
func NewProjectID() ProjectID { return ProjectID(mustID()) }

// NewWorkspaceID returns a fresh WorkspaceID.
func NewWorkspaceID() WorkspaceID { return WorkspaceID(mustID()) }

// NewSandboxID returns a fresh SandboxID.
func NewSandboxID() SandboxID { return SandboxID(mustID()) }

// NewToolCallID returns a fresh ToolCallID.
func NewToolCallID() ToolCallID { return ToolCallID(mustID()) }

// NewApprovalID returns a fresh ApprovalID.
func NewApprovalID() ApprovalID { return ApprovalID(mustID()) }

// NewCandidateID returns a fresh CandidateID.
func NewCandidateID() CandidateID { return CandidateID(mustID()) }

// NewScenarioID returns a fresh ScenarioID.
func NewScenarioID() ScenarioID { return ScenarioID(mustID()) }

// NewEventID returns a fresh EventID.
func NewEventID() EventID { return EventID(mustID()) }

// NewGateID returns a fresh GateID.
func NewGateID() GateID { return GateID(mustID()) }

// NewPolicyID returns a fresh PolicyID.
func NewPolicyID() PolicyID { return PolicyID(mustID()) }
