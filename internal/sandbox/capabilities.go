package sandbox

import "github.com/ataidesorg/ink/internal/core"

// FileAccess is the optional direct file I/O capability (see core.FileAccess).
type FileAccess = core.FileAccess

// Snapshotter is the optional snapshot capability (see core.Snapshotter).
type Snapshotter = core.Snapshotter

// Files reports whether the sandbox offers direct file access.
func Files(sb core.Sandbox) (FileAccess, bool) {
	f, ok := sb.(FileAccess)
	return f, ok
}

// Snapshots reports whether the sandbox can snapshot its tree.
func Snapshots(sb core.Sandbox) (Snapshotter, bool) {
	s, ok := sb.(Snapshotter)
	return s, ok
}
