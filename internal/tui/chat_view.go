package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/ataidesorg/friday/internal/core"
)

// promptRows is the prompt's height in rows: one for a fresh draft, growing
// with the draft's lines up to promptMaxRows.
func (m ChatModel) promptRows() int {
	limit := promptMaxRows
	if m.height > 0 {
		limit = min(promptMaxRows, max(6, m.height/2))
	}
	return min(max(m.ta.LineCount(), 1), limit)
}

// slashEntry is one row of the typeahead menu for a drafted /command.
type slashEntry struct {
	name, detail, group, title string
	custom                     bool
}

// slashMatches is the ranked command menu for the current draft. Empty unless
// the draft is a single-line "/prefix" that has not reached its arguments,
// and always empty under an overlay or a pending approval.
func (m ChatModel) slashMatches() []slashEntry {
	d := m.ta.Value()
	if m.ov != nil || m.pending != nil || m.question != nil || m.conn != nil || !strings.HasPrefix(d, "/") || strings.ContainsAny(d, " \n") {
		return nil
	}
	q := strings.TrimPrefix(d, "/")
	type ranked struct {
		e    slashEntry
		rank int
		idx  int
	}
	var hits []ranked
	add := func(e slashEntry, idx int) {
		r, ok := slashRank(q, e)
		if !ok {
			return
		}
		hits = append(hits, ranked{e: e, rank: r, idx: idx})
	}
	for i, e := range builtinSlash() {
		if e.name == "exit" && q == "" {
			continue
		}
		add(e, i)
	}
	base := len(builtinSlash())
	for i, c := range m.commands {
		add(slashEntry{name: c.Name, detail: c.Description, group: gCmds, title: c.Name, custom: true}, base+i)
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].rank != hits[j].rank {
			return hits[i].rank < hits[j].rank
		}
		return hits[i].idx < hits[j].idx
	})
	out := make([]slashEntry, len(hits))
	for i, h := range hits {
		out[i] = h.e
	}
	return out
}

// slashRank scores a command against the typed prefix. Lower is better.
// Empty query keeps catalog order. Name prefix, then substring, then a
// tight subsequence of the command name — never the description.
func slashRank(q string, e slashEntry) (int, bool) {
	if q == "" {
		return 0, true
	}
	ql := strings.ToLower(q)
	name := strings.ToLower(e.name)
	switch {
	case name == ql:
		return 0, true
	case strings.HasPrefix(name, ql):
		return 1, true
	case strings.Contains(name, ql):
		return 3, true
	}
	r, ok := fuzzyRank(q, e.name)
	if !ok {
		return 0, false
	}
	if r > len(ql)+1 {
		return 0, false
	}
	return 10 + r, true
}

func (m ChatModel) slashMenuLimit() int {
	h := m.height
	if h <= 0 {
		h = defaultHeight
	}
	limit := h - chatChrome - m.promptRows() - promptFrame - footerRows - 6
	if limit < 4 {
		limit = 4
	}
	return limit
}

func (m ChatModel) slashWindow(menu []slashEntry) (int, []slashEntry) {
	limit := m.slashMenuLimit()
	if len(menu) <= limit {
		return 0, menu
	}
	sel := min(max(m.slashSel, 0), len(menu)-1)
	start := 0
	if sel >= limit {
		start = sel - limit + 1
	}
	return start, menu[start : start+limit]
}

