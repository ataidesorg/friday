package models_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ataidesorg/ink/internal/auth"
	"github.com/ataidesorg/ink/internal/core"
	"github.com/ataidesorg/ink/internal/models"
	"github.com/ataidesorg/ink/internal/providers"
)

type noopRegistrar struct{}

func (noopRegistrar) AddLiteral(...string) {}

func cred(t *testing.T, value string) *auth.Credential {
	t.Helper()
	return auth.NewCredential(noopRegistrar{}, value)
}

var probeTime = time.Unix(1756000000, 0).UTC()

func TestProbeHealthyChatCompletions(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := models.Probe(context.Background(), srv.Client(), providers.WireChatCompletions, srv.URL, cred(t, "sk-test-123"), probeTime)
	if h.State != core.HealthHealthy || h.Reason != "" {
		t.Fatalf("state %q reason %q, want healthy", h.State, h.Reason)
	}
	if gotPath != "/v1/models" || gotAuth != "Bearer sk-test-123" {
		t.Fatalf("request %q auth %q", gotPath, gotAuth)
	}
	if !h.CheckedAt.Equal(probeTime) {
		t.Fatalf("CheckedAt %v", h.CheckedAt)
	}
}

func TestProbeNoDoubledV1(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	models.Probe(context.Background(), srv.Client(), providers.WireResponses, srv.URL+"/v1", nil, probeTime)
	if gotPath != "/v1/models" {
		t.Fatalf("path %q, want /v1/models", gotPath)
	}
}

func TestProbeDegradedOnAuthReject(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	h := models.Probe(context.Background(), srv.Client(), providers.WireChatCompletions, srv.URL, cred(t, "bad-key"), probeTime)
	if h.State != core.HealthDegraded || !strings.Contains(h.Reason, "401") {
		t.Fatalf("state %q reason %q, want degraded 401", h.State, h.Reason)
	}
	if strings.Contains(h.Reason, "bad-key") {
		t.Fatalf("reason leaks credential: %q", h.Reason)
	}
}

func TestProbeUnhealthyOnServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	h := models.Probe(context.Background(), srv.Client(), providers.WireChatCompletions, srv.URL, nil, probeTime)
	if h.State != core.HealthUnhealthy || h.Reason != "HTTP 500" {
		t.Fatalf("state %q reason %q", h.State, h.Reason)
	}
}

func TestProbeUnreachableReasonSanitized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	srv.Close()

	base := srv.URL + "?api_key=super-secret"
	h := models.Probe(context.Background(), nil, providers.WireChatCompletions, base, cred(t, "super-secret"), probeTime)
	if h.State != core.HealthUnhealthy || !strings.HasPrefix(h.Reason, "unreachable: ") {
		t.Fatalf("state %q reason %q", h.State, h.Reason)
	}
	if strings.Contains(h.Reason, "super-secret") || strings.Contains(h.Reason, "?") {
		t.Fatalf("reason leaks URL or credential: %q", h.Reason)
	}
}

func TestProbeAnthropic405IsReachable(t *testing.T) {
	var method, path, key, version string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		key, version = r.Header.Get("x-api-key"), r.Header.Get("anthropic-version")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}))
	defer srv.Close()

	h := models.Probe(context.Background(), srv.Client(), providers.WireAnthropicMessages, srv.URL, cred(t, "sk-ant-1"), probeTime)
	if h.State != core.HealthHealthy {
		t.Fatalf("state %q reason %q, want healthy on 405", h.State, h.Reason)
	}
	if method != http.MethodHead || path != "/v1/messages" || key != "sk-ant-1" || version == "" {
		t.Fatalf("request %s %s key %q version %q", method, path, key, version)
	}
}

func TestProbeUnknownWireAndEmptyBase(t *testing.T) {
	if h := models.Probe(context.Background(), nil, providers.WireACP, "http://x", nil, probeTime); h.State != core.HealthUnknown {
		t.Fatalf("unknown wire: state %q", h.State)
	}
	if h := models.Probe(context.Background(), nil, providers.WireChatCompletions, "", nil, probeTime); h.State != core.HealthUnknown || h.Reason != "no base URL" {
		t.Fatalf("empty base: state %q reason %q", h.State, h.Reason)
	}
}

func TestCache(t *testing.T) {
	c := models.NewCache()
	if _, ok := c.Get("p"); ok {
		t.Fatal("empty cache returned a value")
	}
	c.Put("p", core.ProviderHealth{State: core.HealthHealthy, CheckedAt: probeTime})
	h, ok := c.Get("p")
	if !ok || h.State != core.HealthHealthy {
		t.Fatalf("get after put: %v %v", h, ok)
	}
	c.Invalidate("p")
	if _, ok := c.Get("p"); ok {
		t.Fatal("invalidate did not drop the entry")
	}
}
