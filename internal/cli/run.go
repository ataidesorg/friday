package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ataidesorg/friday/internal/config"
	"github.com/ataidesorg/friday/internal/core"
	"github.com/ataidesorg/friday/internal/lsp"
	"github.com/ataidesorg/friday/internal/models"
	"github.com/ataidesorg/friday/internal/models/mock"
	"github.com/ataidesorg/friday/internal/observability"
	"github.com/ataidesorg/friday/internal/policy"
	"github.com/ataidesorg/friday/internal/redact"
	"github.com/ataidesorg/friday/internal/routing"
	"github.com/ataidesorg/friday/internal/runtime"
	"github.com/ataidesorg/friday/internal/sandbox"
	"github.com/ataidesorg/friday/internal/sandbox/container"
	"github.com/ataidesorg/friday/internal/sandbox/process"
	"github.com/ataidesorg/friday/internal/skills"
	"github.com/ataidesorg/friday/internal/tools"
	"github.com/ataidesorg/friday/internal/tui"
	"github.com/ataidesorg/friday/internal/workspace"
)

const runUsage = `usage: friday run [flags] "task text"

flags:
  --project DIR      project root (default: current directory)
  --profile NAME     profile to activate
  --config-dir DIR   user config directory
  --set key=value    override one key; repeatable
  --script FILE      scripted mock provider instead of the configured routes
  --model NAME       model name (default: the route's model, or the script's)
  --no-tui           plain line output instead of the terminal interface
  --yes              pre-approve workspace writes and allow-listed commands for this run
  --keep-workspace   keep the ephemeral workspace and sandbox files for inspection
  --worktree NAME    run on the dedicated git worktree NAME (created on first use)
  --graph FILE       run a task graph (JSON) as parallel worktrees, then merge
  --goal TEXT        start a session goal; prose "done" does not complete it
  --tokens N         optional goal token budget (100k, 1.5m, or an integer)
`

// teardownTimeout bounds workspace cleanup and the diff after a run.
const teardownTimeout = 30 * time.Second

type runFlags struct {
	globalFlags
	script, model, worktree, graph, goal, tokens string
	noTUI, yes, keep                             bool
}

// sandboxProviders is every backend the CLI can build; config names them.
var sandboxProviders = sandbox.Registry{process.Name: process.Factory, container.Name: container.Factory}

func parseRunFlags(args []string, stderr io.Writer) (runFlags, string, bool) {
	var f runFlags
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	f.bind(fs)
	fs.StringVar(&f.script, "script", "", "scripted mock provider file")
	fs.StringVar(&f.model, "model", "", "model name")
	fs.BoolVar(&f.noTUI, "no-tui", false, "plain line output")
	fs.BoolVar(&f.yes, "yes", false, "pre-approve writes and allow-listed commands")
	fs.BoolVar(&f.keep, "keep-workspace", false, "keep the ephemeral workspace")
	fs.StringVar(&f.worktree, "worktree", "", "dedicated git worktree name")
	fs.StringVar(&f.graph, "graph", "", "task graph JSON file")
	fs.StringVar(&f.goal, "goal", "", "session goal objective")
	fs.StringVar(&f.tokens, "tokens", "", "goal token budget")
	positional, err := parseInterleaved(fs, args)
	if err != nil || len(positional) != 1 {
		fmt.Fprint(stderr, runUsage)
		return f, "", false
	}
	return f, positional[0], true
}

