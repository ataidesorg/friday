package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ataidesorg/ink/internal/auth"
	"github.com/ataidesorg/ink/internal/config"
	"github.com/ataidesorg/ink/internal/core"
	"github.com/ataidesorg/ink/internal/models"
	"github.com/ataidesorg/ink/internal/providers"
	"github.com/ataidesorg/ink/internal/redact"
	"golang.org/x/term"
)

const authUsage = "usage: ink auth <set PROVIDER [--check] | status | login PROVIDER [--no-browser] | logout PROVIDER> [flags]"

func authCmd(args []string, stdout, stderr io.Writer, stdin io.Reader, environ []string, getwd func() (string, error)) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, authUsage)
		return exitUsage
	}
	switch args[0] {
	case "set":
		return authSet(args[1:], stdout, stderr, stdin, environ, getwd)
	case "status":
		return authStatus(args[1:], stdout, stderr, environ, getwd)
	case "login":
		return authLogin(args[1:], stdout, stderr, stdin, environ, getwd)
	case "logout":
		return authLogout(args[1:], stdout, stderr, environ)
	default:
		fmt.Fprintf(stderr, "ink auth: unknown subcommand %q\n%s\n", args[0], authUsage)
		return exitUsage
	}
}

// authSet reads a secret from a hidden prompt (or one stdin line when not a
// terminal) and writes it into the encrypted secret store under the
// provider's canonical id. The value never appears in argv, output, config,
// or the trail: it is registered with the redactor before anything else can
// print, and the store holds only ciphertext.
func authSet(args []string, stdout, stderr io.Writer, stdin io.Reader, environ []string, getwd func() (string, error)) int {
	var g globalFlags
	var check bool
	fs := flag.NewFlagSet("auth set", flag.ContinueOnError)
	fs.SetOutput(stderr)
	g.bind(fs)
	fs.BoolVar(&check, "check", false, "probe the provider with the new credential after storing it")
	positional, err := parseInterleaved(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(positional) != 1 {
		fmt.Fprintln(stderr, authUsage)
		return exitUsage
	}
	id := positional[0]
	opts, err := g.options(environ, getwd, stderr)
	if err != nil {
		return fail(stderr, "auth set", exitUsage, err)
	}
	resolved, err := config.Load(opts)
	if err != nil {
		return fail(stderr, "auth set", exitError, err)
	}
	warnDropped(stderr, resolved)

	entry, isRegistry := providers.Lookup(id)
	pc, isCustom := resolved.Config.Providers[id]
	if !isRegistry && !isCustom {
		fmt.Fprintf(stderr, "ink auth set: unknown provider %q; `ink providers` lists them\n", id)
		return exitError
	}
	name := id
	if isRegistry {
		name = entry.ID // canonical id, aliases collapse
		warnOptInRisk(stderr, entry)
	}

	value, err := readSecret(stdin, stderr, name)
	if err != nil {
		return fail(stderr, "auth set", exitError, err)
	}
	out := redact.New()
	out.AddLiteral(value)
	resolver := auth.NewResolver(out, envLookupOK(environ), auth.WithGetenv(envLookup(environ)), auth.WithWarnf(func(format string, a ...any) {
		fmt.Fprintf(stderr, "warning: "+format+"\n", a...)
	}))
	if err := resolver.StoreSet(name, value); err != nil {
		fmt.Fprintf(stderr, "ink auth set: %v\n", out.Redact(err.Error()))
		return exitError
	}
	storePath, _ := config.StateFilePath(envLookup(environ), "secrets.enc")
	fmt.Fprintf(stdout, "stored credential for %s in the encrypted secret store (%s)\n", name, storePath)

	if check {
		if err := riskOptInErr(entry, pc); err != nil {
			fmt.Fprintf(stderr, "warning: probe skipped: %v\n", err)
			return exitOK
		}
		wire, baseURL := probeTarget(isRegistry, entry, pc, envLookupOK(environ))
		cred := auth.NewCredential(out, value)
		h := models.Probe(context.Background(), nil, wire, baseURL, cred, time.Now())
		cred.Zero()
		line := string(h.State)
		if h.Reason != "" {
			line += " (" + h.Reason + ")"
		}
		fmt.Fprintf(stdout, "health: %s\n", out.Redact(line))
	}
	return exitOK
}

// readSecret takes the credential from a hidden terminal prompt, or one
// line of stdin when stdin is not a terminal (pipes, tests) — never argv.
func readSecret(stdin io.Reader, stderr io.Writer, name string) (string, error) {
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprintf(stderr, "enter credential for %s (input hidden): ", name)
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(stderr)
		if err != nil {
			return "", fmt.Errorf("read credential: %w", err)
		}
		return validSecret(string(b))
	}
	fmt.Fprintln(stderr, "warning: stdin is not a terminal; reading the credential from the first line of stdin")
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read credential: %w", err)
	}
	return validSecret(line)
}

