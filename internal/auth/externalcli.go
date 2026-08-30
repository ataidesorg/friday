package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ataidesorg/ink/internal/config"
	"github.com/ataidesorg/ink/internal/core"
	"github.com/ataidesorg/ink/internal/providers"
)

// claudeKeychainService is the macOS keychain item Claude Code writes its
// OAuth token set into.
const claudeKeychainService = "Claude Code-credentials"

// claudeCredentials is the JSON shape Claude Code stores, in the keychain
// item and in ~/.claude/.credentials.json alike.
type claudeCredentials struct {
	ClaudeAiOauth struct {
		AccessToken string `json:"accessToken"`
	} `json:"claudeAiOauth"`
}

// resolveExternalCLI is the ForProvider branch for auth kind external_cli:
// reuse a credential another tool on this machine already holds. Only
// anthropic-oauth is wired; copilot-acp stays safely unavailable.
func (r *Resolver) resolveExternalCLI(ctx context.Context, entry providers.Entry, cfg *config.ProviderConfig) (*Credential, error) {
	if entry.ID != "anthropic-oauth" {
		return nil, core.NotImplementedError{Feature: "external CLI auth for " + entry.ID}
	}
	return r.resolveClaudeCode(ctx, entry, cfg)
}

// resolveClaudeCode finds a usable Anthropic OAuth token: env overrides,
// Ink's own oauth store (populated by `ink auth login`), the Claude
// Code keychain item, then ~/.claude/.credentials.json. Every hit is
// registered with the redactor and held in memory only; nothing is copied
// into Ink's store.
func (r *Resolver) resolveClaudeCode(ctx context.Context, entry providers.Entry, cfg *config.ProviderConfig) (*Credential, error) {
	for _, name := range entry.Auth.EnvNames {
		if v, ok := r.environ(name); ok && v != "" {
			return r.credential(v), nil
		}
	}
	cred, err := r.resolveOAuth2(ctx, entry, cfg)
	var miss *ErrNoCredential
	if err == nil || !errors.As(err, &miss) {
		return cred, err // hit, or a real failure (corrupt store, dead refresh)
	}
	if v, found, err := r.claudeKeychain(ctx); err != nil {
		return nil, err // malformed JSON fails closed, never a partial token
	} else if found {
		return r.credential(v), nil
	}
	if v, found, err := r.claudeCredentialsFile(); err != nil {
		return nil, err
	} else if found {
		return r.credential(v), nil
	}
	where := fmt.Sprintf("env %v, secret store %s%s, keychain %q, or ~/.claude/.credentials.json",
		entry.Auth.EnvNames, oauthStorePrefix, entry.ID, claudeKeychainService)
	return nil, &ErrNoCredential{Source: "external_cli", Where: where,
		Hint: "run `ink auth login " + entry.ID + "`, sign in to Claude Code, or export " + entry.Auth.EnvNames[0]}
}

// claudeKeychain reads Claude Code's keychain item. An unavailable keyring
// or a missing item is a clean miss; a present item that does not parse
// fails closed.
func (r *Resolver) claudeKeychain(ctx context.Context) (string, bool, error) {
	v, found, err := r.keyring(ctx, claudeKeychainService, "")
	if errors.Is(err, ErrKeyringUnavailable) || (err == nil && !found) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("claude code keychain item %q: %w", claudeKeychainService, err)
	}
	tok, err := parseClaudeCredentials([]byte(v))
	if err != nil {
		return "", false, fmt.Errorf("claude code keychain item %q: %w", claudeKeychainService, err)
	}
	return tok, true, nil
}

// claudeCredentialsFile reads ~/.claude/.credentials.json. A missing home
// or file is a clean miss; an unreadable or malformed file fails closed.
func (r *Resolver) claudeCredentialsFile() (string, bool, error) {
	home := r.getenv("HOME")
	if home == "" {
		return "", false, nil
	}
	path := filepath.Join(home, ".claude", ".credentials.json")
	raw, err := os.ReadFile(path) //nolint:gosec // fixed well-known path under $HOME
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("claude code credentials file: %w", err)
	}
	tok, err := parseClaudeCredentials(raw)
	if err != nil {
		return "", false, fmt.Errorf("claude code credentials file %s: %w", path, err)
	}
	return tok, true, nil
}

// parseClaudeCredentials extracts claudeAiOauth.accessToken. The raw
// content never appears in errors: it may carry secrets.
func parseClaudeCredentials(raw []byte) (string, error) {
	var cc claudeCredentials
	if err := json.Unmarshal(raw, &cc); err != nil {
		return "", errors.New("malformed JSON; sign in to Claude Code again or use `ink auth login anthropic-oauth`")
	}
	if cc.ClaudeAiOauth.AccessToken == "" {
		return "", errors.New("no claudeAiOauth.accessToken in the credential store; sign in to Claude Code again")
	}
	return cc.ClaudeAiOauth.AccessToken, nil
}

// ghAuthToken asks the GitHub CLI for its stored OAuth token. A missing
// binary, a failed run, or empty output is a clean miss, never a crash.
func (r *Resolver) ghAuthToken(ctx context.Context) (string, bool) {
	out, err := r.exec(ctx, []string{"gh", "auth", "token"}, "")
	if err != nil {
		return "", false
	}
	v := trimEOL(string(out))
	return v, v != ""
}
