package cli

// The interactive chat REPL: bare `ink` on a terminal opens a
// multi-turn conversation that persists to the session store and resumes
// across launches. One heavy graph (provider, sandbox, workspace, policy) is
// built per launch and reused across every turn; each turn runs one
// runtime.Run over the prior transcript as history.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ataidesorg/ink/internal/commands"
	"github.com/ataidesorg/ink/internal/config"
	"github.com/ataidesorg/ink/internal/core"
	"github.com/ataidesorg/ink/internal/redact"
	"github.com/ataidesorg/ink/internal/runtime"
	sessionstore "github.com/ataidesorg/ink/internal/session"
	"github.com/ataidesorg/ink/internal/skills"
	"github.com/ataidesorg/ink/internal/tui"
)

const chatUsage = `usage: ink [chat] [flags]

Open the interactive chat REPL (the default when ink is run on a terminal).

flags:
  --project DIR      project root (default: current directory)
  --profile NAME     profile to activate
  --config-dir DIR   user config directory
  --set key=value    override one key; repeatable
  --model NAME       model name (default: the route's model)
  --resume           resume the most recent session (default: start fresh)
  --continue ID      resume a specific session by id
  --yes              pre-approve workspace writes and allow-listed commands
  --worktree NAME    chat on the dedicated git worktree NAME (created on first use)
`

type chatFlags struct {
	globalFlags
	model      string
	resume     bool
	continueID string
	yes        bool
	worktree   string
}

func parseChatFlags(args []string, stderr io.Writer) (chatFlags, bool) {
	var f chatFlags
	fs := flag.NewFlagSet("chat", flag.ContinueOnError)
	fs.SetOutput(stderr)
	f.bind(fs)
	fs.StringVar(&f.model, "model", "", "model name")
	fs.BoolVar(&f.resume, "resume", false, "resume the most recent session")
	fs.StringVar(&f.continueID, "continue", "", "resume a session by id")
	fs.BoolVar(&f.yes, "yes", false, "pre-approve writes and allow-listed commands")
	fs.StringVar(&f.worktree, "worktree", "", "dedicated git worktree name")
	positional, err := parseInterleaved(fs, args)
	if err != nil || len(positional) != 0 {
		fmt.Fprint(stderr, chatUsage)
		return f, false
	}
	return f, true
}

