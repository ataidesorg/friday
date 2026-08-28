package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ataidesorg/friday/internal/providers"
)

// RFC 8628 device authorization grant. The device-auth request and every
// poll also carry PKCE parameters: some IdPs require them (Qwen's device
// flow), the rest ignore unknown form fields (GitHub), and RFC 6749 §3.1
// permits extension parameters in both requests.

const deviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"

// deviceAuthResponse is RFC 8628 §3.2.
type deviceAuthResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int64  `json:"expires_in"`
	Interval                int64  `json:"interval"`
	Error                   string `json:"error"`
	ErrorDesc               string `json:"error_description"`
}

// requireDeviceEndpoints fails closed when the registry carries no recorded
// device endpoints for this provider; endpoints are never fabricated.
func requireDeviceEndpoints(id string, ep providers.OAuth) error {
	var missing []string
	for _, f := range []struct{ name, v string }{
		{"device_auth_url", ep.DeviceAuthURL},
		{"token_url", ep.TokenURL},
		{"client_id", ep.ClientID},
	} {
		if f.v == "" {
			missing = append(missing, f.name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("oauth endpoints unverified for %s (missing %s); set providers.%s.oauth.{device_auth_url,token_url,client_id} in your user config",
		id, strings.Join(missing, ", "), id)
}

// LoginDevice runs the RFC 8628 device-code flow for id: request the codes,
// show the human where to type the user code, poll the token endpoint until
// approval, and store the token set under "oauth:<id>".
func (r *Resolver) LoginDevice(ctx context.Context, id string, ep providers.OAuth, o LoginOptions) error {
	if err := requireDeviceEndpoints(id, ep); err != nil {
		return err
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	verifier, challenge, err := pkcePair()
	if err != nil {
		return err
	}
	da, err := r.deviceAuthRequest(ctx, ep, challenge)
	if err != nil {
		return fmt.Errorf("device authorization for %s: %w", id, err)
	}

	fmt.Fprintf(o.Out, "To sign in to %s, open\n\n  %s\n\nand enter code %s\n", id, sanitizeLine(da.VerificationURI), sanitizeLine(da.UserCode))
	if da.VerificationURIComplete != "" {
		fmt.Fprintf(o.Out, "(or open %s directly)\n", sanitizeLine(da.VerificationURIComplete))
		if !o.NoBrowser {
			r.openBrowser(ctx, da.VerificationURIComplete)
		}
	} else if !o.NoBrowser {
		r.openBrowser(ctx, da.VerificationURI)
	}
	fmt.Fprintln(o.Out, "waiting for approval...")

	timeout := o.Timeout
	if timeout == 0 {
		timeout = 15 * time.Minute
	}
	if da.ExpiresIn > 0 {
		if lifespan := time.Duration(da.ExpiresIn) * time.Second; lifespan < timeout {
			timeout = lifespan
		}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	tr, err := r.pollDeviceToken(ctx, ep, da, verifier)
	if err != nil {
		return fmt.Errorf("device login for %s: %w", id, err)
	}
	return r.saveTokenSet(id, tr)
}

// deviceAuthRequest posts RFC 8628 §3.1 and parses the code grant.
func (r *Resolver) deviceAuthRequest(ctx context.Context, ep providers.OAuth, challenge string) (deviceAuthResponse, error) {
	form := url.Values{
		"client_id":             {ep.ClientID},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	if len(ep.Scopes) > 0 {
		form.Set("scope", strings.Join(ep.Scopes, " "))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.DeviceAuthURL, strings.NewReader(form.Encode()))
	if err != nil {
		return deviceAuthResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := r.http.Do(req)
	if err != nil {
		return deviceAuthResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return deviceAuthResponse{}, err
	}
	var da deviceAuthResponse
	if err := json.Unmarshal(body, &da); err != nil {
		return deviceAuthResponse{}, fmt.Errorf("device endpoint returned HTTP %d with an unparseable body", resp.StatusCode)
	}
	if da.Error != "" {
		msg := sanitizeLine(da.Error)
		if da.ErrorDesc != "" {
			msg += ": " + sanitizeLine(da.ErrorDesc)
		}
		return deviceAuthResponse{}, errors.New(msg)
	}
	if resp.StatusCode != http.StatusOK || da.DeviceCode == "" || da.UserCode == "" || da.VerificationURI == "" {
		return deviceAuthResponse{}, fmt.Errorf("device endpoint returned HTTP %d without a device code grant", resp.StatusCode)
	}
	return da, nil
}

// pollDeviceToken polls per RFC 8628 §3.4-3.5: wait interval between polls,
// keep going on authorization_pending, add five seconds on slow_down, stop
// on access_denied / expired_token / context timeout.
func (r *Resolver) pollDeviceToken(ctx context.Context, ep providers.OAuth, da deviceAuthResponse, verifier string) (tokenResponse, error) {
	interval := time.Duration(da.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	form := url.Values{
		"grant_type":    {deviceGrantType},
		"device_code":   {da.DeviceCode},
		"client_id":     {ep.ClientID},
		"code_verifier": {verifier},
	}
	for {
		if err := r.sleep(ctx, interval); err != nil {
			return tokenResponse{}, fmt.Errorf("timed out waiting for approval: %w", err)
		}
		tr, status, err := r.tokenRequestRaw(ctx, ep.TokenURL, form)
		if err != nil {
			return tokenResponse{}, err
		}
		switch tr.Error {
		case "":
			if status != http.StatusOK || tr.AccessToken == "" {
				return tokenResponse{}, fmt.Errorf("token endpoint returned HTTP %d without an access token", status)
			}
			return tr, nil
		case "authorization_pending":
			continue
		case "slow_down":
			interval += 5 * time.Second
			continue
		case "access_denied":
			return tokenResponse{}, errors.New("the sign-in was denied")
		case "expired_token":
			return tokenResponse{}, errors.New("the device code expired before approval; run login again")
		default:
			msg := sanitizeLine(tr.Error)
			if tr.ErrorDesc != "" {
				msg += ": " + sanitizeLine(tr.ErrorDesc)
			}
			return tokenResponse{}, errors.New(msg)
		}
	}
}

// sleepCtx waits d or until ctx ends, whichever comes first.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
