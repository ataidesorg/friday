package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/ataidesorg/ink/internal/core"
	"github.com/ataidesorg/ink/internal/providers"
	"github.com/ataidesorg/ink/internal/redact"
)

// SchemaVersion is the only config schema this build accepts.
const SchemaVersion = 1

// ProjectLayerGatedPrefixes are the keys a repository's .ink/config.toml
// may not set until its owner trusts the file: they reach the network with
// your prompts, launch commands, downgrade privacy, spend money, or take the
// sandbox away. Everything else — routes, profiles, evals, project
// metadata, TUI — merges without asking.
var ProjectLayerGatedPrefixes = []string{"budgets", "lsp", "mcp", "providers", "sandbox", "telemetry", "tools"}

const msgSecretLiteral = "secret literal in config; use an auth reference"

// ValidationError is one rule violation at one key.
type ValidationError struct{ Key, Message string }

func (e ValidationError) Error() string { return e.Key + ": " + e.Message }

// ValidationErrors aggregates every violation so the user fixes them at once.
type ValidationErrors []ValidationError

func (es ValidationErrors) Error() string {
	lines := make([]string, 0, len(es))
	for _, e := range es {
		lines = append(lines, e.Error())
	}
	return strings.Join(lines, "\n")
}

// Is makes errors.Is(err, core.ErrInvalidInput) true for validation failures.
func (ValidationErrors) Is(target error) bool { return target == core.ErrInvalidInput }

type rule func(*Resolved) []ValidationError

var rules = []rule{ruleSchemaVersion, ruleUnknownKeys, ruleEnums, ruleRoutes, ruleBudgets, rulePricing, ruleAuthAndSecrets, ruleOAuth, ruleAgents, ruleCustomTools, rulePermissions}

// Validate runs every rule and returns ValidationErrors sorted by key, or nil.
func Validate(r *Resolved) error {
	var errs ValidationErrors
	for _, rule := range rules {
		errs = append(errs, rule(r)...)
	}
	if len(errs) == 0 {
		return nil
	}
	sort.SliceStable(errs, func(i, j int) bool { return errs[i].Key < errs[j].Key })
	return errs
}

// fail builds one violation. The message is redacted because rule arguments
// may echo a user value that is itself a secret.
func fail(key, format string, args ...any) ValidationError {
	return ValidationError{Key: key, Message: messageRedactor.Redact(fmt.Sprintf(format, args...))}
}

var messageRedactor = redact.New()

func ruleSchemaVersion(r *Resolved) []ValidationError {
	if r.Config.SchemaVersion != SchemaVersion {
		return []ValidationError{fail("schema_version", "must be %d, got %d", SchemaVersion, r.Config.SchemaVersion)}
	}
	return nil
}

func ruleUnknownKeys(r *Resolved) []ValidationError {
	var errs []ValidationError
	for _, k := range r.Unknown {
		errs = append(errs, fail(k, "unknown key"))
	}
	return errs
}

var enumValues = map[string][]string{
	"sandbox.provider":        {"unavailable", "process", "container"},
	"tools.default_effect":    {string(core.EffectDeny), string(core.EffectRequireApproval), string(core.EffectAllow)},
	"telemetry.privacy":       {string(core.PrivacyStandard), string(core.PrivacyMinimal)},
	"evals.gate":              {"required", "advisory"},
	"profiles.*.style":        {"concise", "detailed"},
	"profiles.*.posture":      {"strict", "standard"},
	"providers.*.privacy":     {string(core.PrivacyLocal), string(core.PrivacyPrivateCloud), string(core.PrivacyPublicCloud)},
	"providers.*.auth.source": {"env", "keyring", "secret_store", "command"},
	"models.routes.*.privacy": {string(core.PrivacyLocal), string(core.PrivacyPrivateCloud), string(core.PrivacyPublicCloud)},
}

