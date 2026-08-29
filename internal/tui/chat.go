package tui

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ataidesorg/friday/internal/core"
	"github.com/ataidesorg/friday/internal/runtime"
)

// Runner executes one conversation turn to completion, forwarding live
// events to obs so they stream into the scrollback, and returns the result.
// The CLI wires the real runner (runtime.Run with prior-turn history and
// per-turn persistence); tests supply a fake. A turn's ctx is cancellable so
// a later Ctrl-C can stop the turn without quitting the app.
type Runner func(ctx context.Context, prompt string, obs runtime.Observer) (runtime.Result, error)

const (
	// chatChrome is the header row above the scrollback. Composer frame and
	// footer are counted separately.
	chatChrome = 1
	// promptFrame is the composer box (top and bottom border).
	promptFrame = 2
	// framePad is the blank column on each side of the TUI.
	framePad = 2
	// promptMaxRows caps how far the prompt grows as the draft gains lines;
	// it starts at one row and grows to fit.
	promptMaxRows = 14
	// footerRows is the persistent key-hint bar under the prompt.
	footerRows = 1
	// slashMenuMax caps the typeahead menu shown while drafting a /command.
	slashMenuMax = 8
	// eventBuffer keeps the runtime goroutine from blocking on a busy UI.
	// ponytail: 64 is ample for one turn's events; raise if a turn ever
	// out-produces the render loop.
	eventBuffer = 64
)

// compactDoneMsg ends a /compact run; note describes what was folded away.
type compactDoneMsg struct {
	note string
	err  error
}

// turnDoneMsg ends a turn; err is non-nil when the run never produced a result.
type turnDoneMsg struct {
	res runtime.Result
	err error
}

type queuedPrompt struct {
	text    string
	display string
}

type pasteBlock struct {
	token string
	text  string
}

// chanObserver forwards runtime callbacks onto the turn's event channel, so
// events stream into the scrollback through the same render path as a run.
type chanObserver struct{ ch chan tea.Msg }

func (o chanObserver) OnEvent(e core.Event)  { o.ch <- EventMsg{E: e} }
func (o chanObserver) OnPhase(p core.Phase)  { o.ch <- PhaseMsg(p) }
func (o chanObserver) OnModelDelta(s string) { o.ch <- DeltaMsg(s) }

