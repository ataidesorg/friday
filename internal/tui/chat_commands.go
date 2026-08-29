package tui

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ataidesorg/friday/internal/core"
)

// startCompact runs /compact like a turn: spinner on, cancellable with
// Ctrl-C, summary deltas streaming into the live reply until the note lands.
func (m ChatModel) startCompact() (tea.Model, tea.Cmd) {
	if m.Running {
		return m.append(tagWarn + " finish or cancel this turn before /compact"), nil
	}
	if m.compact == nil {
		return m.append(tagWarn + " compaction not available in this session"), nil
	}
	m.Running, m.reply = true, ""
	m.turnStart = m.now()
	ctx, cancel := context.WithCancel(m.baseCtx)
	m.cancel = cancel
	ch, fn := m.events, m.compact
	go func() {
		defer cancel()
		note, err := fn(ctx, chanObserver{ch})
		ch <- compactDoneMsg{note: note, err: err}
	}()
	return m, waitEvent(ch)
}

func (m ChatModel) invokeSkill(name string) (tea.Model, tea.Cmd) {
	if m.Running {
		return m.append(tagWarn + " finish or cancel this turn before invoking a skill"), nil
	}
	found := false
	for _, s := range m.skills {
		if s.Name == name {
			found = true
			break
		}
	}
	if !found {
		return m.append(tagWarn + " unknown skill " + clip(name)), nil
	}
	return m.send("Use the skill \"" + name + "\".")
}

