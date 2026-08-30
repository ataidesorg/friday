package routing

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ataidesorg/ink/internal/config"
	"github.com/ataidesorg/ink/internal/core"
)

func usd(micros int64) *core.USDMicros {
	m := core.USDMicros(micros)
	return &m
}

func testProviders() map[string]core.ProviderDescriptor {
	return map[string]core.ProviderDescriptor{
		"cloudp": {ID: "cloudp", Kind: core.ProviderOpenAICompatible, Privacy: core.PrivacyPublicCloud,
			Capabilities: core.ProviderCapabilities{MaxContextTokens: 128000}},
		"localp": {ID: "localp", Kind: core.ProviderOpenAICompatible, Privacy: core.PrivacyLocal},
	}
}

func testRoutes() map[string]config.RouteConfig {
	return map[string]config.RouteConfig{
		"cloud": {Provider: "cloudp", Model: "gpt-test", Fallbacks: []string{"local"}},
		"local": {Provider: "localp", Model: "llama-test"},
	}
}

// testPrices: gpt-test $5/$15/$2.50 per MTok; llama-test free.
func testPrices() PriceTable {
	return PriceTable{
		"gpt-test":   {InputPerMTok: 5_000_000, OutputPerMTok: 15_000_000, CachedPerMTok: 2_500_000},
		"llama-test": {},
	}
}

