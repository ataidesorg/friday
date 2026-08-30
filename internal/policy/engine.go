// Package policy decides whether a capability request is allowed, denied, or
// needs a human, from the tools.* config and the profile posture. It fails
// closed: anything it cannot classify is denied.
package policy

import (
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/ataidesorg/ink/internal/config"
	"github.com/ataidesorg/ink/internal/core"
)

// Rule names recorded in every decision.
const (
	RuleDeny            = "tools.deny"
	RuleRequireApproval = "tools.require_approval"
	RuleAllow           = "tools.allow"
	RuleDefault         = "tools.default_effect"
	RulePostureStrict   = "posture.strict"
	RuleRiskDangerous   = "risk.dangerous"
	RuleRiskUnknown     = "risk.unknown"
	RuleInvalid         = "request.invalid"
	RuleCommandAllowed  = "tools.commands.allowed"
	RuleCustomTool      = "tools.custom"
	RuleMCPTool         = "tools.mcp"
	RulePermissions     = "tools.permissions"
	RuleResourceRule    = "tools.rules"
)

var effects = map[string]core.Effect{
	string(core.EffectDeny):            core.EffectDeny,
	string(core.EffectRequireApproval): core.EffectRequireApproval,
	string(core.EffectAllow):           core.EffectAllow,
}

var dangerous = map[core.RiskClass]bool{
	core.RiskDestructive: true, core.RiskPrivileged: true, core.RiskSecretBearing: true,
}

type listed struct {
	effect core.Effect
	rule   string
}

// Engine is an immutable core.PolicyEngine.
type Engine struct {
	policy   core.Policy
	byTool   map[string]listed
	posture  core.PolicyPosture
	commands [][]string
	// customArgv holds each custom tool's declared argv, honored only for
	// that tool's own command scope — never for run_command.
	customArgv map[string][]string
	// mcpPrefixes are "mcp_<server>_" name prefixes; a bridged tool not
	// listed exactly in byTool falls to require_approval, never allow.
	mcpPrefixes []string
	// rules are the experimental per-resource matchers; first match wins
	// over the tool listings. Empty unless tools.experimental_rules.
	rules []resourceRule
}

// resourceRule matches a tool call by name plus an optional path glob or
// argv prefix and forces an effect.
type resourceRule struct {
	tool   string
	path   string
	argv   []string
	effect core.Effect
}

