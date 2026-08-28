package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ataidesorg/friday/internal/runtime"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type dashRun struct {
	running bool
	peek    string
	cancel  context.CancelFunc
	ch      chan tea.Msg
}

type dashEventMsg struct {
	id  string
	msg tea.Msg
}

type dashDoneMsg struct {
	id  string
	res runtime.Result
	err error
}

func (m ChatModel) toggleDashboard() (tea.Model, tea.Cmd) {
	if m.listAgents == nil && m.createAgent == nil {
		return m.append(tagWarn + " dashboard is not available in this session"), nil
	}
	m.dash = !m.dash
	if !m.dash {
		m.dashSearch, m.dashSearchQ, m.ctrlXHint = false, "", false
		return m.layout(), nil
	}
	m.dashFocusList = true
	m.dashSel = 0
	m.ta.Blur()
	return m.layout(), nil
}

func (m ChatModel) dashRows() []DashAgent {
	var rows []DashAgent
	if m.listAgents != nil {
		rows = append(rows, m.listAgents()...)
	}
	seen := map[string]bool{}
	for i := range rows {
		seen[rows[i].ID] = true
		if rows[i].ID == m.sessionID {
			if m.Running {
				rows[i].State = "working"
			} else if rows[i].State == "" {
				rows[i].State = "idle"
			}
		}
		if run := m.dashLive[rows[i].ID]; run != nil {
			if run.running {
				rows[i].State = "working"
			}
			if run.peek != "" {
				rows[i].Peek = run.peek
			}
		}
	}
	for id, run := range m.dashLive {
		if seen[id] {
			continue
		}
		st := "idle"
		if run.running {
			st = "working"
		}
		rows = append(rows, DashAgent{ID: id, Title: "new agent", State: st, Peek: run.peek})
	}
	if m.dashSearchQ != "" {
		q := strings.ToLower(m.dashSearchQ)
		kept := rows[:0]
		for _, r := range rows {
			blob := strings.ToLower(r.Title + " " + r.Detail + " " + r.State + " " + r.Peek)
			if strings.Contains(blob, q) {
				kept = append(kept, r)
			}
		}
		rows = kept
	}
	slices.SortStableFunc(rows, func(a, b DashAgent) int {
		return dashRank(a.State) - dashRank(b.State)
	})
	return rows
}

func dashRank(state string) int {
	switch state {
	case "needs-input":
		return 0
	case "working":
		return 1
	case "idle", "":
		return 2
	default:
		return 3
	}
}

func dashGroup(state string) string {
	switch state {
	case "needs-input":
		return "Needs input"
	case "working":
		return "Working"
	case "idle", "":
		return "Idle"
	default:
		return "Other"
	}
}