// runCmd runs one task end to end: config → policy → sandbox → runtime → TUI.
func runCmd(args []string, stdout, stderr io.Writer, stdin io.Reader, environ []string, getwd func() (string, error)) int {
	f, text, ok := parseRunFlags(args, stderr)
	if !ok {
		return exitUsage
	}
	opts, err := f.options(environ, getwd, stderr)
	if err != nil {
		return fail(stderr, "run", exitUsage, err)
	}
	if opts.ProjectRoot, err = filepath.Abs(opts.ProjectRoot); err != nil {
		return fail(stderr, "run", exitUsage, err)
	}
	interactive := isTerminal(stdin) && isTerminal(stdout)
	if interactive {
		opts.Prompt = trustPrompt(stdin, stdout)
	}
	resolved, err := config.Load(opts)
	if err != nil {
		return fail(stderr, "run", exitConfigInvalid, err)
	}
	warnDropped(stderr, resolved)
	red := redact.New()
	if verr := config.Validate(resolved); verr != nil {
		fmt.Fprintf(stderr, "friday run: configuration is invalid\n%s\n", red.Redact(verr.Error()))
		return exitConfigInvalid
	}
	cfg := resolved.Config
	var (
		provider core.ModelProvider
		model    string
		target   *providerTarget
	)
	if f.script != "" {
		script, err := mock.LoadScript(f.script)
		if err != nil {
			return fail(stderr, "run", exitFailed, err)
		}
		provider = mock.New(script)
		if model = f.model; model == "" {
			model = script.Model
		}
	} else {
		t, err := resolveProvider(cfg, f.model, red, environ)
		if err != nil {
			fmt.Fprintf(stderr, "friday run: %s\n", red.Redact(err.Error()))
			if errors.Is(err, core.ErrNotImplemented) {
				return exitNotImplemented
			}
			return exitError
		}
		target = t
		provider, model = t.provider, t.model
	}
	if cfg.Sandbox.Provider == "unavailable" {
		fmt.Fprintln(stderr, "friday run: sandbox.provider is \"unavailable\"; set it to \"process\" to run tasks")
		return exitNotImplemented
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if f.graph != "" {
		return runGraphCmd(ctx, cfg, f, text, provider, model, target, opts.ProjectRoot, red, environ, stdout, stderr, stdin)
	}
	s, err := newSession(ctx, cfg, f, text, provider, model, target, opts.ProjectRoot, red, environ)
	if err != nil {
		return fail(stderr, "run", exitFailed, err)
	}
	defer s.close(stderr)
	p := tui.New(stdout, stdin, tui.Options{IsTTY: interactive && !f.noTUI, NoColor: envLookup(environ)("NO_COLOR") != "", Budget: s.input.Task.Budget})
	s.deps.Observer = p.Observer()
	if target != nil {
		target.relay.obs = p.Observer()
	}
	s.deps.Approve = p.Approver()
	if f.yes {
		s.deps.Approve = yesApprover(p.Approver())
	}
	if interactive && !f.noTUI {
		s.deps.Tools = s.deps.Tools.WithAskUser(p.Asker())
	}
	return s.run(ctx, p, stderr)
}

// session is everything one run owns; close releases it in reverse order.
type session struct {
	deps    runtime.Deps
	input   runtime.Input
	sink    *observability.LazyTrail
	cleanup workspace.Cleanup
	keep    bool
	target  *providerTarget // nil on the --script path
	lsp     *lsp.Manager    // nil unless a language server is enabled
}

func newSession(ctx context.Context, cfg config.Config, f runFlags, text string, provider core.ModelProvider, model string, target *providerTarget, root string, red *redact.Redactor, environ []string) (*session, error) {
	deps, in, err := buildGraph(cfg, provider, model, red, f.keep)
	if err != nil {
		return nil, err
	}
	if target != nil {
		deps.Route = &target.decision
		deps.Streamed = true
	}
	if home, herr := config.FridayHome(envLookup(environ)); herr == nil {
		if sk := skills.Load(root, home, os.Stderr); len(sk) > 0 {
			if reg, werr := deps.Tools.With(skills.Tool(sk)); werr == nil {
				deps.Tools = reg
				infos := make([]runtime.SkillInfo, len(sk))
				for i, s := range sk {
					infos[i] = runtime.SkillInfo{Name: s.Name, Description: s.Description}
				}
				in.Skills = infos
			} else {
				fmt.Fprintf(os.Stderr, "warning: skills disabled: %v\n", werr)
			}
		}
	}
	spendPath, err := config.StateFilePath(envLookup(environ), "spend.jsonl")
	if err != nil {
		return nil, fmt.Errorf("spend ledger: %w", err)
	}
	if deps.Spend, err = runtime.NewSpend(cfg.Budgets.PerSessionUSD, cfg.Budgets.PerDayUSD, spendPath); err != nil {
		return nil, err
	}
	task, err := core.NewTask(text, core.HarnessCode, core.NewProfileID(), core.NewSessionID(), core.Principal{Kind: core.PrincipalUser, Name: userName(environ)})
	if err != nil {
		return nil, err
	}
	task.Budget = in.Task.Budget
	in.Task = task
	in.Project.ID, in.Project.Root = core.NewProjectID(), root
	rulesHome, _ := config.FridayHome(envLookup(environ))
	in.Project.InstructionFiles, in.Project.GlobalInstructionFiles = discoverRules(root, rulesHome, in.Project.InstructionFiles)
	if vcs, err := workspace.Status(ctx, root); err == nil {
		in.Project.VCS = &vcs
	}
	wsOpts, err := worktreeOpts(workspace.Options{Root: root, Project: in.Project.ID}, environ, f.worktree)
	if err != nil {
		return nil, err
	}
	ws, cleanup, err := workspace.Prepare(ctx, wsOpts)
	if err != nil {
		return nil, fmt.Errorf("workspace: %w", err)
	}
	in.Workspace, in.Spec.WorkDir = ws, ws.Root
	parts, warns := imageAttachments(ws.Root, text)
	for _, w := range warns {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
	in.Images = parts
	lspMgr := lspManager(cfg.LSP, ws.Root)
	if lspMgr != nil {
		deps.Tools = tools.WrapDiagnostics(deps.Tools, lspMgr)
	}
	sink := observability.NewLazyTrail(root, red, core.PrivacyMode(cfg.Telemetry.Privacy))
	deps.Sink = sink
	s := &session{deps: deps, input: in, sink: sink, cleanup: cleanup, keep: f.keep, target: target, lsp: lspMgr}
	if f.goal != "" || f.tokens != "" {
		if f.goal == "" {
			return nil, fmt.Errorf("%w: --tokens requires --goal", core.ErrInvalidInput)
		}
		g, err := core.NewGoal(f.goal, time.Now())
		if err != nil {
			return nil, err
		}
		if f.tokens != "" {
			n, err := core.ParseTokenBudget(f.tokens)
			if err != nil {
				return nil, err
			}
			g, err = g.WithTokenBudget(n, time.Now())
			if err != nil {
				return nil, err
			}
		}
		s.input.Goal = &g
		s.input.SaveGoal = func(next core.Goal) error {
			cp := next
			s.input.Goal = &cp
			return nil
		}
	}
	return s, nil
}

// buildGraph wires what config decides and no single run owns: policy,
// tools, sandbox provider, the scripted model, budget, posture, and the
// project's instruction files and commands. Callers add the task, the
// workspace, the sandbox work dir, and the event sink.
func buildGraph(cfg config.Config, provider core.ModelProvider, model string, red *redact.Redactor, keep bool) (runtime.Deps, runtime.Input, error) {
	agent := cfg.Profiles[cfg.Profile.Active].ToAgent(cfg.Profile.Active)
	posture := agent.Posture
	if posture == "" {
		posture = core.PostureStrict
	}
	toolsCfg, err := withProjectCommands(cfg.Tools, cfg.Project.Commands)
	if err != nil {
		return runtime.Deps{}, runtime.Input{}, err
	}
	eng, err := policy.FromConfig(toolsCfg, posture, slices.Sorted(maps.Keys(cfg.MCP)))
	if err != nil {
		return runtime.Deps{}, runtime.Input{}, fmt.Errorf("policy: %w", err)
	}
	testCmd, err := testCommand(cfg.Project.Commands)
	if err != nil {
		return runtime.Deps{}, runtime.Input{}, err
	}
	budget, err := budgetFrom(cfg.Budgets)
	if err != nil {
		return runtime.Deps{}, runtime.Input{}, err
	}
	prov, err := sandboxProviders.New(cfg.Sandbox.Provider, sandbox.Options{Redactor: red, KeepWorkspace: keep})
	if err != nil {
		return runtime.Deps{}, runtime.Input{}, fmt.Errorf("sandbox: %w", err)
	}
	prices, err := routing.Prices(cfg.Models.Pricing)
	if err != nil {
		return runtime.Deps{}, runtime.Input{}, err
	}
	reg := tools.Default(nil, eng.AllowedCommands())
	if len(cfg.Tools.Custom) > 0 {
		if reg, err = reg.With(customTools(cfg.Tools.Custom)...); err != nil {
			return runtime.Deps{}, runtime.Input{}, fmt.Errorf("custom tools: %w", err)
		}
	}
	reg = tools.WrapFormatters(reg, formatRules(cfg.Format))
	deps := runtime.Deps{
		Provider:  provider,
		Tools:     reg,
		Policy:    eng,
		Approvals: policy.NewApprovals(),
		Sandbox:   prov,
		Price: func(_, model string, u core.Usage) *core.USDMicros {
			p, ok := prices[model]
			if !ok {
				return nil
			}
			c := p.Cost(u)
			return &c
		},
	}
	in := runtime.Input{
		Task:    core.Task{Budget: budget},
		Project: core.Project{Name: cfg.Project.Name, InstructionFiles: cfg.Project.Instructions, Commands: cfg.Project.Commands},
		Model:   model,
		Spec:    sandboxSpec(cfg.Sandbox, ""),
		Posture: posture,
		TestCmd: testCmd,
		Agent:   agent,
	}
	return deps, in, nil
}

// run drives the runtime in the background while the interface owns the
// foreground; the interface quitting early cancels the run.
func (s *session) run(ctx context.Context, p tui.Program, stderr io.Writer) int {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var res runtime.Result
	var runErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		res, runErr = runtime.Run(ctx, s.deps, s.input)
		p.Done(res, s.diff())
	}()
	if err := p.Start(ctx); err != nil && ctx.Err() == nil {
		fmt.Fprintf(stderr, "friday run: interface: %v\n", err)
	}
	cancel()
	<-done
	if runErr != nil {
		return fail(stderr, "run", exitFailed, runErr)
	}
	next := s.flushWarnings(res, stderr)
	s.diagnose(res, next, stderr)
	if res.Goal != nil {
		fmt.Fprintf(stderr, "goal %s", res.Goal.Status)
		if res.Goal.PauseCause != "" {
			fmt.Fprintf(stderr, " (%s)", res.Goal.PauseCause)
		}
		if res.Goal.EvidenceKind != "" {
			fmt.Fprintf(stderr, " %s", res.Goal.EvidenceKind)
		}
		fmt.Fprintln(stderr)
	}
	fmt.Fprintf(stderr, "run %s: %s\n", res.Run.ID, s.trail(res.Run.ID))
	return exitFor(res.Outcome)
}

// flushWarnings appends provider-layer warnings buffered mid-run (401
// retry) to the trail as Warning events, sequenced after the runtime's own
// events. Returns the next free sequence offset. A run that failed before
// producing a Result keeps its warnings on stderr only.
func (s *session) flushWarnings(res runtime.Result, stderr io.Writer) uint64 {
	next := uint64(1)
	if s.target == nil || s.target.warns == nil {
		return next
	}
	for _, msg := range s.target.warns.drain() {
		s.emitWarning(res, next, msg, stderr)
		next++
	}
	return next
}

// diagnose probes the selected provider after a provider_error failure so
// the trail records health evidence next to the failure. next is the first
// free sequence offset after the runtime's events and any flushed warnings.
func (s *session) diagnose(res runtime.Result, next uint64, stderr io.Writer) {
	if s.target == nil || res.Outcome.Category != core.FailureProviderError {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), models.ProbeTimeout+time.Second)
	defer cancel()
	h := s.target.probe(ctx)
	msg := fmt.Sprintf("provider %s health: %s", s.target.decision.Selected.Provider, h.State)
	if h.Reason != "" {
		msg += " (" + h.Reason + ")"
	}
	fmt.Fprintln(stderr, msg)
	s.emitWarning(res, next, msg, stderr)
}

