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

	"github.com/ataidesorg/ink/internal/core"
)

// MergeResult is the outcome of merging one or more branches into root.
type MergeResult struct {
	OK        bool
	Merged    []string
	Conflicts []string
	StoppedAt string
}

// MergeBranches fast-forwards nothing: it merges each branch into root in
// order with --no-ff. A conflict aborts that merge, leaves the tree clean,
// and reports the unmerged paths. Missing branches are errors.
func MergeBranches(ctx context.Context, root string, branches []string) (MergeResult, error) {
	if err := ctx.Err(); err != nil {
		return MergeResult{}, err
	}
	root, err := realDir(root)
	if err != nil {
		return MergeResult{}, err
	}
	if len(branches) == 0 {
		return MergeResult{}, fmt.Errorf("%w: no branches to merge", core.ErrInvalidInput)
	}
	var merged []string
	for _, branch := range branches {
		if strings.TrimSpace(branch) == "" {
			return MergeResult{}, fmt.Errorf("%w: empty branch name", core.ErrInvalidInput)
		}
		_, stderr, err := gitWrite(ctx, root, "merge", "--no-edit", "--no-ff", branch)
		if err == nil {
			merged = append(merged, branch)
			continue
		}
		if ctx.Err() != nil {
			return MergeResult{}, ctx.Err()
		}
		conflicts, cerr := unmergedPaths(ctx, root)
		_ = abortMerge(ctx, root)
		if cerr != nil {
			return MergeResult{}, fmt.Errorf("merge %s: %w: %s", branch, err, strings.TrimSpace(stderr))
		}
		if len(conflicts) == 0 {
			return MergeResult{}, fmt.Errorf("%w: merge %s: %s", core.ErrUnavailable, branch, strings.TrimSpace(stderr))
		}
		return MergeResult{Merged: merged, Conflicts: conflicts, StoppedAt: branch}, nil
	}
	return MergeResult{OK: true, Merged: merged}, nil
}

func unmergedPaths(ctx context.Context, root string) ([]string, error) {
	out, _, err := gitWrite(ctx, root, "ls-files", "-u", "-z")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var paths []string
	for _, rec := range strings.Split(strings.TrimSuffix(out, "\x00"), "\x00") {
		if rec == "" {
			continue
		}
		// unmerged: <mode> <hash> <stage>\t<path>
		_, path, ok := strings.Cut(rec, "\t")
		if !ok || path == "" || seen[path] {
			continue
		}
		seen[path] = true
		paths = append(paths, path)
	}
	slices.Sort(paths)
	return paths, nil
}

func abortMerge(ctx context.Context, root string) error {
	_, _, err := gitWrite(ctx, root, "merge", "--abort")
	return err
}

// gitWrite is git() without GIT_OPTIONAL_LOCKS=0, so merge can take the index.
func gitWrite(ctx context.Context, dir string, args ...string) (stdout, stderr string, err error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	argv := append([]string{
		"-c", "core.hooksPath=/dev/null",
		"-c", "user.name=ink",
		"-c", "user.email=ink@localhost",
		"-C", dir,
	}, args...)
	cmd := exec.CommandContext(ctx, "git", argv...) //nolint:gosec // argv only, no shell; subcommands are fixed by this package
	cmd.Env = gitWriteEnv()
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &outBuf, &errBuf
	err = cmd.Run()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = errors.Join(ctxErr, err)
		}
		err = fmt.Errorf("git %s: %w", args[0], err)
	}
	return outBuf.String(), errBuf.String(), err
}

func gitWriteEnv() []string {
	env := []string{"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "LC_ALL=C"}
	for _, k := range []string{"PATH", "HOME", "TMPDIR"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}
