package tools

import (
	"path/filepath"

	"github.com/ataidesorg/friday/internal/fsutil"
)

// ErrOutsideWorkspace is returned for any path that would leave the workspace root.
var ErrOutsideWorkspace = fsutil.ErrOutside

// cleanRel normalises a workspace-relative path lexically. "" means the root.
func cleanRel(rel string) (string, error) { return fsutil.CleanRel(rel) }

// confine joins rel to root and returns the symlink-resolved absolute path,
// which must remain inside root.
func confine(root, rel string) (string, error) { return fsutil.Confine(root, rel) }

// displayPath renders an absolute path inside root as a slash-separated relative path.
func displayPath(root, abs string) string {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		realRoot = root
	}
	rel, err := filepath.Rel(realRoot, abs)
	if err != nil {
		return abs
	}
	return filepath.ToSlash(rel)
}
