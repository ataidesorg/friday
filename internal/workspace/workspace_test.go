package workspace_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ataidesorg/ink/internal/core"
	"github.com/ataidesorg/ink/internal/workspace"
)

var ctx = context.Background()

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...) //nolint:gosec // test fixture; argv only
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func write(t *testing.T, dir, rel, data string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, rel)) //nolint:gosec // test reads its own temp dir
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// repo creates a committed git repository with main.go and README.md and a
// .gitignore for .ink/local, returning its symlink-resolved root.
func repo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "main.go", "package main\n")
	write(t, dir, "README.md", "# demo\n")
	write(t, dir, ".gitignore", ".ink/local/\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func installFailingHooks(t *testing.T, dir string) {
	t.Helper()
	hooks := filepath.Join(dir, ".git", "hooks")
	for _, h := range []string{"pre-commit", "post-checkout", "post-index-change"} {
		script := "#!/bin/sh\necho ran > \"$(git rev-parse --show-toplevel)/hook-ran\"\nexit 1\n"
		if err := os.WriteFile(filepath.Join(hooks, h), []byte(script), 0o755); err != nil { //nolint:gosec // test hook must be executable
			t.Fatal(err)
		}
	}
}

func TestStatus(t *testing.T) {
	dir := repo(t)
	vcs, err := workspace.Status(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if vcs.Kind != "git" || vcs.Branch != "main" || len(vcs.Head) != 40 || vcs.Dirty {
		t.Fatalf("clean status = %+v", vcs)
	}
	write(t, dir, "main.go", "package main // edited\n")
	if vcs, err = workspace.Status(ctx, dir); err != nil || !vcs.Dirty {
		t.Fatalf("dirty status = %+v, %v", vcs, err)
	}
	plain := t.TempDir()
	if vcs, err = workspace.Status(ctx, plain); err != nil || vcs.Kind != "none" || vcs.Dirty {
		t.Fatalf("plain dir status = %+v, %v", vcs, err)
	}
	if _, err := workspace.Status(ctx, filepath.Join(plain, "missing")); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("missing root: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := workspace.Status(cancelled, dir); err == nil {
		t.Fatal("cancelled context should fail")
	}
}

func TestPrepareAutoClean(t *testing.T) {
	dir := repo(t)
	ws, cleanup, err := workspace.Prepare(ctx, workspace.Options{Root: dir, Project: core.NewProjectID()})
	if err != nil {
		t.Fatal(err)
	}
	if ws.Kind != core.WorkspacePrimary || ws.Root != dir || ws.Branch != "main" || !core.ValidID(string(ws.ID)) {
		t.Fatalf("workspace = %+v", ws)
	}
	if err := cleanup(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "main.go")); err != nil {
		t.Fatalf("primary cleanup must not touch the checkout: %v", err)
	}
}

