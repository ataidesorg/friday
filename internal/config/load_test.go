package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ataidesorg/ink/internal/core"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadDefaultsOnly(t *testing.T) {
	r, err := Load(Options{})
	if err != nil {
		t.Fatal(err)
	}
	want, _ := DefaultConfig()
	if !reflect.DeepEqual(r.Config, want) || !reflect.DeepEqual(r.Sources, []Source{{Layer: LayerDefaults}}) || len(r.Unknown) != 0 {
		t.Fatalf("defaults-only load: sources=%v unknown=%v", r.Sources, r.Unknown)
	}
	if chain := r.Provenance["sandbox.provider"]; len(chain) != 1 || chain[0].Source.Layer != LayerDefaults || chain[0].Value != "process" {
		t.Fatalf("provenance %+v", chain)
	}
}

func TestLoadSevenLayerPrecedence(t *testing.T) {
	cfg, proj := t.TempDir(), t.TempDir()
	write(t, filepath.Join(cfg, "config.toml"), "[profile]\nactive = \"work\"\n[sandbox]\nprovider = \"user\"\n[tools]\nallow = [\"only_me\"]\n")
	write(t, filepath.Join(cfg, "profiles", "work.toml"), "[sandbox]\nprovider = \"profile\"\n")
	write(t, filepath.Join(proj, ".ink", "config.toml"), "[sandbox]\nprovider = \"project\"\n[evals]\nmin_pass_rate = 80\n")
	write(t, filepath.Join(proj, ".ink", "config.local.toml"), "[sandbox]\nprovider = \"project_local\"\n")
	store := &TrustStore{Path: filepath.Join(t.TempDir(), "trust.toml")}
	trustAll := func(string, []string) (TrustDecision, error) { return TrustTrusted, nil }
	r, err := Load(Options{ConfigDir: cfg, ProjectRoot: proj, Environ: []string{"INK__SANDBOX__PROVIDER=env", "PATH=/bin"}, Overrides: map[string]string{"sandbox.provider": "cli"}, Trust: store, Prompt: trustAll})
	if err != nil {
		t.Fatal(err)
	}
	if r.Config.Sandbox.Provider != "cli" {
		t.Fatalf("provider = %q", r.Config.Sandbox.Provider)
	}
	chain := r.Provenance["sandbox.provider"]
	if len(chain) != len(LayerOrder) {
		t.Fatalf("chain %+v", chain)
	}
	for i, e := range chain {
		want := string(LayerOrder[i])
		if i == 0 {
			want = "process"
		}
		if e.Source.Layer != LayerOrder[i] || e.Value != want {
			t.Errorf("chain[%d] = %+v", i, e)
		}
	}
	if r.Config.Evals.MinPassRate != 80 || r.Config.Evals.Gate != "required" {
		t.Fatalf("table merge lost siblings: %+v", r.Config.Evals)
	}
	if !reflect.DeepEqual(r.Config.Tools.Allow, []string{"only_me"}) {
		t.Fatalf("array must replace: %v", r.Config.Tools.Allow)
	}
	if r.Config.Profile.Active != "work" || len(r.Sources) != 7 {
		t.Fatalf("profile=%q sources=%v", r.Config.Profile.Active, r.Sources)
	}
}

