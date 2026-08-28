// Package fsutil holds the filesystem primitives shared by the packages that
// touch a working tree: path confinement to a root and tree copying.
package fsutil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ataidesorg/friday/internal/core"
)

// ErrOutside is returned for any path that would leave its root.
var ErrOutside = fmt.Errorf("%w: path escapes root", core.ErrInvalidInput)

// CleanRel normalises a root-relative path lexically. "" means the root.
func CleanRel(rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: %q is absolute", ErrOutside, rel)
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q", ErrOutside, rel)
	}
	return clean, nil
}

// Confine joins rel to root and returns the symlink-resolved absolute path,
// which must remain inside root. Missing trailing components are allowed so
// callers can create files.
func Confine(root, rel string) (string, error) {
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("%w: root %q is not absolute", core.ErrInvalidInput, root)
	}
	clean, err := CleanRel(rel)
	if err != nil {
		return "", err
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("%w: root: %w", core.ErrInvalidInput, err)
	}
	resolved, err := resolveExisting(filepath.Join(realRoot, clean))
	if err != nil {
		return "", fmt.Errorf("%w: %q: %w", core.ErrInvalidInput, rel, err)
	}
	if resolved != realRoot && !strings.HasPrefix(resolved, realRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q resolves outside the root", ErrOutside, rel)
	}
	return resolved, nil
}

// resolveExisting evaluates symlinks on the deepest existing prefix of p and
// re-attaches the missing tail.
func resolveExisting(p string) (string, error) {
	resolved, err := filepath.EvalSymlinks(p)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	dir, base := filepath.Split(filepath.Clean(p))
	if dir == "" || dir == p {
		return "", err
	}
	parent, err := resolveExisting(filepath.Clean(dir))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, base), nil
}

// CopyTree copies src into dst (which it creates), skipping excluded paths.
// Symlinks are recreated verbatim, file modes are preserved, and special
// files are left out. WalkDir never follows symlinks, so a link out of the
// tree cannot pull host files into the copy.
func CopyTree(ctx context.Context, src, dst string, exclude []string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.Mkdir(dst, 0o750)
		}
		if Excluded(exclude, filepath.ToSlash(rel)) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch mode := info.Mode(); {
		case mode.IsDir():
			return os.Mkdir(target, mode.Perm()|0o700)
		case mode&fs.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(link, target) //nolint:gosec // src is the caller's own tree; the copy is not a trust boundary
		case mode.IsRegular():
			return copyFile(p, target, mode.Perm())
		default:
			return nil
		}
	})
}

func copyFile(src, dst string, perm fs.FileMode) (err error) {
	in, err := os.Open(src) //nolint:gosec // path comes from walking the caller's validated tree
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm|0o600) //nolint:gosec // dst is inside the fresh copy; mode mirrors the source file
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
	}()
	_, err = io.Copy(out, in)
	return err
}

// Excluded reports whether rel (slash-separated, relative to the tree root)
// or any of its ancestors matches a pattern. A trailing "/**" is accepted
// and means the same as naming the directory.
func Excluded(patterns []string, rel string) bool {
	for _, pat := range patterns {
		pat = strings.TrimSuffix(pat, "/**")
		for p := rel; p != ""; p = dirOf(p) {
			if ok, _ := filepath.Match(pat, p); ok {
				return true
			}
		}
	}
	return false
}

func dirOf(p string) string {
	i := strings.LastIndex(p, "/")
	if i < 0 {
		return ""
	}
	return p[:i]
}