func checkEnum(errs []ValidationError, pattern, key, value string) []ValidationError {
	allowed := enumValues[pattern]
	for _, a := range allowed {
		if a == value {
			return errs
		}
	}
	return append(errs, fail(key, "must be one of %s, got %q", strings.Join(allowed, " | "), value))
}

func ruleEnums(r *Resolved) []ValidationError {
	c := r.Config
	var errs []ValidationError
	errs = checkEnum(errs, "sandbox.provider", "sandbox.provider", c.Sandbox.Provider)
	errs = checkEnum(errs, "tools.default_effect", "tools.default_effect", c.Tools.DefaultEffect)
	errs = checkEnum(errs, "telemetry.privacy", "telemetry.privacy", c.Telemetry.Privacy)
	errs = checkEnum(errs, "evals.gate", "evals.gate", c.Evals.Gate)
	for _, name := range sortedKeys(c.Profiles) {
		p := c.Profiles[name]
		errs = checkEnum(errs, "profiles.*.style", "profiles."+name+".style", p.Style)
		errs = checkEnum(errs, "profiles.*.posture", "profiles."+name+".posture", p.Posture)
	}
	for _, name := range sortedKeys(c.Providers) {
		p := c.Providers[name]
		if kerr := providers.KnownKind(p.Kind); kerr != nil {
			errs = append(errs, fail("providers."+name+".kind", "%s", kerr.Error()))
		}
		errs = checkEnum(errs, "providers.*.privacy", "providers."+name+".privacy", p.Privacy)
		if p.Auth != nil {
			errs = checkEnum(errs, "providers.*.auth.source", "providers."+name+".auth.source", p.Auth.Source)
		}
		for i, fb := range p.AuthFallbacks {
			errs = checkEnum(errs, "providers.*.auth.source", fmt.Sprintf("providers.%s.auth_fallbacks[%d].source", name, i), fb.Source)
		}
	}
	return errs
}

func ruleRoutes(r *Resolved) []ValidationError {
	c := r.Config
	var errs []ValidationError
	for _, name := range sortedKeys(c.Models.Routes) {
		rt := c.Models.Routes[name]
		key := "models.routes." + name
		provider, ok := c.Providers[rt.Provider]
		privacy := provider.Privacy
		if !ok {
			if entry, hit := providers.Lookup(rt.Provider); hit {
				ok = true // registry providers need no [providers] stanza
				privacy = string(entry.Privacy)
			} else {
				errs = append(errs, fail(key+".provider", "provider %q is not configured and is not a registry id", rt.Provider))
			}
		}
		if rt.Privacy != "" {
			before := len(errs)
			errs = checkEnum(errs, "models.routes.*.privacy", key+".privacy", rt.Privacy)
			if ok && len(errs) == before && !core.PrivacyClass(rt.Privacy).AllowsFallbackTo(core.PrivacyClass(privacy)) {
				errs = append(errs, fail(key+".privacy", "provider %q (%s) is less private than the route requires (%s)", rt.Provider, privacy, rt.Privacy))
			}
		}
		for _, fb := range rt.Fallbacks {
			fbRoute, ok := c.Models.Routes[fb]
			if !ok {
				errs = append(errs, fail(key+".fallbacks", "route %q does not exist", fb))
				continue
			}
			fbProvider := c.Providers[fbRoute.Provider]
			if !core.PrivacyClass(provider.Privacy).AllowsFallbackTo(core.PrivacyClass(fbProvider.Privacy)) {
				errs = append(errs, fail(key+".fallbacks", "fallback %q (%s) is less private than provider %q (%s)", fb, fbProvider.Privacy, rt.Provider, provider.Privacy))
			}
		}
	}
	def := c.Models.Routing.Default
	if _, ok := c.Models.Routes[def]; !ok && (def != "" || len(c.Models.Routes) > 0) {
		errs = append(errs, fail("models.routing.default", "route %q does not exist", def))
	}
	return errs
}

