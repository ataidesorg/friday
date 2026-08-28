package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// AskFunc presents questions to a human and returns their selections.
type AskFunc func(ctx context.Context, qs []UserQuestion) ([]UserAnswer, error)

// ErrQuestionDeclined is returned when the human dismisses the prompt.
var ErrQuestionDeclined = errors.New("user declined the question")

// Question limits keep a prompt scannable in the TUI.
const (
	MaxUserQuestions = 8
	MinUserOptions   = 2
	MaxUserOptions   = 8
)

// UserQuestion is one multiple-choice prompt the model asks the human.
// Options are the only legal answers; free-text is refused.
type UserQuestion struct {
	Question    string       `json:"question"`
	Options     []UserOption `json:"options"`
	MultiSelect bool         `json:"multi_select,omitempty"`
}

// UserOption is one labeled choice. Preview is TUI-only and never required.
type UserOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Preview     string `json:"preview,omitempty"`
}

// UserAnswer is the human's selection for one question, by option label.
type UserAnswer struct {
	Question string   `json:"question"`
	Selected []string `json:"selected"`
}

// ValidateQuestions rejects an empty list, oversized lists, blank text,
// too few/many options, and duplicate labels inside a question.
func ValidateQuestions(qs []UserQuestion) error {
	if len(qs) == 0 {
		return fmt.Errorf("%w: ask_user_question needs at least one question", ErrInvalidInput)
	}
	if len(qs) > MaxUserQuestions {
		return fmt.Errorf("%w: ask_user_question allows at most %d questions", ErrInvalidInput, MaxUserQuestions)
	}
	for i, q := range qs {
		if strings.TrimSpace(q.Question) == "" {
			return fmt.Errorf("%w: question %d is empty", ErrInvalidInput, i)
		}
		if len(q.Options) < MinUserOptions || len(q.Options) > MaxUserOptions {
			return fmt.Errorf("%w: question %d needs %d–%d options", ErrInvalidInput, i, MinUserOptions, MaxUserOptions)
		}
		seen := map[string]bool{}
		for j, o := range q.Options {
			label := strings.TrimSpace(o.Label)
			if label == "" {
				return fmt.Errorf("%w: question %d option %d has no label", ErrInvalidInput, i, j)
			}
			if seen[label] {
				return fmt.Errorf("%w: question %d has duplicate label %q", ErrInvalidInput, i, label)
			}
			seen[label] = true
		}
	}
	return nil
}

// ValidateAnswers checks that each question has a matching answer whose
// labels exist on that question. Multi-select may pick several; single
// select must pick exactly one.
func ValidateAnswers(qs []UserQuestion, ans []UserAnswer) error {
	if err := ValidateQuestions(qs); err != nil {
		return err
	}
	if len(ans) != len(qs) {
		return fmt.Errorf("%w: expected %d answers, got %d", ErrInvalidInput, len(qs), len(ans))
	}
	for i, q := range qs {
		a := ans[i]
		allowed := map[string]bool{}
		for _, o := range q.Options {
			allowed[strings.TrimSpace(o.Label)] = true
		}
		if len(a.Selected) == 0 {
			return fmt.Errorf("%w: question %d has no selection", ErrInvalidInput, i)
		}
		if !q.MultiSelect && len(a.Selected) != 1 {
			return fmt.Errorf("%w: question %d is single-select", ErrInvalidInput, i)
		}
		seen := map[string]bool{}
		for _, s := range a.Selected {
			s = strings.TrimSpace(s)
			if !allowed[s] {
				return fmt.Errorf("%w: question %d has unknown option %q", ErrInvalidInput, i, s)
			}
			if seen[s] {
				return fmt.Errorf("%w: question %d repeats option %q", ErrInvalidInput, i, s)
			}
			seen[s] = true
		}
	}
	return nil
}
