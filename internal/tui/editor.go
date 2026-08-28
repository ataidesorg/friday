package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type editorDoneMsg struct {
	path string
	err  error
}

func (m ChatModel) openEditor() (tea.Model, tea.Cmd) {
	if m.Running {
		return m.append(tagWarn + " finish or cancel this turn before editing the prompt"), nil
	}
	draft := m.ta.Value()
	if m.editFn != nil {
		got, err := m.editFn(draft)
		if err != nil {
			return m.append(tagWarn + " editor: " + clip(err.Error())), nil
		}
		m.ta.SetValue(got)
		m.ta.CursorEnd()
		return m.focusPrompt(), nil
	}
	path, cmd, err := editorCmd(draft)
	if err != nil {
		return m.append(tagWarn + " editor: " + clip(err.Error())), nil
	}
	return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
		return editorDoneMsg{path: path, err: err}
	})
}

func (m ChatModel) applyEditor(v editorDoneMsg) (tea.Model, tea.Cmd) {
	defer func() { _ = os.Remove(v.path) }()
	if v.err != nil {
		return m.append(tagWarn + " editor: " + clip(v.err.Error())), nil
	}
	b, err := os.ReadFile(v.path) //nolint:gosec // temp file Friday created
	if err != nil {
		return m.append(tagWarn + " editor: " + clip(err.Error())), nil
	}
	m.ta.SetValue(strings.TrimRight(string(b), "\n"))
	m.ta.CursorEnd()
	return m.focusPrompt(), nil
}

func editorCmd(draft string) (string, *exec.Cmd, error) {
	f, err := os.CreateTemp("", "friday-prompt-*.md")
	if err != nil {
		return "", nil, err
	}
	path := f.Name()
	if _, err := f.WriteString(draft); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", nil, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", nil, err
	}
	argv := resolveEditor()
	if len(argv) == 0 {
		_ = os.Remove(path)
		return "", nil, fmt.Errorf("no editor")
	}
	cmd := exec.Command(argv[0], append(argv[1:], path)...) //nolint:gosec // user $VISUAL/$EDITOR plus a temp file we own
	return path, cmd, nil
}

func resolveEditor() []string {
	for _, k := range []string{"VISUAL", "EDITOR"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return strings.Fields(v)
		}
	}
	return []string{"vi"}
}
