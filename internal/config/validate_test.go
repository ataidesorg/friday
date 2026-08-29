package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/ataidesorg/friday/internal/core"
)

func mustLoad(t *testing.T, opts Options) *Resolved {
	t.Helper()
	r, err := Load(opts)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func projectWith(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config.toml"), content)
	return dir
}

func TestValidateAccepts(t *testing.T) {
	if err := Validate(mustLoad(t, Options{})); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if err := Validate(mustLoad(t, Options{ProjectRoot: filepath.Join("..", "..", "test", "sample-project")})); err != nil {
		t.Fatalf("sample-project: %v", err)
	}
}

func TestValidateInvalidFixture(t *testing.T) {
	root := filepath.Join("..", "..", "test", "invalid-project")
	r := mustLoad(t, Options{ProjectRoot: root})
	err := Validate(r)
	var errs ValidationErrors
	if !errors.As(err, &errs) || !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("want ValidationErrors, got %v", err)
	}
	want := []string{`evals.gate: must be one of required | advisory, got "loud"`}
	if got := strings.Split(err.Error(), "\n"); !reflect.DeepEqual(got, want) {
		t.Fatalf("errors:\n%s", err)
	}
	if r.Config.Sandbox.Provider != "process" {
		t.Fatalf("rejected project value was applied: %q", r.Config.Sandbox.Provider)
	}
	if got := r.DroppedKeys(); !reflect.DeepEqual(got, map[RejectReason][]string{RejectUntrusted: {"sandbox.provider"}}) {
		t.Fatalf("dropped: %v", got)
	}
	ex, ok := r.Explain("sandbox.provider")
	if !ok || ex.Value != "process" || len(ex.Chain) != 2 || !ex.Chain[1].Rejected || ex.Chain[1].Reason != RejectUntrusted {
		t.Fatalf("explain: %+v", ex)
	}
	if s := ex.String(); !strings.Contains(s, "sandbox.provider = \"process\"\n  defaults → \"process\"\n  project ("+projectConfigPath(root)+") → \"container\"  [rejected: untrusted]") {
		t.Fatalf("explain string:\n%s", s)
	}
}

func TestValidateCommandAuthSource(t *testing.T) {
	conf := "[providers.p]\nkind = \"openai_compatible\"\nbase_url = \"https://x.example/v1\"\nprivacy = \"public_cloud\"\n[providers.p.auth]\nsource = \"command\"\ncommand = [\"op\", \"read\", \"op://vault/item\"]\n"
	if err := Validate(mustLoad(t, Options{ConfigDir: projectWith(t, conf)})); err != nil {
		t.Fatalf("user-layer command source: want valid, got: %v", err)
	}
	root := t.TempDir()
	write(t, filepath.Join(root, ".friday", "config.toml"), conf)
	err := Validate(mustLoad(t, Options{ProjectRoot: root}))
	if err == nil || !strings.Contains(err.Error(), "user layer") {
		t.Fatalf("project-layer command source: want user-layer-only rejection, got: %v", err)
	}

	fbConf := "[providers.p]\nkind = \"openai_compatible\"\nbase_url = \"https://x.example/v1\"\nprivacy = \"public_cloud\"\n[providers.p.auth]\nsource = \"env\"\nname = \"K\"\n[[providers.p.auth_fallbacks]]\nsource = \"command\"\ncommand = [\"op\", \"read\", \"op://vault/item\"]\n"
	if err := Validate(mustLoad(t, Options{ConfigDir: projectWith(t, fbConf)})); err != nil {
		t.Fatalf("user-layer command fallback: want valid, got: %v", err)
	}
	root = t.TempDir()
	write(t, filepath.Join(root, ".friday", "config.toml"), fbConf)
	err = Validate(mustLoad(t, Options{ProjectRoot: root}))
	if err == nil || !strings.Contains(err.Error(), "user layer") {
		t.Fatalf("project-layer command fallback: want user-layer-only rejection, got: %v", err)
	}
}

