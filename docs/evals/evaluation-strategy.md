# Evaluation strategy

How Friday will measure whether a change made things better or worse. The types below
exist today (`internal/core/evaluation.go`), and a minimal runner executes one scenario
at a time (`internal/evals`, `friday eval run`) — see [Status](#status).

## Scenario model

An `EvaluationScenario` is a reproducible task against a checked-in fixture, not a
free-form prompt graded by another model call:

```go
type EvaluationScenario struct {
    ID           ScenarioID
    Name         string
    Description  string
    Fixture      string        // e.g. test/sample-project
    Task         string        // the task text given to the harness
    Expectations []Expectation
    Tags         []string
}
```

Each `Expectation` is one deterministic, mechanical check:

| Kind | Fields used | Checks |
|---|---|---|
| `file_exists` | `Path` | a file exists at `Path` |
| `file_contains` | `Path`, `Needle` | `Path` contains the substring `Needle` |
| `file_sha256` | `Path`, `SHA256` | `Path`'s content hashes to `SHA256` |
| `command_succeeds` | `Argv` | running `Argv` exits 0 |
| `command_fails` | `Argv` | running `Argv` exits non-zero |
| `approval_required` | `Risk` | a capability at that `RiskClass` triggered an approval |
| `memory_written` | `Memory` (namespace) | a memory record landed in that namespace |
| `no_secret_leak` | — (scans the run's event trail) | no secret-shaped value appears anywhere in the trail |

`no_secret_leak` is the mechanical link to
[`docs/security/threat-model.md`](../security/threat-model.md)'s secret-exfiltration
row: it re-runs the same redaction check against the full event trail a scenario
produced, rather than trusting that redaction "worked" by inspection.

## Determinism

Every scenario runs against a `Fixture` — a checked-in project state, e.g.
`test/sample-project` or `test/invalid-project` — so a scenario's outcome
doesn't depend on ambient network state, and its pass/fail check is a mechanical
assertion (file hash, exit code, event-trail scan), not an LLM grading another LLM's
output. Determinism here is about the *check*, not about the model's output: the model
may still vary between runs, but whether it satisfied the scenario is decided by code.

## Metrics and attribution

Every scenario run produces one `EvaluationResult`:

```go
type EvaluationResult struct {
    Scenario       ScenarioID
    Run            RunID
    Passed         bool
    Checks         []CheckResult   // one per Expectation, with a Detail on failure
    Usage          Usage
    Cost           CostReport
    Elapsed        time.Duration
    Failure        FailureCategory // set when Passed is false
    HarnessVersion string
    Commit         string
    Provider       string
    Model          string
    Route          string
}
```

The last five fields are not optional metadata — they are what makes a number
comparable at all. A pass rate or a cost figure without harness version, commit,
provider, model, and route attached is an anecdote, not a result.

## Release gate

A `ReleaseGate` is the set of checks a change must pass before promotion:

```go
type ReleaseGate struct {
    ID                    GateID
    Name                  string
    Checks                []GateCheck // Kind + optional Percent threshold
    RequiresHumanApproval bool
}
```

`GateCheckKind` has five values: `no_regression`, `min_pass_rate`,
`max_cost_increase`, `max_latency_increase`, `all_tests_pass`. The default gate,
`DefaultReleaseGate()`, uses only two of them:

```go
ReleaseGate{
    Name:                  "default",
    Checks:                []GateCheck{{Kind: GateNoRegression}, {Kind: GateAllTestsPass}},
    RequiresHumanApproval: true,
}
```

Config defaults (`internal/config/defaults.toml`) set `evals.gate = "required"` and
`evals.min_pass_rate = 100` — but nothing enforces those yet; see Status.

This is the mechanism Stage 4's self-improvement loop promotes through:
`ImprovementProposal` → `ImprovementExperiment` (baseline `EvaluationResult`s vs.
candidate `EvaluationResult`s on the same scenarios, `PassedGates bool`) → promotion.
> "Self-improvement never auto-promotes; human approval is the default."
`RequiresHumanApproval: true` on the default gate is that rule expressed as code, not
just policy.

## Reporting rule

> "Do not fabricate command output, benchmark results, provider support, OAuth scopes,
> security guarantees, or completed implementations."

> "Never publish or imply 'better than X' without a controlled comparison."

Concretely: every published number carries harness version, commit, provider, model,
and route (the five attribution fields above). A comparison is only a comparison when
baseline and candidate ran the same scenarios under those same five coordinates modulo
the one thing being changed. No numbers are published yet — the only scenario on disk is
a smoke test against a scripted model, which measures the harness, not a provider.

## Status

`evals.Runner` (`internal/evals/runner.go`) is the only runner. It runs one scenario per
instance: it copies the fixture into an ephemeral workspace, runs the task through
`runtime.Run` with the scripted provider, then checks every expectation against the
resulting tree, the redacted trail, and a fresh process sandbox for `command_*` kinds.
`memory_written` returns `NotImplementedError` until a memory store exists. `friday eval run
SCENARIO.json --script FILE` prints one line per check and exits 0 only when the run
did not fail and every check passed. `test/scenarios/001-add-farewell.json` is the
first scenario (`file_contains`, `command_succeeds`, `no_secret_leak`).

`implemented: internal/evals/evals_test.go TestRunnerRun, internal/cli/report_test.go`

Known limits: the eval trail is discarded with the ephemeral copy (keep a `friday run`
trail instead), `no_secret_leak` scans the whole tree rather than only changed files,
and results are printed, not stored — the baseline store, suites, and the
`internal/improve` promotion flow are Stage 4 work; see
`tasks/stage-4-evals-improvement-lab.md`
for the build plan.
