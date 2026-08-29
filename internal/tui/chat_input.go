package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ataidesorg/friday/internal/core"
)

func (m ChatModel) mouse(v tea.MouseMsg) (tea.Model, tea.Cmd) {
	if v.Action == tea.MouseActionPress {
		m.notice = ""
		switch v.Button {
		case tea.MouseButtonWheelUp:
			m.vp.ScrollUp(3)
			m.followTail = m.vp.AtBottom()
			return m.layout(), nil
		case tea.MouseButtonWheelDown:
			m.vp.ScrollDown(3)
			m.followTail = m.vp.AtBottom()
			return m.layout(), nil
		case tea.MouseButtonLeft:
			if m.ov != nil || m.question != nil || m.pending != nil || m.conn != nil || m.dash || m.todosOpen {
				return m, nil
			}
			if col, row, ok := m.mousePaneCell(v.X, v.Y); ok {
				m.mouseCopy = true
				m.mouseMoved = false
				m.mouseStartX, m.mouseStartY = col, row
				m.mouseEndX, m.mouseEndY = col, row
			}
			return m, nil
		default:
			return m, nil
		}
	}
	if v.Action == tea.MouseActionMotion && m.mouseCopy {
		if col, row, ok := m.mousePaneCell(v.X, v.Y); ok {
			if col != m.mouseEndX || row != m.mouseEndY {
				m.mouseMoved = true
			}
			m.mouseEndX, m.mouseEndY = col, row
		}
		return m, nil
	}
	if v.Action == tea.MouseActionRelease && m.mouseCopy {
		m.mouseCopy = false
		if col, row, ok := m.mousePaneCell(v.X, v.Y); ok && (col != m.mouseStartX || row != m.mouseStartY) {
			m.mouseEndX, m.mouseEndY = col, row
			m.mouseMoved = true
		}
		if !m.mouseMoved {
			m.mouseStartX, m.mouseStartY, m.mouseEndX, m.mouseEndY = 0, 0, 0, 0
			return m, nil
		}
		text := m.copyPaneSelection()
		m.mouseMoved, m.mouseStartX, m.mouseStartY, m.mouseEndX, m.mouseEndY = false, 0, 0, 0, 0
		if strings.TrimSpace(text) == "" {
			return m, nil
		}
		return m.copyOut(text)
	}
	return m, nil
}

func (m ChatModel) mousePaneCell(x, y int) (int, int, bool) {
	row := y - chatChrome
	if row < 0 || row >= m.vp.Height {
		return 0, 0, false
	}
	col := x - framePad
	if col < 0 {
		col = 0
	}
	return col, row, true
}

func (m ChatModel) copyPaneSelection() string {
	lines := strings.Split(m.paneView(), "\n")
	if len(lines) == 0 {
		return ""
	}
	a := cellPos{x: m.mouseStartX, y: m.mouseStartY}
	b := cellPos{x: m.mouseEndX, y: m.mouseEndY}
	return cleanCopiedPaneSelection(lines, normalizeCellRange(a, b))
}

