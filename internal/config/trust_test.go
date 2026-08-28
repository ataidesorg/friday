package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ataidesorg/friday/internal/core"
)

func TestTrustStatePath(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	cases := []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"FRIDAY_STATE_DIR": "/explicit", "XDG_STATE_HOME": "/xdg", "HOME": "/home"}, filepath.Join("/explicit", "trust.toml")},
		{map[string]string{"XDG_STATE_HOME": "/xdg", "HOME": "/home"}, filepath.Join("/xdg", "friday", "trust.toml")},
		{map[string]string{"HOME": "/home"}, filepath.Join("/home", ".local", "state", "friday", "trust.toml")},
	}
	for _, c := range cases {
		got, err := TrustStatePath(env(c.env))
		if err != nil || got != c.want {
			t.Errorf("TrustStatePath(%v) = %q, %v", c.env, got, err)
		}
	}
	if _, err := TrustStatePath(env(nil)); !errors.Is(err, core.ErrInvalidInput) {
		t.Errorf("no env: %v", err)
	}
}

func TestTrustStoreRoundTrip(t *testing.T) {
	s := TrustStore{Path: filepath.Join(t.TempDir(), "state", "trust.toml")}
	if entries, err := s.List(); err != nil || len(entries) != 0 {
		t.Fatalf("missing file: %v %v", entries, err)
	}
	if _, found, err := s.Lookup("/nowhere"); err != nil || found {
		t.Fatalf("lookup on missing file: found=%v err=%v", found, err)
	}
	if err := s.Revoke("/nowhere"); err != nil {
		t.Fatalf("revoke missing: %v", err)
	}
	at := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	a := TrustEntry{Path: "/repo/a/.friday/config.toml", SHA256: fileSHA256([]byte("a")), Decision: TrustTrusted, DecidedAt: at}
	b := TrustEntry{Path: "/repo/b/.friday/config.toml", SHA256: fileSHA256([]byte("b")), Decision: TrustUntrusted, DecidedAt: at.Add(time.Hour)}
	for _, e := range []TrustEntry{a, b} {
		if err := s.Record(e); err != nil {
			t.Fatal(err)
		}
	}
	got, found, err := s.Lookup(a.Path)
	if err != nil || !found || !reflect.DeepEqual(got, a) {
		t.Fatalf("lookup a: %+v %v %v", got, found, err)
	}
	// Re-recording the same path replaces, never duplicates.
	a2 := a
	a2.SHA256 = fileSHA256([]byte("a-changed"))
	if err := s.Record(a2); err != nil {
		t.Fatal(err)
	}
	entries, err := s.List()
	if err != nil || len(entries) != 2 || entries[0] != a2 || entries[1] != b {
		t.Fatalf("list: %+v %v", entries, err)
	}
	if err := s.Revoke(a.Path); err != nil {
		t.Fatal(err)
	}
	if entries, _ := s.List(); len(entries) != 1 || entries[0] != b {
		t.Fatalf("after revoke: %+v", entries)
	}
	if info, err := os.Stat(s.Path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("state file mode: %v %v", info, err)
	}
	for _, bad := range []TrustEntry{{Path: "", SHA256: a.SHA256, Decision: TrustTrusted}, {Path: "rel/path", SHA256: a.SHA256, Decision: TrustTrusted}, {Path: "/x", SHA256: "", Decision: TrustTrusted}, {Path: "/x", SHA256: a.SHA256, Decision: "maybe"}} {
		if err := s.Record(bad); !errors.Is(err, core.ErrInvalidInput) {
			t.Errorf("Record(%+v) = %v", bad, err)
		}
	}
}

func TestTrustStoreRecordIsAtomic(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a read-only directory the process cannot write")
	}
	dir := t.TempDir()
	s := TrustStore{Path: filepath.Join(dir, "trust.toml")}
	first := TrustEntry{Path: "/repo/.friday/config.toml", SHA256: fileSHA256([]byte("x")), Decision: TrustTrusted, DecidedAt: time.Unix(0, 0).UTC()}
	if err := s.Record(first); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(s.Path)
	if err := os.Chmod(dir, 0o500); err != nil { //nolint:gosec // directory made read-only to force a write failure
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) //nolint:gosec // restore so cleanup can delete it
	second := first
	second.Decision = TrustUntrusted
	if err := s.Record(second); err == nil {
		t.Fatal("write into read-only dir must fail")
	}
	after, _ := os.ReadFile(s.Path)
	if string(after) != string(before) {
		t.Fatalf("partial write: %q", after)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, "*.tmp*")); len(leftovers) != 0 {
		t.Fatalf("temp files left behind: %v", leftovers)
	}
}

func TestTrustStoreRejectsCorruptFile(t *testing.T) {
	s := TrustStore{Path: filepath.Join(t.TempDir(), "trust.toml")}
	write(t, s.Path, "[[entry]\n")
	if _, err := s.List(); err == nil || !strings.Contains(err.Error(), s.Path) {
		t.Fatalf("corrupt file must name the path: %v", err)
	}
}