// ChatModel is the multi-turn REPL: a viewport scrollback above a textarea
// prompt. Update returns a new value and never mutates the receiver's Lines
// (copied on append); the widgets and event channel are shared by design.
type ChatModel struct {
	Lines   []string
	Usage   core.Usage
	Cost    core.CostReport
	Budget  core.TaskBudget
	Running bool

	// followTail pins the viewport to the newest line. PageUp/PageDown
	// clear it when the user leaves the bottom; sending a prompt sets it.
	followTail   bool
	queue        []queuedPrompt // drafts typed while a turn is running; sent in order when it ends
	queueOpen    bool           // expand the queued prompt strip above the composer
	mouseCopy    bool           // app-owned transcript drag selection is in progress
	mouseMoved   bool
	mouseStartX  int
	mouseStartY  int
	mouseEndX    int
	mouseEndY    int
	homeOpen     bool // show the welcome surface without deleting history
	homePane     bool // the welcome surface is the current pane; center it
	usageOpen    bool // show context/cost limits beside the model label
	toolsOpen    bool // show full tool output under each card; ctrl+e toggles
	showTools    bool // show tool call cards in the transcript
	showThinking bool // show transient thinking indicator
	promptFocus  bool // false when Tab has moved the keyboard to the scrollback
	sel          int  // selected stored line while the scrollback is focused

	runner    Runner
	events    chan tea.Msg
	vp        viewport.Model
	ta        textarea.Model
	sp        spinner.Model
	cstyle    chatStyles
	keys      Keymap
	width     int
	height    int
	ov        *overlay                     // modal selector over the pane; nil when closed
	reply     string                       // live streaming buffer; rendered after Lines, committed by finish
	pane      string                       // rendered conversation handed to the viewport; paneView re-slices it
	verbose   bool                         // /verbose: show the full event trace, not just tools and warnings
	hideAdvis bool                         // config tui.hide_advisories: drop unenforceable-guardrail warnings
	slashSel  int                          // cursor in the slash typeahead menu; reset when the draft changes
	atSel     int                          // cursor in the @-file typeahead menu
	atGone    string                       // @token whose menu esc dismissed; cleared when the token changes
	files     func(prefix string) []string // workspace file completion; nil disables @
	now       func() time.Time
	turnStart time.Time

	themes     []Theme            // selectable palettes; built-ins plus customs
	themeName  string             // committed palette
	styledName string             // palette currently painted (preview may differ)
	setTheme   func(string) error // persist a theme choice; may be nil

	route       string // provider/model shown by /model
	worktree    string // header tag for a worktree session; empty hides it
	folder      string
	branch      string
	dirty       bool
	ctxUsed     int
	ctxMax      int
	usageLimits string
	routes      []RouteInfo                                             // routes /model can switch between
	active      string                                                  // name of the route in use
	switchFn    func(string) error                                      // swap the active route live (/model NAME); may be nil
	newSess     func() (string, error)                                  // rotate to a fresh session (/new); may be nil
	resume      func() (string, []HistoryTurn, error)                   // reopen the previous session (/resume); may be nil
	resumeByID  func(id string) (string, []HistoryTurn, error)          // reopen a named session from the picker
	sessions    []SessionInfo                                           // prior sessions the picker lists
	mode        string                                                  // code | plan | ask; Shift+Tab cycles
	setMode     func(string) error                                      // persist mode on the session; may be nil
	compact     func(context.Context, runtime.Observer) (string, error) // fold history into a summary (/compact); may be nil
	commands    []CommandInfo                                           // custom slash commands (/commands)
	skills      []SkillInfo                                             // loaded skills (/skills)
	agents      []AgentInfo                                             // named agent profiles /agent lists
	setAgent    func(name string) error                                 // activates an agent profile; nil disables /agent
	agentName   string                                                  // active agent; empty means none
	baseCtx     context.Context                                         // parent of every turn ctx; set by RunChat
	cancel      context.CancelFunc                                      // cancels the in-flight turn; nil when idle

	providers       []ProviderInfo                                    // registry providers /connect offers
	connectFn       func(ConnectRequest) (RouteInfo, error)           // stores a key and writes the route (/connect); may be nil
	connectModelsFn func(ConnectRequest) ([]string, string)           // live model catalog for the wizard; may be nil
	loginFn         func(context.Context, string, func(string)) error // OAuth sign-in for the wizard; may be nil
	connGen         int                                               // generation counter guarding stale wizard messages
	conn            *connectState                                     // in-flight /connect wizard; nil when idle

	pending *core.Approval                 // approval awaiting a y/s/n answer; nil when none
	replyCh chan<- core.ApprovalResolution // answers the pending approval; capacity 1

	question *questionPrompt // multiple-choice overlay; nil when none

	sessionID string
	title     string
	copyFn    func(string) error
	saveCopy  func(path, text string) error
	notice    string
	lineTimes []time.Time
	pastes    []pasteBlock
	hist      []string  // sent user prompts, oldest first
	histI     int       // index while walking history; -1 = not walking
	lastEsc   time.Time // for Esc Esc clear / rewind (Grok: 800ms)
	escHint   bool      // footer: press esc again to clear
	escRewind bool      // silent first Esc on empty prompt arms rewind
	lastQuit  time.Time
	quitHint  bool
	quitKey   string
	lastCtrlN time.Time
	ctrlNHint bool
	yolo      bool
	auto      bool // approve write/execute without prompting; still ask for destructive
	vim       bool
	setVim    func(bool) error
	rewindFn  func(int) error
	forkFn    func() (string, error)
	renameFn  func(string) error
	doctor    []string
	multiline bool
	showTimes bool

	listAgents    func() []DashAgent
	createAgent   func() (string, error)
	runOn         func(context.Context, string, string, runtime.Observer) (runtime.Result, error)
	attachAgent   func(string) (string, []HistoryTurn, error)
	deleteAgent   func(string) error
	deleteSession func() (string, error)
	todosFn       func() []TodoItem
	goalFn        func() (core.Goal, bool)
	setGoal       func(core.Goal) error
	clearGoal     func() error
	goal          *core.Goal
	continueGoal  bool
	approvalSel   int // 0 allow once, 1 session, 2 reject
	editFn        func(string) (string, error)
	dash          bool
	dashSel       int
	dashDraft     string
	dashFocusList bool
	dashSearch    bool
	dashSearchQ   string
	dashLive      map[string]*dashRun
	todosOpen     bool
	todos         []TodoItem
	lastCtrlX     time.Time
	ctrlXHint     bool
}

