package observability_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ataidesorg/friday/internal/core"
	"github.com/ataidesorg/friday/internal/observability"
	"github.com/ataidesorg/friday/internal/redact"
)

var (
	taskID = core.TaskID("01900000-0000-7000-8000-000000000001")
	runID  = core.RunID("01900000-0000-7000-8000-000000000002")
	t0     = time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
)

// secret is assembled from fragments so the repository never holds a
// secret-shaped literal.
var secret = "sk-" + strings.Repeat("a1b2", 10)

func fixtureEvents() []core.Event {
	cost := core.USDMicros(1234)
	payloads := []core.EventData{
		core.TaskCreated{Description: "add Farewell(name) with a test", Harness: core.HarnessCode},
		core.StateChanged{From: core.TaskState{Status: core.StatusActive, Phase: core.PhaseIntake}, To: core.TaskState{Status: core.StatusActive, Phase: core.PhasePreflight}, Transition: core.TransitionAdvance},
		core.SandboxCreated{Sandbox: "01900000-0000-7000-8000-0000000000aa", Provider: "process"},
		core.ContextAssembled{BudgetTokens: 8000, UsedTokens: 512, Items: 3, Excluded: 1},
		core.ModelSelected{Route: "default", Provider: "mock", Model: "scripted", Reason: "single provider configured"},
		core.ModelUsage{Provider: "mock", Model: "scripted", Usage: core.Usage{InputTokens: 100, OutputTokens: 20}, Cost: core.CostReport{Actual: &cost}, Latency: 15 * time.Millisecond},
		core.ToolCalled{Call: "01900000-0000-7000-8000-0000000000bb", Tool: "write_file", Risk: core.RiskWriteLocal, InputSummary: "farewell.go (120 bytes) token=" + secret},
		core.PolicyDecided{Call: "01900000-0000-7000-8000-0000000000bb", Tool: "write_file", Risk: core.RiskWriteLocal, Effect: core.EffectAllow, Rule: "tools.allow[write_file]", Reason: "listed"},
		core.ApprovalRequested{Approval: "01900000-0000-7000-8000-0000000000cc", Tool: "run_command", Risk: core.RiskExecuteLocal, Justification: "run the tests"},
		core.ApprovalResolved{Approval: "01900000-0000-7000-8000-0000000000cc", Decision: core.ApprovalApproved, By: core.Principal{Kind: core.PrincipalUser, Name: "lucas"}, Scope: core.ApprovalSession},
		core.ToolCompleted{Call: "01900000-0000-7000-8000-0000000000bb", Tool: "write_file", Success: true, Elapsed: 2 * time.Millisecond, OutputSummary: "wrote 120 bytes"},
		core.ValidationResult{Command: "go test ./...", Passed: true, ExitCode: 0, Elapsed: 1200 * time.Millisecond, Summary: "ok"},
		core.MemoryCandidateEvent{Candidate: "01900000-0000-7000-8000-0000000000dd", Category: core.MemoryProject, Status: core.CandidatePending},
		core.Warning{Message: strings.Repeat("long warning text ", 20)},
		core.SandboxDestroyed{Sandbox: "01900000-0000-7000-8000-0000000000aa"},
		core.TaskFinished{Outcome: core.Outcome{Kind: core.OutcomeCompletedVerified}, Elapsed: 3 * time.Second, Usage: core.Usage{InputTokens: 100, OutputTokens: 20}, Cost: core.CostReport{Actual: &cost}},
	}
	// Trails on disk are always redacted; the fixture mirrors that.
	r := redact.New(secret)
	out := make([]core.Event, 0, len(payloads))
	for i, p := range payloads {
		e := core.NewEvent(taskID, runID, uint64(i+1), t0.Add(time.Duration(i)*250*time.Millisecond), p)
		e.ID = core.EventID(fmt.Sprintf("01900000-0000-7000-8000-0000000000%02x", i))
		red, err := e.Redacted(r, core.PrivacyStandard)
		if err != nil {
			panic(err)
		}
		out = append(out, red)
	}
	return out
}

