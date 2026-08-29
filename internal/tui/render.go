package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/ataidesorg/friday/internal/core"
	"github.com/ataidesorg/friday/internal/runtime"
)

// Line tags; every rendered line starts with one so plain output greps.
const (
	tagPhase    = "[phase]"
	tagTask     = "[task]"
	tagRoute    = "[route]"
	tagContext  = "[context]"
	tagUsage    = "[usage]"
	tagTool     = "[tool]"
	tagSandbox  = "[sandbox]"
	tagPolicy   = "[policy]"
	tagApproval = "[approval]"
	tagValidate = "[validate]"
	tagMemory   = "[memory]"
	tagWarn     = "[warn]"
	tagModel    = "[model]"
	tagOutcome  = "[outcome]"
	tagCost     = "[cost]"
	tagSummary  = "[summary]"
	tagUser     = "[you]"
	tagReply    = "[friday]"
	tagStatus   = "[status]"
	tagDiff     = "[diff]"
	tagToolOut  = "[tool-out]"
	// Synthetic style keys for deny/ok/failed colouring.
	tagDeny   = "deny"
	tagOK     = "ok"
	tagFailed = "failed"
)

const (
	// maxArgRunes caps tool arguments and other untrusted text per line.
	maxArgRunes   = 200
	maxToolOut    = 32 * 1024
	maxDiffLines  = 200
	truncatedMark = "(… truncated)"
)

// clip collapses whitespace and caps untrusted text, marking the cut.
func clip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= maxArgRunes {
		return s
	}
	return string(r[:maxArgRunes]) + " " + truncatedMark
}

func money(p *core.USDMicros) string {
	if p == nil {
		return "-"
	}
	return p.String()
}

func phaseLine(p core.Phase) string { return tagPhase + " " + string(p) }

func deltaLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	var out []string
	for _, l := range strings.Split(s, "\n") {
		out = append(out, tagModel+" "+strings.TrimRight(l, "\r"))
	}
	return out
}

func eventLines(e core.Event) []string {
	switch d := e.Data.(type) {
	case core.TaskCreated:
		return []string{tagTask + " " + clip(d.Description)}
	case core.ModelSelected:
		return []string{fmt.Sprintf("%s %s via %s/%s (%s)", tagRoute, d.Route, d.Provider, d.Model, clip(d.Reason))}
	case core.ContextAssembled:
		return []string{fmt.Sprintf("%s %d items, %d excluded, %d/%d tokens", tagContext, d.Items, d.Excluded, d.UsedTokens, d.BudgetTokens)}
	case core.ModelUsage:
		return []string{fmt.Sprintf("%s %s/%s in %d out %d cached %d cost %s latency %s", tagUsage, d.Provider, d.Model, d.Usage.InputTokens, d.Usage.OutputTokens, d.Usage.CachedInputTokens, money(d.Cost.Actual), d.Latency.Round(time.Millisecond))}
	case core.ToolCalled:
		return []string{fmt.Sprintf("%s %s %s %s", tagTool, d.Tool, d.Risk, clip(d.InputSummary))}
	case core.ToolCompleted:
		status := tagOK
		if !d.Success {
			status = tagFailed
		}
		return []string{fmt.Sprintf("%s %s %s in %s: %s", tagTool, d.Tool, status, d.Elapsed.Round(time.Millisecond), clip(d.OutputSummary))}
	case core.SandboxCreated:
		return []string{fmt.Sprintf("%s %s created by %s", tagSandbox, d.Sandbox, d.Provider)}
	case core.SandboxDestroyed:
		return []string{fmt.Sprintf("%s %s destroyed", tagSandbox, d.Sandbox)}
	case core.PolicyDecided:
		return []string{fmt.Sprintf("%s %s %s %s (%s): %s", tagPolicy, d.Effect, d.Tool, d.Risk, d.Rule, clip(d.Reason))}
	case core.ApprovalRequested:
		return []string{fmt.Sprintf("%s requested for %s %s: %s", tagApproval, d.Tool, d.Risk, clip(d.Justification))}
	case core.ApprovalResolved:
		return []string{fmt.Sprintf("%s %s by %s/%s (%s)", tagApproval, d.Decision, d.By.Kind, d.By.Name, d.Scope)}
	case core.ValidationResult:
		status := "passed"
		if !d.Passed {
			status = "failed"
		}
		return []string{fmt.Sprintf("%s %s %s exit %d in %s: %s", tagValidate, status, clip(d.Command), d.ExitCode, d.Elapsed.Round(time.Millisecond), clip(d.Summary))}
	case core.MemoryCandidateEvent:
		return []string{fmt.Sprintf("%s %s candidate %s %s", tagMemory, d.Category, d.Candidate, d.Status)}
	case core.Warning:
		return []string{tagWarn + " " + clip(d.Message)}
	case core.StateChanged, core.TaskFinished:
		return nil // phase lines and the outcome already show these
	}
	return []string{"[" + string(e.Kind) + "]"}
}