// NewChat returns a chat model focused on an empty prompt.
func NewChat(opts Options, runner Runner) ChatModel {
	w := opts.Width
	if w <= 0 {
		w = defaultWidth
	}
	keys := opts.Keys
	if keys == (Keymap{}) {
		keys = DefaultKeymap()
	}
	themes := mergeThemes(builtinThemes(), opts.Themes)
	themeName, theme := opts.ThemeName, defaultTheme()
	if i := slices.IndexFunc(themes, func(t Theme) bool { return t.Name == themeName }); i >= 0 {
		theme = themes[i]
	} else {
		themeName = theme.Name
	}
	cstyle := themedStyles(!opts.NoColor, theme)
	ta := textarea.New()
	ta.Placeholder = "Message"
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.Prompt = "> "
	ta.SetPromptFunc(2, func(lineIdx int) string {
		if lineIdx == 0 {
			return "> "
		}
		return "  "
	})
	blank := lipgloss.NewStyle()
	ta.FocusedStyle.Base = blank
	ta.FocusedStyle.CursorLine = blank
	ta.FocusedStyle.Text = blank
	ta.FocusedStyle.Prompt = cstyle.dim
	ta.FocusedStyle.Placeholder = cstyle.dim
	ta.BlurredStyle = ta.FocusedStyle
	ta.Focus()
	sp := spinner.New()
	sp.Spinner = spinner.MiniDot
	sp.Style = cstyle.spin
	m := ChatModel{
		runner:          runner,
		events:          make(chan tea.Msg, eventBuffer),
		ta:              ta,
		sp:              sp,
		cstyle:          cstyle,
		keys:            keys,
		Budget:          opts.Budget,
		hideAdvis:       opts.HideAdvisories,
		compact:         opts.Compact,
		commands:        opts.Commands,
		skills:          opts.Skills,
		agents:          opts.Agents,
		setAgent:        opts.SetAgent,
		themes:          themes,
		themeName:       themeName,
		styledName:      themeName,
		setTheme:        opts.SetTheme,
		width:           w,
		height:          defaultHeight,
		now:             time.Now,
		route:           opts.Route,
		worktree:        opts.Worktree,
		folder:          opts.Folder,
		branch:          opts.Branch,
		dirty:           opts.Dirty,
		ctxMax:          opts.ContextWindow,
		usageLimits:     opts.UsageLimits,
		files:           opts.CompleteFiles,
		routes:          opts.Routes,
		active:          opts.Active,
		switchFn:        opts.SwitchModel,
		newSess:         opts.NewSession,
		resume:          opts.Resume,
		resumeByID:      opts.ResumeByID,
		sessions:        opts.Sessions,
		mode:            launchMode(opts.Mode),
		setMode:         opts.SetMode,
		providers:       opts.Providers,
		connectFn:       opts.Connect,
		connectModelsFn: opts.ConnectModels,
		loginFn:         opts.Login,
		baseCtx:         context.Background(),
		followTail:      true,
		promptFocus:     true,
		showTools:       true,
		showThinking:    true,
		sessionID:       opts.SessionID,
		title:           opts.Title,
		copyFn:          opts.Copy,
		saveCopy:        opts.SaveCopy,
		histI:           -1,
		yolo:            opts.AlwaysApprove,
		vim:             opts.VimMode,
		setVim:          opts.SetVimMode,
		rewindFn:        opts.Rewind,
		forkFn:          opts.Fork,
		renameFn:        opts.Rename,
		doctor:          opts.Doctor,
		listAgents:      opts.ListAgents,
		createAgent:     opts.CreateAgent,
		runOn:           opts.RunOn,
		attachAgent:     opts.AttachAgent,
		deleteAgent:     opts.DeleteAgent,
		deleteSession:   opts.DeleteSession,
		todosFn:         opts.Todos,
		goalFn:          opts.Goal,
		setGoal:         opts.SetGoal,
		clearGoal:       opts.ClearGoal,
		editFn:          opts.EditPrompt,
	}
	m = m.reloadGoal()
	return m.layout()
}

// Init implements tea.Model.
func (m ChatModel) Init() tea.Cmd { return tea.Batch(textarea.Blink, m.sp.Tick) }