func TestLoadExplicitProfileAndErrors(t *testing.T) {
	cfg := t.TempDir()
	write(t, filepath.Join(cfg, "profiles", "fast.toml"), "[profiles.fast]\nstyle = \"detailed\"\nposture = \"standard\"\n")
	r, err := Load(Options{ConfigDir: cfg, Profile: "fast"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Config.Profile.Active != "fast" || r.Config.Profiles["fast"].Style != "detailed" {
		t.Fatalf("explicit profile: %+v", r.Config.Profiles)
	}
	bad := filepath.Join(cfg, "config.toml")
	write(t, bad, "[sandbox\n")
	if _, err := Load(Options{ConfigDir: cfg}); err == nil || !strings.Contains(err.Error(), bad) {
		t.Fatalf("invalid TOML must name the path: %v", err)
	}
	write(t, bad, "[sandbox]\nnetwrk = \"disabled\"\n[extra]\nx = 1\n")
	r, err = Load(Options{ConfigDir: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.Unknown, []string{"extra.x", "sandbox.netwrk"}) {
		t.Fatalf("unknown = %v", r.Unknown)
	}
	if _, err := Load(Options{Environ: []string{"INK____X=1"}}); err == nil {
		t.Fatal("empty env segment must fail")
	}
	if _, err := Load(Options{Overrides: map[string]string{"sandbox": "1"}}); err == nil {
		t.Fatal("scalar over table must fail")
	}
}

func TestParseEnv(t *testing.T) {
	m, err := parseEnv([]string{"HOME=/x", "INK__EVALS__MIN_PASS_RATE=50", "INK__SANDBOX__PROVIDER=container", "INK__EVALS__GATE=\"advisory\""})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"sandbox": map[string]any{"provider": "container"},
		"evals":   map[string]any{"gate": "advisory", "min_pass_rate": int64(50)},
	}
	if !reflect.DeepEqual(m, want) {
		t.Fatalf("parseEnv = %#v", m)
	}
	for _, bad := range []string{"INK____X=1", "INK__A=1\x00", "INK__=1"} {
		if _, err := parseEnv([]string{bad}); err == nil && bad != "INK__A=1\x00" {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := parseEnv([]string{"INK__A=1", "INK__A__B=2"}); err == nil {
		t.Error("value/table conflict accepted")
	}
	if v := ParseValue("true"); v != true {
		t.Errorf("ParseValue(true) = %#v", v)
	}
	if v := ParseValue("2.5"); v != 2.5 {
		t.Errorf("ParseValue(2.5) = %#v", v)
	}
}

func TestDir(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	cases := []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"INK_CONFIG_DIR": "/explicit", "XDG_CONFIG_HOME": "/xdg", "HOME": "/home"}, "/explicit"},
		{map[string]string{"XDG_CONFIG_HOME": "/xdg", "HOME": "/home"}, filepath.Join("/xdg", "ink")},
		{map[string]string{"HOME": "/home"}, filepath.Join("/home", ".config", "ink")},
	}
	for _, c := range cases {
		got, err := Dir(env(c.env))
		if err != nil || got != c.want {
			t.Errorf("Dir(%v) = %q, %v", c.env, got, err)
		}
	}
	if _, err := Dir(env(nil)); err == nil || !strings.Contains(err.Error(), core.ErrInvalidInput.Error()) {
		t.Errorf("no env: %v", err)
	}
}

func TestMergeDoesNotMutate(t *testing.T) {
	dst := map[string]any{"a": map[string]any{"x": int64(1), "y": int64(2)}, "list": []any{"a"}}
	src := map[string]any{"a": map[string]any{"x": int64(9)}, "list": []any{"b"}}
	prov := Provenance{}
	out := merge(dst, src, Source{Layer: LayerUser}, prov, "")
	if dst["a"].(map[string]any)["x"] != int64(1) || out["a"].(map[string]any)["x"] != int64(9) || out["a"].(map[string]any)["y"] != int64(2) {
		t.Fatalf("merge mutated or lost data: dst=%v out=%v", dst, out)
	}
	if !reflect.DeepEqual(out["list"], []any{"b"}) || len(prov["a.x"]) != 1 || len(prov["list"]) != 1 {
		t.Fatalf("out=%v prov=%v", out, prov)
	}
}

func TestLayerOrderHasProjectLocalAfterProject(t *testing.T) {
	want := []Layer{LayerDefaults, LayerUser, LayerProfile, LayerProject, LayerProjectLocal, LayerEnv, LayerCLI}
	if !reflect.DeepEqual(LayerOrder, want) {
		t.Fatalf("LayerOrder = %v", LayerOrder)
	}
}

const commandsProject = "[project]\nname = \"p\"\n[project.commands]\ntest = \"go test ./...\"\n"

// Project commands become the command allowlist and run during verification,
// so unlike the rest of project.*, they apply only after the file is trusted.
func TestLoadProjectCommandsGated(t *testing.T) {
	proj := t.TempDir()
	write(t, projectConfigPath(proj), commandsProject)
	r, err := Load(Options{ProjectRoot: proj})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Config.Project.Commands) != 0 || r.Config.Project.Name != "p" {
		t.Fatalf("untrusted commands applied: %+v", r.Config.Project)
	}
	if got := r.DroppedKeys(); !reflect.DeepEqual(got, map[RejectReason][]string{RejectUntrusted: {"project.commands.test"}}) {
		t.Fatalf("DroppedKeys = %v", got)
	}
	chain := r.Provenance["project.commands.test"]
	if len(chain) == 0 || !chain[len(chain)-1].Rejected || chain[len(chain)-1].Reason != RejectUntrusted {
		t.Fatalf("provenance: %+v", chain)
	}

	store := &TrustStore{Path: filepath.Join(t.TempDir(), "trust.toml")}
	trust := func(string, []string) (TrustDecision, error) { return TrustTrusted, nil }
	r, err = Load(Options{ProjectRoot: proj, Trust: store, Prompt: trust})
	if err != nil {
		t.Fatal(err)
	}
	if r.Config.Project.Commands["test"] != "go test ./..." || len(r.DroppedKeys()) != 0 {
		t.Fatalf("trusted commands not applied: %+v dropped=%v", r.Config.Project.Commands, r.DroppedKeys())
	}
}

