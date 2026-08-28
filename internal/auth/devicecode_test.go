package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ataidesorg/friday/internal/providers"
)

// fakeDeviceIDP scripts an RFC 8628 server: the device-auth grant, then one
// scripted verdict per poll ("pending", "slow_down", "denied", "expired",
// "ok").
type fakeDeviceIDP struct {
	mu        sync.Mutex
	script    []string
	polls     int
	deviceReq url.Values
	pollReqs  []url.Values
	access    string
	interval  int64
}

func (f *fakeDeviceIDP) handler(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/device", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		f.mu.Lock()
		f.deviceReq = r.PostForm
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"device_code":"dev-code-1","user_code":"WXYZ-1234","verification_uri":"https://idp.example/activate","expires_in":900,"interval":%d}`, f.interval)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		f.mu.Lock()
		f.pollReqs = append(f.pollReqs, r.PostForm)
		verdict := "ok"
		if f.polls < len(f.script) {
			verdict = f.script[f.polls]
		}
		f.polls++
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch verdict {
		case "ok":
			fmt.Fprintf(w, `{"access_token":%q,"token_type":"Bearer","expires_in":3600}`, f.access)
		case "denied":
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"access_denied"}`)
		case "expired":
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"expired_token"}`)
		default:
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"error":%q}`, map[string]string{"pending": "authorization_pending", "slow_down": "slow_down"}[verdict])
		}
	})
	return mux
}

func deviceEntry(base string) providers.Entry {
	return providers.Entry{
		ID:   "testdev",
		Auth: providers.Auth{Kind: providers.AuthOAuth2Device},
		OAuth: providers.OAuth{
			DeviceAuthURL: base + "/device",
			TokenURL:      base + "/token",
			ClientID:      "device-client-1",
			Scopes:        []string{"openid", "model.completion"},
		},
	}
}

// sleepSpy records every poll wait and returns instantly.
func sleepSpy(sleeps *[]time.Duration) Option {
	return WithSleep(func(ctx context.Context, d time.Duration) error {
		*sleeps = append(*sleeps, d)
		return ctx.Err()
	})
}

func TestLoginDeviceEndToEnd(t *testing.T) {
	idp := &fakeDeviceIDP{script: []string{"pending", "slow_down", "ok"}, access: "spy-device-access", interval: 1} //nolint:gosec // planted test value
	srv := httptest.NewServer(idp.handler(t))
	defer srv.Close()

	spy := &spyRegistrar{}
	var sleeps []time.Duration
	r := testResolver(t, spy, nil, WithHTTPClient(srv.Client()), sleepSpy(&sleeps))
	var out strings.Builder
	err := r.LoginDevice(context.Background(), "testdev", deviceEntry(srv.URL).OAuth, LoginOptions{NoBrowser: true, Out: &out})
	if err != nil {
		t.Fatalf("device login: %v", err)
	}

	if got := idp.deviceReq.Get("client_id"); got != "device-client-1" {
		t.Fatalf("device request client_id = %q", got)
	}
	if idp.deviceReq.Get("code_challenge") == "" || idp.deviceReq.Get("code_challenge_method") != "S256" {
		t.Fatalf("device request must carry PKCE params, got %v", idp.deviceReq)
	}
	if got := idp.deviceReq.Get("scope"); got != "openid model.completion" {
		t.Fatalf("scope = %q", got)
	}
	if len(idp.pollReqs) != 3 {
		t.Fatalf("polls = %d", len(idp.pollReqs))
	}
	p := idp.pollReqs[0]
	if p.Get("grant_type") != deviceGrantType || p.Get("device_code") != "dev-code-1" || p.Get("code_verifier") == "" {
		t.Fatalf("poll form = %v", p)
	}
	// interval 1s before each poll; slow_down adds five seconds.
	want := []time.Duration{time.Second, time.Second, 6 * time.Second}
	if len(sleeps) != 3 || sleeps[0] != want[0] || sleeps[1] != want[1] || sleeps[2] != want[2] {
		t.Fatalf("sleeps = %v, want %v", sleeps, want)
	}
	for _, s := range []string{"https://idp.example/activate", "WXYZ-1234"} {
		if !strings.Contains(out.String(), s) {
			t.Fatalf("output %q must show %q", out.String(), s)
		}
	}
	if strings.Contains(out.String(), idp.access) {
		t.Fatal("access token leaked into login output")
	}
	if !spy.saw(idp.access) {
		t.Fatal("access token must be registered with the redactor")
	}
	cred, err := r.ForProvider(context.Background(), deviceEntry(srv.URL), nil)
	if err != nil || cred.Value() != idp.access {
		t.Fatalf("resolve after device login: %v", err)
	}
}

func TestLoginDeviceDeniedAndExpired(t *testing.T) {
	for verdict, wantMsg := range map[string]string{"denied": "denied", "expired": "expired"} {
		idp := &fakeDeviceIDP{script: []string{verdict}, access: "spy-unreachable", interval: 1} //nolint:gosec // planted test value
		srv := httptest.NewServer(idp.handler(t))
		var sleeps []time.Duration
		r := testResolver(t, &spyRegistrar{}, nil, WithHTTPClient(srv.Client()), sleepSpy(&sleeps))
		err := r.LoginDevice(context.Background(), "testdev", deviceEntry(srv.URL).OAuth, LoginOptions{NoBrowser: true})
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), wantMsg) {
			t.Fatalf("%s: err = %v", verdict, err)
		}
	}
}

func TestLoginDeviceMissingEndpointsFailClosed(t *testing.T) {
	r := testResolver(t, &spyRegistrar{}, nil)
	err := r.LoginDevice(context.Background(), "nous", providers.OAuth{}, LoginOptions{NoBrowser: true})
	if err == nil {
		t.Fatal("missing endpoints must fail closed")
	}
	for _, want := range []string{"unverified", "providers.nous.oauth", "device_auth_url", "token_url", "client_id"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q must mention %q", err, want)
		}
	}
}

func TestSleepCtxCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepCtx(ctx, time.Hour); err == nil {
		t.Fatal("cancelled context must interrupt the sleep")
	}
	if err := sleepCtx(context.Background(), time.Millisecond); err != nil {
		t.Fatalf("normal sleep: %v", err)
	}
}
