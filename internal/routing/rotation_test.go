package routing

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ataidesorg/friday/internal/core"
)

// statusErr matches wire.Error's HTTPStatus method without importing the
// wire package; routing detects auth and rate-limit failures structurally.
type statusErr struct{ status int }

func (e *statusErr) Error() string   { return "upstream failure" }
func (e *statusErr) HTTPStatus() int { return e.status }

func decideCloud(t *testing.T, r *Router) core.RouteDecision {
	t.Helper()
	d, err := r.Decide(context.Background(), "cloud", openConstraints(), core.Usage{InputTokens: 10})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	return d
}

func TestRotationSecondKeyOn401And429(t *testing.T) {
	for _, status := range []int{401, 429} {
		r := newTestRouter(t) // cloudp has 2 keys, localp none
		d := decideCloud(t, r)
		if d.KeyIndex != 0 {
			t.Fatalf("status %d: initial KeyIndex = %d, want 0", status, d.KeyIndex)
		}

		d2, ok := r.OnFailure(d, &statusErr{status})
		if !ok || d2.Selected.Name != "cloud" || d2.KeyIndex != 1 {
			t.Fatalf("status %d: first failure → (%+v, %v), want cloud on key 1", status, d2.Selected, ok)
		}
		if !strings.Contains(d2.Reason, "key") {
			t.Errorf("status %d: reason %q does not explain the key rotation", status, d2.Reason)
		}

		d3, ok := r.OnFailure(d2, &statusErr{status})
		if !ok || d3.Selected.Name != "local" || d3.KeyIndex != 0 {
			t.Fatalf("status %d: keys exhausted → (%+v, %v), want fallback to local", status, d3.Selected, ok)
		}
		if got := altReason(t, d3, "cloud"); got == "" {
			t.Errorf("status %d: failed cloud route missing a reason in alternatives", status)
		}
		if d3.Reason == "" {
			t.Errorf("status %d: fallback decision carries no reason", status)
		}

		if d4, ok := r.OnFailure(d3, &statusErr{status}); ok {
			t.Fatalf("status %d: want exhaustion after the last route, got %+v", status, d4)
		}
	}
}

func TestNonAuthErrorSkipsRotation(t *testing.T) {
	for _, err := range []error{errors.New("connection refused"), &statusErr{503}} {
		r := newTestRouter(t)
		d := decideCloud(t, r)
		d2, ok := r.OnFailure(d, err)
		if !ok || d2.Selected.Name != "local" {
			t.Fatalf("err %v: want direct fallback to local, got (%+v, %v)", err, d2.Selected, ok)
		}
		if again := decideCloud(t, r); again.KeyIndex != 0 {
			t.Errorf("err %v: cloudp key advanced to %d on a non-auth failure", err, again.KeyIndex)
		}
	}
}

func TestRotationStickyAcrossDecides(t *testing.T) {
	r := newTestRouter(t)
	d := decideCloud(t, r)
	if _, ok := r.OnFailure(d, &statusErr{401}); !ok {
		t.Fatal("rotation to the second key must succeed")
	}
	if again := decideCloud(t, r); again.KeyIndex != 1 {
		t.Fatalf("KeyIndex = %d after rotation, want 1: rejected keys stay retired", again.KeyIndex)
	}
}

func TestOnFailureExhaustedWithoutFallback(t *testing.T) {
	r := newTestRouter(t)
	d, err := r.Decide(context.Background(), "local", openConstraints(), core.Usage{InputTokens: 10})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if _, ok := r.OnFailure(d, &statusErr{401}); ok {
		t.Fatal("local has no keys to rotate and no fallback: want ok = false")
	}
}
