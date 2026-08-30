package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ataidesorg/ink/internal/core"
)

const maxListDepth = 8

// ListDir lists a directory up to a depth without following symlinks.
type ListDir struct{}

type listDirArgs struct {
	Path  string `json:"path"`
	Depth int    `json:"depth,omitempty"`
}

type dirEntry struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Size int64  `json:"size"`
}

// Spec describes the tool.
func (*ListDir) Spec() core.ToolSpec {
	return core.ToolSpec{Name: "list_dir", Description: "List files and directories inside the workspace.", Risk: core.RiskReadOnly, InputSchema: schema("list_dir")}
}

// Capability scopes the call to the requested path.
func (*ListDir) Capability(in core.ToolInput) core.Capability {
	var a listDirArgs
	_ = json.Unmarshal(in.Arguments, &a)
	return pathScope(core.RiskReadOnly, a.Path)
}

// Invoke walks the directory.
func (t *ListDir) Invoke(_ context.Context, in core.ToolInput, tc core.ToolContext) (core.ToolOutput, error) {
	if err := requireRoot(tc); err != nil {
		return core.ToolOutput{}, err
	}
	var a listDirArgs
	if err := decodeArgs("list_dir", in.Arguments, &a); err != nil {
		return core.ToolOutput{}, err
	}
	depth := a.Depth
	if depth <= 0 {
		depth = 1
	}
	if depth > maxListDepth {
		return core.ToolOutput{}, fmt.Errorf("%w: depth %d exceeds %d", core.ErrInvalidInput, depth, maxListDepth)
	}
	abs, err := confine(tc.WorkspaceRoot, a.Path)
	if err != nil {
		return core.ToolOutput{}, err
	}
	var entries []dirEntry
	walk := func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == abs {
			return nil
		}
		rel, _ := filepath.Rel(abs, p)
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		entries = append(entries, dirEntry{Name: filepath.ToSlash(rel), Kind: kindOf(info), Size: info.Size()})
		if d.IsDir() && strings.Count(rel, string(filepath.Separator))+1 >= depth {
			return filepath.SkipDir
		}
		return nil
	}
	if err := filepath.WalkDir(abs, walk); err != nil {
		return core.ToolOutput{}, fmt.Errorf("%w: %s: %w", core.ErrNotFound, a.Path, err)
	}
	res := struct {
		Entries []dirEntry `json:"entries"`
	}{entries}
	var sb strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&sb, "%s\t%s\t%d\n", e.Kind, e.Name, e.Size)
	}
	return output(sb.String(), res, t.Capability(in))
}

func kindOf(info os.FileInfo) string {
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		return "symlink"
	case info.IsDir():
		return "dir"
	default:
		return "file"
	}
}
