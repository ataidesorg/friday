package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ataidesorg/friday/internal/core"
)

func (m ChatModel) reloadGoal() ChatModel {
	if m.goalFn != nil {
		if g, ok := m.goalFn(); ok {
			cp := g
			m.goal = &cp
			return m
		}
		m.goal = nil
		return m
	}
	return m
}

func (m ChatModel) noteGoalEvent(e core.Event) ChatModel {
	d, ok := e.Data.(core.ToolCompleted)
	if !ok {
		return m
	}
	switch d.Tool {
	case "goal_complete", "goal_blocked", "goal_wait":
		return m.reloadGoal()
	}
	return m
}

func (m ChatModel) persistGoal(g core.Goal) (ChatModel, error) {
	if m.setGoal != nil {
		if err := m.setGoal(g); err != nil {
			return m, err
		}
	}
	cp := g
	m.goal = &cp
	return m, nil
}

func (m ChatModel) dropGoal() (ChatModel, error) {
	if m.clearGoal != nil {
		if err := m.clearGoal(); err != nil {
			return m, err
		}
	}
	m.goal = nil
	return m, nil
}

func (m ChatModel) applyGoal(line string) (tea.Model, tea.Cmd) {
	if m.Running {
		return m.append(tagWarn + " finish or cancel this turn before /goal"), nil
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return m.append(goalStatusLines(m.goal)...), nil
	}
	tokens, rest := splitGoalTokens(fields)
	if len(rest) == 0 {
		if tokens != "" {
			return m.append(tagWarn + " usage: /goal start [--tokens N] OBJECTIVE"), nil
		}
		return m.append(goalStatusLines(m.goal)...), nil
	}
	switch strings.ToLower(rest[0]) {
	case "status":
		return m.append(goalStatusLines(m.goal)...), nil
	case "pause":
		return m.goalPause()
	case "resume":
		return m.goalResume()
	case "clear":
		return m.goalClear()
	case "start":
		return m.goalStart(tokens, strings.Join(rest[1:], " "))
	case "edit":
		return m.goalEdit(tokens, strings.Join(rest[1:], " "))
	default:
		return m.goalStart(tokens, strings.Join(rest, " "))
	}
}

func (m ChatModel) goalStart(tokens, objective string) (tea.Model, tea.Cmd) {
	objective = strings.TrimSpace(objective)
	if objective == "" {
		return m.append(tagWarn + " usage: /goal start [--tokens N] OBJECTIVE"), nil
	}
	if m.goal != nil && m.goal.Open() {
		return m.append(tagWarn + " a goal is already " + string(m.goal.Status) + " — /goal edit to change it, or /goal clear then /goal start"), nil
	}
	g, err := core.NewGoal(objective, m.now())
	if err != nil {
		return m.append(tagWarn + " /goal: " + clip(err.Error())), nil
	}
	g, err = applyGoalTokens(g, tokens, m.now())
	if err != nil {
		return m.append(tagWarn + " /goal: " + clip(err.Error())), nil
	}
	next, err := m.persistGoal(g)
	if err != nil {
		return m.append(tagWarn + " /goal: " + clip(err.Error())), nil
	}
	return next.append(goalStatusLines(next.goal)...), nil
}

func (m ChatModel) goalEdit(tokens, objective string) (tea.Model, tea.Cmd) {
	if m.goal == nil {
		return m.append(tagWarn + " no goal to edit — /goal start OBJECTIVE"), nil
	}
	objective = strings.TrimSpace(objective)
	if objective == "" && tokens == "" {
		return m.append(tagWarn + " usage: /goal edit [--tokens N] OBJECTIVE"), nil
	}
	g := *m.goal
	var err error
	if objective != "" {
		g, err = g.Edit(objective, m.now())
		if err != nil {
			return m.append(tagWarn + " /goal: " + clip(err.Error())), nil
		}
	}
	g, err = applyGoalTokens(g, tokens, m.now())
	if err != nil {
		return m.append(tagWarn + " /goal: " + clip(err.Error())), nil
	}
	next, err := m.persistGoal(g)
	if err != nil {
		return m.append(tagWarn + " /goal: " + clip(err.Error())), nil
	}
	return next.append(goalStatusLines(next.goal)...), nil
}

