package tui

import (
	"bufio"
	"context"
	"io"
	"strconv"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ataidesorg/ink/internal/core"
	"github.com/ataidesorg/ink/internal/runtime"
)

// plain prints every line the Model adds, as it is added.
type plain struct {
	out  io.Writer
	in   *bufio.Reader
	mu   sync.Mutex
	m    Model
	err  error
	done chan struct{}
	once sync.Once
}

func newPlain(out io.Writer, in io.Reader, opts Options) *plain {
	return &plain{out: out, in: bufio.NewReader(in), m: NewModel(opts), done: make(chan struct{})}
}

func (p *plain) Start(ctx context.Context) error {
	select {
	case <-p.done:
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *plain) Observer() runtime.Observer     { return p }
func (p *plain) Approver() runtime.ApprovalFunc { return p.approve }
func (p *plain) Asker() core.AskFunc            { return p.ask }
func (p *plain) OnEvent(e core.Event)           { p.apply(EventMsg{E: e}) }
func (p *plain) OnPhase(ph core.Phase)          { p.apply(PhaseMsg(ph)) }
func (p *plain) OnModelDelta(s string)          { p.apply(DeltaMsg(s)) }

func (p *plain) Done(res runtime.Result, diff string) {
	p.apply(DoneMsg{Result: res, Diff: diff})
	p.once.Do(func() { close(p.done) })
}

// apply runs msg through the Model and prints the lines it added.
func (p *plain) apply(msg tea.Msg) {
	p.mu.Lock()
	defer p.mu.Unlock()
	before := len(p.m.Lines)
	next, _ := p.m.Update(msg)
	p.m = next.(Model)
	for _, l := range p.m.Lines[before:] {
		if _, err := io.WriteString(p.out, l+"\n"); err != nil && p.err == nil {
			p.err = err
		}
	}
}

func (p *plain) approve(ctx context.Context, a core.Approval) (core.ApprovalResolution, error) {
	if err := ctx.Err(); err != nil {
		return core.ApprovalResolution{}, err
	}
	reply := make(chan core.ApprovalResolution, 1)
	p.apply(ApprovalMsg{A: a, Reply: reply})
	answer := make(chan string, 1)
	// ponytail: a cancelled ctx leaves this read blocked until the process
	// exits; approvals are sequential so it never races a later prompt.
	go func() {
		line, _ := p.in.ReadString('\n')
		answer <- strings.TrimSpace(line)
	}()
	select {
	case <-ctx.Done():
		p.apply(denyMsg("cancelled"))
		return core.ApprovalResolution{}, ctx.Err()
	case s := <-answer:
		switch s {
		case keyApproveOnce, keyApproveSession, keyDeny:
			p.apply(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
		case "":
			p.apply(denyMsg("no answer (input closed)"))
		default:
			p.apply(denyMsg("unrecognised answer " + strconv.Quote(clip(s))))
		}
		return <-reply, nil
	}
}

func (p *plain) ask(ctx context.Context, qs []core.UserQuestion) ([]core.UserAnswer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	reply := make(chan QuestionResult, 1)
	p.apply(QuestionMsg{Questions: qs, Reply: reply})
	answer := make(chan string, 1)
	go func() {
		line, _ := p.in.ReadString('\n')
		answer <- strings.TrimSpace(line)
	}()
	select {
	case <-ctx.Done():
		p.apply(tea.KeyMsg{Type: tea.KeyEsc})
		return nil, ctx.Err()
	case s := <-answer:
		if s == "" || s == "n" {
			p.apply(tea.KeyMsg{Type: tea.KeyEsc})
		} else {
			p.apply(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
			if len(qs) == 0 || qs[0].MultiSelect {
				p.apply(tea.KeyMsg{Type: tea.KeyEnter})
			}
		}
		r := <-reply
		if r.Stop {
			return nil, core.ErrQuestionDeclined
		}
		return r.Answers, nil
	}
}
