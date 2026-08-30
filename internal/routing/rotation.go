package routing

import (
	"errors"
	"fmt"
	"sync"

	"github.com/ataidesorg/ink/internal/core"
)

// rotation tracks which configured key each provider is on. Only counts and
// indices live here — never key values. Advancing past the last key
// exhausts the provider and the router walks the fallback chain instead.
type rotation struct {
	mu   sync.Mutex
	keys map[string]int
	idx  map[string]int
}

func newRotation(keys map[string]int) *rotation {
	k := make(map[string]int, len(keys))
	for p, n := range keys {
		k[p] = n
	}
	return &rotation{keys: k, idx: map[string]int{}}
}

func (r *rotation) current(provider string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.idx[provider]
}

func (r *rotation) count(provider string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return max(r.keys[provider], 1)
}

// advance retires the provider's current key; false when none remain.
func (r *rotation) advance(provider string) (int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	next := r.idx[provider] + 1
	if next >= max(r.keys[provider], 1) {
		return r.idx[provider], false
	}
	r.idx[provider] = next
	return next, true
}

// statusCoder is implemented by wire.Error. Routing matches the method via
// errors.As instead of importing the wire package, keeping the dependency
// direction flat.
type statusCoder interface{ HTTPStatus() int }

// OnFailure reacts to a failed call on the decided route: HTTP 401/429
// rotates to the provider's next configured key; any other failure — or an
// exhausted key set — moves to the next fallback route. false means nothing
// is left to try. Rotation state is remembered, so rejected keys stay
// retired across later decisions.
func (r *Router) OnFailure(decision core.RouteDecision, err error) (core.RouteDecision, bool) {
	status := 0
	var sc statusCoder
	if errors.As(err, &sc) {
		status = sc.HTTPStatus()
	}
	provider := decision.Selected.Provider

	failWhy := "provider call failed"
	if status != 0 {
		failWhy = fmt.Sprintf("HTTP %d", status)
	}
	if status == 401 || status == 429 {
		if next, ok := r.rot.advance(provider); ok {
			d := decision
			d.KeyIndex = next
			d.Reason = fmt.Sprintf("retrying route %q with key %d of %d after HTTP %d", decision.Selected.Name, next+1, r.rot.count(provider), status)
			return d, true
		}
		failWhy = fmt.Sprintf("HTTP %d with all configured keys exhausted", status)
	}

	if len(decision.Fallback) == 0 {
		return core.RouteDecision{}, false
	}
	next := decision.Fallback[0]
	cfg, ok := r.routes[next]
	if !ok {
		return core.RouteDecision{}, false
	}
	return core.RouteDecision{
		Selected: r.modelRoute(next),
		Alternatives: append(append([]core.RankedAlternative{}, decision.Alternatives...),
			core.RankedAlternative{Route: decision.Selected.Name, Reason: failWhy}),
		Reason:      fmt.Sprintf("fell back from %q to %q: %s", decision.Selected.Name, next, failWhy),
		Constraints: decision.Constraints,
		Fallback:    append([]string(nil), decision.Fallback[1:]...),
		KeyIndex:    r.rot.current(cfg.Provider),
	}, true
}
