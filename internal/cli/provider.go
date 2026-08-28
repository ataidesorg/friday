package cli

// The real-provider path for `friday run` without --script: resolve
// the default route, let the router decide, and build the wire adapter for
// the selected provider. Credentials resolve at call time, are registered
// with redact the moment they resolve, and are zeroed when the run closes.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ataidesorg/friday/internal/auth"
	"github.com/ataidesorg/friday/internal/config"
	"github.com/ataidesorg/friday/internal/core"
	"github.com/ataidesorg/friday/internal/models"
	"github.com/ataidesorg/friday/internal/models/wire"
	"github.com/ataidesorg/friday/internal/providers"
	"github.com/ataidesorg/friday/internal/redact"
	"github.com/ataidesorg/friday/internal/routing"
	"github.com/ataidesorg/friday/internal/runtime"
)

// deltaRelay forwards wire stream deltas to the runtime observer. The
// provider is built before the interface exists; runCmd sets obs before
// the run goroutine starts.
type deltaRelay struct{ obs runtime.Observer }

func (r *deltaRelay) send(d string) {
	if r.obs != nil {
		r.obs.OnModelDelta(d)
	}
}

// credSource resolves the provider credential at call time and remembers
// every resolved credential so close can zero them all: tokens live in
// memory only and die with the run.
type credSource struct {
	resolve func(ctx context.Context) (*auth.Credential, error)

	mu    sync.Mutex
	creds []*auth.Credential
}

func (c *credSource) credential(ctx context.Context) (*auth.Credential, error) {
	cred, err := c.resolve(ctx)
	if err != nil || cred == nil {
		return cred, err
	}
	c.mu.Lock()
	c.creds = append(c.creds, cred)
	c.mu.Unlock()
	return cred, nil
}

func (c *credSource) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, cred := range c.creds {
		cred.Zero()
	}
	c.creds = nil
}

// warnBuffer collects provider-layer warnings raised mid-run (401 retry)
// so the session can append them to the trail as Warning events once the
// run ends and sequence numbers are known. Messages never carry credentials.
type warnBuffer struct {
	mu   sync.Mutex
	msgs []string
}

func (b *warnBuffer) add(msg string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.msgs = append(b.msgs, msg)
}

func (b *warnBuffer) drain() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	msgs := b.msgs
	b.msgs = nil
	return msgs
}

// providerTarget is what route resolution hands the run path.
type providerTarget struct {
	provider core.ModelProvider
	model    string
	decision core.RouteDecision
	creds    *credSource
	relay    *deltaRelay
	warns    *warnBuffer
	probe    func(ctx context.Context) core.ProviderHealth
}

func (t *providerTarget) close() {
	if t != nil && t.creds != nil {
		t.creds.close()
	}
}

// resolveProvider picks the default route, asks the router to decide, and
// builds the wire adapter for the winner. modelOverride (--model) replaces
// the route's model name on every request.
func resolveProvider(cfg config.Config, modelOverride string, red *redact.Redactor, environ []string) (*providerTarget, error) {
	if len(cfg.Models.Routes) == 0 {
		return nil, fmt.Errorf("no model routes configured; add [models.routes.NAME] to your config or pass --script FILE")
	}
	routeName := cfg.Models.Routing.Default
	if routeName == "" {
		return nil, fmt.Errorf("no default route; set models.routing.default (try `friday model --set ROUTE`) or pass --script FILE")
	}
	return resolveRoute(cfg, routeName, modelOverride, red, environ)
}

// resolveRoute builds the wire adapter for a named route. It is the seam the
// TUI's live /model switch and a per-session route override both target, so
// resolution is identical whether the route comes from config default or a
// runtime choice.
func resolveRoute(cfg config.Config, routeName, modelOverride string, red *redact.Redactor, environ []string) (*providerTarget, error) {
	rc, ok := cfg.Models.Routes[routeName]
	if !ok {
		return nil, fmt.Errorf("route %q is not defined under [models.routes]", routeName)
	}
	prices, err := routing.Prices(cfg.Models.Pricing)
	if err != nil {
		return nil, err
	}
	descriptors, keys := map[string]core.ProviderDescriptor{}, map[string]int{}
	for _, r := range cfg.Models.Routes {
		if _, done := descriptors[r.Provider]; done {
			continue
		}
		d, n, err := describeProvider(r.Provider, cfg)
		if err != nil {
			return nil, err
		}
		descriptors[r.Provider], keys[r.Provider] = d, n
	}
	router, err := routing.New(cfg.Models.Routes, descriptors, prices, keys)
	if err != nil {
		return nil, err
	}
	decision, err := router.Decide(context.Background(), routeName, core.RouteConstraints{AllowFallback: rc.AllowFallback}, core.Usage{})
	if err != nil {
		return nil, err
	}
	return buildTarget(decision, modelOverride, cfg, red, environ)
}