// emitWarning records one Warning trail event at sequence Events+offset.
func (s *session) emitWarning(res runtime.Result, offset uint64, msg string, stderr io.Writer) {
	ctx, cancel := context.WithTimeout(context.Background(), teardownTimeout)
	defer cancel()
	ev := core.NewEvent(res.Run.Task, res.Run.ID, uint64(res.Events)+offset, time.Now(), core.Warning{Message: msg}) //nolint:gosec // Events is a small non-negative count
	if err := s.sink.Emit(ctx, ev); err != nil {
		fmt.Fprintf(stderr, "warning: trail: %v\n", err)
	}
}

// diff lists the changed files; git-less workspaces say so instead of lying.
func (s *session) diff() string {
	ctx, cancel := context.WithTimeout(context.Background(), teardownTimeout)
	defer cancel()
	files, err := workspace.ChangedFiles(ctx, s.input.Workspace)
	switch {
	case errors.Is(err, core.ErrUnavailable):
		return "no diff available: workspace is not a git checkout (" + s.input.Workspace.Root + ")"
	case err != nil:
		return "no diff available: " + err.Error()
	case len(files) == 0:
		return ""
	}
	return "changed files:\n  " + strings.Join(files, "\n  ")
}

func (s *session) trail(id core.RunID) string {
	if path := s.sink.Path(); path != "" {
		return "trail " + path + " (replay with `friday trace " + string(id) + "`)"
	}
	return "no events recorded"
}

