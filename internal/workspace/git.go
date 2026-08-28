package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/ataidesorg/friday/internal/core"
)

// gitTimeout bounds every git invocation.
const gitTimeout = 30 * time.Second

// Status reports the git state of root: kind "git" or "none", the branch and
// head when known, and Dirty when any tracked change or untracked file exists.
func Status(ctx context.Context, root string) (core.VCSInfo, error) {
	root, err := realDir(root)
	if err != nil {
		return core.VCSInfo{}, err
	}
	vcs, _, err := status(ctx, root)
	return vcs, err
}

// ChangedFiles lists the paths `git status` reports for the workspace, sorted.
func ChangedFiles(ctx context.Context, ws core.Workspace) ([]string, error) {
	root, err := realDir(ws.Root)
	if err != nil {
		return nil, err
	}
	entries, err := porcelain(ctx, root)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(entries))
	for _, e := range entries {
		paths = append(paths, e.path)
	}
	return paths, nil
}

// status is Status on an already resolved root, also returning the dirty paths.
func status(ctx context.Context, root string) (core.VCSInfo, []string, error) {
	none := core.VCSInfo{Kind: "none"}
	if _, err := exec.LookPath("git"); err != nil {
		return none, nil, nil
	}
	out, err := git(ctx, root, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		if ctx.Err() != nil {
			return core.VCSInfo{}, nil, err
		}
		return none, nil, nil
	}
	if strings.TrimSpace(out) != "true" {
		return none, nil, nil
	}
	entries, err := porcelain(ctx, root)
	if err != nil {
		return core.VCSInfo{}, nil, err
	}
	vcs := core.VCSInfo{Kind: "git", Dirty: len(entries) > 0}
	// An unborn branch has no HEAD; that is not an error for a status report.
	if out, err := git(ctx, root, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		vcs.Branch = strings.TrimSpace(out)
	}
	if out, err := git(ctx, root, "rev-parse", "HEAD"); err == nil {
		vcs.Head = strings.TrimSpace(out)
	}
	dirty := make([]string, 0, len(entries))
	for _, e := range entries {
		dirty = append(dirty, e.path)
	}
	return vcs, dirty, nil
}

type entry struct {
	code string // two-letter porcelain status, "??" for untracked
	path string // slash-separated, relative to root
}

// porcelain parses `git status --porcelain=v1 -z` for the tree under root.
// ErrUnavailable is returned for a directory git does not manage.
func porcelain(ctx context.Context, root string) ([]entry, error) {
	out, err := git(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--", ".")
	if err != nil {
		if ctx.Err() != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %s is not a git checkout: %w", core.ErrUnavailable, root, err)
	}
	fields := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	var entries []entry
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if len(f) < 4 {
			continue
		}
		e := entry{code: f[:2], path: f[3:]}
		if e.code[0] == 'R' || e.code[0] == 'C' {
			i++ // the original path follows as its own field
		}
		entries = append(entries, e)
	}
	slices.SortFunc(entries, func(a, b entry) int { return strings.Compare(a.path, b.path) })
	return entries, nil
}

// git runs one git command in dir with hooks disabled, prompts off, and
// optional index locks skipped so a status never writes to the checkout.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	argv := append([]string{"-c", "core.hooksPath=/dev/null", "-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", argv...) //nolint:gosec // argv only, no shell; subcommands are fixed by this package
	cmd.Env = gitEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = errors.Join(ctxErr, err)
		}
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func gitEnv() []string {
	env := []string{"GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_CONFIG_NOSYSTEM=1", "LC_ALL=C"}
	for _, k := range []string{"PATH", "HOME", "TMPDIR"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}
