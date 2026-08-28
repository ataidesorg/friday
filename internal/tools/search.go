package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ataidesorg/friday/internal/core"
)

const (
	defaultMaxResults = 100
	maxSearchResults  = 1000
	maxSearchFileSize = 1 << 20
	binarySniffBytes  = 8 << 10
)

// Search greps files with an RE2 regexp, line by line, without a shell.
type Search struct{}

type searchArgs struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path,omitempty"`
	MaxResults int    `json:"max_results,omitempty"`
}

type match struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

// Spec describes the tool.
func (*Search) Spec() core.ToolSpec {
	return core.ToolSpec{Name: "search", Description: "Search workspace text files with a regular expression.", Risk: core.RiskReadOnly, InputSchema: schema("search")}
}

// Capability scopes the call to the searched path.
func (*Search) Capability(in core.ToolInput) core.Capability {
	var a searchArgs
	_ = json.Unmarshal(in.Arguments, &a)
	return pathScope(core.RiskReadOnly, a.Path)
}

// Invoke runs the search.
func (t *Search) Invoke(ctx context.Context, in core.ToolInput, tc core.ToolContext) (core.ToolOutput, error) {
	if err := requireRoot(tc); err != nil {
		return core.ToolOutput{}, err
	}
	var a searchArgs
	if err := decodeArgs("search", in.Arguments, &a); err != nil {
		return core.ToolOutput{}, err
	}
	re, err := regexp.Compile(a.Pattern)
	if err != nil {
		return core.ToolOutput{}, fmt.Errorf("%w: pattern: %w", core.ErrInvalidInput, err)
	}
	limit := a.MaxResults
	if limit <= 0 {
		limit = defaultMaxResults
	}
	if limit > maxSearchResults {
		return core.ToolOutput{}, fmt.Errorf("%w: max_results %d exceeds %d", core.ErrInvalidInput, limit, maxSearchResults)
	}
	abs, err := confine(tc.WorkspaceRoot, a.Path)
	if err != nil {
		return core.ToolOutput{}, err
	}
	var matches []match
	walk := func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if d.Name() == ".git" && p != abs {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		found, err := grepFile(p, re, limit-len(matches))
		if err != nil {
			return err
		}
		for _, m := range found {
			m.Path = displayPath(tc.WorkspaceRoot, p)
			matches = append(matches, m)
		}
		if len(matches) >= limit {
			return fs.SkipAll
		}
		return nil
	}
	if err := filepath.WalkDir(abs, walk); err != nil {
		return core.ToolOutput{}, fmt.Errorf("search %s: %w", a.Path, err)
	}
	res := struct {
		Matches []match `json:"matches"`
	}{matches}
	var sb strings.Builder
	for _, m := range matches {
		fmt.Fprintf(&sb, "%s:%d: %s\n", m.Path, m.Line, m.Text)
	}
	return output(sb.String(), res, t.Capability(in))
}

func grepFile(path string, re *regexp.Regexp, limit int) ([]match, error) {
	if limit <= 0 {
		return nil, nil
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() > maxSearchFileSize {
		return nil, err
	}
	f, err := os.Open(path) //nolint:gosec // path comes from a confined walk
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck // read-only handle
	head := make([]byte, binarySniffBytes)
	n, _ := f.Read(head)
	if bytes.IndexByte(head[:n], 0) >= 0 {
		return nil, nil
	}
	if _, err := f.Seek(0, 0); err != nil {
		return nil, err
	}
	var out []match
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), maxSearchFileSize)
	for line := 1; sc.Scan(); line++ {
		if re.Match(sc.Bytes()) {
			out = append(out, match{Line: line, Text: sc.Text()})
			if len(out) >= limit {
				break
			}
		}
	}
	return out, sc.Err()
}