func validSecret(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("empty credential")
	}
	return s, nil
}

// warnOptInRisk warns about third-party auth flows the
// vendor's terms prohibit. Storing a key is allowed; using such a provider
// still requires the explicit config opt-in.
func warnOptInRisk(stderr io.Writer, entry providers.Entry) {
	if !entry.Auth.OptInRisk {
		return
	}
	fmt.Fprintf(stderr, "warning: %s uses a third-party auth flow its vendor's terms prohibit; the account may be suspended. Using it requires providers.%s.accept_third_party_oauth_risk = true in your user config.\n", entry.ID, entry.ID)
}

// probeTarget picks the wire and base URL that authSet --check probes.
func probeTarget(isRegistry bool, entry providers.Entry, pc config.ProviderConfig, lookup func(string) (string, bool)) (string, string) {
	if isRegistry {
		return entry.Wire, registryBaseURL(entry, pc.BaseURL, lookup)
	}
	return customWire(pc), pc.BaseURL
}

// authStatus lists which providers have a resolvable credential: every
// configured provider, plus registry providers whose credential resolves
// from the environment or the secret store. Values are never printed.
func authStatus(args []string, stdout, stderr io.Writer, environ []string, getwd func() (string, error)) int {
	var g globalFlags
	fs := flag.NewFlagSet("auth status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	g.bind(fs)
	positional, err := parseInterleaved(fs, args)
	if err != nil || len(positional) != 0 {
		fmt.Fprintln(stderr, authUsage)
		return exitUsage
	}
	opts, err := g.options(environ, getwd, stderr)
	if err != nil {
		return fail(stderr, "auth status", exitUsage, err)
	}
	resolved, err := config.Load(opts)
	if err != nil {
		return fail(stderr, "auth status", exitError, err)
	}
	warnDropped(stderr, resolved)

	out := redact.New()
	resolver := auth.NewResolver(out, envLookupOK(environ), auth.WithGetenv(envLookup(environ)), auth.WithWarnf(func(string, ...any) {}))
	ctx := context.Background()

	var b strings.Builder
	w := tabwriter.NewWriter(&b, 2, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PROVIDER\tAUTH\tSTATUS")
	shown := 0
	for _, r := range providerRows(resolved.Config) {
		status, configured := credentialStatus(ctx, r, resolver)
		if !configured && status != "present" {
			continue // an unconfigured registry row with nothing set is noise
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", r.id, orDash(r.authKind), status)
		shown++
	}
	if err := w.Flush(); err != nil {
		return fail(stderr, "auth status", exitError, err)
	}
	if shown == 0 {
		fmt.Fprintln(stdout, "no credentials configured; run `ink auth set <provider>`")
		return exitOK
	}
	fmt.Fprint(stdout, out.Redact(b.String()))
	return exitOK
}

// credentialStatus resolves one row's credential and reports
// present/absent — never the value; anything resolved is zeroed here.
func credentialStatus(ctx context.Context, r providerRow, resolver *auth.Resolver) (status string, configured bool) {
	resolve := func() (*auth.Credential, error) {
		if r.registry {
			return resolver.ForProvider(ctx, r.entry, r.override)
		}
		if r.custom.Auth == nil {
			return nil, nil
		}
		return resolver.Resolve(ctx, *r.custom.Auth)
	}
	configured = !r.registry || r.override != nil
	cred, err := resolve()
	if cred != nil {
		cred.Zero()
		return "present", configured
	}
	if err == nil {
		return "absent", configured
	}
	var missing *auth.ErrNoCredential
	if errors.As(err, &missing) {
		return "absent", configured
	}
	if errors.Is(err, core.NotImplementedError{}) {
		return "unavailable", configured
	}
	return "error: " + err.Error(), configured
}

// authLogin runs the OAuth authorization-code + PKCE flow for a registry
// provider and stores the token set in the encrypted secret store. Kinds
// that are not implemented (device code, vendor CLI reuse) stay honestly
// NotImplemented; nothing is faked.
func authLogin(args []string, stdout, stderr io.Writer, stdin io.Reader, environ []string, getwd func() (string, error)) int {
	var g globalFlags
	var noBrowser bool
	fs := flag.NewFlagSet("auth login", flag.ContinueOnError)
	fs.SetOutput(stderr)
	g.bind(fs)
	fs.BoolVar(&noBrowser, "no-browser", false, "print the authorize URL and read the pasted code instead of opening a browser")
	positional, err := parseInterleaved(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(positional) != 1 {
		fmt.Fprintln(stderr, authUsage)
		return exitUsage
	}
	opts, err := g.options(environ, getwd, stderr)
	if err != nil {
		return fail(stderr, "auth login", exitUsage, err)
	}
	resolved, err := config.Load(opts)
	if err != nil {
		return fail(stderr, "auth login", exitError, err)
	}
	warnDropped(stderr, resolved)

	entry, ok := providers.Lookup(positional[0])
	if !ok {
		fmt.Fprintf(stderr, "ink auth login: unknown provider %q; `ink providers` lists them\n", positional[0])
		return exitError
	}
	warnOptInRisk(stderr, entry)
	switch entry.Auth.Kind {
	case providers.AuthOAuth2PKCE, providers.AuthOAuth2Device:
	case providers.AuthExternalCLI:
		// anthropic-oauth carries recorded PKCE endpoints and logs in like
		// any oauth2_pkce provider; endpoint-less entries (copilot-acp)
		// stay safely unavailable.
		if entry.OAuth.AuthURL == "" {
			nerr := core.NotImplementedError{Feature: "auth login (" + entry.Auth.Kind + ") for " + entry.ID}
			return fail(stderr, "auth login", exitNotImplemented, nerr)
		}
	default:
		fmt.Fprintf(stderr, "ink auth login: %s uses auth kind %q, not an OAuth login; `ink auth set %s` stores its key\n", entry.ID, entry.Auth.Kind, entry.ID)
		return exitError
	}

	var pc *config.ProviderConfig
	if p, ok := resolved.Config.Providers[entry.ID]; ok {
		p := p
		pc = &p
	}
	out := redact.New()
	resolver := auth.NewResolver(out, envLookupOK(environ), auth.WithGetenv(envLookup(environ)), auth.WithWarnf(func(format string, a ...any) {
		fmt.Fprintf(stderr, "warning: "+format+"\n", a...)
	}))
	o := auth.LoginOptions{NoBrowser: noBrowser, Stdin: stdin, Out: stderr}
	login := resolver.LoginPKCE
	if entry.Auth.Kind == providers.AuthOAuth2Device {
		login = resolver.LoginDevice
	}
	if err := login(context.Background(), entry.ID, auth.MergedOAuth(entry, pc), o); err != nil {
		fmt.Fprintf(stderr, "ink auth login: %s\n", out.Redact(err.Error()))
		return exitError
	}
	storePath, _ := config.StateFilePath(envLookup(environ), "secrets.enc")
	fmt.Fprintf(stdout, "logged in to %s; tokens stored in the encrypted secret store (%s)\n", entry.ID, storePath)
	return exitOK
}

// authLogout deletes the provider's stored OAuth token set. It needs no
// configuration: only local state changes, and a missing set is a clean no-op.
func authLogout(args []string, stdout, stderr io.Writer, environ []string) int {
	var g globalFlags
	fs := flag.NewFlagSet("auth logout", flag.ContinueOnError)
	fs.SetOutput(stderr)
	g.bind(fs)
	positional, err := parseInterleaved(fs, args)
	if err != nil || len(positional) != 1 {
		fmt.Fprintln(stderr, authUsage)
		return exitUsage
	}
	id := positional[0]
	if entry, ok := providers.Lookup(id); ok {
		id = entry.ID // canonical id, aliases collapse
	}
	out := redact.New()
	resolver := auth.NewResolver(out, envLookupOK(environ), auth.WithGetenv(envLookup(environ)), auth.WithWarnf(func(format string, a ...any) {
		fmt.Fprintf(stderr, "warning: "+format+"\n", a...)
	}))
	found, err := resolver.Logout(id)
	if err != nil {
		fmt.Fprintf(stderr, "ink auth logout: %s\n", out.Redact(err.Error()))
		return exitError
	}
	if !found {
		fmt.Fprintf(stdout, "no stored login for %s\n", id)
		return exitOK
	}
	fmt.Fprintf(stdout, "removed stored login for %s\n", id)
	return exitOK
}
