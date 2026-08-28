package workspace

import (
	"context"
	"fmt"
	"os"

	"github.com/ataidesorg/friday/internal/core"
	"github.com/ataidesorg/friday/internal/fsutil"
)

// RollbackReport says what Rollback did, by workspace-relative path.
type RollbackReport struct {
	Restored []string // tracked files put back to their pre-run content
	Removed  []string // created files deleted
	Skipped  []string // created paths not removed, and untracked files left in place
}

// Rollback deletes the files a run created (only those listed, only regular
// files inside the workspace) and restores every tracked file git reports
// as changed. Untracked files not in created are left alone and reported
// as skipped. It fails if tracked changes remain afterwards.
func Rollback(ctx context.Context, ws core.Workspace, created []string) (RollbackReport, error) {
	var rep RollbackReport
	root, err := realDir(ws.Root)
	if err != nil {
		return rep, err
	}
	for _, rel := range created {
		abs, err := fsutil.Confine(root, rel)
		if err != nil || abs == root {
			rep.Skipped = append(rep.Skipped, rel)
			continue
		}
		if st, err := os.Lstat(abs); err != nil || !st.Mode().IsRegular() {
			rep.Skipped = append(rep.Skipped, rel)
			continue
		}
		if err := os.Remove(abs); err != nil {
			return rep, fmt.Errorf("remove %s: %w", rel, err)
		}
		rep.Removed = append(rep.Removed, rel)
	}
	vcs, _, err := status(ctx, root)
	if err != nil || vcs.Kind != "git" {
		return rep, err
	}
	entries, err := porcelain(ctx, root)
	if err != nil {
		return rep, err
	}
	tracked := false
	for _, e := range entries {
		if e.code == "??" {
			rep.Skipped = append(rep.Skipped, e.path)
			continue
		}
		tracked = true
		rep.Restored = append(rep.Restored, e.path)
	}
	if !tracked {
		return rep, nil
	}
	// ponytail: restores the index state, exact for a primary (clean) tree; an
	// ephemeral copy of a dirty tree loses its unstaged edits, the original
	// checkout is untouched either way. Record a baseline ref if kept copies
	// need byte-exact restoration.
	if _, err := git(ctx, root, "checkout", "--", "."); err != nil {
		return rep, fmt.Errorf("restore tracked files: %w", err)
	}
	after, err := porcelain(ctx, root)
	if err != nil {
		return rep, err
	}
	for _, e := range after {
		if e.code != "??" {
			return rep, fmt.Errorf("%w: rollback left %s changed (%s)", core.ErrConflict, e.path, e.code)
		}
	}
	return rep, nil
}
