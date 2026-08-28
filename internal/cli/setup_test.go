package cli

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ataidesorg/friday/internal/auth"
	"github.com/ataidesorg/friday/internal/config"
	"github.com/ataidesorg/friday/internal/redact"
	"github.com/ataidesorg/friday/internal/tui"
)

func TestWriteUserRouteRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path, err := writeUserRoute(dir, "openai", "openai", "gpt-4o", true)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, "config.toml") {
		t.Fatalf("path = %q", path)
	}
	resolved, err := config.Load(config.Options{ConfigDir: dir})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	rc, ok := resolved.Config.Models.Routes["openai"]
	if !ok || rc.Provider != "openai" || rc.Model != "gpt-4o" {
		t.Fatalf("route = %+v ok=%v", rc, ok)
	}
	if got := resolved.Config.Models.Routing.Default; got != "openai" {
		t.Fatalf("default = %q", got)
	}
	if needsModelSetup(resolved.Config) {
		t.Fatal("still needs setup after writing a route")
	}
}

func TestWriteUserRoutePreservesExistingKeys(t *testing.T) {
	dir := t.TempDir()
	pre := "[budgets]\nper_task_usd = 2.5\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(pre), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := writeUserRoute(dir, "anthropic", "anthropic", "claude-haiku-4-5", true); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "config.toml")) //nolint:gosec // temp dir
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "per_task_usd") {
		t.Fatalf("dropped existing key:\n%s", raw)
	}
	resolved, err := config.Load(config.Options{ConfigDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Config.Models.Routing.Default != "anthropic" {
		t.Fatalf("default not set:\n%s", raw)
	}
}

func TestCredReasonOfflineReadiness(t *testing.T) {
	cfg := config.Config{Providers: map[string]config.ProviderConfig{
		"localbox":  {BaseURL: "http://localhost:11434/v1"},
		"remotebox": {BaseURL: "https://api.example.com/v1"},
	}}
	rows := providerRows(cfg)
	resolver := auth.NewResolver(redact.New(), envLookupOK(nil), auth.WithGetenv(envLookup(nil)))
	if got := credReason(rowByID(t, rows, "localbox"), resolver, envLookupOK(nil)); got != "" {
		t.Fatalf("loopback custom should be ready, got %q", got)
	}
	if got := credReason(rowByID(t, rows, "remotebox"), resolver, envLookupOK(nil)); got != "no credential" {
		t.Fatalf("remote no-auth should need a credential, got %q", got)
	}
}

func rowByID(t *testing.T, rows []providerRow, id string) providerRow {
	t.Helper()
	for _, r := range rows {
		if r.id == id {
			return r
		}
	}
	t.Fatalf("no provider row %q", id)
	return providerRow{}
}

func TestFirstScreenTiersReadyPlusActions(t *testing.T) {
	rows := []providerRow{{id: "a"}, {id: "b"}, {id: "c"}}
	items, index := firstScreen(rows, []bool{false, true, false})
	labels := make([]string, len(items))
	for i, it := range items {
		labels[i] = it.label
	}
	want := []string{"b", "openai-compatible", "all providers"}
	if len(labels) != len(want) {
		t.Fatalf("labels = %v, want %v", labels, want)
	}
	for i := range want {
		if labels[i] != want[i] {
			t.Fatalf("labels = %v, want %v", labels, want)
		}
	}
	if index[0] != 1 || index[1] != pickCustom || index[2] != pickAll {
		t.Fatalf("index = %v", index)
	}
}

func TestCustomNameFromBaseURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://api.groq.com/openai/v1": "groq",
		"https://www.example.io/v1":      "example",
		"http://localhost:8080/v1":       "localhost",
		"https://ÜÑ.tld/v1":              "custom",
		"nonsense":                       "custom",
	} {
		if got := customName(in); got != want {
			t.Fatalf("customName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWriteUserProviderValidatesAndPreserves(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[agent]\nname = \"keepme\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := writeUserProvider(dir, "myendpoint", "https://llm.corp.example/v1", map[string]any{"source": "env", "name": "CORP_LLM_KEY"}); err != nil {
		t.Fatal(err)
	}
	if _, err := writeUserRoute(dir, "myendpoint", "myendpoint", "m-1", true); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "config.toml")) //nolint:gosec // test temp dir
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"keepme", "openai_compatible", "https://llm.corp.example/v1", "CORP_LLM_KEY", "public_cloud"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("config missing %q:\n%s", want, raw)
		}
	}
	cfg, err := config.Load(config.Options{ConfigDir: dir})
	if err != nil {
		t.Fatalf("written config does not load: %v", err)
	}
	pc, ok := cfg.Config.Providers["myendpoint"]
	if !ok || pc.Auth == nil || pc.Auth.Name != "CORP_LLM_KEY" || pc.Auth.Source != "env" {
		t.Fatalf("provider not decoded as written: %+v", pc)
	}
}

