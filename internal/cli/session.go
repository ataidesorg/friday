package cli

// The interactive chat REPL: bare `friday` on a terminal opens a
// multi-turn conversation that persists to the session store and resumes
// across launches. One heavy graph (provider, sandbox, workspace, policy) is
// built per launch and reused across every turn; each turn runs one
// runtime.Run over the prior transcript as history.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/ataidesorg/friday/internal/config"
	"github.com/ataidesorg/friday/internal/core"
	"github.com/ataidesorg/friday/internal/lsp"
	"github.com/ataidesorg/friday/internal/mcp"
	"github.com/ataidesorg/friday/internal/observability"
	"github.com/ataidesorg/friday/internal/redact"
	"github.com/ataidesorg/friday/internal/runtime"
	sessionstore "github.com/ataidesorg/friday/internal/session"
	"github.com/ataidesorg/friday/internal/skills"
	"github.com/ataidesorg/friday/internal/tools"
	"github.com/ataidesorg/friday/internal/workspace"
)

// chatSession owns the one graph a whole conversation reuses. turn is the
// per-prompt Runner the TUI calls.
type chatSession struct {
	deps         runtime.Deps
	base         runtime.Input
	store        *sessionstore.Store
	id           string
	model        string
	routeName    string // active route name; switchRoute updates it
	profile      core.ProfileID
	sess         core.SessionID
	principal    core.Principal
	sink         *observability.LazyTrail
	mcp          []*mcp.Client            // stdio MCP servers; close shuts them down
	lsp          *lsp.Manager             // nil unless a language server is enabled
	metrics      *sessionstore.MetricsLog // per-turn local metrics; may be nil
	cleanup      workspace.Cleanup
	target       *providerTarget
	histChars    int
	baseTools    *tools.Registry // pre-agent tool set; setAgent filters from it
	agent        string          // active agent profile; empty means none
	loadedSkills []skills.Skill  // advertised in /skills; not mixed into Ctrl+P
	mode         string          // code | plan | ask; plan/ask filter to read-only tools
	clock        func() time.Time
	red          *redact.Redactor // redacts turn errors before they reach the TUI
	cfg          config.Config    // held so a live /model switch can resolve another route
	environ      []string
	wg           sync.WaitGroup // tracks an in-flight turn so close joins it before teardown
	runMu        sync.Mutex     // one runtime.Run at a time (shared provider relay)
	idMu         sync.Mutex
	runID        string // session the in-flight turn writes to; empty means c.id
	// run is the runtime seam; production points it at runtime.Run and tests
	// substitute a fake to assert turn ordering without a real provider.
	run func(context.Context, runtime.Deps, runtime.Input) (runtime.Result, error)
}

func (c *chatSession) sid() string {
	c.idMu.Lock()
	defer c.idMu.Unlock()
	if c.runID != "" {
		return c.runID
	}
	return c.id
}