func (m ChatModel) key(v tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.notice = ""
	if m.question != nil {
		return m.questionKey(v)
	}
	if m.pending != nil {
		return m.approvalKey(v)
	}
	if m.ov != nil {
		return m.overlayKey(v)
	}
	if m.homeOpen && (v.Type == tea.KeyEsc || v.Type == tea.KeyTab) {
		m.homeOpen = false
		return m.layout(), nil
	}
	if m.queueOpen {
		if v.Type == tea.KeyEsc || v.Type == tea.KeyCtrlB {
			return m.toggleQueue()
		}
	}
	if m.todosOpen && (v.Type == tea.KeyEsc || v.Type == tea.KeyCtrlT) {
		return m.toggleTodos()
	}
	if m.dash {
		return m.dashKey(v)
	}
	if m.conn != nil {
		return m.connectKey(v)
	}
	if menu := m.slashMatches(); len(menu) > 0 {
		if next, cmd, handled := m.slashKey(v, menu); handled {
			return next, cmd
		}
	}
	if menu := m.atMatches(); len(menu) > 0 {
		if next, cmd, handled := m.atKey(v, menu); handled {
			return next, cmd
		}
	}
	if next, cmd, handled := m.scrollKey(v); handled {
		return next, cmd
	} else if cm, ok := next.(ChatModel); ok {
		m = cm
	}
	switch v.Type {
	case m.keys.Palette:
		return m.openPalette()
	case tea.KeyTab:
		m.promptFocus = false
		m.ta.Blur()
		m.followTail = false
		if n := len(m.Lines); n > 0 {
			m.sel = n - 1
		}
		return m.layout(), nil
	case tea.KeyShiftTab:
		return m.cycleMode()
	case tea.KeyCtrlE:
		m.toolsOpen = !m.toolsOpen
		return m.layout(), nil
	case tea.KeyCtrlO:
		return m.toggleYolo()
	case tea.KeyCtrlS:
		return m.resumeSession()
	case tea.KeyCtrlN:
		return m.confirmNew()
	case tea.KeyCtrlBackslash:
		return m.toggleDashboard()
	case tea.KeyCtrlT:
		return m.toggleTodos()
	case tea.KeyCtrlG:
		return m.openEditor()
	case tea.KeyCtrlQ:
		if m.cancel != nil {
			m.cancel()
			m.cancel = nil
		}
		return m.confirmQuit("ctrl+q")
	case tea.KeyCtrlU:
		m.vp.HalfPageUp()
		m.followTail = m.vp.AtBottom()
		return m, nil
	case tea.KeyCtrlD:
		m.vp.HalfPageDown()
		m.followTail = m.vp.AtBottom()
		return m, nil
	case tea.KeyCtrlC:
		// First Ctrl-C during a turn cancels it (the outstanding waiter
		// delivers the cancelled result); quitting always needs a second press.
		if m.Running && m.cancel != nil {
			m.cancel()
			m.cancel = nil
			m.lastQuit, m.quitHint, m.quitKey = m.now(), true, "ctrl+c"
			return m.append(tagWarn + " cancelling turn... press ctrl+c again to quit Friday"), nil
		}
		return m.confirmQuit("ctrl+c")
	case m.keys.ScrollUp:
		// Scroll by page directly: the viewport's own keymap only knows the
		// default keys, and these are rebindable.
		m.vp.PageUp()
		m.followTail = m.vp.AtBottom()
		return m, nil
	case m.keys.ScrollDown:
		m.vp.PageDown()
		m.followTail = m.vp.AtBottom()
		return m, nil
	case tea.KeyEsc:
		if m.Running && m.cancel != nil {
			m.cancel()
			m.cancel = nil
			return m.append(tagWarn + " cancelling turn..."), nil
		}
		return m.escKey()
	case tea.KeyCtrlJ:
		m.ta.InsertString("\n")
		return m.layout(), nil
	case tea.KeyEnter:
		if v.Alt != m.multiline {
			m.ta.InsertString("\n")
			return m.layout(), nil
		}
		display, prompt := m.draftPair()
		if strings.TrimSpace(prompt) == "" {
			return m, nil
		}
		if m.Running {
			m.queue = append(slices.Clip(m.queue), queuedPrompt{text: prompt, display: display})
			m.ta.Reset()
			m.pastes = nil
			return m.layout(), nil
		}
		m.ta.Reset()
		m.pastes = nil
		m.homeOpen = false
		return m.sendDisplay(display, prompt)
	}
	if v.Type == tea.KeyCtrlB {
		return m.toggleQueue()
	}
	if v.Paste && v.Type == tea.KeyRunes {
		if next, handled := m.pasteRunes(string(v.Runes)); handled {
			return next.layout(), nil
		}
	}
	if m.promptFocus && strings.TrimSpace(m.ta.Value()) == "" && v.String() == "?" {
		return m.openPalette()
	}
	if v.Type == tea.KeyUp && m.promptFocus && len(m.slashMatches()) == 0 &&
		(strings.TrimSpace(m.ta.Value()) == "" || m.histI >= 0) {
		return m.histPrev()
	}
	if v.Type == tea.KeyDown && m.histI >= 0 {
		return m.histNext()
	}
	before := len(m.slashMatches()) + len(m.atMatches())
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(v)
	if tok := lastToken(m.ta.Value()); tok != m.atGone {
		m.atGone = ""
	}
	if after := len(m.slashMatches()) + len(m.atMatches()); after != before || m.promptRows() != m.ta.Height() {
		// The draft changed shape: the prompt grew, or the typeahead menu
		// gained or lost rows; either way the pane must re-fit.
		m.slashSel, m.atSel = 0, 0
		m = m.layout()
	}
	return m, cmd
}

func wrapSel(sel, n, delta int) int {
	if n <= 0 {
		return 0
	}
	return (sel + delta%n + n) % n
}

