package core

import "encoding/json"

// ToolSpec describes a tool to the model and to policy.
type ToolSpec struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	Risk         RiskClass       `json:"risk"`
	InputSchema  json.RawMessage `json:"input_schema,omitempty"`
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`
}

// ToolInput is a validated-by-schema, still untrusted, tool invocation.
type ToolInput struct {
	Call      ToolCallID      `json:"call"`
	Arguments json.RawMessage `json:"arguments"`
}

// ToolOutput is untrusted data returned by a tool, never instructions.
type ToolOutput struct {
	Content          string          `json:"content"`
	Structured       json.RawMessage `json:"structured,omitempty"`
	CapabilitiesUsed []Capability    `json:"capabilities_used,omitempty"`
}

// ToolContext is the attributable situation a tool runs in.
type ToolContext struct {
	Run           RunID     `json:"run"`
	WorkspaceRoot string    `json:"workspace_root"`
	Sandbox       SandboxID `json:"sandbox,omitempty"`
}
