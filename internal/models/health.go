// Package models holds provider-level plumbing that sits above the wire
// adapters: the health probe and its in-memory cache. Health is advisory
// metadata for routing and `friday providers`; it never gates a call and
// never carries a credential.
package models

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ataidesorg/friday/internal/auth"
	"github.com/ataidesorg/friday/internal/core"
	"github.com/ataidesorg/friday/internal/providers"
)

// ProbeTimeout bounds one health probe. Probes are advisory: nothing may
// hang a listing or a run on a slow endpoint.
const ProbeTimeout = 2 * time.Second

// anthropicVersion mirrors the wire adapter's pinned API version.
const anthropicVersion = "2023-06-01"

// Probe measures one provider endpoint with the cheapest request its wire
// supports: GET the model list for the OpenAI-shaped wires, HEAD /v1/messages
// for Anthropic (405 Method Not Allowed proves the endpoint is reachable).
// A nil cred probes keyless. The credential goes only into this request's
// auth header; the returned reason never contains it, and never contains a
// URL query string (a user-configured base URL may embed one).
func Probe(ctx context.Context, client *http.Client, wire, baseURL string, cred *auth.Credential, now time.Time) core.ProviderHealth {
	req, err := probeRequest(ctx, wire, baseURL, cred)
	if err != nil {
		return core.ProviderHealth{State: core.HealthUnknown, Reason: err.Error(), CheckedAt: now}
	}
	if client == nil {
		client = &http.Client{}
	}
	ctx, cancel := context.WithTimeout(req.Context(), ProbeTimeout)
	defer cancel()
	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		return core.ProviderHealth{State: core.HealthUnhealthy, Reason: "unreachable: " + sanitizeErr(err), CheckedAt: now}
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return core.ProviderHealth{State: core.HealthDegraded, Reason: fmt.Sprintf("reachable, credential rejected (HTTP %d)", resp.StatusCode), CheckedAt: now}
	case resp.StatusCode >= 200 && resp.StatusCode < 300,
		wire == providers.WireAnthropicMessages && resp.StatusCode == http.StatusMethodNotAllowed:
		return core.ProviderHealth{State: core.HealthHealthy, CheckedAt: now}
	default:
		return core.ProviderHealth{State: core.HealthUnhealthy, Reason: fmt.Sprintf("HTTP %d", resp.StatusCode), CheckedAt: now}
	}
}

func probeRequest(ctx context.Context, wire, baseURL string, cred *auth.Credential) (*http.Request, error) {
	if baseURL == "" {
		return nil, errors.New("no base URL")
	}
	base := strings.TrimRight(baseURL, "/")
	switch wire {
	case providers.WireChatCompletions, providers.WireResponses:
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, joinV1(base, "models"), nil)
		if err != nil {
			return nil, errors.New("invalid base URL")
		}
		if cred != nil {
			req.Header.Set("Authorization", "Bearer "+cred.Value())
		}
		return req, nil
	case providers.WireAnthropicMessages:
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, joinV1(base, "messages"), nil)
		if err != nil {
			return nil, errors.New("invalid base URL")
		}
		if cred != nil {
			req.Header.Set("x-api-key", cred.Value())
		}
		req.Header.Set("anthropic-version", anthropicVersion)
		return req, nil
	default:
		return nil, fmt.Errorf("wire %q has no health probe", wire)
	}
}

// joinV1 appends leaf under the versioned API root without doubling /v1:
// registry base URLs mostly end in /v1 already, Anthropic's does not.
func joinV1(base, leaf string) string {
	if strings.HasSuffix(base, "/v1") {
		return base + "/" + leaf
	}
	return base + "/v1/" + leaf
}

// sanitizeErr flattens a transport error to its cause, dropping the URL a
// *url.Error embeds. The row already names the provider, and a configured
// base URL may carry a query string that must never reach trails or output.
func sanitizeErr(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Sprintf("no response within %s", ProbeTimeout)
	}
	for {
		var ue *url.Error
		if !errors.As(err, &ue) {
			return err.Error()
		}
		err = ue.Err
	}
}

// Cache remembers the last probe per provider so health is measured lazily,
// served from memory, and re-measured only after Invalidate (a call failure)
// or a fresh Put overwrites it.
type Cache struct {
	mu sync.Mutex
	m  map[string]core.ProviderHealth
}

// NewCache returns an empty health cache.
func NewCache() *Cache { return &Cache{m: map[string]core.ProviderHealth{}} }

// Get returns the cached health for id, if any.
func (c *Cache) Get(id string) (core.ProviderHealth, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h, ok := c.m[id]
	return h, ok
}

// Put stores the latest measurement for id.
func (c *Cache) Put(id string, h core.ProviderHealth) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[id] = h
}

// Invalidate drops id's cached health so the next reader re-probes.
func (c *Cache) Invalidate(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, id)
}