// Update implements tea.Model.
func (m ChatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = v.Width, v.Height
		return m.layout(), nil
	case EventMsg:
		if d, ok := v.E.Data.(core.ContextAssembled); ok {
			m.ctxUsed, m.ctxMax = d.UsedTokens, d.BudgetTokens
		}
		m = m.noteToolEvent(v.E)
		m = m.noteGoalEvent(v.E)
		return m.stream(m.append(chatEventLines(v.E, m.verbose, m.showTools, m.hideAdvis)...))
	case dashEventMsg:
		return m.handleDashEvent(v)
	case editorDoneMsg:
		return m.applyEditor(v)
	case PhaseMsg:
		if m.verbose {
			return m.stream(m.append(phaseLine(core.Phase(v))))
		}
		return m.stream(m)
	case DeltaMsg:
		// Tokens accumulate into one growing reply, wrapped at render time —
		// never one stored line per delta.
		m.reply += string(v)
		return m.stream(m.layout())
	case compactDoneMsg:
		m.Running, m.reply, m.cancel = false, "", nil
		switch {
		case v.err == nil:
			m = m.append(tagReply + " " + v.note)
		case errors.Is(v.err, context.Canceled):
			m = m.append(tagWarn + " compact cancelled")
		default:
			m = m.append(tagWarn + " compact failed: " + clip(v.err.Error()))
		}
		// Idle again: no producer is left, so arming another waiter here would
		// leak a second permanent reader on the events channel.
		return m.layout(), nil
	case ApprovalMsg:
		a := v.A
		m.pending, m.replyCh, m.approvalSel = &a, v.Reply, 0
		if m.autoApprove(a) {
			return m.stream(m.resolveApproval(core.ApprovalApproved, core.ApprovalSession))
		}
		m = m.append(approvalLine(a))
		return m.stream(m.layout())
	case QuestionMsg:
		m.question = newQuestionPrompt(v.Questions, v.Reply)
		if q := m.question.current(); q.Question != "" {
			m = m.append(tagWarn + " " + q.Question)
		}
		return m.stream(m.layout())
	case turnDoneMsg:
		m.Running = false
		m.cancel = nil
		// A turn that dies mid-prompt orphans the approval; the approver
		// already returned on its ctx, so just clear the prompt.
		m.pending, m.replyCh = nil, nil
		if m.question != nil {
			m.question.finish(true)
			m.question = nil
		}
		return m.finish(v).dequeue()
	case connectModelsMsg:
		return m.connectModelsDone(v)
	case connectLoginMsg:
		return m.connectLoginDone(v)
	case connectNoteMsg:
		if m.conn != nil && m.conn.gen == v.gen && strings.TrimSpace(v.line) != "" {
			m = m.append(strings.TrimSpace(v.line))
		}
		return m, listenConn(v.ch)
	case spinner.TickMsg:
		// The tick chain runs for the app's life; only Running renders it.
		// A running turn relays the pane too, so the thinking line animates.
		var cmd tea.Cmd
		m.sp, cmd = m.sp.Update(v)
		if m.Running {
			m = m.layout()
		}
		return m, cmd
	case tea.MouseMsg:
		return m.mouse(v)
	case tea.KeyMsg:
		return m.key(v)
	}
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	return m, cmd
}

// stream re-arms the wait for the next turn event while a turn is running.
func (m ChatModel) stream(next ChatModel) (tea.Model, tea.Cmd) {
	if next.Running {
		return next, waitEvent(next.events)
	}
	return next, nil
}

// send starts one user prompt: slash commands dispatch immediately, anything
// else becomes a turn. The caller has already cleared the textarea.
func (m ChatModel) send(prompt string) (tea.Model, tea.Cmd) {
	return m.sendDisplay(prompt, prompt)
}

func (m ChatModel) sendDisplay(display, prompt string) (tea.Model, tea.Cmd) {
	m.followTail = true
	m.homeOpen = false
	if strings.HasPrefix(display, "/") {
		return m.command(prompt)
	}
	m.hist = append(m.hist, display)
	m.histI = -1
	m = m.append(userLines(display)...)
	m.Running, m.reply = true, ""
	m.turnStart = m.now()
	ctx, cancel := context.WithCancel(m.baseCtx)
	m.cancel = cancel
	return m, m.startTurn(ctx, cancel, prompt)
}

// dequeue starts the next queued prompt after a turn ends. Empty queue is a no-op
// unless an active goal asked to continue from idle.
func (m ChatModel) dequeue() (tea.Model, tea.Cmd) {
	if len(m.queue) > 0 {
		m.continueGoal = false
		prompt := m.queue[0]
		m.queue = append([]queuedPrompt(nil), m.queue[1:]...)
		return m.sendDisplay(prompt.display, prompt.text)
	}
	if !m.continueGoal {
		return m, nil
	}
	m.continueGoal = false
	m.followTail = true
	m.homeOpen = false
	m = m.append(tagStatus + " continuing goal")
	m.Running, m.reply = true, ""
	m.turnStart = m.now()
	ctx, cancel := context.WithCancel(m.baseCtx)
	m.cancel = cancel
	return m, m.startTurn(ctx, cancel, core.GoalContinuePrompt)
}