func (m ChatModel) dashKey(v tea.KeyMsg) (tea.Model, tea.Cmd) {
	rows := m.dashRows()
	switch v.Type {
	case tea.KeyCtrlBackslash, tea.KeyEsc:
		if m.dashSearch {
			m.dashSearch, m.dashSearchQ = false, ""
			return m.layout(), nil
		}
		return m.toggleDashboard()
	case tea.KeyCtrlC:
		return m, tea.Quit
	case tea.KeyUp:
		n := len(rows)
		if n == 0 {
			return m.layout(), nil
		}
		if m.dashSel <= 0 {
			m.dashSel = n - 1
		} else {
			m.dashSel--
		}
		return m.layout(), nil
	case tea.KeyDown:
		n := len(rows)
		if n == 0 {
			return m.layout(), nil
		}
		if m.dashSel >= n-1 {
			m.dashSel = 0
		} else {
			m.dashSel++
		}
		return m.layout(), nil
	case tea.KeyTab:
		m.dashFocusList = !m.dashFocusList
		return m.layout(), nil
	case tea.KeyCtrlS:
		prompt := strings.TrimSpace(m.dashDraft)
		if prompt == "" {
			return m.dashAttach()
		}
		return m.dashDispatch(prompt, true)
	case tea.KeyCtrlR:
		return m.dashRename()
	case tea.KeyCtrlX:
		return m.dashDelete()
	case tea.KeyCtrlT:
		return m.toggleTodos()
	case tea.KeyEnter:
		if m.dashSearch {
			m.dashSearch = false
			return m.layout(), nil
		}
		prompt := strings.TrimSpace(m.dashDraft)
		if prompt == "" {
			return m.dashAttach()
		}
		return m.dashDispatch(prompt, false)
	case tea.KeyBackspace:
		if m.dashSearch {
			r := []rune(m.dashSearchQ)
			if len(r) > 0 {
				m.dashSearchQ = string(r[:len(r)-1])
			}
			return m.layout(), nil
		}
		r := []rune(m.dashDraft)
		if len(r) > 0 {
			m.dashDraft = string(r[:len(r)-1])
		}
		return m.layout(), nil
	case tea.KeyRunes:
		s := string(v.Runes)
		if m.dashSearch {
			m.dashSearchQ += s
			return m.layout(), nil
		}
		if s == "/" && m.dashDraft == "" && m.dashFocusList {
			m.dashSearch = true
			return m.layout(), nil
		}
		m.dashDraft += s
		m.dashFocusList = false
		return m.layout(), nil
	case tea.KeySpace:
		if m.dashSearch {
			m.dashSearchQ += " "
			return m.layout(), nil
		}
		m.dashDraft += " "
		m.dashFocusList = false
		return m.layout(), nil
	}
	if v.String() == "ctrl+/" {
		m.dashSearch = !m.dashSearch
		if !m.dashSearch {
			m.dashSearchQ = ""
		}
		return m.layout(), nil
	}
	return m, nil
}

func (m ChatModel) selectedDash(rows []DashAgent) (DashAgent, bool) {
	if len(rows) == 0 || m.dashSel < 0 || m.dashSel >= len(rows) {
		return DashAgent{}, false
	}
	return rows[m.dashSel], true
}

func (m ChatModel) dashAttach() (tea.Model, tea.Cmd) {
	row, ok := m.selectedDash(m.dashRows())
	if !ok {
		return m.append(tagWarn + " no agent to open"), nil
	}
	if m.attachAgent == nil {
		return m.append(tagWarn + " attach is not available"), nil
	}
	title, turns, err := m.attachAgent(row.ID)
	if err != nil {
		return m.append(tagWarn + " attach: " + clip(err.Error())), nil
	}
	m.dash = false
	m = m.applyResume(row.ID, turns)
	if title != "" {
		m.title = title
	}
	m = m.reloadTodos()
	return m.focusPrompt(), nil
}

func (m ChatModel) dashDispatch(prompt string, attach bool) (tea.Model, tea.Cmd) {
	if m.createAgent == nil {
		return m.append(tagWarn + " dispatch is not available"), nil
	}
	id, err := m.createAgent()
	if err != nil {
		return m.append(tagWarn + " dispatch: " + clip(err.Error())), nil
	}
	m.dashDraft = ""
	if attach || m.runOn == nil {
		if m.attachAgent != nil {
			if _, turns, aerr := m.attachAgent(id); aerr == nil {
				m.dash = false
				m = m.applyResume(id, turns)
			}
		}
		m.sessionID = id
		return m.send(prompt)
	}
	if m.dashLive == nil {
		m.dashLive = map[string]*dashRun{}
	}
	ctx, cancel := context.WithCancel(m.baseCtx)
	ch := make(chan tea.Msg, eventBuffer)
	m.dashLive[id] = &dashRun{running: true, cancel: cancel, ch: ch, peek: prompt}
	run := m.runOn
	go func() {
		defer cancel()
		res, err := run(ctx, id, prompt, chanObserver{ch})
		ch <- dashDoneMsg{id: id, res: res, err: err}
	}()
	return m.layout(), waitDash(id, ch)
}

