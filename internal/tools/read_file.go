package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/ataidesorg/ink/internal/core"
)

const (
	defaultReadBytes = 256 << 10
	maxReadBytes     = 1 << 20
)

// ReadFile returns a file's content, truncated at max_bytes.
type ReadFile struct{}

type readFileArgs struct {
	Path     string `json:"path"`
	MaxBytes int    `json:"max_bytes,omitempty"`
}

// Spec describes the tool.
func (*ReadFile) Spec() core.ToolSpec {
	return core.ToolSpec{Name: "read_file", Description: "Read a text file inside the workspace.", Risk: core.RiskReadOnly, InputSchema: schema("read_file")}
}

// Capability scopes the call to the requested path.
func (*ReadFile) Capability(in core.ToolInput) core.Capability {
	var a readFileArgs
	_ = json.Unmarshal(in.Arguments, &a)
	return pathScope(core.RiskReadOnly, a.Path)
}

// Invoke reads the file.
func (t *ReadFile) Invoke(_ context.Context, in core.ToolInput, tc core.ToolContext) (core.ToolOutput, error) {
	if err := requireRoot(tc); err != nil {
		return core.ToolOutput{}, err
	}
	var a readFileArgs
	if err := decodeArgs("read_file", in.Arguments, &a); err != nil {
		return core.ToolOutput{}, err
	}
	limit := a.MaxBytes
	if limit <= 0 {
		limit = defaultReadBytes
	}
	if limit > maxReadBytes {
		return core.ToolOutput{}, fmt.Errorf("%w: max_bytes %d exceeds %d", core.ErrInvalidInput, limit, maxReadBytes)
	}
	abs, err := confine(tc.WorkspaceRoot, a.Path)
	if err != nil {
		return core.ToolOutput{}, err
	}
	f, err := os.Open(abs) //nolint:gosec // path confined above
	if err != nil {
		return core.ToolOutput{}, fmt.Errorf("%w: %s: %w", core.ErrNotFound, a.Path, err)
	}
	defer f.Close() //nolint:errcheck // read-only handle
	if st, err := f.Stat(); err != nil || st.IsDir() {
		return core.ToolOutput{}, fmt.Errorf("%w: %s is not a regular file", core.ErrInvalidInput, a.Path)
	}
	buf, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return core.ToolOutput{}, fmt.Errorf("read %s: %w", a.Path, err)
	}
	truncated := len(buf) > limit
	if truncated {
		buf = buf[:limit]
	}
	res := struct {
		Content   string `json:"content"`
		Truncated bool   `json:"truncated"`
	}{string(buf), truncated}
	return output(res.Content, res, t.Capability(in))
}