func newTestRouter(t *testing.T) *Router {
	t.Helper()
	r, err := New(testRoutes(), testProviders(), testPrices(), map[string]int{"cloudp": 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

func openConstraints() core.RouteConstraints {
	return core.RouteConstraints{Privacy: core.PrivacyPublicCloud, AllowFallback: true}
}

func altReason(t *testing.T, d core.RouteDecision, route string) string {
	t.Helper()
	for _, a := range d.Alternatives {
		if a.Route == route {
			return a.Reason
		}
	}
	t.Fatalf("route %q missing from alternatives %v", route, d.Alternatives)
	return ""
}

func TestPrimaryRouteSelected(t *testing.T) {
	r := newTestRouter(t)
	d, err := r.Decide(context.Background(), "cloud", openConstraints(), core.Usage{InputTokens: 1000, OutputTokens: 100})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if d.Selected.Name != "cloud" || d.Selected.Provider != "cloudp" || d.Selected.Model != "gpt-test" {
		t.Fatalf("selected = %+v, want route cloud", d.Selected)
	}
	if d.KeyIndex != 0 {
		t.Errorf("KeyIndex = %d, want 0", d.KeyIndex)
	}
	if len(d.Fallback) != 1 || d.Fallback[0] != "local" {
		t.Errorf("Fallback = %v, want [local]", d.Fallback)
	}
	if d.Reason == "" {
		t.Error("decision carries no reason")
	}
	if got := altReason(t, d, "local"); got == "" {
		t.Error("alternative local carries no reason")
	}
	if d.EstimatedCost == nil || *d.EstimatedCost != 6_500 {
		t.Errorf("EstimatedCost = %v, want 6500 micros", d.EstimatedCost)
	}
	if !d.Constraints.AllowFallback || d.Constraints.Privacy != core.PrivacyPublicCloud {
		t.Errorf("Constraints not echoed: %+v", d.Constraints)
	}
}

func TestCostCapForcesLocalRoute(t *testing.T) {
	r := newTestRouter(t)
	c := openConstraints()
	c.MaxCost = usd(500_000) // $0.50; cloud estimate is $0.65
	d, err := r.Decide(context.Background(), "cloud", c, core.Usage{InputTokens: 100_000, OutputTokens: 10_000})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if d.Selected.Name != "local" {
		t.Fatalf("selected %q, want local", d.Selected.Name)
	}
	if got := altReason(t, d, "cloud"); !strings.Contains(got, "cost") {
		t.Errorf("cloud loss reason %q does not mention cost", got)
	}
	if !strings.Contains(d.Reason, "cloud") {
		t.Errorf("decision reason %q does not explain the fallback from cloud", d.Reason)
	}
	if d.EstimatedCost == nil || *d.EstimatedCost != 0 {
		t.Errorf("EstimatedCost = %v, want 0 for llama-test", d.EstimatedCost)
	}
}

func TestLocalPrivacyNeverFallsBackToPublicCloud(t *testing.T) {
	routes := map[string]config.RouteConfig{
		"local": {Provider: "localp", Model: "llama-test", Fallbacks: []string{"cloud"}},
		"cloud": {Provider: "cloudp", Model: "gpt-test"},
	}
	providers := testProviders()
	lp := providers["localp"]
	lp.Health = core.ProviderHealth{State: core.HealthUnhealthy, Reason: "probe failed"}
	providers["localp"] = lp
	r, err := New(routes, providers, testPrices(), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c := core.RouteConstraints{Privacy: core.PrivacyLocal, AllowFallback: true}
	_, err = r.Decide(context.Background(), "local", c, core.Usage{InputTokens: 10})
	if err == nil {
		t.Fatal("want error: the only fallback is public_cloud and the floor is local")
	}
	if !strings.Contains(err.Error(), "privacy") && !strings.Contains(err.Error(), "private") {
		t.Errorf("error %q does not carry the privacy reason", err)
	}
}

func TestRoutePrivacyFloorsChain(t *testing.T) {
	// The primary route itself declares privacy = local; even a permissive
	// caller constraint must not let its chain reach public_cloud.
	routes := map[string]config.RouteConfig{
		"local": {Provider: "localp", Model: "llama-test", Privacy: "local", Fallbacks: []string{"cloud"}},
		"cloud": {Provider: "cloudp", Model: "gpt-test"},
	}
	providers := testProviders()
	lp := providers["localp"]
	lp.Health = core.ProviderHealth{State: core.HealthUnhealthy, Reason: "down"}
	providers["localp"] = lp
	r, err := New(routes, providers, testPrices(), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c := core.RouteConstraints{Privacy: core.PrivacyPublicCloud, AllowFallback: true}
	if _, err := r.Decide(context.Background(), "local", c, core.Usage{InputTokens: 10}); err == nil {
		t.Fatal("want error: route-declared local privacy must floor the whole chain")
	}
}

func TestUnknownModelWithCapFailsClosed(t *testing.T) {
	prices := PriceTable{"llama-test": {}} // gpt-test missing
	r, err := New(testRoutes(), testProviders(), prices, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c := openConstraints()
	c.MaxCost = usd(1_000_000)
	d, err := r.Decide(context.Background(), "cloud", c, core.Usage{InputTokens: 10})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if d.Selected.Name != "local" {
		t.Fatalf("selected %q, want local: unknown cost with a cap set must fail closed", d.Selected.Name)
	}
	if got := altReason(t, d, "cloud"); !strings.Contains(got, "unknown") {
		t.Errorf("cloud loss reason %q does not mention the unknown cost", got)
	}
}

func TestUnknownModelWithoutCapIsEligible(t *testing.T) {
	prices := PriceTable{}
	r, err := New(testRoutes(), testProviders(), prices, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d, err := r.Decide(context.Background(), "cloud", openConstraints(), core.Usage{InputTokens: 10})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if d.Selected.Name != "cloud" {
		t.Fatalf("selected %q, want cloud", d.Selected.Name)
	}
	if d.EstimatedCost != nil {
		t.Errorf("EstimatedCost = %v, want nil for an unpriced model", d.EstimatedCost)
	}
}

func TestNoFallbackWhenDisallowed(t *testing.T) {
	r := newTestRouter(t)
	c := core.RouteConstraints{Privacy: core.PrivacyPublicCloud, AllowFallback: false, MaxCost: usd(500_000)}
	_, err := r.Decide(context.Background(), "cloud", c, core.Usage{InputTokens: 100_000, OutputTokens: 10_000})
	if err == nil {
		t.Fatal("want error: primary over the cap and fallback disallowed")
	}
	if !strings.Contains(err.Error(), "cost") {
		t.Errorf("error %q does not carry the cost reason", err)
	}
}

func TestDegradedProviderDemoted(t *testing.T) {
	providers := testProviders()
	cp := providers["cloudp"]
	cp.Health = core.ProviderHealth{State: core.HealthDegraded, Reason: "slow probes"}
	providers["cloudp"] = cp
	r, err := New(testRoutes(), providers, testPrices(), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d, err := r.Decide(context.Background(), "cloud", openConstraints(), core.Usage{InputTokens: 10})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if d.Selected.Name != "local" {
		t.Fatalf("selected %q, want local over the degraded cloud provider", d.Selected.Name)
	}
	if got := altReason(t, d, "cloud"); !strings.Contains(got, "degraded") {
		t.Errorf("cloud reason %q does not mention degradation", got)
	}
	if len(d.Fallback) != 1 || d.Fallback[0] != "cloud" {
		t.Errorf("Fallback = %v, want [cloud]: degraded routes stay reachable", d.Fallback)
	}
}

func TestContextWindowFilter(t *testing.T) {
	r := newTestRouter(t)
	d, err := r.Decide(context.Background(), "cloud", openConstraints(), core.Usage{InputTokens: 200_000})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if d.Selected.Name != "local" {
		t.Fatalf("selected %q, want local: 200k tokens exceed cloudp's 128k window", d.Selected.Name)
	}
	if got := altReason(t, d, "cloud"); !strings.Contains(got, "context") {
		t.Errorf("cloud reason %q does not mention the context window", got)
	}
}

func TestMaxLatencyFilter(t *testing.T) {
	routes := testRoutes()
	rt := routes["cloud"]
	rt.MaxLatencyMS = 500
	routes["cloud"] = rt
	r, err := New(routes, testProviders(), testPrices(), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c := openConstraints()
	c.MaxLatency = 200 * time.Millisecond
	d, err := r.Decide(context.Background(), "cloud", c, core.Usage{InputTokens: 10})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if d.Selected.Name != "local" {
		t.Fatalf("selected %q, want local: cloud declares 500ms against a 200ms constraint", d.Selected.Name)
	}
	if got := altReason(t, d, "cloud"); !strings.Contains(got, "latency") {
		t.Errorf("cloud reason %q does not mention latency", got)
	}
}

func TestFallbackCycleSafe(t *testing.T) {
	routes := map[string]config.RouteConfig{
		"a": {Provider: "cloudp", Model: "gpt-test", Fallbacks: []string{"b"}},
		"b": {Provider: "localp", Model: "llama-test", Fallbacks: []string{"a"}},
	}
	r, err := New(routes, testProviders(), testPrices(), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d, err := r.Decide(context.Background(), "a", openConstraints(), core.Usage{InputTokens: 10})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if d.Selected.Name != "a" || len(d.Fallback) != 1 || d.Fallback[0] != "b" {
		t.Fatalf("cycle not deduplicated: selected %q fallback %v", d.Selected.Name, d.Fallback)
	}
}

func TestDecideUnknownRoute(t *testing.T) {
	r := newTestRouter(t)
	_, err := r.Decide(context.Background(), "nope", openConstraints(), core.Usage{})
	if !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

func TestNewValidation(t *testing.T) {
	cases := []struct {
		name   string
		routes map[string]config.RouteConfig
	}{
		{"unknown provider", map[string]config.RouteConfig{"r": {Provider: "ghost", Model: "m"}}},
		{"unknown fallback", map[string]config.RouteConfig{"r": {Provider: "cloudp", Model: "m", Fallbacks: []string{"ghost"}}}},
		{"negative cost cap", map[string]config.RouteConfig{"r": {Provider: "cloudp", Model: "m", MaxCostUSD: -1}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.routes, testProviders(), testPrices(), nil); err == nil {
				t.Fatal("want construction error")
			}
		})
	}
}

func TestPriceCost(t *testing.T) {
	p := Price{InputPerMTok: 5_000_000, OutputPerMTok: 15_000_000, CachedPerMTok: 2_500_000}
	cases := []struct {
		name string
		u    core.Usage
		want core.USDMicros
	}{
		{"plain", core.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000}, 20_000_000},
		{"cached split", core.Usage{InputTokens: 1_000_000, CachedInputTokens: 400_000}, 4_000_000},
		{"cached beyond input clamps", core.Usage{InputTokens: 100, CachedInputTokens: 200}, 250},
		{"zero", core.Usage{}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := p.Cost(tc.u); got != tc.want {
				t.Fatalf("Cost(%+v) = %d, want %d", tc.u, got, tc.want)
			}
		})
	}
}
