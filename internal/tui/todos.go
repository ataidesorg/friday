package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ataidesorg/ink/internal/core"
)

func (m ChatModel) toggleTodos() (tea.Model, tea.Cmd) {
	m.todosOpen = !m.todosOpen
	if m.todosOpen {
		m = m.reloadTodos()
	}
	return m.layout(), nil
}

func (m ChatModel) reloadTodos() ChatModel {
	if m.todosFn != nil {
		m.todos = m.todosFn()
	}
	return m
}

func (m ChatModel) noteToolEvent(e core.Event) ChatModel {
	d, ok := e.Data.(core.ToolCompleted)
	if !ok || d.Tool != "todo_write" {
		return m
	}
	return m.reloadTodos()
}

func (m ChatModel) todosView(width, height int) string {
	width = max(20, width)
	height = max(3, height)
	rows := []string{m.cstyle.overlayTitle("Todos")}
	if len(m.todos) == 0 {
		rows = append(rows, m.cstyle.dimText("empty — the agent fills this with todo_write"))
	}
	for _, it := range m.todos {
		mark := "[ ]"
		switch it.Status {
		case "in_progress":
			mark = "[~]"
		case "completed":
			mark = "[x]"
		case "cancelled":
			mark = "[-]"
		}
		rows = append(rows, fit(fmt.Sprintf("%s %s", mark, it.Content), width-4))
	}
	rows = append(rows, m.cstyle.dimText("ctrl+t closes"))
	box := m.cstyle.modal.Width(min(56, width-4)).Render(strings.Join(rows, "\n"))
	return centerBlock(box, width, height)
}

func (m ChatModel) queueRows() int {
	if len(m.queue) == 0 {
		if m.queueOpen {
			return 1
		}
		return 0
	}
	if m.queueOpen {
		return min(len(m.queue)+1, 4)
	}
	return 1
}

func (m ChatModel) queueStrip(width int) string {
	if len(m.queue) == 0 && !m.queueOpen {
		return ""
	}
	limit := m.queueRows()
	rows := make([]string, 0, max(1, limit))
	if len(m.queue) == 0 {
		rows = append(rows, m.cstyle.dimText("Next prompt  empty"))
	} else if len(m.queue) > 0 {
		head := fmt.Sprintf("Next prompt  %s", queuePreview(m.queue[0].display))
		if len(m.queue) > 1 {
			head = fmt.Sprintf("Next prompt  %s  (+%d)", queuePreview(m.queue[0].display), len(m.queue)-1)
		}
		rows = append(rows, m.cstyle.dimText(head))
	}
	if m.queueOpen {
		if len(m.queue) > 1 {
			for i, prompt := range m.queue[1:] {
				if len(rows) >= limit {
					break
				}
				rows = append(rows, m.cstyle.dimText(fmt.Sprintf("Queued %d  %s", i+2, queuePreview(prompt.display))))
			}
		}
	}
	for i, row := range rows {
		rows[i] = fit(row, max(1, width))
	}
	return strings.Join(rows, "\n")
}

func queuePreview(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
