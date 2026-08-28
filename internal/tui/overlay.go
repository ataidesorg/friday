package tui

import (
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// overlayKind selects what an overlay lists and how a commit dispatches.
type overlayKind int

const (
	overlayPalette      overlayKind = iota // ctrl+p — fuzzy search over every action
	overlayModels                          // /model — pick the active route
	overlayThemes                          // pick the palette
	overlayAgents                          // /agent — pick an agent profile
	overlayConnect                         // /connect — pick a provider to configure
	overlayConnectModel                    // /connect — pick a model the credential can reach
	overlaySessions                        // /resume — pick a prior session
	overlayHistory                         // /history — pick a prior prompt
	overlayRewind                          // /rewind — pick a user turn
	overlaySkills                          // /skills — loaded skills
)

// overlayItem is one selectable row. id is handed to overlayCommit.
type overlayItem struct {
	id     string
	title  string
	detail string
	key    string
	group  string // palette section header; empty means ungrouped
	state  string // "on"/"off" for toggles; empty if not a toggle
}

// overlay is the one modal selector: a fuzzy-filtered list rendered in the
// pane region. Esc closes, enter commits, up/down move the cursor. Values
// are immutable; update returns a new copy.
type overlay struct {
	kind   overlayKind
	title  string
	items  []overlayItem
	query  string
	cursor int
}

// overlayDone reports what a key did: closed the overlay, or committed id.
type overlayDone struct {
	closed bool
	commit string
}

// matches returns the items the query keeps, best rank first.
func (o overlay) matches() []overlayItem {
	if o.query == "" {
		return o.items
	}
	type ranked struct {
		item overlayItem
		rank int
	}
	var kept []ranked
	for _, it := range o.items {
		if r, ok := fuzzyRank(o.query, it.title+" "+it.detail); ok {
			kept = append(kept, ranked{it, r})
		}
	}
	slices.SortStableFunc(kept, func(a, b ranked) int { return a.rank - b.rank })
	out := make([]overlayItem, len(kept))
	for i, r := range kept {
		out[i] = r.item
	}
	return out
}

// update applies one key. A closed or commit result tells the chat to drop
// the overlay; otherwise it keeps the returned copy.
func (o overlay) update(v tea.KeyMsg) (overlay, overlayDone) {
	switch v.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		return o, overlayDone{closed: true}
	case tea.KeyEnter:
		m := o.matches()
		if len(m) == 0 {
			return o, overlayDone{closed: true}
		}
		return o, overlayDone{commit: m[min(o.cursor, len(m)-1)].id}
	case tea.KeyUp:
		n := len(o.matches())
		if n == 0 {
			return o, overlayDone{}
		}
		if o.cursor <= 0 {
			if o.wraps() {
				o.cursor = n - 1
			}
		} else {
			o.cursor--
		}
		return o, overlayDone{}
	case tea.KeyDown:
		n := len(o.matches())
		if n == 0 {
			return o, overlayDone{}
		}
		if o.cursor >= n-1 {
			if o.wraps() {
				o.cursor = 0
			}
		} else {
			o.cursor++
		}
		return o, overlayDone{}
	case tea.KeyBackspace:
		if r := []rune(o.query); len(r) > 0 {
			o.query, o.cursor = string(r[:len(r)-1]), 0
		}
		return o, overlayDone{}
	case tea.KeyRunes, tea.KeySpace:
		s := " "
		if v.Type == tea.KeyRunes {
			s = string(v.Runes)
		}
		o.query, o.cursor = o.query+s, 0
		return o, overlayDone{}
	}
	return o, overlayDone{}
}

func (o overlay) wraps() bool {
	switch o.kind {
	case overlayPalette, overlaySkills:
		return true
	}
	return false
}

// view renders the overlay as a modal box centered in the pane region:
// title, query row, then the matched rows, framed and horizontally centered,
// so it floats over the conversation instead of flooding the corner. Rows
// gain only a left indent and vertical padding is bare newlines (copy
// hygiene); inside the frame lipgloss pads to the border.
func (o overlay) view(cs chatStyles, width, height int) string {
	switch o.kind {
	case overlayPalette, overlaySkills:
		return o.paletteView(cs, width, height)
	}
	bw := min(56, max(20, width-8)) // content cells inside the frame
	rows := []string{cs.overlayTitle(o.title), cs.dimText("› ") + o.query + cs.dimText("▏")}
	m := o.matches()
	if len(m) == 0 {
		rows = append(rows, cs.dimText("no matches — esc closes"))
	}
	listRows := max(1, height-2-len(rows)) // 2 frame rows
	cursor := min(o.cursor, max(0, len(m)-1))
	start := 0
	if cursor >= listRows {
		start = cursor - listRows + 1
	}
	for i := start; i < len(m) && i-start < listRows; i++ {
		rows = append(rows, o.row(cs, m[i], i == cursor, bw))
	}
	box := cs.modal.Width(bw + 2).Render(strings.Join(rows, "\n")) // +2 padding cells
	return centerBlock(box, width, height)
}