func newChatSession(ctx context.Context, cfg config.Config, target *providerTarget, root string, red *redact.Redactor, environ []string, store *sessionstore.Store, id, routeName string, yes bool, worktree string) (*chatSession, error) {
	deps, in, err := buildGraph(cfg, target.provider, target.model, red, false)
	if err != nil {
		return nil, err
	}
	deps.Route, deps.Streamed = &target.decision, true
	spendPath, err := config.StateFilePath(envLookup(environ), "spend.jsonl")
	if err != nil {
		return nil, fmt.Errorf("spend ledger: %w", err)
	}
	if deps.Spend, err = runtime.NewSpend(cfg.Budgets.PerSessionUSD, cfg.Budgets.PerDayUSD, spendPath); err != nil {
		return nil, err
	}
	deps.Approve = chatApprover(yes)
	in.Project.ID, in.Project.Root = core.NewProjectID(), root
	rulesHome, _ := config.FridayHome(envLookup(environ))
	in.Project.InstructionFiles, in.Project.GlobalInstructionFiles = discoverRules(root, rulesHome, in.Project.InstructionFiles)
	if vcs, err := workspace.Status(ctx, root); err == nil {
		in.Project.VCS = &vcs
	}
	wsOpts, err := worktreeOpts(workspace.Options{Root: root, Project: in.Project.ID}, environ, worktree)
	if err != nil {
		return nil, err
	}
	ws, cleanup, err := workspace.Prepare(ctx, wsOpts)
	if err != nil {
		return nil, fmt.Errorf("workspace: %w", err)
	}
	in.Workspace, in.Spec.WorkDir = ws, ws.Root
	lspMgr := lspManager(cfg.LSP, ws.Root)
	if lspMgr != nil {
		deps.Tools = tools.WrapDiagnostics(deps.Tools, lspMgr)
	}
	sink := observability.NewLazyTrail(root, red, core.PrivacyMode(cfg.Telemetry.Privacy))
	deps.Sink = sink
	// Per-turn metrics land under <home>/logs/, separate from the session dir,
	// and never leave the machine. A home resolution failure disables
	// metrics rather than blocking the chat: they are best-effort local analytics.
	var metrics *sessionstore.MetricsLog
	if home, herr := config.FridayHome(envLookup(environ)); herr == nil {
		if ml, merr := sessionstore.NewMetricsLog(home, red); merr == nil {
			metrics = ml
		}
	}
	cs := &chatSession{
		deps: deps, base: in, store: store, id: id, model: target.model, routeName: routeName,
		profile: core.NewProfileID(), sess: core.SessionID(id),
		principal: core.Principal{Kind: core.PrincipalUser, Name: userName(environ)},
		sink:      sink, metrics: metrics, cleanup: cleanup, target: target, lsp: lspMgr,
		histChars: histCharsFor(target.provider), clock: time.Now, run: runtime.Run, red: red,
		cfg: cfg, environ: environ, baseTools: deps.Tools, mode: sessionMode(store, id),
	}
	cs.applyTools()
	cs.deps.Tools = cs.deps.Tools.WithTodos(cs.loadTodos, cs.saveTodos)
	cs.baseTools = cs.deps.Tools
	cs.applyTools()
	return cs, nil
}

// histCharsFor caps history at ~half the model's context window.
// ponytail: chars ≈ tokens×4 (phases.go bytes/4 estimate) × ½ window = ×2.
// Unknown window (0) means unbounded.
func histCharsFor(p core.ModelProvider) int {
	if maxCtx := p.Descriptor().Capabilities.MaxContextTokens; maxCtx > 0 {
		return maxCtx * 2
	}
	return 0
}

// toolCounter wraps a turn's observer to count completed tool calls for the
// metrics record. runtime.Result carries only a total Events count, so the
// tool-call tally has to be observed as events stream by.
type toolCounter struct {
	runtime.Observer
	n int
}

func (t *toolCounter) OnEvent(e core.Event) {
	if e.Kind == core.EventToolCompleted {
		t.n++
	}
	t.Observer.OnEvent(e)
}

// turn runs one prompt: load the prior transcript as history, run, then
// persist the user prompt and assistant reply together. The exchange is
// appended only after the run succeeds, so history never duplicates the
// prompt (phases.go appends Task.Description after History) and a failed or
// cancelled turn leaves no orphan user turn.
func (c *chatSession) turn(ctx context.Context, prompt string, obs runtime.Observer) (runtime.Result, error) {
	return c.turnOn(ctx, c.id, prompt, obs)
}

