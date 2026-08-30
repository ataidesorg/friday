package core

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ataidesorg/ink/internal/redact"
)

var (
	t0     = time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	user   = Principal{Kind: PrincipalUser, Name: "lucas"}
	secret = "ghp_" + strings.Repeat("A", 36) // fragment-built; never a literal
)

func TestNewTask(t *testing.T) {
	cases := []struct {
		name string
		desc string
		ok   bool
	}{
		{"ok", "fix the bug", true},
		{"empty", "", false},
		{"whitespace", " \n\t", false},
		{"max", strings.Repeat("a", MaxDescriptionBytes), true},
		{"oversized", strings.Repeat("a", MaxDescriptionBytes+1), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			task, err := NewTask(c.desc, HarnessCode, NewProfileID(), NewSessionID(), user)
			if (err == nil) != c.ok {
				t.Fatalf("err=%v want ok=%v", err, c.ok)
			}
			if !c.ok && !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("want ErrInvalidInput, got %v", err)
			}
			if c.ok && !ValidID(string(task.ID)) {
				t.Fatalf("bad id %q", task.ID)
			}
		})
	}
}

func TestRunTransition(t *testing.T) {
	run := NewRun(NewTaskID(), 1, t0)
	if run.State != InitialState() || run.StartedAt != t0 || !run.FinishedAt.IsZero() {
		t.Fatalf("bad initial run %+v", run)
	}
	next, err := run.Transition(Transition{Kind: TransitionAdvance}, t0.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if next.State.Phase != Phases[1] || run.State.Phase != PhaseIntake {
		t.Fatalf("advance mutated receiver or failed: %+v / %+v", run, next)
	}
	if _, err := run.Transition(Transition{Kind: TransitionResolveApproval}, t0); !errors.Is(err, ErrNoPendingApproval) {
		t.Fatalf("want ErrNoPendingApproval, got %v", err)
	}
	done, err := next.Transition(Transition{Kind: TransitionFail, Reason: "boom", Category: FailureInternal}, t0.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !done.State.Terminal() || done.FinishedAt != t0.Add(time.Minute) {
		t.Fatalf("terminal run should stamp FinishedAt: %+v", done)
	}
	if got := done.Elapsed(t0.Add(time.Hour)); got != time.Minute {
		t.Fatalf("elapsed after finish = %v, want 1m", got)
	}
	if got := next.Elapsed(t0.Add(time.Hour)); got != time.Hour {
		t.Fatalf("elapsed while running = %v, want 1h", got)
	}
}

func TestNewMemoryCandidate(t *testing.T) {
	r := redact.New()
	prov := Provenance{Origin: OriginUserStated, By: user}
	c, err := NewMemoryCandidate("project", MemoryProject, "uses Go 1.27", prov, ConfidenceHigh, SensitivityInternal, r, t0)
	if err != nil || c.Status != CandidatePending || !ValidID(string(c.ID)) {
		t.Fatalf("good candidate failed: %+v %v", c, err)
	}
	cases := []struct {
		name    string
		ns, txt string
		sens    Sensitivity
		want    error
	}{
		{"empty ns", "", "x", SensitivityInternal, ErrInvalidInput},
		{"empty content", "ns", "  ", SensitivityInternal, ErrInvalidInput},
		{"secret sensitivity", "ns", "x", SensitivitySecret, ErrSecretContent},
		{"secret content", "ns", "token is " + secret, SensitivityInternal, ErrSecretContent},
	}
	for _, c := range cases {
		_, err := NewMemoryCandidate(c.ns, MemoryProject, c.txt, prov, ConfidenceLow, c.sens, r, t0)
		if !errors.Is(err, c.want) {
			t.Errorf("%s: want %v, got %v", c.name, c.want, err)
		}
	}
	if _, err := NewMemoryCandidate("ns", MemoryProject, "token is "+secret, prov, ConfidenceLow, SensitivityInternal, nil, t0); !errors.Is(err, ErrSecretContent) {
		t.Errorf("nil redactor must still refuse secrets, got %v", err)
	}
}

func TestRiskRequiresApproval(t *testing.T) {
	for _, r := range RiskClasses {
		if got := r.RequiresApprovalByDefault(); got != (r != RiskReadOnly) {
			t.Errorf("%s: RequiresApprovalByDefault=%v", r, got)
		}
	}
}

func TestSandboxSpec(t *testing.T) {
	r := redact.New()
	spec := NewSandboxSpec("/work")
	if spec.AllowSecretAccess || spec.Limits != DefaultResourceLimits() || spec.Source.Kind != SourceCopy {
		t.Fatalf("default spec not closed: %+v", spec)
	}
	if err := spec.Validate(r); err != nil {
		t.Fatal(err)
	}
	with := func(f func(*SandboxSpec)) SandboxSpec { s := spec; f(&s); return s }
	cases := []struct {
		name string
		s    SandboxSpec
		want error
	}{
		{"relative workdir", with(func(s *SandboxSpec) { s.WorkDir = "work" }), ErrInvalidInput},
		{"relative mount", with(func(s *SandboxSpec) { s.Mounts = []Mount{{Host: "/a", Guest: "b"}} }), ErrInvalidInput},
		{"zero limit", with(func(s *SandboxSpec) { s.Limits.CPUCores = 0 }), ErrInvalidInput},
		{"unknown source", with(func(s *SandboxSpec) { s.Source.Kind = "clone" }), ErrInvalidInput},
		{"negative wall clock", with(func(s *SandboxSpec) { s.WallClock = -1 }), ErrInvalidInput},
		{"secret env", with(func(s *SandboxSpec) { s.Env = map[string]string{"GH": secret} }), ErrSecretContent},
	}
	for _, c := range cases {
		if err := c.s.Validate(r); !errors.Is(err, c.want) {
			t.Errorf("%s: want %v, got %v", c.name, c.want, err)
		}
	}
	if err := cases[len(cases)-1].s.Validate(nil); !errors.Is(err, ErrSecretContent) {
		t.Errorf("nil redactor must still refuse secret env, got %v", err)
	}
	ok := with(func(s *SandboxSpec) {
		s.Mounts = []Mount{{Host: "/a", Guest: "/b"}}
		s.Env = map[string]string{"GOFLAGS": "-mod=mod"}
		s.Source = SandboxSource{Kind: SourceInPlace, Exclude: []string{"node_modules/**"}}
		s.WallClock = time.Hour
	})
	if err := ok.Validate(r); err != nil {
		t.Fatalf("valid allowlist spec rejected: %v", err)
	}
}

func TestPrivacyFallback(t *testing.T) {
	classes := []PrivacyClass{PrivacyLocal, PrivacyPrivateCloud, PrivacyPublicCloud}
	for i, from := range classes {
		for j, to := range classes {
			if got := from.AllowsFallbackTo(to); got != (j <= i) {
				t.Errorf("%s -> %s = %v", from, to, got)
			}
		}
	}
	if PrivacyLocal.AllowsFallbackTo("bogus") || PrivacyClass("bogus").AllowsFallbackTo(PrivacyLocal) {
		t.Error("unknown class must never be a fallback target")
	}
}

func TestCodeProfileDefaults(t *testing.T) {
	c := DefaultCodeProfile()
	if c.Style != StyleConcise || c.Posture != PostureStrict || c.Name != "default" {
		t.Fatalf("code %+v", c)
	}
}

func TestWithinSensitivityCap(t *testing.T) {
	if !WithinSensitivityCap("", SensitivityInternal) || WithinSensitivityCap("", SensitivitySecret) {
		t.Fatal("empty cap allows anything but secret")
	}
	if !WithinSensitivityCap(SensitivityPersonal, SensitivityPublic) || !WithinSensitivityCap(SensitivityPersonal, SensitivityPersonal) {
		t.Fatal("personal cap must allow public and personal")
	}
	if WithinSensitivityCap(SensitivityInternal, SensitivityPersonal) || WithinSensitivityCap(SensitivityPersonal, SensitivitySecret) {
		t.Fatal("cap must not leak upward")
	}
}

func TestUsageAddAndGate(t *testing.T) {
	sum := Usage{1, 2, 3}.Add(Usage{10, 20, 30})
	if sum != (Usage{11, 22, 33}) {
		t.Fatalf("Add = %+v", sum)
	}
	g := DefaultReleaseGate()
	if !g.RequiresHumanApproval || len(g.Checks) != 2 || g.Checks[0].Kind != GateNoRegression || g.Checks[1].Kind != GateAllTestsPass {
		t.Fatalf("DefaultReleaseGate = %+v", g)
	}
}

func TestDomainJSONRoundTrip(t *testing.T) {
	cost := USDMicros(1_500_000)
	values := []any{
		Task{ID: NewTaskID(), Description: "d", Harness: HarnessCode, CreatedBy: user, Budget: TaskBudget{MaxCost: cost}},
		MemoryCandidate{ID: NewCandidateID(), Namespace: "p", Category: MemorySemantic, Content: "c", Provenance: Provenance{Origin: OriginDeterministic, By: user}, Confidence: ConfidenceMedium, Sensitivity: SensitivityPublic, Status: CandidatePending, CreatedAt: t0},
		NewSandboxSpec("/w"),
		RouteDecision{Selected: ModelRoute{Name: "fast", Provider: "mock", Model: "m", Constraints: RouteConstraints{Privacy: PrivacyLocal}}, Alternatives: []RankedAlternative{{Route: "slow", Reason: "cost"}}, Reason: "cheapest", EstimatedCost: &cost},
		EvaluationResult{Scenario: NewScenarioID(), Run: NewRunID(), Passed: true, Checks: []CheckResult{{Expectation: Expectation{Kind: ExpectFileExists, Path: "go.mod"}, Passed: true}}, Usage: Usage{1, 2, 3}, Cost: CostReport{Actual: &cost}, Elapsed: time.Second, HarnessVersion: "0.0.1", Commit: "abc", Provider: "mock", Model: "m", Route: "fast"},
	}
	for _, v := range values {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		out := reflect.New(reflect.TypeOf(v))
		if err := json.Unmarshal(data, out.Interface()); err != nil {
			t.Fatal(err)
		}
		if got := out.Elem().Interface(); !reflect.DeepEqual(got, v) {
			t.Errorf("%T round trip mismatch:\n%s", v, data)
		}
	}
}
