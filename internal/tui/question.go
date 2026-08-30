package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ataidesorg/ink/internal/core"
)

// QuestionMsg asks the human to pick options. Reply must have capacity 1.
type QuestionMsg struct {
	Questions []core.UserQuestion
	Reply     chan<- QuestionResult
}

// QuestionResult is the human's answer, or Stop when they dismissed it.
type QuestionResult struct {
	Answers []core.UserAnswer
	Stop    bool
}

// questionPrompt is the in-flight multiple-choice overlay.
type questionPrompt struct {
	questions []core.UserQuestion
	answers   []core.UserAnswer
	idx       int
	cursor    int
	picked    map[int]bool
	reply     chan<- QuestionResult
}

func newQuestionPrompt(qs []core.UserQuestion, reply chan<- QuestionResult) *questionPrompt {
	return &questionPrompt{questions: qs, reply: reply, picked: map[int]bool{}}
}

func (q *questionPrompt) current() core.UserQuestion {
	if q == nil || q.idx < 0 || q.idx >= len(q.questions) {
		return core.UserQuestion{}
	}
	return q.questions[q.idx]
}

func (q *questionPrompt) view(on bool, accent, dim func(string) string) string {
	cur := q.current()
	if cur.Question == "" {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s  (%d/%d)", cur.Question, q.idx+1, len(q.questions))
	if cur.MultiSelect {
		b.WriteString("  multi")
	}
	b.WriteByte('\n')
	for i, o := range cur.Options {
		mark := " "
		if q.picked[i] || (!cur.MultiSelect && q.cursor == i) {
			mark = "▸"
		}
		line := fmt.Sprintf("%s %d. %s", mark, i+1, o.Label)
		if o.Description != "" {
			line += " — " + o.Description
		}
		switch {
		case on && q.cursor == i:
			b.WriteString(accent(line))
		default:
			b.WriteString(line)
		}
		b.WriteByte('\n')
	}
	hint := "1–9 select · enter confirm · esc skip"
	if cur.MultiSelect {
		hint = "1–9 toggle · enter confirm · esc skip"
	}
	if on {
		b.WriteString(dim(hint))
	} else {
		b.WriteString(hint)
	}
	return strings.TrimRight(b.String(), "\n")
}

func (q *questionPrompt) key(k tea.KeyMsg) (done, stop bool) {
	if q == nil {
		return true, true
	}
	cur := q.current()
	switch {
	case k.Type == tea.KeyEsc || k.String() == "n":
		return true, true
	case k.Type == tea.KeyUp:
		if q.cursor > 0 {
			q.cursor--
		}
		return false, false
	case k.Type == tea.KeyDown:
		if q.cursor+1 < len(cur.Options) {
			q.cursor++
		}
		return false, false
	case k.Type == tea.KeyEnter:
		if cur.MultiSelect {
			return q.confirmMulti()
		}
		return q.pick(q.cursor)
	}
	if n, err := strconv.Atoi(k.String()); err == nil && n >= 1 && n <= 9 {
		i := n - 1
		if i >= len(cur.Options) {
			return false, false
		}
		if cur.MultiSelect {
			q.picked[i] = !q.picked[i]
			q.cursor = i
			return false, false
		}
		return q.pick(i)
	}
	return false, false
}

func (q *questionPrompt) pick(i int) (done, stop bool) {
	cur := q.current()
	if i < 0 || i >= len(cur.Options) {
		return false, false
	}
	q.answers = append(q.answers, core.UserAnswer{
		Question: cur.Question,
		Selected: []string{cur.Options[i].Label},
	})
	return q.advance()
}

func (q *questionPrompt) confirmMulti() (done, stop bool) {
	cur := q.current()
	var sel []string
	for i, o := range cur.Options {
		if q.picked[i] {
			sel = append(sel, o.Label)
		}
	}
	if len(sel) == 0 {
		return false, false
	}
	q.answers = append(q.answers, core.UserAnswer{Question: cur.Question, Selected: sel})
	return q.advance()
}

func (q *questionPrompt) advance() (done, stop bool) {
	q.idx++
	q.cursor = 0
	q.picked = map[int]bool{}
	if q.idx >= len(q.questions) {
		return true, false
	}
	return false, false
}

func (q *questionPrompt) finish(stop bool) {
	if q == nil || q.reply == nil {
		return
	}
	q.reply <- QuestionResult{Answers: q.answers, Stop: stop}
	q.reply = nil
}

func askerFromChan(ch chan tea.Msg) core.AskFunc {
	return func(ctx context.Context, qs []core.UserQuestion) ([]core.UserAnswer, error) {
		if err := core.ValidateQuestions(qs); err != nil {
			return nil, err
		}
		reply := make(chan QuestionResult, 1)
		select {
		case ch <- QuestionMsg{Questions: qs, Reply: reply}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		select {
		case r := <-reply:
			if r.Stop {
				return nil, core.ErrQuestionDeclined
			}
			return r.Answers, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}
