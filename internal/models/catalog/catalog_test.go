package catalog

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fixedNow(t time.Time) func() time.Time { return func() time.Time { return t } }

// modelsSrv serves /models with the given ids and counts hits.
func modelsSrv(t *testing.T, hits *atomic.Int64, auth *string, ids ...string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if auth != nil {
			*auth = r.Header.Get("Authorization")
		}
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		type m struct {
			ID string `json:"id"`
		}
		data := make([]m, 0, len(ids))
		for _, id := range ids {
			data = append(data, m{ID: id})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
}

func TestFetchCachesAndRespectsTTL(t *testing.T) {
	var hits atomic.Int64
	srv := modelsSrv(t, &hits, nil, "beta", "alpha")
	defer srv.Close()
	dir := t.TempDir()
	t0 := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	opts := Options{Provider: "prov", BaseURL: srv.URL + "/v1", CacheDir: dir, Now: fixedNow(t0)}

	res := Models(context.Background(), opts)
	if res.Note != "" || res.FromCache {
		t.Fatalf("first fetch = %+v", res)
	}
	if strings.Join(res.Models, ",") != "alpha,beta" {
		t.Fatalf("models = %v, want sorted alpha,beta", res.Models)
	}
	path := cachePath(dir, "prov")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("cache perm = %o, want 0600", perm)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("tmp file left behind")
	}

	// Within TTL: served from cache, no second request.
	opts.Now = fixedNow(t0.Add(23 * time.Hour))
	res = Models(context.Background(), opts)
	if !res.FromCache || res.Note != "" || len(res.Models) != 2 {
		t.Fatalf("within-TTL = %+v", res)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1", hits.Load())
	}

	// Past TTL: re-fetched.
	opts.Now = fixedNow(t0.Add(25 * time.Hour))
	if res = Models(context.Background(), opts); res.FromCache {
		t.Fatalf("past-TTL = %+v", res)
	}
	if hits.Load() != 2 {
		t.Fatalf("hits = %d, want 2", hits.Load())
	}
}

func TestRefreshBypassesFreshCache(t *testing.T) {
	var hits atomic.Int64
	srv := modelsSrv(t, &hits, nil, "m1")
	defer srv.Close()
	t0 := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	opts := Options{Provider: "prov", BaseURL: srv.URL + "/v1", CacheDir: t.TempDir(), Now: fixedNow(t0)}
	Models(context.Background(), opts)
	opts.Refresh = true
	opts.Now = fixedNow(t0.Add(time.Minute))
	if res := Models(context.Background(), opts); res.FromCache {
		t.Fatalf("refresh served cache: %+v", res)
	}
	if hits.Load() != 2 {
		t.Fatalf("hits = %d, want 2", hits.Load())
	}
}

func TestCorruptCacheRefetched(t *testing.T) {
	var hits atomic.Int64
	srv := modelsSrv(t, &hits, nil, "good")
	defer srv.Close()
	dir := t.TempDir()
	path := cachePath(dir, "prov")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := Models(context.Background(), Options{Provider: "prov", BaseURL: srv.URL + "/v1", CacheDir: dir})
	if res.FromCache || res.Note != "" || len(res.Models) != 1 {
		t.Fatalf("corrupt-cache result = %+v", res)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1", hits.Load())
	}
	// Mismatched provider id in an otherwise valid file is also no cache.
	raw, _ := json.Marshal(cacheFile{Provider: "other", FetchedAt: time.Now(), Models: []string{"x"}})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if res := Models(context.Background(), Options{Provider: "prov", BaseURL: srv.URL + "/v1", CacheDir: dir}); res.FromCache {
		t.Fatalf("mismatched provider served: %+v", res)
	}
}