// slashKey drives the typeahead menu for a drafted /command: arrows move,
// tab completes the draft, enter runs the selection, esc clears the draft.
// Unhandled keys fall through to normal editing.
func (m ChatModel) slashKey(v tea.KeyMsg, menu []slashEntry) (tea.Model, tea.Cmd, bool) {
	sel := min(m.slashSel, len(menu)-1)
	switch v.Type {
	case tea.KeyUp:
		m.slashSel = wrapSel(sel, len(menu), -1)
		return m, nil, true
	case tea.KeyDown:
		m.slashSel = wrapSel(sel, len(menu), 1)
		return m, nil, true
	case tea.KeyTab:
		m.ta.SetValue("/" + menu[sel].name + " ")
		m.ta.CursorEnd()
		m.slashSel = 0
		return m.layout(), nil, true
	case tea.KeyEnter:
		m.ta.Reset()
		m.slashSel = 0
		next, cmd := m.command("/" + menu[sel].name)
		if cm, ok := next.(ChatModel); ok {
			next = cm.layout() // the menu rows are gone; give them back to the pane
		}
		return next, cmd, true
	case tea.KeyEsc:
		m.ta.Reset()
		m.slashSel = 0
		return m.layout(), nil, true
	}
	return m, nil, false
}

// approvalKey answers a pending approval. Arrow keys move the highlighted
// row; enter commits it. y / s / n and esc still work as shortcuts.
// Ctrl-C denies and then cancels the turn. Other keys are swallowed so a
// half-typed draft never answers a prompt.
func (m ChatModel) approvalKey(v tea.KeyMsg) (tea.Model, tea.Cmd) {
	if v.Type == tea.KeyCtrlC {
		m = m.resolveApproval(core.ApprovalDenied, core.ApprovalOnce)
		if m.cancel != nil {
			m.cancel()
			m.cancel = nil
			m.lastQuit, m.quitHint, m.quitKey = m.now(), true, "ctrl+c"
			return m.append(tagWarn + " cancelling turn... press ctrl+c again to quit Friday"), nil
		}
		return m.confirmQuit("ctrl+c")
	}
	if v.Type == tea.KeyEsc {
		return m.resolveApproval(core.ApprovalDenied, core.ApprovalOnce), nil
	}
	switch v.Type {
	case tea.KeyUp, tea.KeyShiftTab:
		m.approvalSel = (m.approvalSel + len(approvalChoices) - 1) % len(approvalChoices)
		return m.layout(), nil
	case tea.KeyDown, tea.KeyTab:
		m.approvalSel = (m.approvalSel + 1) % len(approvalChoices)
		return m.layout(), nil
	case tea.KeyEnter:
		c := approvalChoices[m.approvalSel]
		return m.resolveApproval(c.decision, c.scope), nil
	}
	switch v.String() {
	case keyApproveOnce:
		return m.resolveApproval(core.ApprovalApproved, core.ApprovalOnce), nil
	case keyApproveSession:
		return m.resolveApproval(core.ApprovalApproved, core.ApprovalSession), nil
	case keyDeny:
		return m.resolveApproval(core.ApprovalDenied, core.ApprovalOnce), nil
	}
	return m, nil
}

func (m ChatModel) questionKey(v tea.KeyMsg) (tea.Model, tea.Cmd) {
	if v.Type == tea.KeyCtrlC {
		if m.question != nil {
			m.question.finish(true)
			m.question = nil
		}
		if m.cancel != nil {
			m.cancel()
			m.cancel = nil
			m.lastQuit, m.quitHint, m.quitKey = m.now(), true, "ctrl+c"
			return m.append(tagWarn + " cancelling turn... press ctrl+c again to quit Friday"), nil
		}
		return m.confirmQuit("ctrl+c")
	}
	done, stop := m.question.key(v)
	if !done {
		return m.layout(), nil
	}
	m.question.finish(stop)
	m.question = nil
	return m.layout(), nil
}

func (m ChatModel) resolveApproval(d core.ApprovalDecision, scope core.ApprovalScope) ChatModel {
	if m.pending == nil {
		return m
	}
	if m.replyCh != nil {
		m.replyCh <- core.ApprovalResolution{Decision: d, By: owner, At: m.now(), Scope: scope}
	}
	m.pending, m.replyCh = nil, nil
	return m
}

func (m ChatModel) histPrev() (tea.Model, tea.Cmd) {
	if len(m.hist) == 0 {
		return m, nil
	}
	if m.histI < 0 {
		m.histI = len(m.hist) - 1
	} else if m.histI > 0 {
		m.histI--
	}
	m.ta.SetValue(m.hist[m.histI])
	m.ta.CursorEnd()
	return m.layout(), nil
}

