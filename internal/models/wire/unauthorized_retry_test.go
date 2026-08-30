package wire_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/ataidesorg/ink/internal/auth"
	"github.com/ataidesorg/ink/internal/core"
	"github.com/ataidesorg/ink/internal/models/wire"
)

// serve401Then200 answers 401 for the first n requests, then a valid
// completion, recording every Authorization header it sees.
func serve401Then200(fail401 int64, seen *[]string, hits *atomic.Int64) *httptest.Server {
	var mu = make(chan struct{}, 1)
	mu <- struct{}{}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-mu
		*seen = append(*seen, r.Header.Get("Authorization"))
		mu <- struct{}{}
		if hits.Add(1) <= fail401 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
}

// rotatingCred hands out tok-1, tok-2, ... per resolution.
func rotatingCred(reg auth.Registrar, n *atomic.Int64) func(context.Context) (*auth.Credential, error) {
	return func(context.Context) (*auth.Credential, error) {
		return auth.NewCredential(reg, fmt.Sprintf("tok-%d", n.Add(1))), nil
	}
}

func TestUnauthorizedRetryOnce(t *testing.T) {
	var hits, resolves atomic.Int64
	var seen []string
	var invalidated, warned atomic.Int64
	srv := serve401Then200(1, &seen, &hits)
	defer srv.Close()

	p := newProvider(t, srv, func(o *wire.Options) {
		o.Credential = rotatingCred(&fakeRegistrar{}, &resolves)
		o.RetryUnauthorized = true
		o.InvalidateCredential = func() { invalidated.Add(1) }
		o.Warnf = func(string, ...any) { warned.Add(1) }
	})
	resp, err := p.Complete(context.Background(), core.CompletionRequest{
		Model:    "test-model",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err != nil || resp.Content != "ok" {
		t.Fatalf("complete after retry: %v %+v", err, resp)
	}
	if hits.Load() != 2 || invalidated.Load() != 1 || warned.Load() != 1 {
		t.Fatalf("hits=%d invalidated=%d warned=%d", hits.Load(), invalidated.Load(), warned.Load())
	}
	if len(seen) != 2 || seen[0] != "Bearer tok-1" || seen[1] != "Bearer tok-2" {
		t.Fatalf("auth headers = %v; the retry must re-resolve", seen)
	}
}

func TestUnauthorizedSecondFailureIsFinal(t *testing.T) {
	var hits, resolves atomic.Int64
	var seen []string
	srv := serve401Then200(99, &seen, &hits)
	defer srv.Close()

	p := newProvider(t, srv, func(o *wire.Options) {
		o.Credential = rotatingCred(&fakeRegistrar{}, &resolves)
		o.RetryUnauthorized = true
	})
	_, err := p.Complete(context.Background(), core.CompletionRequest{
		Model:    "test-model",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	var we *wire.Error
	if !errors.As(err, &we) || we.Status != http.StatusUnauthorized {
		t.Fatalf("want wire 401, got %v", err)
	}
	if hits.Load() != 2 {
		t.Fatalf("exactly one retry allowed, hits = %d", hits.Load())
	}
}

func TestUnauthorizedNoRetryByDefault(t *testing.T) {
	var hits atomic.Int64
	var seen []string
	srv := serve401Then200(99, &seen, &hits)
	defer srv.Close()

	p := newProvider(t, srv, nil)
	_, err := p.Complete(context.Background(), core.CompletionRequest{
		Model:    "test-model",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	var we *wire.Error
	if !errors.As(err, &we) || we.Status != http.StatusUnauthorized {
		t.Fatalf("want wire 401, got %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("opt-out must not retry, hits = %d", hits.Load())
	}
}
