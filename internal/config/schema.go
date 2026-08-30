// Package config loads, merges, validates, and explains Ink configuration.
package config

import (
	"fmt"
	"time"

	"github.com/ataidesorg/ink/internal/core"
)

// Config is the complete typed configuration. defaults.toml mirrors it.
type Config struct {
	SchemaVersion int                        `toml:"schema_version"`
	Profile       ProfileSelection           `toml:"profile"`
	Profiles      map[string]ProfileConfig   `toml:"profiles"`
	Providers     map[string]ProviderConfig  `toml:"providers"`
	Models        ModelsConfig               `toml:"models"`
	Budgets       BudgetsConfig              `toml:"budgets"`
	Sandbox       SandboxConfig              `toml:"sandbox"`
	Tools         ToolsConfig                `toml:"tools"`
	Telemetry     TelemetryConfig            `toml:"telemetry"`
	Evals         EvalsConfig                `toml:"evals"`
	Project       ProjectConfig              `toml:"project"`
	TUI           TUIConfig                  `toml:"tui"`
	Agents        map[string]AgentConfig     `toml:"agents"`
	Format        FormatConfig               `toml:"format"`
	MCP           map[string]MCPServerConfig `toml:"mcp"`
	LSP           map[string]LSPServerConfig `toml:"lsp"`
}

// AgentConfig is one named agent profile: an extra prompt, an optional route
// to run on, and an optional tool allowlist narrowing what the agent may
// hold. Policy still gates every tool the allowlist leaves in.
type AgentConfig struct {
	Description string   `toml:"description,omitempty"`
	Prompt      string   `toml:"prompt,omitempty"`
	Route       string   `toml:"route,omitempty"`
	Tools       []string `toml:"tools,omitempty"`
}

// TUIConfig sets terminal-UI preferences. Theme names the launch palette
// (a live picker choice saved in the Ink home wins over it); Keys rebinds
// chat actions to key names. Both are validated at chat launch, where the
// theme list and the keymap actually live. HideAdvisories drops warnings
// about guardrails that could not be enforced, such as an unpriced model or
// a missing validation command.
type TUIConfig struct {
	Theme          string            `toml:"theme,omitempty"`
	Keys           map[string]string `toml:"keys,omitempty"`
	HideAdvisories bool              `toml:"hide_advisories,omitempty"`
}

// ProfileSelection names the active profile.
type ProfileSelection struct {
	Active string `toml:"active"`
}

// ProfileConfig is one agent profile.
type ProfileConfig struct {
	Description string `toml:"description"`
	Style       string `toml:"style"`
	Posture     string `toml:"posture"`
}

// ToAgent maps a config profile onto the core type.
func (p ProfileConfig) ToAgent(name string) core.AgentProfile {
	d := core.DefaultCodeProfile()
	d.Name = name
	if p.Description != "" {
		d.Identity = p.Description
	}
	if p.Style != "" {
		d.Style = core.CommunicationStyle(p.Style)
	}
	if p.Posture != "" {
		d.Posture = core.PolicyPosture(p.Posture)
	}
	return d
}

// AuthRef points at a credential without holding it.
type AuthRef struct {
	Source  string   `toml:"source"`
	Name    string   `toml:"name,omitempty"`
	Service string   `toml:"service,omitempty"`
	Account string   `toml:"account,omitempty"`
	ID      string   `toml:"id,omitempty"`
	Command []string `toml:"command,omitempty"` // argv for source = "command"; user layer only
}

// OAuthRef overrides a registry entry's OAuth endpoints from user config —
// the escape hatch for flows whose endpoints Ink has not verified.
// Non-empty fields win over the registry values.
type OAuthRef struct {
	AuthURL       string   `toml:"auth_url,omitempty"`
	TokenURL      string   `toml:"token_url,omitempty"`
	DeviceAuthURL string   `toml:"device_auth_url,omitempty"`
	ClientID      string   `toml:"client_id,omitempty"`
	Scopes        []string `toml:"scopes,omitempty"`
	RedirectPort  int      `toml:"redirect_port,omitempty"`
	RedirectURI   string   `toml:"redirect_uri,omitempty"`
	ExchangeURL   string   `toml:"exchange_url,omitempty"`
}