func statusLines(m ChatModel) []string {
	out := []string{tagStatus + " session"}
	if m.title != "" {
		out = append(out, "  title "+m.title)
	}
	if m.sessionID != "" {
		out = append(out, "  id "+m.sessionID)
	}
	if m.route != "" {
		out = append(out, "  route "+m.route)
	}
	if m.mode != "" {
		out = append(out, "  mode "+m.mode)
	}
	out = append(out, "  always-approve "+onOff(m.yolo))
	out = append(out, "  vim-mode "+onOff(m.vim))
	if m.goal != nil {
		out = append(out, "  "+strings.TrimPrefix(goalStatusLine(*m.goal), tagStatus+" "))
	}
	out = append(out, costLines(m.Usage, m.Cost)...)
	return out
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

func (m ChatModel) toggleYolo() (tea.Model, tea.Cmd) {
	m.yolo = !m.yolo
	if m.yolo {
		m.auto = false
	}
	if m.yolo && m.pending != nil {
		m = m.resolveApproval(core.ApprovalApproved, core.ApprovalSession)
	}
	return m.append(tagStatus + " always-approve " + onOff(m.yolo)), nil
}

func (m ChatModel) toggleVim() (tea.Model, tea.Cmd) {
	m.vim = !m.vim
	if m.setVim != nil {
		if err := m.setVim(m.vim); err != nil {
			return m.append(tagWarn + " vim-mode: " + clip(err.Error())), nil
		}
	}
	return m.persistPrefs().append(tagStatus + " vim-mode " + onOff(m.vim)), nil
}

func (m ChatModel) currentPrefs() Prefs {
	return Prefs{
		Verbose:        m.verbose,
		ShowTools:      m.showTools,
		ShowThinking:   m.showThinking,
		Timestamps:     m.showTimes,
		Multiline:      m.multiline,
		UsageMeter:     m.usageOpen,
		HideAdvisories: m.hideAdvis,
		VimMode:        m.vim,
	}
}

func (m ChatModel) persistPrefs() ChatModel {
	if m.setPrefs == nil {
		return m
	}
	if err := m.setPrefs(m.currentPrefs()); err != nil {
		return m.append(tagWarn + " save prefs: " + clip(err.Error()))
	}
	return m
}

func (m ChatModel) doctorLines() []string {
	out := []string{
		tagStatus + " doctor",
		fmt.Sprintf("  size %d×%d", m.width, m.height),
		"  color " + onOff(m.cstyle.on),
		"  vim-mode " + onOff(m.vim),
		"  always-approve " + onOff(m.yolo),
		"  clipboard osc52",
	}
	for _, l := range m.doctor {
		if strings.TrimSpace(l) != "" {
			out = append(out, "  "+l)
		}
	}
	return out
}

func (m ChatModel) applyRewind(keepUsers int) (tea.Model, tea.Cmd) {
	if m.rewindFn != nil {
		if err := m.rewindFn(keepUsers); err != nil {
			return m.append(tagWarn + " rewind: " + clip(err.Error())), nil
		}
	}
	before := len(m.Lines)
	m.Lines = rewindLines(m.Lines, keepUsers)
	if len(m.lineTimes) >= before {
		m.lineTimes = slices.Clip(m.lineTimes[:len(m.Lines)])
	} else {
		m.lineTimes = slices.Clip(m.lineTimes)
	}
	return m.append(fmt.Sprintf("%s rewind kept %d prompts", tagReply, keepUsers)), nil
}

func (m ChatModel) applyFork() (tea.Model, tea.Cmd) {
	if m.Running {
		return m.append(tagWarn + " finish or cancel this turn before /fork"), nil
	}
	if m.forkFn == nil {
		return m.append(tagWarn + " fork is not available in this session"), nil
	}
	id, err := m.forkFn()
	if err != nil {
		return m.append(tagWarn + " fork: " + clip(err.Error())), nil
	}
	if id != "" {
		m.sessionID = id
	}
	return m.append(tagStatus + " forked session " + id), nil
}

func (m ChatModel) applyRename(title string) (tea.Model, tea.Cmd) {
	title = strings.TrimSpace(title)
	if title == "" {
		return m.append(tagWarn + " usage: /rename TITLE"), nil
	}
	if m.renameFn != nil {
		if err := m.renameFn(title); err != nil {
			return m.append(tagWarn + " rename: " + clip(err.Error())), nil
		}
	}
	m.title = title
	return m.append(tagStatus + " renamed " + title), nil
}

func (m ChatModel) deleteCurrent(arg string) (tea.Model, tea.Cmd) {
	if m.Running {
		return m.append(tagWarn + " finish or cancel this turn before /delete"), nil
	}
	if strings.TrimSpace(arg) != "confirm" {
		return m.append(tagWarn + " /delete permanently removes this session. Type /delete confirm."), nil
	}
	if m.deleteSession == nil {
		return m.append(tagWarn + " delete is not available in this session"), nil
	}
	id, err := m.deleteSession()
	if err != nil {
		return m.append(tagWarn + " delete: " + clip(err.Error())), nil
	}
	m.Lines, m.lineTimes = nil, nil
	m.Usage, m.Cost = core.Usage{}, core.CostReport{}
	m.title, m.hist, m.histI = "", nil, -1
	m.sessionID = id
	m.homeOpen = true
	return m.append(tagStatus + " deleted session, started fresh"), nil
}

func (m ChatModel) copyCommand(arg string) (tea.Model, tea.Cmd) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return m.copyReply()
	}
	if n, err := strconv.Atoi(arg); err == nil {
		return m.copyOut(nthAssistantReply(m.Lines, n))
	}
	text := lastAssistantReply(m.Lines)
	if strings.TrimSpace(text) == "" {
		return m.append(tagWarn + " nothing to copy"), nil
	}
	if m.saveCopy == nil {
		return m.append(tagWarn + " writing a copy file is not available"), nil
	}
	if err := m.saveCopy(arg, text); err != nil {
		return m.append(tagWarn + " copy: " + clip(err.Error())), nil
	}
	return m.append(tagStatus + " wrote " + arg), nil
}

func (m ChatModel) setChatMode(name string) (tea.Model, tea.Cmd) {
	if m.Running {
		return m.append(tagWarn + " finish or cancel this turn before changing mode"), nil
	}
	ok := false
	for _, n := range chatModes {
		if n == name {
			ok = true
			break
		}
	}
	if !ok {
		return m.append(tagWarn + " unknown mode " + clip(name)), nil
	}
	if m.setMode != nil {
		if err := m.setMode(name); err != nil {
			return m.append(tagWarn + " mode: " + clip(err.Error())), nil
		}
	}
	m.mode = name
	return m.layout(), nil
}

var chatModes = []string{"code", "plan", "ask"}

type permStep struct {
	label string
	mode  string
	yolo  bool
	auto  bool
}

