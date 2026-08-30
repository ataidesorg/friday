package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ataidesorg/ink/internal/providers"
)

func providerLine(t *testing.T, out, id string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, id+" ") {
			return line
		}
	}
	t.Fatalf("no row for %q in output:\n%s", id, out)
	return ""
}

func TestProvidersListOffline(t *testing.T) {
	code, out, errOut := exec(t, "providers")
	if code != 0 {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
	if !strings.Contains(out, "ID") || !strings.Contains(out, "HEALTH") {
		t.Fatalf("missing header:\n%s", out)
	}
	anthropic := providerLine(t, out, "anthropic")
	if !strings.Contains(anthropic, "anthropic_messages") || !strings.Contains(anthropic, "unverified") {
		t.Fatalf("anthropic row: %q", anthropic)
	}
	providerLine(t, out, "ollama")
	// Offline listing never probes: no measured state appears.
	for _, s := range []string{"healthy", "degraded", "unreachable"} {
		if strings.Contains(out, s) {
			t.Fatalf("offline listing contains %q:\n%s", s, out)
		}
	}
}

func TestProvidersCheckProbesAndNeverLeaks(t *testing.T) {
	const secret = "super-secret-token-xyz"
	var gotAuth string
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer ok.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer bad.Close()

	dir := t.TempDir()
	cfg := fmt.Sprintf(`[providers.localok]
kind = "openai_compatible"
base_url = %q
privacy = "local"
auth = { source = "env", name = "LOCALOK_KEY" }

[providers.localbad]
kind = "openai_compatible"
base_url = %q
privacy = "local"
auth = { source = "env", name = "LOCALBAD_KEY" }
`, ok.URL, bad.URL)
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	env := []string{"LOCALOK_KEY=" + secret, "LOCALBAD_KEY=wrong-key", "INK_STATE_DIR=" + t.TempDir()}
	code, out, errOut := execEnv(t, env, "providers", "--check", "--config-dir", dir)
	if code != 0 {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
	if gotAuth != "Bearer "+secret {
		t.Fatalf("probe sent auth %q", gotAuth)
	}
	if line := providerLine(t, out, "localok"); !strings.Contains(line, "healthy") {
		t.Fatalf("localok row: %q", line)
	}
	if line := providerLine(t, out, "localbad"); !strings.Contains(line, "degraded") || !strings.Contains(line, "401") {
		t.Fatalf("localbad row: %q", line)
	}
	// An optional-key cloud provider without a key is not a keyless local.
	if line := providerLine(t, out, "actual"); !strings.Contains(line, "not probed") {
		t.Fatalf("actual row probed without credential: %q", line)
	}
	// Acceptance: no credential and no Authorization header value anywhere.
	for _, leak := range []string{secret, "Bearer "} {
		if strings.Contains(out, leak) || strings.Contains(errOut, leak) {
			t.Fatalf("output leaks %q:\nstdout: %s\nstderr: %s", leak, out, errOut)
		}
	}
}

// TestProbeRowRiskOptIn: `providers --check` never probes an opt-in-risk
// provider without the user-layer flag — the row says why.
func TestProbeRowRiskOptIn(t *testing.T) {
	entry, ok := providers.Lookup("anthropic-oauth")
	if !ok {
		t.Fatal("anthropic-oauth missing from registry")
	}
	row := providerRow{id: entry.ID, registry: true, entry: entry}
	got := probeRow(row, nil, func(string) (string, bool) { return "", false })
	if !strings.Contains(got, "accept_third_party_oauth_risk") {
		t.Fatalf("probeRow = %q", got)
	}
}

const modelTestConf = `[models.routes.fast]
provider = "anthropic"
model = "claude-haiku-4-5"

[models.routes.deep]
provider = "anthropic"
model = "claude-opus-5"

[models.routing]
default = "fast"

[budgets]
per_task_usd = 1.5
`

func modelDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(modelTestConf), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestModelListNonTTY(t *testing.T) {
	code, out, errOut := exec(t, "model", "--config-dir", modelDir(t))
	if code != 0 {
		t.Fatalf("model: %d\n%s%s", code, out, errOut)
	}
	if !strings.Contains(out, "* fast") || !strings.Contains(out, "  deep") {
		t.Fatalf("route listing: %q", out)
	}
	if !strings.Contains(out, "--set") {
		t.Fatalf("expected --set hint: %q", out)
	}
}

func TestModelSetWritesUserConfig(t *testing.T) {
	dir := modelDir(t)
	code, out, errOut := exec(t, "model", "--set", "deep", "--config-dir", dir)
	if code != 0 {
		t.Fatalf("model --set: %d\n%s%s", code, out, errOut)
	}
	if !strings.Contains(out, "models.routing.default = deep") {
		t.Fatalf("confirmation: %q", out)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "config.toml")) //nolint:gosec // temp dir
	if err != nil {
		t.Fatal(err)
	}
	// The new default is written and the rest of the user layer survives.
	for _, want := range []string{`default = "deep"`, "claude-haiku-4-5", "per_task_usd = 1.5"} {
		if !bytes.Contains(raw, []byte(want)) {
			t.Fatalf("rewritten config missing %q:\n%s", want, raw)
		}
	}
	if code, out, _ := exec(t, "model", "--config-dir", dir); code != 0 || !strings.Contains(out, "* deep") {
		t.Fatalf("default not applied: %d %q", code, out)
	}
}

func TestModelSetCreatesConfigWhenAbsent(t *testing.T) {
	// Routes come from a project layer; the user layer does not exist yet.
	proj := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proj, ".ink"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, ".ink", "config.toml"), []byte("[models.routes.fast]\nprovider = \"anthropic\"\nmodel = \"claude-haiku-4-5\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	env := []string{"INK_STATE_DIR=" + state}
	if code, _, errOut := execEnv(t, env, "trust", filepath.Join(proj, ".ink", "config.toml")); code != 0 {
		t.Fatalf("trust: %d %q", code, errOut)
	}
	dir := t.TempDir() // empty user config dir
	code, out, errOut := execEnv(t, env, "model", "--set", "fast", "--project", proj, "--config-dir", dir)
	if code != 0 {
		t.Fatalf("model --set: %d\n%s%s", code, out, errOut)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "config.toml")) //nolint:gosec // temp dir
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`default = "fast"`)) {
		t.Fatalf("created config: %s", raw)
	}
}

