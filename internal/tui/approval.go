package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/ataidesorg/friday/internal/core"
)

// owner is who answers approval prompts; Friday never reads $USER.
var owner = core.Principal{Kind: core.PrincipalUser, Name: "owner"}

const (
	keyApproveOnce    = "y"
	keyApproveSession = "s"
	keyDeny           = "n"
)

func (m Model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if k.Type == tea.KeyCtrlC {
		if m.question != nil {
			m.question.finish(true)
			m.question = nil
		}
		return m.resolve(core.ApprovalDenied, core.ApprovalOnce, "interrupted"), tea.Quit
	}
	if m.question != nil {
		done, stop := m.question.key(k)
		if done {
			m.question.finish(stop)
			m.question = nil
		}
		return m, nil
	}
	if m.Pending == nil {
		vp, cmd := m.vp.Update(k)
		m.vp = vp
		return m, cmd
	}
	switch k.String() {
	case keyApproveOnce:
		return m.resolve(core.ApprovalApproved, core.ApprovalOnce, ""), nil
	case keyApproveSession:
		return m.resolve(core.ApprovalApproved, core.ApprovalSession, ""), nil
	case keyDeny:
		return m.resolve(core.ApprovalDenied, core.ApprovalOnce, ""), nil
	}
	return m, nil
}

// resolve answers the pending approval (if any) and clears it. A denial
// with a note is logged because the runtime's event carries no note.
func (m Model) resolve(d core.ApprovalDecision, scope core.ApprovalScope, note string) Model {
	if m.Pending == nil {
		return m
	}
	if m.reply != nil {
		m.reply <- core.ApprovalResolution{Decision: d, By: owner, At: m.now(), Scope: scope, Note: note}
	}
	m.Pending, m.reply = nil, nil
	if d == core.ApprovalDenied && note != "" {
		return m.append(tagApproval + " " + string(d) + ": " + note)
	}
	return m
}
