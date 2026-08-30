package runtime_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ataidesorg/ink/internal/config"
	"github.com/ataidesorg/ink/internal/core"
	"github.com/ataidesorg/ink/internal/models/mock"
	"github.com/ataidesorg/ink/internal/policy"
	"github.com/ataidesorg/ink/internal/runtime"
	"github.com/ataidesorg/ink/internal/tools"
)

const scriptDir = "../../test/scripts"

type fakeProvider struct {
	mu         sync.Mutex
	created    int
	destroyed  int
	specs      []core.SandboxSpec
	argv       [][]string
	createErr  error
	destroyErr error
	exec       func(ctx context.Context, req core.ExecRequest) (core.ExecResult, error)
}

type fakeSandbox struct {
	p    *fakeProvider
	info core.SandboxInfo
}

func (p *fakeProvider) Name() string { return "fake" }

func (p *fakeProvider) Create(_ context.Context, spec core.SandboxSpec) (core.Sandbox, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.createErr != nil {
		return nil, p.createErr
	}
	p.created++
	p.specs = append(p.specs, spec)
	return &fakeSandbox{p: p, info: core.SandboxInfo{ID: core.NewSandboxID(), Provider: "fake", Spec: spec}}, nil
}

func (s *fakeSandbox) Info() core.SandboxInfo { return s.info }

func (s *fakeSandbox) Exec(ctx context.Context, req core.ExecRequest) (core.ExecResult, error) {
	s.p.mu.Lock()
	s.p.argv = append(s.p.argv, req.Argv)
	s.p.mu.Unlock()
	if s.p.exec != nil {
		return s.p.exec(ctx, req)
	}
	return core.ExecResult{ExitCode: 0, Stdout: "ok  \tsample\t0.012s\n"}, nil
}

func (s *fakeSandbox) Destroy(context.Context) error {
	s.p.mu.Lock()
	defer s.p.mu.Unlock()
	s.p.destroyed++
	return s.p.destroyErr
}

func (p *fakeProvider) sawArgv0(name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.argv {
		if len(a) > 0 && a[0] == name {
			return true
		}
	}
	return false
}

type observer struct {
	mu     sync.Mutex
	phases []core.Phase
	events int
	deltas int
}

func (o *observer) OnEvent(core.Event) { o.mu.Lock(); o.events++; o.mu.Unlock() }
func (o *observer) OnPhase(p core.Phase) {
	o.mu.Lock()
	o.phases = append(o.phases, p)
	o.mu.Unlock()
}
func (o *observer) OnModelDelta(string) { o.mu.Lock(); o.deltas++; o.mu.Unlock() }

type harness struct {
	sink *core.MemorySink
	prov *fakeProvider
	obs  *observer
	deps runtime.Deps
	in   runtime.Input
	root string
}

func toolsCfg() config.ToolsConfig {
	return config.ToolsConfig{
		DefaultEffect: "deny",
		Allow:         []string{"read_file", "list_dir", "search", "write_file", "run_command", "ask_user_question", "todo_write", "goal_complete", "goal_blocked", "goal_wait"},
		Commands:      config.CommandsConfig{Allowed: []string{"go test"}},
	}
}

func newHarness(t *testing.T, script string, cfg config.ToolsConfig) *harness {
	t.Helper()
	return newHarnessAt(t, filepath.Join(scriptDir, script), cfg)
}

