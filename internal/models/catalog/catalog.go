// Package catalog fetches optional model manifests (`/v1/models` on
// OpenAI-compatible providers) for picker lists and caches them under the
// user cache directory. The catalog is never required: routing and
// runs never read it, and every failure degrades to the cached copy (with
// an age note) or an empty list — never an error that blocks anything else.
package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
)

// TTL is how long a cached manifest stays fresh.
const TTL = 24 * time.Hour

const (
	maxIDLen  = 128     // ids are untrusted data: length-capped before display
	maxModels = 4096    // and count-capped
	maxBody   = 4 << 20 // response body cap
)

// Options configures one catalog lookup.
type Options struct {
	// Provider is the canonical provider id; it keys the cache file.
	Provider string
	// BaseURL is the provider's OpenAI-compatible base (…/v1).
	BaseURL string
	// Bearer, when non-empty, rides the Authorization header. Held in
	// memory only; never written to the cache or any error.
	Bearer string
	// CacheDir is the catalog cache directory (see Dir).
	CacheDir string
	// Refresh bypasses a fresh cached copy.
	Refresh bool
	// Client defaults to a 10s-timeout client.
	Client *http.Client
	// Now defaults to time.Now (tests pin it).
	Now func() time.Time
}

// Result is what a lookup produced. It always comes back: degraded data
// carries a Note instead of an error.
type Result struct {
	Models    []string
	FetchedAt time.Time
	FromCache bool
	// Note explains degraded data (stale cache, failed fetch, failed cache
	// write). Empty on a clean fetch or a fresh cache hit.
	Note string
}

// Dir resolves the catalog cache directory: $XDG_CACHE_HOME/friday, else
// $HOME/.cache/friday.
func Dir(getenv func(string) string) (string, error) {
	if v := getenv("XDG_CACHE_HOME"); v != "" {
		return filepath.Join(v, "friday"), nil
	}
	if home := getenv("HOME"); home != "" {
		return filepath.Join(home, ".cache", "friday"), nil
	}
	return "", errors.New("cannot place the model catalog cache: neither XDG_CACHE_HOME nor HOME is set")
}

type cacheFile struct {
	Provider  string    `json:"provider"`
	FetchedAt time.Time `json:"fetched_at"`
	Models    []string  `json:"models"`
}

// Models returns the provider's advertised model ids, from the cache when
// fresh, fetching otherwise. It never returns an error: see Result.Note.
func Models(ctx context.Context, o Options) Result {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	client := o.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	path := cachePath(o.CacheDir, o.Provider)
	cached, cachedOK := readCache(path, o.Provider)
	if cachedOK && !o.Refresh && now().Sub(cached.FetchedAt) < TTL {
		return Result{Models: cached.Models, FetchedAt: cached.FetchedAt, FromCache: true}
	}
	models, err := fetch(ctx, client, o.BaseURL, o.Bearer)
	if err != nil {
		if cachedOK {
			return Result{
				Models: cached.Models, FetchedAt: cached.FetchedAt, FromCache: true,
				Note: fmt.Sprintf("stale: cached %s; refresh failed (%v)", cached.FetchedAt.UTC().Format(time.RFC3339), err),
			}
		}
		return Result{Note: fmt.Sprintf("no catalog: %v", err)}
	}
	res := Result{Models: models, FetchedAt: now().UTC()}
	if werr := writeCache(path, cacheFile{Provider: o.Provider, FetchedAt: res.FetchedAt, Models: models}); werr != nil {
		res.Note = fmt.Sprintf("fetched, but the cache write failed (%v)", werr)
	}
	return res
}

// fetch GETs <baseURL>/models and returns the validated, sorted id list.
// Errors carry the status code, never response content.
func fetch(ctx context.Context, client *http.Client, baseURL, bearer string) ([]string, error) {
	if baseURL == "" {
		return nil, errors.New("no base URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&parsed); err != nil {
		return nil, errors.New("malformed response") // never echo body content
	}
	seen := map[string]bool{}
	models := make([]string, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		if !validID(m.ID) || seen[m.ID] {
			continue // untrusted data: drop, never display
		}
		seen[m.ID] = true
		models = append(models, m.ID)
		if len(models) == maxModels {
			break
		}
	}
	sort.Strings(models)
	return models, nil
}

// validID accepts printable, length-capped ids (untrusted data rule).
func validID(id string) bool {
	if id == "" || len(id) > maxIDLen {
		return false
	}
	for _, r := range id {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

func cachePath(dir, provider string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			return r
		}
		return '_'
	}, provider)
	return filepath.Join(dir, "models-"+safe+".json")
}

// readCache loads and re-validates a cache file; anything corrupt or
// mismatched reads as no cache (the caller re-fetches).
func readCache(path, provider string) (cacheFile, bool) {
	raw, err := os.ReadFile(path) //nolint:gosec // path minted by cachePath under the cache dir
	if err != nil {
		return cacheFile{}, false
	}
	var cf cacheFile
	if err := json.Unmarshal(raw, &cf); err != nil {
		return cacheFile{}, false
	}
	if cf.Provider != provider || cf.FetchedAt.IsZero() {
		return cacheFile{}, false
	}
	kept := cf.Models[:0]
	for _, id := range cf.Models {
		if validID(id) {
			kept = append(kept, id)
		}
	}
	cf.Models = kept
	return cf, true
}

// writeCache writes atomically (tmp + rename), file 0600, dir 0700.
func writeCache(path string, cf cacheFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(cf)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
