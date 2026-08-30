package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ataidesorg/ink/internal/core"
)

func TestAskUserRequiresInteractive(t *testing.T) {
	_, tc := ws(t)
	_, err := call(t, &AskUser{}, tc, `{"questions":[{"question":"Ship?","options":[{"label":"yes","description":"go"},{"label":"no","description":"stop"}]}]}`)
	if !errors.Is(err, core.ErrUnavailable) {
		t.Fatalf("nil Ask: %v", err)
	}
}

func TestAskUserRoundTrip(t *testing.T) {
	_, tc := ws(t)
	tool := &AskUser{Ask: func(_ context.Context, qs []core.UserQuestion) ([]core.UserAnswer, error) {
		if len(qs) != 1 || qs[0].Question != "Ship?" {
			t.Fatalf("questions %+v", qs)
		}
		return []core.UserAnswer{{Question: "Ship?", Selected: []string{"yes"}}}, nil
	}}
	out, err := call(t, tool, tc, `{"questions":[{"question":"Ship?","options":[{"label":"yes","description":"go"},{"label":"no","description":"stop"}]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Content, "Ship?: yes") {
		t.Fatalf("content %q", out.Content)
	}
	var got []core.UserAnswer
	if err := json.Unmarshal(out.Structured, &got); err != nil || len(got) != 1 || got[0].Selected[0] != "yes" {
		t.Fatalf("structured %s (%v)", out.Structured, err)
	}
}

func TestAskUserRejectsBadArgsAndBadAnswers(t *testing.T) {
	_, tc := ws(t)
	tool := &AskUser{Ask: func(context.Context, []core.UserQuestion) ([]core.UserAnswer, error) {
		return []core.UserAnswer{{Selected: []string{"nope"}}}, nil
	}}
	if _, err := call(t, tool, tc, `{"questions":[]}`); !errors.Is(err, core.ErrInvalidInput) {
		t.Errorf("empty questions: %v", err)
	}
	if _, err := call(t, tool, tc, `{"questions":[{"question":"Q","options":[{"label":"a","description":"x"},{"label":"b","description":"y"}]}]}`); !errors.Is(err, core.ErrInvalidInput) {
		t.Errorf("unknown answer: %v", err)
	}
}

func TestAskUserHonoursContext(t *testing.T) {
	_, tc := ws(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tool := &AskUser{Ask: func(ctx context.Context, _ []core.UserQuestion) ([]core.UserAnswer, error) {
		return nil, ctx.Err()
	}}
	_, err := tool.Invoke(ctx, core.ToolInput{Call: core.NewToolCallID(), Arguments: json.RawMessage(`{"questions":[{"question":"Q","options":[{"label":"a","description":"x"},{"label":"b","description":"y"}]}]}`)}, tc)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v", err)
	}
}

func TestWithAskUserReplacesCallback(t *testing.T) {
	r := Default(nil, nil).WithAskUser(func(context.Context, []core.UserQuestion) ([]core.UserAnswer, error) {
		return []core.UserAnswer{{Question: "Q", Selected: []string{"a"}}}, nil
	})
	t1, ok := r.Get("ask_user_question")
	if !ok {
		t.Fatal("missing tool")
	}
	_, tc := ws(t)
	out, err := t1.Invoke(context.Background(), core.ToolInput{Arguments: json.RawMessage(`{"questions":[{"question":"Q","options":[{"label":"a","description":"x"},{"label":"b","description":"y"}]}]}`)}, tc)
	if err != nil || !strings.Contains(out.Content, "Q: a") {
		t.Fatalf("%v %q", err, out.Content)
	}
	plain := Default(nil, nil)
	if t2, _ := plain.Get("ask_user_question"); t2 == t1 {
		t.Fatal("WithAskUser must not mutate Default")
	}
}
