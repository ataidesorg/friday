package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ataidesorg/ink/internal/config"
	"github.com/ataidesorg/ink/internal/core"
	"github.com/ataidesorg/ink/internal/providers"
)

// spyRegistrar records every literal handed to the redactor.
type spyRegistrar struct {
	mu   sync.Mutex
	seen []string
}

func (s *spyRegistrar) AddLiteral(literals ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen = append(s.seen, literals...)
}

func (s *spyRegistrar) saw(v string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range s.seen {
		if l == v {
			return true
		}
	}
	return false
}

func envOf(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func testResolver(t *testing.T, spy *spyRegistrar, env map[string]string, opts ...Option) *Resolver {
	t.Helper()
	stateDir := t.TempDir()
	base := []Option{
		WithGetenv(func(k string) string {
			if k == "INK_STATE_DIR" {
				return stateDir
			}
			return ""
		}),
		// Hermetic default: never touch the host OS keyring from tests.
		WithKeyringLookup(func(_ context.Context, _, _ string) (string, bool, error) {
			return "", false, ErrKeyringUnavailable
		}),
		// Hermetic default: never spawn real processes from tests.
		WithExec(func(_ context.Context, argv []string, _ string) ([]byte, error) {
			return nil, fmt.Errorf("test resolver: exec %v not stubbed", argv)
		}),
	}
	return NewResolver(spy, envOf(env), append(base, opts...)...)
}

func TestEnvSourceResolvesAndRegisters(t *testing.T) {
	secret := strings.Join([]string{"tok", "env", "secret", "1234567890"}, "-") // fragments so secret scanners see no literal
	spy := &spyRegistrar{}
	r := testResolver(t, spy, map[string]string{"FIREWORKS_API_KEY": secret})
	cred, err := r.Resolve(context.Background(), config.AuthRef{Source: "env", Name: "FIREWORKS_API_KEY"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cred.Value() != secret {
		t.Fatalf("value = %q, want the env secret", cred.Value())
	}
	if !spy.saw(secret) {
		t.Fatal("resolved literal was not registered with the redactor")
	}
}

func TestEnvSourceMissingIsTypedAndSilent(t *testing.T) {
	spy := &spyRegistrar{}
	r := testResolver(t, spy, nil)
	_, err := r.Resolve(context.Background(), config.AuthRef{Source: "env", Name: "NOPE_KEY"})
	var noCred *ErrNoCredential
	if !errors.As(err, &noCred) {
		t.Fatalf("want *ErrNoCredential, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "ink auth set") {
		t.Fatalf("error must name the remedy, got: %v", err)
	}
}

func TestUnknownSourceFails(t *testing.T) {
	r := testResolver(t, &spyRegistrar{}, nil)
	_, err := r.Resolve(context.Background(), config.AuthRef{Source: "carrier-pigeon"})
	if err == nil || !strings.Contains(err.Error(), "carrier-pigeon") {
		t.Fatalf("want unknown-source error, got: %v", err)
	}
}

func TestCredentialZero(t *testing.T) {
	c := newCredential([]byte("tok-zero-me-1234567890"))
	if c.Value() == "" {
		t.Fatal("value empty before Zero")
	}
	c.Zero()
	if c.Value() != "" {
		t.Fatalf("value survives Zero: %q", c.Value())
	}
	c.Zero() // double Zero must not panic
}

func TestCredentialConcurrent(t *testing.T) {
	c := newCredential([]byte("tok-race-1234567890"))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); _ = c.Value() }()
		go func() { defer wg.Done(); c.Zero() }()
	}
	wg.Wait()
	if c.Value() != "" {
		t.Fatal("credential survived every Zero")
	}
}

func TestCommandSourceTrimsStdout(t *testing.T) {
	secret := strings.Join([]string{"tok", "cmd", "secret", "1234567890"}, "-") // fragments so secret scanners see no literal
	spy := &spyRegistrar{}
	var gotArgv []string
	r := testResolver(t, spy, nil, WithExec(func(_ context.Context, argv []string, _ string) ([]byte, error) {
		gotArgv = argv
		return []byte("  " + secret + "\n"), nil
	}))
	cred, err := r.Resolve(context.Background(), config.AuthRef{Source: "command", Command: []string{"op", "read", "op://v/i"}})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cred.Value() != secret {
		t.Fatalf("value = %q, want trimmed stdout", cred.Value())
	}
	if len(gotArgv) != 3 || gotArgv[0] != "op" {
		t.Fatalf("argv = %v", gotArgv)
	}
	if !spy.saw(secret) {
		t.Fatal("command credential not registered with redactor")
	}
}

func TestCommandSourceFailureNeverLeaksOutput(t *testing.T) {
	secret := strings.Join([]string{"tok", "leaky", "stderr", "1234567890"}, "-") // fragments so secret scanners see no literal
	r := testResolver(t, &spyRegistrar{}, nil, WithExec(func(_ context.Context, _ []string, _ string) ([]byte, error) {
		return []byte(secret), fmt.Errorf("exit status 1: %s", secret)
	}))
	_, err := r.Resolve(context.Background(), config.AuthRef{Source: "command", Command: []string{"op"}})
	if err == nil {
		t.Fatal("want error on non-zero exit")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("command output leaked into error: %v", err)
	}
}

func TestCommandSourceEmptyArgv(t *testing.T) {
	r := testResolver(t, &spyRegistrar{}, nil)
	_, err := r.Resolve(context.Background(), config.AuthRef{Source: "command"})
	if err == nil {
		t.Fatal("want error for empty command argv")
	}
}

func TestKeyringHit(t *testing.T) {
	secret := "tok-keyring-1234567890"
	spy := &spyRegistrar{}
	r := testResolver(t, spy, nil, WithKeyringLookup(func(_ context.Context, service, account string) (string, bool, error) {
		if service == "ink" && account == "fireworks" {
			return secret, true, nil
		}
		return "", false, nil
	}))
	cred, err := r.Resolve(context.Background(), config.AuthRef{Source: "keyring", Service: "ink", Account: "fireworks"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cred.Value() != secret || !spy.saw(secret) {
		t.Fatal("keyring credential not resolved and registered")
	}
}

func TestKeyringUnavailableFallsBackToStore(t *testing.T) {
	secret := "tok-store-fallback-1234567890"
	spy := &spyRegistrar{}
	var warned []string
	r := testResolver(t, spy, nil,
		WithKeyringLookup(func(_ context.Context, _, _ string) (string, bool, error) {
			return "", false, ErrKeyringUnavailable
		}),
		WithWarnf(func(format string, args ...any) { warned = append(warned, fmt.Sprintf(format, args...)) }),
	)
	if err := r.StoreSet("fireworks", secret); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	cred, err := r.Resolve(context.Background(), config.AuthRef{Source: "keyring", Service: "ink", Account: "fireworks"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cred.Value() != secret {
		t.Fatalf("value = %q, want store fallback", cred.Value())
	}
	if len(warned) == 0 {
		t.Fatal("fallback must record a warning")
	}
	for _, w := range warned {
		if strings.Contains(w, secret) {
			t.Fatalf("warning leaked the credential: %q", w)
		}
	}
}

func TestSecretStoreRoundTripAndPermissions(t *testing.T) {
	secret := "tok-store-roundtrip-1234567890"
	spy := &spyRegistrar{}
	stateDir := t.TempDir()
	getenv := func(k string) string {
		if k == "INK_STATE_DIR" {
			return stateDir
		}
		return ""
	}
	r := NewResolver(spy, envOf(nil), WithGetenv(getenv),
		WithKeyringLookup(func(_ context.Context, _, _ string) (string, bool, error) {
			return "", false, ErrKeyringUnavailable
		}))
	if err := r.StoreSet("anthropic", secret); err != nil {
		t.Fatalf("set: %v", err)
	}
	cred, err := r.Resolve(context.Background(), config.AuthRef{Source: "secret_store", Name: "anthropic"})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if cred.Value() != secret {
		t.Fatalf("value = %q", cred.Value())
	}
	for _, name := range []string{"secrets.enc", "secrets.key"} {
		fi, err := os.Stat(filepath.Join(stateDir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Fatalf("%s mode = %o, want 0600", name, perm)
		}
	}
	raw, err := os.ReadFile(filepath.Join(stateDir, "secrets.enc")) //nolint:gosec // test path under t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatal("secret stored in plaintext")
	}
	if _, err := r.Resolve(context.Background(), config.AuthRef{Source: "secret_store", Name: "missing"}); err == nil {
		t.Fatal("missing name must fail")
	} else {
		var noCred *ErrNoCredential
		if !errors.As(err, &noCred) {
			t.Fatalf("want *ErrNoCredential, got: %v", err)
		}
	}
}

func TestSecretStoreCorruptFileFailsClosed(t *testing.T) {
	stateDir := t.TempDir()
	getenv := func(k string) string {
		if k == "INK_STATE_DIR" {
			return stateDir
		}
		return ""
	}
	if err := os.WriteFile(filepath.Join(stateDir, "secrets.enc"), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewResolver(&spyRegistrar{}, envOf(nil), WithGetenv(getenv),
		WithKeyringLookup(func(_ context.Context, _, _ string) (string, bool, error) {
			return "", false, ErrKeyringUnavailable
		}))
	if _, err := r.Resolve(context.Background(), config.AuthRef{Source: "secret_store", Name: "x"}); err == nil {
		t.Fatal("corrupt store must fail, not return empty")
	}
}

func TestForProviderEnvOrder(t *testing.T) {
	secret := "tok-gemini-second-1234567890" // planted test fixture; gitleaks:allow
	spy := &spyRegistrar{}
	entry, ok := providers.Lookup("gemini")
	if !ok {
		t.Fatal("registry lost gemini")
	}
	r := testResolver(t, spy, map[string]string{"GEMINI_API_KEY": secret})
	cred, err := r.ForProvider(context.Background(), entry, nil)
	if err != nil {
		t.Fatalf("ForProvider: %v", err)
	}
	if cred.Value() != secret {
		t.Fatalf("value = %q, want first set env (GEMINI_API_KEY)", cred.Value())
	}
}

func TestForProviderMissingNamesEnvs(t *testing.T) {
	entry, _ := providers.Lookup("fireworks")
	r := testResolver(t, &spyRegistrar{}, nil)
	_, err := r.ForProvider(context.Background(), entry, nil)
	var noCred *ErrNoCredential
	if !errors.As(err, &noCred) {
		t.Fatalf("want *ErrNoCredential, got: %v", err)
	}
	if !strings.Contains(err.Error(), "FIREWORKS_API_KEY") {
		t.Fatalf("error must name the env var, got: %v", err)
	}
}

func TestForProviderKeylessAndOptional(t *testing.T) {
	for _, id := range []string{"ollama", "lmstudio"} {
		entry, ok := providers.Lookup(id)
		if !ok {
			t.Fatalf("registry lost %s", id)
		}
		r := testResolver(t, &spyRegistrar{}, nil)
		cred, err := r.ForProvider(context.Background(), entry, nil)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if cred != nil {
			t.Fatalf("%s: want nil credential, got one", id)
		}
	}
}

func TestForProviderOAuthNotLoggedIn(t *testing.T) {
	entry, _ := providers.Lookup("codex") // oauth2_pkce, nothing stored
	r := testResolver(t, &spyRegistrar{}, nil)
	_, err := r.ForProvider(context.Background(), entry, nil)
	var missing *ErrNoCredential
	if !errors.As(err, &missing) {
		t.Fatalf("pkce with no stored token must be ErrNoCredential, got: %v", err)
	}
	if !strings.Contains(err.Error(), "ink auth login codex") {
		t.Fatalf("error must point at auth login, got: %v", err)
	}
}

func TestForProviderUnshippedKindsNotImplemented(t *testing.T) {
	entry, _ := providers.Lookup("copilot-acp")
	r := testResolver(t, &spyRegistrar{}, nil)
	_, err := r.ForProvider(context.Background(), entry, nil)
	if !errors.Is(err, core.ErrNotImplemented) {
		t.Fatalf("copilot-acp (%s) must be NotImplemented until its 2b task lands, got: %v", entry.Auth.Kind, err)
	}
}

// TestForProviderCloudKinds: cloud auth through the registry path.
// bedrock has no bearer to hand out (SigV4 signs requests instead), and
// vertex walks the GCP chain — with nothing configured that is a clean miss
// naming the chain, never a fake credential.
func TestForProviderCloudKinds(t *testing.T) {
	bedrock, _ := providers.Lookup("bedrock")
	_, err := testResolver(t, &spyRegistrar{}, nil).ForProvider(context.Background(), bedrock, nil)
	if err == nil || !strings.Contains(err.Error(), "SigV4 request signing") {
		t.Fatalf("bedrock err = %v, want the not-a-bearer explanation", err)
	}

	vertex, _ := providers.Lookup("vertex")
	r := testResolver(t, &spyRegistrar{}, nil, WithGetenv(func(k string) string {
		if k == "HOME" {
			return t.TempDir()
		}
		return ""
	}))
	_, err = r.ForProvider(context.Background(), vertex, nil)
	var miss *ErrNoCredential
	if !errors.As(err, &miss) || miss.Source != "gcp chain" {
		t.Fatalf("vertex err = %v, want the gcp chain miss", err)
	}

	unknown := bedrock
	unknown.Auth.Cloud = "digitalocean"
	_, err = testResolver(t, &spyRegistrar{}, nil).ForProvider(context.Background(), unknown, nil)
	if err == nil || !strings.Contains(err.Error(), "unknown cloud family") {
		t.Fatalf("unknown family err = %v, want fail closed", err)
	}
}

func TestForProviderOverrideWins(t *testing.T) {
	secret := "tok-override-1234567890"
	entry, _ := providers.Lookup("fireworks")
	r := testResolver(t, &spyRegistrar{}, map[string]string{"MY_KEY": secret})
	cred, err := r.ForProvider(context.Background(), entry, &config.ProviderConfig{Auth: &config.AuthRef{Source: "env", Name: "MY_KEY"}})
	if err != nil {
		t.Fatalf("override: %v", err)
	}
	if cred.Value() != secret {
		t.Fatalf("value = %q, want override env", cred.Value())
	}
}
