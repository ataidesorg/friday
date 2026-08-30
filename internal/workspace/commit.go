package workspace

import (
	"context"
	"fmt"
	"strings"

	"github.com/ataidesorg/ink/internal/core"
)

// CommitResult is one local commit attempt. Created is false when the tree
// was already clean. Hash is always HEAD afterwards. Ink never pushes.
type CommitResult struct {
	Hash    string
	Created bool
}

// TaskBranch returns the namespaced branch for a task worktree (`ink/<name>`).
func TaskBranch(name string) (string, error) {
	if !worktreeName.MatchString(name) {
		return "", fmt.Errorf("%w: worktree name %q must match %s", core.ErrInvalidInput, name, worktreeName)
	}
	return branchPrefix + name, nil
}

// CommitStep stages every change under root and creates one commit when
// there is something to record. An empty message is rejected. The remote
// is never contacted.
func CommitStep(ctx context.Context, root, message string) (CommitResult, error) {
	if err := ctx.Err(); err != nil {
		return CommitResult{}, err
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return CommitResult{}, fmt.Errorf("%w: commit message is empty", core.ErrInvalidInput)
	}
	root, err := realDir(root)
	if err != nil {
		return CommitResult{}, err
	}
	if _, _, err := gitWrite(ctx, root, "add", "-A", "--", "."); err != nil {
		return CommitResult{}, err
	}
	changed, err := ChangedFiles(ctx, core.Workspace{Root: root})
	if err != nil {
		return CommitResult{}, err
	}
	if len(changed) == 0 {
		hash, err := head(ctx, root)
		if err != nil {
			return CommitResult{}, err
		}
		return CommitResult{Hash: hash}, nil
	}
	if _, _, err := gitWrite(ctx, root, "commit", "-m", message); err != nil {
		return CommitResult{}, err
	}
	hash, err := head(ctx, root)
	if err != nil {
		return CommitResult{}, err
	}
	return CommitResult{Hash: hash, Created: true}, nil
}

func head(ctx context.Context, root string) (string, error) {
	out, err := git(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	h := strings.TrimSpace(out)
	if h == "" {
		return "", fmt.Errorf("%w: empty HEAD", core.ErrUnavailable)
	}
	return h, nil
}