// lastToken is the draft's final whitespace-separated token; the @-file
// typeahead completes it in place.
func lastToken(s string) string {
	if i := strings.LastIndexAny(s, " \t\n"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// atMatches is the @-file typeahead for the draft's last token: files whose
// path contains the fragment after "@". Empty without a completer, under an
// overlay or approval, or after esc dismissed this token's menu.
func (m ChatModel) atMatches() []string {
	if m.files == nil || m.ov != nil || m.pending != nil || m.question != nil || m.conn != nil {
		return nil
	}
	tok := lastToken(m.ta.Value())
	if !strings.HasPrefix(tok, "@") || tok == m.atGone {
		return nil
	}
	out := m.files(strings.TrimPrefix(tok, "@"))
	if len(out) > slashMenuMax {
		out = out[:slashMenuMax]
	}
	return out
}

// atMenuView renders the @-file rows shown above the prompt.
func (m ChatModel) atMenuView(menu []string) string {
	sel := min(m.atSel, len(menu)-1)
	rows := make([]string, len(menu))
	for i, name := range menu {
		marker := "  "
		if i == sel {
			marker = m.cstyle.glyph("▍", m.cstyle.accent) + " "
		}
		rows[i] = marker + "@" + name
	}
	return strings.Join(rows, "\n")
}

// slashMenuView renders the typeahead rows shown above the prompt.
func (m ChatModel) slashMenuView(menu []slashEntry) string {
	sel := min(m.slashSel, len(menu)-1)
	start, win := m.slashWindow(menu)
	w := max(24, m.innerWidth())
	q := strings.TrimPrefix(m.ta.Value(), "/")
	grouped := q == ""
	var rows []string
	prev := ""
	for i, e := range win {
		if grouped && e.group != "" && e.group != prev {
			if prev != "" {
				rows = append(rows, "")
			}
			label := e.group
			prefix := "  " + label + " "
			rule := max(1, w-runeLen(prefix))
			rows = append(rows, m.cstyle.dimText(prefix+strings.Repeat("─", rule)))
			prev = e.group
		}
		rows = append(rows, m.slashRow(e, start+i == sel, w))
	}
	if len(menu) > len(win) {
		rows = append(rows, m.cstyle.dimText(fmt.Sprintf("  %d/%d", sel+1, len(menu))))
	}
	return m.cstyle.modal.Width(w).Padding(0, 0).Render(strings.Join(rows, "\n"))
}

func (m ChatModel) slashRow(e slashEntry, selected bool, w int) string {
	avail := max(8, w)
	mark := "  "
	if selected {
		mark = "▸ "
	}
	name := "/" + e.name
	right := ""
	if e.custom {
		right = "custom"
	}
	leftBudget := avail - runeLen(mark)
	if right != "" {
		leftBudget = max(6, avail-runeLen(mark)-runeLen(right)-2)
	}
	title := fit(name, min(runeLen(name), leftBudget))
	left := mark + title
	rest := leftBudget - runeLen(title)
	if e.detail != "" && rest >= 4 {
		left += "  " + fit(e.detail, rest-2)
	}
	plain := left
	if right != "" {
		plain = padBetween(left, right, avail)
	} else if pad := avail - runeLen(plain); pad > 0 {
		plain += strings.Repeat(" ", pad)
	}
	if selected {
		if m.cstyle.on {
			return lipgloss.NewStyle().Foreground(thCodeFg()).Background(thCodeBg()).Render(plain)
		}
		return plain
	}
	if !m.cstyle.on {
		return strings.TrimRight(plain, " ")
	}
	styled := mark + m.cstyle.accent.Render(title)
	if e.detail != "" && rest >= 4 {
		styled += "  " + m.cstyle.dimText(fit(e.detail, rest-2))
	}
	if right == "" {
		return styled
	}
	return padBetween(styled, m.cstyle.dimText(right), avail)
}

func (m ChatModel) slashMenuRows(menu []slashEntry) int {
	if len(menu) == 0 {
		return 0
	}
	return len(strings.Split(m.slashMenuView(menu), "\n"))
}

func stampLines(lines []string, times []time.Time, now time.Time) []string {
	out := make([]string, len(lines))
	for i, raw := range lines {
		when := now
		if i < len(times) && !times[i].IsZero() {
			when = times[i]
		}
		stamp := "[" + when.Format("15:04") + "]"
		if strings.HasPrefix(raw, "[") {
			tag, rest, ok := strings.Cut(raw, " ")
			if ok {
				out[i] = tag + " " + stamp + " " + rest
				continue
			}
		}
		out[i] = stamp + " " + raw
	}
	return out
}

func (m ChatModel) innerWidth() int {
	return max(16, m.width-2*framePad)
}

func (m ChatModel) layout() ChatModel {
	rows := m.promptRows()
	if m.conn != nil {
		rows = 1 // the connect wizard is a single-line prompt
	}
	inner := m.innerWidth()
	m.ta.SetWidth(max(1, inner-4)) // box borders + 1 cell pad each side
	m.ta.SetHeight(rows)
	m.vp.Width = inner
	menuRows := m.slashMenuRows(m.slashMatches()) + len(m.atMatches()) + m.queueRows()
	m.vp.Height = max(1, m.height-chatChrome-rows-promptFrame-footerRows-menuRows)
	lines := m.Lines
	times := m.lineTimes
	if m.reply != "" {
		// The live reply renders after the committed lines without joining
		// them; finish moves it into Lines.
		reply := replyLines(m.reply)
		lines = append(slices.Clip(lines), reply...)
		for range reply {
			times = append(slices.Clip(times), m.now())
		}
	}
	if !m.toolsOpen {
		kept := lines[:0:0]
		keptTimes := times[:0:0]
		for i, raw := range lines {
			if strings.HasPrefix(raw, tagToolOut) {
				continue
			}
			kept = append(kept, raw)
			if i < len(times) {
				keptTimes = append(keptTimes, times[i])
			}
		}
		lines = kept
		times = keptTimes
	}
	if !m.showTools {
		kept := lines[:0:0]
		keptTimes := times[:0:0]
		for i, raw := range lines {
			if strings.HasPrefix(raw, tagTool) || strings.HasPrefix(raw, tagToolOut) {
				continue
			}
			kept = append(kept, raw)
			if i < len(times) {
				keptTimes = append(keptTimes, times[i])
			}
		}
		lines = kept
		times = keptTimes
	}
	if m.showTimes {
		lines = stampLines(lines, times, m.now())
	}
	switch {
	case (len(lines) == 0 && !m.Running) || m.homeOpen:
		m.homePane = true
		m.pane = m.cstyle.welcome(m.innerWidth(), m.vp.Height)
	default:
		m.homePane = false
		sel := -1
		if !m.promptFocus {
			sel = m.sel
		}
		m.pane = m.cstyle.conversation(lines, m.innerWidth(), sel)
		if m.Running && m.reply == "" && m.showThinking {
			// Nothing has streamed yet: show a live thinking line where the
			// reply will land. The spinner tick relays while Running.
			m.pane += "\n\n" + m.sp.View() + " " + m.cstyle.dimText("thinking…")
		}
	}
	m.vp.SetContent(m.pane)
	if m.followTail {
		m.vp.GotoBottom()
	}
	return m
}

// paneView renders the viewport's visible slice without the component's own
// View, which pads every row to full width; padded rows poison terminal
// selection with trailing blanks, so text ends at its last glyph.
// A short transcript is pinned to the bottom of the pane (just above the
// composer). Empty rows then sit above the text, so a native copy of the
// reply does not drag a screenful of blank lines with it.
func (m ChatModel) paneView() string {
	lines := m.paneLines()
	h := max(1, m.vp.Height)
	if top, _, ok := m.homeSplit(); ok {
		// Home is a splash, not the tail of a transcript: the blank rows the
		// composer does not use go below it, so the whole block sits in the
		// middle of the screen.
		return strings.Repeat("\n", top) + strings.Join(lines, "\n") + "\n"
	}
	if len(lines) >= h {
		top := min(max(m.vp.YOffset, 0), len(lines)-h)
		return strings.Join(lines[top:top+h], "\n")
	}
	if len(lines) == 0 {
		return strings.Repeat("\n", h-1)
	}
	return strings.Repeat("\n", h-len(lines)) + strings.Join(lines, "\n")
}

// paneLines is the pane's content without its trailing blank rows.
func (m ChatModel) paneLines() []string {
	lines := strings.Split(m.pane, "\n")
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// homeSplit divides the pane's spare rows for the idle home layout: top go
// above the welcome block, bottom below the composer, so the two are one
// centered group. It is off whenever something else needs the whole pane —
// an overlay, the todo panel, a mouse selection — or the block does not fit.
func (m ChatModel) homeSplit() (top, bottom int, ok bool) {
	if !m.homePane || m.ov != nil || m.todosOpen || m.mouseCopy {
		return 0, 0, false
	}
	free := max(1, m.vp.Height) - len(m.paneLines()) - 1 // one row of air over the composer
	if free <= 0 {
		return 0, 0, false
	}
	return free / 2, free - free/2, true
}

// View implements tea.Model.
func (m ChatModel) View() string {
	if m.dash {
		return m.cstyle.applyCanvas(m.dashView(), m.width, m.height)
	}
	pane := m.paneView()
	if m.mouseCopy {
		pane = m.highlightMouseCopy(pane)
	}
	if m.todosOpen {
		base := strings.Split(pane, "\n")
		for i, l := range strings.Split(m.todosView(m.innerWidth(), m.vp.Height), "\n") {
			if i < len(base) && l != "" {
				base[i] = l
			}
		}
		pane = strings.Join(base, "\n")
	}
	if m.ov != nil {
		// The overlay floats as a centered modal; conversation rows show
		// through above and below it (blank modal rows keep the pane row).
		base := strings.Split(pane, "\n")
		for i, l := range strings.Split(m.ov.view(m.cstyle, m.innerWidth(), m.vp.Height), "\n") {
			if i < len(base) && l != "" {
				base[i] = l
			}
		}
		pane = strings.Join(base, "\n")
	}
	inner := m.innerWidth()
	parts := []string{
		m.cstyle.headerBar(m.headerLeft(), m.headerRight(), inner),
		pane,
	}
	if menu := m.slashMatches(); len(menu) > 0 {
		parts = append(parts, m.slashMenuView(menu))
	}
	if menu := m.atMatches(); len(menu) > 0 {
		parts = append(parts, m.atMenuView(menu))
	}
	if q := m.queueStrip(inner); q != "" {
		parts = append(parts, q)
	}
	prompt := trimRightLines(m.ta.View())
	switch {
	case m.conn != nil:
		prompt = m.connectPromptView()
	case m.question != nil:
		prompt = m.question.view(m.cstyle.on, func(s string) string { return m.cstyle.accent.Render(s) }, m.cstyle.dimText)
	case m.pending != nil:
		prompt = m.approvalPromptView()
	}
	parts = append(parts, m.composerBox(prompt), m.footerView())
	if _, bottom, ok := m.homeSplit(); ok && bottom > 0 {
		parts = append(parts, strings.Repeat("\n", bottom-1))
	}
	return m.cstyle.applyCanvas(padFrame(strings.Join(parts, "\n"), framePad), m.width, m.height)
}

func (m ChatModel) highlightMouseCopy(pane string) string {
	rows := strings.Split(pane, "\n")
	if len(rows) == 0 {
		return pane
	}
	rng := normalizeCellRange(
		cellPos{x: m.mouseStartX, y: m.mouseStartY},
		cellPos{x: m.mouseEndX, y: m.mouseEndY},
	)
	for y := max(0, rng.start.y); y <= min(len(rows)-1, rng.end.y); y++ {
		start, end := rng.colsForRow(y)
		rows[y] = m.cstyle.selectionLine(rows[y], start, end)
	}
	return strings.Join(rows, "\n")
}

func padFrame(s string, pad int) string {
	if pad <= 0 {
		return s
	}
	sp := strings.Repeat(" ", pad)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if l == "" {
			continue
		}
		lines[i] = sp + l
	}
	return strings.Join(lines, "\n")
}

func (m ChatModel) composerBox(body string) string {
	w := m.innerWidth()
	inner := max(1, w-2)
	lines := strings.Split(trimRightLines(body), "\n")
	if len(lines) == 0 {
		lines = []string{""}
	}
	top := "╭" + strings.Repeat("─", inner) + "╮"
	bot := composerBottom(m.composerLabel(), inner, m.cstyle)
	out := make([]string, 0, len(lines)+2)
	out = append(out, m.cstyle.frameText(top))
	for _, l := range lines {
		lw := lipgloss.Width(l)
		pad := inner - lw
		if pad < 0 {
			l = fit(l, inner)
			pad = 0
		}
		out = append(out, m.cstyle.frameText("│")+l+strings.Repeat(" ", pad)+m.cstyle.frameText("│"))
	}
	out = append(out, bot)
	return strings.Join(out, "\n")
}

func (m ChatModel) composerLabel() string {
	var parts []string
	if r := shortRoute(m.route); r != "" {
		parts = append(parts, r)
	}
	if lab := m.permLabel(); lab != "" {
		parts = append(parts, m.modeText(lab))
	}
	if m.usageOpen {
		if lab := m.usageLabel(); lab != "" {
			parts = append(parts, lab)
		}
	}
	return strings.Join(parts, " · ")
}

func composerBottom(label string, inner int, cs chatStyles) string {
	if label == "" {
		return cs.frameText("╰" + strings.Repeat("─", inner) + "╯")
	}
	lab := " " + label + " "
	if lipgloss.Width(lab) > inner-2 {
		lab = " " + fit(label, max(1, inner-4)) + " "
	}
	// One color for the whole line so the corner glyph is not split
	// across ANSI (that reads as a broken "_" in many fonts).
	s := "╰" + strings.Repeat("─", max(0, inner-lipgloss.Width(lab))) + lab + "╯"
	return cs.frameText(s)
}

func (m ChatModel) modeText(s string) string {
	if !m.cstyle.on {
		return s
	}
	switch s {
	case "plan":
		return m.cstyle.accent.Render(s)
	case "auto":
		return m.cstyle.ok.Render(s)
	case "always-approve":
		return m.cstyle.warn.Render(s)
	case "always-ask":
		return m.cstyle.fail.Render(s)
	default:
		return m.cstyle.dimText(s)
	}
}

func (m ChatModel) usageLabel() string {
	parts := []string{}
	used, maxn := m.contextNumbers()
	if maxn > 0 {
		parts = append(parts, fmt.Sprintf("ctx %d%%", min(100, used*100/maxn)))
	} else if used > 0 {
		parts = append(parts, "ctx "+formatTokens(used))
	}
	if m.Cost.Actual != nil {
		parts = append(parts, "cost "+m.Cost.Actual.String())
	}
	if m.Budget.MaxCost > 0 {
		parts = append(parts, fmt.Sprintf("task cap $%.2f", m.Budget.MaxCost.Float()))
	}
	if m.usageLimits != "" {
		parts = append(parts, "configured caps "+m.usageLimits)
	}
	return strings.Join(parts, " ")
}

func trimRightLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.Join(lines, "\n")
}

func (m ChatModel) headerLeft() string {
	cwd := displayCwd(m.folder)
	if m.worktree != "" {
		cwd = m.worktree + "  " + cwd
	}
	if m.cstyle.on {
		cwd = m.cstyle.user.Render(cwd)
	}
	if m.branch == "" {
		return cwd
	}
	br := m.branch
	if m.dirty {
		br += "*"
	}
	if m.cstyle.on {
		br = m.cstyle.accent.Render(br)
	}
	return cwd + "  " + br
}

func displayCwd(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "friday"
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if path == home {
			return "~"
		}
		if strings.HasPrefix(path, home+string(os.PathSeparator)) {
			path = "~" + path[len(home):]
		}
	}
	if runeLen(path) <= 40 {
		return path
	}
	sep := string(os.PathSeparator)
	parts := strings.Split(filepath.Clean(path), sep)
	if len(parts) > 3 {
		return strings.Join(parts[len(parts)-3:], "/")
	}
	return path
}