// startTurn runs the turn in the background and begins draining its events.
// ctx is the turn's cancellable context; Ctrl-C cancels it mid-turn.
func (m ChatModel) startTurn(ctx context.Context, cancel context.CancelFunc, prompt string) tea.Cmd {
	ch, runner := m.events, m.runner
	go func() {
		defer cancel() // release the turn ctx on normal completion; a prior Ctrl-C cancel is then a no-op
		res, err := runner(ctx, prompt, chanObserver{ch})
		ch <- turnDoneMsg{res: res, err: err}
	}()
	return waitEvent(ch)
}

// chanApprover asks the UI to decide: the request lands on the events
// channel as an ApprovalMsg and the answer comes back on its reply channel.
// It runs on the turn goroutine; ctx end (Ctrl-C, quit) unblocks it.
func chanApprover(ch chan tea.Msg) runtime.ApprovalFunc {
	return func(ctx context.Context, a core.Approval) (core.ApprovalResolution, error) {
		reply := make(chan core.ApprovalResolution, 1)
		select {
		case ch <- ApprovalMsg{A: a, Reply: reply}:
		case <-ctx.Done():
			return core.ApprovalResolution{}, ctx.Err()
		}
		select {
		case r := <-reply:
			return r, nil
		case <-ctx.Done():
			return core.ApprovalResolution{}, ctx.Err()
		}
	}
}

func waitEvent(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg { return <-ch }
}

// finish commits the turn: the streamed reply buffer is replaced by the
// result's summary (the same text, authoritative), or kept as-is when the
// turn errored mid-stream, then a per-turn status line lands.
func (m ChatModel) finish(v turnDoneMsg) ChatModel {
	partial := m.reply
	m.reply = ""
	if v.err != nil {
		out := replyLines(partial) // keep what streamed before the failure
		if errors.Is(v.err, context.Canceled) {
			out = append(out, tagWarn+" turn cancelled")
		} else {
			out = append(out, tagWarn+" turn failed: "+clip(v.err.Error()))
		}
		return m.append(out...)
	}
	m.Usage = m.Usage.Add(v.res.Usage)
	m.Cost = addCost(m.Cost, v.res.Cost)
	out := replyLines(v.res.Summary)
	if strings.TrimSpace(v.res.Summary) == "" && v.res.Outcome.Reason != "" {
		out = append(out, tagWarn+" "+clip(v.res.Outcome.Reason))
	}
	if v.res.Goal != nil {
		g := *v.res.Goal
		m.goal = &g
		out = append(out, goalStatusLine(g))
		m.continueGoal = v.res.ContinueGoal
	}
	return m.append(out...)
}

// addCost sums two cost reports field-wise into fresh pointers, so neither
// input is aliased. A field nil on both sides stays nil.
func addCost(a, b core.CostReport) core.CostReport {
	return core.CostReport{
		Estimated: addMicros(a.Estimated, b.Estimated),
		Actual:    addMicros(a.Actual, b.Actual),
	}
}

func addMicros(a, b *core.USDMicros) *core.USDMicros {
	if a == nil && b == nil {
		return nil
	}
	var sum core.USDMicros
	if a != nil {
		sum += *a
	}
	if b != nil {
		sum += *b
	}
	return &sum
}

// append adds lines on a fresh backing array so the receiver's slice is never
// written through, then relays out the viewport.
func (m ChatModel) append(lines ...string) ChatModel {
	if len(lines) == 0 {
		return m
	}
	m.Lines = append(slices.Clip(m.Lines), lines...)
	when := m.now()
	for range lines {
		m.lineTimes = append(slices.Clip(m.lineTimes), when)
	}
	return m.layout()
}

// RunChat renders the chat model full-screen until the user quits or ctx
// ends. The CLI supplies the runner; every turn streams through it.
func RunChat(ctx context.Context, out io.Writer, in io.Reader, opts Options, runner Runner) error {
	m := NewChat(opts, runner)
	m.baseCtx = ctx // turn ctxs derive from the app ctx, so quit cancels a live turn
	if opts.OnApprover != nil {
		opts.OnApprover(chanApprover(m.events))
	}
	if opts.OnAsker != nil {
		opts.OnAsker(askerFromChan(m.events))
	}
	p := tea.NewProgram(m,
		tea.WithContext(ctx), tea.WithInput(in), tea.WithOutput(out),
		tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := p.Run()
	if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