func newHarnessAt(t *testing.T, scriptPath string, cfg config.ToolsConfig) *harness {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "greet.go"), "package sample\n\n// Greet says hello.\nfunc Greet(name string) string { return \"hi \" + name }\n")
	write(t, filepath.Join(root, "AGENTS.md"), "Keep one exported function per file.\n")
	sc, err := mock.LoadScript(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	eng, err := policy.FromConfig(cfg, core.PostureStandard, nil)
	if err != nil {
		t.Fatal(err)
	}
	task, err := core.NewTask("add Farewell(name) with a test", core.HarnessCode, core.NewProfileID(), core.NewSessionID(), core.Principal{Kind: core.PrincipalUser, Name: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{sink: &core.MemorySink{}, prov: &fakeProvider{}, obs: &observer{}, root: root}
	h.deps = runtime.Deps{
		Provider:  mock.New(sc),
		Tools:     tools.Default(nil, eng.AllowedCommands()),
		Policy:    eng,
		Approvals: policy.NewApprovals(),
		Sandbox:   h.prov,
		Sink:      h.sink,
		Observer:  h.obs,
	}
	h.in = runtime.Input{
		Task:      task,
		Project:   core.Project{ID: core.NewProjectID(), Name: "sample", Root: root, InstructionFiles: []string{"AGENTS.md", "MISSING.md"}},
		Workspace: core.Workspace{ID: core.NewWorkspaceID(), Root: root, Kind: core.WorkspaceEphemeral},
		Model:     sc.Model,
		Posture:   core.PostureStandard,
		TestCmd:   []string{"go", "test", "./..."},
	}
	return h
}

func (h *harness) run(ctx context.Context, t *testing.T) runtime.Result {
	t.Helper()
	res, err := runtime.Run(ctx, h.deps, h.in)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if h.prov.destroyed != h.prov.created {
		t.Fatalf("sandboxes created %d, destroyed %d", h.prov.created, h.prov.destroyed)
	}
	evs := h.sink.Events()
	if res.Events != len(evs) {
		t.Fatalf("Result.Events %d, sink has %d", res.Events, len(evs))
	}
	if h.obs.events != len(evs) {
		t.Fatalf("observer saw %d events, sink %d", h.obs.events, len(evs))
	}
	var seq uint64
	for i, e := range evs {
		if e.Seq <= seq {
			t.Fatalf("event %d (%s) seq %d not increasing after %d", i, e.Kind, e.Seq, seq)
		}
		seq = e.Seq
		if e.Task != h.in.Task.ID || e.Run != res.Run.ID {
			t.Fatalf("event %d (%s) attributed to task %s run %s", i, e.Kind, e.Task, e.Run)
		}
		if _, err := e.MarshalJSON(); err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
	}
	if len(evs) == 0 || evs[len(evs)-1].Kind != core.EventTaskFinished {
		t.Fatalf("last event is not task_finished: %v", kinds(evs))
	}
	if !res.Run.State.Terminal() {
		t.Fatalf("run not terminal: %+v", res.Run.State)
	}
	checkTrail(t, evs)
	return res
}

// checkTrail is the acceptance rule: no tool runs without a recorded
// policy decision for the same call, and a successful call was allowed.
func checkTrail(t *testing.T, evs []core.Event) {
	t.Helper()
	decided := map[core.ToolCallID]core.Effect{}
	approved := map[core.ToolCallID]bool{}
	var lastCall core.ToolCallID
	for _, e := range evs {
		switch d := e.Data.(type) {
		case core.ToolCalled:
			lastCall = d.Call
		case core.PolicyDecided:
			decided[d.Call] = d.Effect
		case core.ApprovalResolved:
			approved[lastCall] = d.Decision == core.ApprovalApproved
		case core.ToolCompleted:
			eff, ok := decided[d.Call]
			if !ok {
				t.Fatalf("tool_completed %s without policy_decision", d.Call)
			}
			if d.Success && eff != core.EffectAllow && (eff != core.EffectRequireApproval || !approved[d.Call]) {
				t.Fatalf("call %s succeeded under effect %s", d.Call, eff)
			}
		}
	}
}

func kinds(evs []core.Event) []core.EventKind {
	out := make([]core.EventKind, len(evs))
	for i, e := range evs {
		out[i] = e.Kind
	}
	return out
}

func find[T core.EventData](evs []core.Event) []T {
	var out []T
	for _, e := range evs {
		if d, ok := e.Data.(T); ok {
			out = append(out, d)
		}
	}
	return out
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestHappyPath(t *testing.T) {
	h := newHarness(t, "add-farewell.json", toolsCfg())
	res := h.run(context.Background(), t)
	if res.Outcome.Kind != core.OutcomeCompletedVerified {
		t.Fatalf("outcome %+v", res.Outcome)
	}
	want := []core.EventKind{
		"task_created", "state_changed", "sandbox_created", "state_changed", "warning", "context_assembled", "state_changed",
		"model_selected", "state_changed", "model_usage", "state_changed",
		"tool_called", "policy_decision", "tool_completed", "model_usage",
		"tool_called", "policy_decision", "tool_completed", "tool_called", "policy_decision", "tool_completed", "model_usage",
		"tool_called", "policy_decision", "tool_completed", "model_usage",
		"state_changed", "validation_result", "state_changed", "state_changed", "memory_candidate", "state_changed", "state_changed",
		"sandbox_destroyed", "task_finished",
	}
	evs := h.sink.Events()
	got := kinds(evs)
	if len(got) != len(want) {
		t.Fatalf("got %d events %v\nwant %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event %d = %s, want %s\nall: %v", i, got[i], want[i], got)
		}
	}
	if _, err := os.Stat(filepath.Join(h.root, "farewell_test.go")); err != nil {
		t.Fatalf("write_file did not land in the workspace: %v", err)
	}
	if !strings.Contains(res.Summary, "Learned:") || len(res.Memories) != 1 || res.Memories[0].Category != core.MemoryProject || res.Memories[0].Status != core.CandidatePending {
		t.Fatalf("summary %q memories %+v", res.Summary, res.Memories)
	}
	if res.Usage.InputTokens != 2490 || res.Usage.OutputTokens != 244 {
		t.Fatalf("usage %+v", res.Usage)
	}
	if res.Cost.Actual != nil || res.Cost.Estimated != nil {
		t.Fatalf("mock cost must be unknown, got %+v", res.Cost)
	}
	v := find[core.ValidationResult](evs)
	if len(v) != 1 || !v[0].Passed || v[0].Command != "go test ./..." {
		t.Fatalf("validation %+v", v)
	}
	ms := find[core.ModelSelected](evs)
	if len(ms) != 1 || ms[0].Provider != "mock" || ms[0].Model != "mock-1" || ms[0].Reason != "single provider configured" {
		t.Fatalf("model_selected %+v", ms)
	}
	ca := find[core.ContextAssembled](evs)
	if len(ca) != 1 || ca[0].Items != 1 || ca[0].Excluded != 1 || ca[0].UsedTokens == 0 {
		t.Fatalf("context_assembled %+v", ca)
	}
	fin := find[core.TaskFinished](evs)
	if len(fin) != 1 || fin[0].Outcome.Kind != core.OutcomeCompletedVerified || fin[0].Usage != res.Usage {
		t.Fatalf("task_finished %+v", fin)
	}
	if len(h.prov.specs) != 1 || h.prov.specs[0].WorkDir != h.root || h.prov.specs[0].Source.Kind != core.SourceInPlace {
		t.Fatalf("sandbox spec %+v", h.prov.specs)
	}
	wantPhases := []core.Phase{"intake", "preflight", "context_assembly", "model_selection", "planning", "tool_execution", "validation", "synthesis", "memory_extraction", "telemetry_capture"}
	if len(h.obs.phases) != len(wantPhases) {
		t.Fatalf("phases %v", h.obs.phases)
	}
	for i := range wantPhases {
		if h.obs.phases[i] != wantPhases[i] {
			t.Fatalf("phases %v", h.obs.phases)
		}
	}
	if h.obs.deltas != 4 {
		t.Fatalf("model deltas %d", h.obs.deltas)
	}
}

func TestForbiddenCommandDeniedByPolicy(t *testing.T) {
	h := newHarness(t, "forbidden-rm.json", toolsCfg())
	res := h.run(context.Background(), t)
	if res.Outcome.Kind != core.OutcomeCompletedVerified {
		t.Fatalf("outcome %+v", res.Outcome)
	}
	evs := h.sink.Events()
	var denied []core.PolicyDecided
	for _, d := range find[core.PolicyDecided](evs) {
		if d.Effect == core.EffectDeny {
			denied = append(denied, d)
		}
	}
	if len(denied) != 1 || denied[0].Call != "call-5" || denied[0].Tool != "run_command" || denied[0].Rule != policy.RuleCommandAllowed {
		t.Fatalf("denials %+v", denied)
	}
	if h.prov.sawArgv0("rm") {
		t.Fatal("rm reached the sandbox")
	}
	for _, c := range find[core.ToolCompleted](evs) {
		if c.Call == "call-5" && (c.Success || !strings.Contains(c.OutputSummary, "denied")) {
			t.Fatalf("call-5 completion %+v", c)
		}
	}
}

func TestFailingValidationIsUnverified(t *testing.T) {
	h := newHarness(t, "add-farewell.json", toolsCfg())
	// The tool's `go test` is the first exec and passes; the validation exec is the second and fails.
	calls := 0
	h.prov.exec = func(context.Context, core.ExecRequest) (core.ExecResult, error) {
		calls++
		if calls == 1 {
			return core.ExecResult{ExitCode: 0, Stdout: "ok\n"}, nil
		}
		return core.ExecResult{ExitCode: 1, Stdout: "--- FAIL: TestFarewell\nFAIL\n"}, nil
	}
	res := h.run(context.Background(), t)
	if res.Outcome.Kind != core.OutcomeCompletedUnverified || !strings.Contains(res.Outcome.Reason, "exit 1") {
		t.Fatalf("outcome %+v", res.Outcome)
	}
	v := find[core.ValidationResult](h.sink.Events())
	if len(v) != 1 || v[0].Passed || v[0].ExitCode != 1 || !strings.Contains(v[0].Summary, "FAIL") {
		t.Fatalf("validation %+v", v)
	}
}

func TestNoValidationCommandIsUnverified(t *testing.T) {
	h := newHarness(t, "add-farewell.json", toolsCfg())
	h.in.TestCmd = nil
	res := h.run(context.Background(), t)
	if res.Outcome.Kind != core.OutcomeCompletedUnverified {
		t.Fatalf("outcome %+v", res.Outcome)
	}
	if len(find[core.ValidationResult](h.sink.Events())) != 0 {
		t.Fatal("validation_result without a command")
	}
}

func TestToolCallBudget(t *testing.T) {
	h := newHarness(t, "add-farewell.json", toolsCfg())
	h.in.Task.Budget.MaxToolCalls = 1
	res := h.run(context.Background(), t)
	if res.Outcome.Kind != core.OutcomeFailed || res.Outcome.Category != core.FailureBudgetExceeded {
		t.Fatalf("outcome %+v", res.Outcome)
	}
	if n := len(find[core.ToolCompleted](h.sink.Events())); n != 1 {
		t.Fatalf("tool calls completed %d, want 1", n)
	}
}

func TestCostBudget(t *testing.T) {
	h := newHarness(t, "add-farewell.json", toolsCfg())
	usd, _ := core.USDFromFloat(0.5)
	h.in.Task.Budget.MaxCost = usd
	h.deps.Price = func(_, _ string, u core.Usage) *core.USDMicros {
		c := core.USDMicros(int64(u.InputTokens+u.OutputTokens) * 1000)
		return &c
	}
	res := h.run(context.Background(), t)
	if res.Outcome.Kind != core.OutcomeFailed || res.Outcome.Category != core.FailureBudgetExceeded {
		t.Fatalf("outcome %+v", res.Outcome)
	}
	if res.Cost.Actual == nil || *res.Cost.Actual < usd {
		t.Fatalf("cost %+v", res.Cost)
	}
	mu := find[core.ModelUsage](h.sink.Events())
	if len(mu) != 2 || mu[0].Cost.Actual == nil || *mu[0].Cost.Actual != 452000 {
		t.Fatalf("model_usage %+v", mu)
	}
}

func TestCancelledContextIsUserAborted(t *testing.T) {
	h := newHarness(t, "add-farewell.json", toolsCfg())
	ctx, cancel := context.WithCancel(context.Background())
	h.prov.exec = func(ctx context.Context, _ core.ExecRequest) (core.ExecResult, error) {
		cancel()
		return core.ExecResult{}, ctx.Err()
	}
	res := h.run(ctx, t)
	if res.Outcome.Kind != core.OutcomeFailed || res.Outcome.Category != core.FailureUserAborted {
		t.Fatalf("outcome %+v", res.Outcome)
	}
}

func TestWallClockBudgetIsTimeout(t *testing.T) {
	h := newHarness(t, "add-farewell.json", toolsCfg())
	h.in.Task.Budget.MaxWallClock = 20 * time.Millisecond
	h.prov.exec = func(ctx context.Context, _ core.ExecRequest) (core.ExecResult, error) {
		<-ctx.Done()
		return core.ExecResult{}, ctx.Err()
	}
	res := h.run(context.Background(), t)
	if res.Outcome.Kind != core.OutcomeFailed || res.Outcome.Category != core.FailureTimeout {
		t.Fatalf("outcome %+v", res.Outcome)
	}
}

func TestNoApproverDeniesRequireApproval(t *testing.T) {
	cfg := toolsCfg()
	cfg.Allow = []string{"read_file", "list_dir", "search", "run_command"}
	cfg.RequireApproval = []string{"write_file"}
	h := newHarness(t, "add-farewell.json", cfg)
	res := h.run(context.Background(), t)
	if res.Outcome.Kind != core.OutcomeCompletedVerified {
		t.Fatalf("outcome %+v", res.Outcome)
	}
	evs := h.sink.Events()
	req, resolved := find[core.ApprovalRequested](evs), find[core.ApprovalResolved](evs)
	if len(req) != 2 || len(resolved) != 2 {
		t.Fatalf("requested %d resolved %d", len(req), len(resolved))
	}
	for _, r := range resolved {
		if r.Decision != core.ApprovalDenied || r.By.Kind != core.PrincipalSystem {
			t.Fatalf("resolved %+v", r)
		}
	}
	for _, c := range find[core.ToolCompleted](evs) {
		if c.Tool == "write_file" && (c.Success || !strings.Contains(c.OutputSummary, "no approver (non-interactive)")) {
			t.Fatalf("write_file completion %+v", c)
		}
	}
	if _, err := os.Stat(filepath.Join(h.root, "farewell.go")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("denied write landed: %v", err)
	}
}

func TestApproverDenyDoesNotWrite(t *testing.T) {
	body := `{
  "model": "mock-1",
  "turns": [
    {
      "finish": "tool_calls",
      "usage": {"input_tokens": 8, "output_tokens": 6},
      "tool_calls": [
        {"id": "call-1", "name": "write_file", "arguments": {"path": "hello.txt", "content": "hi\n"}},
        {"id": "call-2", "name": "write_file", "arguments": {"path": "hello.txt", "content": "hi\n"}}
      ]
    },
    {
      "content": "stopped",
      "finish": "stop",
      "match": "denied",
      "usage": {"input_tokens": 8, "output_tokens": 2}
    }
  ]
}`
	script := filepath.Join(t.TempDir(), "deny-write.json")
	if err := os.WriteFile(script, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := toolsCfg()
	cfg.Allow = []string{"read_file", "list_dir", "search", "run_command"}
	cfg.RequireApproval = []string{"write_file"}
	h := newHarnessAt(t, script, cfg)
	asked := 0
	h.deps.Approve = func(_ context.Context, _ core.Approval) (core.ApprovalResolution, error) {
		asked++
		return core.ApprovalResolution{Decision: core.ApprovalDenied, By: core.Principal{Kind: core.PrincipalUser, Name: "owner"}, Scope: core.ApprovalOnce, Note: "no"}, nil
	}
	res := h.run(context.Background(), t)
	if res.Outcome.Kind == core.OutcomeFailed {
		t.Fatalf("outcome %+v", res.Outcome)
	}
	if asked != 1 {
		t.Fatalf("harness re-asked after deny: %d prompts", asked)
	}
	if _, err := os.Stat(filepath.Join(h.root, "hello.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("denied write landed: %v", err)
	}
}

func TestApproverSessionScope(t *testing.T) {
	cfg := toolsCfg()
	cfg.Allow = []string{"read_file", "list_dir", "search"}
	cfg.RequireApproval = []string{"write_file", "run_command"}
	h := newHarness(t, "add-farewell.json", cfg)
	var asked []core.Approval
	h.deps.Approve = func(_ context.Context, a core.Approval) (core.ApprovalResolution, error) {
		asked = append(asked, a)
		return core.ApprovalResolution{Decision: core.ApprovalApproved, By: core.Principal{Kind: core.PrincipalUser, Name: "tester"}, Scope: core.ApprovalSession}, nil
	}
	res := h.run(context.Background(), t)
	if res.Outcome.Kind != core.OutcomeCompletedVerified {
		t.Fatalf("outcome %+v", res.Outcome)
	}
	// Two distinct write paths and one command: three keys, three prompts.
	if len(asked) != 3 {
		t.Fatalf("asked %d times", len(asked))
	}
	for _, a := range asked {
		if a.Task != h.in.Task.ID || a.Run != res.Run.ID || a.Request.Tool == "" || a.RequestedAt.IsZero() {
			t.Fatalf("approval %+v", a)
		}
	}
	if _, ok := h.deps.Approvals.Lookup(asked[0].Request); !ok {
		t.Fatal("session approval not recorded")
	}
	// Same run again with the same store: nothing is asked a second time.
	h2 := newHarness(t, "add-farewell.json", cfg)
	h2.deps.Approvals = h.deps.Approvals
	h2.deps.Approve = func(context.Context, core.Approval) (core.ApprovalResolution, error) {
		t.Fatal("approver called despite session approvals")
		return core.ApprovalResolution{}, nil
	}
	if res := h2.run(context.Background(), t); res.Outcome.Kind != core.OutcomeCompletedVerified {
		t.Fatalf("outcome %+v", res.Outcome)
	}
	if n := len(find[core.ApprovalResolved](h2.sink.Events())); n != 3 {
		t.Fatalf("cached resolutions recorded %d, want 3", n)
	}
}

func TestApproverErrorsFailClosed(t *testing.T) {
	cfg := toolsCfg()
	cfg.Allow = []string{"read_file", "list_dir", "search", "run_command"}
	cfg.RequireApproval = []string{"write_file"}
	h := newHarness(t, "add-farewell.json", cfg)
	h.deps.Approve = func(context.Context, core.Approval) (core.ApprovalResolution, error) {
		return core.ApprovalResolution{}, errors.New("tty closed")
	}
	res := h.run(context.Background(), t)
	if res.Outcome.Kind != core.OutcomeFailed || res.Outcome.Category != core.FailureInternal || !strings.Contains(res.Outcome.Reason, "tty closed") {
		t.Fatalf("outcome %+v", res.Outcome)
	}
	h = newHarness(t, "add-farewell.json", cfg)
	h.deps.Approve = func(context.Context, core.Approval) (core.ApprovalResolution, error) {
		return core.ApprovalResolution{Decision: "maybe", By: core.Principal{Kind: core.PrincipalUser, Name: "x"}}, nil
	}
	if res := h.run(context.Background(), t); res.Outcome.Kind != core.OutcomeCompletedVerified {
		t.Fatalf("outcome %+v", res.Outcome)
	}
	for _, r := range find[core.ApprovalResolved](h.sink.Events()) {
		if r.Decision != core.ApprovalDenied {
			t.Fatalf("unknown decision must deny: %+v", r)
		}
	}
}

func TestProviderError(t *testing.T) {
	h := newHarness(t, "add-farewell.json", toolsCfg())
	h.in.Model = "other-model"
	res := h.run(context.Background(), t)
	if res.Outcome.Kind != core.OutcomeFailed || res.Outcome.Category != core.FailureProviderError {
		t.Fatalf("outcome %+v", res.Outcome)
	}
	if h.prov.created != 1 {
		t.Fatalf("sandbox created %d", h.prov.created)
	}
}

func TestSandboxError(t *testing.T) {
	h := newHarness(t, "add-farewell.json", toolsCfg())
	h.prov.createErr = errors.New("no space")
	res := h.run(context.Background(), t)
	if res.Outcome.Kind != core.OutcomeFailed || res.Outcome.Category != core.FailureSandboxError {
		t.Fatalf("outcome %+v", res.Outcome)
	}
	h = newHarness(t, "add-farewell.json", toolsCfg())
	h.prov.exec = func(context.Context, core.ExecRequest) (core.ExecResult, error) {
		return core.ExecResult{}, errors.New("exec failed")
	}
	res = h.run(context.Background(), t)
	// A tool-level exec error is data for the model, not a run failure; the script then stalls on its "ok" match.
	if res.Outcome.Kind != core.OutcomeFailed || res.Outcome.Category != core.FailureProviderError {
		t.Fatalf("outcome %+v", res.Outcome)
	}
	done := find[core.ToolCompleted](h.sink.Events())
	if last := done[len(done)-1]; last.Success || !strings.Contains(last.OutputSummary, "exec failed") {
		t.Fatalf("exec error not fed back: %+v", last)
	}
}

func TestUnknownToolAndBadArguments(t *testing.T) {
	h := newHarness(t, "add-farewell.json", toolsCfg())
	sc := mock.Script{Model: "mock-1", Turns: []mock.Turn{
		{Finish: core.FinishToolCalls, ToolCalls: []core.ToolCall{
			{ID: "c1", Name: "teleport", Arguments: []byte(`{}`)},
			{ID: "c2", Name: "read_file", Arguments: []byte(`{"path": 7}`)},
		}},
		{Content: "done", Finish: core.FinishStop, Match: "error"},
	}}
	h.deps.Provider = mock.New(sc)
	res := h.run(context.Background(), t)
	if res.Outcome.Kind != core.OutcomeCompletedVerified {
		t.Fatalf("outcome %+v", res.Outcome)
	}
	evs := h.sink.Events()
	decs := find[core.PolicyDecided](evs)
	if len(decs) != 2 || decs[0].Effect != core.EffectDeny || decs[0].Rule != runtime.RuleUnknownTool || decs[1].Effect != core.EffectAllow {
		t.Fatalf("unknown tool must be denied by the runtime, read_file allowed: %+v", decs)
	}
	done := find[core.ToolCompleted](evs)
	if len(done) != 2 || done[0].Success || done[1].Success {
		t.Fatalf("completions %+v", done)
	}
}

func TestInvalidInput(t *testing.T) {
	h := newHarness(t, "add-farewell.json", toolsCfg())
	for name, mut := range map[string]func(*runtime.Input){
		"empty description": func(in *runtime.Input) { in.Task.Description = " " },
		"relative root":     func(in *runtime.Input) { in.Workspace.Root = "rel" },
		"empty model":       func(in *runtime.Input) { in.Model = "" },
		"bad posture":       func(in *runtime.Input) { in.Posture = "lenient" },
	} {
		h := newHarness(t, "add-farewell.json", toolsCfg())
		mut(&h.in)
		res := h.run(context.Background(), t)
		if res.Outcome.Kind != core.OutcomeFailed || res.Outcome.Category != core.FailureInternal {
			t.Fatalf("%s: outcome %+v", name, res.Outcome)
		}
		if h.prov.created != 0 {
			t.Fatalf("%s: sandbox created before intake passed", name)
		}
	}
	for name, mut := range map[string]func(*runtime.Deps){
		"provider": func(d *runtime.Deps) { d.Provider = nil },
		"tools":    func(d *runtime.Deps) { d.Tools = nil },
		"policy":   func(d *runtime.Deps) { d.Policy = nil },
		"sandbox":  func(d *runtime.Deps) { d.Sandbox = nil },
		"sink":     func(d *runtime.Deps) { d.Sink = nil },
	} {
		d := h.deps
		mut(&d)
		if _, err := runtime.Run(context.Background(), d, h.in); !errors.Is(err, core.ErrInvalidInput) {
			t.Fatalf("nil %s: %v", name, err)
		}
	}
}

func TestEmptyPostureFailsClosedToStrict(t *testing.T) {
	h := newHarness(t, "add-farewell.json", toolsCfg())
	h.in.Posture = ""
	res := h.run(context.Background(), t)
	// Strict upgrades allowed writes and commands to approval; with no approver every one is denied,
	// so the scripted model never sees "ok" and the run fails on the provider, not by writing anything.
	if res.Outcome.Kind != core.OutcomeFailed || res.Outcome.Category != core.FailureProviderError {
		t.Fatalf("outcome %+v", res.Outcome)
	}
	if n := len(find[core.ApprovalRequested](h.sink.Events())); n != 3 {
		t.Fatalf("strict posture approvals = %d, want 3", n)
	}
	if _, err := os.Stat(filepath.Join(h.in.Workspace.Root, "farewell.go")); err == nil {
		t.Fatal("denied write reached the tree")
	}
}

type unhealthy struct{ core.ModelProvider }

func (u unhealthy) Descriptor() core.ProviderDescriptor {
	d := u.ModelProvider.Descriptor()
	d.Health = core.ProviderHealth{State: core.HealthUnhealthy, Reason: "circuit open"}
	return d
}

type noTools struct{ core.ModelProvider }

func (n noTools) Descriptor() core.ProviderDescriptor {
	d := n.ModelProvider.Descriptor()
	d.Capabilities.ToolCalling = false
	return d
}

type failingSink struct{ after int }

func (f *failingSink) Emit(context.Context, core.Event) error {
	f.after--
	if f.after < 0 {
		return errors.New("disk full")
	}
	return nil
}

func TestPreflightRefusesUnusableProvider(t *testing.T) {
	for name, wrap := range map[string]func(core.ModelProvider) core.ModelProvider{
		"unhealthy": func(p core.ModelProvider) core.ModelProvider { return unhealthy{p} },
		"no tools":  func(p core.ModelProvider) core.ModelProvider { return noTools{p} },
	} {
		h := newHarness(t, "add-farewell.json", toolsCfg())
		h.deps.Provider = wrap(h.deps.Provider)
		res := h.run(context.Background(), t)
		if res.Outcome.Kind != core.OutcomeFailed || res.Outcome.Category != core.FailureProviderError || h.prov.created != 0 {
			t.Fatalf("%s: outcome %+v created %d", name, res.Outcome, h.prov.created)
		}
	}
}

func TestSinkFailureAbortsRun(t *testing.T) {
	h := newHarness(t, "add-farewell.json", toolsCfg())
	h.deps.Sink = &failingSink{after: 3}
	res, err := runtime.Run(context.Background(), h.deps, h.in)
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("err = %v", err)
	}
	if h.prov.destroyed != h.prov.created || res.Outcome.Kind != core.OutcomeFailed || res.Events != 3 {
		t.Fatalf("sandbox %d/%d outcome %+v events %d", h.prov.destroyed, h.prov.created, res.Outcome, res.Events)
	}
}

func TestUnknownCostWarnsOnce(t *testing.T) {
	h := newHarness(t, "add-farewell.json", toolsCfg())
	h.in.Task.Budget.MaxCost = 10_000
	res := h.run(context.Background(), t)
	if res.Outcome.Kind != core.OutcomeCompletedVerified || res.Cost.Actual != nil {
		t.Fatalf("outcome %+v cost %+v", res.Outcome, res.Cost)
	}
	n := 0
	for _, w := range find[core.Warning](h.sink.Events()) {
		if strings.Contains(w.Message, "max_cost cannot be enforced") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("unknown-cost warnings = %d", n)
	}
}

func TestSecretMemoryDroppedAndDestroyErrorWarned(t *testing.T) {
	h := newHarness(t, "add-farewell.json", toolsCfg())
	sc, err := mock.LoadScript(filepath.Join(scriptDir, "add-farewell.json"))
	if err != nil {
		t.Fatal(err)
	}
	last := &sc.Turns[len(sc.Turns)-1]
	last.Content = "Learned: the token is " + strings.Repeat("A", 20) + "\nLearned: keep one function per file."
	last.Content = strings.Replace(last.Content, "the token is ", "the token is sk-"+"ant-api03-", 1)
	h.deps.Provider = mock.New(sc)
	h.prov.destroyErr = errors.New("busy")
	res := h.run(context.Background(), t)
	if res.Outcome.Kind != core.OutcomeCompletedVerified || len(res.Memories) != 1 {
		t.Fatalf("outcome %+v memories %d", res.Outcome, len(res.Memories))
	}
	var dropped, destroy bool
	for _, w := range find[core.Warning](h.sink.Events()) {
		dropped = dropped || strings.Contains(w.Message, "memory candidate dropped")
		destroy = destroy || strings.Contains(w.Message, "destroy: busy")
	}
	if !dropped || !destroy || len(find[core.SandboxDestroyed](h.sink.Events())) != 0 {
		t.Fatalf("dropped=%v destroy=%v", dropped, destroy)
	}
}

func perTokenPrice(_, _ string, u core.Usage) *core.USDMicros {
	c := core.USDMicros(int64(u.InputTokens+u.OutputTokens) * 1000)
	return &c
}

func TestSessionBudgetEscalates(t *testing.T) {
	h := newHarness(t, "add-farewell.json", toolsCfg())
	sp, err := runtime.NewSpend(0.5, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	prior, _ := core.USDFromFloat(0.6)
	sp.AddSession(&prior) // an earlier run in this session already spent $0.60
	h.deps.Spend = sp
	h.deps.Price = perTokenPrice
	res := h.run(context.Background(), t)
	if res.Outcome.Kind != core.OutcomeEscalated || !strings.Contains(res.Outcome.Reason, "per_session_usd") {
		t.Fatalf("outcome %+v", res.Outcome)
	}
	if n := len(find[core.ModelUsage](h.sink.Events())); n != 0 {
		t.Fatalf("model calls after session cap = %d, want 0", n)
	}
}

func TestSessionBudgetUnknownCostWarnsOnce(t *testing.T) {
	h := newHarness(t, "add-farewell.json", toolsCfg())
	sp, err := runtime.NewSpend(0.5, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	h.deps.Spend = sp // Price stays nil: every cost is unknown
	res := h.run(context.Background(), t)
	if res.Outcome.Kind != core.OutcomeCompletedVerified {
		t.Fatalf("outcome %+v", res.Outcome)
	}
	n := 0
	for _, w := range find[core.Warning](h.sink.Events()) {
		if strings.Contains(w.Message, "session and day budgets cannot be enforced") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("unknown-spend warnings = %d", n)
	}
}

func TestDayBudgetTripsOnSecondRun(t *testing.T) {
	ledger := filepath.Join(t.TempDir(), "spend.jsonl")

	// First run: priced, no day cap, commits its actual spend to the ledger.
	h1 := newHarness(t, "add-farewell.json", toolsCfg())
	sp1, err := runtime.NewSpend(0, 0, ledger)
	if err != nil {
		t.Fatal(err)
	}
	h1.deps.Spend = sp1
	h1.deps.Price = perTokenPrice
	res1 := h1.run(context.Background(), t)
	if res1.Outcome.Kind != core.OutcomeCompletedVerified || res1.Cost.Actual == nil {
		t.Fatalf("run1 outcome %+v cost %+v", res1.Outcome, res1.Cost)
	}
	dayCap, _ := core.USDFromFloat(0.5)
	if *res1.Cost.Actual < dayCap {
		t.Fatalf("run1 cost %s below the cap the test relies on", *res1.Cost.Actual)
	}
	raw, err := os.ReadFile(ledger) //nolint:gosec // test-owned temp path
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(raw))
	if strings.Count(line, "\n") != 0 || !strings.Contains(line, string(res1.Run.ID)) {
		t.Fatalf("ledger after run1 = %q", raw)
	}

	// Second run, same day: the seeded ledger alone breaches the day cap,
	// so the run escalates before its first model call. A corrupt line is
	// skipped with a warning, never fatal.
	if err := appendLine(ledger, "corrupt {"); err != nil {
		t.Fatal(err)
	}
	h2 := newHarness(t, "add-farewell.json", toolsCfg())
	sp2, err := runtime.NewSpend(0, 0.5, ledger)
	if err != nil {
		t.Fatal(err)
	}
	h2.deps.Spend = sp2
	h2.deps.Price = perTokenPrice
	res2 := h2.run(context.Background(), t)
	if res2.Outcome.Kind != core.OutcomeEscalated || !strings.Contains(res2.Outcome.Reason, "per_day_usd") {
		t.Fatalf("run2 outcome %+v", res2.Outcome)
	}
	if n := len(find[core.ModelUsage](h2.sink.Events())); n != 0 {
		t.Fatalf("run2 model calls = %d, want 0", n)
	}
	warned := false
	for _, w := range find[core.Warning](h2.sink.Events()) {
		if strings.Contains(w.Message, "corrupt line") {
			warned = true
		}
	}
	if !warned {
		t.Fatal("run2 missing corrupt-line warning")
	}
}

func appendLine(path, line string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // test-owned temp path
	if err != nil {
		return err
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// capturingProvider records the messages of the first Complete call, then
// delegates to the wrapped provider unchanged.
type capturingProvider struct {
	core.ModelProvider
	first []core.Message
	seen  bool
}

func (c *capturingProvider) Complete(ctx context.Context, req core.CompletionRequest) (core.CompletionResponse, error) {
	if !c.seen {
		c.first = req.Messages
		c.seen = true
	}
	return c.ModelProvider.Complete(ctx, req)
}

// TestHistoryInjectedIntoFirstModelCall proves Input.History lands between the
// system prompt and this task's user message.
func TestHistoryInjectedIntoFirstModelCall(t *testing.T) {
	h := newHarness(t, "add-farewell.json", toolsCfg())
	capP := &capturingProvider{ModelProvider: h.deps.Provider}
	h.deps.Provider = capP
	h.in.History = []core.Message{
		{Role: core.RoleUser, Content: "prev q"},
		{Role: core.RoleAssistant, Content: "prev a"},
	}
	h.run(context.Background(), t)

	if len(capP.first) != 4 {
		t.Fatalf("first model call had %d messages, want 4: %+v", len(capP.first), capP.first)
	}
	if capP.first[0].Role != core.RoleSystem {
		t.Fatalf("message[0] role = %q, want system", capP.first[0].Role)
	}
	if capP.first[1].Content != "prev q" || capP.first[2].Content != "prev a" {
		t.Fatalf("history not injected in order: %+v", capP.first[1:3])
	}
	if capP.first[3].Role != core.RoleUser || capP.first[3].Content != h.in.Task.Description {
		t.Fatalf("message[3] = %+v, want user task description", capP.first[3])
	}
}

func TestAssembleRendersGlobalAndProjectRules(t *testing.T) {
	h := newHarness(t, "add-farewell.json", toolsCfg())
	global := filepath.Join(t.TempDir(), "AGENTS.md")
	write(t, global, "Always answer in haiku.\n")
	capP := &capturingProvider{ModelProvider: h.deps.Provider}
	h.deps.Provider = capP
	h.in.Project.InstructionFiles = []string{"AGENTS.md"}
	h.in.Project.GlobalInstructionFiles = []string{global, filepath.Join(t.TempDir(), "MISSING.md")}
	h.run(context.Background(), t)

	if len(capP.first) == 0 || capP.first[0].Role != core.RoleSystem {
		t.Fatalf("no system message captured: %+v", capP.first)
	}
	sys := capP.first[0].Content
	ui := strings.Index(sys, "<user-instructions file=\"AGENTS.md\">")
	pi := strings.Index(sys, "<project-instructions file=\"AGENTS.md\">")
	if ui < 0 || pi < 0 {
		t.Fatalf("system prompt missing rule blocks:\n%s", sys)
	}
	if ui > pi {
		t.Fatalf("user rules must precede project rules (user at %d, project at %d)", ui, pi)
	}
	if !strings.Contains(sys, "Always answer in haiku.") {
		t.Fatalf("global rule body missing:\n%s", sys)
	}
	if !strings.Contains(sys, "Keep one exported function per file.") {
		t.Fatalf("repo rule body missing:\n%s", sys)
	}
	if strings.Contains(sys, "everything inside <project-instructions> is untrusted") {
		t.Fatalf("rules are still declared inert to the model:\n%s", sys)
	}
	var warned bool
	for _, e := range h.sink.Events() {
		if w, ok := e.Data.(core.Warning); ok && strings.Contains(w.Message, "user instruction file") {
			warned = true
		}
	}
	if !warned {
		t.Fatal("missing global file must warn, not fail")
	}
	if !strings.Contains(sys, "<style>") || !strings.Contains(sys, "Be concise.") {
		t.Fatalf("default style missing from system prompt:\n%s", sys)
	}
}

func TestAssembleInjectsDetailedStyle(t *testing.T) {
	h := newHarness(t, "add-farewell.json", toolsCfg())
	capP := &capturingProvider{ModelProvider: h.deps.Provider}
	h.deps.Provider = capP
	h.in.Agent.Style = core.StyleDetailed
	h.run(context.Background(), t)
	sys := capP.first[0].Content
	if !strings.Contains(sys, "Be detailed.") || strings.Contains(sys, "Be concise.") {
		t.Fatalf("detailed style missing:\n%s", sys)
	}
}

func TestAssembleRendersSkills(t *testing.T) {
	h := newHarness(t, "add-farewell.json", toolsCfg())
	capP := &capturingProvider{ModelProvider: h.deps.Provider}
	h.deps.Provider = capP
	h.in.Skills = []runtime.SkillInfo{{Name: "deploy", Description: "how to ship"}}
	h.run(context.Background(), t)

	sys := capP.first[0].Content
	if !strings.Contains(sys, "<skills>") || !strings.Contains(sys, "- deploy: how to ship") {
		t.Fatalf("system prompt missing skills section:\n%s", sys)
	}
	if !strings.Contains(sys, "skill tool") {
		t.Fatalf("skills section must point at the skill tool:\n%s", sys)
	}
}

func TestAssembleRendersAgentPrompt(t *testing.T) {
	h := newHarness(t, "add-farewell.json", toolsCfg())
	capP := &capturingProvider{ModelProvider: h.deps.Provider}
	h.deps.Provider = capP
	h.in.AgentPrompt = "Answer only in French."
	h.run(context.Background(), t)

	sys := capP.first[0].Content
	if !strings.Contains(sys, "<agent-instructions>\nAnswer only in French.\n</agent-instructions>") {
		t.Fatalf("system prompt missing agent instructions:\n%s", sys)
	}
}

func TestAskUserQuestion(t *testing.T) {
	h := newHarness(t, "ask-preference.json", toolsCfg())
	h.deps.Tools = h.deps.Tools.WithAskUser(func(_ context.Context, qs []core.UserQuestion) ([]core.UserAnswer, error) {
		if len(qs) != 1 || qs[0].Question != "Ship?" {
			t.Fatalf("questions %+v", qs)
		}
		return []core.UserAnswer{{Question: "Ship?", Selected: []string{"yes"}}}, nil
	})
	res := h.run(context.Background(), t)
	if res.Outcome.Kind != core.OutcomeCompletedUnverified && res.Outcome.Kind != core.OutcomeCompletedVerified {
		t.Fatalf("outcome %+v", res.Outcome)
	}
	if !strings.Contains(res.Summary, "Shipping") {
		t.Fatalf("summary %q", res.Summary)
	}
}