func TestModelSetUnknownRoute(t *testing.T) {
	code, _, errOut := exec(t, "model", "--set", "nosuch", "--config-dir", modelDir(t))
	if code != exitError || !strings.Contains(errOut, "unknown route") {
		t.Fatalf("unknown route: %d %q", code, errOut)
	}
}

func TestModelNoRoutes(t *testing.T) {
	code, _, errOut := exec(t, "model", "--config-dir", t.TempDir())
	if code != exitError || !strings.Contains(errOut, "no routes") {
		t.Fatalf("no routes: %d %q", code, errOut)
	}
}

// modelsFixture serves /v1/models and records the Authorization header.
func modelsFixture(t *testing.T, auth *string, ids ...string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*auth = r.Header.Get("Authorization")
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		type m struct {
			ID string `json:"id"`
		}
		data := make([]m, 0, len(ids))
		for _, id := range ids {
			data = append(data, m{ID: id})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
}

func modelsCfgDir(t *testing.T, baseURL string) string {
	t.Helper()
	dir := t.TempDir()
	cfg := fmt.Sprintf(`[providers.locallist]
kind = "openai_compatible"
base_url = %q
privacy = "local"
auth = { source = "env", name = "LOCALLIST_KEY" }
`, baseURL+"/v1")
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestModelsCommandListsAndSendsBearer(t *testing.T) {
	var auth string
	srv := modelsFixture(t, &auth, "zeta", "alpha")
	defer srv.Close()
	cache := t.TempDir()
	env := []string{
		"LOCALLIST_KEY=spy-models-key-1",
		"XDG_CACHE_HOME=" + cache,
		"INK_STATE_DIR=" + t.TempDir(),
	}
	dir := modelsCfgDir(t, srv.URL)
	code, out, errOut := execEnv(t, env, "models", "--provider", "locallist", "--config-dir", dir)
	if code != 0 {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
	if out != "alpha\nzeta\n" {
		t.Fatalf("stdout = %q, want sorted list", out)
	}
	if auth != "Bearer spy-models-key-1" {
		t.Fatalf("fetch auth = %q", auth)
	}
	// Cache landed under XDG_CACHE_HOME/ink with 0600.
	path := filepath.Join(cache, "ink", "models-locallist.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("cache perm = %o", perm)
	}
	// No secret in any output.
	for _, s := range []string{out, errOut} {
		if strings.Contains(s, "spy-models-key-1") {
			t.Fatalf("output leaks credential: %q", s)
		}
	}

	// Second call: served from cache, no fetch (kill the server first).
	srv.Close()
	code, out, errOut = execEnv(t, env, "models", "--provider", "locallist", "--config-dir", dir)
	if code != 0 || out != "alpha\nzeta\n" {
		t.Fatalf("cached call: code %d out %q stderr %q", code, out, errOut)
	}
}

func TestModelsCommandMissingCredentialDegradesKeyless(t *testing.T) {
	var auth string
	srv := modelsFixture(t, &auth, "m1")
	defer srv.Close()
	env := []string{
		"XDG_CACHE_HOME=" + t.TempDir(),
		"INK_STATE_DIR=" + t.TempDir(),
	}
	code, out, errOut := execEnv(t, env, "models", "--provider", "locallist", "--config-dir", modelsCfgDir(t, srv.URL))
	if code != 0 {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
	if out != "m1\n" || auth != "" {
		t.Fatalf("keyless fetch: out %q auth %q", out, auth)
	}
}

func TestModelsCommandUsageAndBadProvider(t *testing.T) {
	if code, _, _ := exec(t, "models"); code != exitUsage {
		t.Fatalf("no --provider: code %d", code)
	}
	code, _, errOut := execEnv(t, []string{"INK_STATE_DIR=" + t.TempDir()}, "models", "--provider", "no-such-provider")
	if code != exitError || !strings.Contains(errOut, "no-such-provider") {
		t.Fatalf("unknown provider: code %d stderr %q", code, errOut)
	}
	// A non-OpenAI wire has no /v1/models catalog.
	code, _, errOut = execEnv(t, []string{"INK_STATE_DIR=" + t.TempDir()}, "models", "--provider", "anthropic")
	if code != exitError || !strings.Contains(errOut, "no /v1/models catalog") {
		t.Fatalf("anthropic wire: code %d stderr %q", code, errOut)
	}
}
