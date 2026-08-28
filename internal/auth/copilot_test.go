package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ataidesorg/friday/internal/config"
	"github.com/ataidesorg/friday/internal/providers"
)

func copilotEntry(t *testing.T) providers.Entry {
	t.Helper()
	entry, ok := providers.Lookup("copilot")
	if !ok {
		t.Fatal("registry must carry copilot")
	}
	return entry
}

// exchangeSrv fakes the Copilot bearer mint: asserts the GitHub token
// header, returns a bearer with the given expiry.
func exchangeSrv(t *testing.T, calls *atomic.Int64, wantGH string, bearer string, expiresAt int64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if got := r.Header.Get("Authorization"); got != "token "+wantGH {
			t.Errorf("exchange auth header = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"token":%q,"expires_at":%d}`, bearer, expiresAt)
	}))
}

func TestCopilotTokenGate(t *testing.T) {
	if err := copilotTokenGate("ghp_classic123"); err == nil || !strings.Contains(err.Error(), "classic") {
		t.Fatalf("ghp_ must be rejected with an explanation, got %v", err)
	}
	for _, ok := range []string{"gho_abc", "ghu_abc", "github_pat_abc", "weird-unknown-shape"} {
		if err := copilotTokenGate(ok); err != nil {
			t.Fatalf("%s: %v", ok, err)
		}
	}
}

func TestCopilotExchangeCacheAndRemint(t *testing.T) {
	gh := "gho_spy_github_1234567890" //nolint:gosec // planted test value
	bearer := "spy-copilot-bearer-1"  //nolint:gosec // planted test value
	now := time.Now()
	clock := now
	var calls atomic.Int64
	srv := exchangeSrv(t, &calls, gh, bearer, now.Add(30*time.Minute).Unix())
	defer srv.Close()

	spy := &spyRegistrar{}
	r := testResolver(t, spy, map[string]string{"COPILOT_GITHUB_TOKEN": gh},
		WithHTTPClient(srv.Client()), WithNow(func() time.Time { return clock }))
	cfg := &config.ProviderConfig{OAuth: &config.OAuthRef{ExchangeURL: srv.URL}}

	cred, err := r.ForProvider(context.Background(), copilotEntry(t), cfg)
	if err != nil || cred.Value() != bearer {
		t.Fatalf("first resolve: %v", err)
	}
	if !spy.saw(gh) || !spy.saw(bearer) {
		t.Fatal("github token and bearer must both be registered with the redactor")
	}
	if _, err := r.ForProvider(context.Background(), copilotEntry(t), cfg); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("fresh bearer must come from cache, exchanges = %d", calls.Load())
	}

	clock = now.Add(time.Hour) // past expiry (30m) - skew
	if _, err := r.ForProvider(context.Background(), copilotEntry(t), cfg); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expired bearer must re-mint, exchanges = %d", calls.Load())
	}

	clock = now
	r.DropBearer("copilot")
	if _, err := r.ForProvider(context.Background(), copilotEntry(t), cfg); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("DropBearer must force a re-mint, exchanges = %d", calls.Load())
	}
}

func TestCopilotEnvChainOrder(t *testing.T) {
	var calls atomic.Int64
	srv := exchangeSrv(t, &calls, "gho_first_choice", "spy-bearer-chain", 0) //nolint:gosec // planted test values
	defer srv.Close()
	env := map[string]string{ //nolint:gosec // planted test values
		"COPILOT_GITHUB_TOKEN": "gho_first_choice",
		"GH_TOKEN":             "gho_second_choice",
		"GITHUB_TOKEN":         "gho_third_choice",
	}
	r := testResolver(t, &spyRegistrar{}, env, WithHTTPClient(srv.Client()))
	cfg := &config.ProviderConfig{OAuth: &config.OAuthRef{ExchangeURL: srv.URL}}
	if _, err := r.ForProvider(context.Background(), copilotEntry(t), cfg); err != nil {
		t.Fatal(err) // exchangeSrv asserts COPILOT_GITHUB_TOKEN won
	}
}

func TestCopilotStoreFallbackAndMemoryOnlyBearer(t *testing.T) {
	gh := "gho_spy_stored_1234567890" //nolint:gosec // planted test value
	bearer := "spy-copilot-bearer-2"  //nolint:gosec // planted test value
	var calls atomic.Int64
	srv := exchangeSrv(t, &calls, gh, bearer, 0)

	spy := &spyRegistrar{}
	stateDir := t.TempDir()
	mk := func() *Resolver {
		return NewResolver(spy, envOf(nil),
			WithGetenv(func(k string) string {
				if k == "FRIDAY_STATE_DIR" {
					return stateDir
				}
				return ""
			}),
			WithKeyringLookup(func(_ context.Context, _, _ string) (string, bool, error) {
				return "", false, ErrKeyringUnavailable
			}),
			WithHTTPClient(srv.Client()))
	}
	r := mk()
	raw, err := json.Marshal(tokenSet{AccessToken: gh}) //nolint:gosec // planted test value
	if err != nil {
		t.Fatal(err)
	}
	if err := r.StoreSet(oauthStorePrefix+"copilot", string(raw)); err != nil {
		t.Fatal(err)
	}
	cfg := &config.ProviderConfig{OAuth: &config.OAuthRef{ExchangeURL: srv.URL}}
	cred, err := r.ForProvider(context.Background(), copilotEntry(t), cfg)
	if err != nil || cred.Value() != bearer {
		t.Fatalf("store-fallback resolve: %v", err)
	}

	// The bearer must never be persisted: a fresh process with the same
	// state dir and a dead exchange endpoint cannot produce it.
	srv.Close()
	r2 := mk()
	if _, err := r2.ForProvider(context.Background(), copilotEntry(t), cfg); err == nil {
		t.Fatal("a fresh resolver with a dead exchange endpoint resolved a bearer; it must have been persisted somewhere")
	}
}

func TestCopilotRejectsForeignExchangeURL(t *testing.T) {
	r := testResolver(t, &spyRegistrar{}, map[string]string{"COPILOT_GITHUB_TOKEN": "gho_ok"})
	cfg := &config.ProviderConfig{OAuth: &config.OAuthRef{ExchangeURL: "https://evil.example/token"}}
	_, err := r.ForProvider(context.Background(), copilotEntry(t), cfg)
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("foreign exchange host must be rejected, got %v", err)
	}
}

func TestCopilotNoCredentialAndGhpRejected(t *testing.T) {
	cfg := &config.ProviderConfig{OAuth: &config.OAuthRef{ExchangeURL: "https://exchange.invalid/token"}}
	r := testResolver(t, &spyRegistrar{}, nil)
	_, err := r.ForProvider(context.Background(), copilotEntry(t), cfg)
	var missing *ErrNoCredential
	if !errors.As(err, &missing) {
		t.Fatalf("want ErrNoCredential, got %v", err)
	}
	for _, want := range []string{"friday auth login copilot", "COPILOT_GITHUB_TOKEN"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q must mention %q", err, want)
		}
	}

	r = testResolver(t, &spyRegistrar{}, map[string]string{"COPILOT_GITHUB_TOKEN": "ghp_classic_pat"}) //nolint:gosec // planted test value
	_, err = r.ForProvider(context.Background(), copilotEntry(t), cfg)
	if err == nil || !strings.Contains(err.Error(), "classic") {
		t.Fatalf("ghp_ must fail with the kind explanation, got %v", err)
	}
}