func (m ChatModel) dashRename() (tea.Model, tea.Cmd) {
	row, ok := m.selectedDash(m.dashRows())
	if !ok {
		return m, nil
	}
	title := strings.TrimSpace(m.dashDraft)
	if title == "" {
		return m.append(tagWarn + " type a title, then ctrl+r"), nil
	}
	if m.renameFn == nil {
		return m.append(tagWarn + " rename is not available"), nil
	}
	// renameFn acts on the live session; attach first when needed.
	if row.ID != m.sessionID && m.attachAgent != nil {
		if _, turns, err := m.attachAgent(row.ID); err == nil {
			m = m.applyResume(row.ID, turns)
		}
	}
	next, cmd := m.applyRename(title)
	if cm, ok := next.(ChatModel); ok {
		cm.dashDraft = ""
		cm.dash = true
		return cm.layout(), cmd
	}
	return next, cmd
}

func (m ChatModel) dashDelete() (tea.Model, tea.Cmd) {
	row, ok := m.selectedDash(m.dashRows())
	if !ok || m.deleteAgent == nil {
		return m, nil
	}
	now := m.now()
	if !m.ctrlXHint || m.lastCtrlX.IsZero() || now.Sub(m.lastCtrlX) > 2*time.Second {
		m.lastCtrlX, m.ctrlXHint = now, true
		return m.append(tagWarn + " press ctrl+x again to delete " + clip(row.Title)), nil
	}
	m.lastCtrlX, m.ctrlXHint = time.Time{}, false
	if run := m.dashLive[row.ID]; run != nil && run.cancel != nil {
		run.cancel()
	}
	if err := m.deleteAgent(row.ID); err != nil {
		return m.append(tagWarn + " delete: " + clip(err.Error())), nil
	}
	delete(m.dashLive, row.ID)
	if row.ID == m.sessionID && m.newSess != nil {
		return m.newSession()
	}
	return m.layout(), nil
}

func waitDash(id string, ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg { return dashEventMsg{id: id, msg: <-ch} }
}

func (m ChatModel) handleDashEvent(v dashEventMsg) (tea.Model, tea.Cmd) {
	run := m.dashLive[v.id]
	if run == nil {
		return m, nil
	}
	switch msg := v.msg.(type) {
	case DeltaMsg:
		run.peek += string(msg)
	case dashDoneMsg:
		run.running = false
		run.cancel = nil
		if msg.err != nil {
			run.peek = clip(msg.err.Error())
		} else if strings.TrimSpace(msg.res.Summary) != "" {
			run.peek = msg.res.Summary
		}
		return m.layout(), nil
	}
	return m.layout(), waitDash(v.id, run.ch)
}

