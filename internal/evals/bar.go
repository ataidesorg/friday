package evals

import (
	"fmt"
	"strings"
	"time"

	"github.com/ataidesorg/friday/internal/core"
)

// Bar is a cheap/fast/quality ceiling. Zero fields are ignored except
// MustPass, which requires the scenario itself to have passed.
type Bar struct {
	MaxElapsed      time.Duration
	MaxInputTokens  int64
	MaxOutputTokens int64
	MaxCost         core.USDMicros
	MustPass        bool
}

// MockBar is the scripted-harness bar: add-farewell on the mock provider
// must pass and finish in well under a slow tool loop. Live bars are set
// by the caller; tests never dial a network.
func MockBar() Bar {
	return Bar{
		MaxElapsed:      30 * time.Second,
		MaxInputTokens:  50_000,
		MaxOutputTokens: 8_000,
		MustPass:        true,
	}
}

// Verdict is Judge's answer: Met means every set threshold held.
type Verdict struct {
	Met     bool
	Reasons []string
}

// Judge compares one evaluation result to a bar. It does not run anything.
func Judge(res core.EvaluationResult, bar Bar) Verdict {
	var reasons []string
	if bar.MustPass && !res.Passed {
		reasons = append(reasons, "scenario failed")
	}
	if bar.MaxElapsed > 0 && res.Elapsed > bar.MaxElapsed {
		reasons = append(reasons, fmt.Sprintf("elapsed %s > %s", res.Elapsed.Round(time.Millisecond), bar.MaxElapsed))
	}
	if bar.MaxInputTokens > 0 && res.Usage.InputTokens > bar.MaxInputTokens {
		reasons = append(reasons, fmt.Sprintf("input tokens %d > %d", res.Usage.InputTokens, bar.MaxInputTokens))
	}
	if bar.MaxOutputTokens > 0 && res.Usage.OutputTokens > bar.MaxOutputTokens {
		reasons = append(reasons, fmt.Sprintf("output tokens %d > %d", res.Usage.OutputTokens, bar.MaxOutputTokens))
	}
	if bar.MaxCost > 0 {
		if c := actualCost(res.Cost); c > bar.MaxCost {
			reasons = append(reasons, fmt.Sprintf("cost %s > %s", c, bar.MaxCost))
		}
	}
	return Verdict{Met: len(reasons) == 0, Reasons: reasons}
}

// FormatVerdict is a one-block report for `friday eval bench`.
func FormatVerdict(v Verdict, res core.EvaluationResult) string {
	mark := "MET"
	if !v.Met {
		mark = "MISS"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "bench %s: %s\n", mark, res.Model)
	fmt.Fprintf(&b, "  elapsed %s\n", res.Elapsed.Round(time.Millisecond))
	fmt.Fprintf(&b, "  tokens in=%d out=%d\n", res.Usage.InputTokens, res.Usage.OutputTokens)
	if c := actualCost(res.Cost); c > 0 {
		fmt.Fprintf(&b, "  cost %s\n", c)
	}
	fmt.Fprintf(&b, "  passed %t\n", res.Passed)
	for _, r := range v.Reasons {
		fmt.Fprintf(&b, "  - %s\n", r)
	}
	return b.String()
}

func actualCost(c core.CostReport) core.USDMicros {
	if c.Actual != nil {
		return *c.Actual
	}
	return 0
}
