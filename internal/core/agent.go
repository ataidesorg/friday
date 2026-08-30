package core

// HarnessKind is the product surface a task runs under. Ink ships the
// coding harness only.
type HarnessKind string

// Harness kinds.
const (
	HarnessCode HarnessKind = "code"
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
	ID       ProfileID          `json:"id"`
	Name     string             `json:"name"`
	Identity string             `json:"identity,omitempty"`
	Style    CommunicationStyle `json:"style"`
	Posture  PolicyPosture      `json:"posture"`
}

// DefaultCodeProfile is the shipped coding-agent profile.
func DefaultCodeProfile() AgentProfile {
	return AgentProfile{
		Name:    "default",
		Style:   StyleConcise,
		Posture: PostureStrict,
	}
}