func ruleBudgets(r *Resolved) []ValidationError {
	b, l := r.Config.Budgets, r.Config.Sandbox.Limits
	var errs []ValidationError
	for key, v := range map[string]float64{
		"budgets.per_task_usd": b.PerTaskUSD, "budgets.per_session_usd": b.PerSessionUSD, "budgets.per_day_usd": b.PerDayUSD,
		"sandbox.limits.cpu_cores": float64(l.CPUCores), "sandbox.limits.memory_mb": float64(l.MemoryMB), "sandbox.limits.disk_mb": float64(l.DiskMB),
		"sandbox.limits.max_processes": float64(l.MaxProcesses), "sandbox.limits.wall_clock_secs": float64(l.WallClockSecs),
	} {
		if v <= 0 {
			errs = append(errs, fail(key, "must be > 0, got %v", v))
		}
	}
	if b.PerTaskUSD > b.PerSessionUSD || b.PerSessionUSD > b.PerDayUSD {
		errs = append(errs, fail("budgets", "per_task_usd ≤ per_session_usd ≤ per_day_usd required, got %v ≤ %v ≤ %v", b.PerTaskUSD, b.PerSessionUSD, b.PerDayUSD))
	}
	return errs
}

func rulePricing(r *Resolved) []ValidationError {
	var errs []ValidationError
	for model, p := range r.Config.Models.Pricing {
		for field, v := range map[string]float64{
			"input_usd_per_mtok": p.InputUSDPerMTok, "output_usd_per_mtok": p.OutputUSDPerMTok, "cached_usd_per_mtok": p.CachedUSDPerMTok,
		} {
			if _, err := core.USDFromFloat(v); err != nil {
				errs = append(errs, fail(fmt.Sprintf("models.pricing.%s.%s", model, field), "must be a finite non-negative USD amount, got %v", v))
			}
		}
	}
	return errs
}

func ruleAuthAndSecrets(r *Resolved) []ValidationError {
	var errs []ValidationError
	for _, name := range sortedKeys(r.Config.Providers) {
		p := r.Config.Providers[name]
		key := "providers." + name + ".auth"
		raw, present := lookup(r.Merged, key)
		table, isTable := raw.(map[string]any)
		switch {
		case !present && p.Kind == string(core.ProviderOpenAICompatible):
			errs = append(errs, fail(key, "auth reference required: [%s] source = \"env\" | \"keyring\" | \"secret_store\"", key))
		case present && (!isTable || table["source"] == nil):
			errs = append(errs, fail(key, "must be a table with a source field; never a credential"))
		}
	}
	for key, chain := range r.Provenance {
		parts := splitKey(key)
		isSource := strings.HasPrefix(key, "providers.") && strings.HasSuffix(key, ".auth.source")
		isFallbacks := strings.HasPrefix(key, "providers.") && strings.HasSuffix(key, ".auth_fallbacks")
		isOAuth := len(parts) >= 4 && parts[0] == "providers" && parts[2] == "oauth"
		if !isSource && !isFallbacks && !isOAuth {
			continue
		}
		for _, e := range chain {
			fromRepo := e.Source.Layer == LayerProject || e.Source.Layer == LayerProjectLocal
			if !fromRepo {
				continue
			}
			if isSource && e.Value == "command" {
				errs = append(errs, fail(key, "auth source \"command\" runs a program and is user layer only; set it in your user config, not the repository"))
			}
			if isFallbacks && fallbacksUseCommand(e.Value) {
				errs = append(errs, fail(key, "auth source \"command\" runs a program and is user layer only; set it in your user config, not the repository"))
			}
			if isOAuth {
				errs = append(errs, fail(key, "oauth endpoint overrides route real credentials to a URL and are user layer only; set them in your user config, not the repository"))
			}
		}
	}
	return append(errs, secretLiterals(redact.New(), r.Merged, "")...)
}

// fallbacksUseCommand reports whether a raw auth_fallbacks array contains a
// command-source reference; repository layers may never configure one.
func fallbacksUseCommand(v any) bool {
	var tables []map[string]any
	switch arr := v.(type) {
	case []map[string]any:
		tables = arr
	case []any:
		for _, el := range arr {
			if t, ok := el.(map[string]any); ok {
				tables = append(tables, t)
			}
		}
	}
	for _, t := range tables {
		if t["source"] == "command" {
			return true
		}
	}
	return false
}