func TestPrepareAutoDirty(t *testing.T) {
	dir := repo(t)
	write(t, dir, "main.go", "package main // wip\n")
	write(t, dir, "scratch.txt", "untracked\n")
	write(t, dir, ".ink/local/runs/x/events.jsonl", "{}\n")
	before := git(t, dir, "status", "--porcelain")
	ws, cleanup, err := workspace.Prepare(ctx, workspace.Options{Root: dir, Mode: workspace.ModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	if ws.Kind != core.WorkspaceEphemeral || ws.Root == dir || strings.HasPrefix(ws.Root, dir) {
		t.Fatalf("workspace = %+v", ws)
	}
	for _, rel := range []string{"main.go", "scratch.txt", "README.md", ".gitignore"} {
		if read(t, ws.Root, rel) != read(t, dir, rel) {
			t.Errorf("%s differs in the copy", rel)
		}
	}
	if _, err := os.Stat(filepath.Join(ws.Root, ".ink", "local")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf(".ink/local should not be copied: %v", err)
	}
	if vcs, err := workspace.Status(ctx, ws.Root); err != nil || vcs.Kind != "git" || !vcs.Dirty {
		t.Fatalf("copy status = %+v, %v", vcs, err)
	}
	if after := git(t, dir, "status", "--porcelain"); after != before {
		t.Fatalf("primary status changed:\n%s\n---\n%s", before, after)
	}
	if err := cleanup(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ws.Root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ephemeral copy should be removed: %v", err)
	}
}

func TestPrepareEphemeralKeep(t *testing.T) {
	dir := repo(t)
	ws, cleanup, err := workspace.Prepare(ctx, workspace.Options{Root: dir, Mode: workspace.ModeEphemeral})
	if err != nil {
		t.Fatal(err)
	}
	if ws.Kind != core.WorkspaceEphemeral {
		t.Fatalf("kind = %s", ws.Kind)
	}
	if err := cleanup(ctx, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ws.Root, "main.go")); err != nil {
		t.Fatalf("kept copy should survive: %v", err)
	}
	t.Cleanup(func() { _ = cleanup(ctx, false) })
}

func TestPreparePlainDir(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "notes.txt", "hi\n")
	ws, cleanup, err := workspace.Prepare(ctx, workspace.Options{Root: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cleanup(ctx, false) }()
	if ws.Kind != core.WorkspaceEphemeral || read(t, ws.Root, "notes.txt") != "hi\n" {
		t.Fatalf("workspace = %+v", ws)
	}
	if _, err := workspace.ChangedFiles(ctx, ws); !errors.Is(err, core.ErrUnavailable) {
		t.Fatalf("ChangedFiles on plain dir: %v", err)
	}
	write(t, ws.Root, "made.txt", "x")
	rep, err := workspace.Rollback(ctx, ws, []string{"made.txt"})
	if err != nil || !slices.Equal(rep.Removed, []string{"made.txt"}) || len(rep.Restored) != 0 {
		t.Fatalf("rollback = %+v, %v", rep, err)
	}
}

func TestPrepareErrors(t *testing.T) {
	dir := repo(t)
	write(t, dir, "main.go", "package main // wip\n")
	write(t, dir, "new.txt", "n")
	_, _, err := workspace.Prepare(ctx, workspace.Options{Root: dir, Mode: workspace.ModePrimary})
	if !errors.Is(err, core.ErrConflict) || !strings.Contains(err.Error(), "main.go") || !strings.Contains(err.Error(), "new.txt") {
		t.Fatalf("dirty primary: %v", err)
	}
	if _, _, err := workspace.Prepare(ctx, workspace.Options{Root: t.TempDir(), Mode: workspace.ModePrimary}); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("primary on plain dir: %v", err)
	}
	if _, _, err := workspace.Prepare(ctx, workspace.Options{Root: dir, Mode: "bogus"}); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("bogus mode: %v", err)
	}
	if _, _, err := workspace.Prepare(ctx, workspace.Options{Root: filepath.Join(dir, "nope")}); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("missing root: %v", err)
	}
	if _, _, err := workspace.Prepare(ctx, workspace.Options{Root: filepath.Join(dir, "main.go")}); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("file root: %v", err)
	}
	if _, _, err := workspace.Prepare(ctx, workspace.Options{Root: "relative"}); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("relative root: %v", err)
	}
}

func TestRollbackAndChangedFiles(t *testing.T) {
	dir := repo(t)
	installFailingHooks(t, dir)
	write(t, dir, "pre.txt", "pre-existing untracked\n")
	original := read(t, dir, "main.go")
	ws, cleanup, err := workspace.Prepare(ctx, workspace.Options{Root: dir, Mode: workspace.ModeEphemeral})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cleanup(ctx, false) }()
	write(t, ws.Root, "main.go", "package main // broken\n")
	write(t, ws.Root, "new.txt", "made by the run\n")
	write(t, ws.Root, "sub/deep.txt", "made by the run\n")
	if err := os.Remove(filepath.Join(ws.Root, "README.md")); err != nil {
		t.Fatal(err)
	}
	changed, err := workspace.ChangedFiles(ctx, ws)
	if err != nil || !slices.Equal(changed, []string{"README.md", "main.go", "new.txt", "pre.txt", "sub/deep.txt"}) {
		t.Fatalf("changed = %v, %v", changed, err)
	}
	rep, err := workspace.Rollback(ctx, ws, []string{"new.txt", "sub/deep.txt", "missing.txt", "../outside", "", "sub"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rep.Removed, []string{"new.txt", "sub/deep.txt"}) {
		t.Errorf("removed = %v", rep.Removed)
	}
	if !slices.Equal(rep.Restored, []string{"README.md", "main.go"}) {
		t.Errorf("restored = %v", rep.Restored)
	}
	for _, want := range []string{"missing.txt", "../outside", "", "sub", "pre.txt"} {
		if !slices.Contains(rep.Skipped, want) {
			t.Errorf("skipped %v lacks %q", rep.Skipped, want)
		}
	}
	if got := read(t, ws.Root, "main.go"); got != original {
		t.Errorf("main.go = %q, want %q", got, original)
	}
	if read(t, ws.Root, "README.md") != "# demo\n" || read(t, ws.Root, "pre.txt") != "pre-existing untracked\n" {
		t.Error("README.md not restored or pre.txt touched")
	}
	if _, err := os.Stat(filepath.Join(ws.Root, "hook-ran")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a repository hook ran: %v", err)
	}
	if changed, err = workspace.ChangedFiles(ctx, ws); err != nil || !slices.Equal(changed, []string{"pre.txt"}) {
		t.Fatalf("after rollback changed = %v, %v", changed, err)
	}
	if _, err := workspace.Rollback(ctx, core.Workspace{Root: filepath.Join(dir, "nope")}, nil); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("rollback on missing root: %v", err)
	}
}
