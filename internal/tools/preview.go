package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/ataidesorg/friday/internal/core"
)

// previewLines caps how much of a diff or patch an approval prompt shows.
const previewLines = 40

// Previewer lets a tool show the human exactly what approving a call does.
// The string is UI-only: it reaches the approval prompt, never the trail.
type Previewer interface {
	Preview(in core.ToolInput, tc core.ToolContext) string
}

// Preview renders t's preview when it offers one; "" otherwise.
func Preview(t core.Tool, in core.ToolInput, tc core.ToolContext) string {
	if p, ok := t.(Previewer); ok {
		return p.Preview(in, tc)
	}
	return ""
}

// Preview shows a naive old/new line diff of the write.
// ponytail: -/+ lines only, no hunk headers; a real diff lib if this grates.
func (*WriteFile) Preview(in core.ToolInput, tc core.ToolContext) string {
	var a writeFileArgs
	if json.Unmarshal(in.Arguments, &a) != nil || a.Path == "" {
		return ""
	}
	var old string
	if abs, err := confine(tc.WorkspaceRoot, a.Path); err == nil {
		if b, err := os.ReadFile(abs); err == nil { //nolint:gosec // path confined above
			old = string(b)
		}
	}
	return clipPreview(a.Path + "\n" + naiveDiff(old, a.Content))
}

// Preview shows the patch text itself.
func (*ApplyPatch) Preview(in core.ToolInput, _ core.ToolContext) string {
	var a applyPatchArgs
	if json.Unmarshal(in.Arguments, &a) != nil {
		return ""
	}
	return clipPreview(a.Patch)
}

// Preview shows the exact argv the sandbox would run.
func (t *RunCommand) Preview(in core.ToolInput, _ core.ToolContext) string {
	var a runCommandArgs
	if json.Unmarshal(in.Arguments, &a) != nil || len(a.Argv) == 0 {
		return ""
	}
	s := "$ " + strings.Join(a.Argv, " ")
	if a.Dir != "" {
		s += "  (in " + a.Dir + ")"
	}
	return clipPreview(s)
}

// Preview forwards to the wrapped write tool.
func (f *formatted) Preview(in core.ToolInput, tc core.ToolContext) string {
	return Preview(f.inner, in, tc)
}

// naiveDiff emits removed-then-added lines for the regions that differ,
// skipping the common prefix and suffix.
func naiveDiff(old, cur string) string {
	if old == cur {
		return "(no change)"
	}
	ol, nl := strings.Split(old, "\n"), strings.Split(cur, "\n")
	p := 0
	for p < len(ol) && p < len(nl) && ol[p] == nl[p] {
		p++
	}
	so, sn := len(ol), len(nl)
	for so > p && sn > p && ol[so-1] == nl[sn-1] {
		so, sn = so-1, sn-1
	}
	var b strings.Builder
	for _, l := range ol[p:so] {
		fmt.Fprintf(&b, "- %s\n", l)
	}
	for _, l := range nl[p:sn] {
		fmt.Fprintf(&b, "+ %s\n", l)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func clipPreview(s string) string {
	// Previews carry model-controlled bytes onto the approval prompt; strip
	// terminal control characters so escape sequences can't repaint or hide
	// the lines the human is judging.
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return '\ufffd'
		}
		return r
	}, s)
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	if len(lines) <= previewLines {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[:previewLines], "\n") + fmt.Sprintf("\n… (%d more lines)", len(lines)-previewLines)
}
