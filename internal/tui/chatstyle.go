package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// chatStyles is the chat REPL's own palette, distinct from the run viewer's
// per-tag colours in model.go. It adapts to the terminal's light or dark
// background and degrades to plain text when colour is off, so the stored
// "[tag] text" lines render the same content either way.
type chatStyles struct {
	on bool

	accent    lipgloss.Style // Ink's voice and the app name
	user      lipgloss.Style // the human's voice
	ok        lipgloss.Style
	warn      lipgloss.Style
	fail      lipgloss.Style
	dim       lipgloss.Style
	rule      lipgloss.Style
	header    lipgloss.Style
	box       lipgloss.Style // the rounded prompt frame (border kept even when off)
	modal     lipgloss.Style // the centered overlay frame
	spin      lipgloss.Style
	canvas    lipgloss.Style // optional full-frame background (light)
	hasCanvas bool
}

func newChatStyles(on bool) chatStyles { return themedStyles(on, defaultTheme()) }

// themedStyles resolves a theme into the chat palette. Colour off keeps the
// structural frame only, whatever the theme says.
func themedStyles(on bool, th Theme) chatStyles {
	cs := chatStyles{on: on}
	// The frame is structure, not colour, so it is drawn in both modes.
	cs.box = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	cs.modal = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	if !on {
		return cs
	}
	cs.accent = lipgloss.NewStyle().Foreground(th.Accent.paint())
	cs.user = lipgloss.NewStyle().Foreground(th.User.paint())
	cs.ok = lipgloss.NewStyle().Foreground(th.OK.paint())
	cs.warn = lipgloss.NewStyle().Foreground(th.Warn.paint())
	cs.fail = lipgloss.NewStyle().Foreground(th.Fail.paint())
	cs.dim = lipgloss.NewStyle().Foreground(th.Dim.paint())
	cs.rule = lipgloss.NewStyle().Foreground(th.Rule.paint())
	cs.header = lipgloss.NewStyle().Bold(true).Foreground(th.Accent.paint())
	cs.box = cs.box.BorderForeground(th.Dim.paint())
	cs.modal = cs.modal.BorderForeground(th.Accent.paint())
	cs.spin = lipgloss.NewStyle().Foreground(th.Accent.paint())
	if th.Bg.Light != "" || th.Bg.Dark != "" {
		cs.hasCanvas = true
		cs.canvas = lipgloss.NewStyle().Foreground(th.User.paint()).Background(th.Bg.paint())
	}
	return cs
}

func (cs chatStyles) applyCanvas(s string, width, height int) string {
	if !cs.on || !cs.hasCanvas {
		return s
	}
	w := max(1, width)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		pad := w - lipgloss.Width(l)
		if pad < 0 {
			pad = 0
		}
		lines[i] = cs.canvas.Render(l + strings.Repeat(" ", pad))
	}
	for len(lines) < height {
		lines = append(lines, cs.canvas.Render(strings.Repeat(" ", w)))
	}
	return strings.Join(lines, "\n")
}

// Speaker classes for a stored line.
const (
	classNone = iota
	classUser
	classAssistant
	classSystem
	classTool
	classWarn
	classThinking
	classEdit
)

// classify maps a stored "[tag] text" line to a speaker class and the text to
// show. System lines are shown as a cleaned "tag rest" (brackets dropped). A
// line with no leading tag — a wrapped continuation of a multi-line reply or a
// help/model sub-line — returns classNone so the caller reuses the prior
// speaker's rail.
func classify(raw string) (int, string) {
	if !strings.HasPrefix(raw, "[") {
		return classNone, raw
	}
	tag, rest, ok := strings.Cut(raw, " ")
	if !ok {
		tag, rest = raw, ""
	}
	switch tag {
	case tagUser:
		return classUser, rest
	case tagReply, tagSummary:
		return classAssistant, rest
	case tagStatus:
		return classSystem, rest
	case tagModel:
		return classThinking, rest
	case tagTool, tagToolOut:
		return classTool, rest
	case tagDiff:
		return classEdit, rest
	case tagWarn:
		return classWarn, rest
	default:
		word := strings.TrimSuffix(strings.TrimPrefix(tag, "["), "]")
		if rest != "" {
			word += " " + rest
		}
		return classSystem, word
	}
}

