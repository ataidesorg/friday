package evals

import (
	"strings"
	"testing"
	"time"

	"github.com/ataidesorg/friday/internal/core"
)

func TestJudge(t *testing.T) {
	cost := core.USDMicros(500)
	ok := core.EvaluationResult{
		Passed:  true,
		Elapsed: 200 * time.Millisecond,
		Usage:   core.Usage{InputTokens: 100, OutputTokens: 20},
		Cost:    core.CostReport{Actual: &cost},
	}
	bar := Bar{MaxElapsed: time.Second, MaxInputTokens: 1000, MaxOutputTokens: 100, MaxCost: 1000, MustPass: true}
	v := Judge(ok, bar)
	if !v.Met || len(v.Reasons) != 0 {
		t.Fatalf("ok: %+v", v)
	}
	fail := ok
	fail.Passed = false
	v = Judge(fail, bar)
	if v.Met || !strings.Contains(strings.Join(v.Reasons, " "), "scenario failed") {
		t.Fatalf("must pass: %+v", v)
	}
	slow := ok
	slow.Elapsed = 2 * time.Second
	v = Judge(slow, bar)
	if v.Met || !strings.Contains(strings.Join(v.Reasons, " "), "elapsed") {
		t.Fatalf("elapsed: %+v", v)
	}
	fat := ok
	fat.Usage.InputTokens = 5000
	v = Judge(fat, bar)
	if v.Met || !strings.Contains(strings.Join(v.Reasons, " "), "input tokens") {
		t.Fatalf("tokens: %+v", v)
	}
	pricey := ok
	hi := core.USDMicros(5000)
	pricey.Cost.Actual = &hi
	v = Judge(pricey, bar)
	if v.Met || !strings.Contains(strings.Join(v.Reasons, " "), "cost") {
		t.Fatalf("cost: %+v", v)
	}
	// A zero bar only checks what is set; MustPass false ignores outcome.
	v = Judge(fail, Bar{})
	if !v.Met {
		t.Fatalf("empty bar: %+v", v)
	}
}

func TestMockBar(t *testing.T) {
	b := MockBar()
	if !b.MustPass || b.MaxElapsed < time.Second {
		t.Fatalf("MockBar = %+v", b)
	}
}

func TestFormatVerdict(t *testing.T) {
	cost := core.USDMicros(12)
	res := core.EvaluationResult{Passed: true, Elapsed: time.Second, Usage: core.Usage{InputTokens: 10, OutputTokens: 2}, Cost: core.CostReport{Actual: &cost}, Model: "mock-1"}
	s := FormatVerdict(Judge(res, MockBar()), res)
	for _, want := range []string{"MET", "elapsed", "tokens in=10 out=2", "mock-1"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in %q", want, s)
		}
	}
	miss := FormatVerdict(Verdict{Reasons: []string{"scenario failed"}}, res)
	if !strings.Contains(miss, "MISS") || !strings.Contains(miss, "scenario failed") {
		t.Fatalf("miss: %q", miss)
	}
}
