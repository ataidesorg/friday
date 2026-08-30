package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ataidesorg/ink/internal/core"
)

// WriteFile writes a whole file inside the workspace.
type WriteFile struct{}

type writeFileArgs struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	CreateOnly bool   `json:"create_only,omitempty"`
}

// Spec describes the tool.
func (*WriteFile) Spec() core.ToolSpec {
	return core.ToolSpec{Name: "write_file", Description: "Create or overwrite a file inside the workspace.", Risk: core.RiskWriteLocal, InputSchema: schema("write_file")}
}

// Capability scopes the call to the written path.
func (*WriteFile) Capability(in core.ToolInput) core.Capability {
	var a writeFileArgs
	_ = json.Unmarshal(in.Arguments, &a)
	return pathScope(core.RiskWriteLocal, a.Path)
}

// Invoke writes the file, creating parent directories.
func (t *WriteFile) Invoke(_ context.Context, in core.ToolInput, tc core.ToolContext) (core.ToolOutput, error) {
	if err := requireRoot(tc); err != nil {
		return core.ToolOutput{}, err
	}
	var a writeFileArgs
	if err := decodeArgs("write_file", in.Arguments, &a); err != nil {
		return core.ToolOutput{}, err
	}
	abs, err := confine(tc.WorkspaceRoot, a.Path)
	if err != nil {
		return core.ToolOutput{}, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
		return core.ToolOutput{}, fmt.Errorf("write %s: %w", a.Path, err)
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if a.CreateOnly {
		flags |= os.O_EXCL
	}
	f, err := os.OpenFile(abs, flags, 0o640) //nolint:gosec // path confined above
	if err != nil {
		if a.CreateOnly && os.IsExist(err) {
			return core.ToolOutput{}, fmt.Errorf("%w: %s already exists", core.ErrConflict, a.Path)
		}
		return core.ToolOutput{}, fmt.Errorf("write %s: %w", a.Path, err)
	}
	n, err := f.WriteString(a.Content)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return core.ToolOutput{}, fmt.Errorf("write %s: %w", a.Path, err)
	}
	res := struct {
		BytesWritten int `json:"bytes_written"`
	}{n}
	return output(fmt.Sprintf("wrote %d bytes to %s", n, a.Path), res, t.Capability(in))
}