// conversation paints stored tagged lines as a labelled transcript: user
// prompts, assistant prose, tool activity, warnings, and edits stay visually
// distinct even when colour is disabled.
func (cs chatStyles) conversation(lines []string, width, selected int) string {
	inner := width
	if inner < 8 {
		inner = 8
	}
	var b strings.Builder
	prev := classNone
	for i := 0; i < len(lines); {
		raw := lines[i]
		class, text := classify(raw)
		if class == classNone {
			class = prev
			if class == classNone {
				class = classSystem
			}
		}
		if class != prev && prev != classNone {
			b.WriteByte('\n')
			if speaker(class) || speaker(prev) {
				b.WriteByte('\n')
			}
		}
		if speaker(class) {
			group := []string{text}
			selectedGroup := i == selected
			j := i + 1
			for ; j < len(lines); j++ {
				nextClass, nextText := classify(lines[j])
				if nextClass == classNone {
					nextClass = class
				}
				if nextClass != class {
					break
				}
				group = append(group, nextText)
				if j == selected {
					selectedGroup = true
				}
			}
			b.WriteString(cs.speakerBlock(class, group, inner, selectedGroup))
			b.WriteByte('\n')
			prev = class
			i = j
			continue
		}
		if class == classTool {
			if strings.HasPrefix(raw, tagToolOut) {
				for _, row := range wrapText(text, max(1, inner-4)) {
					b.WriteString(cs.toolRow(row, inner, false))
					b.WriteByte('\n')
				}
			} else {
				b.WriteString(cs.toolRow(text, inner, i == selected))
				b.WriteByte('\n')
			}
			prev = class
			i++
			continue
		}
		// A run of system rows (a /help screen, a policy trail) carries one
		// label, not one per row: the repeated word is louder than the text.
		b.WriteString(cs.labelledBlock(class, text, inner, i == selected, class == prev))
		b.WriteByte('\n')
		prev = class
		i++
	}
	return strings.TrimRight(b.String(), "\n")
}

func mdHeading(s string) (string, bool) {
	n := 0
	for n < len(s) && s[n] == '#' {
		n++
	}
	if n < 1 || n > 3 || n == len(s) || s[n] != ' ' {
		return "", false
	}
	return strings.TrimSpace(s[n+1:]), true
}

func thCodeFg() lipgloss.AdaptiveColor {
	return lipgloss.AdaptiveColor{Light: "#1F2328", Dark: "#E6EDF3"}
}

func thCodeBg() lipgloss.AdaptiveColor {
	return lipgloss.AdaptiveColor{Light: "#F4F5F7", Dark: "#161B22"}
}

func speaker(class int) bool { return class == classUser || class == classAssistant }

func (cs chatStyles) toolRow(text string, inner int, selected bool) string {
	return cs.bodyBlock(classTool, text, inner, selected)
}

func (cs chatStyles) speakerBlock(class int, texts []string, inner int, selected bool) string {
	bodyW := max(1, inner-4)
	var rows []string
	inFence := false
	for _, text := range texts {
		if class == classAssistant {
			rows = append(rows, cs.assistantRows(text, bodyW, &inFence)...)
			continue
		}
		wrapped := wrapText(text, bodyW)
		if len(wrapped) == 0 {
			rows = append(rows, "")
			continue
		}
		if cs.on {
			style := cs.messageTextStyle(class)
			for i, row := range wrapped {
				wrapped[i] = style.Render(row)
			}
		}
		rows = append(rows, wrapped...)
	}
	return cs.messageBox(classLabel(class), rows, inner, selected)
}

func (cs chatStyles) assistantRows(text string, bodyW int, inFence *bool) []string {
	trim := strings.TrimSpace(text)
	if strings.HasPrefix(trim, "```") {
		*inFence = !*inFence
		return nil
	}
	if *inFence {
		if runeLen(text) > bodyW {
			text = fit(text, bodyW)
		}
		if cs.on {
			text = lipgloss.NewStyle().Foreground(thCodeFg()).Background(thCodeBg()).Render(" " + text + " ")
		}
		return []string{text}
	}
	if title, ok := mdHeading(text); ok {
		if cs.on {
			title = cs.header.Render(title)
		}
		return []string{title}
	}
	rendered := cs.paintMarkdownLine(text, bodyW, false)
	if rendered == "" {
		return []string{""}
	}
	return strings.Split(rendered, "\n")
}

// bodyBlock wraps one stored line with a stable label column so model output,
// tool activity, edits, and warnings do not collapse into one log-like stream.
func (cs chatStyles) bodyBlock(class int, text string, inner int, selected bool) string {
	return cs.labelledBlock(class, text, inner, selected, false)
}

