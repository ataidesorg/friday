package workspace_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ataidesorg/friday/internal/core"
	"github.com/ataidesorg/friday/internal/workspace"
)

func prepWT(t *testing.T, root, dir, name string) (core.Workspace, workspace.Cleanup, error) {
	t.Helper()
	return workspace.Prepare(context.Background(), workspace.Options{
		Root: root, Mode: workspace.ModeWorktree, Project: core.NewProjectID(),
		Worktree: name, WorktreeDir: dir,
	})
}

func TestWorktreeCreateSelectCleanup(t *testing.T) {
	root := repo(t)
	wtDir := t.TempDir()

	ws, cleanup, err := prepWT(t, root, wtDir, "api")
	if err != nil {
		t.Fatal(err)
	}
	if ws.Kind != core.WorkspaceWorktree {
		t.Fatalf("kind %q, want worktree", ws.Kind)
	}
	if ws.Branch != "friday/api" {
		t.Fatalf("branch %q, want friday/api", ws.Branch)
	}
	if read(t, ws.Root, "main.go") == "" {
		t.Fatal("worktree misses the committed tree")
	}

	// Commit inside the worktree; a clean worktree is removed on cleanup and
	// the branch (with the commit) survives in the main repository.
	write(t, ws.Root, "feature.go", "package main\n")
	git(t, ws.Root, "add", "feature.go")
	git(t, ws.Root, "commit", "-q", "-m", "feature")
	if err := cleanup(context.Background(), false); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, err := os.Stat(ws.Root); !os.IsNotExist(err) {
		t.Fatalf("clean worktree not removed: %v", err)
	}
	if out := git(t, root, "branch", "--list", "friday/api"); !strings.Contains(out, "friday/api") {
		t.Fatalf("branch gone after cleanup: %q", out)
	}

	// Re-opening the same name resumes the surviving branch: the committed
	// file is there without any copy of uncommitted state.
	ws2, cleanup2, err := prepWT(t, root, wtDir, "api")
	if err != nil {
		t.Fatal(err)
	}
	if read(t, ws2.Root, "feature.go") == "" {
		t.Fatal("resumed worktree misses the branch commit")
	}
	if err := cleanup2(context.Background(), false); err != nil {
		t.Fatal(err)
	}
}

func TestWorktreeDirtyKeptAndSelected(t *testing.T) {
	root := repo(t)
	wtDir := t.TempDir()
	ws, cleanup, err := prepWT(t, root, wtDir, "wip")
	if err != nil {
		t.Fatal(err)
	}
	write(t, ws.Root, "draft.txt", "uncommitted\n")
	if err := cleanup(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ws.Root, "draft.txt")); err != nil {
		t.Fatalf("dirty worktree was removed: %v", err)
	}
	// A second session selects the existing worktree, uncommitted work intact.
	ws2, _, err := prepWT(t, root, wtDir, "wip")
	if err != nil {
		t.Fatal(err)
	}
	if ws2.Root != ws.Root {
		t.Fatalf("select returned %s, want %s", ws2.Root, ws.Root)
	}
	if read(t, ws2.Root, "draft.txt") != "uncommitted\n" {
		t.Fatal("uncommitted work lost on select")
	}
}

func TestWorktreeParallelSessionsDoNotCollide(t *testing.T) {
	root := repo(t)
	wtDir := t.TempDir()
	a, _, err := prepWT(t, root, wtDir, "one")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := prepWT(t, root, wtDir, "two")
	if err != nil {
		t.Fatal(err)
	}
	if a.Root == b.Root || a.Branch == b.Branch {
		t.Fatalf("sessions collide: %s/%s vs %s/%s", a.Root, a.Branch, b.Root, b.Branch)
	}
	write(t, a.Root, "main.go", "package one\n")
	if got := read(t, b.Root, "main.go"); strings.Contains(got, "one") {
		t.Fatal("write in one worktree leaked into the other")
	}
}

func TestWorktreeErrors(t *testing.T) {
	ctx := context.Background()
	plain := t.TempDir()
	if _, _, err := prepWT(t, plain, t.TempDir(), "x"); err == nil {
		t.Fatal("non-git root accepted")
	}
	root := repo(t)
	if _, _, err := prepWT(t, root, t.TempDir(), "a/b"); err == nil {
		t.Fatal("path separator in name accepted")
	}
	if _, _, err := prepWT(t, root, t.TempDir(), ""); err == nil {
		t.Fatal("empty name accepted")
	}
	if _, _, err := workspace.Prepare(ctx, workspace.Options{Root: root, Mode: workspace.ModeWorktree, Worktree: "ok"}); err == nil {
		t.Fatal("missing WorktreeDir accepted")
	}
	// A foreign directory squatting on the worktree path is a conflict.
	wtDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wtDir, "taken"), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepWT(t, root, wtDir, "taken"); err == nil {
		t.Fatal("non-worktree directory accepted")
	}
}