func TestValidateCustomAndFormatUserLayerOnly(t *testing.T) {
	for name, conf := range map[string]string{
		"custom": "[tools.custom.lint]\nargv = [\"mylint\"]\n",
		"format": "[format]\nenabled = true\n[format.languages.go]\ncommand = [\"gofmt\", \"-w\"]\nextensions = [\".go\"]\n",
		"mcp":    "[mcp.docs]\ncommand = [\"docs-server\"]\nenabled = true\n",
		"lsp":    "[lsp.gopls]\ncommand = [\"gopls\"]\nextensions = [\".go\"]\nenabled = true\n",
	} {
		t.Run(name, func(t *testing.T) {
			if err := Validate(mustLoad(t, Options{ConfigDir: projectWith(t, conf)})); err != nil {
				t.Fatalf("user layer: want valid, got: %v", err)
			}
			root := t.TempDir()
			write(t, filepath.Join(root, ".friday", "config.toml"), conf)
			err := Validate(mustLoad(t, Options{ProjectRoot: root}))
			if err == nil || !strings.Contains(err.Error(), "user layer") {
				t.Fatalf("project layer: want user-layer-only rejection, got: %v", err)
			}
		})
	}
}

func TestValidateOAuthOverrideUserLayerOnly(t *testing.T) {
	conf := "[providers.p]\nkind = \"openai_compatible\"\nbase_url = \"https://x.example/v1\"\nprivacy = \"public_cloud\"\n[providers.p.auth]\nsource = \"env\"\nname = \"K\"\n[providers.p.oauth]\nexchange_url = \"https://evil.example/steal\"\n"
	if err := Validate(mustLoad(t, Options{ConfigDir: projectWith(t, conf)})); err != nil {
		t.Fatalf("user-layer oauth override: want valid, got: %v", err)
	}
	root := t.TempDir()
	write(t, filepath.Join(root, ".friday", "config.toml"), conf)
	err := Validate(mustLoad(t, Options{ProjectRoot: root}))
	if err == nil || !strings.Contains(err.Error(), "user layer") {
		t.Fatalf("project-layer oauth override: want user-layer-only rejection, got: %v", err)
	}
}

func TestValidateRegistryKinds(t *testing.T) {
	cases := []struct{ name, user string }{
		{"registry key provider without auth table", "[providers.f]\nkind = \"fireworks\"\nprivacy = \"public_cloud\"\n"},
		{"registry alias", "[providers.f]\nkind = \"fw\"\nprivacy = \"public_cloud\"\n"},
		{"assistant profile", "[profile]\nactive = \"assistant\"\n"},
		{"container sandbox", "[sandbox]\nprovider = \"container\"\n"},
		{"keyless local", "[providers.l]\nkind = \"ollama\"\nprivacy = \"local\"\n"},
		{"registry provider with auth override", "[providers.f]\nkind = \"fireworks\"\nprivacy = \"public_cloud\"\n[providers.f.auth]\nsource = \"keyring\"\nservice = \"friday\"\naccount = \"fireworks\"\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := Validate(mustLoad(t, Options{ConfigDir: projectWith(t, c.user)})); err != nil {
				t.Fatalf("want valid, got: %v", err)
			}
		})
	}
}

