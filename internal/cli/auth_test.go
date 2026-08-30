package cli

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// execAuth runs the CLI with a controllable stdin and INK_NO_KEYRING set
// so the OS keychain is never read or written: the secret-store key lands in
// secrets.key inside the test state dir instead.
func execAuth(t *testing.T, extra []string, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	environ := append([]string{"HOME=" + t.TempDir(), "PATH=/usr/bin", "INK_NO_KEYRING=1"}, extra...)
	code := Run(args, &stdout, &stderr, strings.NewReader(stdin), environ, func() (string, error) { return t.TempDir(), nil })
	return code, stdout.String(), stderr.String()
}

// TestAuthRoundTripNeverLeaks: auth set via piped
// stdin, auth status shows present, and the value is absent from all process
// output and every state file except the secrets.enc ciphertext.
func TestAuthRoundTripNeverLeaks(t *testing.T) {
	const secret = "roundtrip-secret-abc123xyz" // planted test fixture; gitleaks:allow
	state := t.TempDir()
	env := []string{"INK_STATE_DIR=" + state}
	cfg := t.TempDir()

	code, out, errOut := execAuth(t, env, secret+"\n", "auth", "set", "anthropic", "--config-dir", cfg)
	if code != 0 {
		t.Fatalf("auth set: %d\n%s%s", code, out, errOut)
	}
	if !strings.Contains(out, "stored credential for anthropic") {
		t.Fatalf("auth set stdout: %q", out)
	}
	if !strings.Contains(errOut, "not a terminal") {
		t.Fatalf("expected non-tty warning, got %q", errOut)
	}
	for _, s := range []string{out, errOut} {
		if strings.Contains(s, secret) {
			t.Fatalf("secret leaked into process output: %q", s)
		}
	}

	code, out, errOut = execAuth(t, env, "", "auth", "status", "--config-dir", cfg)
	if code != 0 {
		t.Fatalf("auth status: %d\n%s%s", code, out, errOut)
	}
	line := providerLine(t, out, "anthropic")
	if !strings.Contains(line, "present") {
		t.Fatalf("anthropic status line: %q", line)
	}
	if strings.Contains(out+errOut, secret) {
		t.Fatal("secret leaked into auth status output")
	}

	// Every state file: the plaintext must appear nowhere, ciphertext included.
	entries, err := os.ReadDir(state)
	if err != nil {
		t.Fatal(err)
	}
	var sawStore bool
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(state, e.Name())) //nolint:gosec // temp dir
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("secret in state file %s", e.Name())
		}
		if e.Name() == "secrets.enc" {
			sawStore = true
		}
	}
	if !sawStore {
		t.Fatalf("secrets.enc not written; state dir has %v", entries)
	}
}

