package core

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestNewIDsAreDistinctAndValid(t *testing.T) {
	a, b := NewTaskID(), NewTaskID()
	if a == b {
		t.Fatalf("two generated ids are equal: %s", a)
	}
	for _, id := range []string{string(a), string(b), string(NewRunID()), string(NewEventID())} {
		if !ValidID(id) {
			t.Errorf("ValidID(%q) = false", id)
		}
	}
}

func TestValidIDRejectsGarbage(t *testing.T) {
	for _, s := range []string{"", "abc", "0190f4f0-0000-7000-8000-00000000000", "{0190f4f0-0000-7000-8000-000000000000}"} {
		if ValidID(s) {
			t.Errorf("ValidID(%q) = true", s)
		}
	}
}

func TestIDsSortByCreationOrder(t *testing.T) {
	ids := []string{string(NewTaskID()), string(NewTaskID()), string(NewTaskID())}
	if !slices.IsSorted(ids) {
		t.Fatalf("uuidv7 ids not in creation order: %v", ids)
	}
}

func TestIDJSONRoundTrip(t *testing.T) {
	in := struct {
		Task TaskID `json:"task"`
		Run  RunID  `json:"run"`
	}{NewTaskID(), NewRunID()}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Task TaskID `json:"task"`
		Run  RunID  `json:"run"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Fatalf("round trip mismatch: %+v != %+v", out, in)
	}
}

func TestEveryConstructorProducesValidID(t *testing.T) {
	ids := []string{
		string(NewTaskID()), string(NewRunID()), string(NewSessionID()), string(NewProfileID()),
		string(NewProjectID()), string(NewWorkspaceID()), string(NewSandboxID()), string(NewToolCallID()),
		string(NewApprovalID()), string(NewCandidateID()), string(NewScenarioID()),
		string(NewEventID()), string(NewGateID()), string(NewPolicyID()),
		string(NewGoalID()),
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !ValidID(id) || seen[id] {
			t.Errorf("bad or duplicate id %q", id)
		}
		seen[id] = true
	}
}