// FromConfig builds an engine; unknown effects, unknown postures, and a tool
// named in two lists are errors. tools.commands.allowed entries are split on
// whitespace into argv prefixes (ponytail: no quoting; a prefix is a list of
// exact argv elements).
func FromConfig(t config.ToolsConfig, posture core.PolicyPosture, mcpServers []string) (*Engine, error) {
	def, ok := effects[t.DefaultEffect]
	if !ok {
		return nil, fmt.Errorf("%w: tools.default_effect %q is not deny, require_approval, or allow", core.ErrInvalidInput, t.DefaultEffect)
	}
	if posture != core.PostureStrict && posture != core.PostureStandard {
		return nil, fmt.Errorf("%w: posture %q is not strict or standard", core.ErrInvalidInput, posture)
	}
	byTool := map[string]listed{}
	permEffects := map[string]core.Effect{"allow": core.EffectAllow, "ask": core.EffectRequireApproval, "deny": core.EffectDeny}
	policy := core.Policy{ID: core.NewPolicyID(), Name: "tools." + string(posture), DefaultEffect: def}
	lists := []struct {
		names  []string
		effect core.Effect
		rule   string
	}{{t.Deny, core.EffectDeny, RuleDeny}, {t.RequireApproval, core.EffectRequireApproval, RuleRequireApproval}, {t.Allow, core.EffectAllow, RuleAllow}}
	for _, l := range lists {
		for _, name := range l.names {
			if prev, dup := byTool[name]; dup {
				return nil, fmt.Errorf("%w: tool %q listed in both %s and %s", core.ErrConflict, name, prev.rule, l.rule)
			}
			byTool[name] = listed{l.effect, l.rule}
			policy.Rules = append(policy.Rules, core.PolicyRule{Name: l.rule + "[" + name + "]", Effect: l.effect})
		}
	}
	for risk := range dangerous {
		policy.Rules = append(policy.Rules, core.PolicyRule{Name: RuleRiskDangerous, Risk: risk, Effect: core.EffectDeny})
	}
	if posture == core.PostureStrict {
		policy.Rules = append(policy.Rules, core.PolicyRule{Name: RulePostureStrict, Effect: core.EffectRequireApproval})
	}
	var commands [][]string
	var customArgv map[string][]string
	for _, entry := range t.Commands.Allowed {
		if argv := strings.Fields(entry); len(argv) > 0 {
			commands = append(commands, argv)
		}
	}
	// Custom tools are user-declared programs: unless the owner listed one
	// explicitly, every call asks first, and its declared argv satisfies the
	// command scope for that tool only — run_command never inherits it.
	for _, name := range slices.Sorted(maps.Keys(t.Permissions)) {
		eff, ok := permEffects[t.Permissions[name]]
		if !ok {
			return nil, fmt.Errorf("%w: tools.permissions.%s %q is not allow, ask, or deny", core.ErrInvalidInput, name, t.Permissions[name])
		}
		if prev, dup := byTool[name]; dup {
			return nil, fmt.Errorf("%w: tool %q listed in both %s and %s", core.ErrConflict, name, prev.rule, RulePermissions)
		}
		byTool[name] = listed{eff, RulePermissions}
		policy.Rules = append(policy.Rules, core.PolicyRule{Name: RulePermissions + "[" + name + "]", Effect: eff})
	}
	var rules []resourceRule
	if t.ExperimentalRules {
		for i, rc := range t.Rules {
			eff, ok := permEffects[rc.Effect]
			if !ok || rc.Tool == "" {
				return nil, fmt.Errorf("%w: tools.rules[%d] needs a tool and an allow/ask/deny effect", core.ErrInvalidInput, i)
			}
			rules = append(rules, resourceRule{tool: rc.Tool, path: rc.Path, argv: slices.Clone(rc.Argv), effect: eff})
			policy.Rules = append(policy.Rules, core.PolicyRule{Name: fmt.Sprintf("%s[%d:%s]", RuleResourceRule, i, rc.Tool), Effect: eff})
		}
	}
	for _, name := range slices.Sorted(maps.Keys(t.Custom)) {
		if _, ok := byTool[name]; !ok {
			byTool[name] = listed{core.EffectRequireApproval, RuleCustomTool}
			policy.Rules = append(policy.Rules, core.PolicyRule{Name: RuleCustomTool + "[" + name + "]", Effect: core.EffectRequireApproval})
		}
		if argv := t.Custom[name].Argv; len(argv) > 0 {
			if customArgv == nil {
				customArgv = map[string][]string{}
			}
			customArgv[name] = slices.Clone(argv)
		}
	}
	var mcpPrefixes []string
	for _, name := range slices.Sorted(slices.Values(mcpServers)) {
		mcpPrefixes = append(mcpPrefixes, "mcp_"+name+"_")
		policy.Rules = append(policy.Rules, core.PolicyRule{Name: RuleMCPTool + "[" + name + "]", Effect: core.EffectRequireApproval})
	}
	policy.Rules = append(policy.Rules, core.PolicyRule{Name: RuleCommandAllowed, Risk: core.RiskExecuteLocal, Effect: core.EffectDeny})
	return &Engine{policy: policy, byTool: byTool, posture: posture, commands: commands, customArgv: customArgv, mcpPrefixes: mcpPrefixes, rules: rules}, nil
}

// AllowedCommands returns the parsed tools.commands.allowed argv prefixes,
// the same list the run_command tool enforces.
func (e *Engine) AllowedCommands() [][]string {
	if e == nil {
		return nil
	}
	out := make([][]string, len(e.commands))
	for i, argv := range e.commands {
		out[i] = slices.Clone(argv)
	}
	return out
}

// Policy renders the engine for the trail.
func (e *Engine) Policy() core.Policy {
	if e == nil {
		return core.Policy{DefaultEffect: core.EffectDeny}
	}
	p := e.policy
	p.Rules = slices.Clone(e.policy.Rules)
	return p
}