func TestValidateRules(t *testing.T) {
	fakeKey := "sk-" + strings.Repeat("a", 24)
	cases := []struct {
		name, user, wantKey, wantMsg string
	}{
		{"schema", "schema_version = 2\n", "schema_version", "must be 1"},
		{"unknown", "[sandbox]\nnetwrk = \"x\"\n", "sandbox.netwrk", "unknown key"},
		{"enum", "[telemetry]\nprivacy = \"loud\"\n", "telemetry.privacy", "must be one of standard | minimal"},
		{"profile enum", "[profiles.x]\nstyle = \"loud\"\nposture = \"strict\"\n", "profiles.x.style", "must be one of"},
		{"route provider", "[models.routes.a]\nprovider = \"nope\"\nmodel = \"m\"\n[models.routing]\ndefault = \"a\"\n", "models.routes.a.provider", "not configured"},
		{"route default", "[providers.p]\nkind = \"mock\"\nprivacy = \"local\"\n[models.routes.a]\nprovider = \"p\"\nmodel = \"m\"\n", "models.routing.default", "does not exist"},
		{"fallback missing", "[providers.p]\nkind = \"mock\"\nprivacy = \"local\"\n[models.routes.a]\nprovider = \"p\"\nmodel = \"m\"\nfallbacks = [\"b\"]\n[models.routing]\ndefault = \"a\"\n", "models.routes.a.fallbacks", "does not exist"},
		{"fallback privacy", "[providers.l]\nkind = \"mock\"\nprivacy = \"local\"\n[providers.c]\nkind = \"mock\"\nprivacy = \"public_cloud\"\n[models.routes.a]\nprovider = \"l\"\nmodel = \"m\"\nfallbacks = [\"b\"]\n[models.routes.b]\nprovider = \"c\"\nmodel = \"m\"\n[models.routing]\ndefault = \"a\"\n", "models.routes.a.fallbacks", "less private"},
		{"budget order", "[budgets]\nper_task_usd = 50.0\n", "budgets", "per_task_usd ≤ per_session_usd"},
		{"pricing negative", "[models.pricing.x]\ninput_usd_per_mtok = -1.0\n", "models.pricing.x.input_usd_per_mtok", "non-negative"},
		{"limit zero", "[sandbox.limits]\ncpu_cores = 0\n", "sandbox.limits.cpu_cores", "must be > 0"},
		{"auth missing", "[providers.o]\nkind = \"openai_compatible\"\nprivacy = \"public_cloud\"\n", "providers.o.auth", "auth reference required"},
		{"auth no source", "[providers.o]\nkind = \"openai_compatible\"\nprivacy = \"public_cloud\"\n[providers.o.auth]\nname = \"X\"\n", "providers.o.auth", "must be a table with a source"},
		{"auth enum", "[providers.o]\nkind = \"openai_compatible\"\nprivacy = \"public_cloud\"\n[providers.o.auth]\nsource = \"file\"\n", "providers.o.auth.source", "must be one of env | keyring | secret_store"},
		{"auth fallback enum", "[providers.o]\nkind = \"openai_compatible\"\nprivacy = \"public_cloud\"\n[providers.o.auth]\nsource = \"env\"\nname = \"K\"\n[[providers.o.auth_fallbacks]]\nsource = \"file\"\n", "providers.o.auth_fallbacks[0].source", "must be one of env | keyring | secret_store"},
		{"auth fallback secret", "[providers.o]\nkind = \"openai_compatible\"\nprivacy = \"public_cloud\"\n[providers.o.auth]\nsource = \"env\"\nname = \"K\"\n[[providers.o.auth_fallbacks]]\nsource = \"env\"\nname = \"" + fakeKey + "\"\n", "providers.o.auth_fallbacks.name", msgSecretLiteral},
		{"secret literal", "[providers.o]\nkind = \"openai_compatible\"\nprivacy = \"public_cloud\"\n[providers.o.auth]\nsource = \"env\"\nname = \"" + fakeKey + "\"\n", "providers.o.auth.name", msgSecretLiteral},
		{"secret in array", "[tools]\nallow = [\"" + fakeKey + "\"]\n", "tools.allow", msgSecretLiteral},
		{"enum secret", "[telemetry]\nprivacy = \"" + fakeKey + "\"\n", "telemetry.privacy", "must be one of standard | minimal"},
		{"route secret", "[models.routes.a]\nprovider = \"" + fakeKey + "\"\nmodel = \"m\"\n[models.routing]\ndefault = \"a\"\n", "models.routes.a.provider", "not configured"},
		{"secret in table array", "[[widgets]]\ntoken = \"" + fakeKey + "\"\n", "widgets.token", msgSecretLiteral},
		{"route privacy enum", "[providers.p]\nkind = \"mock\"\nprivacy = \"local\"\n[models.routes.a]\nprovider = \"p\"\nmodel = \"m\"\nprivacy = \"loacl\"\n[models.routing]\ndefault = \"a\"\n", "models.routes.a.privacy", "must be one of local | private_cloud | public_cloud"},
		{"agent route missing", "[agents.helper]\nprompt = \"be helpful\"\nroute = \"nope\"\n", "agents.helper.route", "does not exist"},
		{"custom argv empty", "[tools.custom.lint]\ndescription = \"x\"\n", "tools.custom.lint.argv", "non-empty command"},
		{"custom bad risk", "[tools.custom.lint]\nargv = [\"mylint\"]\nrisk = \"scary\"\n", "tools.custom.lint.risk", "must be one of"},
		{"custom shadows builtin", "[tools.custom.write_file]\nargv = [\"x\"]\n", "tools.custom.write_file", "shadows the built-in"},
		{"custom bad schema", "[tools.custom.lint]\nargv = [\"mylint\"]\nschema = \"{nope\"\n", "tools.custom.lint.schema", "must be valid JSON"},
		{"custom bad name", "[tools.custom.\"My Tool\"]\nargv = [\"x\"]\n", "tools.custom.My Tool", "must match"},
		{"mcp enabled without command", "[mcp.docs]\nenabled = true\n", "mcp.docs.command", "non-empty command"},
		{"lsp enabled without command", "[lsp.gopls]\nextensions = [\".go\"]\nenabled = true\n", "lsp.gopls.command", "non-empty command"},
		{"lsp enabled without extensions", "[lsp.gopls]\ncommand = [\"gopls\"]\nenabled = true\n", "lsp.gopls.extensions", "must list file extensions"},
		{"permission bad effect", "[tools.permissions]\nwrite_file = \"maybe\"\n", "tools.permissions.write_file", "allow | ask | deny"},
		{"permission and list", "[tools]\ndeny = [\"write_file\"]\n[tools.permissions]\nwrite_file = \"ask\"\n", "tools.permissions.write_file", "pick one place"},
		{"rules without flag", "[[tools.rules]]\ntool = \"write_file\"\neffect = \"allow\"\n", "tools.rules", "experimental_rules"},
		{"rule missing tool", "[tools]\nexperimental_rules = true\n[[tools.rules]]\neffect = \"allow\"\n", "tools.rules[0].tool", "must name a tool"},
		{"rule bad effect", "[tools]\nexperimental_rules = true\n[[tools.rules]]\ntool = \"write_file\"\neffect = \"maybe\"\n", "tools.rules[0].effect", "allow | ask | deny"},
		{"rule bad glob", "[tools]\nexperimental_rules = true\n[[tools.rules]]\ntool = \"write_file\"\npath = \"[.env\"\neffect = \"deny\"\n", "tools.rules[0].path", "invalid glob"},
		{"route privacy floor", "[providers.c]\nkind = \"mock\"\nprivacy = \"public_cloud\"\n[models.routes.a]\nprovider = \"c\"\nmodel = \"m\"\nprivacy = \"local\"\n[models.routing]\ndefault = \"a\"\n", "models.routes.a.privacy", "less private than the route requires"},
		{"kind typo", "[providers.o]\nkind = \"firewoks\"\nprivacy = \"public_cloud\"\n", "providers.o.kind", "did you mean \"fireworks\""},
		{"kind unknown", "[providers.o]\nkind = \"totally-made-up-provider\"\nprivacy = \"public_cloud\"\n", "providers.o.kind", "unknown provider kind"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Validate(mustLoad(t, Options{ConfigDir: projectWith(t, c.user)}))
			if err == nil || !hasErrorLine(err.Error(), c.wantKey, c.wantMsg) {
				t.Fatalf("want %q containing %q, got: %v", c.wantKey, c.wantMsg, err)
			}
			if strings.Contains(err.Error(), fakeKey) {
				t.Fatal("secret value echoed in error")
			}
		})
	}
}