func TestAuthSetCheckProbes(t *testing.T) {
	const secret = "check-secret-9f8e7d"
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := t.TempDir()
	conf := fmt.Sprintf(`[providers.localok]
kind = "openai_compatible"
base_url = %q
privacy = "local"
auth = { source = "secret_store", id = "localok" }
`, srv.URL)
	if err := os.WriteFile(filepath.Join(cfg, "config.toml"), []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{"INK_STATE_DIR=" + t.TempDir()}

	code, out, errOut := execAuth(t, env, secret+"\n", "auth", "set", "localok", "--check", "--config-dir", cfg)
	if code != 0 {
		t.Fatalf("auth set --check: %d\n%s%s", code, out, errOut)
	}
	if gotAuth != "Bearer "+secret {
		t.Fatalf("probe sent %q", gotAuth)
	}
	if !strings.Contains(out, "health: healthy") {
		t.Fatalf("expected healthy, got %q", out)
	}
	if strings.Contains(out+errOut, secret) {
		t.Fatal("secret leaked by --check")
	}
}

func TestAuthSetRefusals(t *testing.T) {
	env := []string{"INK_STATE_DIR=" + t.TempDir()}
	if code, _, errOut := execAuth(t, env, "x\n", "auth", "set", "nosuch-provider"); code != exitError || !strings.Contains(errOut, "unknown provider") {
		t.Fatalf("unknown provider: %d %q", code, errOut)
	}
	if code, _, errOut := execAuth(t, env, "\n", "auth", "set", "anthropic"); code != exitError || !strings.Contains(errOut, "empty") {
		t.Fatalf("empty secret: %d %q", code, errOut)
	}
	if code, _, errOut := execAuth(t, env, "x\n", "auth", "set"); code != exitUsage || !strings.Contains(errOut, "usage:") {
		t.Fatalf("missing id: %d %q", code, errOut)
	}
}

func TestAuthOptInRiskWarns(t *testing.T) {
	env := []string{"INK_STATE_DIR=" + t.TempDir()}
	code, _, errOut := execAuth(t, env, "tok\n", "auth", "set", "anthropic-oauth")
	if code != 0 {
		t.Fatalf("auth set anthropic-oauth: %d %q", code, errOut)
	}
	if !strings.Contains(errOut, "accept_third_party_oauth_risk") {
		t.Fatalf("expected risk warning, got %q", errOut)
	}
}

func TestAuthLoginNotImplemented(t *testing.T) {
	// copilot-acp is external_cli with no recorded OAuth endpoints: login
	// stays safely unavailable. (anthropic-oauth logs in via PKCE paste
	// mode; TestAuthLoginAnthropicPaste covers it.)
	env := []string{"INK_STATE_DIR=" + t.TempDir()}
	code, _, errOut := execAuth(t, env, "", "auth", "login", "copilot-acp")
	if code != exitNotImplemented {
		t.Fatalf("auth login: %d %q", code, errOut)
	}
	if !strings.Contains(errOut, "not implemented") || !strings.Contains(errOut, "copilot-acp") {
		t.Fatalf("login stderr: %q", errOut)
	}
}

// TestAuthLoginAnthropicPaste drives `ink auth login anthropic-oauth`:
// external_cli with recorded PKCE endpoints logs in via paste mode (the
// registry redirect_uri is a vendor-hosted code page, so no loopback
// listener exists). The token endpoint is overridden to a local server;
// the real claude.ai auth URL is only printed, never fetched.
func TestAuthLoginAnthropicPaste(t *testing.T) {
	access := "spy-anthropic-paste-access" //nolint:gosec // planted test value
	var gotCode, gotVerifier string
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		gotCode = r.PostForm.Get("code")
		gotVerifier = r.PostForm.Get("code_verifier")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":%q,"token_type":"Bearer","expires_in":3600}`, access)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := t.TempDir()
	conf := fmt.Sprintf("[providers.anthropic-oauth.oauth]\ntoken_url = %q\n", srv.URL+"/token")
	if err := os.WriteFile(filepath.Join(cfg, "config.toml"), []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{"INK_STATE_DIR=" + t.TempDir()}

	code, out, errOut := execAuth(t, env, "pasted-code-cli-1\n",
		"auth", "login", "anthropic-oauth", "--no-browser", "--config-dir", cfg)
	if code != 0 {
		t.Fatalf("paste login: %d\n%s%s", code, out, errOut)
	}
	if gotCode != "pasted-code-cli-1" || gotVerifier == "" {
		t.Fatalf("token exchange: code=%q verifier=%q", gotCode, gotVerifier)
	}
	if !strings.Contains(errOut, "accept_third_party_oauth_risk") {
		t.Fatalf("risk warning missing: %q", errOut)
	}
	if !strings.Contains(errOut, "claude.ai/oauth/authorize") {
		t.Fatalf("auth URL prompt missing: %q", errOut)
	}
	if !strings.Contains(out, "logged in to anthropic-oauth") {
		t.Fatalf("login stdout: %q", out)
	}
	if strings.Contains(out+errOut, access) {
		t.Fatalf("credential leaked: %s%s", out, errOut)
	}
}

func TestAuthStatusEmpty(t *testing.T) {
	env := []string{"INK_STATE_DIR=" + t.TempDir()}
	code, out, errOut := execAuth(t, env, "", "auth", "status")
	if code != 0 {
		t.Fatalf("auth status: %d %q", code, errOut)
	}
	if !strings.Contains(out, "no credentials configured") {
		t.Fatalf("empty status: %q", out)
	}
}

// TestAuthLoginLogoutPKCE drives the paste-mode PKCE login end to end at the
// CLI layer: the token endpoint is an httptest IdP wired in via the
// providers.<id>.oauth config override, stdin supplies the pasted code, and
// logout removes the stored set.
func TestAuthLoginLogoutPKCE(t *testing.T) {
	const access = "cli-access-token-1a2b3c"
	var gotGrant, gotCode, gotVerifier string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		gotGrant = r.PostForm.Get("grant_type")
		gotCode = r.PostForm.Get("code")
		gotVerifier = r.PostForm.Get("code_verifier")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":%q,"token_type":"Bearer","expires_in":3600}`, access)
	}))
	defer srv.Close()

	cfg := t.TempDir()
	conf := fmt.Sprintf(`[providers.nous.oauth]
auth_url = "https://idp.example/authorize"
token_url = %q
client_id = "cli-test-client"
`, srv.URL)
	if err := os.WriteFile(filepath.Join(cfg, "config.toml"), []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{"INK_STATE_DIR=" + t.TempDir()}

	code, out, errOut := execAuth(t, env, "pasted-code-77\n", "auth", "login", "nous", "--no-browser", "--config-dir", cfg)
	if code != 0 {
		t.Fatalf("auth login: %d\n%s%s", code, out, errOut)
	}
	if gotGrant != "authorization_code" || gotCode != "pasted-code-77" || gotVerifier == "" {
		t.Fatalf("exchange: grant=%q code=%q verifier=%q", gotGrant, gotCode, gotVerifier)
	}
	if !strings.Contains(out, "logged in to nous") {
		t.Fatalf("login stdout: %q", out)
	}
	if !strings.Contains(errOut, "https://idp.example/authorize") {
		t.Fatalf("paste mode must print the authorize URL, got %q", errOut)
	}
	if strings.Contains(out+errOut, access) {
		t.Fatal("access token leaked into login output")
	}

	code, out, errOut = execAuth(t, env, "", "auth", "status", "--config-dir", cfg)
	if code != 0 {
		t.Fatalf("auth status: %d %q", code, errOut)
	}
	if line := providerLine(t, out, "nous"); !strings.Contains(line, "present") {
		t.Fatalf("nous after login: %q", line)
	}
	if strings.Contains(out, access) {
		t.Fatal("access token leaked into status output")
	}

	code, out, errOut = execAuth(t, env, "", "auth", "logout", "nous")
	if code != 0 || !strings.Contains(out, "removed stored login for nous") {
		t.Fatalf("auth logout: %d %q %q", code, out, errOut)
	}
	code, out, _ = execAuth(t, env, "", "auth", "logout", "nous")
	if code != 0 || !strings.Contains(out, "no stored login") {
		t.Fatalf("second logout: %d %q", code, out)
	}
}