func (m ChatModel) goalPause() (tea.Model, tea.Cmd) {
	if m.goal == nil {
		return m.append(tagWarn + " no goal to pause"), nil
	}
	g, err := m.goal.Pause(core.GoalCauseUser, m.now())
	if err != nil {
		return m.append(tagWarn + " /goal: " + clip(err.Error())), nil
	}
	next, err := m.persistGoal(g)
	if err != nil {
		return m.append(tagWarn + " /goal: " + clip(err.Error())), nil
	}
	return next.append(goalStatusLines(next.goal)...), nil
}

func (m ChatModel) goalResume() (tea.Model, tea.Cmd) {
	if m.goal == nil {
		return m.append(tagWarn + " no goal to resume"), nil
	}
	g, err := m.goal.Resume(m.now())
	if err != nil {
		return m.append(tagWarn + " /goal: " + clip(err.Error())), nil
	}
	next, err := m.persistGoal(g)
	if err != nil {
		return m.append(tagWarn + " /goal: " + clip(err.Error())), nil
	}
	return next.append(goalStatusLines(next.goal)...), nil
}

func (m ChatModel) goalClear() (tea.Model, tea.Cmd) {
	if m.goal == nil {
		return m.append(tagStatus + " no goal"), nil
	}
	next, err := m.dropGoal()
	if err != nil {
		return m.append(tagWarn + " /goal: " + clip(err.Error())), nil
	}
	return next.append(tagStatus + " goal cleared"), nil
}

func applyGoalTokens(g core.Goal, tokens string, now time.Time) (core.Goal, error) {
	if strings.TrimSpace(tokens) == "" {
		return g, nil
	}
	n, err := core.ParseTokenBudget(tokens)
	if err != nil {
		return core.Goal{}, err
	}
	return g.WithTokenBudget(n, now)
}

func splitGoalTokens(fields []string) (tokens string, rest []string) {
	rest = make([]string, 0, len(fields))
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		switch {
		case f == "--tokens" && i+1 < len(fields):
			tokens = fields[i+1]
			i++
		case strings.HasPrefix(f, "--tokens="):
			tokens = strings.TrimPrefix(f, "--tokens=")
		default:
			rest = append(rest, f)
		}
	}
	return tokens, rest
}

func goalStatusLine(g core.Goal) string {
	line := fmt.Sprintf("%s goal %s", tagStatus, g.Status)
	if g.PauseCause != "" {
		line += " (" + string(g.PauseCause) + ")"
	}
	if g.Objective != "" {
		line += ": " + clip(g.Objective)
	}
	return line
}

func goalStatusLines(g *core.Goal) []string {
	if g == nil {
		return []string{tagStatus + " no goal"}
	}
	out := []string{goalStatusLine(*g)}
	if g.ID != "" {
		out = append(out, "  id "+string(g.ID))
	}
	if g.AutomaticTurns > 0 || g.MaxAutomaticTurns > 0 {
		maxTurns := g.MaxAutomaticTurns
		if maxTurns <= 0 {
			maxTurns = core.DefaultGoalAutomaticTurns
		}
		out = append(out, fmt.Sprintf("  turns %d/%d", g.AutomaticTurns, maxTurns))
	}
	if g.TokenBudget > 0 {
		out = append(out, fmt.Sprintf("  tokens %d/%d", g.Usage.Total(), g.TokenBudget))
	}
	if g.Evidence != "" {
		out = append(out, "  evidence "+string(g.EvidenceKind)+" — "+clip(g.Evidence))
	}
	if g.BlockReason != "" {
		out = append(out, "  blocked "+clip(g.BlockReason))
	}
	if g.WaitReason != "" {
		out = append(out, "  waiting "+clip(g.WaitReason))
	}
	return out
}