func (m ChatModel) headerRight() string {
	used, maxn := m.contextNumbers()
	if maxn <= 0 && used <= 0 {
		return ""
	}
	if maxn <= 0 {
		return formatTokens(used)
	}
	return formatTokens(used) + " / " + formatTokens(maxn)
}

func (m ChatModel) contextNumbers() (int, int) {
	used, maxn := m.ctxUsed, m.ctxMax
	if used == 0 && (m.Usage.InputTokens > 0 || m.Usage.OutputTokens > 0) {
		used = int(m.Usage.InputTokens + m.Usage.OutputTokens)
	}
	return used, maxn
}

func formatTokens(n int) string {
	if n <= 0 {
		return "0"
	}
	if n < 1000 {
		return strconv.Itoa(n)
	}
	if n < 10_000 {
		s := fmt.Sprintf("%.1f", float64(n)/1000)
		return strings.TrimSuffix(s, ".0") + "k"
	}
	return fmt.Sprintf("%dk", n/1000)
}

type approvalChoice struct {
	label    string
	key      string
	decision core.ApprovalDecision
	scope    core.ApprovalScope
}

var approvalChoices = []approvalChoice{
	{label: "Allow once", key: "y", decision: core.ApprovalApproved, scope: core.ApprovalOnce},
	{label: "Allow this session", key: "s", decision: core.ApprovalApproved, scope: core.ApprovalSession},
	{label: "Reject", key: "n", decision: core.ApprovalDenied, scope: core.ApprovalOnce},
}