const gatedProject = "[sandbox]\nprovider = \"container\"\n[tools]\ndefault_effect = \"allow\"\n[project]\nname = \"p\"\n"

func TestLoadProjectUntrustedWithoutStore(t *testing.T) {
	proj := t.TempDir()
	write(t, projectConfigPath(proj), gatedProject)
	r, err := Load(Options{ProjectRoot: proj})
	if err != nil {
		t.Fatal(err)
	}
	if r.Config.Sandbox.Provider != "process" || r.Config.Tools.DefaultEffect != "deny" || r.Config.Project.Name != "p" {
		t.Fatalf("untrusted keys applied: %+v", r.Config.Sandbox)
	}
	if _, ok := lookup(r.Merged, "tools.default_effect"); ok && r.Merged["tools"].(map[string]any)["default_effect"] != "deny" {
		t.Fatalf("merged carries the rejected value")
	}
	chain := r.Provenance["sandbox.provider"]
	if len(chain) != 2 || !chain[1].Rejected || chain[1].Reason != RejectUntrusted || chain[1].Value != "container" {
		t.Fatalf("provenance: %+v", chain)
	}
	want := map[RejectReason][]string{RejectUntrusted: {"sandbox.provider", "tools.default_effect"}}
	if got := r.DroppedKeys(); !reflect.DeepEqual(got, want) {
		t.Fatalf("DroppedKeys = %v", got)
	}
	if err := Validate(r); err != nil {
		t.Fatalf("untrusted keys are a warning, not a validation error: %v", err)
	}
}

