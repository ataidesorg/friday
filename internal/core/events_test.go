package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/ataidesorg/ink/internal/redact"
)

func sampleEvents() []Event {
	cost := USDMicros(250_000)
	task, run := NewTaskID(), NewRunID()
	from := InitialState()
	to, _ := from.Apply(Transition{Kind: TransitionAdvance})
	payloads := []EventData{
		TaskCreated{Description: "fix it", Project: NewProjectID(), Harness: HarnessCode},
		StateChanged{From: from, To: to, Transition: TransitionAdvance},
		ModelSelected{Route: "fast", Provider: "mock", Model: "m", Reason: "cheapest", EstimatedCost: &cost},
		ModelUsage{Provider: "mock", Model: "m", Usage: Usage{1, 2, 3}, Cost: CostReport{Actual: &cost}, Latency: time.Second},
		ContextAssembled{BudgetTokens: 100, UsedTokens: 40, Items: 3, Excluded: 1},
		ToolCalled{Call: NewToolCallID(), Tool: "read_file", Risk: RiskReadOnly, InputSummary: "go.mod"},
		ToolCompleted{Call: NewToolCallID(), Tool: "read_file", Success: true, Elapsed: time.Millisecond, OutputSummary: "12 lines"},
		SandboxCreated{Sandbox: NewSandboxID(), Provider: "local"},
		SandboxDestroyed{Sandbox: NewSandboxID()},
		PolicyDecided{Call: NewToolCallID(), Tool: "shell", Risk: RiskExecuteLocal, Effect: EffectRequireApproval, Rule: "exec", Reason: "execute_local needs approval"},
		ApprovalRequested{Approval: NewApprovalID(), Tool: "shell", Risk: RiskExecuteLocal, Justification: "run tests"},
		ApprovalResolved{Approval: NewApprovalID(), Decision: ApprovalApproved, By: user, Scope: ApprovalOnce},
		ValidationResult{Command: "go test ./...", Passed: true, ExitCode: 0, Elapsed: time.Second, Summary: "ok"},
		MemoryCandidateEvent{Candidate: NewCandidateID(), Category: MemoryProject, Status: CandidatePending},
		Warning{Message: "slow provider"},
		TaskFinished{Outcome: Outcome{Kind: OutcomeCompletedVerified}, Elapsed: time.Minute, Usage: Usage{10, 20, 0}, Cost: CostReport{Estimated: &cost, Actual: &cost}},
	}
	events := make([]Event, len(payloads))
	for i, p := range payloads {
		events[i] = NewEvent(task, run, uint64(i), t0.Add(time.Duration(i)*time.Second), p)
	}
	return events
}

func roundTrip(t *testing.T, e Event) Event {
	t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var out Event
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("%s: %v\n%s", e.Kind, err, b)
	}
	return out
}

func TestEventRoundTrip(t *testing.T) {
	events := sampleEvents()
	if len(events) != len(registry) {
		t.Fatalf("sample covers %d kinds, registry has %d", len(events), len(registry))
	}
	for _, e := range events {
		b, _ := json.Marshal(e)
		if !strings.Contains(string(b), `"schema_version":1`) {
			t.Errorf("%s: missing schema_version in %s", e.Kind, b)
		}
		if got := roundTrip(t, e); !reflect.DeepEqual(got, e) {
			t.Errorf("%s round trip mismatch:\n%s", e.Kind, b)
		}
	}
}

func TestEventDecodeRejects(t *testing.T) {
	var e Event
	err := json.Unmarshal([]byte(`{"schema_version":1,"kind":"bogus","data":{}}`), &e)
	if !errors.Is(err, ErrInvalidInput) || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("unknown kind: %v", err)
	}
	err = json.Unmarshal([]byte(`{"schema_version":2,"kind":"warning","data":{"message":"x"}}`), &e)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("schema version: %v", err)
	}
	if _, err := json.Marshal(Event{Kind: EventWarning}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("nil data: %v", err)
	}
	if _, err := json.Marshal(Event{Kind: EventWarning, Data: TaskCreated{}}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("kind mismatch: %v", err)
	}
}