func TestRunDir(t *testing.T) {
	got := observability.RunDir("/p", runID)
	want := filepath.Join("/p", ".friday", "local", "runs", string(runID))
	if got != want {
		t.Fatalf("RunDir = %q, want %q", got, want)
	}
	if observability.TrailPath("/p", runID) != filepath.Join(want, "events.jsonl") {
		t.Fatalf("TrailPath = %q", observability.TrailPath("/p", runID))
	}
}

func TestRoundTripRedactedAndPrivate(t *testing.T) {
	root := t.TempDir()
	path := observability.TrailPath(root, runID)
	sink, closer, err := observability.NewRedactingJSONL(path, redact.New(secret), core.PrivacyStandard)
	if err != nil {
		t.Fatal(err)
	}
	var want []core.Event
	for i := 0; i < 100; i++ {
		e := core.NewEvent(taskID, runID, uint64(i+1), t0.Add(time.Duration(i)*time.Second), core.Warning{Message: "event " + secret})
		if err := sink.Emit(context.Background(), e); err != nil {
			t.Fatal(err)
		}
		want = append(want, e)
	}
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path) //nolint:gosec // test temp dir
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(secret)) {
		t.Fatal("secret literal written to the trail")
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("trail mode %v, want 0600", st.Mode().Perm())
	}
	if st, _ := os.Stat(filepath.Dir(path)); st.Mode().Perm() != 0o700 {
		t.Fatalf("run dir mode %v, want 0700", st.Mode().Perm())
	}
	got, err := observability.ReadTrail(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("read %d events, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i].ID || got[i].Seq != want[i].Seq || got[i].Kind != want[i].Kind || !got[i].At.Equal(want[i].At) {
			t.Fatalf("event %d envelope mismatch: %+v vs %+v", i, got[i], want[i])
		}
		if msg := got[i].Data.(core.Warning).Message; strings.Contains(msg, secret) || !strings.Contains(msg, "[REDACTED:") {
			t.Fatalf("event %d not redacted: %q", i, msg)
		}
	}
	// The same file reopened appends; the sink refuses use after Close.
	if err := sink.Emit(context.Background(), want[0]); err == nil {
		t.Fatal("emit after close succeeded")
	}
}