// TestAuthLoginFailClosed covers the honest-unavailability paths: a PKCE
// provider with no recorded endpoints refuses with the config-override hint,
// and key-based providers point at `auth set`. (endpoint-less external_cli
// stays NotImplemented; TestAuthLoginNotImplemented covers it.)
func TestAuthLoginFailClosed(t *testing.T) {
	env := []string{"INK_STATE_DIR=" + t.TempDir()}
	code, _, errOut := execAuth(t, env, "", "auth", "login", "nous")
	if code != exitError {
		t.Fatalf("nous login: %d %q", code, errOut)
	}
	for _, want := range []string{"unverified", "providers.nous.oauth"} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("stderr %q must mention %q", errOut, want)
		}
	}
	code, _, errOut = execAuth(t, env, "", "auth", "login", "fireworks")
	if code != exitError || !strings.Contains(errOut, "auth set fireworks") {
		t.Fatalf("fireworks login: %d %q", code, errOut)
	}
}

// TestAuthLoginDevice drives `ink auth login` for a device-kind provider
// end to end against a local IdP: xai-oauth with both endpoints overridden
// so no real network is touched.
func TestAuthLoginDevice(t *testing.T) {
	access := "spy-device-cli-access" //nolint:gosec // planted test value
	var gotChallenge string
	mux := http.NewServeMux()
	mux.HandleFunc("/device", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		gotChallenge = r.PostForm.Get("code_challenge")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"device_code":"dev-cli-1","user_code":"ABCD-9876","verification_uri":"https://idp.example/activate","expires_in":900,"interval":1}`)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":%q,"token_type":"Bearer","expires_in":3600}`, access)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := t.TempDir()
	conf := fmt.Sprintf(`[providers.xai-oauth.oauth]
device_auth_url = %q
token_url = %q
client_id = "cli-dev-client"
`, srv.URL+"/device", srv.URL+"/token")
	if err := os.WriteFile(filepath.Join(cfg, "config.toml"), []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{"INK_STATE_DIR=" + t.TempDir()}

	code, out, errOut := execAuth(t, env, "", "auth", "login", "xai-oauth", "--no-browser", "--config-dir", cfg)
	if code != 0 {
		t.Fatalf("device login: %d\n%s%s", code, out, errOut)
	}
	if gotChallenge == "" {
		t.Fatal("device request must carry a PKCE challenge")
	}
	for _, want := range []string{"https://idp.example/activate", "ABCD-9876"} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("stderr %q must show %q", errOut, want)
		}
	}
	if !strings.Contains(out, "logged in to xai-oauth") {
		t.Fatalf("login stdout: %q", out)
	}
	if strings.Contains(out+errOut, access) {
		t.Fatal("access token leaked into login output")
	}

	code, out, errOut = execAuth(t, env, "", "auth", "logout", "xai-oauth")
	if code != 0 || !strings.Contains(out, "removed stored login for xai-oauth") {
		t.Fatalf("device logout: %d %q %q", code, out, errOut)
	}
}

// TestAuthSetCheckRiskSkipsProbe: storing a credential for an opt-in-risk
// provider is allowed, but --check must not send it anywhere without the
// flag. The base URL points at a server that fails the test if touched.
func TestAuthSetCheckRiskSkipsProbe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("probe must not run without the risk opt-in")
	}))
	defer srv.Close()

	cfg := t.TempDir()
	conf := fmt.Sprintf("[providers.anthropic-oauth]\nbase_url = %q\n", srv.URL)
	if err := os.WriteFile(filepath.Join(cfg, "config.toml"), []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{"INK_STATE_DIR=" + t.TempDir()}

	code, out, errOut := execAuth(t, env, "spy-risk-set-1\n",
		"auth", "set", "anthropic-oauth", "--check", "--config-dir", cfg)
	if code != 0 {
		t.Fatalf("auth set --check: %d\n%s%s", code, out, errOut)
	}
	if !strings.Contains(errOut, "probe skipped") || !strings.Contains(errOut, "accept_third_party_oauth_risk") {
		t.Fatalf("skip warning missing: %q", errOut)
	}
	if strings.Contains(out, "health:") {
		t.Fatalf("no health line without opt-in: %q", out)
	}
}
