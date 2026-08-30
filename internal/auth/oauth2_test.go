package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ataidesorg/ink/internal/config"
	"github.com/ataidesorg/ink/internal/providers"
)

// fakeIDP is an httptest token endpoint that enforces PKCE: the exchange
// must present the verifier whose S256 hash the "browser" saw as the
// challenge.
type fakeIDP struct {
	mu           sync.Mutex
	challenge    string // captured from the authorize URL by the fake browser
	code         string
	accessToken  string
	refreshToken string
	expiresIn    int64
	refreshCalls int
	exchanges    int
}

func (f *fakeIDP) tokenHandler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("token endpoint: %v", err)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.PostForm.Get("grant_type") {
		case "authorization_code":
			f.exchanges++
			if got := r.PostForm.Get("code"); got != f.code {
				t.Errorf("exchange code = %q, want %q", got, f.code)
			}
			verifier := r.PostForm.Get("code_verifier")
			sum := sha256.Sum256([]byte(verifier))
			if got := base64.RawURLEncoding.EncodeToString(sum[:]); got != f.challenge {
				t.Errorf("S256(code_verifier) = %q, want challenge %q", got, f.challenge)
			}
			fmt.Fprintf(w, `{"access_token":%q,"refresh_token":%q,"expires_in":%d,"token_type":"Bearer"}`,
				f.accessToken, f.refreshToken, f.expiresIn)
		case "refresh_token":
			f.refreshCalls++
			if got := r.PostForm.Get("refresh_token"); got != f.refreshToken {
				t.Errorf("refresh_token = %q, want %q", got, f.refreshToken)
			}
			fmt.Fprintf(w, `{"access_token":"refreshed-access","expires_in":3600,"token_type":"Bearer"}`)
		default:
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"unsupported_grant_type"}`)
		}
	}
}

// browserExec fakes `open <url>`: it parses the authorize URL, records the
// challenge, and drives the loopback redirect like a signed-in human would.
func (f *fakeIDP) browserExec(t *testing.T) execFunc {
	return func(_ context.Context, argv []string, _ string) ([]byte, error) {
		if len(argv) != 2 {
			t.Errorf("browser exec argv = %v", argv)
			return nil, errors.New("bad argv")
		}
		u, err := url.Parse(argv[1])
		if err != nil {
			return nil, err
		}
		q := u.Query()
		if q.Get("code_challenge_method") != "S256" {
			t.Errorf("challenge method = %q, want S256", q.Get("code_challenge_method"))
		}
		f.mu.Lock()
		f.challenge = q.Get("code_challenge")
		f.mu.Unlock()
		redirect := q.Get("redirect_uri") + "?" + url.Values{"code": {f.code}, "state": {q.Get("state")}}.Encode()
		resp, err := http.Get(redirect) //nolint:noctx,gosec // test drives its own loopback redirect
		if err != nil {
			return nil, err
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("redirect handler returned HTTP %d", resp.StatusCode)
		}
		return nil, nil
	}
}

func oauthEntry(tokenURL string) providers.Entry {
	return providers.Entry{
		ID:   "testidp",
		Auth: providers.Auth{Kind: providers.AuthOAuth2PKCE},
		OAuth: providers.OAuth{
			AuthURL:  "https://idp.example/authorize",
			TokenURL: tokenURL,
			ClientID: "client-1",
			Scopes:   []string{"profile", "inference"},
		},
	}
}

func TestLoginPKCEEndToEnd(t *testing.T) {
	idp := &fakeIDP{code: "auth-code-1", accessToken: "spy-access-e2e", refreshToken: "spy-refresh-e2e", expiresIn: 3600} //nolint:gosec // planted test values
	srv := httptest.NewServer(idp.tokenHandler(t))
	defer srv.Close()

	spy := &spyRegistrar{}
	r := testResolver(t, spy, nil, WithExec(idp.browserExec(t)))
	entry := oauthEntry(srv.URL)

	var out strings.Builder
	err := r.LoginPKCE(context.Background(), entry.ID, entry.OAuth, LoginOptions{Out: &out})
	if err != nil {
		t.Fatalf("LoginPKCE: %v", err)
	}
	if idp.exchanges != 1 {
		t.Fatalf("exchanges = %d, want 1", idp.exchanges)
	}
	if !spy.saw("spy-access-e2e") || !spy.saw("spy-refresh-e2e") {
		t.Fatal("tokens must be redact-registered at save")
	}
	if strings.Contains(out.String(), "spy-access-e2e") || strings.Contains(out.String(), "spy-refresh-e2e") {
		t.Fatal("login output leaked a token")
	}

	cred, err := r.ForProvider(context.Background(), entry, nil)
	if err != nil {
		t.Fatalf("ForProvider after login: %v", err)
	}
	defer cred.Zero()
	if got := cred.Value(); got != "spy-access-e2e" {
		t.Fatalf("credential = %q, want the access token", got)
	}
	if idp.refreshCalls != 0 {
		t.Fatal("fresh token must not refresh")
	}
}

func TestLoginPKCEPasteMode(t *testing.T) {
	idp := &fakeIDP{code: "paste-code", accessToken: "spy-access-paste", expiresIn: 0}
	srv := httptest.NewServer(idp.tokenHandler(t))
	defer srv.Close()

	spy := &spyRegistrar{}
	execCalled := false
	r := testResolver(t, spy, nil, WithExec(func(context.Context, []string, string) ([]byte, error) {
		execCalled = true
		return nil, nil
	}))
	entry := oauthEntry(srv.URL)
	// Paste mode has no listener, so the IdP never sees the challenge; it
	// verifies against what the pasted flow computed. Capture it from the
	// printed URL like a human copying from the terminal.
	var out strings.Builder
	pr, pw := newPipe()
	done := make(chan error, 1)
	go func() {
		done <- r.LoginPKCE(context.Background(), entry.ID, entry.OAuth, LoginOptions{
			NoBrowser: true, Stdin: pr, Out: &syncWriter{b: &out, onURL: func(u string) {
				q, _ := url.Parse(u)
				idp.mu.Lock()
				idp.challenge = q.Query().Get("code_challenge")
				idp.mu.Unlock()
				pw.write("paste-code\n") // human pastes the bare code
			}},
		})
	}()
	if err := <-done; err != nil {
		t.Fatalf("LoginPKCE paste: %v", err)
	}
	if execCalled {
		t.Fatal("no-browser mode must not spawn anything")
	}
	cred, err := r.ForProvider(context.Background(), entry, nil)
	if err != nil {
		t.Fatalf("ForProvider: %v", err)
	}
	defer cred.Zero()
	if cred.Value() != "spy-access-paste" {
		t.Fatal("paste-mode token not stored")
	}
}

func TestOAuthRefreshOnExpiry(t *testing.T) {
	idp := &fakeIDP{refreshToken: "stored-refresh"}
	srv := httptest.NewServer(idp.tokenHandler(t))
	defer srv.Close()

	spy := &spyRegistrar{}
	now := time.Unix(1_700_000_000, 0)
	r := testResolver(t, spy, nil, WithNow(func() time.Time { return now }))
	entry := oauthEntry(srv.URL)

	seed, _ := json.Marshal(tokenSet{AccessToken: "stale-access", RefreshToken: "stored-refresh", ExpiresAt: now.Unix() - 10}) //nolint:gosec // planted test values
	if err := r.StoreSet(oauthStorePrefix+entry.ID, string(seed)); err != nil {
		t.Fatal(err)
	}

	cred, err := r.ForProvider(context.Background(), entry, nil)
	if err != nil {
		t.Fatalf("ForProvider with expired token: %v", err)
	}
	defer cred.Zero()
	if cred.Value() != "refreshed-access" {
		t.Fatalf("credential = %q, want refreshed-access", cred.Value())
	}
	if idp.refreshCalls != 1 {
		t.Fatalf("refreshCalls = %d, want 1", idp.refreshCalls)
	}
	ts, found, err := r.loadTokenSet(entry.ID)
	if err != nil || !found {
		t.Fatalf("token set after refresh: found=%v err=%v", found, err)
	}
	if ts.AccessToken != "refreshed-access" || ts.RefreshToken != "stored-refresh" {
		t.Fatalf("persisted set = %+v: new access must persist, unrotated refresh must survive", ts)
	}
}

func TestOAuthExpiredWithoutRefreshFailsClosed(t *testing.T) {
	r := testResolver(t, &spyRegistrar{}, nil, WithNow(func() time.Time { return time.Unix(2_000_000_000, 0) }))
	entry := oauthEntry("https://idp.example/token")
	seed, _ := json.Marshal(tokenSet{AccessToken: "stale", ExpiresAt: 1}) //nolint:gosec // planted test values
	if err := r.StoreSet(oauthStorePrefix+entry.ID, string(seed)); err != nil {
		t.Fatal(err)
	}
	_, err := r.ForProvider(context.Background(), entry, nil)
	if err == nil || !strings.Contains(err.Error(), "auth login") {
		t.Fatalf("want re-login error, got: %v", err)
	}
}

func TestLoginPKCEMissingEndpointsFailClosed(t *testing.T) {
	r := testResolver(t, &spyRegistrar{}, nil)
	err := r.LoginPKCE(context.Background(), "nous", providers.OAuth{}, LoginOptions{})
	if err == nil {
		t.Fatal("missing endpoints must fail")
	}
	for _, want := range []string{"unverified", "providers.nous.oauth", "auth_url", "token_url", "client_id"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q must mention %q", err, want)
		}
	}
}

func TestLogoutDeletesTokenSet(t *testing.T) {
	r := testResolver(t, &spyRegistrar{}, nil)
	entry := oauthEntry("https://idp.example/token")
	seed, _ := json.Marshal(tokenSet{AccessToken: "spy-logout"}) //nolint:gosec // planted test value
	if err := r.StoreSet(oauthStorePrefix+entry.ID, string(seed)); err != nil {
		t.Fatal(err)
	}
	found, err := r.Logout(entry.ID)
	if err != nil || !found {
		t.Fatalf("Logout: found=%v err=%v", found, err)
	}
	if _, err := r.ForProvider(context.Background(), entry, nil); err == nil {
		t.Fatal("credential must be gone after logout")
	}
	found, err = r.Logout(entry.ID)
	if err != nil || found {
		t.Fatalf("second Logout must be a clean miss: found=%v err=%v", found, err)
	}
}

func TestOAuthConfigOverrideWins(t *testing.T) {
	entry := oauthEntry("https://registry.example/token")
	merged := MergedOAuth(entry, &config.ProviderConfig{OAuth: &config.OAuthRef{ //nolint:gosec // test endpoint values, no credentials
		TokenURL: "https://override.example/token", ClientID: "override-client",
	}})
	if merged.TokenURL != "https://override.example/token" || merged.ClientID != "override-client" {
		t.Fatalf("override must win: %+v", merged)
	}
	if merged.AuthURL != entry.OAuth.AuthURL || len(merged.Scopes) != 2 {
		t.Fatalf("unset override fields must keep registry values: %+v", merged)
	}
}

func TestReadPastedCode(t *testing.T) {
	cases := []struct {
		in, want, wantErr string
	}{
		{in: "bare-code\n", want: "bare-code"},
		{in: "code-ok#st-1\n", want: "code-ok"},
		{in: "code-only#\n", want: "code-only"},
		{in: "code-part#state-part\n", wantErr: "state mismatch"},
		{in: "https://cb.example/done?code=url-code&state=st-1\n", want: "url-code"},
		{in: "https://cb.example/done?code=url-code&state=WRONG\n", wantErr: "state mismatch"},
		{in: "https://cb.example/done?state=st-1\n", wantErr: "no code"},
	}
	for _, tc := range cases {
		got, err := readPastedCode(strings.NewReader(tc.in), "st-1")
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("%q: want error %q, got %v", tc.in, tc.wantErr, err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("%q: got %q err %v, want %q", tc.in, got, err, tc.want)
		}
	}
}

// syncWriter forwards writes and fires onURL once with the first https URL
// seen — the paste-mode test's stand-in for a human reading the terminal.
type syncWriter struct {
	b     *strings.Builder
	once  sync.Once
	onURL func(string)
}

func (w *syncWriter) Write(p []byte) (int, error) {
	n, err := w.b.WriteString(string(p))
	if i := strings.Index(w.b.String(), "https://"); i >= 0 {
		s := w.b.String()[i:]
		if j := strings.IndexAny(s, "\n "); j > 0 {
			w.once.Do(func() { w.onURL(s[:j]) })
		}
	}
	return n, err
}

// pipe is a tiny blocking string pipe for driving stdin after the URL prints.
type pipe struct {
	ch chan byte
}

func newPipe() (*pipe, *pipe) {
	p := &pipe{ch: make(chan byte, 4096)}
	return p, p
}

func (p *pipe) write(s string) {
	for i := 0; i < len(s); i++ {
		p.ch <- s[i]
	}
}

func (p *pipe) Read(b []byte) (int, error) {
	c, ok := <-p.ch
	if !ok {
		return 0, errors.New("pipe closed")
	}
	b[0] = c
	return 1, nil
}

func TestIsHTTPURL(t *testing.T) {
	for _, tc := range []struct {
		u    string
		want bool
	}{
		{"https://example.com/auth", true},
		{"http://127.0.0.1:8080/cb", true},
		{"file:///etc/passwd", false},
		{"javascript:alert(1)", false},
		{"vscode://vendor/handler", false},
		{"", false},
	} {
		if got := isHTTPURL(tc.u); got != tc.want {
			t.Errorf("isHTTPURL(%q) = %v, want %v", tc.u, got, tc.want)
		}
	}
}

func TestSanitizeLine(t *testing.T) {
	// Control characters and newlines are stripped; printable runes stay.
	got := sanitizeLine("code\x1b[31m-\nABCD\r\t123")
	if strings.ContainsAny(got, "\x1b\n\r") {
		t.Fatalf("sanitizeLine kept control chars: %q", got)
	}
	if !strings.Contains(got, "code") || !strings.Contains(got, "ABCD") || !strings.Contains(got, "\t") {
		t.Fatalf("sanitizeLine dropped wanted content: %q", got)
	}
	// Length is capped.
	if n := len(sanitizeLine(strings.Repeat("x", 5000))); n > 512 {
		t.Fatalf("sanitizeLine length = %d, want <= 512", n)
	}
}

// TestOpenBrowserRefusesNonHTTP proves a non-http(s) sign-in URL from a
// server response never reaches the OS opener (F3).
func TestOpenBrowserRefusesNonHTTP(t *testing.T) {
	var called bool
	r := testResolver(t, &spyRegistrar{}, nil, WithExec(func(_ context.Context, _ []string, _ string) ([]byte, error) {
		called = true
		return nil, nil
	}))
	r.openBrowser(context.Background(), "file:///etc/passwd")
	if called {
		t.Fatal("openBrowser executed the opener for a file:// URL")
	}
}