// secretLiterals walks every value in the merged tree, including arrays of
// tables, and reports each key holding a secret-shaped string once.
func secretLiterals(d *redact.Redactor, v any, key string) []ValidationError {
	var errs []ValidationError
	switch x := v.(type) {
	case string:
		if d.ContainsSecret(x) {
			errs = append(errs, fail(key, "%s", msgSecretLiteral))
		}
	case map[string]any:
		for _, k := range sortedKeys(x) {
			errs = append(errs, secretLiterals(d, x[k], joinKey(key, k))...)
		}
	case []map[string]any:
		for _, table := range x {
			errs = append(errs, secretLiterals(d, table, key)...)
		}
	case []any:
		for _, item := range x {
			errs = append(errs, secretLiterals(d, item, key)...)
		}
	}
	return dedupe(errs)
}

func joinKey(prefix, k string) string {
	if prefix == "" {
		return k
	}
	return prefix + "." + k
}

func dedupe(errs []ValidationError) []ValidationError {
	var out []ValidationError
	for _, e := range errs {
		if len(out) == 0 || out[len(out)-1] != e {
			out = append(out, e)
		}
	}
	return out
}

// rulePermissions checks the per-tool permission map and the experimental
// resource rules: effects are enums, a tool never sits in both a coarse
// list and permissions, and rules demand the experimental flag.
func rulePermissions(r *Resolved) []ValidationError {
	var errs []ValidationError
	t := r.Config.Tools
	listed := map[string]bool{}
	for _, l := range [][]string{t.Allow, t.RequireApproval, t.Deny} {
		for _, name := range l {
			listed[name] = true
		}
	}
	for _, name := range sortedKeys(t.Permissions) {
		if !validPermission(t.Permissions[name]) {
			errs = append(errs, fail("tools.permissions."+name, "must be one of allow | ask | deny"))
		}
		if listed[name] {
			errs = append(errs, fail("tools.permissions."+name, "also appears in a tools allow/require_approval/deny list; pick one place"))
		}
	}
	if len(t.Rules) > 0 && !t.ExperimentalRules {
		errs = append(errs, fail("tools.rules", "requires tools.experimental_rules = true"))
	}
	for i, rl := range t.Rules {
		key := fmt.Sprintf("tools.rules[%d]", i)
		if rl.Tool == "" {
			errs = append(errs, fail(key+".tool", "must name a tool"))
		}
		if !validPermission(rl.Effect) {
			errs = append(errs, fail(key+".effect", "must be one of allow | ask | deny"))
		}
		// A malformed glob would silently never match — a deny rule that
		// never fires is a hole, so bad patterns are load errors.
		if pat := rl.Path; pat != "" && !strings.HasSuffix(pat, "/**") {
			if _, err := path.Match(pat, "probe"); err != nil {
				errs = append(errs, fail(key+".path", "invalid glob pattern"))
			}
		}
	}
	return errs
}

func validPermission(v string) bool { return v == "allow" || v == "ask" || v == "deny" }

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ruleOAuth sanity-checks providers.*.oauth overrides: URLs must parse as
// http(s) and the loopback port must be valid. Empty fields are fine — they
// fall back to the registry values.
func ruleOAuth(r *Resolved) []ValidationError {
	var errs []ValidationError
	for _, name := range sortedKeys(r.Config.Providers) {
		o := r.Config.Providers[name].OAuth
		if o == nil {
			continue
		}
		key := "providers." + name + ".oauth"
		for field, v := range map[string]string{
			".auth_url":        o.AuthURL,
			".token_url":       o.TokenURL,
			".device_auth_url": o.DeviceAuthURL,
			".redirect_uri":    o.RedirectURI,
			".exchange_url":    o.ExchangeURL,
		} {
			if v == "" {
				continue
			}
			u, err := url.Parse(v)
			if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
				errs = append(errs, fail(key+field, "must be an http(s) URL, got %q", v))
			}
		}
		if o.RedirectPort < 0 || o.RedirectPort > 65535 {
			errs = append(errs, fail(key+".redirect_port", "must be 0-65535, got %d", o.RedirectPort))
		}
	}
	return errs
}