func (m ChatModel) dashView() string {
	rows := m.dashRows()
	if m.dashSel >= len(rows) {
		m.dashSel = max(0, len(rows)-1)
	}
	width := max(20, m.innerWidth())
	height := max(8, m.height)
	working, waiting := 0, 0
	for _, r := range rows {
		switch r.State {
		case "working":
			working++
		case "needs-input":
			waiting++
		}
	}
	right := fmt.Sprintf("%d agents", len(rows))
	if working > 0 {
		right += fmt.Sprintf(" · %d working", working)
	}
	if waiting > 0 {
		right += fmt.Sprintf(" · %d awaiting", waiting)
	}
	left := "Friday · Dashboard"
	if m.cstyle.on {
		left = m.cstyle.header.Render("Friday") + m.cstyle.dimText(" · Dashboard")
	}

	type vis struct {
		header bool
		idx    int
	}
	var visRows []vis
	prev := ""
	for i, r := range rows {
		g := dashGroup(r.State)
		if g != prev {
			visRows = append(visRows, vis{header: true, idx: i})
			prev = g
		}
		visRows = append(visRows, vis{idx: i})
	}

	composerH := 3
	footerH := 1
	listH := max(1, height-1-composerH-footerH) // header + composer + footer
	start := 0
	focus := 0
	for i, v := range visRows {
		if !v.header && v.idx == m.dashSel {
			focus = i
			break
		}
	}
	if focus >= listH {
		start = focus - listH + 1
	}

	var body []string
	if len(rows) == 0 {
		body = append(body, m.cstyle.dimText("  no agents — type a prompt below to dispatch"))
	}
	for i := start; i < len(visRows) && i-start < listH; i++ {
		v := visRows[i]
		if v.header {
			label := strings.ToUpper(dashGroup(rows[v.idx].State))
			line := "  " + label + " " + strings.Repeat("─", max(1, width-3-runeLen(label)))
			body = append(body, m.cstyle.dimText(line))
			continue
		}
		r := rows[v.idx]
		selected := v.idx == m.dashSel
		body = append(body, m.dashRow(r, selected, width))
		if selected && strings.TrimSpace(r.Peek) != "" {
			peek := "    " + strings.Join(strings.Fields(r.Peek), " ")
			body = append(body, m.cstyle.dimText(fit(peek, width)))
		}
	}
	for len(body) < listH {
		body = append(body, "")
	}

	draft := m.dashDraft
	if m.dashSearch {
		draft = m.dashSearchQ
	}
	prompt := dashComposer(draft, m.dashSearch, width, m.cstyle)
	hint := "↑↓ select · enter open · type to dispatch · ctrl+s attach · ctrl+r rename · ctrl+x delete · esc back"
	if m.dashSearch {
		hint = "type to filter · enter keep filter · esc cancel"
	}

	parts := []string{
		m.cstyle.headerBar(left, right, width),
		strings.Join(body, "\n"),
		prompt,
		m.cstyle.dimText(fit(hint, width)),
	}
	out := strings.Join(parts, "\n")
	lines := strings.Split(out, "\n")
	if pad := height - len(lines); pad > 0 {
		out += strings.Repeat("\n", pad)
	} else if len(lines) > height {
		out = strings.Join(lines[:height], "\n")
	}
	return padFrame(out, framePad)
}

func (m ChatModel) dashRow(r DashAgent, selected bool, width int) string {
	dot, st := "○", m.cstyle.dim
	switch r.State {
	case "working":
		dot, st = "⋅", m.cstyle.accent
	case "needs-input":
		dot, st = "●", m.cstyle.warn
	}
	mark := "  "
	if selected {
		mark = m.cstyle.glyph("▌", m.cstyle.accent)
	}
	title := r.Title
	if title == "" {
		title = r.ID
	}
	if r.ID == m.sessionID {
		title += " · live"
	}
	state := r.State
	if state == "" {
		state = "idle"
	}
	right := state
	if r.Detail != "" {
		right = r.Detail
	}
	plain := padBetween(fit(mark+dot+" "+title, max(8, width-runeLen(right)-1)), right, width)
	if selected && m.cstyle.on {
		return lipgloss.NewStyle().Reverse(true).Render(plain)
	}
	if !m.cstyle.on {
		return plain
	}
	left := mark + m.cstyle.glyph(dot, st) + " " + title
	return padBetween(fit(left, max(8, width-runeLen(right)-1)), m.cstyle.dimText(right), width)
}

func dashComposer(draft string, search bool, width int, cs chatStyles) string {
	inner := max(1, width-2)
	prefix := "❯ "
	if search {
		prefix = "Search: "
	}
	body := prefix + draft
	if strings.TrimSpace(draft) == "" && !search {
		ph := "Dispatch a new agent"
		if cs.on {
			body = prefix + cs.dimText(ph)
		} else {
			body = prefix + ph
		}
	}
	top := "╭" + strings.Repeat("─", inner) + "╮"
	botLab := " dispatch "
	if search {
		botLab = " search "
	}
	bot := "╰" + strings.Repeat("─", max(0, inner-runeLen(botLab))) + botLab + "╯"
	lw := lipgloss.Width(body)
	pad := inner - lw
	if pad < 0 {
		body = fit(body, inner)
		pad = 0
	}
	mid := cs.frameText("│") + body + strings.Repeat(" ", pad) + cs.frameText("│")
	return cs.frameText(top) + "\n" + mid + "\n" + cs.frameText(bot)
}