// readConfig loads the config the wizard wrote into a test temp dir.
func readConfig(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "config.toml")) //nolint:gosec // t.TempDir path in a test
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// The picker offers key-auth registry providers with a usable base URL,
// plus the OAuth providers whose endpoints the registry records; flows with no
// recorded endpoints (nous) stay out.
func TestConnectProviders(t *testing.T) {
	rows := connectProviders()
	byName := map[string]tui.ProviderInfo{}
	for _, r := range rows {
		byName[r.Name] = r
	}
	fw, ok := byName["fireworks"]
	if !ok {
		t.Fatalf("fireworks missing from %v", rows)
	}
	if fw.Detail != "api.fireworks.ai" {
		t.Fatalf("fireworks detail %q, want the endpoint host", fw.Detail)
	}
	if fw.KeyURL == "" || fw.OAuth {
		t.Fatalf("fireworks row wrong: %+v", fw)
	}
	codex, ok := byName["codex"]
	if !ok || !codex.OAuth || codex.Risk != "" {
		t.Fatalf("codex must be a no-risk OAuth row, got %+v", codex)
	}
	xo, ok := byName["xai-oauth"]
	if !ok || !xo.OAuth || xo.Risk != "" {
		t.Fatalf("xai-oauth must be a no-risk OAuth row, got %+v", xo)
	}
	cp, ok := byName["copilot"]
	if !ok || !cp.OAuth || cp.Risk != "" {
		t.Fatalf("copilot must be a no-risk OAuth row, got %+v", cp)
	}
	if _, ok := byName["anthropic-oauth"]; ok {
		t.Fatal("anthropic-oauth is vendor-prohibited and must stay out of /connect")
	}
	for _, absent := range []string{"nous", "copilot-acp", "gemini"} {
		if _, ok := byName[absent]; ok {
			t.Fatalf("%s has no recorded flow or base URL and must stay out", absent)
		}
	}
}

