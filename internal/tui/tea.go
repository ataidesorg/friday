package tui

import (
	"context"
	"errors"
	"io"
	"sync"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ataidesorg/ink/internal/core"
	"github.com/ataidesorg/ink/internal/runtime"
)

// teaProgram runs the Model under Bubble Tea. Messages sent before Start
// wait for it; messages sent after the program ended are dropped.
type teaProgram struct {
	out     io.Writer
	in      io.Reader
	opts    Options
	p       *tea.Program
	started chan struct{}
	quit    chan struct{}
	once    sync.Once
}

func newTea(out io.Writer, in io.Reader, opts Options) *teaProgram {
	return &teaProgram{out: out, in: in, opts: opts, started: make(chan struct{}), quit: make(chan struct{})}
}

func (t *teaProgram) Start(ctx context.Context) error {
	defer t.once.Do(func() { close(t.quit) })
	t.p = tea.NewProgram(NewModel(t.opts), tea.WithContext(ctx), tea.WithInput(t.in), tea.WithOutput(t.out))
	close(t.started)
	_, err := t.p.Run()
	if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func (t *teaProgram) send(ctx context.Context, msg tea.Msg) {
	select {
	case <-t.started:
		t.p.Send(msg)
	case <-t.quit:
	case <-ctx.Done():
	}
}

func (t *teaProgram) Observer() runtime.Observer { return t }
func (t *teaProgram) OnEvent(e core.Event)       { t.send(context.Background(), EventMsg{E: e}) }
func (t *teaProgram) OnPhase(p core.Phase)       { t.send(context.Background(), PhaseMsg(p)) }
func (t *teaProgram) OnModelDelta(s string)      { t.send(context.Background(), DeltaMsg(s)) }

func (t *teaProgram) Done(res runtime.Result, diff string) {
	t.send(context.Background(), DoneMsg{Result: res, Diff: diff})
}

func (t *teaProgram) Approver() runtime.ApprovalFunc {
	return func(ctx context.Context, a core.Approval) (core.ApprovalResolution, error) {
		reply := make(chan core.ApprovalResolution, 1)
		go t.send(ctx, ApprovalMsg{A: a, Reply: reply})
		select {
		case r := <-reply:
			return r, nil
		case <-ctx.Done():
			return core.ApprovalResolution{}, ctx.Err()
		case <-t.quit:
			return core.ApprovalResolution{Decision: core.ApprovalDenied, By: owner, Scope: core.ApprovalOnce, Note: "interface closed"}, nil
		}
	}
}

func (t *teaProgram) Asker() core.AskFunc {
	return func(ctx context.Context, qs []core.UserQuestion) ([]core.UserAnswer, error) {
		reply := make(chan QuestionResult, 1)
		go t.send(ctx, QuestionMsg{Questions: qs, Reply: reply})
		select {
		case r := <-reply:
			if r.Stop {
				return nil, core.ErrQuestionDeclined
			}
			return r.Answers, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-t.quit:
			return nil, core.ErrQuestionDeclined
		}
	}
}
