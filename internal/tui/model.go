package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ataidesorg/friday/internal/core"
)

const (
	defaultWidth  = 80
	defaultHeight = 24
	// chromeLines are header + status + prompt/outcome around the body.
	chromeLines = 3
)

// Model is the whole view state. Update returns a new Model; it never
// mutates its receiver.
type Model struct {
	Phase   core.Phase
	Lines   []string
	Pending *core.Approval
	Usage   core.Usage
	Cost    core.CostReport
	Diff    string
	Done    bool
	Outcome *core.Outcome
	// Calls counts tool calls seen so far.
	Calls int

	reply       chan<- core.ApprovalResolution
	question    *questionPrompt
	opts        Options
	width       int
	height      int
	costUnknown bool
	vp          viewport.Model
	now         func() time.Time
	style       styles
}

// NewModel returns an empty Model sized by opts.
func NewModel(opts Options) Model {
	w := opts.Width
	if w <= 0 {
		w = defaultWidth
	}
	m := Model{opts: opts, width: w, height: defaultHeight, now: time.Now, style: newStyles(!opts.NoColor)}
	return m.refresh()
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd { return nil }

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = v.Width, v.Height
		return m.refresh(), nil
	case PhaseMsg:
		m.Phase = core.Phase(v)
		return m.append(phaseLine(m.Phase)), nil
	case EventMsg:
		return m.event(v.E), nil
	case DeltaMsg:
		return m.append(deltaLines(string(v))...), nil
	case ApprovalMsg:
		a := v.A
		m.Pending, m.reply = &a, v.Reply
		m = m.append(approvalLine(a))
		for _, l := range strings.Split(a.Preview, "\n") {
			if l != "" {
				m = m.append("  " + l)
			}
		}
		return m, nil
	case QuestionMsg:
		m.question = newQuestionPrompt(v.Questions, v.Reply)
		if q := m.question.current(); q.Question != "" {
			m = m.append(tagApproval + " " + q.Question)
			for i, o := range q.Options {
				m = m.append(fmt.Sprintf("  %d. %s", i+1, o.Label))
			}
		}
		return m, nil
	case DoneMsg:
		o := v.Result.Outcome
		m.Done, m.Outcome, m.Diff = true, &o, v.Diff
		m.Usage, m.Cost = v.Result.Usage, v.Result.Cost
		m.costUnknown = v.Result.Cost.Actual == nil
		return m.append(doneLines(v.Result, v.Diff)...), tea.Quit
	case denyMsg:
		return m.resolve(core.ApprovalDenied, core.ApprovalOnce, string(v)), nil
	case tea.KeyMsg:
		return m.key(v)
	}
	return m, nil
}

func (m Model) event(e core.Event) Model {
	switch d := e.Data.(type) {
	case core.ModelUsage:
		m.Usage = m.Usage.Add(d.Usage)
		m = m.addCost(d.Cost.Actual)
	case core.ToolCalled:
		m.Calls++
	}
	return m.append(eventLines(e)...)
}

func (m Model) addCost(c *core.USDMicros) Model {
	if c == nil || m.costUnknown {
		m.costUnknown = true
		return m
	}
	sum := *c
	if m.Cost.Actual != nil {
		var err error
		if sum, err = m.Cost.Actual.Add(*c); err != nil {
			m.costUnknown = true
			return m
		}
	}
	m.Cost.Actual = &sum
	return m
}

// append adds lines on a fresh backing array so the receiver's slice is
// never written through. ponytail: O(n) per append; fine for a run's worth
// of lines, switch to a ring if runs grow to many thousands.
func (m Model) append(lines ...string) Model {
	if len(lines) == 0 {
		return m
	}
	m.Lines = append(slices.Clip(m.Lines), lines...)
	return m.refresh()
}

func (m Model) refresh() Model {
	m.vp.Width, m.vp.Height = m.width, max(1, m.height-chromeLines)
	wrapped := make([]string, len(m.Lines))
	for i, l := range m.Lines {
		wrapped[i] = m.style.wrap(m.width, m.style.line(l))
	}
	m.vp.SetContent(strings.Join(wrapped, "\n"))
	m.vp.GotoBottom()
	return m
}