// ProviderConfig describes a model provider.
type ProviderConfig struct {
	Kind          string    `toml:"kind"`
	BaseURL       string    `toml:"base_url,omitempty"`
	Auth          *AuthRef  `toml:"auth,omitempty"`
	AuthFallbacks []AuthRef `toml:"auth_fallbacks,omitempty"`
	Privacy       string    `toml:"privacy"`
	Models        []string  `toml:"models,omitempty"`
	OAuth         *OAuthRef `toml:"oauth,omitempty"`
	// AcceptThirdPartyOAuthRisk opts into the flows the vendor's terms
	// prohibit (currently anthropic-oauth). User
	// layer only: the config gate
	// rejects every accept_*_risk key from repository layers.
	AcceptThirdPartyOAuthRisk bool `toml:"accept_third_party_oauth_risk,omitempty"`
}

// AuthRefs lists the provider's credentials in rotation order: the primary
// auth reference first, then the fallbacks. Rotation on 401/429 walks this
// order by index; the values themselves never enter routing decisions.
func (p ProviderConfig) AuthRefs() []AuthRef {
	refs := make([]AuthRef, 0, 1+len(p.AuthFallbacks))
	if p.Auth != nil {
		refs = append(refs, *p.Auth)
	}
	return append(refs, p.AuthFallbacks...)
}

// ModelsConfig holds routes, model prices, and the routing policy.
type ModelsConfig struct {
	Routes  map[string]RouteConfig `toml:"routes"`
	Pricing map[string]PriceConfig `toml:"pricing"`
	Routing RoutingConfig          `toml:"routing"`
}

// PriceConfig prices one model in USD per million tokens. A model absent
// from the pricing table has unknown cost, and any cost cap then fails
// closed at route selection.
type PriceConfig struct {
	InputUSDPerMTok  float64 `toml:"input_usd_per_mtok"`
	OutputUSDPerMTok float64 `toml:"output_usd_per_mtok"`
	CachedUSDPerMTok float64 `toml:"cached_usd_per_mtok"`
}

// RoutingConfig selects the default route.
type RoutingConfig struct {
	Default string `toml:"default"`
}

// RouteConfig is one named provider/model pair.
type RouteConfig struct {
	Provider      string   `toml:"provider"`
	Model         string   `toml:"model"`
	Fallbacks     []string `toml:"fallbacks,omitempty"`
	MaxCostUSD    float64  `toml:"max_cost_usd,omitempty"`
	MaxLatencyMS  int      `toml:"max_latency_ms,omitempty"`
	Privacy       string   `toml:"privacy,omitempty"`
	AllowFallback bool     `toml:"allow_fallback"`
}

// BudgetsConfig holds USD ceilings.
type BudgetsConfig struct {
	PerTaskUSD    float64 `toml:"per_task_usd"`
	PerSessionUSD float64 `toml:"per_session_usd"`
	PerDayUSD     float64 `toml:"per_day_usd"`
}

// SandboxConfig selects the sandbox backend and its posture.
type SandboxConfig struct {
	Provider string       `toml:"provider"`
	Limits   LimitsConfig `toml:"limits"`
}

// LimitsConfig are sandbox resource ceilings.
type LimitsConfig struct {
	CPUCores      int   `toml:"cpu_cores"`
	MemoryMB      int64 `toml:"memory_mb"`
	DiskMB        int64 `toml:"disk_mb"`
	MaxProcesses  int   `toml:"max_processes"`
	WallClockSecs int   `toml:"wall_clock_secs"`
}

// ToolsConfig is the tool policy.
type ToolsConfig struct {
	DefaultEffect   string                      `toml:"default_effect"`
	Allow           []string                    `toml:"allow"`
	RequireApproval []string                    `toml:"require_approval"`
	Deny            []string                    `toml:"deny"`
	Commands        CommandsConfig              `toml:"commands"`
	Custom          map[string]CustomToolConfig `toml:"custom"`
	Permissions     map[string]string           `toml:"permissions"`
	// ExperimentalRules turns on Rules; without it they are rejected.
	ExperimentalRules bool             `toml:"experimental_rules"`
	Rules             []ToolRuleConfig `toml:"rules,omitempty"`
}