func approvalTitle(a core.Approval) string {
	r := a.Request
	sc := r.Capability.Scope
	switch {
	case sc.Path != "" && r.Tool == "write_file":
		return "Write " + sc.Path
	case sc.Path != "" && r.Tool == "apply_patch":
		return "Patch " + sc.Path
	case sc.Path != "":
		return r.Tool + " " + sc.Path
	case len(sc.Argv) > 0:
		return "Run " + strings.Join(sc.Argv, " ")
	case r.Tool != "":
		return r.Tool
	default:
		return "tool"
	}
}

func approvalRiskLabel(a core.Approval) string {
	r := a.Request
	parts := []string{r.Tool}
	if r.Capability.Risk != "" {
		parts = append(parts, strings.ReplaceAll(string(r.Capability.Risk), "_", " "))
	}
	return strings.Join(parts, " · ")
}

// approvalPromptView is the composer card: action, risk, preview, then a
// selectable list of decisions. Arrow keys move; y/s/n still work.
func (m ChatModel) approvalPromptView() string {
	if m.pending == nil {
		return ""
	}
	a := *m.pending
	var b strings.Builder
	title := approvalTitle(a)
	if m.cstyle.on {
		b.WriteString(m.cstyle.header.Render(title))
	} else {
		b.WriteString(title)
	}
	b.WriteByte('\n')
	risk := approvalRiskLabel(a)
	if m.cstyle.on {
		b.WriteString(m.cstyle.dimText(risk))
	} else {
		b.WriteString(risk)
	}
	preview := strings.TrimSpace(a.Preview)
	if preview != "" {
		b.WriteByte('\n')
		lines := strings.Split(preview, "\n")
		if len(lines) > 8 {
			lines = append(lines[:8], fmt.Sprintf("… (%d more lines)", len(lines)-8))
		}
		for _, l := range lines {
			b.WriteByte('\n')
			if m.cstyle.on {
				b.WriteString(m.cstyle.dimText("  " + l))
			} else {
				b.WriteString("  " + l)
			}
		}
	}
	b.WriteByte('\n')
	sel := m.approvalSel
	if sel < 0 || sel >= len(approvalChoices) {
		sel = 0
	}
	for i, c := range approvalChoices {
		mark := "  "
		if i == sel {
			mark = "▸ "
		}
		line := fmt.Sprintf("%s%s  (%s)", mark, c.label, c.key)
		b.WriteByte('\n')
		switch {
		case i == sel && m.cstyle.on && c.decision == core.ApprovalDenied:
			b.WriteString(m.cstyle.fail.Render(line))
		case i == sel && m.cstyle.on:
			b.WriteString(m.cstyle.accent.Render(line))
		case m.cstyle.on:
			b.WriteString(m.cstyle.dimText(line))
		default:
			b.WriteString(line)
		}
	}
	return b.String()
}

