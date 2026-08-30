package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ataidesorg/ink/internal/auth"
	"github.com/ataidesorg/ink/internal/config"
	"github.com/ataidesorg/ink/internal/models"
	"github.com/ataidesorg/ink/internal/models/catalog"
	"github.com/ataidesorg/ink/internal/providers"
	"github.com/ataidesorg/ink/internal/redact"
	"golang.org/x/term"
)

// providerRow is one line of `ink providers`: a registry entry or a
// custom provider from config. It carries what probing needs but never a
// credential value.
type providerRow struct {
	id, wire, privacy, authKind, status, health string
	entry                                       providers.Entry
	registry                                    bool
	override                                    *config.ProviderConfig // config override for a registry provider
	overrideURL                                 string                 // config base_url override for a registry provider
	custom                                      config.ProviderConfig
}

// providersCmd lists every registry provider plus the customs configured
// under [providers.*]: id, wire, privacy, auth kind, status, health. The
// listing is offline and instant; --check probes the rows whose credentials
// resolve (or that are keyless) and prints measured health. The output never
// holds a credential: every resolved token is registered with the redactor,
// and the whole table passes through it before printing. Base URLs are not
// printed at all, so a query string in one cannot leak.
func providersCmd(args []string, stdout, stderr io.Writer, environ []string, getwd func() (string, error)) int {
	var g globalFlags
	var check bool
	fs := flag.NewFlagSet("providers", flag.ContinueOnError)
	fs.SetOutput(stderr)
	g.bind(fs)
	fs.BoolVar(&check, "check", false, "probe providers whose credentials resolve")
	positional, err := parseInterleaved(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(positional) != 0 {
		fmt.Fprintln(stderr, "usage: ink providers [--check] [flags]")
		return exitUsage
	}
	opts, err := g.options(environ, getwd, stderr)
	if err != nil {
		return fail(stderr, "providers", exitUsage, err)
	}
	resolved, err := config.Load(opts)
	if err != nil {
		return fail(stderr, "providers", exitError, err)
	}
	out := redact.New()
	warnDropped(stderr, resolved)
	if verr := config.Validate(resolved); verr != nil {
		// The offline listing is registry data and stays available; probing
		// with credentials from an invalid configuration is not.
		if check {
			fmt.Fprintf(stderr, "ink providers: --check needs a valid configuration\n%s\n", out.Redact(verr.Error()))
			return exitError
		}
		fmt.Fprintln(stderr, "warning: configuration is invalid; run `ink config validate`")
	}

	rows := providerRows(resolved.Config)
	if check {
		lookup := envLookupOK(environ)
		resolver := auth.NewResolver(out, lookup, auth.WithGetenv(envLookup(environ)), auth.WithWarnf(func(format string, args ...any) {
			fmt.Fprintf(stderr, "warning: "+format+"\n", args...)
		}))
		for i := range rows {
			rows[i].health = probeRow(rows[i], resolver, lookup)
		}
	}

	var b strings.Builder
	w := tabwriter.NewWriter(&b, 2, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tWIRE\tPRIVACY\tAUTH\tSTATUS\tHEALTH")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", r.id, r.wire, orDash(r.privacy), orDash(r.authKind), r.status, r.health)
	}
	if err := w.Flush(); err != nil {
		return fail(stderr, "providers", exitError, err)
	}
	fmt.Fprint(stdout, out.Redact(b.String()))
	return exitOK
}

// providerRows builds the offline table: the full registry first, then the
// configured customs (config names that match no registry id or alias),
// sorted by name. A custom stays unverified until a real call passes.
func providerRows(cfg config.Config) []providerRow {
	all := providers.All()
	rows := make([]providerRow, 0, len(all)+len(cfg.Providers))
	for _, e := range all {
		row := providerRow{
			id: e.ID, wire: e.Wire, privacy: e.Privacy, authKind: e.Auth.Kind,
			status: e.Status, health: "-", entry: e, registry: true,
		}
		if pc, ok := cfg.Providers[e.ID]; ok {
			pc := pc
			row.override, row.overrideURL = &pc, pc.BaseURL
		}
		rows = append(rows, row)
	}
	customs := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		if _, ok := providers.Lookup(name); !ok {
			customs = append(customs, name)
		}
	}
	sort.Strings(customs)
	for _, name := range customs {
		pc := cfg.Providers[name]
		rows = append(rows, providerRow{
			id: name, wire: customWire(pc), privacy: pc.Privacy, authKind: customAuthKind(pc),
			status: providers.StatusUnverified, health: "-", custom: pc,
		})
	}
	return rows
}