func (o overlay) paletteView(cs chatStyles, width, height int) string {
	bw := max(36, width-2)
	inner := max(24, bw-4)
	matches := o.matches()
	title := o.title
	if title == "" {
		title = "Commands"
	}
	ruleW := max(2, inner-lipgloss.Width(title)-6)
	head := padBetween(cs.overlayTitle(title)+" "+cs.dimText(strings.Repeat("─", ruleW)), cs.dimText("[x]"), inner)
	search := "  " + cs.dimText("search: ")
	if o.query == "" {
		search += cs.dimText("▏")
	} else {
		search += o.query + cs.dimText("▏")
	}
	rows := []string{head, "", search, cs.dimText(strings.Repeat("─", inner))}
	if len(matches) == 0 {
		rows = append(rows, cs.dimText("  no matches"))
	}

	type vis struct {
		header bool
		blank  bool
		item   overlayItem
		idx    int
	}
	var visRows []vis
	prev := ""
	for i, it := range matches {
		g := it.group
		if g == "" {
			g = "Other"
		}
		if g != prev {
			if prev != "" {
				visRows = append(visRows, vis{blank: true})
			}
			visRows = append(visRows, vis{header: true, item: overlayItem{title: g}})
			prev = g
		}
		visRows = append(visRows, vis{item: it, idx: i})
	}
	cursor := min(o.cursor, max(0, len(matches)-1))
	listRows := max(1, height-6)
	start := 0
	focus := 0
	for i, v := range visRows {
		if !v.header && !v.blank && v.idx == cursor {
			focus = i
			break
		}
	}
	if focus >= listRows {
		start = focus - listRows + 1
	}
	for i := start; i < len(visRows) && i-start < listRows; i++ {
		v := visRows[i]
		if v.blank {
			rows = append(rows, "")
			continue
		}
		if v.header {
			label := "  " + v.item.title
			line := label + " " + strings.Repeat("─", max(1, inner-1-runeLen(label)))
			rows = append(rows, cs.dimText(line))
			continue
		}
		rows = append(rows, o.paletteRow(cs, v.item, v.idx == cursor, inner))
	}
	box := cs.modal.Width(bw).Padding(0, 1).Render(strings.Join(rows, "\n"))
	return placeTop(box, width, height)
}

func placeTop(block string, width, height int) string {
	lines := strings.Split(block, "\n")
	if len(lines) > height {
		lines = lines[:max(1, height)]
	}
	indent := strings.Repeat(" ", max(0, (width-lipgloss.Width(lines[0]))/2))
	for i, l := range lines {
		lines[i] = indent + l
	}
	out := strings.Join(lines, "\n")
	if pad := height - len(lines); pad > 0 {
		out += strings.Repeat("\n", pad)
	}
	return out
}

func paletteKey(it overlayItem) string {
	if it.key != "" {
		return prettyKey(it.key)
	}
	if it.id != "" && !strings.ContainsAny(it.id, " :") {
		return "/" + it.id
	}
	return ""
}

func prettyKey(s string) string {
	if s == "" || strings.HasPrefix(s, "/") {
		return s
	}
	parts := strings.Split(s, "+")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, "+")
}

func (o overlay) paletteRow(cs chatStyles, it overlayItem, selected bool, w int) string {
	avail := max(8, w)
	key := paletteKey(it)
	state := it.state
	right := key
	if state != "" {
		if right != "" {
			right = state + "  " + right
		} else {
			right = state
		}
	}
	mark := "  ◆ "
	leftBudget := avail - runeLen(mark)
	if right != "" {
		leftBudget = max(6, avail-runeLen(mark)-runeLen(right)-2)
	}
	title := fit(it.title, leftBudget)
	left := mark + title
	rest := leftBudget - runeLen(title)
	if it.detail != "" && rest >= 4 {
		left += "  " + fit(it.detail, rest-2)
	}
	plain := left
	if right != "" {
		plain = padBetween(left, right, avail)
	}
	if selected {
		selectedLine := strings.Replace(plain, "  ◆ ", "  ▸ ", 1)
		if cs.on {
			return lipgloss.NewStyle().
				Foreground(thCodeFg()).
				Background(thCodeBg()).
				Render(selectedLine)
		}
		return selectedLine
	}
	if !cs.on {
		return plain
	}
	styledLeft := cs.glyph("◆", cs.accent) + " " + title
	if it.detail != "" && rest >= 4 {
		styledLeft += "  " + cs.dimText(fit(it.detail, rest-2))
	}
	styledLeft = "  " + styledLeft
	if right == "" {
		return styledLeft
	}
	styledRight := cs.dimText(key)
	if state != "" {
		st := cs.dimText(state)
		if state == "on" {
			st = cs.glyph(state, cs.ok)
		}
		if key != "" {
			styledRight = st + "  " + styledRight
		} else {
			styledRight = st
		}
	}
	return padBetween(styledLeft, styledRight, avail)
}

