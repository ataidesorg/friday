package observability

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ataidesorg/friday/internal/core"
)

// TraceOptions selects the output form and an optional kind filter.
type TraceOptions struct {
	JSON  bool
	Kinds []core.EventKind
}

// summaryRunes caps free text in a trace line.
const summaryRunes = 120

// Trace writes events as a table (seq, +elapsed since the first event,
// kind, summary) or, with JSON, as one raw line per event.
func Trace(w io.Writer, events []core.Event, opts TraceOptions) error {
	for _, k := range opts.Kinds {
		if !core.KnownEventKind(k) {
			return fmt.Errorf("%w: unknown event kind %q", core.ErrInvalidInput, k)
		}
	}
	keep := func(e core.Event) bool { return len(opts.Kinds) == 0 || slices.Contains(opts.Kinds, e.Kind) }
	if opts.JSON {
		enc := json.NewEncoder(w)
		for _, e := range events {
			if !keep(e) {
				continue
			}
			if err := enc.Encode(e); err != nil {
				return err
			}
		}
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SEQ\tELAPSED\tKIND\tSUMMARY")
	var start time.Time
	for i, e := range events {
		if i == 0 {
			start = e.At
		}
		if !keep(e) {
			continue
		}
		fmt.Fprintf(tw, "%d\t+%s\t%s\t%s\n", e.Seq, e.At.Sub(start).Round(time.Millisecond), e.Kind, Summarize(e))
	}
	return tw.Flush()
}

// Summarize renders one line per payload kind; free text is cut at 120
// runes. Unknown payloads summarise to "".
func Summarize(e core.Event) string {
	var s string
	switch d := e.Data.(type) {
	case core.TaskCreated:
		s = fmt.Sprintf("%s harness: %s", d.Harness, d.Description)
	case core.StateChanged:
		s = fmt.Sprintf("%s: %s/%s → %s/%s", d.Transition, d.From.Status, d.From.Phase, d.To.Status, d.To.Phase)
	case core.ModelSelected:
		s = fmt.Sprintf("%s/%s via %s: %s", d.Provider, d.Model, d.Route, d.Reason)
	case core.ModelUsage:
		s = fmt.Sprintf("%s/%s in=%d out=%d cached=%d cost=%s latency=%s", d.Provider, d.Model, d.Usage.InputTokens, d.Usage.OutputTokens, d.Usage.CachedInputTokens, cost(d.Cost), d.Latency)
	case core.ContextAssembled:
		s = fmt.Sprintf("%d/%d tokens, %d items, %d excluded", d.UsedTokens, d.BudgetTokens, d.Items, d.Excluded)
	case core.ToolCalled:
		s = fmt.Sprintf("%s (%s) call=%s: %s", d.Tool, d.Risk, short(d.Call), d.InputSummary)
	case core.ToolCompleted:
		s = fmt.Sprintf("%s call=%s ok=%t in %s: %s", d.Tool, short(d.Call), d.Success, d.Elapsed, d.OutputSummary)
	case core.SandboxCreated:
		s = fmt.Sprintf("%s sandbox %s", d.Provider, short(d.Sandbox))
	case core.SandboxDestroyed:
		s = "sandbox " + short(d.Sandbox)
	case core.PolicyDecided:
		s = fmt.Sprintf("%s %s (%s) by %s: %s", d.Effect, d.Tool, d.Risk, d.Rule, d.Reason)
	case core.ApprovalRequested:
		s = fmt.Sprintf("%s (%s) approval=%s: %s", d.Tool, d.Risk, short(d.Approval), d.Justification)
	case core.ApprovalResolved:
		s = fmt.Sprintf("%s by %s %s, scope %s, approval=%s", d.Decision, d.By.Kind, d.By.Name, d.Scope, short(d.Approval))
	case core.ValidationResult:
		s = fmt.Sprintf("%q passed=%t exit=%d in %s: %s", d.Command, d.Passed, d.ExitCode, d.Elapsed, d.Summary)
	case core.MemoryCandidateEvent:
		s = fmt.Sprintf("%s %s candidate=%s", d.Category, d.Status, short(d.Candidate))
	case core.Warning:
		s = d.Message
	case core.TaskFinished:
		s = fmt.Sprintf("%s in %s, in=%d out=%d cost=%s", d.Outcome.Kind, d.Elapsed, d.Usage.InputTokens, d.Usage.OutputTokens, cost(d.Cost))
		if d.Failure != "" {
			s += " failure=" + string(d.Failure)
		}
	}
	s = strings.ReplaceAll(s, "\n", " ")
	if r := []rune(s); len(r) > summaryRunes {
		return string(r[:summaryRunes-1]) + "…"
	}
	return s
}

func cost(c core.CostReport) string {
	switch {
	case c.Actual != nil:
		return c.Actual.String()
	case c.Estimated != nil:
		return "~" + c.Estimated.String()
	}
	return "n/a"
}

// short keeps the tail of an ID: enough to grep, short enough to scan.
func short[T ~string](id T) string {
	if len(id) > 8 {
		return string(id[len(id)-8:])
	}
	return string(id)
}
