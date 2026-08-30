package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"unicode"

	"github.com/ataidesorg/ink/internal/config"
	"github.com/ataidesorg/ink/internal/providers"
)

// oauthStorePrefix namespaces token sets in the encrypted secret store so
// they never collide with plain API keys stored under the provider id.
const oauthStorePrefix = "oauth:"

// expirySkew is subtracted from expires_in so a token refreshes before the
// provider clock says it died mid-request.
const expirySkew = 60 * time.Second

// tokenSet is the persisted OAuth state for one provider. It lives only in
// the encrypted secret store and in memory; both tokens are registered with
// the redactor the moment they exist.
type tokenSet struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	ExpiresAt    int64  `json:"expires_at,omitempty"` // unix seconds; 0 = unknown, treated as fresh
}

func (t tokenSet) expired(now time.Time) bool {
	return t.ExpiresAt != 0 && now.Unix() >= t.ExpiresAt
}

// tokenResponse is the RFC 6749 token-endpoint answer, success or error.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

// MergedOAuth overlays the user-config override on the registry entry's
// OAuth block; non-empty override fields win.
func MergedOAuth(entry providers.Entry, cfg *config.ProviderConfig) providers.OAuth {
	ep := entry.OAuth
	if cfg == nil || cfg.OAuth == nil {
		return ep
	}
	o := cfg.OAuth
	if o.AuthURL != "" {
		ep.AuthURL = o.AuthURL
	}
	if o.TokenURL != "" {
		ep.TokenURL = o.TokenURL
	}
	if o.DeviceAuthURL != "" {
		ep.DeviceAuthURL = o.DeviceAuthURL
	}
	if o.ClientID != "" {
		ep.ClientID = o.ClientID
	}
	if len(o.Scopes) != 0 {
		ep.Scopes = o.Scopes
	}
	if o.RedirectPort != 0 {
		ep.RedirectPort = o.RedirectPort
	}
	if o.RedirectURI != "" {
		ep.RedirectURI = o.RedirectURI
	}
	if o.ExchangeURL != "" {
		ep.ExchangeURL = o.ExchangeURL
	}
	return ep
}