func (c *chatSession) turnOn(ctx context.Context, id, prompt string, obs runtime.Observer) (_ runtime.Result, rerr error) {
	// A turn owns a WaitGroup slot so close() joins it before tearing down the
	// workspace (resolves the S3a quit-mid-turn teardown race).
	c.wg.Add(1)
	defer c.wg.Done()
	c.runMu.Lock()
	defer c.runMu.Unlock()
	c.idMu.Lock()
	c.runID = id
	c.idMu.Unlock()
	defer func() {
		c.idMu.Lock()
		c.runID = ""
		c.idMu.Unlock()
	}()
	// Redact every returned error before it reaches the TUI (which cannot
	// import redact). Keep context.Canceled intact so the UI still labels a
	// cancelled turn via errors.Is.
	defer func() {
		if rerr != nil && c.red != nil && !errors.Is(rerr, context.Canceled) {
			rerr = errors.New(c.red.Redact(rerr.Error()))
		}
	}()
	_, prior, err := c.store.Load(id)
	if err != nil {
		return runtime.Result{}, err
	}
	harness := c.base.Agent.Harness
	if harness == "" {
		harness = core.HarnessCode
	}
	task, err := core.NewTask(prompt, harness, c.profile, core.SessionID(id), c.principal)
	if err != nil {
		return runtime.Result{}, err
	}
	task.Budget = c.base.Task.Budget
	// Auto-compact: when the replayable history nears its cap, fold it into a
	// summary first so old turns compress instead of silently falling off.
	// ponytail: fires every turn while over threshold if a summary ever fails
	// to shrink below it — one extra model call per turn is the ceiling.
	if c.histChars > 0 && tailChars(prior) > c.histChars*9/10 {
		if note, cerr := c.compact(ctx, obs); cerr == nil {
			obs.OnEvent(core.Event{Data: core.Warning{Message: "context near budget — " + note}})
			if _, fresh, lerr := c.store.Load(id); lerr == nil {
				prior = fresh
			}
		} else if !errors.Is(cerr, context.Canceled) {
			obs.OnEvent(core.Event{Data: core.Warning{Message: "auto-compact failed: " + cerr.Error()}})
		}
	}
	in := c.base
	in.Task = task
	in.History = sessionstore.History(prior, c.histChars)
	if todos := c.loadTodos(); len(todos) > 0 {
		in.History = append([]core.Message{{Role: core.RoleSystem, Content: tools.FormatTodos(todos)}}, in.History...)
	}
	parts, warns := imageAttachments(c.base.Workspace.Root, prompt)
	for _, w := range warns {
		obs.OnEvent(core.Event{Data: core.Warning{Message: w}})
	}
	in.Images = parts
	tc := &toolCounter{Observer: obs}
	c.deps.Observer = tc
	if c.target != nil {
		c.target.relay.obs = obs
	}
	start := c.clock()
	res, err := c.run(ctx, c.deps, in)
	c.recordMetric(res, tc.n, c.clock().Sub(start), err)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			if perr := c.persistCancelledTurn(id, prompt); perr != nil {
				return res, perr
			}
		}
		return res, err
	}
	if _, err := c.store.Append(id, sessionstore.Turn{Role: core.RoleUser, Text: prompt, TS: c.clock()}, c.clock()); err != nil {
		return res, err
	}
	if _, err := c.store.Append(id, sessionstore.Turn{Role: core.RoleAssistant, Text: res.Summary, Model: c.model, Usage: res.Usage, Cost: res.Cost, TS: c.clock()}, c.clock()); err != nil {
		return res, err
	}
	return res, nil
}

func (c *chatSession) persistCancelledTurn(id, prompt string) error {
	if _, err := c.store.Append(id, sessionstore.Turn{Role: core.RoleUser, Text: prompt, TS: c.clock()}, c.clock()); err != nil {
		return err
	}
	returned := "Turn cancelled before completion."
	_, err := c.store.Append(id, sessionstore.Turn{Role: core.RoleAssistant, Text: returned, Model: c.model, TS: c.clock()}, c.clock())
	return err
}

// recordMetric appends the turn's accounting to the local metrics log. A
// user-cancelled turn is not recorded (no meaningful outcome). A write
// failure is swallowed on purpose: metrics are best-effort local analytics
// and must never fail a good turn — the trail and transcript are the
// durable records.
func (c *chatSession) recordMetric(res runtime.Result, tools int, latency time.Duration, runErr error) {
	if c.metrics == nil || errors.Is(runErr, context.Canceled) {
		return
	}
	outcome := string(res.Outcome.Kind)
	if outcome == "" && runErr != nil {
		outcome = "error"
	}
	_ = c.metrics.Append(sessionstore.Metric{
		TS:        c.clock(),
		Session:   c.id,
		Model:     c.model,
		Route:     c.routeName,
		Usage:     res.Usage,
		Cost:      res.Cost,
		LatencyMS: latency.Milliseconds(),
		ToolCalls: tools,
		Outcome:   outcome,
	})
}

func (c *chatSession) close(stderr io.Writer) {
	// Join any in-flight turn before teardown so cleanup's RemoveAll never
	// races a still-running tool (the turn ctx is cancelled by app-ctx end, so
	// this returns promptly; the timeout bounds a wedged tool).
	if !waitTimeout(&c.wg, teardownTimeout) {
		fmt.Fprintln(stderr, "warning: a turn was still running at shutdown; cleaning up anyway")
	}
	c.target.close()
	c.lsp.Close() // nil-safe
	for _, cl := range c.mcp {
		if err := cl.Close(); err != nil {
			fmt.Fprintf(stderr, "warning: mcp %s: %v\n", cl.Name(), err)
		}
	}
	if err := c.sink.Close(); err != nil {
		fmt.Fprintf(stderr, "warning: trail: %v\n", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), teardownTimeout)
	defer cancel()
	if err := c.cleanup(ctx, false); err != nil {
		fmt.Fprintf(stderr, "warning: workspace cleanup: %v\n", err)
	}
}

