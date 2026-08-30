package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/ataidesorg/ink/internal/auth"
	"github.com/ataidesorg/ink/internal/config"
	"github.com/ataidesorg/ink/internal/models/catalog"
	"github.com/ataidesorg/ink/internal/providers"
	"github.com/ataidesorg/ink/internal/redact"
	"github.com/ataidesorg/ink/internal/tui"
)

// errSetupAborted is the sentinel firstRunSetup returns when the user quits a
// picker; the caller reports it as a plain cancellation, not a failure.
var errSetupAborted = errors.New("model setup cancelled")

// needsModelSetup reports whether chat has no route to run at all — the true
// first-run state. A config with routes but no default is not first-run: it
// gets resolveProvider's `ink model --set` guidance instead.
func needsModelSetup(cfg config.Config) bool {
	return len(cfg.Models.Routes) == 0
}

// firstRunSetup walks a new user from an empty config to one runnable route:
// pick a provider whose credentials resolve, pick one of its catalog models
// (or type an id), then write [models.routes.<name>] and
// models.routing.default into the user config layer. It returns the route
// name written, or errSetupAborted if the user quits.
func firstRunSetup(cfg config.Config, opts config.Options, in, out *os.File, stderr io.Writer, environ []string) (string, error) {
	if in == nil || out == nil {
		return "", errors.New("model setup needs a terminal")
	}
	provider, custom, err := pickProvider(cfg, in, out, environ)
	if err != nil {
		return "", err
	}
	if custom {
		return customSetup(opts, in, out, stderr, environ)
	}
	model, err := pickModel(cfg, provider, in, out, stderr, environ)
	if err != nil {
		return "", err
	}
	// No routes exist yet (needsModelSetup guaranteed it), so the provider id
	// is a collision-free route name.
	name := provider
	path, err := writeUserRoute(opts.ConfigDir, name, provider, model, true)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(stderr, "wrote route %q (%s/%s) as the default in %s\n", name, provider, model, path)
	return name, nil
}

// pickProvider opens with a short screen — the providers whose credentials
// already resolve, one generic OpenAI-compatible entry, and an "all
// providers" expander — instead of dumping the full registry. custom is true
// when the user chose the generic endpoint flow. Selecting a provider
// without a usable credential still fails with the command that fixes it.
func pickProvider(cfg config.Config, in, out *os.File, environ []string) (provider string, custom bool, err error) {
	rows := providerRows(cfg)
	if len(rows) == 0 {
		return "", false, errors.New("no providers available to configure")
	}
	lookup := envLookupOK(environ)
	resolver := auth.NewResolver(redact.New(), lookup, auth.WithGetenv(envLookup(environ)))
	full := make([]listItem, len(rows))
	ready := make([]bool, len(rows))
	start := 0
	for i, r := range rows {
		reason := credReason(r, resolver, lookup)
		ready[i] = reason == ""
		note := reason
		if note == "" {
			note = "ready"
			if start == 0 {
				start = i // land the cursor on the first usable provider
			}
		}
		full[i] = listItem{label: r.id, note: note}
	}
	items, index := firstScreen(rows, ready)
	idx, ok, err := selectList(in, out, "pick a provider (enter to select, q to abort)", items, 0)
	if err != nil {
		return "", false, err
	}
	if !ok {
		return "", false, errSetupAborted
	}
	switch pick := index[idx]; pick {
	case pickCustom:
		return "", true, nil
	case pickAll:
		j, ok, err := selectList(in, out, "pick a provider (enter to select, q to abort)", full, start)
		if err != nil {
			return "", false, err
		}
		if !ok {
			return "", false, errSetupAborted
		}
		if !ready[j] {
			// The note carries the exact reason (a missing credential, or the
			// risk opt-in); point at both fixers rather than guess one.
			return "", false, fmt.Errorf("provider %s is not ready (%s); configure it (see `ink auth set %s` or `ink providers`), then run `ink` again", rows[j].id, full[j].note, rows[j].id)
		}
		return rows[j].id, false, nil
	default:
		return rows[pick].id, false, nil
	}
}

// Sentinel indices for firstScreen entries that are actions, not providers.
const (
	pickCustom = -1
	pickAll    = -2
)