// labelledBlock renders one transcript row. cont blanks the label gutter,
// marking a row that continues the block above it.
func (cs chatStyles) labelledBlock(class int, text string, inner int, selected, cont bool) string {
	label := classLabel(class)
	if cont {
		label = strings.Repeat(" ", len(label))
	}
	if speaker(class) {
		avail := max(1, inner-4)
		rows := wrapText(text, avail)
		if cs.on {
			style := cs.messageTextStyle(class)
			for i, r := range rows {
				rows[i] = style.Render(r)
			}
		}
		return cs.messageBox(label, rows, inner, selected)
	}
	pad := fit(label, transcriptLabelWidth)
	if label != "" {
		pad += " "
	}
	if selected {
		pad = cs.glyph("▶", cs.accent) + " " + pad
	}
	style := cs.messageTextStyle(class)
	avail := max(1, inner-lipgloss.Width(pad))
	rows := wrapText(text, avail)
	for i, r := range rows {
		if cs.on {
			r = style.Render(r)
		}
		lead := pad
		if i > 0 && label != "" {
			lead = strings.Repeat(" ", lipgloss.Width(pad))
		}
		rows[i] = lead + r
	}
	return strings.Join(rows, "\n")
}

const transcriptLabelWidth = 9

func (cs chatStyles) messageBox(label string, rows []string, inner int, selected bool) string {
	if inner < 8 {
		inner = 8
	}
	bodyW := max(1, inner-4)
	title := strings.TrimSpace(label)
	if title == "" {
		title = "Note"
	}
	headLabel := " " + title + " "
	ruleW := max(1, inner-lipgloss.Width(headLabel)-2)
	top := "╭" + headLabel + strings.Repeat("─", ruleW) + "╮"
	bot := "╰" + strings.Repeat("─", max(1, inner-2)) + "╯"
	if selected {
		top = cs.glyph("▶", cs.accent) + " " + top
	}
	if cs.on {
		top = cs.frameText(top)
		bot = cs.frameText(bot)
	}
	out := []string{top}
	if len(rows) == 0 {
		rows = []string{""}
	}
	for _, row := range rows {
		if lipgloss.Width(row) > bodyW && !strings.Contains(row, "\x1b[") {
			row = fit(row, bodyW)
		}
		pad := max(0, bodyW-lipgloss.Width(row))
		line := "│ " + row + strings.Repeat(" ", pad) + " │"
		if cs.on {
			line = cs.frameText("│") + " " + row + strings.Repeat(" ", pad) + " " + cs.frameText("│")
		}
		out = append(out, line)
	}
	out = append(out, bot)
	return strings.Join(out, "\n")
}

func (cs chatStyles) messageTextStyle(class int) lipgloss.Style {
	if !cs.on {
		return lipgloss.NewStyle()
	}
	switch class {
	case classAssistant:
		return cs.accent
	case classWarn:
		return cs.warn
	case classUser:
		return cs.dim
	case classTool, classThinking, classSystem:
		return cs.dim
	case classEdit:
		return cs.ok
	default:
		return lipgloss.NewStyle()
	}
}

func classLabel(class int) string {
	label := "Note"
	switch class {
	case classUser:
		label = "You"
	case classAssistant:
		label = "Ink"
	case classTool:
		label = "Tool"
	case classWarn:
		label = "Warning"
	case classThinking:
		label = "Thinking"
	case classEdit:
		label = "Edit"
	}
	return label
}

func (cs chatStyles) glyph(g string, st lipgloss.Style) string {
	if !cs.on {
		return g
	}
	return st.Render(g)
}

// overlayTitle renders an overlay's caption in the header style.
func (cs chatStyles) overlayTitle(s string) string {
	if !cs.on {
		return s
	}
	return cs.header.Render(s)
}

// dimText renders chrome text quietly, or plain when colour is off.
func (cs chatStyles) dimText(s string) string {
	if !cs.on {
		return s
	}
	return cs.dim.Render(s)
}

func (cs chatStyles) frameText(s string) string {
	if !cs.on {
		return s
	}
	return cs.dim.Render(s)
}

func (cs chatStyles) selectionLine(s string, start, end int) string {
	if !cs.on {
		return s
	}
	plain := ansiEscapeRE.ReplaceAllString(s, "")
	runes := []rune(plain)
	start = max(0, min(start, len(runes)))
	end = max(start, min(end, len(runes)))
	return string(runes[:start]) + "\x1b[7m" + string(runes[start:end]) + "\x1b[27m" + string(runes[end:])
}

