package core

import "time"

// EventKind names an event payload type.
type EventKind string

// Event kinds, one per payload type below.
const (
	EventTaskCreated       EventKind = "task_created"
	EventStateChanged      EventKind = "state_changed"
	EventModelSelected     EventKind = "model_selected"
	EventModelUsage        EventKind = "model_usage"
	EventContextAssembled  EventKind = "context_assembled"
	EventToolCalled        EventKind = "tool_called"
	EventToolCompleted     EventKind = "tool_completed"
	EventSandboxCreated    EventKind = "sandbox_created"
	EventSandboxDestroyed  EventKind = "sandbox_destroyed"
	EventPolicyDecision    EventKind = "policy_decision"
	EventApprovalRequested EventKind = "approval_requested"
	EventApprovalResolved  EventKind = "approval_resolved"
	EventValidationResult  EventKind = "validation_result"
	EventMemoryCandidate   EventKind = "memory_candidate"
	EventWarning           EventKind = "warning"
	EventTaskFinished      EventKind = "task_finished"
)

// EventData is a typed event payload.
type EventData interface{ Kind() EventKind }

// TaskCreated records a new task.
type TaskCreated struct {
	Description string      `json:"description"`
	Project     ProjectID   `json:"project,omitempty"`
	Harness     HarnessKind `json:"harness"`
}

// StateChanged records one lifecycle transition.
type StateChanged struct {
	From       TaskState      `json:"from"`
	To         TaskState      `json:"to"`
	Transition TransitionKind `json:"transition"`
}

// ModelSelected records an explainable routing decision.
type ModelSelected struct {
	Route         string     `json:"route"`
	Provider      string     `json:"provider"`
	Model         string     `json:"model"`
	Reason        string     `json:"reason"`
	EstimatedCost *USDMicros `json:"estimated_cost,omitempty"`
}

// ModelUsage records tokens, cost, and latency of one model call.
type ModelUsage struct {
	Provider string        `json:"provider"`
	Model    string        `json:"model"`
	Usage    Usage         `json:"usage"`
	Cost     CostReport    `json:"cost"`
	Latency  time.Duration `json:"latency"`
}

// ContextAssembled records the shape of a context packet.
type ContextAssembled struct {
	BudgetTokens int `json:"budget_tokens"`
	UsedTokens   int `json:"used_tokens"`
	Items        int `json:"items"`
	Excluded     int `json:"excluded"`
}

// ToolCalled records a tool invocation request.
type ToolCalled struct {
	Call         ToolCallID `json:"call"`
	Tool         string     `json:"tool"`
	Risk         RiskClass  `json:"risk"`
	InputSummary string     `json:"input_summary"`
}

// ToolCompleted records a tool invocation result.
type ToolCompleted struct {
	Call          ToolCallID    `json:"call"`
	Tool          string        `json:"tool"`
	Success       bool          `json:"success"`
	Elapsed       time.Duration `json:"elapsed"`
	OutputSummary string        `json:"output_summary"`
}

// SandboxCreated records a new sandbox and its network posture.
type SandboxCreated struct {
	Sandbox  SandboxID `json:"sandbox"`
	Provider string    `json:"provider"`
}

// SandboxDestroyed records sandbox teardown.
type SandboxDestroyed struct {
	Sandbox SandboxID `json:"sandbox"`
}

// PolicyDecided records a policy verdict on a capability request.
type PolicyDecided struct {
	Call   ToolCallID `json:"call"`
	Tool   string     `json:"tool"`
	Risk   RiskClass  `json:"risk"`
	Effect Effect     `json:"effect"`
	Rule   string     `json:"rule"`
	Reason string     `json:"reason"`
}

// ApprovalRequested records a pending human decision.
type ApprovalRequested struct {
	Approval      ApprovalID `json:"approval"`
	Tool          string     `json:"tool"`
	Risk          RiskClass  `json:"risk"`
	Justification string     `json:"justification"`
}

// ApprovalResolved records the human decision.
type ApprovalResolved struct {
	Approval ApprovalID       `json:"approval"`
	Decision ApprovalDecision `json:"decision"`
	By       Principal        `json:"by"`
	Scope    ApprovalScope    `json:"scope"`
}

