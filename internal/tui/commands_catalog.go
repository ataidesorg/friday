package tui

// One catalog feeds `/` typeahead, Ctrl+P, and the command dispatcher names.
// Palette-only rows (cycle-mode) have an empty slash name.

const (
	gSession = "Session"
	gCtx     = "Context"
	gModel   = "Model & Input"
	gDisplay = "Display"
	gTools   = "Extensions"
	gCmds    = "Commands"
	gOther   = "Other"
)

type catalogEntry struct {
	id, slash, title, key, detail, group string
}

func commandCatalog() []catalogEntry {
	return []catalogEntry{
		{"new", "new", "New Session", "ctrl+n", "start a fresh conversation", gSession},
		{"home", "home", "Home", "/home", "return to the welcome surface", gSession},
		{"dashboard", "dashboard", "Agent Dashboard", "ctrl+\\", "roster of live sessions", gSession},
		{"resume", "resume", "Resume Session", "/resume", "reopen a prior conversation", gSession},
		{"rename", "rename", "Rename Session", "/rename", "set the session title", gSession},
		{"fork", "fork", "Fork Session", "/fork", "branch a new session from this transcript", gSession},
		{"delete", "delete", "Delete Session", "/delete", "requires typed confirmation", gSession},
		{"status", "status", "Session Info", "/status", "id, route, tokens", gSession},
		{"goal", "goal", "Session Goal", "/goal", "start, pause, resume, edit, or clear", gSession},
		{"rewind", "rewind", "Rewind", "/rewind", "drop later turns from the transcript", gSession},
		{"clear", "clear", "Clear Scrollback", "/clear", "empty the view; the session keeps history", gSession},
		{"compact", "compact", "Compact History", "/compact", "fold older turns into a summary", gCtx},
		{"queue", "queue", "Prompt Queue", "ctrl+b", "queued prompts waiting to run", gCtx},
		{"usage", "usage", "Usage Meter", "/usage", "toggle context and spend in the composer", gCtx},
		{"cost", "cost", "Session Cost", "/cost", "print session tokens and cost", gCtx},
		{"model", "model", "Switch Model", "/model", "pick the active route", gModel},
		{"agent", "agent", "Manage Agents", "/agent", "switch agent profiles", gModel},
		{"cycle-mode", "", "Switch Mode", "shift+tab", "normal, plan, auto, always-approve, always-ask", gModel},
		{"plan", "plan", "Plan Mode", "/plan", "read-only planning", gModel},
		{"always-approve", "always-approve", "Always Approve", "/always-approve", "auto-approve remaining asks this session", gModel},
		{"multiline", "multiline", "Multiline Input", "/multiline", "enter inserts a newline", gModel},
		{"vim-mode", "vim-mode", "Vim Mode", "/vim-mode", "j/k in the scrollback", gModel},
		{"verbose", "verbose", "Verbose Trace", "/verbose", "full event trace", gDisplay},
		{"tools-display", "tools", "Tool Activity", "/tools", "show tool calls in chat", gDisplay},
		{"thinking", "thinking", "Thinking Indicator", "/thinking", "show live thinking line", gDisplay},
		{"timestamps", "timestamps", "Timestamps", "/timestamps", "show per-line times", gDisplay},
		{"advisories", "advisories", "Advisories", "/advisories", "unpriced-model and unverified-result warnings", gDisplay},
		{"theme", "theme", "Switch Theme", "/theme", "friday, dark, light, ansi", gDisplay},
		{"skills", "skills", "Skills", "/skills", "inspect and invoke loaded skills", gTools},
		{"commands", "commands", "Custom Commands", "/commands", "list saved slash commands", gTools},
		{"connect", "connect", "Connect Provider", "/connect", "add an API key", gTools},
		{"edit-prompt", "edit-prompt", "Edit Prompt", "ctrl+g", "open $VISUAL", gOther},
		{"copy", "copy", "Copy Last Reply", "/copy", "clipboard the last reply", gOther},
		{"export", "export", "Export Transcript", "/export", "copy the whole transcript", gOther},
		{"history", "history", "Prompt History", "/history", "recall a sent prompt", gOther},
		{"doctor", "doctor", "Doctor", "/doctor", "terminal and clipboard checks", gOther},
		{"help", "help", "Help", "/help", "commands and keys", gOther},
		{"quit", "quit", "Quit", "ctrl+c", "exit friday", gOther},
		{"exit", "exit", "Exit", "/exit", "exit friday", gOther},
	}
}

func builtinSlash() []slashEntry {
	var out []slashEntry
	for _, c := range commandCatalog() {
		if c.slash == "" {
			continue
		}
		out = append(out, slashEntry{c.slash, c.detail})
	}
	return out
}

func chatActions() []overlayItem {
	var out []overlayItem
	for _, c := range commandCatalog() {
		out = append(out, itemD(c.group, c.id, c.title, c.key, c.detail))
	}
	return out
}
