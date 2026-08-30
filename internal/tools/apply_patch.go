package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ataidesorg/ink/internal/core"
)

// ApplyPatch applies a unified diff to workspace files, all hunks or none.
type ApplyPatch struct{}

type applyPatchArgs struct {
	Patch string `json:"patch"`
}

type hunk struct {
	oldStart, oldLines, newStart, newLines int
	lines                                  []string
	noNewline                              bool
}

type filePatch struct {
	oldPath, newPath string
	hunks            []hunk
}

type pendingWrite struct {
	rel, abs string
	content  string
	remove   bool
	create   bool
}

// Spec describes the tool.
func (*ApplyPatch) Spec() core.ToolSpec {
	return core.ToolSpec{Name: "apply_patch", Description: "Apply a unified diff to files inside the workspace.", Risk: core.RiskWriteLocal, InputSchema: schema("apply_patch")}
}

// Capability scopes the call to every path the patch touches.
func (*ApplyPatch) Capability(in core.ToolInput) core.Capability {
	var a applyPatchArgs
	_ = json.Unmarshal(in.Arguments, &a)
	files, err := parseUnified(a.Patch)
	if err != nil {
		return pathScope(core.RiskWriteLocal, "")
	}
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.target())
	}
	return pathScope(core.RiskWriteLocal, strings.Join(paths, ","))
}

// Invoke parses, applies in memory, then writes every file.
// ponytail: all hunks are validated before the first write; a disk failure mid-write is left to workspace rollback.
func (t *ApplyPatch) Invoke(_ context.Context, in core.ToolInput, tc core.ToolContext) (core.ToolOutput, error) {
	if err := requireRoot(tc); err != nil {
		return core.ToolOutput{}, err
	}
	var a applyPatchArgs
	if err := decodeArgs("apply_patch", in.Arguments, &a); err != nil {
		return core.ToolOutput{}, err
	}
	files, err := parseUnified(a.Patch)
	if err != nil {
		return core.ToolOutput{}, err
	}
	writes := make([]pendingWrite, 0, len(files))
	for _, f := range files {
		w, err := planFile(tc.WorkspaceRoot, f)
		if err != nil {
			return core.ToolOutput{}, err
		}
		writes = append(writes, w)
	}
	changed := make([]string, 0, len(writes))
	for _, w := range writes {
		if err := commitWrite(w); err != nil {
			return core.ToolOutput{}, err
		}
		changed = append(changed, w.rel)
	}
	res := struct {
		FilesChanged []string `json:"files_changed"`
	}{changed}
	return output("patched "+strings.Join(changed, ", "), res, t.Capability(in))
}

func (f filePatch) target() string {
	if f.newPath != "" {
		return f.newPath
	}
	return f.oldPath
}

func planFile(root string, f filePatch) (pendingWrite, error) {
	rel := f.target()
	abs, err := confine(root, rel)
	if err != nil {
		return pendingWrite{}, err
	}
	if f.oldPath != "" && f.newPath != "" && f.oldPath != f.newPath {
		return pendingWrite{}, fmt.Errorf("%w: renames are not supported (%s -> %s)", core.ErrInvalidInput, f.oldPath, f.newPath)
	}
	var original string
	switch raw, err := os.ReadFile(abs); { //nolint:gosec // path confined above
	case f.oldPath == "" && err == nil:
		return pendingWrite{}, fmt.Errorf("%w: %s already exists", core.ErrConflict, rel)
	case f.oldPath != "" && err != nil:
		return pendingWrite{}, fmt.Errorf("%w: %s: %w", core.ErrNotFound, rel, err)
	case err == nil:
		original = string(raw)
	}
	content, err := applyHunks(original, f.hunks, rel)
	if err != nil {
		return pendingWrite{}, err
	}
	return pendingWrite{rel: rel, abs: abs, content: content, remove: f.newPath == "", create: f.oldPath == ""}, nil
}