// ValidationResult records a deterministic check run.
type ValidationResult struct {
	Command  string        `json:"command"`
	Passed   bool          `json:"passed"`
	ExitCode int           `json:"exit_code"`
	Elapsed  time.Duration `json:"elapsed"`
	Summary  string        `json:"summary"`
}

// MemoryCandidateEvent records a proposed or reviewed memory.
type MemoryCandidateEvent struct {
	Candidate CandidateID     `json:"candidate"`
	Category  MemoryCategory  `json:"category"`
	Status    CandidateStatus `json:"status"`
}

// Warning records a non-fatal problem.
type Warning struct {
	Message string `json:"message"`
	// Advisory marks a warning about a guardrail that could not be enforced
	// (unknown model price, no validation command) rather than about
	// something that went wrong. The chat can be told to hide these.
	Advisory bool `json:"advisory,omitempty"`
}

// TaskFinished records the terminal outcome of a run.
type TaskFinished struct {
	Outcome Outcome         `json:"outcome"`
	Elapsed time.Duration   `json:"elapsed"`
	Usage   Usage           `json:"usage"`
	Cost    CostReport      `json:"cost"`
	Failure FailureCategory `json:"failure,omitempty"`
}

// Kind implements EventData.
func (TaskCreated) Kind() EventKind { return EventTaskCreated }

// Kind implements EventData.
func (StateChanged) Kind() EventKind { return EventStateChanged }

// Kind implements EventData.
func (ModelSelected) Kind() EventKind { return EventModelSelected }

// Kind implements EventData.
func (ModelUsage) Kind() EventKind { return EventModelUsage }

// Kind implements EventData.
func (ContextAssembled) Kind() EventKind { return EventContextAssembled }

// Kind implements EventData.
func (ToolCalled) Kind() EventKind { return EventToolCalled }

// Kind implements EventData.
func (ToolCompleted) Kind() EventKind { return EventToolCompleted }

// Kind implements EventData.
func (SandboxCreated) Kind() EventKind { return EventSandboxCreated }

// Kind implements EventData.
func (SandboxDestroyed) Kind() EventKind { return EventSandboxDestroyed }

// Kind implements EventData.
func (PolicyDecided) Kind() EventKind { return EventPolicyDecision }

// Kind implements EventData.
func (ApprovalRequested) Kind() EventKind { return EventApprovalRequested }

// Kind implements EventData.
func (ApprovalResolved) Kind() EventKind { return EventApprovalResolved }

// Kind implements EventData.
func (ValidationResult) Kind() EventKind { return EventValidationResult }

// Kind implements EventData.
func (MemoryCandidateEvent) Kind() EventKind { return EventMemoryCandidate }

// Kind implements EventData.
func (Warning) Kind() EventKind { return EventWarning }

// Kind implements EventData.
func (TaskFinished) Kind() EventKind { return EventTaskFinished }

func decodeAs[T EventData](raw []byte) (EventData, error) {
	var v T
	if err := jsonUnmarshalStrict(raw, &v); err != nil {
		return nil, err
	}
	return v, nil
}

// registry maps each kind to its decoder; unknown kinds fail to decode.
var registry = map[EventKind]func([]byte) (EventData, error){
	EventTaskCreated:       decodeAs[TaskCreated],
	EventStateChanged:      decodeAs[StateChanged],
	EventModelSelected:     decodeAs[ModelSelected],
	EventModelUsage:        decodeAs[ModelUsage],
	EventContextAssembled:  decodeAs[ContextAssembled],
	EventToolCalled:        decodeAs[ToolCalled],
	EventToolCompleted:     decodeAs[ToolCompleted],
	EventSandboxCreated:    decodeAs[SandboxCreated],
	EventSandboxDestroyed:  decodeAs[SandboxDestroyed],
	EventPolicyDecision:    decodeAs[PolicyDecided],
	EventApprovalRequested: decodeAs[ApprovalRequested],
	EventApprovalResolved:  decodeAs[ApprovalResolved],
	EventValidationResult:  decodeAs[ValidationResult],
	EventMemoryCandidate:   decodeAs[MemoryCandidateEvent],
	EventWarning:           decodeAs[Warning],
	EventTaskFinished:      decodeAs[TaskFinished],
}

// KnownEventKind reports whether k has a registered payload type.
func KnownEventKind(k EventKind) bool { _, ok := registry[k]; return ok }
