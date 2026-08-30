package policy_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ataidesorg/ink/internal/config"
	"github.com/ataidesorg/ink/internal/core"
	"github.com/ataidesorg/ink/internal/policy"
)

func req(tool string, risk core.RiskClass) core.CapabilityRequest {
	return core.CapabilityRequest{Call: core.NewToolCallID(), Tool: tool, Capability: core.Capability{Risk: risk, Scope: core.ResourceScope{Kind: core.ScopeAny}}}
}

func defaults() config.ToolsConfig {
	return config.ToolsConfig{DefaultEffect: "deny", Allow: []string{"read_file", "list_dir", "search"}}
}

func TestFromConfigRejectsBadInput(t *testing.T) {
	if _, err := policy.FromConfig(config.ToolsConfig{DefaultEffect: "maybe"}, core.PostureStrict, nil); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("bad effect: %v", err)
	}
	if _, err := policy.FromConfig(config.ToolsConfig{DefaultEffect: "deny", Allow: []string{"x"}, Deny: []string{"x"}}, core.PostureStrict, nil); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("tool in two lists: %v", err)
	}
	if _, err := policy.FromConfig(defaults(), "lenient", nil); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("bad posture: %v", err)
	}
	if _, err := policy.FromConfig(config.ToolsConfig{}, core.PostureStrict, nil); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("empty default effect: %v", err)
	}
}