var permCycle = []permStep{
	{label: "normal", mode: "code"},
	{label: "plan", mode: "plan"},
	{label: "auto", mode: "code", auto: true},
	{label: "always-approve", mode: "code", yolo: true},
	{label: "always-ask", mode: "ask"},
}

func launchMode(name string) string {
	for _, n := range chatModes {
		if name == n {
			return n
		}
	}
	return "code"
}

func (m ChatModel) permLabel() string {
	if m.yolo {
		return "always-approve"
	}
	if m.auto {
		return "auto"
	}
	switch m.mode {
	case "plan":
		return "plan"
	case "ask":
		return "always-ask"
	}
	return "normal"
}

func (m ChatModel) autoApprove(a core.Approval) bool {
	if m.yolo {
		return true
	}
	if !m.auto {
		return false
	}
	switch a.Request.Capability.Risk {
	case core.RiskWriteLocal, core.RiskExecuteLocal:
		return true
	}
	return false
}

func (m ChatModel) cycleMode() (tea.Model, tea.Cmd) {
	if m.Running {
		return m.append(tagWarn + " finish or cancel this turn before changing mode"), nil
	}
	cur := m.permLabel()
	next := permCycle[0]
	for i, s := range permCycle {
		if s.label == cur {
			next = permCycle[(i+1)%len(permCycle)]
			break
		}
	}
	if m.setMode != nil && next.mode != m.mode {
		if err := m.setMode(next.mode); err != nil {
			return m.append(tagWarn + " mode: " + clip(err.Error())), nil
		}
	}
	m.mode, m.yolo, m.auto = next.mode, next.yolo, next.auto
	return m.layout(), nil
}

func (m ChatModel) listCustomCommands() (tea.Model, tea.Cmd) {
	if len(m.commands) == 0 {
		return m.append(tagStatus + " no custom commands — add .md files under .friday/commands/ or the Friday home commands/ directory"), nil
	}
	lines := []string{tagStatus + " custom commands"}
	for _, c := range m.commands {
		detail := c.Description
		if detail == "" {
			detail = c.Name
		}
		lines = append(lines, "  /"+c.Name+"  "+detail)
	}
	return m.append(lines...), nil
}

func (m ChatModel) showHelp() (tea.Model, tea.Cmd) {
	return m.append(helpLines(m.keys)...), nil
}

func (m ChatModel) showStatus() (tea.Model, tea.Cmd) {
	return m.append(statusLines(m)...), nil
}

func (m ChatModel) showDoctor() (tea.Model, tea.Cmd) {
	return m.append(m.doctorLines()...), nil
}

func (m ChatModel) showCost() (tea.Model, tea.Cmd) {
	return m.append(costLines(m.Usage, m.Cost)...), nil
}

func (m ChatModel) goHome() (tea.Model, tea.Cmd) {
	m.homeOpen = true
	return m.layout(), nil
}

func (m ChatModel) clearView() (tea.Model, tea.Cmd) {
	m.Lines, m.lineTimes = nil, nil
	return m.layout(), nil
}

func (m ChatModel) exportTranscript() (tea.Model, tea.Cmd) {
	return m.copyOut(transcriptPlain(m.Lines))
}

func (m ChatModel) cmdPlan() (tea.Model, tea.Cmd) {
	return m.setChatMode("plan")
}

func (m ChatModel) cmdTheme(arg string) (tea.Model, tea.Cmd) {
	if name := firstField(arg); name != "" {
		return m.applyTheme(name)
	}
	return m.openThemes()
}

func (m ChatModel) cmdModel(arg string) (tea.Model, tea.Cmd) {
	if name := firstField(arg); name != "" {
		return m.switchModel(name)
	}
	return m.openModels()
}

func (m ChatModel) cmdAgent(arg string) (tea.Model, tea.Cmd) {
	if name := firstField(arg); name != "" {
		return m.applyAgent(name)
	}
	return m.openAgents()
}

func (m ChatModel) toggleMultiline() (tea.Model, tea.Cmd) {
	m.multiline = !m.multiline
	return m.persistPrefs().append(tagStatus + " multiline " + onOff(m.multiline)), nil
}

func (m ChatModel) toggleTimestamps() (tea.Model, tea.Cmd) {
	m.showTimes = !m.showTimes
	return m.persistPrefs().append(tagStatus + " timestamps " + onOff(m.showTimes)), nil
}

