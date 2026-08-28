package runtime

import (
	"context"
	"fmt"

	"github.com/ataidesorg/friday/internal/core"
)

// complete makes one model call, accounts usage and cost, and appends the
// assistant turn. It enforces max_cost before the call and refuses to
// continue when the provider reports an error finish.
func (s *state) complete(ctx context.Context) error {
	if err := s.checkCost(ctx); err != nil {
		return err
	}
	desc := s.d.Provider.Descriptor()
	start := s.now()
	resp, err := s.d.Provider.Complete(ctx, core.CompletionRequest{Model: s.in.Model, Messages: s.msgs, Tools: s.tools.Specs()})
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		return fail(core.FailureProviderError, fmt.Errorf("model %s: %w", s.in.Model, err))
	}
	s.usage = s.usage.Add(resp.Usage)
	cost := core.CostReport{Actual: s.d.Price(desc.ID, s.in.Model, resp.Usage)}
	s.addCost(cost.Actual)
	if s.d.Spend != nil {
		s.d.Spend.AddSession(cost.Actual)
	}
	if err := s.emit(ctx, core.ModelUsage{Provider: desc.ID, Model: s.in.Model, Usage: resp.Usage, Cost: cost, Latency: s.now().Sub(start)}); err != nil {
		return err
	}
	if resp.Finish == core.FinishError {
		return fail(core.FailureProviderError, fmt.Errorf("model %s: finish reason %q", s.in.Model, resp.Finish))
	}
	if resp.Finish == core.FinishLength {
		if err := s.emit(ctx, core.Warning{Message: "model output truncated (finish reason length)"}); err != nil {
			return err
		}
	}
	s.last = resp
	s.msgs = append(s.msgs, core.Message{Role: core.RoleAssistant, Content: resp.Content, ToolCalls: resp.ToolCalls})
	if !s.d.Streamed {
		s.d.Observer.OnModelDelta(resp.Content)
	}
	return nil
}

// addCost keeps the running actual cost; one unknown price makes the total
// unknown, which max_cost then cannot enforce.
func (s *state) addCost(c *core.USDMicros) {
	switch {
	case c == nil:
		s.cost.Actual = nil
		s.costUnknown = true
	case s.costUnknown:
	case s.cost.Actual == nil:
		v := *c
		s.cost.Actual = &v
	default:
		v, err := s.cost.Actual.Add(*c)
		if err != nil {
			// Overflow means the total is no longer representable: treat as unknown.
			s.cost.Actual = nil
			s.costUnknown = true
			return
		}
		s.cost.Actual = &v
	}
}

func (s *state) checkCost(ctx context.Context) error {
	if err := s.checkTaskCost(ctx); err != nil {
		return err
	}
	return s.checkSpendCaps(ctx)
}

func (s *state) checkTaskCost(ctx context.Context) error {
	maxCost := s.in.Task.Budget.MaxCost
	if maxCost <= 0 {
		return nil
	}
	if s.costUnknown {
		if s.costWarned {
			return nil
		}
		s.costWarned = true
		return s.emit(ctx, core.Warning{Message: fmt.Sprintf("cost of model %s is unknown; max_cost cannot be enforced", s.in.Model), Advisory: true})
	}
	if s.cost.Actual != nil && *s.cost.Actual >= maxCost {
		return fail(core.FailureBudgetExceeded, fmt.Errorf("cost %s reached max_cost %s", *s.cost.Actual, maxCost))
	}
	return nil
}

// checkSpendCaps enforces per_session_usd and per_day_usd before a model
// call. Breaching either escalates the task instead of failing it: lifting
// a cap that outlives the task is a human decision. An unknown cost makes
// a cap unenforceable, which warns once, like max_cost.
func (s *state) checkSpendCaps(ctx context.Context) error {
	sp := s.d.Spend
	if sp == nil {
		return nil
	}
	sess, exact := sp.SessionTotal()
	if lim := sp.sessionCap(); lim > 0 {
		if !exact {
			return s.warnSpendUnknown(ctx)
		}
		if sess >= lim {
			return escalate(fmt.Sprintf("session spend %s reached per_session_usd %s", sess, lim))
		}
	}
	if lim := sp.dayCap(); lim > 0 {
		if s.costUnknown {
			return s.warnSpendUnknown(ctx)
		}
		if err := s.loadDay(ctx); err != nil {
			return err
		}
		if s.dayBroken {
			return nil
		}
		day := s.dayBase
		if s.cost.Actual != nil {
			sum, err := day.Add(*s.cost.Actual)
			if err != nil {
				return escalate(fmt.Sprintf("day spend overflows past per_day_usd %s", lim))
			}
			day = sum
		}
		if day >= lim {
			return escalate(fmt.Sprintf("day spend %s reached per_day_usd %s", day, lim))
		}
	}
	return nil
}

func (s *state) warnSpendUnknown(ctx context.Context) error {
	if s.spendWarned {
		return nil
	}
	s.spendWarned = true
	return s.emit(ctx, core.Warning{Message: fmt.Sprintf("cost of model %s is unknown; session and day budgets cannot be enforced", s.in.Model), Advisory: true})
}

// loadDay reads today's ledger total once per run. An unreadable ledger
// warns and disables the day check for this run rather than crashing.
func (s *state) loadDay(ctx context.Context) error {
	if s.dayLoaded {
		return nil
	}
	s.dayLoaded = true
	base, corrupt, err := s.d.Spend.DayTotal(s.now().Format(dayLayout))
	if err != nil {
		s.dayBroken = true
		return s.emit(ctx, core.Warning{Message: fmt.Sprintf("per_day_usd cannot be enforced: %v", err), Advisory: true})
	}
	if corrupt > 0 {
		if err := s.emit(ctx, core.Warning{Message: fmt.Sprintf("spend ledger: skipped %d corrupt line(s)", corrupt)}); err != nil {
			return err
		}
	}
	s.dayBase = base
	return nil
}

func (s *state) checkToolBudget() error {
	s.calls++
	if s.calls > s.maxCalls {
		return fail(core.FailureBudgetExceeded, fmt.Errorf("tool call %d exceeds max_tool_calls %d", s.calls, s.maxCalls))
	}
	return nil
}
