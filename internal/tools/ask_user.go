package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/ataidesorg/ink/internal/core"
)

// AskUser is the model's multiple-choice prompt to the human. It never
// writes files or runs commands; it blocks until the UI answers or ctx ends.
type AskUser struct {
	Ask core.AskFunc
}

type askUserArgs struct {
	Questions []core.UserQuestion `json:"questions"`
}

// Spec describes the tool.
func (*AskUser) Spec() core.ToolSpec {
	return core.ToolSpec{
		Name:        "ask_user_question",
		Description: "Ask the human a multiple-choice question and wait for their answer. Use when a preference, trade-off, or missing requirement would change the plan. Not a substitute for the permission prompt on writes or commands.",
		Risk:        core.RiskReadOnly,
		InputSchema: schema("ask_user_question"),
	}
}

func (t *AskUser) bindAsk(ask core.AskFunc) core.Tool { return &AskUser{Ask: ask} }

// Invoke asks the human. A missing AskFunc is unavailable, not a fake answer.
func (t *AskUser) Invoke(ctx context.Context, in core.ToolInput, _ core.ToolContext) (core.ToolOutput, error) {
	var a askUserArgs
	if err := decodeArgs("ask_user_question", in.Arguments, &a); err != nil {
		return core.ToolOutput{}, err
	}
	if err := core.ValidateQuestions(a.Questions); err != nil {
		return core.ToolOutput{}, err
	}
	if t == nil || t.Ask == nil {
		return core.ToolOutput{}, fmt.Errorf("%w: ask_user_question needs an interactive session", core.ErrUnavailable)
	}
	ans, err := t.Ask(ctx, a.Questions)
	if err != nil {
		return core.ToolOutput{}, err
	}
	for i := range ans {
		if strings.TrimSpace(ans[i].Question) == "" && i < len(a.Questions) {
			ans[i].Question = a.Questions[i].Question
		}
	}
	if err := core.ValidateAnswers(a.Questions, ans); err != nil {
		return core.ToolOutput{}, err
	}
	return output(formatAnswers(ans), ans, core.Capability{Risk: core.RiskReadOnly, Scope: core.ResourceScope{Kind: core.ScopeAny}})
}

func formatAnswers(ans []core.UserAnswer) string {
	var b strings.Builder
	for i, a := range ans {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%s: %s", a.Question, strings.Join(a.Selected, ", "))
	}
	return b.String()
}