// footerView is the persistent key-hint bar under the prompt. It rewrites
// itself with the keys that work in the current state, like OpenCode.
func (m ChatModel) footerView() string {
	var h string
	switch {
	case len(m.slashMatches()) > 0 && m.pending == nil && m.ov == nil && m.question == nil && m.conn == nil:
		h = "enter run · tab complete · ↑↓ · esc"
	case m.question != nil:
		h = "1–9 select · enter confirm · esc skip"
	case m.pending != nil:
		h = "↑↓ choose · enter · y once · s session · n reject · esc reject"
	case m.ov != nil:
		h = "type to filter · ↑↓ move · enter selects · esc closes"
	case m.conn != nil:
		h = connectHint(*m.conn)
	case m.queueOpen:
		h = "queued prompts · ctrl+b closes · esc closes"
	case !m.promptFocus:
		h = "tab/space prompt · ↑↓ entries"
		if m.vim {
			h = "j/k entries · y copy · i prompt"
		}
		if m.Running {
			h = "esc/ctrl+c cancel · " + h
		}
	case m.Running:
		elapsed := m.now().Sub(m.turnStart).Round(time.Millisecond)
		h = "working " + elapsed.String() + " · esc/ctrl+c cancel"
		if n := len(m.queue); n > 0 {
			h += fmt.Sprintf(" · %d queued", n)
		}
	default:
		if m.quitHint && m.quitKey != "" {
			return m.cstyle.hintBar([]hint{{m.quitKey, "again to quit"}}, m.innerWidth())
		}
		if m.escHint {
			return m.cstyle.hintBar([]hint{{"esc", "again to clear"}}, m.innerWidth())
		}
		if m.notice != "" {
			return m.cstyle.hintLine(m.notice, m.innerWidth())
		}
		return m.cstyle.hintBar([]hint{
			{"enter", "send"},
			{"/", "commands"},
			{m.keys.Palette.String(), "palette"},
			{"ctrl+c", "quit"},
		}, m.innerWidth())
	}
	if m.notice != "" {
		h = m.notice + " · " + h
	}
	return m.cstyle.hintLine(h, m.innerWidth())
}

// userLines/replyLines tag a prompt or reply, one scrollback line per text
// line, dropping trailing blank lines.
func userLines(s string) []string  { return taggedLines(tagUser, s) }
func replyLines(s string) []string { return taggedLines(tagReply, s) }

func taggedLines(tag, s string) []string {
	s = strings.TrimRight(s, "\n")
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, l := range strings.Split(s, "\n") {
		out = append(out, tag+" "+strings.TrimRight(l, "\r"))
	}
	return out
}