func TestAcceptanceDefaults(t *testing.T) {
	e, err := policy.FromConfig(defaults(), core.PostureStrict, nil)
	if err != nil {
		t.Fatal(err)
	}
	pc := core.PolicyContext{WorkspaceRoot: "/w", Posture: core.PostureStrict}
	cases := map[string]struct {
		req  core.CapabilityRequest
		want core.Effect
		rule string
	}{
		"read":  {req("read_file", core.RiskReadOnly), core.EffectAllow, "tools.allow"},
		"write": {req("write_file", core.RiskWriteLocal), core.EffectDeny, "tools.default_effect"},
		"run":   {req("run_command", core.RiskExecuteLocal), core.EffectDeny, "tools.default_effect"},
	}
	for name, c := range cases {
		d := e.Evaluate(c.req, pc)
		if d.Effect != c.want || d.Rule != c.rule || d.Reason == "" {
			t.Errorf("%s: %+v", name, d)
		}
	}
	cfg := defaults()
	cfg.Allow = append(cfg.Allow, "write_file", "run_command")
	e, err = policy.FromConfig(cfg, core.PostureStrict, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []core.CapabilityRequest{req("write_file", core.RiskWriteLocal), req("run_command", core.RiskExecuteLocal)} {
		if d := e.Evaluate(r, pc); d.Effect != core.EffectRequireApproval || d.Rule != "posture.strict" {
			t.Errorf("strict upgrade %s: %+v", r.Tool, d)
		}
	}
	p := e.Policy()
	if p.DefaultEffect != core.EffectDeny || len(p.Rules) == 0 || p.ID == "" || p.Name == "" {
		t.Fatalf("policy for trail: %+v", p)
	}
}

func TestMatrix(t *testing.T) {
	listing := []struct {
		name string
		cfg  func() config.ToolsConfig
	}{
		{"deny", func() config.ToolsConfig { return config.ToolsConfig{DefaultEffect: "allow", Deny: []string{"t"}} }},
		{"require", func() config.ToolsConfig {
			return config.ToolsConfig{DefaultEffect: "deny", RequireApproval: []string{"t"}}
		}},
		{"allow", func() config.ToolsConfig { return config.ToolsConfig{DefaultEffect: "deny", Allow: []string{"t"}} }},
		{"unlisted-allow", func() config.ToolsConfig { return config.ToolsConfig{DefaultEffect: "allow"} }},
		{"unlisted-deny", func() config.ToolsConfig { return config.ToolsConfig{DefaultEffect: "deny"} }},
	}
	dangerous := map[core.RiskClass]bool{core.RiskDestructive: true, core.RiskPrivileged: true, core.RiskSecretBearing: true}
	for _, l := range listing {
		for _, posture := range []core.PolicyPosture{core.PostureStrict, core.PostureStandard} {
			e, err := policy.FromConfig(l.cfg(), posture, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, risk := range core.RiskClasses {
				d := e.Evaluate(req("t", risk), core.PolicyContext{Posture: posture})
				want, rule := expected(l.name, posture, risk, dangerous[risk])
				if d.Effect != want || d.Rule != rule {
					t.Errorf("%s/%s/%s: got %s (%s) want %s (%s): %s", l.name, posture, risk, d.Effect, d.Rule, want, rule, d.Reason)
				}
				if d.Reason == "" {
					t.Errorf("%s/%s/%s: empty reason", l.name, posture, risk)
				}
			}
		}
	}
}

func expected(listing string, posture core.PolicyPosture, risk core.RiskClass, dangerous bool) (core.Effect, string) {
	listed := listing == "deny" || listing == "require" || listing == "allow"
	base := map[string]core.Effect{"deny": core.EffectDeny, "require": core.EffectRequireApproval, "allow": core.EffectAllow, "unlisted-allow": core.EffectAllow, "unlisted-deny": core.EffectDeny}[listing]
	rule := map[string]string{"deny": "tools.deny", "require": "tools.require_approval", "allow": "tools.allow", "unlisted-allow": "tools.default_effect", "unlisted-deny": "tools.default_effect"}[listing]
	if base == core.EffectDeny {
		return core.EffectDeny, rule
	}
	if dangerous && (!listed || posture != core.PostureStandard) {
		return core.EffectDeny, "risk.dangerous"
	}
	if base == core.EffectAllow && posture == core.PostureStrict && risk != core.RiskReadOnly {
		return core.EffectRequireApproval, "posture.strict"
	}
	return base, rule
}

func TestEvaluateFailsClosed(t *testing.T) {
	e, _ := policy.FromConfig(config.ToolsConfig{DefaultEffect: "allow"}, core.PostureStandard, nil)
	if d := e.Evaluate(req("t", "made_up"), core.PolicyContext{}); d.Effect != core.EffectDeny || d.Rule != "risk.unknown" {
		t.Fatalf("unknown risk: %+v", d)
	}
	if d := e.Evaluate(req("", core.RiskReadOnly), core.PolicyContext{}); d.Effect != core.EffectDeny {
		t.Fatalf("empty tool: %+v", d)
	}
	// Context posture stricter than configured wins.
	if d := e.Evaluate(req("t", core.RiskWriteLocal), core.PolicyContext{Posture: core.PostureStrict}); d.Effect != core.EffectRequireApproval {
		t.Fatalf("context strict: %+v", d)
	}
	strict, _ := policy.FromConfig(config.ToolsConfig{DefaultEffect: "allow"}, core.PostureStrict, nil)
	if d := strict.Evaluate(req("t", core.RiskWriteLocal), core.PolicyContext{Posture: core.PostureStandard}); d.Effect != core.EffectRequireApproval {
		t.Fatalf("configured strict must not be loosened by context: %+v", d)
	}
	var nilEngine *policy.Engine
	if d := nilEngine.Evaluate(req("t", core.RiskReadOnly), core.PolicyContext{}); d.Effect != core.EffectDeny {
		t.Fatalf("nil engine: %+v", d)
	}
}

func cmd(argv ...string) core.CapabilityRequest {
	return core.CapabilityRequest{Call: core.NewToolCallID(), Tool: "run_command", Capability: core.Capability{Risk: core.RiskExecuteLocal, Scope: core.ResourceScope{Kind: core.ScopeCommand, Argv: argv}}}
}

func TestCommandAllowList(t *testing.T) {
	cfg := config.ToolsConfig{DefaultEffect: "deny", Allow: []string{"run_command"}, Commands: config.CommandsConfig{Allowed: []string{"go test", " go  build ", ""}}}
	e, err := policy.FromConfig(cfg, core.PostureStandard, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := e.AllowedCommands(); len(got) != 2 || got[0][0] != "go" || got[0][1] != "test" || got[1][1] != "build" {
		t.Fatalf("AllowedCommands = %q", got)
	}
	pc := core.PolicyContext{Posture: core.PostureStandard}
	for _, argv := range [][]string{{"go", "test", "./..."}, {"go", "build"}} {
		if d := e.Evaluate(cmd(argv...), pc); d.Effect != core.EffectAllow || d.Rule != policy.RuleAllow {
			t.Errorf("%q: %+v", argv, d)
		}
	}
	for _, argv := range [][]string{{"rm", "-rf", "."}, {"go"}, {"go", "tests"}, {}} {
		d := e.Evaluate(cmd(argv...), pc)
		if d.Effect != core.EffectDeny || d.Rule != policy.RuleCommandAllowed {
			t.Errorf("%q: %+v", argv, d)
		}
		if !strings.Contains(d.Reason, "tools.commands.allowed") {
			t.Errorf("%q: reason %q", argv, d.Reason)
		}
	}
	// A listed deny still wins and keeps its own rule; non-command scopes are untouched.
	denied, _ := policy.FromConfig(config.ToolsConfig{DefaultEffect: "allow", Deny: []string{"run_command"}}, core.PostureStandard, nil)
	if d := denied.Evaluate(cmd("go", "test"), pc); d.Rule != policy.RuleDeny {
		t.Fatalf("deny listing: %+v", d)
	}
	if d := e.Evaluate(req("run_command", core.RiskExecuteLocal), pc); d.Effect != core.EffectAllow {
		t.Fatalf("non-command scope: %+v", d)
	}
	// Empty allow-list denies every command.
	none, _ := policy.FromConfig(config.ToolsConfig{DefaultEffect: "allow"}, core.PostureStandard, nil)
	if d := none.Evaluate(cmd("go", "test"), pc); d.Effect != core.EffectDeny || d.Rule != policy.RuleCommandAllowed {
		t.Fatalf("empty allow-list: %+v", d)
	}
	if n := len(none.AllowedCommands()); n != 0 {
		t.Fatalf("AllowedCommands on empty = %d", n)
	}
}

func TestCustomToolGating(t *testing.T) {
	cfg := defaults()
	cfg.Custom = map[string]config.CustomToolConfig{
		"lint":   {Argv: []string{"mylint", "--json"}},
		"listed": {Argv: []string{"othertool"}},
	}
	cfg.Allow = append(cfg.Allow, "listed")
	e, err := policy.FromConfig(cfg, core.PostureStandard, nil)
	if err != nil {
		t.Fatal(err)
	}
	pc := core.PolicyContext{WorkspaceRoot: "/w", Posture: core.PostureStandard}
	// An unlisted custom tool asks first, and its exact argv passes the
	// command scope instead of dying on tools.commands.allowed.
	r := core.CapabilityRequest{Call: core.NewToolCallID(), Tool: "lint", Capability: core.Capability{
		Risk: core.RiskExecuteLocal, Scope: core.ResourceScope{Kind: core.ScopeCommand, Argv: []string{"mylint", "--json"}},
	}}
	if d := e.Evaluate(r, pc); d.Effect != core.EffectRequireApproval || d.Rule != policy.RuleCustomTool {
		t.Fatalf("unlisted custom: %+v", d)
	}
	// A drifted argv still dies on the command allow-list.
	r.Capability.Scope.Argv = []string{"rm", "-rf", "/"}
	if d := e.Evaluate(r, pc); d.Effect != core.EffectDeny {
		t.Fatalf("drifted argv allowed: %+v", d)
	}
	// An explicit listing wins over the custom default.
	l := core.CapabilityRequest{Call: core.NewToolCallID(), Tool: "listed", Capability: core.Capability{
		Risk: core.RiskExecuteLocal, Scope: core.ResourceScope{Kind: core.ScopeCommand, Argv: []string{"othertool"}},
	}}
	if d := e.Evaluate(l, pc); d.Effect != core.EffectAllow || d.Rule != policy.RuleAllow {
		t.Fatalf("listed custom: %+v", d)
	}
	// Custom argv never spills into the shared command set: run_command
	// with a custom tool's argv still dies on tools.commands.allowed.
	if len(e.AllowedCommands()) != 0 {
		t.Fatalf("custom argv leaked into allowed commands: %+v", e.AllowedCommands())
	}
	cfg.Allow = append(cfg.Allow, "run_command")
	e2, err := policy.FromConfig(cfg, core.PostureStandard, nil)
	if err != nil {
		t.Fatal(err)
	}
	rc := core.CapabilityRequest{Call: core.NewToolCallID(), Tool: "run_command", Capability: core.Capability{
		Risk: core.RiskExecuteLocal, Scope: core.ResourceScope{Kind: core.ScopeCommand, Argv: []string{"mylint", "--json", "--extra"}},
	}}
	if d := e2.Evaluate(rc, pc); d.Effect != core.EffectDeny || d.Rule != policy.RuleCommandAllowed {
		t.Fatalf("run_command borrowed custom argv: %+v", d)
	}
}

func TestMCPToolGating(t *testing.T) {
	cfg := config.ToolsConfig{DefaultEffect: "allow", Allow: []string{"mcp_srv_safe"}, Deny: []string{"mcp_srv_evil"}}
	e, err := policy.FromConfig(cfg, core.PostureStandard, []string{"srv"})
	if err != nil {
		t.Fatal(err)
	}
	d := e.Evaluate(req("mcp_srv_echo", core.RiskExecuteLocal), core.PolicyContext{Posture: core.PostureStandard})
	if d.Effect != core.EffectRequireApproval || d.Rule != policy.RuleMCPTool {
		t.Fatalf("unlisted mcp tool got %s via %s", d.Effect, d.Rule)
	}
	if d := e.Evaluate(req("mcp_srv_safe", core.RiskExecuteLocal), core.PolicyContext{Posture: core.PostureStandard}); d.Effect != core.EffectAllow {
		t.Fatalf("exact allow listing beaten by prefix: %s via %s", d.Effect, d.Rule)
	}
	if d := e.Evaluate(req("mcp_srv_evil", core.RiskExecuteLocal), core.PolicyContext{Posture: core.PostureStandard}); d.Effect != core.EffectDeny {
		t.Fatalf("exact deny listing beaten by prefix: %s via %s", d.Effect, d.Rule)
	}
	if d := e.Evaluate(req("read_file", core.RiskReadOnly), core.PolicyContext{Posture: core.PostureStandard}); d.Effect != core.EffectAllow {
		t.Fatalf("non-mcp tool caught by prefix: %s via %s", d.Effect, d.Rule)
	}
}

func TestPermissions(t *testing.T) {
	cfg := config.ToolsConfig{DefaultEffect: "deny", Permissions: map[string]string{"write_file": "ask", "read_file": "allow"}}
	e, err := policy.FromConfig(cfg, core.PostureStandard, nil)
	if err != nil {
		t.Fatal(err)
	}
	d := e.Evaluate(req("write_file", core.RiskWriteLocal), core.PolicyContext{Posture: core.PostureStandard})
	if d.Effect != core.EffectRequireApproval || d.Rule != policy.RulePermissions {
		t.Fatalf("ask permission got %s via %s", d.Effect, d.Rule)
	}
	if d := e.Evaluate(req("read_file", core.RiskReadOnly), core.PolicyContext{Posture: core.PostureStandard}); d.Effect != core.EffectAllow {
		t.Fatalf("allow permission got %s via %s", d.Effect, d.Rule)
	}
	cfg.Allow = []string{"write_file"}
	if _, err := policy.FromConfig(cfg, core.PostureStandard, nil); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("permission + list conflict: %v", err)
	}
	cfg.Allow = nil
	cfg.Permissions["read_file"] = "maybe"
	if _, err := policy.FromConfig(cfg, core.PostureStandard, nil); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("bad permission effect: %v", err)
	}
}

func TestResourceRules(t *testing.T) {
	pathReq := func(tool, p string) core.CapabilityRequest {
		return core.CapabilityRequest{Call: core.NewToolCallID(), Tool: tool, Capability: core.Capability{Risk: core.RiskWriteLocal, Scope: core.ResourceScope{Kind: core.ScopePath, Path: p}}}
	}
	cfg := config.ToolsConfig{DefaultEffect: "deny", ExperimentalRules: true, Rules: []config.ToolRuleConfig{
		{Tool: "write_file", Path: "docs/**", Effect: "allow"},
		{Tool: "write_file", Path: "*.md", Effect: "deny"},
		{Tool: "run_command", Argv: []string{"go", "test"}, Effect: "allow"},
	}, Commands: config.CommandsConfig{Allowed: []string{"go test"}}}
	e, err := policy.FromConfig(cfg, core.PostureStandard, nil)
	if err != nil {
		t.Fatal(err)
	}
	pc := core.PolicyContext{Posture: core.PostureStandard}
	if d := e.Evaluate(pathReq("write_file", "docs/a.md"), pc); d.Effect != core.EffectAllow || d.Rule != policy.RuleResourceRule {
		t.Fatalf("docs subtree got %s via %s", d.Effect, d.Rule)
	}
	// readme.md misses docs/** and hits the *.md deny.
	if d := e.Evaluate(pathReq("write_file", "readme.md"), pc); d.Effect != core.EffectDeny {
		t.Fatalf("*.md deny got %s via %s", d.Effect, d.Rule)
	}
	if d := e.Evaluate(pathReq("write_file", "src/a.go"), pc); d.Effect != core.EffectDeny || d.Rule != policy.RuleDefault {
		t.Fatalf("unmatched path fell to %s via %s", d.Effect, d.Rule)
	}
	cmd := core.CapabilityRequest{Call: core.NewToolCallID(), Tool: "run_command", Capability: core.Capability{Risk: core.RiskExecuteLocal, Scope: core.ResourceScope{Kind: core.ScopeCommand, Argv: []string{"go", "test", "./..."}}}}
	if d := e.Evaluate(cmd, pc); d.Effect != core.EffectAllow || d.Rule != policy.RuleResourceRule {
		t.Fatalf("argv prefix rule got %s via %s", d.Effect, d.Rule)
	}

	// A deny rule fires when ANY comma-joined path matches — padding a
	// patch with an extra file never dodges it.
	multi := core.CapabilityRequest{Call: core.NewToolCallID(), Tool: "write_file", Capability: core.Capability{
		Risk: core.RiskWriteLocal, Scope: core.ResourceScope{Kind: core.ScopePath, Path: "notes.md,src/ok.go"},
	}}
	if d := e.Evaluate(multi, pc); d.Effect != core.EffectDeny || d.Rule != policy.RuleResourceRule {
		t.Fatalf("multi-path write dodged the deny rule: %+v", d)
	}
	// Flag off: identical rules are inert.
	cfg.ExperimentalRules = false
	e, err = policy.FromConfig(cfg, core.PostureStandard, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d := e.Evaluate(pathReq("write_file", "docs/a.md"), pc); d.Rule != policy.RuleDefault {
		t.Fatalf("rules active without flag: %s via %s", d.Effect, d.Rule)
	}
}