// View implements tea.Model.
func (m Model) View() string {
	title := "friday · " + string(m.Phase)
	if m.Done {
		title = "friday · done"
	}
	parts := []string{m.style.wrap(m.width, m.style.header.Render(title)), m.vp.View(), m.style.wrap(m.width, m.style.dim.Render(m.status()))}
	switch {
	case m.Pending != nil:
		parts = append(parts, m.style.wrap(m.width, m.style.prompt.Render(approvalLine(*m.Pending))))
	case m.Outcome != nil:
		parts = append(parts, m.style.wrap(m.width, m.style.outcome(*m.Outcome).Render(outcomeLine(*m.Outcome))))
	}
	return strings.Join(parts, "\n")
}

func (m Model) status() string {
	cost := "cost " + money(m.Cost.Actual)
	if m.costUnknown {
		cost = "cost unknown"
	}
	if m.opts.Budget.MaxCost > 0 {
		cost += " / max " + m.opts.Budget.MaxCost.String()
	}
	tools := fmt.Sprintf("tools %d", m.Calls)
	if m.opts.Budget.MaxToolCalls > 0 {
		tools += fmt.Sprintf("/%d", m.opts.Budget.MaxToolCalls)
	}
	return fmt.Sprintf("tokens in %d out %d · %s · %s", m.Usage.InputTokens, m.Usage.OutputTokens, cost, tools)
}

// styles colour the tag of each line; off means plain text.
type styles struct {
	on                  bool
	header, dim, prompt lipgloss.Style
	tags                map[string]lipgloss.Style
}

func newStyles(on bool) styles {
	s := styles{on: on}
	if !on {
		return s
	}
	red, green, yellow, magenta, cyan := lipgloss.Color("1"), lipgloss.Color("2"), lipgloss.Color("3"), lipgloss.Color("5"), lipgloss.Color("6")
	s.header = lipgloss.NewStyle().Bold(true)
	s.dim = lipgloss.NewStyle().Faint(true)
	s.prompt = lipgloss.NewStyle().Bold(true).Foreground(magenta)
	s.tags = map[string]lipgloss.Style{
		tagPhase:    lipgloss.NewStyle().Bold(true).Foreground(cyan),
		tagWarn:     lipgloss.NewStyle().Foreground(yellow),
		tagApproval: lipgloss.NewStyle().Foreground(magenta),
		tagOutcome:  lipgloss.NewStyle().Bold(true),
		tagDeny:     lipgloss.NewStyle().Bold(true).Foreground(red),
		tagFailed:   lipgloss.NewStyle().Foreground(red),
		tagOK:       lipgloss.NewStyle().Foreground(green),
		tagUser:     lipgloss.NewStyle().Bold(true).Foreground(cyan),
		tagReply:    lipgloss.NewStyle().Bold(true).Foreground(green),
	}
	return s
}

// line colours the "[tag]" prefix; denials and failures are red, passes green.
func (s styles) line(l string) string {
	if !s.on {
		return l
	}
	tag, rest, ok := strings.Cut(l, " ")
	if !ok || !strings.HasPrefix(tag, "[") {
		return l
	}
	key := tag
	switch {
	case strings.HasPrefix(rest, string(core.EffectDeny)+" "), strings.HasPrefix(rest, string(core.ApprovalDenied)+" "), strings.HasPrefix(rest, string(core.ApprovalDenied)+":"):
		key = tagDeny
	case tag == tagValidate && strings.HasPrefix(rest, "passed "), tag == tagTool && strings.Contains(rest, " ok in "):
		key = tagOK
	case tag == tagValidate && strings.HasPrefix(rest, "failed "), tag == tagTool && strings.Contains(rest, " failed in "):
		key = tagFailed
	}
	st, found := s.tags[key]
	if !found {
		st = s.dim
	}
	return st.Render(tag) + " " + rest
}

func (s styles) outcome(o core.Outcome) lipgloss.Style {
	if !s.on {
		return lipgloss.NewStyle()
	}
	if o.Kind == core.OutcomeCompletedVerified {
		return s.tags[tagOK].Bold(true)
	}
	if o.Kind == core.OutcomeFailed {
		return s.tags[tagDeny]
	}
	return s.tags[tagOutcome]
}

// wrap hard-wraps at width; colour codes are preserved.
func (styles) wrap(width int, l string) string {
	return lipgloss.NewStyle().Width(width).Render(l)
}