func TestFailureDegradesToStaleThenEmpty(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`secret-error-body`))
	}))
	defer bad.Close()
	dir := t.TempDir()
	t0 := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	// No cache at all: empty list + note, never an error.
	res := Models(context.Background(), Options{Provider: "prov", BaseURL: bad.URL + "/v1", CacheDir: dir, Now: fixedNow(t0)})
	if len(res.Models) != 0 || res.Note == "" || !strings.Contains(res.Note, "status 500") {
		t.Fatalf("no-cache degrade = %+v", res)
	}
	if strings.Contains(res.Note, "secret-error-body") {
		t.Fatalf("note leaks response body: %q", res.Note)
	}

	// Stale cache present: served with an age note.
	path := cachePath(dir, "prov")
	stale := cacheFile{Provider: "prov", FetchedAt: t0.Add(-48 * time.Hour), Models: []string{"old-model"}}
	raw, _ := json.Marshal(stale)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	res = Models(context.Background(), Options{Provider: "prov", BaseURL: bad.URL + "/v1", CacheDir: dir, Now: fixedNow(t0)})
	if !res.FromCache || len(res.Models) != 1 || res.Models[0] != "old-model" {
		t.Fatalf("stale degrade = %+v", res)
	}
	if !strings.Contains(res.Note, "stale") || !strings.Contains(res.Note, "2026-08-22") {
		t.Fatalf("stale note = %q", res.Note)
	}

	// Unreachable endpoint degrades the same way.
	res = Models(context.Background(), Options{Provider: "prov", BaseURL: "http://127.0.0.1:0/v1", CacheDir: dir, Now: fixedNow(t0)})
	if !res.FromCache || res.Note == "" {
		t.Fatalf("unreachable degrade = %+v", res)
	}
}

func TestBearerRidesHeaderAndKeylessOmitsIt(t *testing.T) {
	var hits atomic.Int64
	var auth string
	srv := modelsSrv(t, &hits, &auth, "m")
	defer srv.Close()
	opts := Options{Provider: "prov", BaseURL: srv.URL + "/v1", CacheDir: t.TempDir(), Bearer: "spy-catalog-token-1", Refresh: true} //nolint:gosec // planted test value
	Models(context.Background(), opts)
	if auth != "Bearer spy-catalog-token-1" {
		t.Fatalf("auth = %q", auth)
	}
	opts.Bearer = ""
	Models(context.Background(), opts)
	if auth != "" {
		t.Fatalf("keyless fetch sent auth %q", auth)
	}
}

func TestUntrustedIDsValidated(t *testing.T) {
	long := strings.Repeat("x", maxIDLen+1)
	srv := modelsSrv(t, &atomic.Int64{}, nil, "ok-model", "bad\x1b[31mansi", "", long, "ok-model")
	defer srv.Close()
	res := Models(context.Background(), Options{Provider: "prov", BaseURL: srv.URL + "/v1", CacheDir: t.TempDir()})
	if strings.Join(res.Models, ",") != "ok-model" {
		t.Fatalf("models = %q, want only ok-model (deduped, invalid dropped)", res.Models)
	}
}

func TestDir(t *testing.T) {
	env := map[string]string{"XDG_CACHE_HOME": "/x/cache", "HOME": "/home/u"}
	lookup := func(k string) string { return env[k] }
	if d, err := Dir(lookup); err != nil || d != filepath.Join("/x/cache", "friday") {
		t.Fatalf("Dir = %q, %v", d, err)
	}
	delete(env, "XDG_CACHE_HOME")
	if d, err := Dir(lookup); err != nil || d != filepath.Join("/home/u", ".cache", "friday") {
		t.Fatalf("Dir = %q, %v", d, err)
	}
	delete(env, "HOME")
	if _, err := Dir(lookup); err == nil {
		t.Fatal("want error with no HOME")
	}
}

func TestCachePathSanitizesProviderID(t *testing.T) {
	got := cachePath("/d", "we/../ird id")
	if filepath.Dir(got) != "/d" || strings.ContainsAny(filepath.Base(got), "/ ") {
		t.Fatalf("cachePath = %q", got)
	}
}