// connectWrite for a registry provider (via an alias) stores the secret,
// writes the route as the default on an empty config, and never puts the
// key anywhere but the encrypted store.
func TestConnectWriteRegistry(t *testing.T) {
	dir := t.TempDir()
	state := t.TempDir()
	env := []string{"FRIDAY_STATE_DIR=" + state}
	red := redact.New()
	provider, route, err := connectWrite(dir, config.Config{}, red, env, tui.ConnectRequest{
		Provider: "fw", Model: "qwen3", Key: "sk-connect-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if provider != "fireworks" || route != "fireworks" {
		t.Fatalf("got provider %q route %q", provider, route)
	}
	cfgText := readConfig(t, dir)
	for _, want := range []string{"[models.routes.fireworks]", `provider = "fireworks"`, `model = "qwen3"`, `default = "fireworks"`} {
		if !strings.Contains(cfgText, want) {
			t.Fatalf("config missing %q:\n%s", want, cfgText)
		}
	}
	if strings.Contains(cfgText, "sk-connect-test") {
		t.Fatalf("secret written to config:\n%s", cfgText)
	}
	if strings.Contains(cfgText, "[providers.fireworks]") {
		t.Fatalf("registry provider does not need a provider block:\n%s", cfgText)
	}
	if _, err := os.Stat(filepath.Join(state, "secrets.enc")); err != nil {
		t.Fatalf("secret store not written: %v", err)
	}
	if got := red.Redact("leak sk-connect-test end"); strings.Contains(got, "sk-connect-test") {
		t.Fatalf("key not registered with the redactor: %q", got)
	}
}

// A custom endpoint gets a provider block whose auth points at the secret
// store — a reference, never the credential.
func TestConnectWriteCustom(t *testing.T) {
	dir := t.TempDir()
	env := []string{"FRIDAY_STATE_DIR=" + t.TempDir()}
	provider, route, err := connectWrite(dir, config.Config{}, redact.New(), env, tui.ConnectRequest{
		BaseURL: "https://api.groq.com/openai/v1", Model: "m-1", Key: "k-custom",
	})
	if err != nil {
		t.Fatal(err)
	}
	if provider != "groq" || route != "groq" {
		t.Fatalf("got provider %q route %q", provider, route)
	}
	cfgText := readConfig(t, dir)
	for _, want := range []string{"[providers.groq]", `kind = "openai_compatible"`, `source = "secret_store"`, `id = "groq"`} {
		if !strings.Contains(cfgText, want) {
			t.Fatalf("config missing %q:\n%s", want, cfgText)
		}
	}
	if strings.Contains(cfgText, "k-custom") {
		t.Fatalf("secret written to config:\n%s", cfgText)
	}
}

// Connecting never clobbers a differently-aimed route and never steals an
// existing default; an identical reconnect reuses its route name.
func TestConnectWriteExistingRoutes(t *testing.T) {
	dir := t.TempDir()
	env := []string{"FRIDAY_STATE_DIR=" + t.TempDir()}
	if _, err := writeUserRoute(dir, "smart", "openai", "gpt-5", true); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Models: config.ModelsConfig{Routes: map[string]config.RouteConfig{
		"smart":     {Provider: "openai", Model: "gpt-5"},
		"fireworks": {Provider: "fireworks", Model: "old-model"},
	}}}
	_, route, err := connectWrite(dir, cfg, redact.New(), env, tui.ConnectRequest{
		Provider: "fireworks", Model: "new-model", Key: "k",
	})
	if err != nil {
		t.Fatal(err)
	}
	if route != "fireworks-2" {
		t.Fatalf("collision route %q, want fireworks-2", route)
	}
	if got := readConfig(t, dir); !strings.Contains(got, `default = "smart"`) {
		t.Fatalf("existing default stolen:\n%s", got)
	}
	if got := freeRouteName(cfg, "fireworks", "fireworks", "old-model"); got != "fireworks" {
		t.Fatalf("identical reconnect renamed to %q", got)
	}
}