// chatCmd opens the REPL: resolve config, build one provider + graph, pick the
// session to resume or start, then drive the terminal chat until the user quits.
func chatCmd(args []string, stdout, stderr io.Writer, stdin io.Reader, environ []string, getwd func() (string, error)) int {
	f, ok := parseChatFlags(args, stderr)
	if !ok {
		return exitUsage
	}
	if !isTerminal(stdin) || !isTerminal(stdout) {
		fmt.Fprintln(stderr, "ink chat: needs a terminal (run `ink` interactively, or use `ink run` for one-shot tasks)")
		return exitUsage
	}
	if f.resume && f.continueID != "" {
		fmt.Fprintln(stderr, "ink chat: --resume and --continue are mutually exclusive")
		return exitUsage
	}
	opts, err := f.options(environ, getwd, stderr)
	if err != nil {
		return fail(stderr, "chat", exitUsage, err)
	}
	if opts.ProjectRoot, err = filepath.Abs(opts.ProjectRoot); err != nil {
		return fail(stderr, "chat", exitUsage, err)
	}
	inF, _ := stdin.(*os.File)
	outF, _ := stdout.(*os.File)
	if inF != nil && outF != nil {
		opts.Prompt = trustPromptTUI(inF, outF)
	}
	resolved, err := config.Load(opts)
	if err != nil {
		return fail(stderr, "chat", exitConfigInvalid, err)
	}
	warnDropped(stderr, resolved)
	red := redact.New()
	if verr := config.Validate(resolved); verr != nil {
		fmt.Fprintf(stderr, "ink chat: configuration is invalid\n%s\n", red.Redact(verr.Error()))
		return exitConfigInvalid
	}
	cfg := resolved.Config
	if cfg.Sandbox.Provider == "unavailable" {
		fmt.Fprintln(stderr, "ink chat: sandbox.provider is \"unavailable\"; set it to \"process\" to run tasks")
		return exitNotImplemented
	}
	if needsModelSetup(cfg) {
		if _, serr := firstRunSetup(cfg, opts, inF, outF, stderr, environ); serr != nil {
			if errors.Is(serr, errSetupAborted) {
				fmt.Fprintln(stderr, "ink chat: "+serr.Error())
				return exitUsage
			}
			fmt.Fprintf(stderr, "ink chat: %s\n", red.Redact(serr.Error()))
			return exitError
		}
		if resolved, err = config.Load(opts); err != nil {
			return fail(stderr, "chat", exitConfigInvalid, err)
		}
		if verr := config.Validate(resolved); verr != nil {
			fmt.Fprintf(stderr, "ink chat: configuration is invalid\n%s\n", red.Redact(verr.Error()))
			return exitConfigInvalid
		}
		cfg = resolved.Config
	}
	store, err := openStore(environ, red)
	if err != nil {
		return fail(stderr, "chat", exitError, err)
	}
	id, err := pickSession(store, f, stdout)
	if err != nil {
		return fail(stderr, "chat", exitError, err)
	}
	// Prefer this session's saved route override over the config
	// default, so resuming a session lands on the model the user last chose.
	routeName, override := effectiveRoute(cfg, store, id)
	var target *providerTarget
	if override {
		target, err = resolveRoute(cfg, routeName, f.model, red, environ)
	} else {
		target, err = resolveProvider(cfg, f.model, red, environ)
		routeName = cfg.Models.Routing.Default
	}
	if err != nil {
		fmt.Fprintf(stderr, "ink chat: %s\n", red.Redact(err.Error()))
		if errors.Is(err, core.ErrNotImplemented) {
			return exitNotImplemented
		}
		return exitError
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	cs, err := newChatSession(ctx, cfg, target, opts.ProjectRoot, red, environ, store, id, routeName, f.yes, f.worktree)
	if err != nil {
		return fail(stderr, "chat", exitFailed, err)
	}
	defer cs.close(stderr)
	home, _ := config.Home(envLookup(environ))
	keys, err := tui.ParseKeymap(cfg.TUI.Keys)
	if err != nil {
		return fail(stderr, "chat", exitError, err)
	}
	cs.startMCP(ctx, cfg.MCP, stderr)
	themeName := loadSettings(home).Theme
	if themeName == "" {
		themeName = cfg.TUI.Theme
	}
	if err := cs.addSkills(skills.Load(opts.ProjectRoot, home, stderr)); err != nil {
		return fail(stderr, "chat", exitError, err)
	}
	rerr := tui.RunChat(ctx, stdout, stdin,
		cs.tuiOptions(f, opts, home, themeName, keys, stdout, stderr), cs.turn)
	if rerr != nil && ctx.Err() == nil {
		return fail(stderr, "chat", exitFailed, rerr)
	}
	// Drop an unused shell before announcing a save. close() will try again
	// on the way out, which is a no-op once the directory is gone.
	cs.dropIfEmpty(stderr)
	if _, err := store.Meta(cs.id); err == nil {
		fmt.Fprintf(stderr, "session %s saved under %s\n", cs.id, filepath.Join(store.Root(), cs.id))
	}
	return exitOK
}

func ptrPrefs(p tui.Prefs) *tui.Prefs { return &p }

// tuiOptions is the chat TUI's wiring table: every callback the chat model
// needs, bound to this session. Kept off chatCmd so the command reads as a
// sequence of steps rather than one screenful of struct literal.
func (c *chatSession) tuiOptions(f chatFlags, opts config.Options, home, themeName string, keys tui.Keymap, stdout, stderr io.Writer) tui.Options {
	return tui.Options{
		IsTTY:         true,
		NoColor:       envLookup(c.environ)("NO_COLOR") != "",
		Budget:        c.base.Task.Budget,
		Route:         fmt.Sprintf("%s/%s", c.target.decision.Selected.Provider, c.model),
		Worktree:      worktreeLabel(c.base.Workspace),
		Folder:        c.base.Workspace.Root,
		Branch:        vcsBranch(c.base.Project.VCS),
		Dirty:         vcsDirty(c.base.Project.VCS),
		ContextWindow: c.target.provider.Descriptor().Capabilities.MaxContextTokens,
		UsageLimits:   budgetLimitsLabel(c.cfg.Budgets),
		CompleteFiles: fileCompleter(c.base.Workspace.Root),
		Routes:        routeInfos(c.cfg),
		Active:        c.routeName,
		NewSession:    c.rotate,
		Resume:        c.resumeLatest,
		ResumeByID:    c.resumeByID,
		Sessions:      c.sessionInfos(),
		Mode:          sessionMode(c.store, c.id),
		SetMode:       c.setMode,
		SessionID:     c.id,
		Title:         sessionTitle(c.store, c.id),
		Copy: func(s string) error {
			return clipboardCopy(stdout, home, s)
		},
		SaveCopy:      saveCopyFile,
		AlwaysApprove: f.yes,
		VimMode:       loadSettings(home).VimMode,
		Prefs:         ptrPrefs(loadSettings(home).prefs(c.cfg.TUI.HideAdvisories)),
		SetPrefs: func(p tui.Prefs) error {
			st := loadSettings(home)
			return saveSettings(home, applyPrefs(st, p))
		},
		SetVimMode: func(on bool) error {
			st := loadSettings(home)
			st.VimMode = on
			return saveSettings(home, st)
		},
		Rewind:         c.rewind,
		Fork:           c.fork,
		Rename:         c.rename,
		Doctor:         doctorReport(),
		ListAgents:     c.listAgents,
		CreateAgent:    c.createAgent,
		RunOn:          c.turnOn,
		AttachAgent:    c.attachAgent,
		DeleteAgent:    c.deleteAgent,
		DeleteSession:  c.deleteSession,
		Todos:          c.todosForTUI,
		Goal:           c.loadGoal,
		SetGoal:        c.saveGoal,
		ClearGoal:      c.clearGoal,
		Compact:        c.compact,
		SwitchModel:    c.switchRoute,
		Themes:         loadThemes(home, stderr),
		HideAdvisories: loadSettings(home).prefs(c.cfg.TUI.HideAdvisories).HideAdvisories,
		ThemeName:      themeName,
		Keys:           keys,
		SetTheme: func(name string) error {
			st := loadSettings(home) // re-read: keep fields other sessions saved
			st.Theme = name
			return saveSettings(home, st)
		},
		OnApprover: func(fn runtime.ApprovalFunc) {
			if !f.yes {
				c.deps.Approve = fn // interactive y/s/n prompt; --yes keeps pre-approval
			}
		},
		OnAsker: func(fn core.AskFunc) {
			c.deps.Tools = c.deps.Tools.WithAskUser(fn)
			c.baseTools = c.deps.Tools
			c.applyTools()
		},
		Commands:  commandInfos(commands.Load(opts.ProjectRoot, home, stderr, reservedSlash())),
		Skills:    c.skillInfos(),
		Agents:    agentInfos(c.cfg.Agents),
		SetAgent:  c.setAgent,
		Providers: connectProviders(),
		Connect: func(req tui.ConnectRequest) (tui.RouteInfo, error) {
			return c.connect(opts, req)
		},
		ConnectModels: c.connectModels,
		Login: func(ctx context.Context, provider string, progress func(string)) error {
			return c.connectLogin(ctx, opts, provider, progress)
		},
	}
}

// openStore roots the session store at the Ink home.
func openStore(environ []string, red *redact.Redactor) (*sessionstore.Store, error) {
	home, err := config.Home(envLookup(environ))
	if err != nil {
		return nil, err
	}
	return sessionstore.NewStore(home, red)
}

func budgetLimitsLabel(b config.BudgetsConfig) string {
	var parts []string
	if b.PerSessionUSD > 0 {
		parts = append(parts, fmt.Sprintf("session cap $%.2f", b.PerSessionUSD))
	}
	if b.PerDayUSD > 0 {
		parts = append(parts, fmt.Sprintf("day cap $%.2f", b.PerDayUSD))
	}
	return strings.Join(parts, " ")
}

// pickSession resolves which session this launch drives: --continue an
// existing one, --resume the most recent one that has turns, or — the
// default — a fresh session. Resuming is always explicit; a bare launch
// never reopens an old conversation.
func pickSession(store *sessionstore.Store, f chatFlags, stdout io.Writer) (string, error) {
	switch {
	case f.continueID != "":
		if _, _, err := store.Load(f.continueID); err != nil {
			return "", fmt.Errorf("cannot resume session %q: %w", f.continueID, err)
		}
		return f.continueID, nil
	case f.resume:
		metas, err := store.List()
		if err != nil {
			return "", err
		}
		for _, m := range metas {
			// Skip empty sessions: fresh-by-default launches leave them
			// behind, and resuming one would reopen nothing.
			if m.Turns == 0 {
				continue
			}
			fmt.Fprintf(stdout, "resuming session %s (%d turns)\n", m.ID, m.Turns)
			return m.ID, nil
		}
		fmt.Fprintln(stdout, "no previous session to resume — starting fresh")
	}
	return newSessionID(store)
}

func newSessionID(store *sessionstore.Store) (string, error) {
	id := string(core.NewSessionID())
	if _, err := store.Create(id, time.Now()); err != nil {
		return "", err
	}
	return id, nil
}

// effectiveRoute returns the route this launch should use: a session's saved
// override when it still names a defined route, otherwise the config default.
// The bool reports whether the override won, so the caller can keep
// resolveProvider's richer no-routes/no-default diagnostics on the default path.
func effectiveRoute(cfg config.Config, store *sessionstore.Store, id string) (string, bool) {
	m, err := store.Meta(id)
	if err != nil || m.DefaultRoute == "" {
		return cfg.Models.Routing.Default, false
	}
	if _, ok := cfg.Models.Routes[m.DefaultRoute]; !ok {
		return cfg.Models.Routing.Default, false // override points at a route since removed
	}
	return m.DefaultRoute, true
}

// routeInfos lists the configured routes (name-sorted) for the TUI /model
// switcher.
func routeInfos(cfg config.Config) []tui.RouteInfo {
	names := make([]string, 0, len(cfg.Models.Routes))
	for name := range cfg.Models.Routes {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]tui.RouteInfo, 0, len(names))
	for _, name := range names {
		rc := cfg.Models.Routes[name]
		out = append(out, tui.RouteInfo{Name: name, Provider: rc.Provider, Model: rc.Model})
	}
	return out
}

