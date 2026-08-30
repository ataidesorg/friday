package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/ataidesorg/ink/internal/core"
)

// TrustDecision is the owner's answer for one repository config file.
type TrustDecision string

// Trust decisions.
const (
	TrustTrusted   TrustDecision = "trusted"
	TrustUntrusted TrustDecision = "untrusted"
)

// TrustEntry binds a decision to the exact content (SHA-256) of one file.
type TrustEntry struct {
	Path      string        `toml:"path"`
	SHA256    string        `toml:"sha256"`
	Decision  TrustDecision `toml:"decision"`
	DecidedAt time.Time     `toml:"decided_at"`
}

// TrustStore is the TOML file holding every TrustEntry, keyed by absolute
// path. It lives in user state (TrustStatePath), never inside a repository.
type TrustStore struct {
	Path string
}

// TrustPrompt asks the owner whether to trust path, which sets keys under
// ProjectLayerGatedPrefixes. A nil prompt means no terminal is attached.
type TrustPrompt func(path string, keys []string) (TrustDecision, error)

type trustFile struct {
	Entries []TrustEntry `toml:"entry"`
}

const (
	trustFileName = "trust.toml"
	stateDirEnv   = "INK_STATE_DIR"
)

// StateFilePath resolves name inside the Ink state directory:
// $INK_STATE_DIR, then $XDG_STATE_HOME/ink, then
// $HOME/.local/state/ink.
func StateFilePath(getenv func(string) string, name string) (string, error) {
	if dir := getenv(stateDirEnv); dir != "" {
		return filepath.Join(dir, name), nil
	}
	if xdg := getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "ink", name), nil
	}
	if home := getenv("HOME"); home != "" {
		return filepath.Join(home, ".local", "state", "ink", name), nil
	}
	return "", fmt.Errorf("%w: none of %s, XDG_STATE_HOME, HOME is set", core.ErrInvalidInput, stateDirEnv)
}

// TrustStatePath resolves the trust file inside the state directory.
func TrustStatePath(getenv func(string) string) (string, error) {
	return StateFilePath(getenv, trustFileName)
}

func fileSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// List returns every entry, sorted by path. A missing file is an empty store.
func (s TrustStore) List() ([]TrustEntry, error) {
	data, err := os.ReadFile(s.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read trust store %s: %w", s.Path, err)
	}
	var f trustFile
	if _, err := toml.NewDecoder(bytes.NewReader(data)).Decode(&f); err != nil {
		return nil, fmt.Errorf("parse trust store %s: %w", s.Path, err)
	}
	slices.SortFunc(f.Entries, func(a, b TrustEntry) int { return compareStrings(a.Path, b.Path) })
	return f.Entries, nil
}

// Lookup finds the entry for an absolute path.
func (s TrustStore) Lookup(path string) (TrustEntry, bool, error) {
	entries, err := s.List()
	if err != nil {
		return TrustEntry{}, false, err
	}
	for _, e := range entries {
		if e.Path == path {
			return e, true, nil
		}
	}
	return TrustEntry{}, false, nil
}

// Trust records path (made absolute) as trusted at the content data, and
// returns the entry written.
func (s TrustStore) Trust(path string, data []byte, now time.Time) (TrustEntry, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return TrustEntry{}, fmt.Errorf("resolve %s: %w", path, err)
	}
	e := TrustEntry{Path: abs, SHA256: fileSHA256(data), Decision: TrustTrusted, DecidedAt: now.UTC()}
	if err := s.Record(e); err != nil {
		return TrustEntry{}, err
	}
	return e, nil
}

// GatedKeys lists the keys a repository config file sets that only trust can
// unlock: ProjectLayerGatedPrefixes and project.commands.*, minus the opt-in
// risk flags, which no repository file may set at all.
func GatedKeys(data []byte) ([]string, error) {
	var m map[string]any
	if _, err := toml.NewDecoder(bytes.NewReader(data)).Decode(&m); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	var keys []string
	for key := range flatten(m, "") {
		if !projectMaySet(key) && !isRiskFlag(key) {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys, nil
}

// Record adds or replaces the entry for e.Path.
func (s TrustStore) Record(e TrustEntry) error {
	switch {
	case e.Path == "" || !filepath.IsAbs(e.Path):
		return fmt.Errorf("%w: trust entry path must be absolute, got %q", core.ErrInvalidInput, e.Path)
	case e.SHA256 == "":
		return fmt.Errorf("%w: trust entry for %s has no hash", core.ErrInvalidInput, e.Path)
	case e.Decision != TrustTrusted && e.Decision != TrustUntrusted:
		return fmt.Errorf("%w: trust decision must be trusted or untrusted, got %q", core.ErrInvalidInput, e.Decision)
	}
	return s.rewrite(func(entries []TrustEntry) []TrustEntry {
		kept := slices.DeleteFunc(slices.Clone(entries), func(x TrustEntry) bool { return x.Path == e.Path })
		return append(kept, e)
	})
}

// Revoke removes the entry for path; a missing entry is not an error.
func (s TrustStore) Revoke(path string) error {
	return s.rewrite(func(entries []TrustEntry) []TrustEntry {
		return slices.DeleteFunc(slices.Clone(entries), func(x TrustEntry) bool { return x.Path == path })
	})
}

// rewrite replaces the whole file atomically: encode to a temp file in the
// same directory, fsync, rename. A failure leaves the previous file intact.
func (s TrustStore) rewrite(update func([]TrustEntry) []TrustEntry) error {
	entries, err := s.List()
	if err != nil {
		return err
	}
	next := update(entries)
	slices.SortFunc(next, func(a, b TrustEntry) int { return compareStrings(a.Path, b.Path) })
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(trustFile{Entries: next}); err != nil {
		return fmt.Errorf("encode trust store: %w", err)
	}
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create state dir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, trustFileName+".tmp-*")
	if err != nil {
		return fmt.Errorf("write trust store %s: %w", s.Path, err)
	}
	if err := writeAndClose(tmp, buf.Bytes()); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("write trust store %s: %w", s.Path, err)
	}
	if err := os.Rename(tmp.Name(), s.Path); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("replace trust store %s: %w", s.Path, err)
	}
	return nil
}

func writeAndClose(f *os.File, data []byte) error {
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