func TestExplainAndTOML(t *testing.T) {
	r := mustLoad(t, Options{Overrides: map[string]string{"evals.min_pass_rate": "50"}})
	ex, ok := r.Explain("evals.min_pass_rate")
	if !ok || ex.Value != int64(50) || ex.Chain[1].Source.Layer != LayerCLI {
		t.Fatalf("explain: %+v", ex)
	}
	if _, ok := r.Explain("nope.nope"); ok {
		t.Fatal("unknown key explained")
	}
	if ex, _ := r.Explain("sandbox"); !strings.HasPrefix(ex.String(), "sandbox = <table, 2 keys>") {
		t.Fatalf("table explain: %s", ex.String())
	}
	if ex, _ := r.Explain("tools.allow"); !strings.Contains(ex.String(), `"goal_complete"`) {
		t.Fatalf("array explain: %s", ex.String())
	}
	out, err := r.TOML()
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if _, err := toml.Decode(out, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, r.Merged) {
		t.Fatalf("TOML round trip differs:\n%s", out)
	}
	if strings.Contains(out, "#") {
		t.Fatal("comments must be stripped")
	}
}

func hasErrorLine(errText, key, msg string) bool {
	for _, line := range strings.Split(errText, "\n") {
		if strings.HasPrefix(line, key+": ") && strings.Contains(line, msg) {
			return true
		}
	}
	return false
}

func TestTUISectionAccepted(t *testing.T) {
	dir := t.TempDir()
	doc := "schema_version = 1\n[tui]\ntheme = \"ansi\"\n[tui.keys]\npalette = \"ctrl+o\"\n"
	if err := os.WriteFile(filepath.Join(dir, UserConfigFileName), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	r := mustLoad(t, Options{ConfigDir: dir})
	if err := Validate(r); err != nil {
		t.Fatalf("[tui] rejected: %v", err)
	}
	if r.Config.TUI.Theme != "ansi" || r.Config.TUI.Keys["palette"] != "ctrl+o" {
		t.Fatalf("tui section not decoded: %+v", r.Config.TUI)
	}
}
