package core

// RiskClass classifies what a capability can do to the world.
type RiskClass string

// Risk classes, roughly least to most dangerous.
const (
	RiskReadOnly      RiskClass = "read_only"
	RiskWriteLocal    RiskClass = "write_local"
	RiskExecuteLocal  RiskClass = "execute_local"
	RiskNetworkRead   RiskClass = "network_read"
	RiskNetworkWrite  RiskClass = "network_write"
	RiskDestructive   RiskClass = "destructive"
	RiskSecretBearing RiskClass = "secret_bearing"
	RiskPrivileged    RiskClass = "privileged"
)

// RiskClasses lists every risk class.
var RiskClasses = []RiskClass{
	RiskReadOnly, RiskWriteLocal, RiskExecuteLocal, RiskNetworkRead,
	RiskNetworkWrite, RiskDestructive, RiskSecretBearing, RiskPrivileged,
}

// RequiresApprovalByDefault reports whether a policy with no matching rule
// must ask a human: everything except read-only access.
func (r RiskClass) RequiresApprovalByDefault() bool { return r != RiskReadOnly }

// ScopeKind says what kind of resource a scope names.
type ScopeKind string

// Scope kinds.
const (
	ScopePath    ScopeKind = "path"
	ScopeCommand ScopeKind = "command"
	ScopeNetwork ScopeKind = "network"
	ScopeEnv     ScopeKind = "env"
	ScopeSecret  ScopeKind = "secret"
	ScopeAny     ScopeKind = "any"
)

// ResourceScope names the concrete resource a capability touches.
type ResourceScope struct {
	Kind ScopeKind `json:"kind"`
	Path string    `json:"path,omitempty"`
	Argv []string  `json:"argv,omitempty"`
	Host string    `json:"host,omitempty"`
	Name string    `json:"name,omitempty"`
	Ref  string    `json:"ref,omitempty"`
}

// Capability is a risk class applied to a resource scope.
type Capability struct {
	Risk  RiskClass     `json:"risk"`
	Scope ResourceScope `json:"scope"`
}

// CapabilityRequest is a tool asking for a capability before acting.
type CapabilityRequest struct {
	Call          ToolCallID `json:"call"`
	Tool          string     `json:"tool"`
	Capability    Capability `json:"capability"`
	Justification string     `json:"justification,omitempty"`
}