func customWire(pc config.ProviderConfig) string {
	if pc.Kind == "mock" {
		return "mock"
	}
	if w := providers.WireFor(pc.BaseURL, ""); w != "" {
		return w
	}
	return providers.WireChatCompletions
}

func customAuthKind(pc config.ProviderConfig) string {
	if pc.Auth == nil {
		return providers.AuthNone
	}
	return providers.AuthKey + " (" + pc.Auth.Source + ")"
}

// probeRow resolves the row's credential, probes its endpoint, and renders
// the health column. Rows without a resolvable credential, a base URL, or a
// probeable wire stay "not probed" — `ink providers --check` never fails
// a whole listing over one provider.
func probeRow(r providerRow, resolver *auth.Resolver, lookup func(string) (string, bool)) string {
	if r.registry && r.entry.Auth.OptInRisk {
		pc := config.ProviderConfig{}
		if r.override != nil {
			pc = *r.override
		}
		if riskOptInErr(r.entry, pc) != nil {
			return "not probed (accept_third_party_oauth_risk not set)"
		}
	}
	baseURL, cred, reason := probeInputs(r, resolver, lookup)
	if reason != "" {
		return reason
	}
	h := models.Probe(context.Background(), nil, r.wire, baseURL, cred, time.Now())
	if cred != nil {
		cred.Zero()
	}
	if h.Reason == "" {
		return string(h.State)
	}
	return string(h.State) + " (" + h.Reason + ")"
}

func probeInputs(r providerRow, resolver *auth.Resolver, lookup func(string) (string, bool)) (string, *auth.Credential, string) {
	ctx := context.Background()
	if r.registry {
		baseURL := registryBaseURL(r.entry, r.overrideURL, lookup)
		if baseURL == "" {
			return "", nil, "not probed (no base URL)"
		}
		cred, err := resolver.ForProvider(ctx, r.entry, r.override)
		if err != nil {
			var missing *auth.ErrNoCredential
			if errors.As(err, &missing) {
				return "", nil, "not probed (no credential)"
			}
			return "", nil, "not probed (" + err.Error() + ")"
		}
		if cred == nil && !loopbackURL(baseURL) {
			// Keyless probes stay local: an optional-key cloud provider
			// without a key is not a "keyless local".
			return "", nil, "not probed (no credential)"
		}
		return baseURL, cred, ""
	}
	if r.custom.BaseURL == "" {
		return "", nil, "not probed (no base URL)"
	}
	if r.custom.Auth == nil {
		if !loopbackURL(r.custom.BaseURL) {
			return "", nil, "not probed (no credential)"
		}
		return r.custom.BaseURL, nil, ""
	}
	cred, err := resolver.Resolve(ctx, *r.custom.Auth)
	if err != nil {
		var missing *auth.ErrNoCredential
		if errors.As(err, &missing) {
			return "", nil, "not probed (no credential)"
		}
		return "", nil, "not probed (" + err.Error() + ")"
	}
	return r.custom.BaseURL, cred, ""
}

// registryBaseURL resolves a registry provider's endpoint: the entry default,
// overridden by its BaseURLEnv when that is set, then by an explicit config
// base_url.
func registryBaseURL(e providers.Entry, override string, lookup func(string) (string, bool)) string {
	base := e.BaseURL
	if e.BaseURLEnv != "" {
		if v, ok := lookup(e.BaseURLEnv); ok && v != "" {
			base = v
		}
	}
	if override != "" {
		base = override
	}
	return base
}