// approvalLine is both the logged prompt and the footer prompt.
func approvalLine(a core.Approval) string {
	r := a.Request
	s := fmt.Sprintf("%s %s %s", tagApproval, r.Tool, r.Capability.Risk)
	switch sc := r.Capability.Scope; {
	case sc.Path != "":
		s += " " + clip(sc.Path)
	case len(sc.Argv) > 0:
		s += " " + clip(strings.Join(sc.Argv, " "))
	case sc.Host != "":
		s += " " + clip(sc.Host)
	case sc.Name != "":
		s += " " + clip(sc.Name)
	}
	if r.Justification != "" {
		s += ": " + clip(r.Justification)
	}
	return s + " — [y] once  [s] session  [n] deny"
}

func outcomeLine(o core.Outcome) string {
	s := tagOutcome + " " + string(o.Kind)
	if o.Category != "" {
		s += " " + string(o.Category)
	}
	if o.Reason != "" {
		s += ": " + clip(o.Reason)
	}
	return s
}

func doneLines(r runtime.Result, diff string) []string {
	lines := []string{outcomeLine(r.Outcome), fmt.Sprintf("%s %s, tokens in %d out %d, %d events", tagCost, money(r.Cost.Actual), r.Usage.InputTokens, r.Usage.OutputTokens, r.Events)}
	for _, l := range deltaLines(r.Summary) {
		lines = append(lines, tagSummary+strings.TrimPrefix(l, tagModel))
	}
	diffLines := strings.Split(strings.TrimRight(diff, "\n"), "\n")
	if diff == "" {
		diffLines = nil
	}
	for i, l := range diffLines {
		if i == maxDiffLines {
			lines = append(lines, tagDiff+" "+truncatedMark)
			break
		}
		lines = append(lines, tagDiff+" "+l)
	}
	return lines
}

// helpLines lists the chat slash commands and the effective key bindings.
func helpLines(km Keymap) []string {
	return []string{
		tagStatus + " FRIDAY(1)",
		"  Local coding-agent chat for this repository.",
		"",
		"USAGE",
		"  Type a prompt and press enter. While Friday is working, enter queues the",
		"  draft as the next prompt above the text box.",
		"",
		"SESSION",
		"  /home              show the welcome surface",
		"  /new               start a fresh session after confirmation",
		"  /resume            reopen a previous session",
		"  /status            show session id, route, agent, tokens, and mode",
		"  /goal              start, pause, resume, edit, or clear the session goal",
		"  /rewind            drop later turns from the transcript",
		"  /fork              branch a new session from this transcript",
		"  /compact           summarize older turns into replayable context",
		"  /clear             empty the scrollback; the session keeps its history",
		"  /delete            delete this session after typed confirmation",
		"",
		"MODEL AND AGENT",
		"  /model             pick the active provider/model route",
		"  /model NAME        switch directly to a configured route",
		"  /agent             pick the active agent profile",
		"  /agent NAME        switch directly to that profile; \"none\" clears it",
		"  /dashboard         open the live agent roster (ctrl+\\)",
		"",
		"MODES",
		"  shift+tab          cycle: normal, plan, auto, always-approve, always-ask",
		"  /plan              switch to read-only planning mode",
		"  /always-approve    auto-approve remaining asks this session",
		"  /usage             toggle context and configured spend limits by the model",
		"  /tools             show or hide tool calls in the transcript",
		"  /thinking          show or hide the live thinking indicator",
		"  /verbose           show the full event stream",
		"",
		"EXTENSIONS",
		"  /skills            inspect and invoke loaded skills",
		"  /connect           add a provider credential",
		"  Skill              reusable instructions the agent can follow",
		"  Command            saved prompt you run with /name",
		"",
		"INPUT",
		"  /multiline         toggle enter-for-newline mode",
		"  /vim-mode          use j/k in scrollback focus",
		"  /copy              copy the last assistant reply",
		"  /export            copy the transcript",
		"  /history           recall a prompt you already sent",
		"",
		"DISPLAY",
		"  /theme             switch the palette",
		"  /timestamps        show or hide per-line times",
		"",
		"KEYS",
		"  " + km.Palette.String() + "          command palette",
		"  ctrl+b             queued prompts",
		"  ctrl+g             edit the current draft in $VISUAL",
		"  " + km.ScrollUp.String() + "/" + km.ScrollDown.String() + "       scroll the conversation",
		"  esc                cancel a running turn; twice clears an empty draft",
		"  ctrl+c, ctrl+q     press twice to quit Friday",
		"",
		"DIAGNOSTICS",
		"  /doctor            terminal and clipboard checks",
		"  /cost              session token and cost totals",
		"  /quit              leave Friday (/exit works too)",
	}
}

