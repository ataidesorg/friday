// Package tui shows a run to a human: a Bubble Tea view on a terminal and
// the identical lines as plain text otherwise. Both paths feed the same
// Model, so `friday run --no-tui | cat` prints exactly what the terminal
// shows, minus layout and colour.
//
// Nothing here reads the environment or the workspace; everything rendered
// comes from runtime events, which are already summarised and redacted.
package tui

import (
	"context"
	"io"

	"github.com/ataidesorg/friday/internal/core"
	"github.com/ataidesorg/friday/internal/runtime"
)

// Options configure New.
type Options struct {
	// IsTTY selects the Bubble Tea program; plain lines otherwise.
	IsTTY bool
	// NoColor disables ANSI colour even on a terminal.
	NoColor bool
	// Budget is shown in the status line; zero fields are hidden.
	Budget core.TaskBudget
	// Width is the initial width in cells; 0 means 80.
	Width int
	// Route is the active provider/model shown by the chat /model command;
	// empty renders as unknown.
	Route string
	// Worktree is the header tag naming the dedicated worktree the session
	// runs on; empty hides it.
	Worktree string
	// Folder is the workspace directory basename shown in the header.
	Folder string
	// Branch is the current git branch; empty if git is unavailable.
	Branch string
	// Dirty marks an uncommitted git worktree in the header.
	Dirty bool
	// ContextWindow is the model's max context tokens (0 = unknown).
	ContextWindow int
	// UsageLimits summarizes configured spend ceilings beyond the per-task
	// budget, for example "session $2 / day $5". Empty hides them.
	UsageLimits string
	// CompleteFiles lists workspace files matching a fragment, feeding the
	// @-attachment typeahead; nil disables it.
	CompleteFiles func(prefix string) []string
	// Routes are the named routes the chat /model command lists; empty hides
	// the list.
	Routes []RouteInfo
	// Active is the name of the route in use at launch; /model marks it and
	// SwitchModel changes it.
	Active string
	// NewSession rotates the chat to a fresh session (called by /new); nil
	// leaves /new clearing the view only. It returns the new session id
	// (may be empty). It runs on the UI goroutine.
	NewSession func() (string, error)
	// SwitchModel changes the active route live (called by /model NAME); nil
	// disables switching. It runs on the UI goroutine and is refused mid-turn.
	SwitchModel func(name string) error
	// Compact folds the transcript into a model-written summary (/compact);
	// nil disables the command. Runs on a background goroutine like Runner.
	Compact func(ctx context.Context, obs runtime.Observer) (string, error)
	// Keys rebinds the chat shortcuts; the zero value means DefaultKeymap.
	// Build it with ParseKeymap so conflicts are rejected before launch.
	Keys Keymap
	// Themes are extra selectable palettes (user customs loaded by the CLI);
	// the built-ins are always present. A custom sharing a built-in's name
	// replaces it.
	Themes []Theme
	// ThemeName is the palette active at launch; empty or unknown keeps the
	// default.
	ThemeName string
	// HideAdvisories drops warnings about guardrails that could not be
	// enforced (unpriced model, no validation command) from the transcript.
	HideAdvisories bool
	// SetTheme persists a theme choice (called by the theme picker); nil
	// keeps choices session-only. It runs on the UI goroutine.
	SetTheme func(name string) error
	// Commands are the user's custom slash commands, loaded by the CLI from
	// the command directories; /NAME expands the body into a turn.
	Commands []CommandInfo
	// Skills are loaded agent skills; /skills lists them. They are not
	// mixed into the Ctrl+P palette.
	Skills []SkillInfo
	// Agents are the named agent profiles /agent lists; empty hides the
	// selector.
	Agents []AgentInfo
	// SetAgent activates a named agent profile ("" resets to none); nil
	// disables /agent. It runs on the UI goroutine and is refused mid-turn.
	SetAgent func(name string) error
	// Resume points the chat at the most recent prior session that has turns
	// (called by /resume); nil disables the command. It returns the resumed
	// session's id and transcript for replay. It runs on the UI goroutine
	// and is refused mid-turn.
	Resume func() (string, []HistoryTurn, error)
	// Sessions are prior conversations /resume can pick; empty falls back
	// to Resume (the previous session only).
	Sessions []SessionInfo
	// ResumeByID loads a named session from the picker; nil disables picking.
	ResumeByID func(id string) (string, []HistoryTurn, error)
	// Mode is the launch session mode (code, plan, ask). Empty means code.
	Mode string
	// SetMode persists a Shift+Tab mode change; nil keeps it session-local.
	SetMode func(name string) error
	// OnApprover hands the CLI the chat's interactive approver (y/s/n prompt
	// with a preview) before the first turn; nil keeps the CLI's default.
	OnApprover func(runtime.ApprovalFunc)
	// OnAsker hands the CLI the chat's multiple-choice asker before the first
	// turn; nil leaves ask_user_question non-interactive (unavailable).
	OnAsker func(core.AskFunc)
	// SessionID is shown by /status; empty hides it.
	SessionID string
	// Title is the current session title shown by /status; empty hides it.
	Title string
	// Copy puts text on the clipboard (/copy, y, /export). Nil disables copying.
	Copy func(string) error
	// SaveCopy writes copied text to a path (/copy FILE). Nil disables that form.
	SaveCopy func(path, text string) error
	// AlwaysApprove starts the session with permission prompts skipped.
	AlwaysApprove bool
	// VimMode starts the session with vim-style scrollback keys.
	VimMode bool
	// SetVimMode persists a /vim-mode toggle; nil keeps it session-local.
	SetVimMode func(bool) error
	// Rewind truncates stored history to the first keepUserTurns user prompts
	// (/rewind). Nil still truncates the on-screen transcript.
	Rewind func(keepUserTurns int) error
	// Fork copies the current session into a new id and points the chat at it.
	Fork func() (string, error)
	// Rename persists a /rename title; nil keeps it session-local.
	Rename func(string) error
	// Doctor is extra /doctor lines from the CLI (terminal, tmux, ssh).
	Doctor []string
	// ListAgents is the dashboard roster (Ctrl+\ / /dashboard). Nil hides it.
	ListAgents func() []DashAgent
	// CreateAgent starts an empty session without switching the live one.
	CreateAgent func() (string, error)
	// RunOn runs a prompt against a session id (dashboard dispatch). Nil
	// makes dispatch attach and send on the live session instead.
	RunOn func(ctx context.Context, id, prompt string, obs runtime.Observer) (runtime.Result, error)
	// AttachAgent points the live session at id and returns its transcript.
	AttachAgent func(id string) (string, []HistoryTurn, error)
	// DeleteAgent permanently removes a session.
	DeleteAgent func(id string) error
	// DeleteSession permanently removes the live session and returns the new
	// live session id. Nil disables /delete.
	DeleteSession func() (string, error)
	// Todos is the live session task list for the Ctrl+T pane.
	Todos func() []TodoItem
	// EditPrompt is a test seam for /edit-prompt. Nil uses $VISUAL/$EDITOR.
	EditPrompt func(draft string) (string, error)
	// Providers are the registry providers the /connect wizard offers; the
	// custom-endpoint row is always present.
	Providers []ProviderInfo
	// Connect stores the wizard's credential and writes the provider and
	// route into the user config, returning the new route (called by
	// /connect); nil disables the command. It runs on the UI goroutine and
	// is refused mid-turn. The Key it receives is a secret: implementations
	// register it with the redactor first and never write it to config
	// files or logs.
	Connect func(ConnectRequest) (RouteInfo, error)
	// ConnectModels fetches the provider's live model catalog with the
	// wizard's fresh credential — or, when Key is empty, the sign-in token
	// the Login callback stored — returning the ids plus a note on degraded
	// fetches. It runs on a background goroutine; nil skips catalog
	// validation and the wizard asks for a typed model id.
	ConnectModels func(ConnectRequest) ([]string, string)
	// Login runs the named provider's browser sign-in, streaming progress
	// lines to the wizard; nil hides OAuth rows' sign-in. It runs on a
	// background goroutine and honors ctx cancellation (esc).
	Login func(ctx context.Context, provider string, progress func(string)) error
}