// centerBlock centers a rendered block in a width×height region. The
// vertical remainder is bare newlines, so the caller can let the pane show
// through above and below the block.
func centerBlock(block string, width, height int) string {
	lines := strings.Split(block, "\n")
	if len(lines) > height {
		lines = lines[:max(1, height)]
	}
	indent := strings.Repeat(" ", max(0, (width-lipgloss.Width(lines[0]))/2))
	for i, l := range lines {
		lines[i] = indent + l
	}
	top := max(0, (height-len(lines))/2)
	out := strings.Repeat("\n", top) + strings.Join(lines, "\n")
	if bottom := height - top - len(lines); bottom > 0 {
		out += strings.Repeat("\n", bottom)
	}
	return out
}

// row renders one item clipped to w cells: cursor rail, title, dim detail.
// Text is fitted before styling so the frame never tears.
func (o overlay) row(cs chatStyles, it overlayItem, selected bool, w int) string {
	marker := "  "
	if selected {
		marker = cs.glyph("▍", cs.accent) + " "
	}
	avail := w - 2
	title := fit(it.title, max(4, avail))
	var b strings.Builder
	b.WriteString(marker)
	b.WriteString(title)
	if rest := avail - runeLen(title) - 2; it.detail != "" && rest >= 4 {
		b.WriteString("  " + cs.dimText(fit(it.detail, rest)))
	}
	return b.String()
}

// fuzzyRank reports whether query is a case-insensitive subsequence of s and
// how well it fits: tighter, earlier matches rank lower (better).
func fuzzyRank(query, s string) (int, bool) {
	q, t := strings.ToLower(query), strings.ToLower(s)
	if q == "" {
		return 0, true
	}
	first, pos := -1, 0
	for _, r := range q {
		j := strings.IndexRune(t[pos:], r)
		if j < 0 {
			return 0, false
		}
		pos += j
		if first < 0 {
			first = pos
		}
		pos++
	}
	return (pos - 1 - first) + first, true
}

func item(group, id, title, key string) overlayItem {
	return overlayItem{group: group, id: id, title: title, key: key}
}

func itemD(group, id, title, key, detail string) overlayItem {
	it := item(group, id, title, key)
	it.detail = detail
	return it
}

// chatActions is the Ctrl+P catalog (no live toggle state).
func chatActions() []overlayItem {
	gSession, gCtx, gModel, gDisplay, gTools, gOther := "Session", "Context", "Model & Input", "Display", "Extensions", "Other"
	return []overlayItem{
		itemD(gSession, "new", "New Session", "ctrl+n", "start a fresh conversation"),
		itemD(gSession, "home", "Home", "/home", "return to the welcome surface"),
		itemD(gSession, "dashboard", "Agent Dashboard", "ctrl+\\", "roster of live sessions"),
		itemD(gSession, "resume", "Resume Session", "/resume", "reopen a prior conversation"),
		itemD(gSession, "rename", "Rename Session", "/rename", "set the session title"),
		itemD(gSession, "delete", "Delete Session", "/delete", "requires typed confirmation"),
		itemD(gSession, "status", "Session Info", "/status", "id, route, tokens"),
		itemD(gCtx, "compact", "Compact History", "/compact", "fold older turns into a summary"),
		itemD(gCtx, "queue", "Prompt Queue", "ctrl+b", "queued prompts waiting to run"),
		itemD(gCtx, "usage", "Usage Meter", "/usage", "toggle context and spend in the composer"),
		itemD(gModel, "model", "Switch Model", "/model", "pick the active route"),
		itemD(gModel, "cycle-mode", "Switch Mode", "shift+tab", "normal, plan, auto, always-approve, always-ask"),
		itemD(gModel, "multiline", "Multiline Input", "/multiline", "enter inserts a newline"),
		itemD(gModel, "vim-mode", "Vim Mode", "/vim-mode", "j/k in the scrollback"),
		itemD(gDisplay, "verbose", "Verbose Trace", "/verbose", "full event trace"),
		itemD(gDisplay, "tools-display", "Tool Activity", "/tools", "show tool calls in chat"),
		itemD(gDisplay, "thinking", "Thinking Indicator", "/thinking", "show live thinking line"),
		itemD(gTools, "skills", "Skills", "/skills", "install, inspect, invoke"),
		itemD(gTools, "connect", "Connect Provider", "/connect", "add an API key"),
		itemD(gTools, "agent", "Manage Agents", "/agent", "switch agent profiles"),
		itemD(gOther, "theme", "Switch Theme", "/theme", "friday, dark, light, ansi"),
		itemD(gOther, "edit-prompt", "Edit Prompt", "ctrl+g", "open $VISUAL"),
		itemD(gOther, "copy", "Copy Last Reply", "/copy", "clipboard the last reply"),
		itemD(gOther, "history", "Prompt History", "/history", "recall a sent prompt"),
		itemD(gOther, "help", "Help", "/help", "commands and keys"),
		itemD(gOther, "quit", "Quit", "ctrl+c", "exit friday"),
	}
}
