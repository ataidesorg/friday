package core

import (
	"errors"
	"testing"
)

func TestValidateQuestions(t *testing.T) {
	ok := []UserQuestion{{
		Question: "Ship it?",
		Options:  []UserOption{{Label: "yes"}, {Label: "no"}},
	}}
	if err := ValidateQuestions(ok); err != nil {
		t.Fatal(err)
	}
	if err := ValidateQuestions(nil); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("empty: %v", err)
	}
	tooMany := make([]UserQuestion, MaxUserQuestions+1)
	for i := range tooMany {
		tooMany[i] = ok[0]
	}
	if err := ValidateQuestions(tooMany); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("too many: %v", err)
	}
	if err := ValidateQuestions([]UserQuestion{{Question: " ", Options: ok[0].Options}}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("blank: %v", err)
	}
	if err := ValidateQuestions([]UserQuestion{{Question: "q", Options: []UserOption{{Label: "only"}}}}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("one option: %v", err)
	}
	if err := ValidateQuestions([]UserQuestion{{Question: "q", Options: []UserOption{{Label: "a"}, {Label: "a"}}}}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("dup: %v", err)
	}
}

func TestValidateAnswers(t *testing.T) {
	qs := []UserQuestion{{
		Question:    "Pick",
		Options:     []UserOption{{Label: "a"}, {Label: "b"}, {Label: "c"}},
		MultiSelect: true,
	}}
	if err := ValidateAnswers(qs, []UserAnswer{{Question: "Pick", Selected: []string{"a", "c"}}}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateAnswers(qs, []UserAnswer{{Selected: []string{"nope"}}}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("unknown: %v", err)
	}
	single := []UserQuestion{{Question: "One", Options: []UserOption{{Label: "a"}, {Label: "b"}}}}
	if err := ValidateAnswers(single, []UserAnswer{{Selected: []string{"a", "b"}}}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("single multi: %v", err)
	}
	if err := ValidateAnswers(single, []UserAnswer{{Selected: nil}}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("empty: %v", err)
	}
}