func TestOpenJSONLErrors(t *testing.T) {
	if _, _, err := observability.NewRedactingJSONL(filepath.Join(t.TempDir(), "x"), nil, core.PrivacyStandard); err == nil {
		t.Fatal("nil redactor accepted")
	}
	if _, _, err := observability.NewRedactingJSONL(filepath.Join(t.TempDir(), "x"), redact.New(), "loose"); err == nil {
		t.Fatal("unknown privacy mode accepted")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := observability.OpenJSONL(filepath.Join(file, "events.jsonl"), 0o600); err == nil {
		t.Fatal("parent is a file, open succeeded")
	}
	s, err := observability.OpenJSONL(filepath.Join(t.TempDir(), "a", "events.jsonl"), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Emit(context.Background(), core.Event{}); err == nil {
		t.Fatal("event without data written")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestReadTrailMalformed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	ev := fixtureEvents()[0]
	line, _ := json.Marshal(ev)
	content := string(line) + "\n" + string(line) + "\n" + string(line[:len(line)/2])
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := observability.ReadTrail(path)
	if err == nil || !strings.Contains(err.Error(), "line 3") {
		t.Fatalf("truncated line not reported with its number: %v", err)
	}
	if _, err := observability.ReadTrail(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("missing trail: %v", err)
	}
	if err := os.WriteFile(path, []byte("\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if evs, err := observability.ReadTrail(path); err != nil || len(evs) != 0 {
		t.Fatalf("blank lines: %v %d", err, len(evs))
	}
}

func TestTraceGolden(t *testing.T) {
	var buf bytes.Buffer
	if err := observability.Trace(&buf, fixtureEvents(), observability.TraceOptions{}); err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "trace.golden")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(golden, buf.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden) //nolint:gosec // test data
	if err != nil {
		t.Fatal(err)
	}
	if buf.String() != string(want) {
		t.Fatalf("trace output differs from golden:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), secret) {
		t.Fatal("trace printed a secret")
	}
	for _, line := range strings.Split(buf.String(), "\n") {
		if len([]rune(line)) > 200 {
			t.Fatalf("line too long: %q", line)
		}
	}
}

func TestTraceFilterAndJSON(t *testing.T) {
	var buf bytes.Buffer
	evs := fixtureEvents()
	if err := observability.Trace(&buf, evs, observability.TraceOptions{Kinds: []core.EventKind{core.EventToolCalled, core.EventTaskFinished}}); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(buf.String(), "\n"); n != 3 { // header + 2 rows
		t.Fatalf("filtered rows: %d lines:\n%s", n, buf.String())
	}
	buf.Reset()
	if err := observability.Trace(&buf, evs, observability.TraceOptions{JSON: true}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != len(evs) {
		t.Fatalf("json lines %d, want %d", len(lines), len(evs))
	}
	for i, l := range lines {
		var e core.Event
		if err := e.UnmarshalJSON([]byte(l)); err != nil {
			t.Fatalf("line %d: %v", i+1, err)
		}
		if e.ID != evs[i].ID {
			t.Fatalf("line %d id %s, want %s", i+1, e.ID, evs[i].ID)
		}
	}
	if err := observability.Trace(&buf, nil, observability.TraceOptions{Kinds: []core.EventKind{"bogus"}}); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("unknown kind: %v", err)
	}
	if err := observability.Trace(&buf, []core.Event{{}}, observability.TraceOptions{JSON: true}); err == nil {
		t.Fatal("event without data marshalled")
	}
}

func TestSummarizeTruncates(t *testing.T) {
	e := core.NewEvent(taskID, runID, 1, t0, core.Warning{Message: strings.Repeat("x", 500)})
	if s := observability.Summarize(e); len([]rune(s)) > 120 || !strings.HasSuffix(s, "…") {
		t.Fatalf("summary not truncated: %d runes", len([]rune(s)))
	}
	if s := observability.Summarize(core.Event{Kind: "mystery"}); s != "" {
		t.Fatalf("unknown payload summary %q", s)
	}
}

func TestSummarizeCostAndFailure(t *testing.T) {
	est := core.USDMicros(500)
	cases := map[string]core.EventData{
		"~$0.000500":               core.ModelUsage{Cost: core.CostReport{Estimated: &est}},
		"cost=n/a failure=timeout": core.TaskFinished{Outcome: core.Outcome{Kind: core.OutcomeFailed}, Failure: core.FailureTimeout},
		"sandbox short":            core.SandboxDestroyed{Sandbox: "short"},
	}
	for want, d := range cases {
		if s := observability.Summarize(core.NewEvent(taskID, runID, 1, t0, d)); !strings.Contains(s, want) {
			t.Errorf("Summarize(%T) = %q, want it to contain %q", d, s, want)
		}
	}
}

func TestReadTrailOversizedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), 17<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := observability.ReadTrail(path); err == nil || !strings.Contains(err.Error(), "read trail") {
		t.Fatalf("oversized line: %v", err)
	}
	if _, _, err := observability.NewRedactingJSONL(filepath.Join(path, "x"), redact.New(), core.PrivacyMinimal); err == nil {
		t.Fatal("open under a file succeeded")
	}
}

func TestEmitCancelledContext(t *testing.T) {
	s, err := observability.OpenJSONL(filepath.Join(t.TempDir(), "events.jsonl"), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Emit(ctx, fixtureEvents()[0]); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled emit: %v", err)
	}
}
