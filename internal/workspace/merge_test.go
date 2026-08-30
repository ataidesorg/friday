package workspace_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ataidesorg/ink/internal/core"
	"github.com/ataidesorg/ink/internal/workspace"
)

func TestMergeBranchesClean(t *testing.T) {
	root := repo(t)
	wtDir := t.TempDir()
	api, apiClean, err := prepWT(t, root, wtDir, "api")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = apiClean(context.Background(), true) })
	write(t, api.Root, "api.go", "package api\n")
	git(t, api.Root, "add", "api.go")
	git(t, api.Root, "commit", "-q", "-m", "api")

	ui, uiClean, err := prepWT(t, root, wtDir, "ui")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = uiClean(context.Background(), true) })
	write(t, ui.Root, "ui.go", "package ui\n")
	git(t, ui.Root, "add", "ui.go")
	git(t, ui.Root, "commit", "-q", "-m", "ui")

	res, err := workspace.MergeBranches(context.Background(), root, []string{"ink/api", "ink/ui"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.StoppedAt != "" || len(res.Conflicts) != 0 {
		t.Fatalf("result %+v", res)
	}
	if read(t, root, "api.go") == "" || read(t, root, "ui.go") == "" {
		t.Fatal("merged tree missing files")
	}
}

func TestMergeBranchesConflict(t *testing.T) {
	root := repo(t)
	wtDir := t.TempDir()
	a, aClean, err := prepWT(t, root, wtDir, "a")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = aClean(context.Background(), true) })
	write(t, a.Root, "README.md", "# a\n")
	git(t, a.Root, "add", "README.md")
	git(t, a.Root, "commit", "-q", "-m", "a")

	b, bClean, err := prepWT(t, root, wtDir, "b")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bClean(context.Background(), true) })
	write(t, b.Root, "README.md", "# b\n")
	git(t, b.Root, "add", "README.md")
	git(t, b.Root, "commit", "-q", "-m", "b")

	res, err := workspace.MergeBranches(context.Background(), root, []string{"ink/a", "ink/b"})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK || res.StoppedAt != "ink/b" {
		t.Fatalf("want conflict on ink/b, got %+v", res)
	}
	if len(res.Merged) != 1 || res.Merged[0] != "ink/a" {
		t.Fatalf("merged %+v, want ink/a kept", res.Merged)
	}
	if len(res.Conflicts) != 1 || res.Conflicts[0] != "README.md" {
		t.Fatalf("conflicts %+v", res.Conflicts)
	}
	if read(t, root, "README.md") != "# a\n" {
		t.Fatalf("abort should keep the last clean merge: %q", read(t, root, "README.md"))
	}
	if _, err := os.Stat(filepath.Join(root, ".git", "MERGE_HEAD")); !os.IsNotExist(err) {
		t.Fatalf("merge left in progress: %v", err)
	}
}

func TestMergeBranchesRejects(t *testing.T) {
	root := repo(t)
	if _, err := workspace.MergeBranches(context.Background(), root, nil); !errors.Is(err, core.ErrInvalidInput) {
		t.Errorf("empty: %v", err)
	}
	if _, err := workspace.MergeBranches(context.Background(), root, []string{" "}); !errors.Is(err, core.ErrInvalidInput) {
		t.Errorf("blank: %v", err)
	}
	if _, err := workspace.MergeBranches(context.Background(), root, []string{"no-such-branch"}); !errors.Is(err, core.ErrUnavailable) {
		t.Errorf("missing: %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := workspace.MergeBranches(cancelled, root, []string{"main"}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
}
