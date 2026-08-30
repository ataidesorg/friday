package evals

import (
	"testing"
	"time"

	"github.com/ataidesorg/ink/internal/core"
)

func usd(n core.USDMicros) core.CostReport {
	v := n
	return core.CostReport{Actual: &v}
}

func TestCompareNoRegression(t *testing.T) {
	base := []core.EvaluationResult{
		{Scenario: "a", Passed: true, Cost: usd(100), Elapsed: time.Second},
		{Scenario: "b", Passed: true, Cost: usd(50), Elapsed: time.Second},
	}
	worse := []core.EvaluationResult{
		{Scenario: "a", Passed: false, Cost: usd(100), Elapsed: time.Second},
		{Scenario: "b", Passed: true, Cost: usd(50), Elapsed: time.Second},
	}
	diffs := Compare(base, worse)
	if len(diffs) != 1 || diffs[0].Scenario != "a" || diffs[0].Kind != core.GateNoRegression {
		t.Fatalf("diffs %+v", diffs)
	}
	ok, got := EvaluateGate(core.DefaultReleaseGate(), base, worse)
	if ok || len(got) == 0 {
		t.Fatalf("worse should fail the default gate: ok=%v diffs=%+v", ok, got)
	}
	same := Compare(base, base)
	if len(same) != 0 {
		t.Fatalf("identical: %+v", same)
	}
	ok, got = EvaluateGate(core.DefaultReleaseGate(), base, base)
	if !ok || len(got) != 0 {
		t.Fatalf("identical should pass: ok=%v diffs=%+v", ok, got)
	}
}

func TestCompareCostAndLatency(t *testing.T) {
	base := []core.EvaluationResult{{Scenario: "a", Passed: true, Cost: usd(100), Elapsed: 100 * time.Millisecond}}
	pricey := []core.EvaluationResult{{Scenario: "a", Passed: true, Cost: usd(150), Elapsed: 100 * time.Millisecond}}
	slow := []core.EvaluationResult{{Scenario: "a", Passed: true, Cost: usd(100), Elapsed: 200 * time.Millisecond}}
	gate := core.ReleaseGate{Checks: []core.GateCheck{
		{Kind: core.GateMaxCostIncrease, Percent: 20},
		{Kind: core.GateMaxLatencyIncrease, Percent: 50},
	}}
	ok, diffs := EvaluateGate(gate, base, pricey)
	if ok || len(diffs) != 1 || diffs[0].Kind != core.GateMaxCostIncrease {
		t.Fatalf("cost: ok=%v diffs=%+v", ok, diffs)
	}
	ok, diffs = EvaluateGate(gate, base, slow)
	if ok || len(diffs) != 1 || diffs[0].Kind != core.GateMaxLatencyIncrease {
		t.Fatalf("latency: ok=%v diffs=%+v", ok, diffs)
	}
	mild := []core.EvaluationResult{{Scenario: "a", Passed: true, Cost: usd(110), Elapsed: 120 * time.Millisecond}}
	ok, diffs = EvaluateGate(gate, base, mild)
	if !ok || len(diffs) != 0 {
		t.Fatalf("within threshold: ok=%v diffs=%+v", ok, diffs)
	}
}

func TestCompareAllTestsPass(t *testing.T) {
	base := []core.EvaluationResult{{Scenario: "a", Passed: false}}
	cand := []core.EvaluationResult{{Scenario: "a", Passed: false}}
	ok, diffs := EvaluateGate(core.ReleaseGate{Checks: []core.GateCheck{{Kind: core.GateAllTestsPass}}}, base, cand)
	if ok || len(diffs) != 1 || diffs[0].Kind != core.GateAllTestsPass {
		t.Fatalf("all_tests_pass: ok=%v diffs=%+v", ok, diffs)
	}
}