func (m ChatModel) histNext() (tea.Model, tea.Cmd) {
	if m.histI < 0 {
		return m, nil
	}
	if m.histI+1 >= len(m.hist) {
		m.histI = -1
		m.ta.Reset()
		return m.layout(), nil
	}
	m.histI++
	m.ta.SetValue(m.hist[m.histI])
	m.ta.CursorEnd()
	return m.layout(), nil
}

func (m ChatModel) copySelected() (tea.Model, tea.Cmd) {
	if m.sel < 0 || m.sel >= len(m.Lines) {
		return m.append(tagWarn + " nothing to copy"), nil
	}
	return m.copyOut(linePlain(m.Lines[m.sel]))
}

func (m ChatModel) copyReply() (tea.Model, tea.Cmd) {
	return m.copyOut(lastAssistantReply(m.Lines))
}

func (m ChatModel) copyOut(s string) (tea.Model, tea.Cmd) {
	s = strings.TrimSpace(s)
	if s == "" {
		return m.append(tagWarn + " nothing to copy"), nil
	}
	if m.copyFn == nil {
		return m.append(tagWarn + " copy is not available"), nil
	}
	if err := m.copyFn(s); err != nil {
		return m.append(tagWarn + " copy: " + clip(err.Error())), nil
	}
	m.notice = "copied to clipboard"
	return m.layout(), nil
}

func (m ChatModel) scrollKey(v tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if m.promptFocus {
		return m, nil, false
	}
	s := v.String()
	if m.vim {
		switch s {
		case "j":
			return m.moveSel(1), nil, true
		case "k":
			return m.moveSel(-1), nil, true
		case "g":
			return m.selTop(), nil, true
		case "G":
			return m.selBottom(), nil, true
		case "y":
			next, cmd := m.copyOut(blockPlain(m.Lines, m.sel))
			return next, cmd, true
		case "Y":
			next, cmd := m.copySelected()
			return next, cmd, true
		case "h":
			m.toolsOpen = false
			return m.layout(), nil, true
		case "l":
			m.toolsOpen = true
			return m.layout(), nil, true
		case "e":
			m.toolsOpen = !m.toolsOpen
			return m.layout(), nil, true
		case "i":
			return m.focusPrompt(), nil, true
		case "H":
			return m.jumpTurn(-1), nil, true
		case "L":
			return m.jumpTurn(1), nil, true
		case "?":
			next, cmd := m.openPalette()
			return next, cmd, true
		}
		if v.Type == tea.KeyRunes {
			return m, nil, true
		}
	}
	switch {
	case s == "?":
		next, cmd := m.openPalette()
		return next, cmd, true
	case v.Type == tea.KeySpace || s == " ":
		return m.focusPrompt(), nil, true
	case v.Type == tea.KeyTab:
		return m.focusPrompt(), nil, true
	case v.Type == tea.KeyUp:
		return m.moveSel(-1), nil, true
	case v.Type == tea.KeyDown:
		return m.moveSel(1), nil, true
	case v.Type == tea.KeyShiftLeft:
		return m.jumpTurn(-1), nil, true
	case v.Type == tea.KeyShiftRight:
		return m.jumpTurn(1), nil, true
	case v.Type == tea.KeyHome:
		return m.selTop(), nil, true
	case v.Type == tea.KeyEnd:
		return m.selBottom(), nil, true
	case v.Type == tea.KeyLeft:
		m.toolsOpen = false
		return m.layout(), nil, true
	case v.Type == tea.KeyRight:
		m.toolsOpen = true
		return m.layout(), nil, true
	case v.Type == tea.KeyEnter:
		next, cmd := m.copyOut(blockPlain(m.Lines, m.sel))
		return next, cmd, true
	}
	return m.focusPrompt(), nil, false
}

func (m ChatModel) moveSel(delta int) ChatModel {
	if n := len(m.Lines); n > 0 {
		m.sel = min(n-1, max(0, m.sel+delta))
	}
	m.followTail = false
	return m.layout()
}

func (m ChatModel) selTop() ChatModel {
	m.sel, m.followTail = 0, false
	m.vp.GotoTop()
	return m.layout()
}

func (m ChatModel) selBottom() ChatModel {
	if n := len(m.Lines); n > 0 {
		m.sel = n - 1
	}
	m.followTail = true
	m.vp.GotoBottom()
	return m.layout()
}

func (m ChatModel) jumpTurn(dir int) ChatModel {
	if len(m.Lines) == 0 {
		return m
	}
	for i := m.sel + dir; i >= 0 && i < len(m.Lines); i += dir {
		if strings.HasPrefix(m.Lines[i], tagUser+" ") {
			m.sel = i
			m.followTail = false
			return m.layout()
		}
	}
	return m
}

