package fsutil

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ataidesorg/friday/internal/core"
)

func TestConfine(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "leak.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"..", "../x", "/etc/passwd", "escape/secret.txt", "leak.txt", "sub/../../x", "escape/new.txt"} {
		if _, err := Confine(root, p); !errors.Is(err, ErrOutside) || !errors.Is(err, core.ErrInvalidInput) {
			t.Errorf("Confine(%q) = %v, want ErrOutside", p, err)
		}
	}
	realRoot, _ := filepath.EvalSymlinks(root)
	for p, want := range map[string]string{"": "", ".": "", "sub": "sub", "sub/new/file.go": "sub/new/file.go", "./sub/../a.txt": "a.txt"} {
		got, err := Confine(root, p)
		if err != nil {
			t.Errorf("Confine(%q): %v", p, err)
			continue
		}
		if got != filepath.Join(realRoot, want) {
			t.Errorf("Confine(%q) = %q, want %q", p, got, filepath.Join(realRoot, want))
		}
	}
	if _, err := Confine("relative/root", "x"); !errors.Is(err, core.ErrInvalidInput) {
		t.Errorf("relative root: %v", err)
	}
	if _, err := Confine(filepath.Join(root, "missing-root"), "x"); !errors.Is(err, core.ErrInvalidInput) {
		t.Errorf("missing root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Confine(root, "file/child"); !errors.Is(err, core.ErrInvalidInput) {
		t.Errorf("path through a file: %v", err)
	}
}

func TestCopyTree(t *testing.T) {
	src := t.TempDir()
	for p, data := range map[string]string{"a.go": "package a", "sub/b.txt": "b", "node_modules/x.js": "x", "debug.log": "log"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(src, p)), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(src, p), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(src, "a.go"), 0o755); err != nil { //nolint:gosec // exercising mode preservation
		t.Fatal(err)
	}
	if err := os.Symlink("a.go", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "copy")
	if err := CopyTree(context.Background(), src, dst, []string{"node_modules/**", "*.log"}); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "sub", "b.txt")); err != nil || string(b) != "b" { //nolint:gosec // test reads its own temp dir
		t.Errorf("sub/b.txt: %q %v", b, err)
	}
	if st, err := os.Stat(filepath.Join(dst, "a.go")); err != nil || st.Mode().Perm() != 0o755 {
		t.Errorf("a.go mode: %v %v", st, err)
	}
	if l, err := os.Readlink(filepath.Join(dst, "link")); err != nil || l != "a.go" {
		t.Errorf("link: %q %v", l, err)
	}
	for _, gone := range []string{"node_modules", "debug.log"} {
		if _, err := os.Lstat(filepath.Join(dst, gone)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s should be excluded: %v", gone, err)
		}
	}
	if err := CopyTree(context.Background(), src, dst, nil); !errors.Is(err, os.ErrExist) {
		t.Errorf("existing dst: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := CopyTree(ctx, src, filepath.Join(t.TempDir(), "c"), nil); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
	if err := CopyTree(context.Background(), filepath.Join(src, "missing"), filepath.Join(t.TempDir(), "m"), nil); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing src: %v", err)
	}
}

func TestExcluded(t *testing.T) {
	pats := []string{"node_modules/**", "*.log", "build"}
	for rel, want := range map[string]bool{
		"node_modules": true, "node_modules/x/y.js": true, "debug.log": true, "sub/debug.log": false,
		"build": true, "build/out": true, "main.go": false, "nodemodules": false,
	} {
		if got := Excluded(pats, rel); got != want {
			t.Errorf("Excluded(%q) = %v", rel, got)
		}
	}
}