// HistoryTurn is one stored exchange replayed into the scrollback on
// /resume. Role is "user" or "assistant"; anything else renders as
// assistant text.
type HistoryTurn struct {
	Role string
	Text string
}

// SessionInfo is one row in the /resume picker.
type SessionInfo struct {
	ID     string
	Title  string
	Detail string
}

// AgentInfo is one selectable agent profile.
type AgentInfo struct {
	Name        string
	Description string
}

// SkillInfo is one loaded skill the /skills picker can invoke.
type SkillInfo struct {
	Name        string
	Description string
	Source      string // project or user
	Path        string
}

// CommandInfo is one custom slash command the chat can dispatch.
type CommandInfo struct {
	Name        string
	Description string
	Model       string // route to switch to first; empty keeps the active route
	Body        string // the prompt; $ARGUMENTS expands to the typed arguments
}

// DashAgent is one row on the agent dashboard.
type DashAgent struct {
	ID     string
	Title  string
	Detail string
	Peek   string
	State  string // working | idle | needs-input
}

// TodoItem is one row in the Ctrl+T task list.
type TodoItem struct {
	ID      string
	Content string
	Status  string
}

// RouteInfo names one configured route for the chat /model switcher.
type RouteInfo struct {
	Name     string
	Provider string
	Model    string
}

// Program is what the CLI drives: Start it, hand Observer and Approver to
// the runtime, and call Done once with the result. Start must be called
// exactly once; Observer and Approver block until it runs.
type Program interface {
	// Start renders until Done is called, the user quits, or ctx ends.
	Start(ctx context.Context) error
	// Observer receives runtime events, phases, and model output.
	Observer() runtime.Observer
	// Approver asks the human to decide a pending approval.
	Approver() runtime.ApprovalFunc
	// Asker asks the human a multiple-choice question from ask_user_question.
	Asker() core.AskFunc
	// Done shows the result and the workspace diff (may be empty), then ends Start.
	Done(res runtime.Result, diff string)
}

// New returns a Bubble Tea program when opts.IsTTY, a plain one otherwise.
func New(out io.Writer, in io.Reader, opts Options) Program {
	if opts.IsTTY {
		return newTea(out, in, opts)
	}
	return newPlain(out, in, opts)
}

// EventMsg carries one runtime event.
type EventMsg struct{ E core.Event }

// PhaseMsg announces the phase the runtime entered.
type PhaseMsg core.Phase

// DeltaMsg carries model output text.
type DeltaMsg string

// ApprovalMsg asks the human to decide; Reply must have capacity 1 and
// receives exactly one resolution.
type ApprovalMsg struct {
	A     core.Approval
	Reply chan<- core.ApprovalResolution
}

// DoneMsg carries the run result and the workspace diff; it quits the program.
type DoneMsg struct {
	Result runtime.Result
	Diff   string
}

// denyMsg resolves the pending approval as denied with a note (EOF, bad
// answer, cancelled context).
type denyMsg string
