// Package wire implements HTTP adapters from Friday's provider contract
// (core.ModelProvider) to remote model APIs. Each wire maps
// core.CompletionRequest/Response to one HTTP dialect.
//
// Secrets: credentials are resolved per call via Options.Credential, used
// only for the request header, and never written into errors — an *Error
// carries the provider id, the HTTP status, and a short hint, never
// response-body content or header values. Zeroing the credential belongs
// to the caller that owns the provider's lifecycle.
package wire

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/ataidesorg/friday/internal/auth"
	"github.com/ataidesorg/friday/internal/core"
)

// StreamObserver receives text deltas as they arrive when streaming.
type StreamObserver func(delta string)

// Options configures one wire-backed provider instance.
type Options struct {
	// ID is the provider id used in descriptors and errors.
	ID string
	// BaseURL is the API root, e.g. "https://api.fireworks.ai/inference/v1".
	BaseURL string
	// Model is the provider-side model name sent on every request.
	Model string
	// Headers are extra request headers; they override defaults per key.
	Headers map[string]string
	// Privacy classifies where prompts travel for routing policy.
	Privacy core.PrivacyClass
	// Credential resolves the API credential at call time; nil means the
	// endpoint is keyless (local servers) and no Authorization header is sent.
	// A nil *auth.Credential result also means keyless.
	Credential func(ctx context.Context) (*auth.Credential, error)
	// HTTPClient overrides the default client (tests, custom transports).
	HTTPClient *http.Client
	// OnDelta, when set, makes Complete stream server-sent events and
	// forward text deltas while still returning the full response.
	OnDelta StreamObserver
	// MaxContextTokens is advertised in the descriptor; 0 means unknown.
	MaxContextTokens int
	// RetryUnauthorized, when true, retries exactly once on HTTP 401 after
	// invalidating and re-resolving the credential. Exchanged short-lived
	// bearers (Copilot) can be revoked server-side before their local
	// expiry; one re-mint recovers that without looping.
	RetryUnauthorized bool
	// Warnf logs the 401-retry warning; nil means silent. Never receives
	// credential values.
	Warnf func(format string, args ...any)
	// InvalidateCredential drops any cached credential so the retry's
	// re-resolution mints a fresh one; nil means nothing is cached.
	InvalidateCredential func()
	// Sign, when set, signs each attempt's request in place after all
	// headers are set (SigV4 request signing). Wires that use Sign do not
	// send a bearer; Credential stays nil. payload is the exact body bytes.
	Sign func(ctx context.Context, req *http.Request, payload []byte) error
}

// Error is a typed transport failure: which provider, which HTTP status.
// It never carries response-body content — bodies may echo credentials.
type Error struct {
	Provider string
	Status   int
	Hint     string
}

func (e *Error) Error() string {
	return fmt.Sprintf("provider %s: HTTP %d (%s)", e.Provider, e.Status, e.Hint)
}

// HTTPStatus reports the upstream status code. Routing matches this method
// via errors.As to detect auth and rate-limit failures without importing
// this package.
func (e *Error) HTTPStatus() int { return e.Status }

func hintFor(status int) string {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return "auth rejected; check the credential"
	case status == http.StatusTooManyRequests:
		return "rate limited"
	case status >= 500:
		return "server error"
	default:
		return "request rejected"
	}
}

// statusError builds the typed error for a non-2xx response.
func statusError(provider string, status int) *Error {
	return &Error{Provider: provider, Status: status, Hint: hintFor(status)}
}

func defaultHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			// No client-level Timeout: it would cut long streams mid-flight.
		},
	}
}

func retryableStatus(status int) bool {
	return status == http.StatusBadGateway ||
		status == http.StatusServiceUnavailable ||
		status == http.StatusGatewayTimeout
}

// doWithRetry sends the request built by build, retrying exactly once on a
// transient failure (network error or 502/503/504). build runs per attempt
// so each attempt gets a fresh body reader.
func doWithRetry(ctx context.Context, client *http.Client, build func(context.Context) (*http.Request, error)) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		req, err := build(ctx)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = err
			continue
		}
		if retryableStatus(resp.StatusCode) && attempt == 0 {
			drain(resp)
			lastErr = errors.New("transient upstream status")
			continue
		}
		return resp, nil
	}
	return nil, fmt.Errorf("request failed after retry: %w", lastErr)
}

func drain(resp *http.Response) {
	_, _ = io.CopyN(io.Discard, resp.Body, 4096)
	_ = resp.Body.Close()
}