func commitWrite(w pendingWrite) error {
	if w.remove {
		if err := os.Remove(w.abs); err != nil {
			return fmt.Errorf("remove %s: %w", w.rel, err)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(w.abs), 0o750); err != nil {
		return fmt.Errorf("patch %s: %w", w.rel, err)
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if w.create {
		flags |= os.O_EXCL
	}
	fh, err := os.OpenFile(w.abs, flags, 0o640) //nolint:gosec // path confined by planFile
	if err != nil {
		return fmt.Errorf("patch %s: %w", w.rel, err)
	}
	_, err = fh.WriteString(w.content)
	if cerr := fh.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("patch %s: %w", w.rel, err)
	}
	return nil
}

func splitLines(s string) (lines []string, trailingNewline bool) {
	if s == "" {
		return nil, true
	}
	trailingNewline = strings.HasSuffix(s, "\n")
	lines = strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	return lines, trailingNewline
}

func applyHunks(original string, hunks []hunk, rel string) (string, error) {
	lines, trailing := splitLines(original)
	offset := 0
	for i, h := range hunks {
		var before, after []string
		for _, l := range h.lines {
			switch l[0] {
			case ' ':
				before = append(before, l[1:])
				after = append(after, l[1:])
			case '-':
				before = append(before, l[1:])
			case '+':
				after = append(after, l[1:])
			}
		}
		pos, ok := locate(lines, before, h.oldStart-1+offset)
		if !ok {
			return "", fmt.Errorf("%w: hunk %d does not apply to %s", core.ErrConflict, i+1, rel)
		}
		next := make([]string, 0, len(lines)-len(before)+len(after))
		next = append(next, lines[:pos]...)
		next = append(next, after...)
		next = append(next, lines[pos+len(before):]...)
		lines = next
		offset += len(after) - len(before)
		if h.noNewline {
			trailing = false
		} else if len(after) > 0 && pos+len(after) == len(lines) {
			trailing = true
		}
	}
	if len(lines) == 0 {
		return "", nil
	}
	out := strings.Join(lines, "\n")
	if trailing {
		out += "\n"
	}
	return out, nil
}

// locate finds where before occurs in lines, preferring the hinted position.
func locate(lines, before []string, hint int) (int, bool) {
	if len(before) == 0 {
		if hint < 0 {
			hint = 0
		}
		if hint > len(lines) {
			hint = len(lines)
		}
		return hint, true
	}
	if hint >= 0 && matchesAt(lines, before, hint) {
		return hint, true
	}
	for i := 0; i+len(before) <= len(lines); i++ {
		if matchesAt(lines, before, i) {
			return i, true
		}
	}
	return 0, false
}

func matchesAt(lines, before []string, at int) bool {
	if at+len(before) > len(lines) {
		return false
	}
	for i, b := range before {
		if lines[at+i] != b {
			return false
		}
	}
	return true
}

func parseUnified(patch string) ([]filePatch, error) {
	if strings.TrimSpace(patch) == "" {
		return nil, fmt.Errorf("%w: empty patch", core.ErrInvalidInput)
	}
	lines := strings.Split(strings.TrimSuffix(patch, "\n"), "\n")
	var files []filePatch
	for i := 0; i < len(lines); {
		l := lines[i]
		switch {
		case strings.HasPrefix(l, "GIT binary patch") || strings.HasPrefix(l, "Binary files"):
			return nil, fmt.Errorf("%w: binary patches are not supported", core.ErrInvalidInput)
		case strings.HasPrefix(l, "--- "):
			if i+1 >= len(lines) || !strings.HasPrefix(lines[i+1], "+++ ") {
				return nil, fmt.Errorf("%w: line %d: '---' header without '+++'", core.ErrInvalidInput, i+1)
			}
			f := filePatch{oldPath: headerPath(l[4:]), newPath: headerPath(lines[i+1][4:])}
			if f.oldPath == "" && f.newPath == "" {
				return nil, fmt.Errorf("%w: line %d: both sides are /dev/null", core.ErrInvalidInput, i+1)
			}
			i += 2
			for i < len(lines) && strings.HasPrefix(lines[i], "@@ ") {
				h, n, err := parseHunk(lines[i:])
				if err != nil {
					return nil, fmt.Errorf("%w: line %d: %w", core.ErrInvalidInput, i+1, err)
				}
				f.hunks = append(f.hunks, h)
				i += n
			}
			if len(f.hunks) == 0 {
				return nil, fmt.Errorf("%w: %s has no hunks", core.ErrInvalidInput, f.target())
			}
			files = append(files, f)
		default:
			i++
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%w: no file headers in patch", core.ErrInvalidInput)
	}
	return files, nil
}

func headerPath(s string) string {
	s = strings.TrimSpace(strings.SplitN(s, "\t", 2)[0])
	if s == "/dev/null" {
		return ""
	}
	for _, p := range []string{"a/", "b/"} {
		if strings.HasPrefix(s, p) {
			return s[len(p):]
		}
	}
	return s
}

func parseHunk(lines []string) (hunk, int, error) {
	header := lines[0]
	fields := strings.Fields(header)
	if len(fields) < 3 || fields[0] != "@@" {
		return hunk{}, 0, errors.New("malformed hunk header")
	}
	var h hunk
	var err error
	if h.oldStart, h.oldLines, err = parseRange(fields[1], '-'); err != nil {
		return hunk{}, 0, err
	}
	if h.newStart, h.newLines, err = parseRange(fields[2], '+'); err != nil {
		return hunk{}, 0, err
	}
	n := 1
	oldSeen, newSeen := 0, 0
	for n < len(lines) && (oldSeen < h.oldLines || newSeen < h.newLines) {
		l := lines[n]
		if l == "" {
			l = " "
		}
		switch l[0] {
		case ' ':
			oldSeen++
			newSeen++
		case '-':
			oldSeen++
		case '+':
			newSeen++
		case '\\':
			n++
			continue
		default:
			return hunk{}, 0, fmt.Errorf("unexpected line %q inside hunk", l)
		}
		h.lines = append(h.lines, l)
		n++
	}
	if oldSeen != h.oldLines || newSeen != h.newLines {
		return hunk{}, 0, errors.New("hunk body shorter than its header")
	}
	if n < len(lines) && strings.HasPrefix(lines[n], "\\") {
		h.noNewline = true
		n++
	}
	return h, n, nil
}

func parseRange(s string, sign byte) (start, count int, err error) {
	if len(s) == 0 || s[0] != sign {
		return 0, 0, fmt.Errorf("malformed range %q", s)
	}
	parts := strings.SplitN(s[1:], ",", 2)
	if start, err = strconv.Atoi(parts[0]); err != nil {
		return 0, 0, fmt.Errorf("malformed range %q", s)
	}
	count = 1
	if len(parts) == 2 {
		if count, err = strconv.Atoi(parts[1]); err != nil {
			return 0, 0, fmt.Errorf("malformed range %q", s)
		}
	}
	return start, count, nil
}
