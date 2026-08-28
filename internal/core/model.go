package core

import (
	"encoding/json"
	"time"
)

// PrivacyClass says where a model runs and therefore where data may flow.
type PrivacyClass string

// Privacy classes, most to least private.
const (
	PrivacyLocal        PrivacyClass = "local"
	PrivacyPrivateCloud PrivacyClass = "private_cloud"
	PrivacyPublicCloud  PrivacyClass = "public_cloud"
)

func (p PrivacyClass) rank() int {
	switch p {
	case PrivacyLocal:
		return 0
	case PrivacyPrivateCloud:
		return 1
	case PrivacyPublicCloud:
		return 2
	default:
		return 3
	}
}

// AllowsFallbackTo reports whether a request constrained to p may fall back
// to a route of class other: never to a less private class.
func (p PrivacyClass) AllowsFallbackTo(other PrivacyClass) bool {
	if p.rank() == 3 || other.rank() == 3 {
		return false
	}
	return other.rank() <= p.rank()
}

// ProviderKind selects a provider adapter.
type ProviderKind string

// Provider kinds.
const (
	ProviderOpenAICompatible ProviderKind = "openai_compatible"
	ProviderResponses        ProviderKind = "responses"
	ProviderAnthropic        ProviderKind = "anthropic"
	ProviderBedrock          ProviderKind = "bedrock"
	ProviderMock             ProviderKind = "mock"
)

// ProviderCapabilities is what a provider has proven it supports.
type ProviderCapabilities struct {
	Streaming        bool `json:"streaming"`
	ToolCalling      bool `json:"tool_calling"`
	StructuredOutput bool `json:"structured_output"`
	MaxContextTokens int  `json:"max_context_tokens"`
}

// HealthState is the last known reachability of a provider.
type HealthState string

// Health states.
const (
	HealthUnknown   HealthState = "unknown"
	HealthHealthy   HealthState = "healthy"
	HealthDegraded  HealthState = "degraded"
	HealthUnhealthy HealthState = "unhealthy"
)

// ProviderHealth is a dated health verdict.
type ProviderHealth struct {
	State     HealthState `json:"state"`
	Reason    string      `json:"reason,omitempty"`
	CheckedAt time.Time   `json:"checked_at,omitempty"`
}

// ProviderDescriptor identifies a configured provider. It never carries credentials.
type ProviderDescriptor struct {
	ID           string               `json:"id"`
	Kind         ProviderKind         `json:"kind"`
	Privacy      PrivacyClass         `json:"privacy"`
	Capabilities ProviderCapabilities `json:"capabilities"`
	Health       ProviderHealth       `json:"health"`
}

// Usage counts tokens for one or more model calls.
type Usage struct {
	InputTokens       int64 `json:"input_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
	CachedInputTokens int64 `json:"cached_input_tokens"`
}

// Add returns the component-wise sum.
func (u Usage) Add(o Usage) Usage {
	return Usage{
		InputTokens:       u.InputTokens + o.InputTokens,
		OutputTokens:      u.OutputTokens + o.OutputTokens,
		CachedInputTokens: u.CachedInputTokens + o.CachedInputTokens,
	}
}

// CostReport separates the estimate made before a call from the actual cost.
type CostReport struct {
	Estimated *USDMicros `json:"estimated,omitempty"`
	Actual    *USDMicros `json:"actual,omitempty"`
}

// RouteConstraints bound route selection.
type RouteConstraints struct {
	MaxCost       *USDMicros    `json:"max_cost,omitempty"`
	MaxLatency    time.Duration `json:"max_latency,omitempty"`
	Privacy       PrivacyClass  `json:"privacy"`
	AllowFallback bool          `json:"allow_fallback"`
}

// ModelRoute names a provider/model pair and its fallbacks.
type ModelRoute struct {
	Name        string           `json:"name"`
	Provider    string           `json:"provider"`
	Model       string           `json:"model"`
	Constraints RouteConstraints `json:"constraints"`
	Fallbacks   []string         `json:"fallbacks,omitempty"`
}

// RankedAlternative is a route that was considered and why it lost.
type RankedAlternative struct {
	Route  string `json:"route"`
	Reason string `json:"reason"`
}

// RouteDecision is an explainable routing outcome.
type RouteDecision struct {
	Selected      ModelRoute          `json:"selected"`
	Alternatives  []RankedAlternative `json:"alternatives,omitempty"`
	Reason        string              `json:"reason"`
	EstimatedCost *USDMicros          `json:"estimated_cost,omitempty"`
	Constraints   RouteConstraints    `json:"constraints"`
	Fallback      []string            `json:"fallback,omitempty"`
	KeyIndex      int                 `json:"key_index,omitempty"` // which configured provider key serves the call, by position; never the key itself
}

// Role is the author of a chat message.
type Role string

// Message roles.
const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is one turn of a model conversation.
type Message struct {
	Role       Role        `json:"role"`
	Content    string      `json:"content"`
	Images     []ImagePart `json:"images,omitempty"`
	ToolCallID ToolCallID  `json:"tool_call_id,omitempty"`
	Name       string      `json:"name,omitempty"`
	ToolCalls  []ToolCall  `json:"tool_calls,omitempty"`
}

// ImagePart is one image attached to a user message, carried as base64 so
// the wire layers can render whichever block shape their API wants.
type ImagePart struct {
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// ToolCall is a model's request to invoke a tool; Arguments are untrusted.
type ToolCall struct {
	ID        ToolCallID      `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// FinishReason says why a completion ended.
type FinishReason string

// Finish reasons.
const (
	FinishStop      FinishReason = "stop"
	FinishLength    FinishReason = "length"
	FinishToolCalls FinishReason = "tool_calls"
	FinishError     FinishReason = "error"
)

// CompletionRequest is a provider-agnostic model call.
type CompletionRequest struct {
	Model           string          `json:"model"`
	Messages        []Message       `json:"messages"`
	Tools           []ToolSpec      `json:"tools,omitempty"`
	MaxOutputTokens int             `json:"max_output_tokens,omitempty"`
	OutputSchema    json.RawMessage `json:"output_schema,omitempty"`
}

// CompletionResponse is the provider-agnostic result.
type CompletionResponse struct {
	Content   string       `json:"content"`
	ToolCalls []ToolCall   `json:"tool_calls,omitempty"`
	Usage     Usage        `json:"usage"`
	Finish    FinishReason `json:"finish"`
}