// waitTimeout waits for wg, returning false if d elapses first.
func waitTimeout(wg *sync.WaitGroup, d time.Duration) bool {
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// startMCP spawns each enabled MCP server and bridges its tools into the
// registry as mcp_<server>_<tool>, each gated by the policy engine. A server
// that fails to start, handshake, or list degrades to a warning — chat never
// fails to launch over an MCP server.
func (c *chatSession) startMCP(ctx context.Context, servers map[string]config.MCPServerConfig, stderr io.Writer) {
	for _, name := range slices.Sorted(maps.Keys(servers)) {
		srv := servers[name]
		if !srv.Enabled {
			continue
		}
		client, err := mcp.Start(ctx, name, srv.Command)
		if err != nil {
			fmt.Fprintf(stderr, "warning: mcp %s: %v\n", name, err)
			continue
		}
		defs, err := func() ([]mcp.ToolDef, error) {
			if err := client.Initialize(); err != nil {
				return nil, err
			}
			return client.ListTools()
		}()
		if err != nil {
			fmt.Fprintf(stderr, "warning: mcp %s: %v\n", name, err)
			_ = client.Close()
			continue
		}
		reg, err := c.deps.Tools.With(client.Tools(defs)...)
		if err != nil {
			fmt.Fprintf(stderr, "warning: mcp %s: %v\n", name, err)
			_ = client.Close()
			continue
		}
		c.deps.Tools = reg
		c.baseTools = reg
		c.mcp = append(c.mcp, client)
	}
}

// compactPrompt asks the model to compress its own context. The reply becomes
// the session's summary turn; History then replays it instead of the turns it
// covers.
const compactPrompt = "Summarise this conversation so far for your own future context. Keep every decision, constraint, open question, and concrete fact (names, paths, numbers); drop pleasantries and repetition. Reply with the plain-text summary only."

// minCompactTurns is the smallest transcript worth a summarisation call.
const minCompactTurns = 4

// compact runs one summarisation turn through the current model and appends
// the result as a summary turn. The transcript stays append-only: nothing is
// rewritten, History just stops replaying past the new summary. The store
// redacts the summary on write, like every other turn.
func (c *chatSession) compact(ctx context.Context, obs runtime.Observer) (_ string, rerr error) {
	c.wg.Add(1)
	defer c.wg.Done()
	defer func() {
		if rerr != nil && c.red != nil && !errors.Is(rerr, context.Canceled) {
			rerr = errors.New(c.red.Redact(rerr.Error()))
		}
	}()
	_, prior, err := c.store.Load(c.sid())
	if err != nil {
		return "", err
	}
	history := sessionstore.History(prior, 0)
	if len(history) < minCompactTurns {
		return "", fmt.Errorf("%w: nothing to compact yet (%d messages)", core.ErrInvalidInput, len(history))
	}
	task, err := core.NewTask(compactPrompt, core.HarnessCode, c.profile, c.sess, c.principal)
	if err != nil {
		return "", err
	}
	task.Budget = c.base.Task.Budget
	in := c.base
	in.Task = task
	in.History = history
	c.deps.Observer = obs
	if c.target != nil {
		c.target.relay.obs = obs
	}
	start := c.clock()
	res, err := c.run(ctx, c.deps, in)
	c.recordMetric(res, 0, c.clock().Sub(start), err)
	if err != nil {
		return "", err
	}
	if res.Summary == "" {
		return "", fmt.Errorf("compact: model returned an empty summary")
	}
	if _, err := c.store.Append(c.sid(), sessionstore.Turn{
		Role: core.RoleAssistant, Kind: sessionstore.KindSummary,
		Text: res.Summary, Model: c.model, Usage: res.Usage, Cost: res.Cost, TS: c.clock(),
	}, c.clock()); err != nil {
		return "", err
	}
	return fmt.Sprintf("compacted %d turns into a summary", len(history)), nil
}

// tailChars measures the history the next turn would replay, in characters —
// the same unit histChars bounds.
func tailChars(turns []sessionstore.Turn) int {
	n := 0
	for _, m := range sessionstore.History(turns, 0) {
		n += len(m.Content)
	}
	return n
}
