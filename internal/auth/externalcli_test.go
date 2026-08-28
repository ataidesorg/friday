package auth

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ataidesorg/friday/internal/config"
	"github.com/ataidesorg/friday/internal/core"
	"github.com/ataidesorg/friday/internal/providers"
)

func anthropicEntry(t *testing.T) providers.Entry {
	t.Helper()
	entry, ok := providers.Lookup("anthropic-oauth")
	if !ok {
		t.Fatal("anthropic-oauth missing from registry")
	}
	return entry
}

func claudeJSON(t *testing.T, token string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"claudeAiOauth": map[string]any{"accessToken": token}})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestClaudeCodeEnvOverrideOrder(t *testing.T) {
	spy := &spyRegistrar{}
	env := map[string]string{ //nolint:gosec // planted test values
		"ANTHROPIC_TOKEN":         "spy-anthropic-env-1",
		"CLAUDE_CODE_OAUTH_TOKEN": "spy-claude-env-2", // gitleaks:allow
	}
	r := testResolver(t, spy, env)
	cred, err := r.ForProvider(context.Background(), anthropicEntry(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cred.Zero()
	if got := string(cred.Value()); got != "spy-anthropic-env-1" {
		t.Errorf("credential = %q, want ANTHROPIC_TOKEN first", got)
	}
	if !spy.saw("spy-anthropic-env-1") {
		t.Error("token not registered with redact")
	}

	delete(env, "ANTHROPIC_TOKEN")
	cred2, err := testResolver(t, &spyRegistrar{}, env).ForProvider(context.Background(), anthropicEntry(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cred2.Zero()
	if got := string(cred2.Value()); got != "spy-claude-env-2" {
		t.Errorf("credential = %q, want CLAUDE_CODE_OAUTH_TOKEN second", got)
	}
}

func TestClaudeCodeOwnStoreHit(t *testing.T) {
	spy := &spyRegistrar{}
	r := testResolver(t, spy, nil)
	raw, err := json.Marshal(tokenSet{AccessToken: "spy-own-store-token-1"}) //nolint:gosec // planted test value
	if err != nil {
		t.Fatal(err)
	}
	if err := r.StoreSet(oauthStorePrefix+"anthropic-oauth", string(raw)); err != nil {
		t.Fatal(err)
	}
	cred, err := r.ForProvider(context.Background(), anthropicEntry(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cred.Zero()
	if got := string(cred.Value()); got != "spy-own-store-token-1" {
		t.Errorf("credential = %q, want the stored login", got)
	}
}

func TestClaudeCodeKeychainParsed(t *testing.T) {
	spy := &spyRegistrar{}
	var service, account string
	r := testResolver(t, spy, nil, WithKeyringLookup(func(_ context.Context, s, a string) (string, bool, error) {
		service, account = s, a
		return claudeJSON(t, "spy-cc-keychain-1"), true, nil
	}))
	cred, err := r.ForProvider(context.Background(), anthropicEntry(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cred.Zero()
	if got := string(cred.Value()); got != "spy-cc-keychain-1" {
		t.Errorf("credential = %q", got)
	}
	if service != claudeKeychainService || account != "" {
		t.Errorf("keychain lookup (%q, %q), want (%q, \"\")", service, account, claudeKeychainService)
	}
	if !spy.saw("spy-cc-keychain-1") {
		t.Error("keychain token not registered with redact")
	}
}

func TestClaudeCodeKeychainMalformedFailsClosed(t *testing.T) {
	secret := "sk-cc-partial-9f8e7d6c" //nolint:gosec // planted test fixture; gitleaks:allow
	r := testResolver(t, &spyRegistrar{}, nil, WithKeyringLookup(func(context.Context, string, string) (string, bool, error) {
		return "{broken json " + secret, true, nil
	}))
	_, err := r.ForProvider(context.Background(), anthropicEntry(t), nil)
	if err == nil {
		t.Fatal("want fail-closed error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "keychain") || !strings.Contains(msg, "malformed JSON") {
		t.Errorf("error = %q, want keychain + malformed JSON", msg)
	}
	if strings.Contains(msg, secret) {
		t.Errorf("error leaks store content: %q", msg)
	}

	// Present but tokenless: fail closed too, never a partial token.
	r = testResolver(t, &spyRegistrar{}, nil, WithKeyringLookup(func(context.Context, string, string) (string, bool, error) {
		return `{"claudeAiOauth":{}}`, true, nil
	}))
	_, err = r.ForProvider(context.Background(), anthropicEntry(t), nil)
	if err == nil || !strings.Contains(err.Error(), "no claudeAiOauth.accessToken") {
		t.Errorf("tokenless item error = %v", err)
	}
}

// fileResolver builds a resolver whose HOME and state dir live under
// t.TempDir(), with the keyring unavailable so the file fallback runs.
func fileResolver(t *testing.T, spy *spyRegistrar) (*Resolver, string) {
	t.Helper()
	home := t.TempDir()
	stateDir := t.TempDir()
	r := NewResolver(spy, envOf(nil),
		WithGetenv(func(k string) string {
			switch k {
			case "FRIDAY_STATE_DIR":
				return stateDir
			case "HOME":
				return home
			}
			return ""
		}),
		WithKeyringLookup(func(context.Context, string, string) (string, bool, error) {
			return "", false, ErrKeyringUnavailable
		}),
		WithExec(func(_ context.Context, _ []string, _ string) ([]byte, error) {
			return nil, errors.New("no exec in tests")
		}),
	)
	return r, home
}

func TestClaudeCodeFileFallback(t *testing.T) {
	spy := &spyRegistrar{}
	r, home := fileResolver(t, spy)
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".credentials.json")
	if err := os.WriteFile(path, []byte(claudeJSON(t, "spy-cc-file-1")), 0o600); err != nil {
		t.Fatal(err)
	}
	cred, err := r.ForProvider(context.Background(), anthropicEntry(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cred.Zero()
	if got := string(cred.Value()); got != "spy-cc-file-1" {
		t.Errorf("credential = %q", got)
	}
	if !spy.saw("spy-cc-file-1") {
		t.Error("file token not registered with redact")
	}

	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = r.ForProvider(context.Background(), anthropicEntry(t), nil)
	if err == nil || !strings.Contains(err.Error(), "malformed JSON") || !strings.Contains(err.Error(), path) {
		t.Errorf("malformed file error = %v, want malformed JSON + path", err)
	}
}

func TestClaudeCodeMissListsEveryLocation(t *testing.T) {
	r, _ := fileResolver(t, &spyRegistrar{})
	_, err := r.ForProvider(context.Background(), anthropicEntry(t), nil)
	var miss *ErrNoCredential
	if !errors.As(err, &miss) {
		t.Fatalf("err = %v, want ErrNoCredential", err)
	}
	msg := err.Error()
	for _, want := range []string{
		"ANTHROPIC_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN",
		oauthStorePrefix + "anthropic-oauth",
		claudeKeychainService,
		".claude/.credentials.json",
		"friday auth login anthropic-oauth",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("miss %q lacks %q", msg, want)
		}
	}
}

func TestExternalCLIUnshippedKindNotImplemented(t *testing.T) {
	entry, ok := providers.Lookup("copilot-acp")
	if !ok {
		t.Fatal("copilot-acp missing from registry")
	}
	_, err := testResolver(t, &spyRegistrar{}, nil).ForProvider(context.Background(), entry, nil)
	var nerr core.NotImplementedError
	if !errors.As(err, &nerr) {
		t.Fatalf("copilot-acp err = %v, want NotImplemented", err)
	}
}

func TestCopilotGhCLIFallback(t *testing.T) {
	spy := &spyRegistrar{}
	var calls atomic.Int64
	gh := "gho_from_gh_cli_1234567890" //nolint:gosec // planted test value
	srv := exchangeSrv(t, &calls, gh, "spy-gh-bearer-1", 0)
	defer srv.Close()
	var sawArgv []string
	r := testResolver(t, spy, nil, WithExec(func(_ context.Context, argv []string, _ string) ([]byte, error) {
		sawArgv = argv
		return []byte(gh + "\n"), nil
	}))
	cfg := &config.ProviderConfig{OAuth: &config.OAuthRef{ExchangeURL: srv.URL}}
	cred, err := r.ForProvider(context.Background(), copilotEntry(t), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cred.Zero()
	if got := string(cred.Value()); got != "spy-gh-bearer-1" {
		t.Errorf("credential = %q", got)
	}
	if strings.Join(sawArgv, " ") != "gh auth token" {
		t.Errorf("exec argv = %v", sawArgv)
	}
	if !spy.saw(gh) {
		t.Error("gh token not registered with redact (trimmed)")
	}
}

func TestCopilotGhCLIMissingIsCleanMiss(t *testing.T) {
	r := testResolver(t, &spyRegistrar{}, nil, WithExec(func(context.Context, []string, string) ([]byte, error) {
		return nil, errors.New(`exec: "gh": executable file not found in $PATH`)
	}))
	_, err := r.ForProvider(context.Background(), copilotEntry(t), nil)
	var miss *ErrNoCredential
	if !errors.As(err, &miss) {
		t.Fatalf("err = %v, want ErrNoCredential", err)
	}
	if !strings.Contains(err.Error(), "gh auth token") {
		t.Errorf("miss %q does not mention `gh auth token`", err)
	}
}