// firstScreen builds the short opening list: every ready provider, the
// generic OpenAI-compatible entry, and the full-registry expander. index maps
// each item back to its provider row, or to a pick* sentinel.
func firstScreen(rows []providerRow, ready []bool) ([]listItem, []int) {
	var items []listItem
	var index []int
	for i, r := range rows {
		if ready[i] {
			items = append(items, listItem{label: r.id, note: "ready"})
			index = append(index, i)
		}
	}
	items = append(items,
		listItem{label: "openai-compatible", note: "any OpenAI-compatible endpoint (base URL + key)"},
		listItem{label: "all providers", note: fmt.Sprintf("full list (%d), credentials needed", len(rows))})
	index = append(index, pickCustom, pickAll)
	return items, index
}

// customSetup walks the generic endpoint flow: a base URL, the env var
// holding the API key, and a model id, written as an openai_compatible
// [providers.<name>] plus the default route. A base URL ending /anthropic
// selects the Anthropic wire (providers.WireFor); everything else speaks
// chat completions.
func customSetup(opts config.Options, in, out *os.File, stderr io.Writer, environ []string) (string, error) {
	baseURL, err := ask(in, out, "base URL (e.g. https://api.example.com/v1): ")
	if err != nil {
		return "", err
	}
	if u, perr := url.Parse(baseURL); perr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("base URL must be http(s)://host[/path], got %q", baseURL)
	}
	envName, err := ask(in, out, "API key env var [OPENAI_API_KEY]: ")
	if err != nil {
		return "", err
	}
	if envName == "" {
		envName = "OPENAI_API_KEY"
	}
	if strings.ContainsFunc(envName, func(r rune) bool {
		return r != '_' && (r < 'A' || r > 'Z') && (r < '0' || r > '9')
	}) {
		return "", fmt.Errorf("%q is not an env var name (A-Z, 0-9, _)", envName)
	}
	if v, ok := envLookupOK(environ)(envName); !ok || v == "" {
		return "", fmt.Errorf("$%s is not set; export it, then run `ink` again", envName)
	}
	model, err := ask(in, out, "model id: ")
	if err != nil {
		return "", err
	}
	if model == "" {
		return "", errSetupAborted
	}
	name := customName(baseURL)
	if _, err := writeUserProvider(opts.ConfigDir, name, baseURL, map[string]any{"source": "env", "name": envName}); err != nil {
		return "", err
	}
	path, err := writeUserRoute(opts.ConfigDir, name, name, model, true)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(stderr, "wrote provider %q (%s) and route %q (%s) as the default in %s\n", name, baseURL, name, model, path)
	return name, nil
}

// ask prints a prompt and returns the trimmed reply; an empty reply on EOF
// aborts the setup like a picker quit.
func ask(in, out *os.File, prompt string) (string, error) {
	fmt.Fprint(out, prompt)
	line, err := readLine(in)
	line = strings.TrimSpace(line)
	if err != nil && line == "" {
		return "", errSetupAborted
	}
	return line, nil
}

// customName derives a config-friendly provider name from the endpoint host
// ("https://api.groq.com/v1" → "groq"); anything unusable becomes "custom".
// Colliding with a registry id is fine — the written base_url and auth then
// override that entry, which is what pointing at its host means.
func customName(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil || u.Hostname() == "" {
		return "custom"
	}
	host := strings.TrimPrefix(strings.TrimPrefix(u.Hostname(), "www."), "api.")
	name, _, _ := strings.Cut(host, ".")
	name = strings.Map(func(r rune) rune {
		if r == '-' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		return -1
	}, strings.ToLower(name))
	if name == "" {
		return "custom"
	}
	return name
}

