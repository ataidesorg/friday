package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"context"
	"strings"

	"github.com/ataidesorg/ink/internal/config"
	"github.com/ataidesorg/ink/internal/providers"
)

// GitHub Copilot auth: a stored GitHub OAuth token (device flow or env) is
// exchanged at the Copilot token endpoint for a short-lived API bearer. The
// bearer lives in Resolver memory until its expiry and is never persisted.

// copilotTokenGate rejects GitHub token kinds the Copilot exchange cannot
// use. Classic PATs (ghp_) lack the Copilot grant; OAuth app tokens (gho_),
// user-to-server tokens (ghu_), and fine-grained PATs (github_pat_) pass.
func copilotTokenGate(token string) error {
	if strings.HasPrefix(token, "ghp_") {
		return fmt.Errorf("classic GitHub personal access token (ghp_) cannot mint a Copilot bearer; run `ink auth login copilot` or supply a gho_/ghu_/github_pat_ token")
	}
	return nil
}

// exchangeResponse is the Copilot token endpoint's answer: the bearer and
// its absolute unix expiry.
type exchangeResponse struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
}

// resolveExchanged is the ForProvider branch for exchanged-bearer providers
// (oauth2_device entries with a recorded exchange_url): find the vendor
// OAuth token, exchange it, cache the bearer in memory until expiry.
func (r *Resolver) resolveExchanged(ctx context.Context, entry providers.Entry, cfg *config.ProviderConfig) (*Credential, error) {
	r.bearerMu.Lock()
	cached, ok := r.bearers[entry.ID]
	r.bearerMu.Unlock()
	if ok && !cached.expired(r.now()) {
		return r.credential(cached.AccessToken), nil
	}

	ghToken, err := r.vendorToken(ctx, entry)
	if err != nil {
		return nil, err
	}
	if err := copilotTokenGate(ghToken); err != nil {
		return nil, err
	}
	ep := MergedOAuth(entry, cfg)
	bearer, err := r.exchangeBearer(ctx, ep.ExchangeURL, ghToken)
	if err != nil {
		return nil, fmt.Errorf("bearer exchange for %s: %w", entry.ID, err)
	}
	r.bearerMu.Lock()
	r.bearers[entry.ID] = bearer
	r.bearerMu.Unlock()
	return r.credential(bearer.AccessToken), nil
}

// DropBearer forgets the cached exchanged bearer so the next resolution
// mints a fresh one — the 401-retry path calls this when the upstream
// rejects a bearer our clock still considered fresh.
func (r *Resolver) DropBearer(id string) {
	r.bearerMu.Lock()
	delete(r.bearers, id)
	r.bearerMu.Unlock()
}

// vendorToken finds the GitHub OAuth token: the entry's env chain first
// (COPILOT_GITHUB_TOKEN → GH_TOKEN → GITHUB_TOKEN), then the stored device
// login, then whatever `gh auth token` already holds. Every hit is
// registered with the redactor before returning.
func (r *Resolver) vendorToken(ctx context.Context, entry providers.Entry) (string, error) {
	for _, name := range entry.Auth.EnvNames {
		if v, ok := r.environ(name); ok && v != "" {
			r.register.AddLiteral(v)
			return v, nil
		}
	}
	ts, found, err := r.loadTokenSet(entry.ID)
	if err != nil {
		return "", err
	}
	if found {
		return ts.AccessToken, nil
	}
	if v, ok := r.ghAuthToken(ctx); ok {
		r.register.AddLiteral(v)
		return v, nil
	}
	where := fmt.Sprintf("env %v, secret store %s%s, or `gh auth token`", entry.Auth.EnvNames, oauthStorePrefix, entry.ID)
	hint := "run `ink auth login " + entry.ID + "`"
	if len(entry.Auth.EnvNames) > 0 {
		hint += " or export " + entry.Auth.EnvNames[0]
	}
	return "", &ErrNoCredential{Source: "oauth", Where: where, Hint: hint}
}

// exchangeBearer trades the GitHub token for the short-lived Copilot API
// bearer. The bearer is registered with the redactor the moment it exists.
func (r *Resolver) exchangeBearer(ctx context.Context, exchangeURL, vendor string) (tokenSet, error) {
	if err := allowedExchangeURL(exchangeURL); err != nil {
		return tokenSet{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, exchangeURL, nil)
	if err != nil {
		return tokenSet{}, err
	}
	req.Header.Set("Authorization", "token "+vendor)
	req.Header.Set("Accept", "application/json")
	resp, err := r.http.Do(req)
	if err != nil {
		return tokenSet{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return tokenSet{}, err
	}
	if resp.StatusCode != http.StatusOK {
		// The body is withheld: error bodies may echo the token.
		return tokenSet{}, fmt.Errorf("exchange endpoint returned HTTP %d", resp.StatusCode)
	}
	var er exchangeResponse
	if err := json.Unmarshal(body, &er); err != nil || er.Token == "" {
		return tokenSet{}, fmt.Errorf("exchange endpoint returned HTTP %d without a bearer", resp.StatusCode)
	}
	r.register.AddLiteral(er.Token)
	ts := tokenSet{AccessToken: er.Token}
	if er.ExpiresAt > 0 {
		ts.ExpiresAt = er.ExpiresAt - int64(expirySkew.Seconds())
	}
	return ts, nil
}

// allowedExchangeURL rejects Copilot bearer-exchange URLs that are not
// GitHub's documented endpoint or a loopback address (tests). User-layer
// oauth.exchange_url must not become an open SSRF.
func allowedExchangeURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("invalid copilot exchange URL")
	}
	host := strings.ToLower(u.Hostname())
	switch host {
	case "api.github.com":
		if u.Scheme != "https" {
			return fmt.Errorf("copilot exchange URL must be https")
		}
		return nil
	case "127.0.0.1", "localhost", "::1":
		return nil
	default:
		return fmt.Errorf("copilot exchange URL host %q is not allowed", host)
	}
}