func TestEventRedacted(t *testing.T) {
	literal := "hunter2hunter2"
	r := redact.New(literal)
	e := NewEvent(NewTaskID(), NewRunID(), 1, t0, ToolCalled{Call: NewToolCallID(), Tool: "shell", Risk: RiskExecuteLocal, InputSummary: "export GH=" + secret + " && echo " + literal})
	std, err := e.Redacted(r, PrivacyStandard)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(std)
	if s := string(b); strings.Contains(s, secret) || strings.Contains(s, literal) || !strings.Contains(s, "[REDACTED:") {
		t.Fatalf("standard mode leaked: %s", s)
	}
	if std.ID != e.ID || std.Seq != e.Seq || std.Kind != e.Kind || std.Data.(ToolCalled).Call != e.Data.(ToolCalled).Call {
		t.Fatal("redaction changed identity fields")
	}
	minimal := func(d EventData) EventData {
		out, err := NewEvent(NewTaskID(), NewRunID(), 1, t0, d).Redacted(r, PrivacyMinimal)
		if err != nil {
			t.Fatal(err)
		}
		return out.Data
	}
	if d := minimal(TaskCreated{Description: "x", Harness: HarnessCode}).(TaskCreated); d.Description != "" || d.Harness != HarnessCode {
		t.Errorf("minimal TaskCreated %+v", d)
	}
	if zero, err := NewEvent(NewTaskID(), NewRunID(), 1, t0, TaskCreated{Description: "x"}).Redacted(r, ""); err != nil || zero.Data.(TaskCreated).Description != "" {
		t.Errorf("empty mode must fail closed to minimal: %+v %v", zero, err)
	}
	if d := minimal(ApprovalRequested{Justification: "x", Risk: RiskDestructive}).(ApprovalRequested); d.Justification != "" || d.Risk != RiskDestructive {
		t.Errorf("minimal ApprovalRequested %+v", d)
	}
	if d := minimal(ValidationResult{Command: "c", Summary: "s", ExitCode: 3}).(ValidationResult); d.Command != "" || d.Summary != "" || d.ExitCode != 3 {
		t.Errorf("minimal ValidationResult %+v", d)
	}
	if d := minimal(Warning{Message: "m"}).(Warning); d.Message != "" {
		t.Errorf("minimal Warning %+v", d)
	}
	if _, err := e.Redacted(r, "loud"); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("unknown mode: %v", err)
	}
}

type brokenData struct{}

func (brokenData) Kind() EventKind              { return "broken" }
func (brokenData) MarshalJSON() ([]byte, error) { return nil, fmt.Errorf("boom") }

func TestRedactingSink(t *testing.T) {
	ctx := context.Background()
	inner := &MemorySink{}
	sink := RedactingSink{Inner: inner, Redactor: redact.New(), Mode: PrivacyStandard}
	good := NewEvent(NewTaskID(), NewRunID(), 1, t0, Warning{Message: "token " + secret})
	if err := sink.Emit(ctx, good); err != nil {
		t.Fatal(err)
	}
	bad := Event{SchemaVersion: EventSchemaVersion, ID: NewEventID(), At: t0, Seq: 2, Kind: "broken", Data: brokenData{}}
	if err := sink.Emit(ctx, bad); err == nil {
		t.Fatal("dropped event must surface an error")
	}
	got := inner.Events()
	if len(got) != 2 {
		t.Fatalf("want 2 events, got %d", len(got))
	}
	if msg := got[0].Data.(Warning).Message; strings.Contains(msg, secret) {
		t.Fatalf("forwarded raw secret: %s", msg)
	}
	w, ok := got[1].Data.(Warning)
	if !ok || got[1].Kind != EventWarning || !strings.Contains(w.Message, string(bad.ID)) || got[1].Seq != bad.Seq {
		t.Fatalf("expected drop warning, got %+v", got[1])
	}
	got[0].Seq = 99
	if inner.Events()[0].Seq == 99 {
		t.Fatal("Events() must return a copy")
	}
}

func TestRedactedLiteralNeverSurvives(t *testing.T) {
	f := func(seed int64) bool {
		rng := rand.New(rand.NewSource(seed))
		n := 8 + rng.Intn(8)
		var sb strings.Builder
		for i := 0; i < n; i++ {
			sb.WriteByte("abcdefghijklmnopqrstuvwxyz"[rng.Intn(26)])
		}
		lit := sb.String()
		e := NewEvent(NewTaskID(), NewRunID(), 1, t0, Warning{Message: "see " + lit + " now"})
		out, err := e.Redacted(redact.New(lit), PrivacyStandard)
		if err != nil {
			return false
		}
		b, _ := json.Marshal(out)
		return !strings.Contains(string(b), lit)
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 200}); err != nil {
		t.Fatal(err)
	}
}

func TestEventTrailReplay(t *testing.T) {
	ctx := context.Background()
	sink := &MemorySink{}
	task, run := NewTaskID(), NewRunID()
	state := InitialState()
	var seq uint64
	emit := func(d EventData) {
		seq++
		if err := sink.Emit(ctx, NewEvent(task, run, seq, t0.Add(time.Duration(len(sink.Events())+1)*time.Second), d)); err != nil {
			t.Fatal(err)
		}
	}
	emit(TaskCreated{Description: "replay me", Harness: HarnessCode})
	for i := 0; i < 10; i++ {
		kind := TransitionAdvance
		if state.Phase == PhaseTelemetryCapture {
			kind = TransitionComplete
		}
		next, err := state.Apply(Transition{Kind: kind})
		if err != nil {
			t.Fatal(err)
		}
		emit(StateChanged{From: state, To: next, Transition: kind})
		state = next
	}
	emit(TaskFinished{Outcome: *state.Outcome, Elapsed: 11 * time.Second})
	var lines []string
	for _, e := range sink.Events() {
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(b))
	}
	for i, line := range lines {
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatal(err)
		}
		if want := sink.Events()[i]; !reflect.DeepEqual(e, want) {
			t.Fatalf("event %d differs after replay:\n%s", i, line)
		}
	}
	if len(lines) != 12 || !state.Terminal() {
		t.Fatalf("trail has %d events, terminal=%v", len(lines), state.Terminal())
	}
}

func TestKnownEventKind(t *testing.T) {
	if !KnownEventKind(EventWarning) || KnownEventKind("bogus") {
		t.Fatal("KnownEventKind wrong")
	}
}
