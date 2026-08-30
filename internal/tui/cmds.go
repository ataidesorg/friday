package tui

import (
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
)

// Palette / typeahead groups. Custom commands land in gCmds at runtime.
const (
	gSession = "Session"
	gCtx     = "Context"
	gModel   = "Model & Input"
	gDisplay = "Display"
	gTools   = "Extensions"
	gCmds    = "Commands"
	gOther   = "Other"
)

// chatCmd is one built-in. This table is the source of truth for `/`
// typeahead, Ctrl+P, and dispatch. An empty Slash means palette-only.
type chatCmd struct {
	ID     string
	Slash  string
	Title  string
	Key    string
	Detail string
	Group  string
	Run    func(m ChatModel, arg string) (tea.Model, tea.Cmd)
	State  func(m ChatModel) string
}

func noArg(fn func(ChatModel) (tea.Model, tea.Cmd)) func(ChatModel, string) (tea.Model, tea.Cmd) {
	return func(m ChatModel, _ string) (tea.Model, tea.Cmd) { return fn(m) }
}

func builtins() []chatCmd {
	return []chatCmd{
		{
			ID: "new", Slash: "new", Title: "New Session", Key: "ctrl+n",
			Detail: "start a fresh conversation", Group: gSession,
			Run: noArg(ChatModel.newSession),
		},
		{
			ID: "home", Slash: "home", Title: "Home", Key: "/home",
			Detail: "return to the welcome surface", Group: gSession,
			Run: noArg(ChatModel.goHome),
		},
		{
			ID: "dashboard", Slash: "dashboard", Title: "Agent Dashboard", Key: "ctrl+\\",
			Detail: "roster of live sessions", Group: gSession,
			Run: noArg(ChatModel.toggleDashboard),
		},
		{
			ID: "resume", Slash: "resume", Title: "Resume Session", Key: "/resume",
			Detail: "reopen a prior conversation", Group: gSession,
			Run: noArg(ChatModel.resumeSession),
		},
		{
			ID: "rename", Slash: "rename", Title: "Rename Session", Key: "/rename",
			Detail: "set the session title", Group: gSession,
			Run: ChatModel.applyRename,
		},
		{
			ID: "fork", Slash: "fork", Title: "Fork Session", Key: "/fork",
			Detail: "branch a new session from this transcript", Group: gSession,
			Run: noArg(ChatModel.applyFork),
		},
		{
			ID: "delete", Slash: "delete", Title: "Delete Session", Key: "/delete",
			Detail: "requires typed confirmation", Group: gSession,
			Run: ChatModel.deleteCurrent,
		},
		{
			ID: "status", Slash: "status", Title: "Session Info", Key: "/status",
			Detail: "id, route, tokens", Group: gSession,
			Run: noArg(ChatModel.showStatus),
		},
		{
			ID: "goal", Slash: "goal", Title: "Session Goal", Key: "/goal",
			Detail: "start, pause, resume, edit, or clear", Group: gSession,
			Run: ChatModel.applyGoal,
		},
		{
			ID: "rewind", Slash: "rewind", Title: "Rewind", Key: "/rewind",
			Detail: "drop later turns from the transcript", Group: gSession,
			Run: noArg(ChatModel.openRewind),
		},
		{
			ID: "clear", Slash: "clear", Title: "Clear Scrollback", Key: "/clear",
			Detail: "empty the view; the session keeps history", Group: gSession,
			Run: noArg(ChatModel.clearView),
		},
		{
			ID: "compact", Slash: "compact", Title: "Compact History", Key: "/compact",
			Detail: "fold older turns into a summary", Group: gCtx,
			Run: noArg(ChatModel.startCompact),
		},
		{
			ID: "queue", Slash: "queue", Title: "Prompt Queue", Key: "ctrl+b",
			Detail: "queued prompts waiting to run", Group: gCtx,
			Run: noArg(ChatModel.toggleQueue),
		},
		{
			ID: "usage", Slash: "usage", Title: "Usage Meter", Key: "/usage",
			Detail: "toggle context and spend in the composer", Group: gCtx,
			Run:   noArg(ChatModel.toggleUsageMeter),
			State: func(m ChatModel) string { return onOff(m.usageOpen) },
		},
		{
			ID: "cost", Slash: "cost", Title: "Session Cost", Key: "/cost",
			Detail: "print session tokens and cost", Group: gCtx,
			Run: noArg(ChatModel.showCost),
		},
		{
			ID: "model", Slash: "model", Title: "Switch Model", Key: "/model",
			Detail: "pick the active route", Group: gModel,
			Run: ChatModel.cmdModel,
		},
		{
			ID: "agent", Slash: "agent", Title: "Manage Agents", Key: "/agent",
			Detail: "switch agent profiles", Group: gModel,
			Run: ChatModel.cmdAgent,
		},
		{
			ID: "cycle-mode", Title: "Switch Mode", Key: "shift+tab",
			Detail: "normal, plan, auto, always-approve, always-ask", Group: gModel,
			Run:   noArg(ChatModel.cycleMode),
			State: func(m ChatModel) string { return m.permLabel() },
		},
		{
			ID: "plan", Slash: "plan", Title: "Plan Mode", Key: "/plan",
			Detail: "read-only planning", Group: gModel,
			Run:   noArg(ChatModel.cmdPlan),
			State: func(m ChatModel) string { return onOff(m.mode == "plan") },
		},
		{
			ID: "always-approve", Slash: "always-approve", Title: "Always Approve", Key: "/always-approve",
			Detail: "auto-approve remaining asks this session", Group: gModel,
			Run:   noArg(ChatModel.toggleYolo),
			State: func(m ChatModel) string { return onOff(m.yolo) },
		},
		{
			ID: "multiline", Slash: "multiline", Title: "Multiline Input", Key: "/multiline",
			Detail: "enter inserts a newline", Group: gModel,
			Run:   noArg(ChatModel.toggleMultiline),
			State: func(m ChatModel) string { return onOff(m.multiline) },
		},
		{
			ID: "vim-mode", Slash: "vim-mode", Title: "Vim Mode", Key: "/vim-mode",
			Detail: "j/k in the scrollback", Group: gModel,
			Run:   noArg(ChatModel.toggleVim),
			State: func(m ChatModel) string { return onOff(m.vim) },
		},
		{
			ID: "verbose", Slash: "verbose", Title: "Verbose Trace", Key: "/verbose",
			Detail: "full event trace", Group: gDisplay,
			Run:   noArg(ChatModel.toggleVerbose),
			State: func(m ChatModel) string { return onOff(m.verbose) },
		},
		{
			ID: "tools-display", Slash: "tools", Title: "Tool Activity", Key: "/tools",
			Detail: "show tool calls in chat", Group: gDisplay,
			Run:   noArg(ChatModel.toggleToolActivity),
			State: func(m ChatModel) string { return onOff(m.showTools) },
		},
		{
			ID: "thinking", Slash: "thinking", Title: "Thinking Indicator", Key: "/thinking",
			Detail: "show live thinking line", Group: gDisplay,
			Run:   noArg(ChatModel.toggleThinkingLine),
			State: func(m ChatModel) string { return onOff(m.showThinking) },
		},
		{
			ID: "timestamps", Slash: "timestamps", Title: "Timestamps", Key: "/timestamps",
			Detail: "show per-line times", Group: gDisplay,
			Run:   noArg(ChatModel.toggleTimestamps),
			State: func(m ChatModel) string { return onOff(m.showTimes) },
		},
		{
			ID: "advisories", Slash: "advisories", Title: "Advisories", Key: "/advisories",
			Detail: "unpriced-model and unverified-result warnings", Group: gDisplay,
			Run:   noArg(ChatModel.toggleAdvisories),
			State: func(m ChatModel) string { return onOff(!m.hideAdvis) },
		},
		{
			ID: "theme", Slash: "theme", Title: "Switch Theme", Key: "/theme",
			Detail: "ink, dark, light, ansi", Group: gDisplay,
			Run: ChatModel.cmdTheme,
		},
		{
			ID: "skills", Slash: "skills", Title: "Skills", Key: "/skills",
			Detail: "inspect and invoke loaded skills", Group: gTools,
			Run: noArg(ChatModel.openSkills),
		},
		{
			ID: "commands", Slash: "commands", Title: "Custom Commands", Key: "/commands",
			Detail: "list saved slash commands", Group: gTools,
			Run: noArg(ChatModel.listCustomCommands),
		},
		{
			ID: "connect", Slash: "connect", Title: "Connect Provider", Key: "/connect",
			Detail: "add an API key", Group: gTools,
			Run: noArg(ChatModel.openConnect),
		},
		{
			ID: "edit-prompt", Slash: "edit-prompt", Title: "Edit Prompt", Key: "ctrl+g",
			Detail: "open $VISUAL", Group: gOther,
			Run: noArg(ChatModel.openEditor),
		},
		{
			ID: "copy", Slash: "copy", Title: "Copy Last Reply", Key: "/copy",
			Detail: "clipboard the last reply", Group: gOther,
			Run: ChatModel.copyCommand,
		},
		{
			ID: "export", Slash: "export", Title: "Export Transcript", Key: "/export",
			Detail: "copy the whole transcript", Group: gOther,
			Run: noArg(ChatModel.exportTranscript),
		},
		{
			ID: "history", Slash: "history", Title: "Prompt History", Key: "/history",
			Detail: "recall a sent prompt", Group: gOther,
			Run: noArg(ChatModel.openHistory),
		},
		{
			ID: "doctor", Slash: "doctor", Title: "Doctor", Key: "/doctor",
			Detail: "terminal and clipboard checks", Group: gOther,
			Run: noArg(ChatModel.showDoctor),
		},
		{
			ID: "help", Slash: "help", Title: "Help", Key: "/help",
			Detail: "commands and keys", Group: gOther,
			Run: noArg(ChatModel.showHelp),
		},
		{
			ID: "quit", Slash: "quit", Title: "Quit", Key: "ctrl+c",
			Detail: "exit ink", Group: gOther,
			Run: noArg(ChatModel.quitChat),
		},
		{
			ID: "exit", Slash: "exit", Title: "Exit", Key: "/exit",
			Detail: "exit ink", Group: gOther,
			Run: noArg(ChatModel.quitChat),
		},
	}
}