// requirePKCEEndpoints fails closed when the merged block is unusable for
// the authorization-code flow. Ink never guesses endpoints.
func requirePKCEEndpoints(id string, ep providers.OAuth) error {
	var missing []string
	if ep.AuthURL == "" {
		missing = append(missing, "auth_url")
	}
	if ep.TokenURL == "" {
		missing = append(missing, "token_url")
	}
	if ep.ClientID == "" {
		missing = append(missing, "client_id")
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("oauth endpoints unverified for %s (missing %s); set providers.%s.oauth.{auth_url,token_url,client_id} in your user config",
		id, strings.Join(missing, ", "), id)
}

// LoginOptions steer LoginPKCE's interaction with the human.
type LoginOptions struct {
	// NoBrowser prints the URL and reads the pasted redirect URL or code
	// from Stdin instead of opening a browser + loopback listener (SSH).
	NoBrowser bool
	Stdin     io.Reader
	// Out receives the URL and instructions (the CLI passes stderr).
	Out io.Writer
	// Timeout bounds the wait for the redirect; 0 means 5 minutes.
	Timeout time.Duration
}

// LoginPKCE runs the authorization-code + PKCE (S256) flow for id and
// stores the resulting token set in the encrypted secret store under
// "oauth:<id>". The access and refresh tokens are registered with the
// redactor before anything can print.
func (r *Resolver) LoginPKCE(ctx context.Context, id string, ep providers.OAuth, o LoginOptions) error {
	if err := requirePKCEEndpoints(id, ep); err != nil {
		return err
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	timeout := o.Timeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	verifier, challenge, err := pkcePair()
	if err != nil {
		return err
	}
	state, err := randomToken(16)
	if err != nil {
		return err
	}

	code, redirectURI, err := r.obtainCode(ctx, ep, challenge, state, o)
	if err != nil {
		return err
	}

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {ep.ClientID},
		"code_verifier": {verifier},
		"state":         {state},
	}
	tr, err := r.tokenRequest(ctx, ep.TokenURL, form)
	if err != nil {
		return fmt.Errorf("token exchange for %s: %w", id, err)
	}
	return r.saveTokenSet(id, tr)
}

// obtainCode gets the authorization code: loopback listener + browser by
// default, print-and-paste when NoBrowser is set (OAuth over SSH) or when
// the provider uses a vendor-hosted code page (RedirectURI).
func (r *Resolver) obtainCode(ctx context.Context, ep providers.OAuth, challenge, state string, o LoginOptions) (code, redirectURI string, err error) {
	if o.NoBrowser || ep.RedirectURI != "" {
		redirectURI = ep.RedirectURI
		if redirectURI == "" {
			// No listener exists in paste mode; the IdP shows the code on
			// its own page or the user pastes the full redirect URL.
			redirectURI = "http://127.0.0.1/callback"
		}
		authURL := authorizeURL(ep, redirectURI, challenge, state)
		fmt.Fprintf(o.Out, "Open this URL to sign in:\n\n  %s\n\nPaste the code (or the full redirect URL) here: ", authURL)
		code, err = readPastedCode(o.Stdin, state)
		return code, redirectURI, err
	}

	host := "127.0.0.1"
	addr := host + ":0"
	if ep.RedirectPort != 0 {
		addr = fmt.Sprintf("%s:%d", host, ep.RedirectPort)
	}
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return "", "", fmt.Errorf("loopback listener %s: %w (is another login running?)", addr, err)
	}
	defer func() { _ = l.Close() }()
	redirectURI = fmt.Sprintf("http://%s:%d/auth/callback", host, l.Addr().(*net.TCPAddr).Port)

	type result struct {
		code string
		err  error
	}
	got := make(chan result, 1)
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		if e := q.Get("error"); e != "" {
			http.Error(w, "login failed: "+e, http.StatusBadRequest)
			got <- result{err: fmt.Errorf("authorization failed: %s", e)}
			return
		}
		if q.Get("state") != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			got <- result{err: errors.New("authorization redirect state mismatch (possible CSRF); try again")}
			return
		}
		c := q.Get("code")
		if c == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			got <- result{err: errors.New("authorization redirect carried no code")}
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<html><body><p>Login complete. You can close this tab and return to the terminal.</p></body></html>")
		got <- result{code: c}
	})}
	go func() { _ = srv.Serve(l) }()
	defer func() { _ = srv.Close() }()

	authURL := authorizeURL(ep, redirectURI, challenge, state)
	fmt.Fprintf(o.Out, "Opening your browser to sign in. If nothing opens, use this URL:\n\n  %s\n\n", authURL)
	r.openBrowser(ctx, authURL)

	select {
	case res := <-got:
		return res.code, redirectURI, res.err
	case <-ctx.Done():
		return "", "", fmt.Errorf("timed out waiting for the login redirect: %w", ctx.Err())
	}
}

// authorizeURL builds the RFC 7636 authorization request URL.
func authorizeURL(ep providers.OAuth, redirectURI, challenge, state string) string {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {ep.ClientID},
		"redirect_uri":          {redirectURI},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	if len(ep.Scopes) != 0 {
		q.Set("scope", strings.Join(ep.Scopes, " "))
	}
	sep := "?"
	if strings.Contains(ep.AuthURL, "?") {
		sep = "&"
	}
	return ep.AuthURL + sep + q.Encode()
}