func (m ChatModel) toggleUsageMeter() (tea.Model, tea.Cmd) {
	m.usageOpen = !m.usageOpen
	return m.persistPrefs().append(tagStatus + " usage " + onOff(m.usageOpen)), nil
}

func (m ChatModel) toggleToolActivity() (tea.Model, tea.Cmd) {
	m.showTools = !m.showTools
	return m.persistPrefs().append(tagStatus + " tool activity " + onOff(m.showTools)).layout(), nil
}

func (m ChatModel) toggleThinkingLine() (tea.Model, tea.Cmd) {
	m.showThinking = !m.showThinking
	return m.persistPrefs().append(tagStatus + " thinking " + onOff(m.showThinking)).layout(), nil
}

func (m ChatModel) toggleVerbose() (tea.Model, tea.Cmd) {
	m.verbose = !m.verbose
	m = m.persistPrefs()
	if m.verbose {
		return m.append(tagStatus + " verbose on, full event trace"), nil
	}
	return m.append(tagStatus + " verbose off, tools and warnings only"), nil
}

func (m ChatModel) toggleAdvisories() (tea.Model, tea.Cmd) {
	m.hideAdvis = !m.hideAdvis
	return m.persistPrefs().append(tagStatus + " advisories " + onOff(!m.hideAdvis)), nil
}

func (m ChatModel) quitChat() (tea.Model, tea.Cmd) {
	// Reachable mid-turn through the palette: cancel the turn first so
	// close() joins it instead of timing out against a live tool.
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	return m, tea.Quit
}

// command dispatches a leading-slash prompt from the built-in table.
// Unknown names fall through to a loaded custom command, then warn.
func (m ChatModel) command(line string) (tea.Model, tea.Cmd) {
	// Every command prints into the transcript, and the home pane paints over
	// it. Leaving home is the price of running one; /home and /new opt back in.
	m.homeOpen = false
	fields := strings.Fields(line)
	name := strings.TrimPrefix(strings.ToLower(fields[0]), "/")
	arg := strings.TrimSpace(strings.TrimPrefix(line, fields[0]))
	if c, ok := lookupSlash(name); ok {
		return c.Run(m, arg)
	}
	for _, c := range m.commands {
		if c.Name == name {
			return m.runCustom(c, arg)
		}
	}
	return m.append(tagWarn + " unknown command " + clip(line) + " — try /help"), nil
}

// runCustom starts a turn from a custom command: switch to its route when it
// names one, expand $ARGUMENTS, and send the body as the prompt. The
// scrollback shows the typed form; the session stores the expanded prompt.
func (m ChatModel) runCustom(c CommandInfo, args string) (tea.Model, tea.Cmd) {
	if m.Running {
		return m.append(tagWarn + " finish or cancel this turn before /" + c.Name), nil
	}
	if c.Model != "" && c.Model != m.active {
		i := slices.IndexFunc(m.routes, func(r RouteInfo) bool { return r.Name == c.Model })
		if i < 0 || m.switchFn == nil {
			return m.append(tagWarn + " /" + c.Name + " wants route " + clip(c.Model) + ", which is not configured"), nil
		}
		if err := m.switchFn(c.Model); err != nil {
			return m.append(tagWarn + " /" + c.Name + ": model switch failed: " + clip(err.Error())), nil
		}
		m.active, m.route = c.Model, m.routes[i].Provider+"/"+m.routes[i].Model
		m = m.append(tagModel + " switched to " + c.Model + " for /" + c.Name)
	}
	prompt := expandCommand(c, args)
	typed := "/" + c.Name
	if args != "" {
		typed += " " + args
	}
	m = m.append(userLines(typed)...)
	m.Running, m.reply = true, ""
	m.turnStart = m.now()
	ctx, cancel := context.WithCancel(m.baseCtx)
	m.cancel = cancel
	return m, m.startTurn(ctx, cancel, prompt)
}

// expandCommand substitutes the typed arguments into the body: $ARGUMENTS
// when the body names it, appended as a trailing paragraph otherwise.
func expandCommand(c CommandInfo, args string) string {
	if strings.Contains(c.Body, "$ARGUMENTS") {
		return strings.ReplaceAll(c.Body, "$ARGUMENTS", args)
	}
	if args == "" {
		return c.Body
	}
	return c.Body + "\n\n" + args
}

