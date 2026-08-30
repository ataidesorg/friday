// Package providers holds Ink's embedded provider registry: the Hermes
// Agent parity matrix as data. A provider is a registry entry,
// not code; the three wire adapters consume entries. The package sits below
// internal/config so validation can reject unknown provider kinds, and it
// never touches credentials — auth entries name sources, not values.
package providers

import (
	_ "embed"
	"fmt"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Wire protocols an entry may declare. All but acp have an adapter in
// internal/cli's adapterFor; acp constructs to NotImplemented.
const (
	WireChatCompletions   = "chat_completions"
	WireResponses         = "responses"
	WireAnthropicMessages = "anthropic_messages"
	WireACP               = "acp"
	WireVertex            = "vertex"
	WireBedrock           = "bedrock"
)

// Auth kinds an entry may declare. Only key and none resolve; the rest
// resolve to NotImplemented.
const (
	AuthKey          = "key"
	AuthOAuth2PKCE   = "oauth2_pkce"
	AuthOAuth2Device = "oauth2_device"
	AuthExternalCLI  = "external_cli"
	AuthCloud        = "cloud"
	AuthNone         = "none"
)

// Verification statuses. Every shipped entry starts unverified; only a
// completed real call flips it to verified, and a stale entry degrades to
// broken rather than to silent failure.
const (
	StatusUnverified = "unverified"
	StatusVerified   = "verified"
	StatusBroken     = "broken"
)

// Cloud credential families for kind "cloud" (and, for Azure, the Entra
// fallback behind kind "key"). The family selects which vendor chain the
// auth package walks; anything else is unknown and fails closed.
const (
	CloudAWS   = "aws"
	CloudGCP   = "gcp"
	CloudAzure = "azure"
)

// Auth says how an entry authenticates: the kind, the ordered env-var
// names (first set wins), whether the flow needs the owner-ruled opt-in
// risk flag, and — for cloud
// providers — which vendor credential family resolves it.
type Auth struct {
	Kind      string   `toml:"kind"`
	EnvNames  []string `toml:"env"`
	Optional  bool     `toml:"optional"`
	OptInRisk bool     `toml:"opt_in_risk"`
	Cloud     string   `toml:"cloud"`
}

// OAuth carries an entry's OAuth endpoints. Populated only when the values
// are recorded with a source; an empty field means "not
// verified by Ink" and the flow fails closed until user config supplies
// it (providers.<id>.oauth). RedirectURI, when set, is a vendor-hosted
// code-display page: login switches to paste mode instead of a loopback
// listener. RedirectPort pins the loopback port for IdPs that register an
// exact URI.
type OAuth struct {
	AuthURL       string   `toml:"auth_url"`
	TokenURL      string   `toml:"token_url"`
	DeviceAuthURL string   `toml:"device_auth_url"`
	ClientID      string   `toml:"client_id"`
	Scopes        []string `toml:"scopes"`
	RedirectPort  int      `toml:"redirect_port"`
	RedirectURI   string   `toml:"redirect_uri"`
	// ExchangeURL mints a short-lived API bearer from the stored OAuth
	// token before each call (GitHub Copilot); empty for direct-use tokens.
	ExchangeURL string `toml:"exchange_url"`
}

// Entry is one provider row. An empty BaseURL means the Hermes docs said
// "verify": the entry is unusable until config supplies the URL or a
// verification task records a vendor-confirmed one.
type Entry struct {
	ID           string            `toml:"id"`
	Aliases      []string          `toml:"aliases"`
	Wire         string            `toml:"wire"`
	BaseURL      string            `toml:"base_url"`
	BaseURLEnv   string            `toml:"base_url_env"`
	RegionalURLs map[string]string `toml:"regional_urls"`
	Privacy      string            `toml:"privacy"`
	Status       string            `toml:"status"`
	Auth         Auth              `toml:"auth"`
	OAuth        OAuth             `toml:"oauth"`
	Notes        string            `toml:"notes"`
	// KeyURL is where the vendor issues API keys — a setup hint the connect
	// wizard shows, never a URL Ink fetches.
	KeyURL string `toml:"key_url"`
}

//go:embed registry.toml
var registryTOML []byte

var (
	entries []Entry          // sorted by ID
	byName  map[string]Entry // id and every alias
)

func init() {
	var doc struct {
		Provider []Entry `toml:"provider"`
	}
	if err := toml.Unmarshal(registryTOML, &doc); err != nil {
		panic("providers: embedded registry.toml is invalid: " + err.Error())
	}
	entries = doc.Provider
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	byName = make(map[string]Entry, len(entries)*2)
	for _, e := range entries {
		for _, name := range append([]string{e.ID}, e.Aliases...) {
			if _, dup := byName[name]; dup {
				panic("providers: duplicate registry name " + name)
			}
			byName[name] = e
		}
	}
}

// All returns every registry entry sorted by id. The slice is a copy.
func All() []Entry {
	out := make([]Entry, len(entries))
	copy(out, entries)
	return out
}

// Lookup resolves an id or alias to its entry.
func Lookup(idOrAlias string) (Entry, bool) {
	e, ok := byName[idOrAlias]
	return e, ok
}

// WireFor detects the wire protocol for a base URL the way Hermes does:
// an explicit override wins, a path ending in /anthropic selects
// anthropic_messages, and anything else returns "" so the caller falls
// back to the registry entry's wire (or chat_completions for customs).
func WireFor(baseURL, override string) string {
	if override != "" {
		return override
	}
	trimmed := strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(trimmed, "/anthropic") {
		return WireAnthropicMessages
	}
	return ""
}

// builtinKinds are the provider kinds that exist without a registry entry.
var builtinKinds = []string{"mock", "openai_compatible"}

// KnownKind reports whether kind names a built-in adapter or a registry
// entry, and on a miss suggests the nearest known name.
func KnownKind(kind string) error {
	if kind == "" {
		return fmt.Errorf("provider kind is empty; use a registry id (see `ink providers`), %q, or %q", "openai_compatible", "mock")
	}
	for _, b := range builtinKinds {
		if kind == b {
			return nil
		}
	}
	if _, ok := byName[kind]; ok {
		return nil
	}
	msg := fmt.Sprintf("unknown provider kind %q", kind)
	if near := nearest(kind); near != "" {
		msg += fmt.Sprintf("; did you mean %q?", near)
	}
	return fmt.Errorf("%s (see `ink providers` for the registry)", msg)
}

// nearest returns the known name with the smallest edit distance from
// name, or "" when nothing comes within distance 3.
func nearest(name string) string {
	best, bestDist := "", 4
	candidates := make([]string, 0, len(byName)+len(builtinKinds))
	for n := range byName {
		candidates = append(candidates, n)
	}
	candidates = append(candidates, builtinKinds...)
	sort.Strings(candidates)
	for _, c := range candidates {
		if d := editDistance(name, c); d < bestDist {
			best, bestDist = c, d
		}
	}
	return best
}

// editDistance is the Levenshtein distance between a and b.
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		row := make([]int, len(rb)+1)
		row[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			row[j] = min(prev[j]+1, min(row[j-1]+1, prev[j-1]+cost))
		}
		prev = row
	}
	return prev[len(rb)]
}