// ToolRuleConfig is one experimental per-resource rule: it matches a tool
// plus an optional path glob or argv prefix, and forces an effect. First
// match wins, ahead of the tool lists.
type ToolRuleConfig struct {
	Tool   string   `toml:"tool"`
	Path   string   `toml:"path,omitempty"`
	Argv   []string `toml:"argv,omitempty"`
	Effect string   `toml:"effect"`
}

// CustomToolConfig declares one config-defined argv tool. The model's
// arguments JSON is piped to the command's stdin; the argv itself is fixed.
// User layer only — repository config never introduces executables.
type CustomToolConfig struct {
	Description string   `toml:"description,omitempty"`
	Argv        []string `toml:"argv,omitempty"`
	Schema      string   `toml:"schema,omitempty"`
	Risk        string   `toml:"risk,omitempty"`
}

// FormatConfig is per-language post-write formatting, disabled by default.
// A formatter failure warns in the tool output and never blocks the turn.
// User layer only — the commands run automatically after writes.
type FormatConfig struct {
	Enabled   bool                       `toml:"enabled"`
	Languages map[string]FormatterConfig `toml:"languages"`
}

// FormatterConfig is one formatter: the command (the file path is appended)
// and the file extensions it covers.
type FormatterConfig struct {
	Command    []string `toml:"command,omitempty"`
	Extensions []string `toml:"extensions,omitempty"`
}

// MCPServerConfig declares one MCP server spawned over stdio. Disabled by
// default; user layer only — the command is a program the harness runs.
type MCPServerConfig struct {
	Command []string `toml:"command,omitempty"`
	Enabled bool     `toml:"enabled"`
}

// LSPServerConfig declares one language server spawned over stdio, feeding
// diagnostics into tool output after edits to matching files. Disabled by
// default; user layer only — the command is a program the harness runs.
type LSPServerConfig struct {
	Command    []string `toml:"command,omitempty"`
	Extensions []string `toml:"extensions,omitempty"`
	Enabled    bool     `toml:"enabled"`
}

// CommandsConfig restricts the shell tool.
type CommandsConfig struct {
	Allowed []string `toml:"allowed"`
}

// TelemetryConfig controls the local event trail.
type TelemetryConfig struct {
	Privacy string `toml:"privacy"`
}

// EvalsConfig controls promotion gating.
type EvalsConfig struct {
	Gate        string `toml:"gate"`
	MinPassRate int    `toml:"min_pass_rate"`
}

// ProjectConfig is the per-repository section.
type ProjectConfig struct {
	Name         string            `toml:"name"`
	Instructions []string          `toml:"instructions"`
	Commands     map[string]string `toml:"commands"`
}

// CoreView is the subset of configuration the core domain consumes.
type CoreView struct {
	Limits  core.ResourceLimits
	Privacy core.PrivacyMode
	Budget  core.TaskBudget
}

// ToCore converts units exactly and rejects unknown enum values.
func (c Config) ToCore() (CoreView, error) {
	cost, err := core.USDFromFloat(c.Budgets.PerTaskUSD)
	if err != nil {
		return CoreView{}, fmt.Errorf("budgets.per_task_usd: %w", err)
	}
	privacy := core.PrivacyMode(c.Telemetry.Privacy)
	if privacy != core.PrivacyStandard && privacy != core.PrivacyMinimal {
		return CoreView{}, fmt.Errorf("%w: telemetry.privacy %q", core.ErrInvalidInput, c.Telemetry.Privacy)
	}
	l := c.Sandbox.Limits
	return CoreView{
		Limits: core.ResourceLimits{
			CPUCores: l.CPUCores, MemoryMB: l.MemoryMB, DiskMB: l.DiskMB, MaxProcesses: l.MaxProcesses,
			WallClock: time.Duration(l.WallClockSecs) * time.Second,
		},
		Privacy: privacy,
		Budget:  core.TaskBudget{MaxCost: cost},
	}, nil
}