// readPastedCode accepts a bare code, a "code#state" pair (Anthropic's code
// page shape), or a full redirect URL. A URL that carries a state must match;
// a bare paste skips the check — the human relayed it by hand, there is no
// interceptable redirect.
func readPastedCode(stdin io.Reader, state string) (string, error) {
	if stdin == nil {
		return "", errors.New("no stdin to read the pasted code from")
	}
	var line string
	if _, err := fmt.Fscanln(stdin, &line); err != nil {
		return "", fmt.Errorf("read pasted code: %w", err)
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "", errors.New("empty code")
	}
	if strings.Contains(line, "://") {
		u, err := url.Parse(line)
		if err != nil {
			return "", fmt.Errorf("pasted value is not a URL or code: %w", err)
		}
		q := u.Query()
		if s := q.Get("state"); s != "" && s != state {
			return "", errors.New("pasted redirect state mismatch; paste the URL from this login attempt")
		}
		if c := q.Get("code"); c != "" {
			return c, nil
		}
		return "", errors.New("pasted URL carries no code parameter")
	}
	if code, st, found := strings.Cut(line, "#"); found {
		if st != "" && st != state {
			return "", errors.New("pasted redirect state mismatch; paste the value from this login attempt")
		}
		return code, nil
	}
	return line, nil
}

// openBrowser best-effort opens url in the default browser through the exec
// seam. Failure only warns: the URL is already printed.
func (r *Resolver) openBrowser(ctx context.Context, u string) {
	if !isHTTPURL(u) {
		r.warnf("refusing to open a non-http(s) sign-in URL; open the printed URL manually")
		return
	}
	bin := "xdg-open"
	if runtime.GOOS == "darwin" {
		bin = "open"
	}
	if _, err := exec.LookPath(bin); err != nil {
		r.warnf("no %s on PATH; open the printed URL manually", bin)
		return
	}
	if _, err := r.exec(ctx, []string{bin, u}, ""); err != nil {
		r.warnf("could not open the browser; open the printed URL manually")
	}
}

// isHTTPURL reports whether u parses as an http or https URL. A sign-in URL
// from a server response is untrusted data: a file:// or custom scheme must
// never reach the OS opener.
func isHTTPURL(u string) bool {
	parsed, err := url.Parse(u)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https")
}

// sanitizeLine strips control characters from an untrusted server string
// before it is printed to the terminal and caps its length, so a response
// field cannot repaint the screen with escape sequences.
func sanitizeLine(s string) string {
	const maxLen = 512
	var b strings.Builder
	for _, rn := range s {
		if rn == '\t' || unicode.IsPrint(rn) {
			b.WriteRune(rn)
		}
		if b.Len() >= maxLen {
			break
		}
	}
	return b.String()
}

// tokenRequest POSTs form to the token endpoint and decodes the answer.
// Error bodies surface only their RFC 6749 error code, never raw payloads.
func (r *Resolver) tokenRequest(ctx context.Context, tokenURL string, form url.Values) (tokenResponse, error) {
	tr, status, err := r.tokenRequestRaw(ctx, tokenURL, form)
	if err != nil {
		return tokenResponse{}, err
	}
	if tr.Error != "" {
		msg := sanitizeLine(tr.Error)
		if tr.ErrorDesc != "" {
			msg += ": " + sanitizeLine(tr.ErrorDesc)
		}
		return tokenResponse{}, errors.New(msg)
	}
	if status != http.StatusOK || tr.AccessToken == "" {
		return tokenResponse{}, fmt.Errorf("token endpoint returned HTTP %d without an access token", status)
	}
	return tr, nil
}

// tokenRequestRaw posts the form and parses the response without judging
// RFC error codes: device-flow polling needs to branch on tr.Error
// (authorization_pending, slow_down) instead of failing.
func (r *Resolver) tokenRequestRaw(ctx context.Context, tokenURL string, form url.Values) (tokenResponse, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := r.http.Do(req)
	if err != nil {
		return tokenResponse{}, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return tokenResponse{}, resp.StatusCode, err
	}
	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return tokenResponse{}, resp.StatusCode, fmt.Errorf("token endpoint returned HTTP %d with an unparseable body", resp.StatusCode)
	}
	return tr, resp.StatusCode, nil
}

