// Package routing turns a route name into an explainable core.RouteDecision:
// candidates are the route plus its fallback chain, filtered by privacy
// floor, provider health, context window, declared latency, and cost caps.
// Every loser keeps its reason. Key rotation and failure fallback live in
// rotation.go. The package never sees credential values, only key counts.
package routing

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/ataidesorg/ink/internal/config"
	"github.com/ataidesorg/ink/internal/core"
)

const microsPerMTok = 1_000_000

// Price is USD micros per million tokens for one model.
type Price struct {
	InputPerMTok  core.USDMicros
	OutputPerMTok core.USDMicros
	CachedPerMTok core.USDMicros
}

// Cost prices a usage estimate in integer micros end to end. Cached input
// tokens bill at the cached rate; counts are clamped to sane bounds first.
func (p Price) Cost(u core.Usage) core.USDMicros {
	cached := min(max(u.CachedInputTokens, 0), max(u.InputTokens, 0))
	fresh := max(u.InputTokens, 0) - cached
	out := max(u.OutputTokens, 0)
	per := func(tokens int64, rate core.USDMicros) int64 {
		if tokens <= 0 || rate <= 0 {
			return 0
		}
		if tokens > math.MaxInt64/int64(rate) {
			return math.MaxInt64 / 4 // saturate: three terms still cannot wrap
		}
		return tokens * int64(rate) / microsPerMTok
	}
	return core.USDMicros(per(fresh, p.InputPerMTok) + per(cached, p.CachedPerMTok) + per(out, p.OutputPerMTok))
}

// PriceTable maps a model name to its price. It is loaded from
// models.pricing; a model missing here has an unknown cost, which fails
// closed whenever a cost cap applies.
type PriceTable map[string]Price

// Router selects routes. It owns the maps it is constructed with; callers
// must not mutate them afterwards.
type Router struct {
	routes    map[string]config.RouteConfig
	providers map[string]core.ProviderDescriptor
	prices    PriceTable
	caps      map[string]core.USDMicros // per-route declared cost cap, converted once
	rot       *rotation
}

// New validates the route graph and builds a router. keys counts the
// configured credentials per provider (see config.ProviderConfig.AuthRefs);
// key values themselves never reach this package.
func New(routes map[string]config.RouteConfig, providers map[string]core.ProviderDescriptor, prices PriceTable, keys map[string]int) (*Router, error) {
	caps := make(map[string]core.USDMicros)
	for name, rt := range routes {
		if _, ok := providers[rt.Provider]; !ok {
			return nil, fmt.Errorf("routing: route %q references unknown provider %q: %w", name, rt.Provider, core.ErrInvalidInput)
		}
		for _, fb := range rt.Fallbacks {
			if _, ok := routes[fb]; !ok {
				return nil, fmt.Errorf("routing: route %q fallback %q does not exist: %w", name, fb, core.ErrInvalidInput)
			}
		}
		if rt.MaxCostUSD != 0 {
			m, err := core.USDFromFloat(rt.MaxCostUSD)
			if err != nil {
				return nil, fmt.Errorf("routing: route %q max_cost_usd: %w", name, err)
			}
			caps[name] = m
		}
	}
	return &Router{routes: routes, providers: providers, prices: prices, caps: caps, rot: newRotation(keys)}, nil
}

// candidate is one considered route with its computed estimate.
type candidate struct {
	name    string
	cfg     config.RouteConfig
	cost    *core.USDMicros
	demoted bool
}

// Decide picks a route for the request, or explains why none is eligible.
func (r *Router) Decide(_ context.Context, route string, c core.RouteConstraints, est core.Usage) (core.RouteDecision, error) {
	primary, ok := r.routes[route]
	if !ok {
		return core.RouteDecision{}, fmt.Errorf("routing: unknown route %q: %w", route, core.ErrInvalidInput)
	}
	floor := privacyFloor(c.Privacy, core.PrivacyClass(primary.Privacy))

	var eligible []candidate
	var losers []core.RankedAlternative
	for _, name := range r.expand(route, c.AllowFallback) {
		cfg := r.routes[name]
		reason, cost, ok := r.check(name, cfg, floor, c, est)
		if !ok {
			losers = append(losers, core.RankedAlternative{Route: name, Reason: reason})
			continue
		}
		demoted := r.providers[cfg.Provider].Health.State == core.HealthDegraded
		eligible = append(eligible, candidate{name: name, cfg: cfg, cost: cost, demoted: demoted})
	}
	if len(eligible) == 0 {
		parts := make([]string, len(losers))
		for i, l := range losers {
			parts[i] = l.Route + ": " + l.Reason
		}
		return core.RouteDecision{}, fmt.Errorf("routing: no eligible route for %q (%s)", route, strings.Join(parts, "; "))
	}
	sort.SliceStable(eligible, func(i, j int) bool { return !eligible[i].demoted && eligible[j].demoted })
	sel := eligible[0]

	alts := append([]core.RankedAlternative{}, losers...)
	fallback := make([]string, 0, len(eligible)-1)
	for _, e := range eligible[1:] {
		reason := fmt.Sprintf("eligible; ranked below %q in the fallback chain", sel.name)
		if e.demoted {
			reason = fmt.Sprintf("provider %q is degraded: %s", e.cfg.Provider, r.providers[e.cfg.Provider].Health.Reason)
		}
		alts = append(alts, core.RankedAlternative{Route: e.name, Reason: reason})
		fallback = append(fallback, e.name)
	}

	reason := fmt.Sprintf("route %q is the first eligible candidate", sel.name)
	if sel.name != route {
		why := fmt.Sprintf("route %q ranked below it", route)
		for _, l := range losers {
			if l.Route == route {
				why = l.Reason
			}
		}
		reason = fmt.Sprintf("fell back from %q to %q: %s", route, sel.name, why)
	}

	return core.RouteDecision{
		Selected:      r.modelRoute(sel.name),
		Alternatives:  alts,
		Reason:        reason,
		EstimatedCost: sel.cost,
		Constraints:   c,
		Fallback:      fallback,
		KeyIndex:      r.rot.current(sel.cfg.Provider),
	}, nil
}