// Evaluate applies: tools.deny → tools.require_approval → tools.allow →
// default_effect, then tools.commands.allowed for command scopes, then the
// dangerous-risk rule, then the strict upgrade. The stricter of the
// configured and contextual posture applies.
func (e *Engine) Evaluate(req core.CapabilityRequest, pc core.PolicyContext) core.PolicyDecision {
	if e == nil {
		return core.PolicyDecision{Effect: core.EffectDeny, Rule: RuleInvalid, Reason: "no policy engine configured"}
	}
	if req.Tool == "" {
		return core.PolicyDecision{Effect: core.EffectDeny, Rule: RuleInvalid, Reason: "capability request names no tool"}
	}
	risk := req.Capability.Risk
	if !slices.Contains(core.RiskClasses, risk) {
		return core.PolicyDecision{Effect: core.EffectDeny, Rule: RuleRiskUnknown, Reason: fmt.Sprintf("risk class %q is unknown", risk)}
	}
	posture := e.posture
	if pc.Posture == core.PostureStrict {
		posture = core.PostureStrict
	}
	effect, rule, reason := e.listing(req.Tool)
	if m, ok := e.matchRule(req); ok {
		effect, rule, reason = m.effect, RuleResourceRule, fmt.Sprintf("resource rule for %q matched", req.Tool)
	}
	if effect == core.EffectDeny {
		return core.PolicyDecision{Effect: effect, Rule: rule, Reason: reason}
	}
	if req.Capability.Scope.Kind == core.ScopeCommand && !e.commandAllowed(req.Tool, req.Capability.Scope.Argv) {
		return core.PolicyDecision{Effect: core.EffectDeny, Rule: RuleCommandAllowed, Reason: fmt.Sprintf("command %q is not in tools.commands.allowed", strings.Join(req.Capability.Scope.Argv, " "))}
	}
	if dangerous[risk] {
		switch {
		case rule == RuleDefault:
			return core.PolicyDecision{Effect: core.EffectDeny, Rule: RuleRiskDangerous, Reason: fmt.Sprintf("risk %s requires an explicit tools rule for %q", risk, req.Tool)}
		case posture != core.PostureStandard:
			return core.PolicyDecision{Effect: core.EffectDeny, Rule: RuleRiskDangerous, Reason: fmt.Sprintf("risk %s is denied under posture %s", risk, posture)}
		}
	}
	if effect == core.EffectAllow && posture == core.PostureStrict && risk != core.RiskReadOnly {
		return core.PolicyDecision{Effect: core.EffectRequireApproval, Rule: RulePostureStrict, Reason: fmt.Sprintf("posture strict upgrades allow to approval for %s", risk)}
	}
	return core.PolicyDecision{Effect: effect, Rule: rule, Reason: reason}
}

func (e *Engine) commandAllowed(tool string, argv []string) bool {
	for _, prefix := range e.commands {
		if argvPrefix(prefix, argv) {
			return true
		}
	}
	if own := e.customArgv[tool]; len(own) > 0 && argvPrefix(own, argv) {
		return true
	}
	return false
}

// matchRule returns the first experimental resource rule matching the
// request. A rule with a path glob or argv prefix matches only calls
// carrying that scope; a bare tool rule matches every call to the tool.
func (e *Engine) matchRule(req core.CapabilityRequest) (resourceRule, bool) {
	for _, r := range e.rules {
		if r.tool != req.Tool {
			continue
		}
		if r.path != "" && !pathMatches(r.path, req.Capability.Scope.Path, r.effect != core.EffectAllow) {
			continue
		}
		if len(r.argv) > 0 && !argvPrefix(r.argv, req.Capability.Scope.Argv) {
			continue
		}
		return r, true
	}
	return resourceRule{}, false
}

// pathMatches accepts a path.Match glob, or a "dir/**" prefix covering the
// whole subtree. A scope listing several comma-joined paths is fail-closed
// both ways: an allow rule needs every path to match (it never covers a
// write it only partly saw), while a deny or ask rule fires when any path
// matches (padding a patch with an extra file never dodges it).
func pathMatches(pattern, scoped string, anyPart bool) bool {
	if scoped == "" {
		return false
	}
	for _, p := range strings.Split(scoped, ",") {
		hit := false
		if prefix, ok := strings.CutSuffix(pattern, "/**"); ok {
			hit = p == prefix || strings.HasPrefix(p, prefix+"/")
		} else if ok, err := path.Match(pattern, p); err == nil && ok {
			hit = true
		}
		if anyPart && hit {
			return true
		}
		if !anyPart && !hit {
			return false
		}
	}
	return !anyPart
}

func argvPrefix(prefix, argv []string) bool {
	if len(argv) < len(prefix) {
		return false
	}
	return slices.Equal(prefix, argv[:len(prefix)])
}

func (e *Engine) listing(tool string) (core.Effect, string, string) {
	if l, ok := e.byTool[tool]; ok {
		return l.effect, l.rule, fmt.Sprintf("tool %q listed in %s", tool, l.rule)
	}
	for _, p := range e.mcpPrefixes {
		if strings.HasPrefix(tool, p) {
			return core.EffectRequireApproval, RuleMCPTool, fmt.Sprintf("tool %q comes from an MCP server and is not listed; approval required", tool)
		}
	}
	return e.policy.DefaultEffect, RuleDefault, fmt.Sprintf("tool %q not listed; tools.default_effect is %s", tool, e.policy.DefaultEffect)
}