// describeProvider builds the minimal descriptor routing needs. Health
// starts unknown: probes are advisory and run only to diagnose a failure.
func describeProvider(id string, cfg config.Config) (core.ProviderDescriptor, int, error) {
	pc, configured := cfg.Providers[id]
	entry, isRegistry := providers.Lookup(id)
	if !configured && !isRegistry {
		return core.ProviderDescriptor{}, 0, fmt.Errorf("provider %q is neither configured under [providers] nor a registry id (see `friday providers`)", id)
	}
	if pc.Kind == "mock" {
		return core.ProviderDescriptor{}, 0, fmt.Errorf("provider %q is a mock; mock providers run only with --script", id)
	}
	privacy := entry.Privacy
	if pc.Privacy != "" {
		privacy = pc.Privacy
	}
	if privacy == "" {
		privacy = string(core.PrivacyPublicCloud) // fail safe: unknown data flow counts as most public
	}
	n := len(pc.AuthRefs())
	if n == 0 {
		n = 1
	}
	return core.ProviderDescriptor{
		ID:      id,
		Privacy: core.PrivacyClass(privacy),
		// Every implemented wire adapter declares tool calling.
		Capabilities: core.ProviderCapabilities{Streaming: true, ToolCalling: true},
	}, n, nil
}

// riskOptInErr enforces the opt-in for vendor-prohibited auth
// flows at provider construction, so every entry point (run, probe) hits
// it. Storing credentials and `auth login` stay allowed with a warning;
// only use is gated. Repository files can never set the flag (the config
// gate drops it), so a true value is always the user's own choice.
func riskOptInErr(entry providers.Entry, pc config.ProviderConfig) error {
	if !entry.Auth.OptInRisk || pc.AcceptThirdPartyOAuthRisk {
		return nil
	}
	return fmt.Errorf("provider %s uses a third-party auth flow its vendor's terms prohibit; set providers.%s.accept_third_party_oauth_risk = true in your user config to use it", entry.ID, entry.ID)
}

// buildTarget constructs the wire adapter for the decided route.
func buildTarget(decision core.RouteDecision, modelOverride string, cfg config.Config, red *redact.Redactor, environ []string) (*providerTarget, error) {
	sel := decision.Selected
	desc, _, err := describeProvider(sel.Provider, cfg)
	if err != nil {
		return nil, err
	}
	pc := cfg.Providers[sel.Provider]
	entry, isRegistry := providers.Lookup(sel.Provider)
	if isRegistry {
		if err := riskOptInErr(entry, pc); err != nil {
			return nil, err
		}
	}
	wireName, baseURL := probeTarget(isRegistry, entry, pc, envLookupOK(environ))
	model := sel.Model
	if modelOverride != "" {
		model = modelOverride
	}
	resolver := auth.NewResolver(red, envLookupOK(environ), auth.WithGetenv(envLookup(environ)))
	cs := &credSource{resolve: func(ctx context.Context) (*auth.Credential, error) {
		if isRegistry {
			return resolver.ForProvider(ctx, entry, &pc)
		}
		if pc.Auth != nil {
			return resolver.Resolve(ctx, *pc.Auth)
		}
		return nil, nil // keyless custom endpoint (local server)
	}}
	relay := &deltaRelay{}
	warns := &warnBuffer{}
	opts := wire.Options{
		ID:         sel.Provider,
		BaseURL:    baseURL,
		Model:      model,
		Privacy:    desc.Privacy,
		Credential: cs.credential,
		OnDelta:    relay.send,
	}
	if isRegistry && auth.MergedOAuth(entry, &pc).ExchangeURL != "" {
		// Exchanged bearers (Copilot) can be revoked server-side before
		// their local expiry: drop the cached bearer and retry once on 401.
		opts.RetryUnauthorized = true
		opts.InvalidateCredential = func() { resolver.DropBearer(entry.ID) }
		opts.Warnf = func(format string, args ...any) {
			// Surface immediately on stderr (buildTarget has no stderr
			// seam) and buffer for the trail: the session records it as a
			// Warning event after the run. Never carries a credential.
			msg := fmt.Sprintf(format, args...)
			warns.add(msg)
			fmt.Fprintln(os.Stderr, "warning: "+msg)
		}
	}
	if isRegistry && entry.ID == "copilot" {
		opts.Headers = copilotHeaders()
	}
	keyless := providerIsKeyless(isRegistry, entry, pc, envLookupOK(environ))
	if keyless && desc.Privacy == core.PrivacyPublicCloud {
		// Anonymous requests against a public endpoint are loud. An
		// unconfirmed endpoint stays unusable until the user confirms one
		// in config.
		if baseURL == "" {
			return nil, fmt.Errorf("provider %s endpoint unconfirmed; set providers.%s.base_url to use it", sel.Provider, sel.Provider)
		}
		msg := fmt.Sprintf("provider %s runs keyless against a public endpoint: prompts leave this machine unauthenticated", sel.Provider)
		warns.add(msg)
		fmt.Fprintln(os.Stderr, "warning: "+msg)
	}
	switch wireName {
	case providers.WireBedrock:
		// SigV4 signs each request; there is no bearer. Credentials resolve
		// per request inside the signer and are zeroed the moment the
		// signature exists.
		region, rerr := awsRegionFor(baseURL, envLookupOK(environ))
		if rerr != nil {
			return nil, rerr
		}
		if baseURL == "" {
			baseURL = "https://bedrock-runtime." + region + ".amazonaws.com"
			opts.BaseURL = baseURL
		}
		opts.Credential = nil
		opts.Sign = func(ctx context.Context, req *http.Request, payload []byte) error {
			creds, err := resolver.AWSCredentials(ctx)
			if err != nil {
				return err
			}
			defer creds.Zero()
			sum := sha256.Sum256(payload)
			creds.SignRequest(req, hex.EncodeToString(sum[:]), region, "bedrock", time.Now())
			return nil
		}
	case providers.WireVertex:
		if baseURL == "" {
			return nil, fmt.Errorf("provider %s needs a base URL: set VERTEX_BASE_URL (or providers.%s.base_url) to https://<region>-aiplatform.googleapis.com/v1beta1/projects/<project>/locations/<region>/endpoints/openapi", sel.Provider, sel.Provider)
		}
	}
	p, err := adapterFor(wireName, opts)
	if err != nil {
		return nil, err
	}
	probe := func(ctx context.Context) core.ProviderHealth {
		cred, err := cs.credential(ctx)
		if err != nil {
			cred = nil // diagnose reachability even when the credential is missing
		}
		return models.Probe(ctx, nil, wireName, baseURL, cred, time.Now())
	}
	return &providerTarget{provider: p, model: model, decision: decision, creds: cs, relay: relay, warns: warns, probe: probe}, nil
}

