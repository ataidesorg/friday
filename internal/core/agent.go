package core

// HarnessKind selects the agent harness flavour.
type HarnessKind string

// Harness kinds.
const (
	HarnessCode      HarnessKind = "code"
	HarnessAssistant HarnessKind = "assistant"
)

// CommunicationStyle tunes how verbose the agent is with the user.
type CommunicationStyle string

// Communication styles.
const (
	StyleConcise  CommunicationStyle = "concise"
	StyleDetailed CommunicationStyle = "detailed"
)

// PolicyPosture selects how conservative policy evaluation is.
type PolicyPosture string

// Policy postures.
const (
	PostureStrict   PolicyPosture = "strict"
	PostureStandard PolicyPosture = "standard"
)

// AgentProfile is a named configuration of identity, style, and posture.
type AgentProfile struct {
	ID              ProfileID          `json:"id"`
	Name            string             `json:"name"`
	Identity        string             `json:"identity,omitempty"`
	Style           CommunicationStyle `json:"style"`
	Posture         PolicyPosture      `json:"posture"`
	MemoryNamespace string             `json:"memory_namespace"`
	SensitivityCap  Sensitivity        `json:"sensitivity_cap,omitempty"`
	Harness         HarnessKind        `json:"harness,omitempty"`
}

// DefaultCodeProfile is the shipped code-harness profile.
func DefaultCodeProfile() AgentProfile {
	return AgentProfile{
		Name:            "default",
		Style:           StyleConcise,
		Posture:         PostureStrict,
		MemoryNamespace: "project",
		SensitivityCap:  SensitivityInternal,
		Harness:         HarnessCode,
	}
}

// DefaultAssistantProfile is the shipped personal-assistant profile:
// personal memory namespace, retrieval capped at personal (never secret).
// Calendar and mail integrations are not part of this default.
func DefaultAssistantProfile() AgentProfile {
	return AgentProfile{
		Name:            "assistant",
		Style:           StyleConcise,
		Posture:         PostureStrict,
		MemoryNamespace: "personal",
		SensitivityCap:  SensitivityPersonal,
		Harness:         HarnessAssistant,
	}
}