// saveTokenSet registers the tokens with the redactor and persists the set.
func (r *Resolver) saveTokenSet(id string, tr tokenResponse) error {
	ts := tokenSet{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		TokenType:    tr.TokenType,
	}
	if tr.ExpiresIn > 0 {
		ts.ExpiresAt = r.now().Add(time.Duration(tr.ExpiresIn)*time.Second - expirySkew).Unix()
	}
	r.register.AddLiteral(ts.AccessToken)
	if ts.RefreshToken != "" {
		r.register.AddLiteral(ts.RefreshToken)
	}
	raw, err := json.Marshal(ts) //nolint:gosec // G117: the set is written to the encrypted store only
	if err != nil {
		return err
	}
	return r.StoreSet(oauthStorePrefix+id, string(raw))
}

// loadTokenSet reads the persisted set; found=false means never logged in.
func (r *Resolver) loadTokenSet(id string) (tokenSet, bool, error) {
	raw, found, err := r.storeGet(oauthStorePrefix + id)
	if err != nil || !found {
		return tokenSet{}, false, err
	}
	var ts tokenSet
	if err := json.Unmarshal([]byte(raw), &ts); err != nil {
		return tokenSet{}, false, fmt.Errorf("stored oauth token for %s is corrupt; run `ink auth login %s`", id, id)
	}
	// A fresh process has a fresh redactor: re-register on every load so a
	// stored token can never print unmasked.
	r.register.AddLiteral(ts.AccessToken)
	if ts.RefreshToken != "" {
		r.register.AddLiteral(ts.RefreshToken)
	}
	return ts, true, nil
}

// resolveOAuth2 is the ForProvider branch for oauth2_pkce providers: load
// the stored set, refresh when expired, return the access token.
func (r *Resolver) resolveOAuth2(ctx context.Context, entry providers.Entry, cfg *config.ProviderConfig) (*Credential, error) {
	ts, found, err := r.loadTokenSet(entry.ID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, &ErrNoCredential{Source: "oauth", Where: "secret store " + oauthStorePrefix + entry.ID,
			Hint: "run `ink auth login " + entry.ID + "`"}
	}
	if ts.expired(r.now()) {
		ts, err = r.refreshTokenSet(ctx, entry.ID, MergedOAuth(entry, cfg), ts)
		if err != nil {
			return nil, err
		}
	}
	return r.credential(ts.AccessToken), nil
}

// refreshTokenSet exchanges the refresh token and persists the new set.
func (r *Resolver) refreshTokenSet(ctx context.Context, id string, ep providers.OAuth, ts tokenSet) (tokenSet, error) {
	if ts.RefreshToken == "" {
		return tokenSet{}, fmt.Errorf("oauth token for %s expired and no refresh token is stored; run `ink auth login %s`", id, id)
	}
	if ep.TokenURL == "" || ep.ClientID == "" {
		return tokenSet{}, fmt.Errorf("oauth token for %s expired and the token endpoint is unverified; set providers.%s.oauth.{token_url,client_id} or run `ink auth login %s`", id, id, id)
	}
	r.register.AddLiteral(ts.RefreshToken)
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {ts.RefreshToken},
		"client_id":     {ep.ClientID},
	}
	tr, err := r.tokenRequest(ctx, ep.TokenURL, form)
	if err != nil {
		return tokenSet{}, fmt.Errorf("oauth refresh for %s: %w", id, err)
	}
	if tr.RefreshToken == "" {
		tr.RefreshToken = ts.RefreshToken // IdPs that do not rotate keep the old one
	}
	if err := r.saveTokenSet(id, tr); err != nil {
		return tokenSet{}, err
	}
	next, _, err := r.loadTokenSet(id)
	return next, err
}

// Logout deletes the stored token set for id.
func (r *Resolver) Logout(id string) (bool, error) {
	return r.StoreDelete(oauthStorePrefix + id)
}

// pkcePair mints the RFC 7636 verifier and its S256 challenge.
func pkcePair() (verifier, challenge string, err error) {
	verifier, err = randomToken(32)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

// randomToken returns n crypto/rand bytes as unpadded base64url.
func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("crypto/rand: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
