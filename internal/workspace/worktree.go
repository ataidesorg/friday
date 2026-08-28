package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ataidesorg/friday/internal/core"
)

// worktreeName restricts worktree names to path-safe tokens.
var worktreeName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// branchPrefix namespaces the branches Friday's worktrees live on.
const branchPrefix = "friday/"

// worktree materialises a dedicated git worktree under o.WorktreeDir so
// parallel sessions never share a checkout. An existing worktree of the same
// name is selected as-is; a missing one is created on branch friday/<name>
// (reusing that branch when a previous worktree left it behind). Cleanup
// removes the worktree only when it is clean — commits survive on the
// branch, uncommitted work keeps the directory.
func worktree(ctx context.Context, ws core.Workspace, vcs core.VCSInfo, o Options) (core.Workspace, Cleanup, error) {
	switch {
	case vcs.Kind != "git":
		return core.Workspace{}, nil, fmt.Errorf("%w: a worktree session needs a git checkout, %s has none", core.ErrInvalidInput, ws.Root)
	case !worktreeName.MatchString(o.Worktree):
		return core.Workspace{}, nil, fmt.Errorf("%w: worktree name %q must match %s", core.ErrInvalidInput, o.Worktree, worktreeName)
	case o.WorktreeDir == "":
		return core.Workspace{}, nil, fmt.Errorf("%w: worktree mode needs a worktree directory", core.ErrInvalidInput)
	}
	mainRoot := ws.Root // resolved by Prepare; captured before ws points at the worktree
	dir := filepath.Join(o.WorktreeDir, o.Worktree)
	branch := branchPrefix + o.Worktree
	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		// Select: the worktree already exists; verify git still owns it.
		if out, err := git(ctx, dir, "rev-parse", "--is-inside-work-tree"); err != nil || strings.TrimSpace(out) != "true" {
			return core.Workspace{}, nil, fmt.Errorf("%w: %s exists but is not a git worktree", core.ErrConflict, dir)
		}
	} else {
		if err := os.MkdirAll(o.WorktreeDir, 0o750); err != nil {
			return core.Workspace{}, nil, fmt.Errorf("worktree dir: %w", err)
		}
		// A manually deleted worktree leaves stale metadata that blocks add.
		if _, err := git(ctx, ws.Root, "worktree", "prune"); err != nil {
			return core.Workspace{}, nil, err
		}
		args := []string{"worktree", "add", dir}
		if _, err := git(ctx, ws.Root, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
			args = append(args, branch) // the branch survived an earlier cleanup; resume it
		} else {
			args = append(args, "-b", branch)
		}
		if _, err := git(ctx, ws.Root, args...); err != nil {
			return core.Workspace{}, nil, fmt.Errorf("create worktree %s: %w", o.Worktree, err)
		}
	}
	root, err := realDir(dir)
	if err != nil {
		return core.Workspace{}, nil, err
	}
	ws.Root, ws.Kind, ws.Branch = root, core.WorkspaceWorktree, branch
	if wvcs, _, serr := status(ctx, root); serr == nil && wvcs.Branch != "" {
		ws.Branch = wvcs.Branch // a selected worktree may sit on another branch
	}
	cleanup := func(ctx context.Context, keep bool) error {
		if keep {
			return nil
		}
		_, dirty, serr := status(ctx, root)
		if serr != nil || len(dirty) > 0 {
			return nil // uncommitted work (or an unreadable state) keeps the worktree
		}
		_, rerr := git(ctx, mainRoot, "worktree", "remove", root)
		return rerr
	}
	return ws, cleanup, nil
}
