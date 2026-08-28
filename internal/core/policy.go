package core

import "time"

// Effect is the result of evaluating a policy.
type Effect string

// Policy effects.
const (
	EffectAllow           Effect = "allow"
	EffectDeny            Effect = "deny"
	EffectRequireApproval Effect = "require_approval"
)

// PolicyRule maps a risk class to an effect.
type PolicyRule struct {
	Name   string    `json:"name"`
	Risk   RiskClass `json:"risk"`
	Effect Effect    `json:"effect"`
}

// Policy is an ordered rule set with a default effect.
type Policy struct {
	ID            PolicyID     `json:"id"`
	Name          string       `json:"name"`
	DefaultEffect Effect       `json:"default_effect"`
	Rules         []PolicyRule `json:"rules,omitempty"`
}

// PolicyDecision is an attributable policy verdict.
type PolicyDecision struct {
	Effect Effect `json:"effect"`
	Rule   string `json:"rule,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// PolicyContext is the situation a policy is evaluated in.
type PolicyContext struct {
	WorkspaceRoot string        `json:"workspace_root"`
	Posture       PolicyPosture `json:"posture"`
}

// ApprovalDecision is the human answer to an approval request.
type ApprovalDecision string

// Approval decisions.
const (
	ApprovalApproved ApprovalDecision = "approved"
	ApprovalDenied   ApprovalDecision = "denied"
)

// ApprovalScope says how long an approval holds.
type ApprovalScope string

// Approval scopes.
const (
	ApprovalOnce    ApprovalScope = "once"
	ApprovalSession ApprovalScope = "session"
)

// ApprovalResolution records who decided what, when, and for how long.
type ApprovalResolution struct {
	Decision ApprovalDecision `json:"decision"`
	By       Principal        `json:"by"`
	At       time.Time        `json:"at"`
	Scope    ApprovalScope    `json:"scope"`
	Note     string           `json:"note,omitempty"`
}

// Approval is a pending or resolved request for human consent.
type Approval struct {
	ID      ApprovalID        `json:"id"`
	Task    TaskID            `json:"task"`
	Run     RunID             `json:"run"`
	Request CapabilityRequest `json:"request"`
	// Preview shows the human exactly what approving does — a diff for a
	// write, the argv for a command. UI-only: events and the trail never
	// carry it.
	Preview     string              `json:"-"`
	RequestedAt time.Time           `json:"requested_at"`
	Resolution  *ApprovalResolution `json:"resolution,omitempty"`
}