// ruleAgents checks each named agent: a route it names must exist.
// builtinToolNames are registry names a custom tool may not shadow.
var builtinToolNames = map[string]bool{
	"read_file": true, "list_dir": true, "search": true, "write_file": true,
	"apply_patch": true, "run_command": true, "skill": true, "ask_user_question": true,
	"todo_write": true, "goal_complete": true, "goal_blocked": true, "goal_wait": true,
}

var customToolName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// ruleCustomTools checks [tools.custom.*] declarations and keeps custom
// tools and formatters out of repository config — both run programs.
func ruleCustomTools(r *Resolved) []ValidationError {
	c := r.Config
	var errs []ValidationError
	for _, name := range sortedKeys(c.Tools.Custom) {
		key := "tools.custom." + name
		ct := c.Tools.Custom[name]
		if !customToolName.MatchString(name) {
			errs = append(errs, fail(key, "tool name must match [a-z][a-z0-9_]*"))
		}
		if builtinToolNames[name] {
			errs = append(errs, fail(key, "shadows the built-in %s tool", name))
		}
		if len(ct.Argv) == 0 || ct.Argv[0] == "" {
			errs = append(errs, fail(key+".argv", "must be a non-empty command"))
		}
		if ct.Risk != "" && !validRiskClass(ct.Risk) {
			errs = append(errs, fail(key+".risk", "must be one of %s", riskClassList()))
		}
		if ct.Schema != "" && !json.Valid([]byte(ct.Schema)) {
			errs = append(errs, fail(key+".schema", "must be valid JSON"))
		}
	}
	for _, name := range sortedKeys(c.MCP) {
		if s := c.MCP[name]; s.Enabled && (len(s.Command) == 0 || s.Command[0] == "") {
			errs = append(errs, fail("mcp."+name+".command", "must be a non-empty command when the server is enabled"))
		}
	}
	for _, name := range sortedKeys(c.LSP) {
		s := c.LSP[name]
		if s.Enabled && (len(s.Command) == 0 || s.Command[0] == "") {
			errs = append(errs, fail("lsp."+name+".command", "must be a non-empty command when the server is enabled"))
		}
		if s.Enabled && len(s.Extensions) == 0 {
			errs = append(errs, fail("lsp."+name+".extensions", "must list file extensions when the server is enabled"))
		}
	}
	for key, chain := range r.Provenance {
		if !strings.HasPrefix(key, "tools.custom.") && !strings.HasPrefix(key, "format.") && !strings.HasPrefix(key, "mcp.") && !strings.HasPrefix(key, "lsp.") {
			continue
		}
		for _, e := range chain {
			if e.Source.Layer == LayerProject || e.Source.Layer == LayerProjectLocal {
				errs = append(errs, fail(key, "runs a program and is user layer only; set it in your user config, not the repository"))
				break
			}
		}
	}
	return errs
}

func validRiskClass(risk string) bool {
	for _, rc := range core.RiskClasses {
		if string(rc) == risk {
			return true
		}
	}
	return false
}

func riskClassList() string {
	names := make([]string, len(core.RiskClasses))
	for i, rc := range core.RiskClasses {
		names[i] = string(rc)
	}
	return strings.Join(names, " | ")
}

func ruleAgents(r *Resolved) []ValidationError {
	c := r.Config
	var errs []ValidationError
	for _, name := range sortedKeys(c.Agents) {
		a := c.Agents[name]
		if a.Route != "" {
			if _, ok := c.Models.Routes[a.Route]; !ok {
				errs = append(errs, fail("agents."+name+".route", "route %q does not exist", a.Route))
			}
		}
	}
	return errs
}