func sessionListTitle(m sessionstore.Meta, turns []sessionstore.Turn) string {
	if strings.TrimSpace(m.Title) != "" {
		return m.Title
	}
	for _, t := range turns {
		if t.Role != core.RoleUser {
			continue
		}
		words := strings.Fields(t.Text)
		if len(words) == 0 {
			break
		}
		title := strings.Join(words, " ")
		r := []rune(title)
		if len(r) > 48 {
			title = string(r[:48]) + "..."
		}
		return title
	}
	id := m.ID
	if len(id) > 8 {
		id = id[:8]
	}
	return "Untitled " + id
}

// chatApprover is the launch default: pre-approve under --yes, deny
// otherwise. RunChat swaps in the interactive y/s/n approver via OnApprover
// before the first turn, so the deny arm only answers if the TUI never
// starts.
func chatApprover(yes bool) runtime.ApprovalFunc {
	if yes {
		return yesApprover(nil)
	}
	by := core.Principal{Kind: core.PrincipalUser, Name: "chat"}
	return func(_ context.Context, _ core.Approval) (core.ApprovalResolution, error) {
		return core.ApprovalResolution{Decision: core.ApprovalDenied, By: by, At: time.Now(), Scope: core.ApprovalOnce,
			Note: "chat runs read-only; restart with `ink --yes` to allow writes and allow-listed commands"}, nil
	}
}