// costLines renders the running session usage and cost totals.
func costLines(u core.Usage, c core.CostReport) []string {
	return []string{
		fmt.Sprintf("%s in %d · out %d · cached %d", tagUsage, u.InputTokens, u.OutputTokens, u.CachedInputTokens),
		fmt.Sprintf("%s actual %s · est %s", tagCost, money(c.Actual), money(c.Estimated)),
	}
}

// modelLines shows the active route and the routes /model can switch to. The
// active route carries a filled marker; switching is live via /model NAME.
func modelLines(route string, routes []RouteInfo, active string) []string {
	if route == "" {
		route = "(unknown)"
	}
	out := []string{tagModel + " route: " + route}
	for _, r := range routes {
		marker := "○"
		if r.Name == active {
			marker = "●"
		}
		out = append(out, fmt.Sprintf("  %s %s  %s/%s", marker, r.Name, r.Provider, r.Model))
	}
	if len(routes) > 1 {
		out = append(out, tagModel+" switch with /model NAME")
	}
	return out
}

// chatEventLines picks what the chat scrollback shows from a turn's event
// stream. A chat is a conversation, not a trace: tool activity, warnings,
// policy/approval decisions, and validation results always show; the
// accounting and lifecycle events (usage, route, context, sandbox, task,
// memory) show only in verbose mode — /cost and the status bar carry the
// totals.
func chatEventLines(e core.Event, verbose, showTools, hideAdvisories bool) []string {
	if w, ok := e.Data.(core.Warning); ok && w.Advisory && hideAdvisories {
		return nil
	}
	if verbose {
		if !showTools {
			switch e.Data.(type) {
			case core.ToolCalled, core.ToolCompleted:
				return nil
			}
		}
		return eventLines(e)
	}
	switch d := e.Data.(type) {
	case core.ToolCalled:
		if !showTools {
			return nil
		}
		return []string{fmt.Sprintf("%s %s %s", tagTool, d.Tool, clip(d.InputSummary))}
	case core.ToolCompleted:
		if !showTools {
			return nil
		}
		status := "ok"
		if !d.Success {
			status = "failed"
		}
		head := fmt.Sprintf("%s %s %s %s", tagTool, d.Tool, status, d.Elapsed.Round(time.Millisecond))
		out := d.OutputSummary
		if out == "" {
			return []string{head}
		}
		if r := []rune(out); len(r) > maxToolOut {
			out = string(r[:maxToolOut]) + " " + truncatedMark
		}
		return []string{head, tagToolOut + " " + out}
	case core.PolicyDecided:
		// An allowed call is the normal case; it says nothing the tool line
		// does not. Only a denial or an approval gate earns a row here.
		if d.Effect == core.EffectAllow {
			return nil
		}
		return eventLines(e)
	case core.Warning, core.ApprovalRequested,
		core.ApprovalResolved, core.ValidationResult:
		return eventLines(e)
	}
	return nil
}