// check applies the filters to one candidate; a false return carries the
// elimination reason. cost is set whenever the model is priced.
func (r *Router) check(name string, cfg config.RouteConfig, floor core.PrivacyClass, c core.RouteConstraints, est core.Usage) (string, *core.USDMicros, bool) {
	d := r.providers[cfg.Provider]
	if floor != "" && !floor.AllowsFallbackTo(d.Privacy) {
		return fmt.Sprintf("provider %q is %s; the privacy floor is %s", cfg.Provider, d.Privacy, floor), nil, false
	}
	if d.Health.State == core.HealthUnhealthy {
		return fmt.Sprintf("provider %q is unhealthy: %s", cfg.Provider, d.Health.Reason), nil, false
	}
	if mc := d.Capabilities.MaxContextTokens; mc > 0 && est.InputTokens > int64(mc) {
		return fmt.Sprintf("estimated %d input tokens exceed the %d-token context window", est.InputTokens, mc), nil, false
	}
	if c.MaxLatency > 0 && cfg.MaxLatencyMS > 0 && time.Duration(cfg.MaxLatencyMS)*time.Millisecond > c.MaxLatency {
		return fmt.Sprintf("declared latency %dms exceeds the %s constraint", cfg.MaxLatencyMS, c.MaxLatency), nil, false
	}
	limit := c.MaxCost
	if routeCap, ok := r.caps[name]; ok && (limit == nil || routeCap < *limit) {
		limit = &routeCap
	}
	price, priced := r.prices[cfg.Model]
	var cost *core.USDMicros
	if priced {
		v := price.Cost(est)
		cost = &v
	}
	if limit != nil {
		if !priced {
			return fmt.Sprintf("cost of model %q is unknown and a cost cap of %s applies", cfg.Model, *limit), nil, false
		}
		if *cost > *limit {
			return fmt.Sprintf("estimated cost %s exceeds the %s cap", *cost, *limit), cost, false
		}
	}
	return "", cost, true
}

// expand walks the fallback chain breadth-first, deduplicating cycles.
func (r *Router) expand(route string, allowFallback bool) []string {
	if !allowFallback {
		return []string{route}
	}
	seen := map[string]bool{route: true}
	chain := []string{route}
	for i := 0; i < len(chain); i++ {
		for _, fb := range r.routes[chain[i]].Fallbacks {
			if !seen[fb] {
				seen[fb] = true
				chain = append(chain, fb)
			}
		}
	}
	return chain
}

// privacyFloor returns the stricter of the caller constraint and the
// route-declared privacy; either may be empty.
func privacyFloor(constraint, declared core.PrivacyClass) core.PrivacyClass {
	if declared == "" {
		return constraint
	}
	if constraint == "" || !declared.AllowsFallbackTo(constraint) {
		return declared
	}
	return constraint
}

// modelRoute materializes the core view of a configured route.
func (r *Router) modelRoute(name string) core.ModelRoute {
	cfg := r.routes[name]
	mr := core.ModelRoute{
		Name:      name,
		Provider:  cfg.Provider,
		Model:     cfg.Model,
		Fallbacks: append([]string(nil), cfg.Fallbacks...),
		Constraints: core.RouteConstraints{
			MaxLatency:    time.Duration(cfg.MaxLatencyMS) * time.Millisecond,
			Privacy:       core.PrivacyClass(cfg.Privacy),
			AllowFallback: cfg.AllowFallback,
		},
	}
	if m, ok := r.caps[name]; ok {
		mr.Constraints.MaxCost = &m
	}
	return mr
}
