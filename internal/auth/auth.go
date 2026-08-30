// Package auth resolves provider credentials at call time. Sources: process
// environment, OS keyring, an encrypted secret store, or a user-configured
// command. Every resolved token is registered with the redactor the moment
// it exists, held in memory only, and wiped with Credential.Zero. Nothing
// here writes a credential to config, logs, or disk in the clear.
package auth

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/ataidesorg/ink/internal/config"
	"github.com/ataidesorg/ink/internal/core"
	"github.com/ataidesorg/ink/internal/providers"
)

// Registrar receives secret literals for redaction; *redact.Redactor
// satisfies it.
type Registrar interface {
	AddLiteral(literals ...string)
}

// ErrNoCredential says where Ink looked and how to fix it — never the
// value.
type ErrNoCredential struct {
	Source string // env | keyring | secret_store | command | oauth
	Where  string // env names, keyring account, or store key looked at
	Hint   string // fix instruction; empty means the key-based default
}

func (e *ErrNoCredential) Error() string {
	hint := e.Hint
	if hint == "" {
		hint = "run `ink auth set <provider>` or export the key"
	}
	return fmt.Sprintf("no credential in %s (%s); %s", e.Source, e.Where, hint)
}

// execFunc runs argv with stdin and returns stdout. The seam exists so tests
// never spawn real processes and so keyring writes can pass secrets via
// stdin, never argv.
type execFunc func(ctx context.Context, argv []string, stdin string) ([]byte, error)

// keyringLookupFunc reads one keyring item. found=false with a nil error is
// a clean miss; ErrKeyringUnavailable triggers the secret-store fallback.
type keyringLookupFunc func(ctx context.Context, service, account string) (value string, found bool, err error)

// Resolver dispatches config.AuthRef sources to credentials.
type Resolver struct {
	register Registrar
	environ  func(string) (string, bool)
	getenv   func(string) string // state dir resolution only
	exec     execFunc
	keyring  keyringLookupFunc
	warnf    func(format string, args ...any)
	http     *http.Client
	now      func() time.Time
	sleep    func(ctx context.Context, d time.Duration) error

	// bearers holds short-lived exchanged API bearers (Copilot), keyed by
	// provider id. Memory only, never persisted.
	bearerMu sync.Mutex
	bearers  map[string]tokenSet
}

// Option overrides one Resolver seam (tests, or callers with their own
// logging).
type Option func(*Resolver)

// WithGetenv sets the state-directory lookup (INK_STATE_DIR et al).
func WithGetenv(getenv func(string) string) Option {
	return func(r *Resolver) { r.getenv = getenv }
}

// WithExec replaces process execution.
func WithExec(f execFunc) Option { return func(r *Resolver) { r.exec = f } }

// WithKeyringLookup replaces the OS keyring read.
func WithKeyringLookup(f keyringLookupFunc) Option {
	return func(r *Resolver) { r.keyring = f }
}

// WithWarnf replaces the warning sink (default: stderr). Warnings never
// carry credential values.
func WithWarnf(f func(format string, args ...any)) Option {
	return func(r *Resolver) { r.warnf = f }
}

// WithHTTPClient replaces the HTTP client OAuth flows use (tests).
func WithHTTPClient(c *http.Client) Option {
	return func(r *Resolver) { r.http = c }
}

// WithNow replaces the clock (token-expiry tests).
func WithNow(f func() time.Time) Option {
	return func(r *Resolver) { r.now = f }
}

// WithSleep replaces the poll-wait sleeper (device-flow tests).
func WithSleep(f func(ctx context.Context, d time.Duration) error) Option {
	return func(r *Resolver) { r.sleep = f }
}

// NewResolver builds a Resolver. register receives every resolved literal;
// environ reads credential env vars (nil reads none).
func NewResolver(register Registrar, environ func(string) (string, bool), opts ...Option) *Resolver {
	r := &Resolver{
		register: register,
		environ:  environ,
		getenv:   os.Getenv,
		warnf: func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "warning: "+format+"\n", args...)
		},
	}
	r.exec = defaultExec
	r.keyring = r.osKeyringLookup
	r.http = &http.Client{Timeout: 30 * time.Second}
	r.now = time.Now
	r.sleep = sleepCtx
	r.bearers = map[string]tokenSet{}
	for _, opt := range opts {
		opt(r)
	}
	if r.environ == nil {
		r.environ = func(string) (string, bool) { return "", false }
	}
	return r
}

func (r *Resolver) credential(value string) *Credential {
	r.register.AddLiteral(value)
	return newCredential([]byte(value))
}

// Resolve turns an explicit AuthRef into a credential.
func (r *Resolver) Resolve(ctx context.Context, ref config.AuthRef) (*Credential, error) {
	switch ref.Source {
	case "env":
		return r.resolveEnv(ref)
	case "keyring":
		return r.resolveKeyring(ctx, ref)
	case "secret_store":
		return r.resolveStore(storeKey(ref))
	case "command":
		return r.resolveCommand(ctx, ref)
	default:
		return nil, fmt.Errorf("%w: unknown auth source %q", core.ErrInvalidInput, ref.Source)
	}
}

// ForProvider resolves a registry provider's credential: an explicit
// override wins; otherwise the registry entry's auth spec decides. Keyless
// providers (and optional keys with nothing set) return (nil, nil).
func (r *Resolver) ForProvider(ctx context.Context, entry providers.Entry, cfg *config.ProviderConfig) (*Credential, error) {
	if cfg != nil && cfg.Auth != nil {
		return r.Resolve(ctx, *cfg.Auth)
	}
	switch entry.Auth.Kind {
	case providers.AuthNone:
		return nil, nil
	case providers.AuthKey:
		for _, name := range entry.Auth.EnvNames {
			if v, ok := r.environ(name); ok && v != "" {
				return r.credential(v), nil
			}
		}
		v, found, err := r.storeGet(entry.ID)
		if err != nil {
			return nil, err // corrupt store fails closed, never keyless
		}
		if found {
			return r.credential(v), nil
		}
		if entry.Auth.Cloud == providers.CloudAzure {
			// azure-foundry: key first, then the Entra client-secret grant.
			return r.AzureBearer(ctx)
		}
		if entry.Auth.Optional {
			return nil, nil
		}
		where := fmt.Sprintf("env %v or secret store %q", entry.Auth.EnvNames, entry.ID)
		return nil, &ErrNoCredential{Source: "env/secret_store", Where: where}
	case providers.AuthCloud:
		switch entry.Auth.Cloud {
		case providers.CloudGCP:
			return r.GCPBearer(ctx)
		case providers.CloudAWS:
			// Bedrock signs requests (SigV4); there is no bearer to hand
			// out. The run path resolves AWSCredentials and signs per
			// request instead of calling ForProvider.
			return nil, fmt.Errorf("provider %s uses AWS SigV4 request signing, not a bearer credential", entry.ID)
		default:
			return nil, fmt.Errorf("provider %s names unknown cloud family %q; failing closed", entry.ID, entry.Auth.Cloud)
		}
	case providers.AuthOAuth2PKCE:
		return r.resolveOAuth2(ctx, entry, cfg)
	case providers.AuthExternalCLI:
		return r.resolveExternalCLI(ctx, entry, cfg)
	case providers.AuthOAuth2Device:
		if MergedOAuth(entry, cfg).ExchangeURL != "" {
			return r.resolveExchanged(ctx, entry, cfg)
		}
		return r.resolveOAuth2(ctx, entry, cfg)
	default:
		return nil, core.NotImplementedError{Feature: "auth kind " + entry.Auth.Kind + " for " + entry.ID}
	}
}