// welcome is the idle empty-session pane: a drop, the name, one line of
// copy, and one example. Vertical placement is paneView's job.
func (cs chatStyles) welcome(width, height int) string {
	_ = height
	mark := inkMarkLarge
	if width < 34 {
		mark = inkMarkSmall
	}
	paint := func(s string, st lipgloss.Style) string {
		if !cs.on || s == "" {
			return s
		}
		return st.Render(s)
	}
	var lines []string
	for _, l := range strings.Split(mark, "\n") {
		lines = append(lines, centerLine(paint(l, cs.accent), width))
	}
	ask := "Ask anything."
	ex := `"what does this repo do?"`
	lines = append(lines,
		"",
		centerLine(paint("Ink", cs.header), width),
		"",
		centerLine(paint(ask, cs.dim), width),
		centerLine(paint(ex, cs.dim), width),
	)
	return strings.Join(lines, "\n")
}

// centerLine left-pads s so it sits in the middle of width. It never
// right-pads: trailing spaces would copy with the row.
func centerLine(s string, width int) string {
	s = strings.TrimRight(s, " \t")
	if s == "" || width <= 0 {
		return s
	}
	w := lipgloss.Width(s)
	if w >= width {
		return fit(s, width)
	}
	return strings.Repeat(" ", (width-w)/2) + s
}

const inkMarkLarge = `        ╱╲
       ╱  ╲
      ╱    ╲
     ╱      ╲
    ╱        ╲
    ╲        ╱
     ╲      ╱
      ╲────╱`

const inkMarkSmall = `    ╱╲
   ╱  ╲
  ╱    ╲
  ╲    ╱
   ╲──╱`

type hint struct{ key, label string }

func (cs chatStyles) hintBar(hints []hint, width int) string {
	parts := make([]string, 0, len(hints))
	plain := make([]string, 0, len(hints))
	for _, h := range hints {
		plain = append(plain, h.key+" "+h.label)
		key, lab := h.key, h.label
		if cs.on {
			key = cs.accent.Bold(true).Render(h.key)
			lab = cs.dimText(h.label)
		}
		parts = append(parts, key+" "+lab)
	}
	sep := " · "
	if cs.on {
		sep = cs.dimText("  ·  ")
	}
	joined := strings.Join(parts, sep)
	if lipgloss.Width(joined) <= width {
		return joined
	}
	return cs.dimText(fit(strings.Join(plain, " · "), max(1, width)))
}

func (cs chatStyles) hintLine(s string, width int) string {
	return cs.dimText(fit(s, max(1, width)))
}

// headerBar is one quiet row: identity left, context right. left/right may
// already be styled; right is dimmed when colour is on.
func (cs chatStyles) headerBar(left, right string, width int) string {
	if cs.on {
		right = cs.dimText(right)
	}
	return padBetween(left, right, width)
}

// shortRoute is the model id without provider or accounts/…/models/ prefix.
func shortRoute(route string) string {
	r := strings.TrimSpace(route)
	if r == "" {
		return ""
	}
	if i := strings.LastIndex(r, "/models/"); i >= 0 {
		return r[i+len("/models/"):]
	}
	if i := strings.LastIndex(r, "/"); i >= 0 && i+1 < len(r) {
		return r[i+1:]
	}
	return r
}

// padBetween places left and right on one row, padding the gap to width. It
// measures display width, so embedded colour codes do not skew the spacing.
func padBetween(left, right string, width int) string {
	if lipgloss.Width(right) == 0 {
		return left // nothing to right-align; padding would only poison copies
	}
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

// wrapText soft-wraps on spaces to width columns, hard-breaking any single
// word longer than width. It counts runes, close enough for the redacted,
// whitespace-collapsed summaries shown here.
func wrapText(s string, width int) []string {
	if width < 1 {
		width = 1
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		line := ""
		for _, w := range words {
			if line == "" {
				line = w
			} else if runeLen(line)+1+runeLen(w) <= width {
				line += " " + w
			} else {
				out = append(out, line)
				line = w
			}
			for runeLen(line) > width {
				r := []rune(line)
				out = append(out, string(r[:width]))
				line = string(r[width:])
			}
		}
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func runeLen(s string) int { return len([]rune(s)) }

// fit truncates s to width runes, ending in an ellipsis when cut. Apply it
// to plain text before styling; it is not ANSI-aware.
func fit(s string, width int) string {
	if width < 1 {
		return ""
	}
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	return string(r[:width-1]) + "…"
}
