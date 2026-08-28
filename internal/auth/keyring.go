package auth

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"

	"github.com/ataidesorg/friday/internal/config"
)

// ErrKeyringUnavailable means no usable OS keyring binary exists; keyring
// reads fall back to the encrypted secret store.
var ErrKeyringUnavailable = errors.New("os keyring unavailable")

// resolveKeyring reads service/account from the OS keyring, falling back to
// the secret store (with a warning) when no keyring exists on this machine.
func (r *Resolver) resolveKeyring(ctx context.Context, ref config.AuthRef) (*Credential, error) {
	service, account := ref.Service, ref.Account
	if service == "" {
		service = "friday"
	}
	if account == "" {
		account = ref.Name
	}
	v, found, err := r.keyring(ctx, service, account)
	switch {
	case errors.Is(err, ErrKeyringUnavailable):
		r.warnf("no OS keyring on this machine; falling back to the encrypted secret store for %s/%s", service, account)
		return r.resolveStore(account)
	case err != nil:
		return nil, fmt.Errorf("keyring lookup %s/%s: %w", service, account, err)
	case !found:
		return nil, &ErrNoCredential{Source: "keyring", Where: service + "/" + account}
	}
	return r.credential(v), nil
}

// osKeyringLookup shells out to the platform keyring reader: macOS
// `security find-generic-password -w`, Linux `secret-tool lookup`. A missing
// binary reports ErrKeyringUnavailable; a missing item is a clean miss.
// FRIDAY_NO_KEYRING=1 (any non-empty value) reports the keyring unavailable
// without spawning anything: headless machines and tests use it to keep the
// secret-store key in a file instead of the OS keychain.
func (r *Resolver) osKeyringLookup(ctx context.Context, service, account string) (string, bool, error) {
	if r.getenv("FRIDAY_NO_KEYRING") != "" {
		return "", false, ErrKeyringUnavailable
	}
	var argv []string
	switch runtime.GOOS {
	case "darwin":
		argv = []string{"security", "find-generic-password", "-s", service}
		if account != "" {
			argv = append(argv, "-a", account)
		}
		argv = append(argv, "-w")
	default:
		argv = []string{"secret-tool", "lookup", "service", service}
		if account != "" {
			argv = append(argv, "account", account)
		}
	}
	if _, err := exec.LookPath(argv[0]); err != nil {
		return "", false, ErrKeyringUnavailable
	}
	out, err := r.exec(ctx, argv, "")
	if err != nil {
		// Exit 44 (macOS) / 1 (secret-tool) = item not found. Output is
		// withheld from the error: it may carry secrets.
		return "", false, nil
	}
	v := trimEOL(string(out))
	if v == "" {
		return "", false, nil
	}
	return v, true, nil
}

// keyringStore writes service/account via stdin (macOS `security -i`, Linux
// `secret-tool store`), so the secret never appears in argv.
func (r *Resolver) keyringStore(ctx context.Context, service, account, value string) error {
	if r.getenv("FRIDAY_NO_KEYRING") != "" {
		return ErrKeyringUnavailable
	}
	switch runtime.GOOS {
	case "darwin":
		if _, err := exec.LookPath("security"); err != nil {
			return ErrKeyringUnavailable
		}
		line := fmt.Sprintf("add-generic-password -U -s %s -a %s -w %s\n", service, account, value)
		if _, err := r.exec(ctx, []string{"security", "-i"}, line); err != nil {
			return fmt.Errorf("keyring store %s/%s failed (output withheld)", service, account)
		}
		return nil
	default:
		if _, err := exec.LookPath("secret-tool"); err != nil {
			return ErrKeyringUnavailable
		}
		argv := []string{"secret-tool", "store", "--label=friday " + account, "service", service, "account", account}
		if _, err := r.exec(ctx, argv, value); err != nil {
			return fmt.Errorf("keyring store %s/%s failed (output withheld)", service, account)
		}
		return nil
	}
}

func trimEOL(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
