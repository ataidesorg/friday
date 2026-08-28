package core

import "time"

// ExpectationKind is a deterministic check an evaluation scenario asserts.
type ExpectationKind string

// Expectation kinds; each uses the Expectation fields named in its comment.
const (
	ExpectFileExists       ExpectationKind = "file_exists"       // Path
	ExpectFileContains     ExpectationKind = "file_contains"     // Path, Needle
	ExpectFileSHA256       ExpectationKind = "file_sha256"       // Path, SHA256
	ExpectCommandSucceeds  ExpectationKind = "command_succeeds"  // Argv
	ExpectCommandFails     ExpectationKind = "command_fails"     // Argv
	ExpectApprovalRequired ExpectationKind = "approval_required" // Risk
	ExpectMemoryWritten    ExpectationKind = "memory_written"    // Memory (namespace)
	ExpectNoSecretLeak     ExpectationKind = "no_secret_leak"    // (trail scan)
)

// Expectation is one check with the parameters its kind needs.
type Expectation struct {
	Kind   ExpectationKind `json:"kind"`
	Path   string          `json:"path,omitempty"`
	Needle string          `json:"needle,omitempty"`
	SHA256 string          `json:"sha256,omitempty"`
	Argv   []string        `json:"argv,omitempty"`
	Risk   RiskClass       `json:"risk,omitempty"`
	Memory string          `json:"memory,omitempty"`
}

// EvaluationScenario is a reproducible task against a fixture.
type EvaluationScenario struct {
	ID           ScenarioID    `json:"id"`
	Name         string        `json:"name"`
	Description  string        `json:"description,omitempty"`
	Fixture      string        `json:"fixture"`
	Task         string        `json:"task"`
	Expectations []Expectation `json:"expectations"`
	Tags         []string      `json:"tags,omitempty"`
}

// CheckResult is the outcome of one expectation.
type CheckResult struct {
	Expectation Expectation `json:"expectation"`
	Passed      bool        `json:"passed"`
	Detail      string      `json:"detail,omitempty"`
}

// EvaluationResult records one scenario run with full attribution.
type EvaluationResult struct {
	Scenario       ScenarioID      `json:"scenario"`
	Run            RunID           `json:"run"`
	Passed         bool            `json:"passed"`
	Checks         []CheckResult   `json:"checks"`
	Usage          Usage           `json:"usage"`
	Cost           CostReport      `json:"cost"`
	Elapsed        time.Duration   `json:"elapsed"`
	Failure        FailureCategory `json:"failure,omitempty"`
	HarnessVersion string          `json:"harness_version"`
	Commit         string          `json:"commit"`
	Provider       string          `json:"provider"`
	Model          string          `json:"model"`
	Route          string          `json:"route"`
}

// GateCheckKind is a deterministic promotion criterion.
type GateCheckKind string

// Gate check kinds.
const (
	GateNoRegression       GateCheckKind = "no_regression"
	GateMinPassRate        GateCheckKind = "min_pass_rate"
	GateMaxCostIncrease    GateCheckKind = "max_cost_increase"
	GateMaxLatencyIncrease GateCheckKind = "max_latency_increase"
	GateAllTestsPass       GateCheckKind = "all_tests_pass"
)

// GateCheck is one criterion with its threshold where applicable.
type GateCheck struct {
	Kind    GateCheckKind `json:"kind"`
	Percent int           `json:"percent,omitempty"`
}

// ReleaseGate is the set of checks a proposal must pass before promotion.
type ReleaseGate struct {
	ID                    GateID      `json:"id"`
	Name                  string      `json:"name"`
	Checks                []GateCheck `json:"checks"`
	RequiresHumanApproval bool        `json:"requires_human_approval"`
}

// DefaultReleaseGate requires no regression, passing tests, and a human.
func DefaultReleaseGate() ReleaseGate {
	return ReleaseGate{
		Name:                  "default",
		Checks:                []GateCheck{{Kind: GateNoRegression}, {Kind: GateAllTestsPass}},
		RequiresHumanApproval: true,
	}
}