// switchModel changes the active route live via the injected callback, then
// updates the header and /model marker. Refused mid-turn; an unknown name or a
// failed swap warns and leaves the route unchanged.
func (m ChatModel) switchModel(name string) (tea.Model, tea.Cmd) {
	if m.Running {
		return m.append(tagWarn + " finish or cancel this turn before /model"), nil
	}
	i := slices.IndexFunc(m.routes, func(r RouteInfo) bool { return r.Name == name })
	if i < 0 {
		return m.append(tagWarn + " unknown route " + clip(name) + " — /model lists routes"), nil
	}
	r := m.routes[i]
	target := r.Provider + "/" + r.Model
	if name == m.active {
		return m.append(tagModel + " already on " + name + " (" + target + ")"), nil
	}
	if m.switchFn != nil {
		if err := m.switchFn(name); err != nil {
			return m.append(tagWarn + " /model failed: " + clip(err.Error())), nil
		}
	}
	m.active, m.route = name, target
	return m.append(tagStatus + " switched to " + name + " (" + target + ")"), nil
}

// newSession rotates to a fresh session via the injected callback, then
// clears the scrollback and running totals. Refused mid-turn.
func (m ChatModel) newSession() (tea.Model, tea.Cmd) {
	if m.Running {
		return m.append(tagWarn + " finish or cancel this turn before /new"), nil
	}
	if m.newSess != nil {
		id, err := m.newSess()
		if err != nil {
			return m.append(tagWarn + " /new failed: " + clip(err.Error())), nil
		}
		if id != "" {
			m.sessionID = id
		}
	}
	m.Lines, m.lineTimes = nil, nil
	m.Usage, m.Cost = core.Usage{}, core.CostReport{}
	m.title, m.hist, m.histI = "", nil, -1
	m.goal, m.continueGoal = nil, false
	m = m.reloadGoal()
	m.homeOpen = true
	return m.append(tagStatus + " new session, history cleared"), nil
}

// resumeSession reopens the previous session via the injected callback and
// replays its transcript into the scrollback. Refused mid-turn; totals
// restart at zero because only the text survives the store.
func (m ChatModel) resumeSession() (tea.Model, tea.Cmd) {
	if m.Running {
		return m.append(tagWarn + " finish or cancel this turn before /resume"), nil
	}
	if len(m.sessions) > 0 && m.resumeByID != nil {
		items := make([]overlayItem, len(m.sessions))
		for i, s := range m.sessions {
			title := s.Title
			if title == "" {
				title = s.ID
			}
			items[i] = overlayItem{id: s.ID, title: title, detail: s.Detail}
		}
		ov := overlay{kind: overlaySessions, title: "Resume session", items: items}
		m.ov = &ov
		return m, nil
	}
	if m.resume == nil {
		return m.append(tagWarn + " resume is not available here"), nil
	}
	id, turns, err := m.resume()
	if err != nil {
		return m.append(tagWarn + " /resume failed: " + clip(err.Error())), nil
	}
	return m.applyResume(id, turns), nil
}

func (m ChatModel) resumeNamed(id string) (tea.Model, tea.Cmd) {
	if m.resumeByID == nil {
		return m.append(tagWarn + " resume is not available here"), nil
	}
	got, turns, err := m.resumeByID(id)
	if err != nil {
		return m.append(tagWarn + " /resume failed: " + clip(err.Error())), nil
	}
	return m.applyResume(got, turns), nil
}

func (m ChatModel) applyResume(id string, turns []HistoryTurn) ChatModel {
	m.Lines, m.lineTimes = nil, nil
	m.sessionID = id
	m.Usage, m.Cost = core.Usage{}, core.CostReport{}
	m.goal, m.continueGoal = nil, false
	m = m.reloadTodos()
	m = m.reloadGoal()
	for _, t := range turns {
		if t.Role == "user" {
			m = m.append(userLines(t.Text)...)
			continue
		}
		m = m.append(replyLines(t.Text)...)
	}
	return m.append(fmt.Sprintf("%s resumed session %s (%d turns)", tagReply, id, len(turns)))
}
