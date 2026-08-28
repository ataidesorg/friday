package tui

import (
	"slices"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func (m ChatModel) openPalette() (tea.Model, tea.Cmd) {
	ov := overlay{kind: overlayPalette, title: "Commands", items: m.paletteItems()}
	m.ov = &ov
	return m, nil
}

func (m ChatModel) paletteItems() []overlayItem {
	items := chatActions()
	for i, it := range items {
		switch it.id {
		case "vim-mode":
			items[i].state = onOff(m.vim)
		case "always-approve":
			items[i].state = onOff(m.yolo)
		case "verbose":
			items[i].state = onOff(m.verbose)
		case "tools-display":
			items[i].state = onOff(m.showTools)
		case "thinking":
			items[i].state = onOff(m.showThinking)
		case "multiline":
			items[i].state = onOff(m.multiline)
		case "plan":
			items[i].state = onOff(m.mode == "plan")
		case "cycle-mode":
			items[i].state = m.permLabel()
		case "usage":
			items[i].state = onOff(m.usageOpen)
		}
	}
	return items
}

func (m ChatModel) openSkills() (tea.Model, tea.Cmd) {
	items := make([]overlayItem, 0, len(m.skills)+4)
	for _, s := range m.skills {
		src := s.Source
		if src == "" {
			src = "loaded"
		}
		items = append(items, overlayItem{
			id: s.Name, title: s.Name, detail: s.Description,
			group: "Installed", state: src, key: "enter",
		})
	}
	items = append(items,
		overlayItem{id: "help:add-skill", title: "Create project skill", detail: "reusable instructions for this repo", group: "How to manage"},
		overlayItem{id: "help:user-skill", title: "Create personal skill", detail: "reusable instructions for every repo", group: "How to manage"},
		overlayItem{id: "help:remove-skill", title: "Remove a skill", detail: "delete its directory", group: "How to manage"},
	)
	m.ov = &overlay{kind: overlaySkills, title: "Skills", items: items}
	return m, nil
}

func (m ChatModel) openHistory() (tea.Model, tea.Cmd) {
	if len(m.hist) == 0 {
		return m.append(tagWarn + " no prompt history yet"), nil
	}
	items := make([]overlayItem, 0, len(m.hist))
	for i := len(m.hist) - 1; i >= 0; i-- {
		items = append(items, overlayItem{id: strconv.Itoa(i), title: m.hist[i]})
	}
	m.ov = &overlay{kind: overlayHistory, title: "Prompt history", items: items}
	return m, nil
}

func (m ChatModel) openRewind() (tea.Model, tea.Cmd) {
	items := userPromptItems(m.Lines)
	if len(items) == 0 {
		return m.append(tagWarn + " nothing to rewind"), nil
	}
	m.ov = &overlay{kind: overlayRewind, title: "Rewind to", items: items, cursor: len(items) - 1}
	return m, nil
}

// overlayKey routes every key to the open overlay; a close drops it and a
// commit dispatches the chosen action.
func (m ChatModel) overlayKey(v tea.KeyMsg) (tea.Model, tea.Cmd) {
	kind := m.ov.kind
	next, done := m.ov.update(v)
	m.ov = &next
	if done.closed {
		m.ov = nil
		if kind == overlayThemes {
			m = m.styleTheme(m.themeName)
		}
		if kind == overlayConnectModel && m.conn != nil {
			return m.connectCancel(*m.conn)
		}
		return m, nil
	}
	if done.commit != "" {
		m.ov = nil
		return m.overlayCommit(kind, done.commit)
	}
	if kind == overlayThemes {
		if items := next.matches(); len(items) > 0 {
			m = m.styleTheme(items[min(next.cursor, len(items)-1)].id)
		}
	}
	return m, nil
}

// overlayCommit runs the committed selection: a route swap from the model
// picker, or a palette action named by its slash command.
func (m ChatModel) overlayCommit(kind overlayKind, id string) (tea.Model, tea.Cmd) {
	if kind == overlayModels {
		return m.switchModel(id)
	}
	if kind == overlayThemes {
		return m.applyTheme(id)
	}
	if kind == overlayAgents {
		return m.applyAgent(id)
	}
	if kind == overlayConnect {
		return m.startConnect(id)
	}
	if kind == overlayConnectModel {
		return m.connectModelPicked(id)
	}
	if kind == overlaySessions {
		return m.resumeNamed(id)
	}
	if kind == overlayHistory {
		i, err := strconv.Atoi(id)
		if err != nil || i < 0 || i >= len(m.hist) {
			return m, nil
		}
		m.ta.SetValue(m.hist[i])
		m.ta.CursorEnd()
		m.histI = i
		return m.focusPrompt(), nil
	}
	if kind == overlayRewind {
		n, err := strconv.Atoi(id)
		if err != nil {
			return m, nil
		}
		return m.applyRewind(n)
	}
	if kind == overlaySkills {
		if strings.HasPrefix(id, "help:") {
			return m.append(skillHelp(id)...), nil
		}
		return m.invokeSkill(id)
	}
	switch id {
	case "model":
		return m.openModels()
	case "theme":
		return m.openThemes()
	case "agent":
		return m.openAgents()
	case "queue":
		return m.toggleQueue()
	case "edit-prompt":
		return m.openEditor()
	case "skills":
		return m.openSkills()
	case "cycle-mode":
		return m.cycleMode()
	case "tools-display":
		m.showTools = !m.showTools
		return m.append(tagStatus + " tool activity " + onOff(m.showTools)).layout(), nil
	case "thinking":
		m.showThinking = !m.showThinking
		return m.append(tagStatus + " thinking " + onOff(m.showThinking)).layout(), nil
	}
	return m.command("/" + id)
}

// skillHelp is the /skills overlay's "How to manage" copy: Friday has no
// remote store, so the answer to every one of these is a directory.
func skillHelp(id string) []string {
	switch id {
	case "help:add-skill":
		return []string{
			tagStatus + " create skill",
			"  A skill is a reusable instruction pack the agent can follow.",
			"  Use it for workflows like review, diagnose, migration, or project-specific taste rules.",
			"  Add one under skills/ in this repo or your Friday home, then reopen Friday to load it.",
		}
	case "help:user-skill":
		return []string{
			tagStatus + " personal skill",
			"  Personal skills are available across projects.",
			"  Project skills override personal skills with the same name.",
		}
	case "help:remove-skill":
		return []string{
			tagStatus + " remove skill",
			"  Delete its directory. Friday has no remote store; skills are local files.",
		}
	default:
		return []string{tagWarn + " no help for " + clip(id)}
	}
}

// openAgents opens the agent picker; without profiles configured it warns.
func (m ChatModel) openAgents() (tea.Model, tea.Cmd) {
	if len(m.agents) == 0 {
		return m.append(tagWarn + " no agents configured — add [agents.NAME] to your config"), nil
	}
	items := make([]overlayItem, 0, len(m.agents)+1)
	items = append(items, overlayItem{id: "none", title: "none", detail: "base prompt and full tool set"})
	cursor := 0
	for i, a := range m.agents {
		detail := a.Description
		if a.Name == m.agentName {
			detail = strings.TrimSpace(detail + " · active")
			cursor = i + 1
		}
		items = append(items, overlayItem{id: a.Name, title: a.Name, detail: detail})
	}
	ov := overlay{kind: overlayAgents, title: "Switch agent", items: items, cursor: cursor}
	m.ov = &ov
	return m, nil
}

// applyAgent activates a named profile ("none" resets). Refused mid-turn: the
// profile swaps the tool set and prompt under the runner otherwise.
func (m ChatModel) applyAgent(name string) (tea.Model, tea.Cmd) {
	if m.Running {
		return m.append(tagWarn + " finish or cancel this turn before /agent"), nil
	}
	if name == "none" || name == "off" {
		name = ""
	}
	if name != "" && !slices.ContainsFunc(m.agents, func(a AgentInfo) bool { return a.Name == name }) {
		return m.append(tagWarn + " unknown agent " + clip(name) + " — /agent lists profiles"), nil
	}
	if m.setAgent == nil {
		return m.append(tagWarn + " agent switching is not available here"), nil
	}
	if err := m.setAgent(name); err != nil {
		return m.append(tagWarn + " /agent failed: " + clip(err.Error())), nil
	}
	m.agentName = name
	if name == "" {
		return m.append(tagStatus + " agent off, base prompt and full tool set"), nil
	}
	return m.append(tagStatus + " agent " + name + " active"), nil
}

// openModels opens the route picker with the cursor on the active route; with
// nothing configured to pick, it falls back to the text summary.
func (m ChatModel) openModels() (tea.Model, tea.Cmd) {
	if len(m.routes) == 0 {
		return m.append(modelLines(m.route, m.routes, m.active)...), nil
	}
	items := make([]overlayItem, len(m.routes))
	cursor := 0
	for i, r := range m.routes {
		detail := r.Provider + "/" + r.Model
		if r.Name == m.active {
			detail += " · active"
			cursor = i
		}
		items[i] = overlayItem{id: r.Name, title: r.Name, detail: detail}
	}
	ov := overlay{kind: overlayModels, title: "Switch model", items: items, cursor: cursor}
	m.ov = &ov
	return m, nil
}

// openThemes opens the palette picker with the cursor on the active theme.
func (m ChatModel) openThemes() (tea.Model, tea.Cmd) {
	items := make([]overlayItem, len(m.themes))
	cursor := 0
	for i, t := range m.themes {
		detail := ""
		if t.Name == m.themeName {
			detail = "· active"
			cursor = i
		}
		items[i] = overlayItem{id: t.Name, title: t.Name, detail: detail}
	}
	ov := overlay{kind: overlayThemes, title: "Switch theme", items: items, cursor: cursor}
	m.ov = &ov
	return m.styleTheme(items[cursor].id), nil
}

func (m ChatModel) styleTheme(name string) ChatModel {
	i := slices.IndexFunc(m.themes, func(t Theme) bool { return t.Name == name })
	if i < 0 {
		return m
	}
	m.cstyle = themedStyles(m.cstyle.on, m.themes[i])
	m.styledName = name
	blank := lipgloss.NewStyle()
	m.ta.FocusedStyle.Base = blank
	m.ta.FocusedStyle.CursorLine = blank
	m.ta.FocusedStyle.Text = blank
	m.ta.FocusedStyle.Prompt = m.cstyle.dim
	m.ta.FocusedStyle.Placeholder = m.cstyle.dim
	m.ta.BlurredStyle = m.ta.FocusedStyle
	m.sp.Style = m.cstyle.spin
	return m
}

// applyTheme swaps the palette live and persists the choice; a failed
// persist keeps the swap and warns.
func (m ChatModel) applyTheme(name string) (tea.Model, tea.Cmd) {
	i := slices.IndexFunc(m.themes, func(t Theme) bool { return t.Name == name })
	if i < 0 {
		return m.append(tagWarn + " unknown theme " + clip(name)), nil
	}
	m = m.styleTheme(name)
	if name == m.themeName {
		return m.append(tagStatus + " already on theme " + name), nil
	}
	m.themeName = name
	out := []string{tagStatus + " theme " + name}
	if m.setTheme != nil {
		if err := m.setTheme(name); err != nil {
			out = append(out, tagWarn+" theme not saved: "+clip(err.Error()))
		}
	}
	return m.append(out...), nil
}

// mergeThemes lays customs over the built-ins: a custom sharing a built-in's
// name replaces it in place, new names append in their given order.
func mergeThemes(builtins, customs []Theme) []Theme {
	out := slices.Clone(builtins)
	for _, c := range customs {
		if i := slices.IndexFunc(out, func(t Theme) bool { return t.Name == c.Name }); i >= 0 {
			out[i] = c
			continue
		}
		out = append(out, c)
	}
	return out
}
