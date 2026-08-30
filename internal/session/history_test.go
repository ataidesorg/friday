package session

import (
	"strings"
	"testing"

	"github.com/ataidesorg/ink/internal/core"
)

func TestHistoryUnboundedPreservesOrderAndRoles(t *testing.T) {
	turns := []Turn{
		{Role: core.RoleUser, Text: "one"},
		{Role: core.RoleAssistant, Text: "two"},
		{Role: core.RoleUser, Text: "three"},
	}
	got := History(turns, 0)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[0].Role != core.RoleUser || got[0].Content != "one" {
		t.Fatalf("first = %+v", got[0])
	}
	if got[1].Role != core.RoleAssistant || got[2].Content != "three" {
		t.Fatalf("mapping = %+v", got)
	}
}

func TestHistoryDropsOldestToFitBudget(t *testing.T) {
	turns := []Turn{
		{Role: core.RoleUser, Text: "aaaa"},      // 4
		{Role: core.RoleAssistant, Text: "bbbb"}, // 4
		{Role: core.RoleUser, Text: "cccc"},      // 4
	}
	// Budget 8 fits only the two newest (total 8); the oldest is dropped.
	got := History(turns, 8)
	if len(got) != 2 || got[0].Content != "bbbb" || got[1].Content != "cccc" {
		t.Fatalf("got = %+v, want [bbbb cccc]", got)
	}
}

func TestHistorySingleTurnOverBudgetIsDropped(t *testing.T) {
	turns := []Turn{{Role: core.RoleUser, Text: "way too long"}}
	if got := History(turns, 3); len(got) != 0 {
		t.Fatalf("got = %+v, want empty", got)
	}
}

func TestHistoryEmpty(t *testing.T) {
	if got := History(nil, 100); len(got) != 0 {
		t.Fatalf("got = %+v, want empty", got)
	}
}

func TestHistoryDropsOrphanUserTurn(t *testing.T) {
	// A crash between the user and assistant appends can leave two consecutive
	// user turns; History must keep only the later one so resume stays strictly
	// alternating (a repeated user role makes some providers 400).
	turns := []Turn{
		{Role: core.RoleUser, Text: "aborted"},
		{Role: core.RoleUser, Text: "retry"},
		{Role: core.RoleAssistant, Text: "reply"},
	}
	got := History(turns, 0)
	if len(got) != 2 || got[0].Role != core.RoleUser || got[0].Content != "retry" || got[1].Role != core.RoleAssistant {
		t.Fatalf("got = %+v, want [user:retry assistant:reply]", got)
	}
}

func TestHistorySummaryBarrier(t *testing.T) {
	turns := []Turn{
		{Role: core.RoleUser, Text: "old question"},
		{Role: core.RoleAssistant, Text: "old answer"},
		{Role: core.RoleAssistant, Kind: KindSummary, Text: "we discussed X"},
		{Role: core.RoleUser, Text: "new question"},
		{Role: core.RoleAssistant, Text: "new answer"},
	}
	got := History(turns, 0)
	if len(got) != 3 {
		t.Fatalf("want summary+2 turns, got %d: %+v", len(got), got)
	}
	if got[0].Role != core.RoleSystem || !strings.Contains(got[0].Content, "we discussed X") {
		t.Fatalf("summary not leading system message: %+v", got[0])
	}
	if got[1].Content != "new question" {
		t.Fatalf("pre-summary turns leaked: %+v", got)
	}
	// The char budget never evicts the summary itself.
	got = History(turns, 5)
	if len(got) == 0 || got[0].Role != core.RoleSystem {
		t.Fatalf("tiny budget dropped the summary: %+v", got)
	}
}