// bodyReader returns a fresh reader over payload for each attempt.
func bodyReader(payload []byte) *bytes.Reader { return bytes.NewReader(payload) }

// postJSON posts payload to endpoint with one transient retry, converting
// non-2xx statuses into *Error (body discarded — it may echo credentials).
// setHeaders runs per attempt after Content-Type is set.
func postJSON(ctx context.Context, client *http.Client, providerID, endpoint string, payload []byte, setHeaders func(http.Header)) (*http.Response, error) {
	resp, err := doWithRetry(ctx, client, func(ctx context.Context) (*http.Request, error) {
		hr, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bodyReader(payload))
		if err != nil {
			return nil, err
		}
		hr.Header.Set("Content-Type", "application/json")
		setHeaders(hr.Header)
		return hr, nil
	})
	if err != nil {
		return nil, fmt.Errorf("provider %s: %w", providerID, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		drain(resp)
		return nil, statusError(providerID, resp.StatusCode)
	}
	return resp, nil
}

// readSSEEvents scans a server-sent-event body and calls on for every data
// line with the event name that preceded it ("" when the stream sends bare
// data lines). on returns stop=true to end cleanly; a stream that ends
// without a clean stop returns io.ErrUnexpectedEOF so truncation never
// passes for success.
func readSSEEvents(body io.Reader, on func(event, data string) (stop bool, err error)) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	event := ""
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case line == "":
			event = ""
			continue
		case strings.HasPrefix(line, ":"):
			continue
		}
		if name, ok := strings.CutPrefix(line, "event:"); ok {
			event = strings.TrimSpace(name)
			continue
		}
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue // id:, retry:, unknown fields: ignored per the SSE spec
		}
		stop, err := on(event, strings.TrimSpace(data))
		if err != nil {
			return err
		}
		if stop {
			return nil
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return io.ErrUnexpectedEOF
}

// validateOptions enforces the fields every wire needs.
func validateOptions(o Options) error {
	switch {
	case strings.TrimSpace(o.ID) == "":
		return fmt.Errorf("%w: wire provider needs an id", core.ErrInvalidInput)
	case strings.TrimSpace(o.BaseURL) == "":
		return fmt.Errorf("%w: provider %s needs a base URL", core.ErrInvalidInput, o.ID)
	case strings.TrimSpace(o.Model) == "":
		return fmt.Errorf("%w: provider %s needs a model", core.ErrInvalidInput, o.ID)
	}
	return nil
}

// clientFor returns the configured client or the shared default.
func clientFor(o Options) *http.Client {
	if o.HTTPClient != nil {
		return o.HTTPClient
	}
	return defaultHTTPClient()
}

// postAuthed resolves the credential and posts payload. When
// Options.RetryUnauthorized is set and the response is HTTP 401, it retries
// exactly once: warn, invalidate the cached credential, re-resolve, resend.
// A second 401 fails for good — no loops. setHeaders receives the resolved
// value per attempt ("" means keyless).
func postAuthed(ctx context.Context, o Options, client *http.Client, endpoint string, payload []byte, setHeaders func(h http.Header, authValue string)) (*http.Response, error) {
	authValue, err := credentialValue(ctx, o)
	if err != nil {
		return nil, err
	}
	resp, err := postJSON(ctx, client, o.ID, endpoint, payload, func(h http.Header) { setHeaders(h, authValue) })
	var we *Error
	if err == nil || !o.RetryUnauthorized || !errors.As(err, &we) || we.Status != http.StatusUnauthorized {
		return resp, err
	}
	if o.Warnf != nil {
		o.Warnf("provider %s rejected the credential (HTTP 401); re-resolving and retrying once", o.ID)
	}
	if o.InvalidateCredential != nil {
		o.InvalidateCredential()
	}
	authValue, err = credentialValue(ctx, o)
	if err != nil {
		return nil, err
	}
	return postJSON(ctx, client, o.ID, endpoint, payload, func(h http.Header) { setHeaders(h, authValue) })
}

// credentialValue resolves the credential for one call; "" means keyless.
// The value is already registered with the redactor by the auth package.
func credentialValue(ctx context.Context, o Options) (string, error) {
	if o.Credential == nil {
		return "", nil
	}
	cred, err := o.Credential(ctx)
	if err != nil {
		return "", fmt.Errorf("provider %s: %w", o.ID, err)
	}
	if cred == nil {
		return "", nil
	}
	return cred.Value(), nil
}