// Bad requests fail before anything is stored or written.
func TestConnectWriteRejects(t *testing.T) {
	dir := t.TempDir()
	env := []string{"FRIDAY_STATE_DIR=" + t.TempDir()}
	cases := []tui.ConnectRequest{
		{Provider: "fireworks", Model: "", Key: "k"},
		{Provider: "no-such-provider", Model: "m", Key: "k"},
		{BaseURL: "ftp://files.example.com", Model: "m", Key: "k"},
		{Provider: "fireworks", Model: "m"},
		{BaseURL: "https://api.example.com/v1", Model: "m"},
	}
	for _, req := range cases {
		if _, _, err := connectWrite(dir, config.Config{}, redact.New(), env, req); err == nil {
			t.Fatalf("request %+v accepted", req)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("rejected request still wrote config: %v", err)
	}
}

// An OAuth provider connects with an empty key: the sign-in already stored
// the tokens, so only the route is written — no provider block, no secret.
func TestConnectWriteOAuthRouteOnly(t *testing.T) {
	dir := t.TempDir()
	state := t.TempDir()
	env := []string{"FRIDAY_STATE_DIR=" + state}
	provider, route, err := connectWrite(dir, config.Config{}, redact.New(), env, tui.ConnectRequest{
		Provider: "codex", Model: "gpt-5-codex",
	})
	if err != nil {
		t.Fatal(err)
	}
	if provider != "codex" || route != "codex" {
		t.Fatalf("got provider %q route %q", provider, route)
	}
	cfgText := readConfig(t, dir)
	for _, want := range []string{"[models.routes.codex]", `provider = "codex"`, `model = "gpt-5-codex"`} {
		if !strings.Contains(cfgText, want) {
			t.Fatalf("config missing %q:\n%s", want, cfgText)
		}
	}
	if strings.Contains(cfgText, "[providers.codex]") {
		t.Fatalf("route-only connect wrote a provider block:\n%s", cfgText)
	}
	if _, err := os.Stat(filepath.Join(state, "secrets.enc")); !os.IsNotExist(err) {
		t.Fatalf("route-only connect touched the secret store: %v", err)
	}
}

// The wizard's accepted risk consent lands in the user config and
// merges with an existing provider block instead of clobbering it.
func TestWriteUserProviderRisk(t *testing.T) {
	dir := t.TempDir()
	if err := writeUserProviderRisk(dir, "codex"); err != nil {
		t.Fatal(err)
	}
	if got := readConfig(t, dir); !strings.Contains(got, "accept_third_party_oauth_risk = true") {
		t.Fatalf("flag not written:\n%s", got)
	}
	ref := map[string]any{"source": "secret_store", "id": "groq"}
	if _, err := writeUserProvider(dir, "groq", "https://api.groq.com/openai/v1", ref); err != nil {
		t.Fatal(err)
	}
	if err := writeUserProviderRisk(dir, "groq"); err != nil {
		t.Fatal(err)
	}
	got := readConfig(t, dir)
	if !strings.Contains(got, `base_url = "https://api.groq.com/openai/v1"`) {
		t.Fatalf("risk write clobbered the provider block:\n%s", got)
	}
	if strings.Count(got, "accept_third_party_oauth_risk = true") != 2 {
		t.Fatalf("flag not merged into both blocks:\n%s", got)
	}
}

// lineWriter reassembles chunked writer output into trimmed, non-empty
// progress lines.
func TestLineWriter(t *testing.T) {
	var lines []string
	w := &lineWriter{fn: func(s string) { lines = append(lines, s) }}
	for _, chunk := range []string{"hel", "lo\nwor", "ld\n\n  x  \n", "tail no newline"} {
		if _, err := w.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"hello", "world", "x"}
	if len(lines) != len(want) {
		t.Fatalf("lines %v, want %v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("lines %v, want %v", lines, want)
		}
	}
}

// connectModels validates a custom-endpoint key against the live /models
// list on a loopback server, and refuses to guess for wires with no
// listable catalog.
func TestConnectModels(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		fmt.Fprint(w, `{"data":[{"id":"m-b"},{"id":"m-a"}]}`)
	}))
	defer srv.Close()
	cache := t.TempDir()
	cs := &chatSession{environ: []string{"XDG_CACHE_HOME=" + cache}}
	models, note := cs.connectModels(tui.ConnectRequest{BaseURL: srv.URL, Key: "sk-probe"})
	if note != "" || len(models) != 2 || models[0] != "m-a" {
		t.Fatalf("models %v note %q", models, note)
	}
	if gotAuth != "Bearer sk-probe" {
		t.Fatalf("key not sent as the bearer: %q", gotAuth)
	}

	if _, note := cs.connectModels(tui.ConnectRequest{Provider: "anthropic-oauth"}); !strings.Contains(note, "no listable model catalog") {
		t.Fatalf("anthropic wire not gated: %q", note)
	}
	if _, note := cs.connectModels(tui.ConnectRequest{Provider: "no-such"}); !strings.Contains(note, "unknown provider") {
		t.Fatalf("unknown provider note %q", note)
	}
}