// loopbackURL reports whether base points at this machine. Only loopback
// endpoints may be probed without a credential.
func loopbackURL(base string) bool {
	u, err := url.Parse(base)
	if err != nil {
		return false
	}
	host := u.Hostname()
	return host == "localhost" || host == "::1" || strings.HasPrefix(host, "127.")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// envLookupOK adapts the injected environ slice to os.LookupEnv shape.
func envLookupOK(environ []string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		for _, kv := range environ {
			if k, v, ok := strings.Cut(kv, "="); ok && k == name {
				return v, true
			}
		}
		return "", false
	}
}

// modelCmd shows and sets the default model route. Interactive terminals
// get a picker; `--set NAME` (and non-terminal
// sessions) work without one. The choice lands in the user config layer as
// models.routing.default — never in the repository.
func modelCmd(args []string, stdout, stderr io.Writer, stdin io.Reader, environ []string, getwd func() (string, error)) int {
	var g globalFlags
	var set string
	fs := flag.NewFlagSet("model", flag.ContinueOnError)
	fs.SetOutput(stderr)
	// The global --set key=value override is not bound here: on this one
	// command the task spec gives --set to the route name.
	fs.StringVar(&g.project, "project", "", "project root")
	fs.StringVar(&g.profile, "profile", "", "profile name")
	fs.StringVar(&g.configDir, "config-dir", "", "user config directory")
	fs.StringVar(&set, "set", "", "route name to make the default (non-interactive)")
	positional, err := parseInterleaved(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(positional) != 0 {
		fmt.Fprintln(stderr, "usage: ink model [--set ROUTE] [flags]")
		return exitUsage
	}
	opts, err := g.options(environ, getwd, stderr)
	if err != nil {
		return fail(stderr, "model", exitUsage, err)
	}
	resolved, err := config.Load(opts)
	if err != nil {
		return fail(stderr, "model", exitError, err)
	}
	warnDropped(stderr, resolved)

	routes := make([]string, 0, len(resolved.Config.Models.Routes))
	for name := range resolved.Config.Models.Routes {
		routes = append(routes, name)
	}
	sort.Strings(routes)
	current := resolved.Config.Models.Routing.Default
	if len(routes) == 0 {
		fmt.Fprintln(stderr, "ink model: no routes configured under [models.routes]")
		return exitError
	}

	if set == "" {
		in, inTTY := stdin.(*os.File)
		out, outTTY := stdout.(*os.File)
		if inTTY && outTTY && term.IsTerminal(int(in.Fd())) && term.IsTerminal(int(out.Fd())) {
			choice, err := pickRoute(in, out, routes, current)
			if err != nil {
				return fail(stderr, "model", exitError, err)
			}
			if choice == "" {
				return exitOK // aborted, nothing written
			}
			set = choice
		} else {
			for _, name := range routes {
				marker := " "
				if name == current {
					marker = "*"
				}
				r := resolved.Config.Models.Routes[name]
				fmt.Fprintf(stdout, "%s %s\t%s/%s\n", marker, name, r.Provider, r.Model)
			}
			fmt.Fprintln(stdout, "run `ink model --set ROUTE` to change the default")
			return exitOK
		}
	}

	if _, ok := resolved.Config.Models.Routes[set]; !ok {
		fmt.Fprintf(stderr, "ink model: unknown route %q (have: %s)\n", set, strings.Join(routes, ", "))
		return exitError
	}
	path, err := writeUserDefaultRoute(opts.ConfigDir, set)
	if err != nil {
		return fail(stderr, "model", exitError, err)
	}
	fmt.Fprintf(stdout, "models.routing.default = %s (written to %s)\n", set, path)
	return exitOK
}

// writeUserDefaultRoute sets models.routing.default in the user config
// layer. The file is decoded and re-encoded, so hand-written comments do
// not survive; values do.
func writeUserDefaultRoute(configDir, route string) (string, error) {
	return updateUserConfig(configDir, func(m map[string]any) {
		subTable(subTable(m, "models"), "routing")["default"] = route
	})
}

func subTable(m map[string]any, key string) map[string]any {
	if t, ok := m[key].(map[string]any); ok {
		return t
	}
	t := map[string]any{}
	m[key] = t
	return t
}

// pickRoute runs the shared picker over route names; empty return means
// aborted.
func pickRoute(in, out *os.File, routes []string, current string) (string, error) {
	items := make([]listItem, len(routes))
	cursor := 0
	for i, r := range routes {
		items[i] = listItem{label: r}
		if r == current {
			cursor = i
		}
	}
	idx, ok, err := selectList(in, out, "default model route (enter to select, q to abort)", items, cursor)
	if err != nil || !ok {
		return "", err
	}
	return routes[idx], nil
}

// modelsCmd lists a provider's advertised models via the optional catalog.
// The catalog is never required: a fetch failure degrades to the
// cached copy (with an age note on stderr) or an empty list, and the exit
// code stays 0 — only an unusable provider or bad flags fail.
func modelsCmd(args []string, stdout, stderr io.Writer, environ []string, getwd func() (string, error)) int {
	var g globalFlags
	var provider string
	var refresh bool
	fs := flag.NewFlagSet("models", flag.ContinueOnError)
	fs.SetOutput(stderr)
	g.bind(fs)
	fs.StringVar(&provider, "provider", "", "provider id to list models for")
	fs.BoolVar(&refresh, "refresh", false, "bypass the 24h catalog cache")
	positional, err := parseInterleaved(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(positional) != 0 || provider == "" {
		fmt.Fprintln(stderr, "usage: ink models --provider ID [--refresh] [flags]")
		return exitUsage
	}
	opts, err := g.options(environ, getwd, stderr)
	if err != nil {
		return fail(stderr, "models", exitUsage, err)
	}
	resolved, err := config.Load(opts)
	if err != nil {
		return fail(stderr, "models", exitError, err)
	}
	warnDropped(stderr, resolved)
	cfg := resolved.Config

	if _, _, err := describeProvider(provider, cfg); err != nil {
		return fail(stderr, "models", exitError, err)
	}
	pc := cfg.Providers[provider]
	entry, isRegistry := providers.Lookup(provider)
	wireName, baseURL := probeTarget(isRegistry, entry, pc, envLookupOK(environ))
	switch wireName {
	case providers.WireChatCompletions, providers.WireResponses:
	default:
		fmt.Fprintf(stderr, "ink models: provider %s (wire %s) has no /v1/models catalog\n", provider, wireName)
		return exitError
	}
	id := provider
	if isRegistry {
		id = entry.ID
	}

	ctx := context.Background()
	dir, err := catalog.Dir(envLookup(environ))
	if err != nil {
		return fail(stderr, "models", exitError, err)
	}
	res := catalog.Models(ctx, catalog.Options{
		Provider: id,
		BaseURL:  baseURL,
		Bearer:   catalogBearer(ctx, isRegistry, entry, pc, environ),
		CacheDir: dir,
		Refresh:  refresh,
	})
	if res.Note != "" {
		fmt.Fprintln(stderr, "note: "+res.Note)
	}
	for _, m := range res.Models {
		fmt.Fprintln(stdout, m)
	}
	return exitOK
}

// catalogBearer resolves the provider's bearer the same way a run would,
// but a miss degrades to a keyless fetch: the catalog is never required,
// so no credential problem may block it. The token stays memory-only and
// is registered with redact by the resolver before it reaches us.
func catalogBearer(ctx context.Context, isRegistry bool, entry providers.Entry, pc config.ProviderConfig, environ []string) string {
	if isRegistry && riskOptInErr(entry, pc) != nil {
		return "" // opt-in-risk flow not accepted: never resolve it
	}
	resolver := auth.NewResolver(redact.New(), envLookupOK(environ), auth.WithGetenv(envLookup(environ)))
	var cred *auth.Credential
	var err error
	switch {
	case isRegistry:
		cred, err = resolver.ForProvider(ctx, entry, &pc)
	case pc.Auth != nil:
		cred, err = resolver.Resolve(ctx, *pc.Auth)
	}
	if err != nil || cred == nil {
		return ""
	}
	defer cred.Zero()
	return string(cred.Value())
}