func TestLoadProjectTrustFlow(t *testing.T) {
	proj := t.TempDir()
	path := projectConfigPath(proj)
	write(t, path, gatedProject)
	store := &TrustStore{Path: filepath.Join(t.TempDir(), "trust.toml")}

	// No prompt: nothing is recorded, keys drop as untrusted.
	r, err := Load(Options{ProjectRoot: proj, Trust: store})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.DroppedKeys(); !reflect.DeepEqual(got, map[RejectReason][]string{RejectUntrusted: {"sandbox.provider", "tools.default_effect"}}) {
		t.Fatalf("no prompt: %v", got)
	}
	if entries, _ := store.List(); len(entries) != 0 {
		t.Fatalf("a load without a prompt must not record: %+v", entries)
	}
	if keys, err := GatedKeys([]byte(gatedProject)); err != nil || !reflect.DeepEqual(keys, []string{"sandbox.provider", "tools.default_effect"}) {
		t.Fatalf("GatedKeys = %v, %v", keys, err)
	}
	if _, err := GatedKeys([]byte("[x\n")); err == nil {
		t.Fatal("GatedKeys must reject bad TOML")
	}

	// Prompt sees the path and the gated keys only; trusting applies and records the hash.
	var promptPath string
	var promptKeys []string
	prompt := func(p string, keys []string) (TrustDecision, error) {
		promptPath, promptKeys = p, keys
		return TrustTrusted, nil
	}
	r, err = Load(Options{ProjectRoot: proj, Trust: store, Prompt: prompt})
	if err != nil {
		t.Fatal(err)
	}
	if promptPath != path || !reflect.DeepEqual(promptKeys, []string{"sandbox.provider", "tools.default_effect"}) {
		t.Fatalf("prompt got %q %v", promptPath, promptKeys)
	}
	if r.Config.Sandbox.Provider != "container" || r.Config.Tools.DefaultEffect != "allow" || len(r.DroppedKeys()) != 0 {
		t.Fatalf("trusted keys not applied: %+v dropped=%v", r.Config.Sandbox, r.DroppedKeys())
	}
	data, _ := os.ReadFile(path) //nolint:gosec // test-owned temp dir
	e, found, _ := store.Lookup(path)
	if !found || e.Decision != TrustTrusted || e.SHA256 != fileSHA256(data) || e.DecidedAt.IsZero() {
		t.Fatalf("recorded entry: %+v found=%v", e, found)
	}

	// Trusted and unchanged: no prompt needed.
	calls := 0
	counting := func(string, []string) (TrustDecision, error) { calls++; return TrustUntrusted, nil }
	r, err = Load(Options{ProjectRoot: proj, Trust: store, Prompt: counting})
	if err != nil || calls != 0 || r.Config.Sandbox.Provider != "container" {
		t.Fatalf("trusted file re-prompted: calls=%d err=%v", calls, err)
	}

	// Edited after trust: hash_changed without a prompt; re-prompt with one;
	// declining sticks to that content, and editing again asks afresh.
	write(t, path, gatedProject+"\n# edited\n")
	r, err = Load(Options{ProjectRoot: proj, Trust: store})
	if err != nil || r.Config.Sandbox.Provider != "process" || !reflect.DeepEqual(r.DroppedKeys(), map[RejectReason][]string{RejectHashChanged: {"sandbox.provider", "tools.default_effect"}}) {
		t.Fatalf("hash changed: %v %v", r.DroppedKeys(), err)
	}
	r, err = Load(Options{ProjectRoot: proj, Trust: store, Prompt: counting})
	if err != nil || calls != 1 || !reflect.DeepEqual(r.DroppedKeys(), map[RejectReason][]string{RejectDeclined: {"sandbox.provider", "tools.default_effect"}}) {
		t.Fatalf("declined: calls=%d dropped=%v err=%v", calls, r.DroppedKeys(), err)
	}
	r, err = Load(Options{ProjectRoot: proj, Trust: store, Prompt: counting})
	if err != nil || calls != 1 || !reflect.DeepEqual(r.DroppedKeys(), map[RejectReason][]string{RejectDeclined: {"sandbox.provider", "tools.default_effect"}}) {
		t.Fatalf("unchanged declined content must not re-prompt: calls=%d dropped=%v err=%v", calls, r.DroppedKeys(), err)
	}
	write(t, path, gatedProject+"\n# edited twice\n")
	accepting := func(string, []string) (TrustDecision, error) { calls++; return TrustTrusted, nil }
	r, err = Load(Options{ProjectRoot: proj, Trust: store, Prompt: accepting})
	if err != nil || calls != 2 || r.Config.Sandbox.Provider != "container" || len(r.DroppedKeys()) != 0 {
		t.Fatalf("editing a declined file must ask again: calls=%d dropped=%v err=%v", calls, r.DroppedKeys(), err)
	}

	// A prompt error aborts the load.
	if err := store.Revoke(path); err != nil {
		t.Fatal(err)
	}
	failing := func(string, []string) (TrustDecision, error) { return "", errors.New("terminal closed") }
	if _, err := Load(Options{ProjectRoot: proj, Trust: store, Prompt: failing}); err == nil || !strings.Contains(err.Error(), "terminal closed") {
		t.Fatalf("prompt error: %v", err)
	}

	// A file inside the allowlist never prompts.
	write(t, path, "[project]\nname = \"quiet\"\n")
	calls = 0
	if _, err := Load(Options{ProjectRoot: proj, Trust: store, Prompt: counting}); err != nil || calls != 0 {
		t.Fatalf("allowlisted file prompted: calls=%d err=%v", calls, err)
	}
}

