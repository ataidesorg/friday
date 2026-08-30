package evals

import (
	"fmt"
	"time"

	"github.com/ataidesorg/ink/internal/core"
)

// Diff is one gate failure when comparing candidate results to a baseline.
type Diff struct {
	Scenario core.ScenarioID
	Kind     core.GateCheckKind
	Detail   string
}

// Compare returns no_regression diffs: scenarios that passed in baseline
// and failed (or disappeared) in candidate. Fail→pass is not a regression.
func Compare(baseline, candidate []core.EvaluationResult) []Diff {
	byID := indexResults(candidate)
	var diffs []Diff
	for _, b := range baseline {
		if !b.Passed {
			continue
		}
		c, ok := byID[b.Scenario]
		switch {
		case !ok:
			diffs = append(diffs, Diff{Scenario: b.Scenario, Kind: core.GateNoRegression, Detail: "missing from candidate"})
		case !c.Passed:
			diffs = append(diffs, Diff{Scenario: b.Scenario, Kind: core.GateNoRegression, Detail: "pass → fail"})
		}
	}
	return diffs
}

// EvaluateGate runs every check on the gate. Human approval is not decided
// here: RequiresHumanApproval is a process flag, not a numeric check.
func EvaluateGate(g core.ReleaseGate, baseline, candidate []core.EvaluationResult) (bool, []Diff) {
	var diffs []Diff
	for _, check := range g.Checks {
		switch check.Kind {
		case core.GateNoRegression:
			diffs = append(diffs, Compare(baseline, candidate)...)
		case core.GateAllTestsPass:
			for _, c := range candidate {
				if !c.Passed {
					diffs = append(diffs, Diff{Scenario: c.Scenario, Kind: core.GateAllTestsPass, Detail: "candidate failed"})
				}
			}
		case core.GateMaxCostIncrease:
			diffs = append(diffs, costDiffs(baseline, candidate, check.Percent)...)
		case core.GateMaxLatencyIncrease:
			diffs = append(diffs, latencyDiffs(baseline, candidate, check.Percent)...)
		case core.GateMinPassRate:
			diffs = append(diffs, passRateDiffs(candidate, check.Percent)...)
		}
	}
	return len(diffs) == 0, diffs
}

func indexResults(in []core.EvaluationResult) map[core.ScenarioID]core.EvaluationResult {
	m := make(map[core.ScenarioID]core.EvaluationResult, len(in))
	for _, r := range in {
		m[r.Scenario] = r
	}
	return m
}

func costDiffs(baseline, candidate []core.EvaluationResult, percent int) []Diff {
	cand := indexResults(candidate)
	var diffs []Diff
	for _, b := range baseline {
		c, ok := cand[b.Scenario]
		if !ok {
			continue
		}
		bc, cc := actual(b.Cost), actual(c.Cost)
		if cc > allowed(bc, percent) {
			diffs = append(diffs, Diff{Scenario: b.Scenario, Kind: core.GateMaxCostIncrease, Detail: fmt.Sprintf("cost %s → %s exceeds %d%%", bc, cc, percent)})
		}
	}
	return diffs
}

func latencyDiffs(baseline, candidate []core.EvaluationResult, percent int) []Diff {
	cand := indexResults(candidate)
	var diffs []Diff
	for _, b := range baseline {
		c, ok := cand[b.Scenario]
		if !ok {
			continue
		}
		if c.Elapsed > allowedDuration(b.Elapsed, percent) {
			diffs = append(diffs, Diff{Scenario: b.Scenario, Kind: core.GateMaxLatencyIncrease, Detail: fmt.Sprintf("latency %s → %s exceeds %d%%", b.Elapsed, c.Elapsed, percent)})
		}
	}
	return diffs
}

func passRateDiffs(candidate []core.EvaluationResult, percent int) []Diff {
	if len(candidate) == 0 {
		return []Diff{{Kind: core.GateMinPassRate, Detail: "no candidate results"}}
	}
	pass := 0
	for _, c := range candidate {
		if c.Passed {
			pass++
		}
	}
	rate := pass * 100 / len(candidate)
	if rate < percent {
		return []Diff{{Kind: core.GateMinPassRate, Detail: fmt.Sprintf("pass rate %d%% < %d%%", rate, percent)}}
	}
	return nil
}

func actual(c core.CostReport) core.USDMicros {
	if c.Actual != nil {
		return *c.Actual
	}
	return 0
}

func allowed(base core.USDMicros, percent int) core.USDMicros {
	if percent < 0 {
		percent = 0
	}
	return base + base*core.USDMicros(percent)/100
}

func allowedDuration(d time.Duration, percent int) time.Duration {
	if percent < 0 {
		percent = 0
	}
	return d + d*time.Duration(percent)/100
}