// sessionsCmd lists persisted sessions, newest first.
func sessionsCmd(args []string, stdout, stderr io.Writer, environ []string) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "usage: ink sessions")
		return exitUsage
	}
	store, err := openStore(environ, redact.New())
	if err != nil {
		return fail(stderr, "sessions", exitError, err)
	}
	metas, err := store.List()
	if err != nil {
		return fail(stderr, "sessions", exitError, err)
	}
	if len(metas) == 0 {
		fmt.Fprintln(stdout, "no sessions yet")
		return exitOK
	}
	tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tTURNS\tUPDATED\tTITLE")
	for _, m := range metas {
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\n", m.ID, m.Turns, m.Updated.Format(time.RFC3339), m.Title)
	}
	_ = tw.Flush()
	return exitOK
}

func reservedSlash() map[string]bool {
	names := tui.SlashNames()
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[n] = true
	}
	return out
}

// commandInfos projects loaded commands into the TUI's option shape.
func commandInfos(list []commands.Command) []tui.CommandInfo {
	out := make([]tui.CommandInfo, len(list))
	for i, c := range list {
		out[i] = tui.CommandInfo{Name: c.Name, Description: c.Description, Model: c.Model, Body: c.Body}
	}
	return out
}

func skillSource(path string) string {
	n := filepath.ToSlash(path)
	switch {
	case strings.Contains(n, "/.ink/skills/"):
		return "user"
	case path == "":
		return ""
	default:
		return "project"
	}
}

// agentInfos projects configured agents into the TUI's option shape, sorted.
func agentInfos(m map[string]config.AgentConfig) []tui.AgentInfo {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]tui.AgentInfo, len(names))
	for i, n := range names {
		out[i] = tui.AgentInfo{Name: n, Description: m[n].Description}
	}
	return out
}

func sessionTitle(store *sessionstore.Store, id string) string {
	if store == nil {
		return ""
	}
	m, err := store.Meta(id)
	if err != nil {
		return ""
	}
	return m.Title
}

func sessionMode(store *sessionstore.Store, id string) string {
	if store == nil {
		return "code"
	}
	m, err := store.Meta(id)
	if err != nil || m.Mode == "" {
		return "code"
	}
	return m.Mode
}
