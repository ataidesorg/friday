package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/ataidesorg/friday/internal/core"
	"github.com/ataidesorg/friday/internal/tools"
)

// RuleUnknownTool is the rule recorded when the model names a tool that is
// not registered; the call never reaches the policy engine.
const RuleUnknownTool = "runtime.unknown_tool"

// execute runs the model's tool calls until a response carries none.
func (s *state) execute(ctx context.Context) (core.Transition, error) {
	for len(s.last.ToolCalls) > 0 {
		for _, c := range s.last.ToolCalls {
			if err := s.checkToolBudget(); err != nil {
				return core.Transition{}, err
			}
			out, err := s.call(ctx, c)
			if err != nil {
				return core.Transition{}, err
			}
			s.msgs = append(s.msgs, core.Message{Role: core.RoleTool, ToolCallID: c.ID, Name: c.Name, Content: out})
		}
		if err := s.complete(ctx); err != nil {
			return core.Transition{}, err
		}
	}
	return advance, nil
}

// call records, authorises and runs one tool call and returns the text fed
// back to the model. Denials and tool errors are data for the model; only
// context errors and approver failures abort the run.
func (s *state) call(ctx context.Context, c core.ToolCall) (string, error) {
	start := s.now()
	t, ok := s.tools.Get(c.Name)
	var risk core.RiskClass
	if ok {
		risk = t.Spec().Risk
	}
	if err := s.emit(ctx, core.ToolCalled{Call: c.ID, Tool: c.Name, Risk: risk, InputSummary: summary(string(c.Arguments))}); err != nil {
		return "", err
	}
	content, success, err := s.dispatch(ctx, t, ok, c)
	if err != nil {
		return "", err
	}
	return content, s.emit(ctx, core.ToolCompleted{Call: c.ID, Tool: c.Name, Success: success, Elapsed: s.now().Sub(start), OutputSummary: summary(content)})
}

func (s *state) dispatch(ctx context.Context, t core.Tool, known bool, c core.ToolCall) (string, bool, error) {
	if !known {
		reason := fmt.Sprintf("tool %q is not registered", c.Name)
		err := s.emit(ctx, core.PolicyDecided{Call: c.ID, Tool: c.Name, Effect: core.EffectDeny, Rule: RuleUnknownTool, Reason: reason})
		return "denied: " + reason, false, err
	}
	in := core.ToolInput{Call: c.ID, Arguments: c.Arguments}
	capability := tools.CapabilityOf(t, in)
	req := core.CapabilityRequest{Call: c.ID, Tool: c.Name, Capability: capability, Justification: "model tool call"}
	dec := s.d.Policy.Evaluate(req, core.PolicyContext{WorkspaceRoot: s.root, Posture: s.posture})
	if err := s.emit(ctx, core.PolicyDecided{Call: c.ID, Tool: c.Name, Risk: capability.Risk, Effect: dec.Effect, Rule: dec.Rule, Reason: dec.Reason}); err != nil {
		return "", false, err
	}
	tc := core.ToolContext{Run: s.run.ID, WorkspaceRoot: s.root, Sandbox: s.sandbox.Info().ID}
	allowed, reason, err := s.authorise(ctx, req, dec, tools.Preview(t, in, tc))
	switch {
	case err != nil:
		return "", false, err
	case !allowed:
		return "denied: " + reason, false, nil
	}
	out, err := t.Invoke(ctx, in, tc)
	switch {
	case err != nil && ctx.Err() != nil:
		return "", false, err
	case errors.Is(err, core.ErrPolicyDenied):
		return "denied: " + err.Error(), false, nil
	case err != nil:
		return "error: " + err.Error(), false, nil
	}
	return out.Content, true, nil
}

// authorise turns a decision into a verdict; anything but an explicit allow
// or an approved request is denied.
func (s *state) authorise(ctx context.Context, req core.CapabilityRequest, dec core.PolicyDecision, preview string) (bool, string, error) {
	switch dec.Effect {
	case core.EffectAllow:
		return true, "", nil
	case core.EffectRequireApproval:
		return s.approve(ctx, req, dec, preview)
	default:
		return false, dec.Reason, nil
	}
}

func (s *state) approve(ctx context.Context, req core.CapabilityRequest, dec core.PolicyDecision, preview string) (bool, string, error) {
	a := core.Approval{ID: core.NewApprovalID(), Task: s.in.Task.ID, Run: s.run.ID, Request: req, Preview: preview, RequestedAt: s.now()}
	if err := s.emit(ctx, core.ApprovalRequested{Approval: a.ID, Tool: req.Tool, Risk: req.Capability.Risk, Justification: dec.Reason}); err != nil {
		return false, "", err
	}
	res, err := s.resolve(ctx, a)
	if err != nil {
		return false, "", err
	}
	if err := s.emit(ctx, core.ApprovalResolved{Approval: a.ID, Decision: res.Decision, By: res.By, Scope: res.Scope}); err != nil {
		return false, "", err
	}
	if res.Decision == core.ApprovalApproved {
		return true, "", nil
	}
	reason := "approval denied"
	if res.Note != "" {
		reason += ": " + res.Note
	}
	return false, reason, nil
}

// resolve answers from the session store, then the approver, then fails
// closed; an approver error aborts the run rather than guessing.
func (s *state) resolve(ctx context.Context, a core.Approval) (core.ApprovalResolution, error) {
	if res, ok := s.d.Approvals.Lookup(a.Request); ok {
		res.Note = "resolved from session approval"
		return res, nil
	}
	system := core.Principal{Kind: core.PrincipalSystem, Name: "runtime"}
	if s.d.Approve == nil {
		return core.ApprovalResolution{Decision: core.ApprovalDenied, By: system, At: s.now(), Scope: core.ApprovalOnce, Note: "no approver (non-interactive)"}, nil
	}
	res, err := s.d.Approve(ctx, a)
	if err != nil {
		if ctx.Err() != nil {
			return res, err
		}
		return res, fail(core.FailureInternal, fmt.Errorf("approver: %w", err))
	}
	if res.Decision != core.ApprovalApproved && res.Decision != core.ApprovalDenied {
		res = core.ApprovalResolution{Decision: core.ApprovalDenied, By: system, At: s.now(), Scope: core.ApprovalOnce, Note: fmt.Sprintf("unknown decision %q treated as denied", res.Decision)}
	}
	if res.At.IsZero() {
		res.At = s.now()
	}
	s.d.Approvals.Record(a.Request, res)
	return res, nil
}
