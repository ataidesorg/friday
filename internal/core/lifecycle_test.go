package core

import (
	"encoding/json"
	"errors"
	"testing"
)

var allKinds = []TransitionKind{
	TransitionAdvance, TransitionRevise, TransitionRequestApproval, TransitionResolveApproval,
	TransitionComplete, TransitionEscalate, TransitionRollback, TransitionFail,
}

func activeAt(p Phase) TaskState { return TaskState{Status: StatusActive, Phase: p} }

// expectedActive is the 10 phases × 8 kinds matrix: "" means success,
// otherwise the sentinel error expected.
func expectedActive(p Phase, k TransitionKind) error {
	switch k {
	case TransitionAdvance:
		if p == PhaseTelemetryCapture {
			return ErrTransitionNotAllowed
		}
	case TransitionRevise:
		if p != PhaseToolExecution && p != PhaseValidation {
			return ErrTransitionNotAllowed
		}
	case TransitionRequestApproval:
		if p != PhasePreflight && p != PhaseToolExecution {
			return ErrTransitionNotAllowed
		}
	case TransitionResolveApproval:
		return ErrNoPendingApproval
	case TransitionComplete:
		if p != PhaseTelemetryCapture {
			return ErrTransitionNotAllowed
		}
	case TransitionRollback:
		if p.Index() < PhaseToolExecution.Index() {
			return ErrTransitionNotAllowed
		}
	}
	return nil
}

func TestLifecycleMatrix(t *testing.T) {
	cells := 0
	for _, p := range Phases {
		for _, k := range allKinds {
			cells++
			before := activeAt(p)
			after, err := before.Apply(Transition{Kind: k, Verified: true})
			want := expectedActive(p, k)
			if want == nil && err != nil {
				t.Errorf("%s@%s: unexpected error %v", k, p, err)
			}
			if want != nil && !errors.Is(err, want) {
				t.Errorf("%s@%s: err = %v, want %v", k, p, err, want)
			}
			if before != activeAt(p) {
				t.Errorf("%s@%s: Apply mutated receiver", k, p)
			}
			if err != nil && after != before {
				t.Errorf("%s@%s: failed Apply returned changed state", k, p)
			}
		}
	}
	if cells != 80 {
		t.Fatalf("matrix has %d cells, want 80", cells)
	}
}

func TestLifecycleHappyPath(t *testing.T) {
	s := InitialState()
	for i := 0; i < len(Phases)-1; i++ {
		var err error
		if s, err = s.Apply(Transition{Kind: TransitionAdvance}); err != nil {
			t.Fatalf("advance %d: %v", i, err)
		}
	}
	if s.Phase != PhaseTelemetryCapture || s.Terminal() {
		t.Fatalf("after 9 advances: %+v", s)
	}
	done, err := s.Apply(Transition{Kind: TransitionComplete, Verified: true})
	if err != nil || !done.Terminal() || done.Outcome.Kind != OutcomeCompletedVerified {
		t.Fatalf("complete verified: %+v %v", done, err)
	}
	unverified, _ := s.Apply(Transition{Kind: TransitionComplete, Reason: "no tests ran"})
	if unverified.Outcome.Kind != OutcomeCompletedUnverified || unverified.Outcome.Reason != "no tests ran" {
		t.Fatalf("complete unverified: %+v", unverified.Outcome)
	}
}

func TestLifecycleApprovalRoundTrip(t *testing.T) {
	id := NewApprovalID()
	waiting, err := activeAt(PhaseToolExecution).Apply(Transition{Kind: TransitionRequestApproval, Approval: id})
	if err != nil || waiting.Status != StatusAwaitingApproval || waiting.Resume != PhaseToolExecution || waiting.Approval != id {
		t.Fatalf("request approval: %+v %v", waiting, err)
	}
	if _, err := waiting.Apply(Transition{Kind: TransitionAdvance}); !errors.Is(err, ErrApprovalPending) {
		t.Fatalf("advance while awaiting: %v", err)
	}
	resumed, err := waiting.Apply(Transition{Kind: TransitionResolveApproval})
	if err != nil || resumed != activeAt(PhaseToolExecution) {
		t.Fatalf("resolve: %+v %v", resumed, err)
	}
	for _, k := range []TransitionKind{TransitionEscalate, TransitionFail} {
		if got, err := waiting.Apply(Transition{Kind: k}); err != nil || !got.Terminal() {
			t.Errorf("%s while awaiting: %+v %v", k, got, err)
		}
	}
}

func TestLifecycleDoneIsTerminal(t *testing.T) {
	done, _ := activeAt(PhasePlanning).Apply(Transition{Kind: TransitionFail, Category: FailureToolError, Message: "boom"})
	if done.Outcome.Category != FailureToolError || done.Outcome.Reason != "boom" {
		t.Fatalf("fail outcome: %+v", done.Outcome)
	}
	for _, k := range allKinds {
		if _, err := done.Apply(Transition{Kind: k}); !errors.Is(err, ErrAlreadyDone) {
			t.Errorf("%s from done: %v", k, err)
		}
	}
	if _, err := activeAt(PhasePlanning).Apply(Transition{Kind: "teleport"}); !errors.Is(err, ErrTransitionNotAllowed) {
		t.Errorf("unknown kind: %v", err)
	}
}

func TestPhaseHelpers(t *testing.T) {
	if Phase("nope").Index() != -1 {
		t.Error("unknown phase index")
	}
	if _, ok := Phase("nope").Next(); ok {
		t.Error("unknown phase has next")
	}
	if n, ok := PhaseIntake.Next(); !ok || n != PhasePreflight {
		t.Errorf("intake.Next = %s %v", n, ok)
	}
}

func TestTaskStateJSONRoundTrip(t *testing.T) {
	in, _ := activeAt(PhaseValidation).Apply(Transition{Kind: TransitionRollback, Reason: "tests failed"})
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out TaskState
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != in.Status || out.Phase != in.Phase || *out.Outcome != *in.Outcome {
		t.Fatalf("round trip: %+v != %+v", out, in)
	}
}