var cmdIndex struct {
	once   sync.Once
	all    []chatCmd
	byID   map[string]chatCmd
	byName map[string]chatCmd
}

func loadCmds() {
	cmdIndex.once.Do(func() {
		cmdIndex.all = builtins()
		cmdIndex.byID = make(map[string]chatCmd, len(cmdIndex.all))
		cmdIndex.byName = make(map[string]chatCmd, len(cmdIndex.all))
		for _, c := range cmdIndex.all {
			cmdIndex.byID[c.ID] = c
			if c.Slash != "" {
				cmdIndex.byName[c.Slash] = c
			}
		}
	})
}

// SlashNames returns every built-in /command name, in catalog order. The CLI
// passes this to commands.Load so a custom file cannot shadow a built-in.
func SlashNames() []string {
	loadCmds()
	out := make([]string, 0, len(cmdIndex.all))
	for _, c := range cmdIndex.all {
		if c.Slash != "" {
			out = append(out, c.Slash)
		}
	}
	return out
}

func builtinSlash() []slashEntry {
	loadCmds()
	var out []slashEntry
	for _, c := range cmdIndex.all {
		if c.Slash == "" {
			continue
		}
		out = append(out, slashEntry{name: c.Slash, detail: c.Detail, group: c.Group, title: c.Title})
	}
	return out
}

func chatActions() []overlayItem {
	loadCmds()
	out := make([]overlayItem, 0, len(cmdIndex.all))
	for _, c := range cmdIndex.all {
		out = append(out, itemD(c.Group, c.ID, c.Title, c.Key, c.Detail))
	}
	return out
}

func lookupSlash(name string) (chatCmd, bool) {
	loadCmds()
	c, ok := cmdIndex.byName[name]
	return c, ok
}

func lookupID(id string) (chatCmd, bool) {
	loadCmds()
	c, ok := cmdIndex.byID[id]
	return c, ok
}

func firstField(arg string) string {
	f := strings.Fields(arg)
	if len(f) == 0 {
		return ""
	}
	return f[0]
}