func TestLoadProjectLocalLayer(t *testing.T) {
	proj := t.TempDir()
	write(t, projectConfigPath(proj), "[evals]\nmin_pass_rate = 80\n[project]\nname = \"p\"\n")
	write(t, projectLocalConfigPath(proj), "[evals]\nmin_pass_rate = 50\n[sandbox]\nprovider = \"container\"\n")
	r, err := Load(Options{ProjectRoot: proj})
	if err != nil {
		t.Fatal(err)
	}
	if r.Config.Evals.MinPassRate != 50 || r.Config.Project.Name != "p" {
		t.Fatalf("local layer must override project: %+v", r.Config.Evals)
	}
	if len(r.Sources) != 3 || r.Sources[2].Layer != LayerProjectLocal || r.Sources[2].Path != projectLocalConfigPath(proj) {
		t.Fatalf("sources: %+v", r.Sources)
	}
	if got := r.DroppedKeys(); !reflect.DeepEqual(got, map[RejectReason][]string{RejectUntrusted: {"sandbox.provider"}}) {
		t.Fatalf("local file must obey the trust rule: %v", got)
	}
	store := &TrustStore{Path: filepath.Join(t.TempDir(), "trust.toml")}
	trustAll := func(string, []string) (TrustDecision, error) { return TrustTrusted, nil }
	r, err = Load(Options{ProjectRoot: proj, Trust: store, Prompt: trustAll})
	if err != nil || r.Config.Sandbox.Provider != "container" {
		t.Fatalf("trusted local file: %+v %v", r.Config.Sandbox, err)
	}
	if entries, _ := store.List(); len(entries) != 1 || entries[0].Path != projectLocalConfigPath(proj) {
		t.Fatalf("entries: %+v", entries)
	}
}

func TestProjectLayersNeverSetRiskFlags(t *testing.T) {
	proj := t.TempDir()
	write(t, projectConfigPath(proj), "[providers.codex]\nkind = \"openai_compatible\"\nprivacy = \"public_cloud\"\naccept_third_party_oauth_risk = true\n")
	store := &TrustStore{Path: filepath.Join(t.TempDir(), "trust.toml")}
	trustAll := func(string, []string) (TrustDecision, error) { return TrustTrusted, nil }
	r, err := Load(Options{ProjectRoot: proj, Trust: store, Prompt: trustAll})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := lookup(r.Merged, "providers.codex.accept_third_party_oauth_risk"); ok {
		t.Fatal("risk flag merged from a trusted project file")
	}
	if got := r.DroppedKeys(); !reflect.DeepEqual(got, map[RejectReason][]string{RejectAllowlist: {"providers.codex.accept_third_party_oauth_risk"}}) {
		t.Fatalf("DroppedKeys = %v", got)
	}
	if err := Validate(r); err != nil && strings.Contains(err.Error(), "accept_third_party_oauth_risk") {
		t.Fatalf("a dropped risk flag is a warning, not a validation error: %v", err)
	}
	if r.Config.Providers["codex"].Kind != "openai_compatible" {
		t.Fatalf("sibling keys must still apply when trusted: %+v", r.Config.Providers)
	}
}