func (s *session) close(stderr io.Writer) {
	s.lsp.Close()    // nil-safe
	s.target.close() // zero every resolved credential; nil-safe
	if err := s.sink.Close(); err != nil {
		fmt.Fprintf(stderr, "warning: trail: %v\n", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), teardownTimeout)
	defer cancel()
	if err := s.cleanup(ctx, s.keep); err != nil {
		fmt.Fprintf(stderr, "warning: workspace cleanup: %v\n", err)
	}
}

// isTerminal reports whether v is a character device (a terminal).
func isTerminal(v any) bool {
	f, ok := v.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func userName(environ []string) string {
	if u := envLookup(environ)("USER"); u != "" {
		return u
	}
	return "owner"
}

// customTools builds the config-declared argv tools, sorted for stable specs.
// Validation already vetted names, argv, risk, and schema.
func customTools(m map[string]config.CustomToolConfig) []core.Tool {
	out := make([]core.Tool, 0, len(m))
	for _, name := range slices.Sorted(maps.Keys(m)) {
		c := m[name]
		out = append(out, tools.NewCustomTool(name, c.Description, core.RiskClass(c.Risk), json.RawMessage(c.Schema), c.Argv))
	}
	return out
}

// formatRules converts config formatters; nil unless format.enabled.
func formatRules(f config.FormatConfig) []tools.FormatRule {
	if !f.Enabled {
		return nil
	}
	var out []tools.FormatRule
	for _, name := range slices.Sorted(maps.Keys(f.Languages)) {
		l := f.Languages[name]
		if len(l.Command) > 0 && len(l.Extensions) > 0 {
			out = append(out, tools.FormatRule{Command: slices.Clone(l.Command), Extensions: slices.Clone(l.Extensions)})
		}
	}
	return out
}

func loadTaskGraph(path string) (core.TaskGraph, error) {
	b, err := os.ReadFile(path) //nolint:gosec // operator-supplied --graph path
	if err != nil {
		return core.TaskGraph{}, err
	}
	var g core.TaskGraph
	if err := json.Unmarshal(b, &g); err != nil {
		return core.TaskGraph{}, fmt.Errorf("%w: graph: %w", core.ErrInvalidInput, err)
	}
	if err := g.Validate(); err != nil {
		return core.TaskGraph{}, err
	}
	return g, nil
}

func runGraphCmd(ctx context.Context, cfg config.Config, f runFlags, text string, provider core.ModelProvider, model string, target *providerTarget, root string, red *redact.Redactor, environ []string, stdout, stderr io.Writer, stdin io.Reader) int {
	g, err := loadTaskGraph(f.graph)
	if err != nil {
		return fail(stderr, "run", exitUsage, err)
	}
	err = runtime.RunGraph(ctx, g, func(ctx context.Context, n core.Subtask) error {
		nf := f
		nf.worktree = n.ID
		nf.noTUI = true
		title := n.Title
		if title == "" {
			title = n.ID
		}
		if text != "" {
			title = title + "\n" + text
		}
		s, err := newSession(ctx, cfg, nf, title, provider, model, target, root, red, environ)
		if err != nil {
			return err
		}
		defer s.close(stderr)
		p := tui.New(stdout, stdin, tui.Options{IsTTY: false, NoColor: true, Budget: s.input.Task.Budget})
		s.deps.Observer = p.Observer()
		if target != nil {
			target.relay.obs = p.Observer()
		}
		s.deps.Approve = p.Approver()
		if f.yes {
			s.deps.Approve = yesApprover(p.Approver())
		}
		code := s.run(ctx, p, stderr)
		if code > 1 {
			return fmt.Errorf("subtask %s failed (exit %d)", n.ID, code)
		}
		return nil
	})
	if err != nil {
		fmt.Fprintf(stderr, "friday run: graph: %v\n", err)
		return exitFailed
	}
	waves, err := g.Waves()
	if err != nil {
		fmt.Fprintf(stderr, "friday run: graph: %v\n", err)
		return exitFailed
	}
	ordered := make([]string, 0, len(g.Nodes))
	for _, wave := range waves {
		for _, n := range wave {
			ordered = append(ordered, "friday/"+n.ID)
		}
	}
	res, err := workspace.MergeBranches(ctx, root, ordered)
	if err != nil {
		fmt.Fprintf(stderr, "friday run: merge: %v\n", err)
		return exitFailed
	}
	if !res.OK {
		fmt.Fprintf(stderr, "friday run: merge conflict on %s: %v\n", res.StoppedAt, res.Conflicts)
		return exitFailed
	}
	fmt.Fprintf(stderr, "graph merged %d branches\n", len(res.Merged))
	return exitOK
}
