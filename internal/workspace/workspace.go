// Package workspace decides where a run is allowed to write. A clean git
// checkout is used in place (primary); anything else — a dirty tree or a
// plain directory — gets a private copy (ephemeral) so the user's
// uncommitted work is never the thing an agent edits. Git is invoked argv
// only, with repository hooks disabled.
package workspace

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ataidesorg/ink/internal/core"
	"github.com/ataidesorg/ink/internal/fsutil"
)

// Mode selects how the workspace is materialised.
type Mode string

// Modes accepted by Prepare.
const (
	// ModeAuto uses the checkout when it is clean, otherwise a copy.
	ModeAuto Mode = "auto"
	// ModeEphemeral always works on a private copy.
	ModeEphemeral Mode = "ephemeral"
	// ModePrimary works in place and refuses a dirty or non-git tree.
	ModePrimary Mode = "primary"
	// ModeWorktree works on a dedicated git worktree named by Options.Worktree.
	ModeWorktree Mode = "worktree"
)

// Options configure Prepare.
type Options struct {
	Root    string
	Mode    Mode // "" means ModeAuto
	Project core.ProjectID
	// Worktree names the dedicated worktree ModeWorktree opens; WorktreeDir
	// is the directory the named worktrees live under (outside the checkout,
	// so a worktree never dirties the primary tree).
	Worktree    string
	WorktreeDir string
}

// Cleanup releases a workspace; keep leaves an ephemeral copy on disk.
type Cleanup func(ctx context.Context, keep bool) error

// Ephemeral copies leave out per-machine run state.
var copyExclude = []string{".ink/local"}

// Prepare resolves o.Root, inspects its git state, and returns the workspace
// a run may write to plus the cleanup that releases it.
func Prepare(ctx context.Context, o Options) (core.Workspace, Cleanup, error) {
	mode := o.Mode
	if mode == "" {
		mode = ModeAuto
	}
	if mode != ModeAuto && mode != ModeEphemeral && mode != ModePrimary && mode != ModeWorktree {
		return core.Workspace{}, nil, fmt.Errorf("%w: workspace mode %q is not auto, ephemeral, primary, or worktree", core.ErrInvalidInput, o.Mode)
	}
	root, err := realDir(o.Root)
	if err != nil {
		return core.Workspace{}, nil, err
	}
	vcs, dirty, err := status(ctx, root)
	if err != nil {
		return core.Workspace{}, nil, err
	}
	ws := core.Workspace{ID: core.NewWorkspaceID(), Project: o.Project, Root: root, Kind: core.WorkspacePrimary, Branch: vcs.Branch}
	if mode == ModeWorktree {
		return worktree(ctx, ws, vcs, o)
	}
	switch {
	case mode == ModePrimary && vcs.Kind != "git":
		return core.Workspace{}, nil, fmt.Errorf("%w: primary workspace needs a git checkout, %s has none", core.ErrInvalidInput, root)
	case mode == ModePrimary && vcs.Dirty:
		return core.Workspace{}, nil, fmt.Errorf("%w: worktree has uncommitted changes: %s", core.ErrConflict, strings.Join(dirty, ", "))
	case mode == ModePrimary, mode == ModeAuto && vcs.Kind == "git" && !vcs.Dirty:
		return ws, func(context.Context, bool) error { return nil }, nil
	}
	return ephemeral(ctx, ws)
}

func ephemeral(ctx context.Context, ws core.Workspace) (core.Workspace, Cleanup, error) {
	tmp, err := os.MkdirTemp("", "ink-ws-*")
	if err != nil {
		return core.Workspace{}, nil, err
	}
	dst := filepath.Join(tmp, "work")
	if err := fsutil.CopyTree(ctx, ws.Root, dst, copyExclude); err != nil {
		_ = os.RemoveAll(tmp)
		return core.Workspace{}, nil, fmt.Errorf("copy workspace: %w", err)
	}
	root, err := filepath.EvalSymlinks(dst)
	if err != nil {
		_ = os.RemoveAll(tmp)
		return core.Workspace{}, nil, err
	}
	ws.Root, ws.Kind = root, core.WorkspaceEphemeral
	cleanup := func(_ context.Context, keep bool) error {
		if keep {
			return nil
		}
		return os.RemoveAll(tmp)
	}
	return ws, cleanup, nil
}

// realDir returns the symlink-resolved absolute path of an existing directory.
func realDir(p string) (string, error) {
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("%w: workspace root %q is not absolute", core.ErrInvalidInput, p)
	}
	resolved, err := filepath.EvalSymlinks(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("%w: workspace root %s", core.ErrNotFound, p)
	case err != nil:
		return "", err
	}
	st, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !st.IsDir() {
		return "", fmt.Errorf("%w: workspace root %s is not a directory", core.ErrInvalidInput, p)
	}
	return resolved, nil
}