// copilotHeaders identifies the client to the Copilot API. The endpoint
// serves only known editor integrations; these values are recorded from
// public Copilot-compatible clients and unverified by
// Friday.
func copilotHeaders() map[string]string {
	return map[string]string{
		"Editor-Version":         "vscode/1.98.1",
		"Copilot-Integration-Id": "vscode-chat",
		"Openai-Intent":          "conversation-panel",
		"X-Initiator":            "user",
	}
}

// adapterFor maps a wire protocol to its adapter; protocols with no
// adapter are safely unavailable.
func adapterFor(wireName string, o wire.Options) (core.ModelProvider, error) {
	switch wireName {
	case providers.WireChatCompletions:
		p, err := wire.NewChatCompletions(o)
		if err != nil {
			return nil, err
		}
		return p, nil
	case providers.WireAnthropicMessages:
		p, err := wire.NewAnthropicMessages(o)
		if err != nil {
			return nil, err
		}
		return p, nil
	case providers.WireResponses:
		p, err := wire.NewResponses(o)
		if err != nil {
			return nil, err
		}
		return p, nil
	case providers.WireVertex:
		// Vertex serves an OpenAI-compatible surface; the GCP bearer from
		// the auth chain rides the standard Authorization header.
		p, err := wire.NewChatCompletions(o)
		if err != nil {
			return nil, err
		}
		return p, nil
	case providers.WireBedrock:
		p, err := wire.NewBedrock(o)
		if err != nil {
			return nil, err
		}
		return p, nil
	default:
		return nil, core.NotImplementedError{Feature: "wire protocol " + wireName}
	}
}

// awsRegionFor picks the SigV4 signing region: AWS_REGION, then
// AWS_DEFAULT_REGION, then the region embedded in a
// bedrock-runtime.<region>.amazonaws.com base URL.
func awsRegionFor(baseURL string, lookup func(string) (string, bool)) (string, error) {
	for _, k := range []string{"AWS_REGION", "AWS_DEFAULT_REGION"} {
		if v, ok := lookup(k); ok && v != "" {
			return v, nil
		}
	}
	if baseURL != "" {
		if u, err := url.Parse(baseURL); err == nil {
			if rest, ok := strings.CutPrefix(u.Hostname(), "bedrock-runtime."); ok {
				if region, ok := strings.CutSuffix(rest, ".amazonaws.com"); ok && region != "" {
					return region, nil
				}
			}
		}
		return "", fmt.Errorf("cannot infer the AWS region from base URL %q; set AWS_REGION", baseURL)
	}
	return "", fmt.Errorf("cannot infer the AWS region: set AWS_REGION (or AWS_DEFAULT_REGION), or point BEDROCK_BASE_URL at bedrock-runtime.<region>.amazonaws.com")
}

// providerIsKeyless reports whether the built request will carry no
// credential: a custom endpoint with no auth block, an auth-none registry
// entry with none configured, or an optional-auth registry entry with no
// env key set and no auth block. It drives the public-cloud warning,
// so a configured auth source (pc.Auth) or a resolvable env key means the
// request is not keyless and stays quiet.
func providerIsKeyless(isRegistry bool, entry providers.Entry, pc config.ProviderConfig, lookup func(string) (string, bool)) bool {
	if pc.Auth != nil {
		return false
	}
	if !isRegistry {
		return true
	}
	switch {
	case entry.Auth.Kind == providers.AuthNone:
		return true
	case entry.Auth.Optional:
		return !anyEnvSet(entry.Auth.EnvNames, lookup)
	}
	return false
}

func anyEnvSet(names []string, lookup func(string) (string, bool)) bool {
	for _, n := range names {
		if v, ok := lookup(n); ok && v != "" {
			return true
		}
	}
	return false
}
