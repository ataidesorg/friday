package session

import "github.com/ataidesorg/ink/internal/core"

// History projects stored turns into the prior-context messages that seed the
// next run. Whole turns are dropped from the oldest end until the total text
// length is within budgetChars; the newest turns are always kept. A
// budgetChars of 0 or less means no bound. The bound is on characters,
// matching the runtime's bytes/4 token estimate — the caller passes the
// context budget so a long conversation never overruns the model window.
func History(turns []Turn, budgetChars int) []core.Message {
	head, turns := splitSummary(turns)
	turns = dropOrphans(turns)
	start := 0
	if budgetChars > 0 {
		total := 0
		for _, t := range turns {
			total += len(t.Text)
		}
		for start < len(turns) && total > budgetChars {
			total -= len(turns[start].Text)
			start++
		}
	}
	out := make([]core.Message, 0, len(turns)-start+len(head))
	out = append(out, head...)
	for _, t := range turns[start:] {
		out = append(out, core.Message{Role: t.Role, Content: t.Text})
	}
	return out
}

// KindSummary marks a turn holding a compacted summary of everything before
// it. History replays only the latest summary plus the turns after it, so a
// compacted session resumes cheap without rewriting the append-only log.
const KindSummary = "summary"

// splitSummary cuts the transcript at the newest summary turn: the summary
// becomes a leading system message and only later turns remain. The summary
// is never dropped by the char budget — it is the compression.
func splitSummary(turns []Turn) ([]core.Message, []Turn) {
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].Kind == KindSummary {
			head := core.Message{Role: core.RoleSystem, Content: "Summary of the earlier conversation:\n" + turns[i].Text}
			return []core.Message{head}, turns[i+1:]
		}
	}
	return nil, turns
}

// dropOrphans removes any turn whose successor repeats its role. A successful
// turn always persists a user+assistant pair, so consecutive same-role turns
// only arise from a partial write (a crash between the two appends); keeping
// the later of each such run preserves strict alternation on resume.
func dropOrphans(turns []Turn) []Turn {
	kept := make([]Turn, 0, len(turns))
	for i, t := range turns {
		if i+1 < len(turns) && turns[i+1].Role == t.Role {
			continue
		}
		kept = append(kept, t)
	}
	return kept
}