// updateUserConfig runs one read-merge-encode round trip on the user
// config layer: parse the existing file (absent is fine), let mutate edit
// the tree, re-encode. Hand-written comments do not survive; values do.
func updateUserConfig(configDir string, mutate func(map[string]any)) (string, error) {
	if configDir == "" {
		return "", fmt.Errorf("no user config directory; pass --config-dir or set $INK_CONFIG_DIR")
	}
	path := filepath.Join(configDir, "config.toml")
	m := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil { //nolint:gosec // user's own config path
		if err := toml.Unmarshal(raw, &m); err != nil {
			return "", fmt.Errorf("parse %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	mutate(m)
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(m); err != nil {
		return "", fmt.Errorf("encode %s: %w", path, err)
	}
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", configDir, err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}

// writeUserProvider writes [providers.<name>] as an openai_compatible entry
// into the user config layer. authRef names where the credential lives (an
// env var or a secret-store id) — never the credential itself.
func writeUserProvider(configDir, name, baseURL string, authRef map[string]any) (string, error) {
	return updateUserConfig(configDir, func(m map[string]any) {
		subTable(m, "providers")[name] = map[string]any{
			"kind":     "openai_compatible",
			"base_url": baseURL,
			"privacy":  "public_cloud", // fail safe: an unknown endpoint counts as most public
			"auth":     authRef,
		}
	})
}

// credReason returns "" when the provider's credential resolves offline (or
// it is a keyless loopback), else a short reason. No network probe runs here:
// first-run setup only needs to know a call could be authenticated.
func credReason(r providerRow, resolver *auth.Resolver, lookup func(string) (string, bool)) string {
	if r.registry && r.entry.Auth.OptInRisk {
		pc := config.ProviderConfig{}
		if r.override != nil {
			pc = *r.override
		}
		if riskOptInErr(r.entry, pc) != nil {
			return "third-party OAuth risk not accepted"
		}
	}
	_, cred, reason := probeInputs(r, resolver, lookup)
	if cred != nil {
		cred.Zero()
	}
	// probeInputs frames reasons as "not probed (X)"; drop that wrapper.
	reason = strings.TrimPrefix(reason, "not probed (")
	return strings.TrimSuffix(reason, ")")
}

// pickModel lists the provider's catalog for selection, or falls back to a
// typed id when the provider advertises no listable catalog.
func pickModel(cfg config.Config, provider string, in, out *os.File, stderr io.Writer, environ []string) (string, error) {
	models := providerCatalog(cfg, provider, environ, stderr)
	if len(models) == 0 {
		fmt.Fprintf(out, "no model catalog for %s; type a model id: ", provider)
		name, err := readLine(in)
		if err != nil && name == "" {
			return "", err
		}
		name = strings.TrimSpace(name)
		if name == "" {
			return "", errSetupAborted
		}
		return name, nil
	}
	items := make([]listItem, len(models))
	for i, m := range models {
		items[i] = listItem{label: m}
	}
	idx, ok, err := selectList(in, out, "pick a model for "+provider+" (enter to select, q to abort)", items, 0)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errSetupAborted
	}
	return models[idx], nil
}

// providerCatalog fetches a provider's advertised models the same way
// `ink models` does; a provider with no catalogable wire, or any fetch
// trouble, degrades to an empty list (the caller then asks for a typed id).
// ponytail: mirrors modelsCmd's fetch; kept separate because that command
// maps the same conditions to exit codes, this one to graceful degradation.
func providerCatalog(cfg config.Config, provider string, environ []string, stderr io.Writer) []string {
	if _, _, err := describeProvider(provider, cfg); err != nil {
		return nil
	}
	pc := cfg.Providers[provider]
	entry, isRegistry := providers.Lookup(provider)
	wireName, baseURL := probeTarget(isRegistry, entry, pc, envLookupOK(environ))
	switch wireName {
	case providers.WireChatCompletions, providers.WireResponses:
	default:
		return nil
	}
	id := provider
	if isRegistry {
		id = entry.ID
	}
	ctx := context.Background()
	dir, err := catalog.Dir(envLookup(environ))
	if err != nil {
		return nil
	}
	res := catalog.Models(ctx, catalog.Options{
		Provider: id,
		BaseURL:  baseURL,
		Bearer:   catalogBearer(ctx, isRegistry, entry, pc, environ),
		CacheDir: dir,
	})
	if res.Note != "" {
		fmt.Fprintln(stderr, "note: "+res.Note)
	}
	return res.Models
}

// writeUserRoute writes [models.routes.<name>] = {provider, model} into the
// user config layer, creating the file if absent, and — when setDefault —
// points models.routing.default at it.
func writeUserRoute(configDir, name, provider, model string, setDefault bool) (string, error) {
	return updateUserConfig(configDir, func(m map[string]any) {
		modelsTab := subTable(m, "models")
		subTable(modelsTab, "routes")[name] = map[string]any{"provider": provider, "model": model}
		if setDefault {
			subTable(modelsTab, "routing")["default"] = name
		}
	})
}

// connectProviders lists the registry entries the chat /connect wizard can
// configure: key-auth providers with a usable base URL, plus OAuth
// providers whose endpoints the registry records — those sign in from the
// wizard itself. Vendor-prohibited (opt_in_risk) flows and entries with
// no recorded endpoints stay out: Ink never invents an OAuth flow.
func connectProviders() []tui.ProviderInfo {
	var out []tui.ProviderInfo
	for _, e := range providers.All() {
		if e.BaseURL == "" {
			continue // unusable until a vendor-confirmed URL is recorded
		}
		switch e.Auth.Kind {
		case providers.AuthKey:
			out = append(out, tui.ProviderInfo{Name: e.ID, Detail: hostOf(e.BaseURL), KeyURL: e.KeyURL})
		case providers.AuthOAuth2PKCE, providers.AuthOAuth2Device, providers.AuthExternalCLI:
			if !oauthReady(e) {
				continue
			}
			if e.Auth.OptInRisk {
				// Vendor-prohibited flows stay out of the default wizard.
				// `ink providers` still lists them with their risk notes.
				continue
			}
			out = append(out, tui.ProviderInfo{Name: e.ID, Detail: "sign in with your browser", OAuth: true})
		}
	}
	return out
}

// oauthReady reports whether the entry carries the recorded endpoints its
// login flow needs; anything less fails closed out of the picker.
func oauthReady(e providers.Entry) bool {
	if e.OAuth.TokenURL == "" || e.OAuth.ClientID == "" {
		return false
	}
	if e.Auth.Kind == providers.AuthOAuth2Device {
		return e.OAuth.DeviceAuthURL != ""
	}
	return e.OAuth.AuthURL != ""
}

// hostOf shortens a base URL to its host for picker details.
func hostOf(baseURL string) string {
	if u, err := url.Parse(baseURL); err == nil && u.Host != "" {
		return u.Host
	}
	return baseURL
}

// connect backs the chat /connect wizard: store the key in the encrypted
// secret store, write the provider (custom endpoints only) and route into
// the user config, then switch the live session onto the new route. The key
// is registered with the redactor before anything can log it and never
// lands in a config file.
func (c *chatSession) connect(opts config.Options, req tui.ConnectRequest) (tui.RouteInfo, error) {
	name, routeName, err := connectWrite(opts.ConfigDir, c.cfg, c.red, c.environ, req)
	if err != nil {
		return tui.RouteInfo{}, errors.New(c.red.Redact(err.Error()))
	}
	resolved, err := config.Load(opts)
	if err == nil {
		err = config.Validate(resolved)
	}
	if err != nil {
		return tui.RouteInfo{}, errors.New(c.red.Redact("config written but failed to reload: " + err.Error()))
	}
	c.cfg = resolved.Config
	if err := c.switchRoute(routeName); err != nil {
		return tui.RouteInfo{}, err // switchRoute already redacts
	}
	return tui.RouteInfo{Name: routeName, Provider: name, Model: req.Model}, nil
}

// connectWrite is the stateful half of /connect — redactor registration,
// the secret-store write, and the user-config writes — split from
// chatSession.connect so it is testable without a live provider target.
// An empty Key means an OAuth sign-in already stored the credential: only
// the route is written. The route becomes the config default only when no
// routes existed, the first-run case; otherwise existing setups keep their
// default and only this session switches.
func connectWrite(configDir string, cfg config.Config, red *redact.Redactor, environ []string, req tui.ConnectRequest) (provider, route string, err error) {
	if strings.TrimSpace(req.Model) == "" {
		return "", "", errors.New("model id is required")
	}
	name := ""
	if req.Provider != "" {
		entry, ok := providers.Lookup(req.Provider)
		if !ok {
			return "", "", fmt.Errorf("unknown provider %q; `ink providers` lists them", req.Provider)
		}
		name = entry.ID // canonical id, aliases collapse
		if strings.TrimSpace(req.Key) == "" && entry.Auth.Kind == providers.AuthKey {
			return "", "", fmt.Errorf("%s needs an api key", entry.ID)
		}
	} else {
		u, perr := url.Parse(req.BaseURL)
		if perr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return "", "", fmt.Errorf("base URL must be http(s)://host[/path], got %q", req.BaseURL)
		}
		if strings.TrimSpace(req.Key) == "" {
			return "", "", errors.New("an api key is required for a custom endpoint")
		}
		name = customName(req.BaseURL)
	}
	if strings.TrimSpace(req.Key) != "" {
		red.AddLiteral(req.Key)
		resolver := auth.NewResolver(red, envLookupOK(environ), auth.WithGetenv(envLookup(environ)))
		if err := resolver.StoreSet(name, req.Key); err != nil {
			return "", "", err
		}
		if req.Provider == "" {
			ref := map[string]any{"source": "secret_store", "id": name}
			if _, err := writeUserProvider(configDir, name, req.BaseURL, ref); err != nil {
				return "", "", err
			}
		}
	}
	routeName := freeRouteName(cfg, name, name, req.Model)
	if _, err := writeUserRoute(configDir, routeName, name, req.Model, len(cfg.Models.Routes) == 0); err != nil {
		return "", "", err
	}
	return name, routeName, nil
}

// connectModels backs the wizard's live catalog fetch: the just-typed key
// (or, when Key is empty, the token the sign-in stored) is validated
// against the provider's model list, so the model step offers what the
// account can actually reach. Degraded fetches return a note, never an
// error — the wizard falls back to a typed model id.
func (c *chatSession) connectModels(req tui.ConnectRequest) ([]string, string) {
	id, wireName, baseURL := req.Provider, providers.WireChatCompletions, req.BaseURL
	bearer := req.Key
	if req.Provider != "" {
		entry, ok := providers.Lookup(req.Provider)
		if !ok {
			return nil, "unknown provider " + req.Provider
		}
		id = entry.ID
		pc := c.cfg.Providers[entry.ID]
		if entry.Auth.OptInRisk {
			// The wizard's risk step is the consent; the flag the sign-in
			// wrote may not be in this session's loaded config yet.
			pc.AcceptThirdPartyOAuthRisk = true
		}
		wireName, baseURL = probeTarget(true, entry, pc, envLookupOK(c.environ))
		if bearer == "" {
			bearer = catalogBearer(context.Background(), true, entry, pc, c.environ)
		}
	}
	switch wireName {
	case providers.WireChatCompletions, providers.WireResponses:
	default:
		return nil, "no listable model catalog for this provider; type a model id"
	}
	dir, err := catalog.Dir(envLookup(c.environ))
	if err != nil {
		return nil, err.Error()
	}
	res := catalog.Models(context.Background(), catalog.Options{
		Provider: id,
		BaseURL:  baseURL,
		Bearer:   bearer,
		CacheDir: dir,
		Refresh:  true, // a cached list would not prove the fresh credential
	})
	return res.Models, res.Note
}

// connectLogin backs the wizard's OAuth sign-in. For opt-in-risk entries
// the wizard's risk step is the consent, recorded as
// providers.<id>.accept_third_party_oauth_risk in the user config before
// the browser opens: the recorded opt-in is required before the provider
// can be used.
func (c *chatSession) connectLogin(ctx context.Context, opts config.Options, provider string, progress func(string)) error {
	entry, ok := providers.Lookup(provider)
	if !ok {
		return fmt.Errorf("unknown provider %q", provider)
	}
	if entry.Auth.OptInRisk {
		if err := writeUserProviderRisk(opts.ConfigDir, entry.ID); err != nil {
			return err
		}
	}
	var pc *config.ProviderConfig
	if p, ok := c.cfg.Providers[entry.ID]; ok {
		p := p
		pc = &p
	}
	resolver := auth.NewResolver(c.red, envLookupOK(c.environ), auth.WithGetenv(envLookup(c.environ)))
	login := resolver.LoginPKCE
	if entry.Auth.Kind == providers.AuthOAuth2Device {
		login = resolver.LoginDevice
	}
	o := auth.LoginOptions{Out: &lineWriter{fn: progress}}
	if err := login(ctx, entry.ID, auth.MergedOAuth(entry, pc), o); err != nil {
		return errors.New(c.red.Redact(err.Error()))
	}
	return nil
}

// writeUserProviderRisk records the wizard's accepted risk consent as
// providers.<id>.accept_third_party_oauth_risk in the user config layer —
// the only layer the config gate accepts the flag from.
func writeUserProviderRisk(configDir, id string) error {
	_, err := updateUserConfig(configDir, func(m map[string]any) {
		prov := subTable(m, "providers")
		tbl, _ := prov[id].(map[string]any)
		if tbl == nil {
			tbl = map[string]any{}
		}
		tbl["accept_third_party_oauth_risk"] = true
		prov[id] = tbl
	})
	return err
}

// lineWriter adapts the login flow's io.Writer progress output into the
// wizard's per-line callback. ponytail: a trailing line without a newline
// stays buffered; the login flows end their prompts with one.
type lineWriter struct {
	fn  func(string)
	buf []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		if line := strings.TrimSpace(string(w.buf[:i])); line != "" {
			w.fn(line)
		}
		w.buf = w.buf[i+1:]
	}
}

// freeRouteName returns name when it is unused or already routes exactly
// provider/model (an idempotent reconnect); otherwise the first free
// name-N, so connecting never clobbers a differently-aimed route.
func freeRouteName(cfg config.Config, name, provider, model string) string {
	rt, taken := cfg.Models.Routes[name]
	if !taken || (rt.Provider == provider && rt.Model == model) {
		return name
	}
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s-%d", name, i)
		if _, ok := cfg.Models.Routes[cand]; !ok {
			return cand
		}
	}
}