func (m ChatModel) escKey() (tea.Model, tea.Cmd) {
	if m.quitHint {
		m.lastQuit, m.quitHint, m.quitKey = time.Time{}, false, ""
		return m.layout(), nil
	}
	if d := strings.TrimSpace(m.ta.Value()); d != "" {
		now := m.now()
		if m.escHint && !m.lastEsc.IsZero() && now.Sub(m.lastEsc) <= 800*time.Millisecond {
			m.ta.Reset()
			m.lastEsc, m.escHint, m.escRewind, m.histI = time.Time{}, false, false, -1
			return m.layout(), nil
		}
		m.lastEsc, m.escHint, m.escRewind = now, true, false
		return m.layout(), nil
	}
	m.escHint = false
	if len(m.queue) > 0 {
		m.queue = m.queue[:len(m.queue)-1]
		m.escRewind = false
		return m.layout(), nil
	}
	if !m.Running && len(m.Lines) > 0 {
		now := m.now()
		if m.escRewind && !m.lastEsc.IsZero() && now.Sub(m.lastEsc) <= 800*time.Millisecond {
			m.lastEsc, m.escRewind = time.Time{}, false
			return m.openRewind()
		}
		m.lastEsc, m.escRewind = now, true
		return m, nil
	}
	return m, nil
}

func (m ChatModel) confirmNew() (tea.Model, tea.Cmd) {
	now := m.now()
	if m.ctrlNHint && !m.lastCtrlN.IsZero() && now.Sub(m.lastCtrlN) <= time.Second {
		m.lastCtrlN, m.ctrlNHint = time.Time{}, false
		return m.newSession()
	}
	m.lastCtrlN, m.ctrlNHint = now, true
	return m.append(tagWarn + " press ctrl+n again to start a new session"), nil
}

func (m ChatModel) confirmQuit(key string) (tea.Model, tea.Cmd) {
	now := m.now()
	if m.quitHint && m.quitKey == key && !m.lastQuit.IsZero() && now.Sub(m.lastQuit) <= time.Second {
		if m.cancel != nil {
			m.cancel()
			m.cancel = nil
		}
		m.lastQuit, m.quitHint, m.quitKey = time.Time{}, false, ""
		return m, tea.Quit
	}
	m.lastQuit, m.quitHint, m.quitKey = now, true, key
	return m.append(tagWarn + " press " + key + " again to quit Friday"), nil
}

func (m ChatModel) toggleQueue() (tea.Model, tea.Cmd) {
	m.queueOpen = !m.queueOpen
	return m.layout(), nil
}

func (m ChatModel) pasteRunes(text string) (ChatModel, bool) {
	if !strings.Contains(text, "\n") {
		return m, false
	}
	lines := strings.Count(strings.TrimRight(text, "\n"), "\n") + 1
	if lines < 1 {
		lines = 1
	}
	token := fmt.Sprintf("[Pasted +%d lines]", lines)
	m.pastes = append(slices.Clip(m.pastes), pasteBlock{token: token, text: text})
	m.ta.InsertString(token)
	return m, true
}

func (m ChatModel) draftPair() (string, string) {
	display := strings.TrimSpace(m.ta.Value())
	text := display
	for _, p := range m.pastes {
		text = strings.Replace(text, p.token, p.text, 1)
	}
	return display, strings.TrimSpace(text)
}

func (m ChatModel) focusPrompt() ChatModel {
	m.promptFocus = true
	m.ta.Focus()
	return m.layout()
}

// atKey drives the @-file menu: arrows move, tab or enter completes the
// token, esc dismisses the menu for this token. Other keys edit normally.
func (m ChatModel) atKey(v tea.KeyMsg, menu []string) (tea.Model, tea.Cmd, bool) {
	sel := min(m.atSel, len(menu)-1)
	switch v.Type {
	case tea.KeyUp:
		m.atSel = wrapSel(sel, len(menu), -1)
		return m, nil, true
	case tea.KeyDown:
		m.atSel = wrapSel(sel, len(menu), 1)
		return m, nil, true
	case tea.KeyTab, tea.KeyEnter:
		val := m.ta.Value()
		tok := lastToken(val)
		m.ta.SetValue(val[:len(val)-len(tok)] + "@" + menu[sel] + " ")
		m.ta.CursorEnd()
		m.atSel = 0
		return m.layout(), nil, true
	case tea.KeyEsc:
		m.atGone = lastToken(m.ta.Value())
		m.atSel = 0
		return m.layout(), nil, true
	}
	return m, nil, false
}
