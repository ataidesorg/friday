package tools

import (
	"path/filepath"
	"testing"
)

func TestDisplayPath(t *testing.T) {
	root := t.TempDir()
	realRoot, _ := filepath.EvalSymlinks(root)
	if got := displayPath(root, filepath.Join(realRoot, "a", "b.go")); got != "a/b.go" {
		t.Fatalf("displayPath = %q", got)
	}
}
