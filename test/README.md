# Test data and fixture projects

External test applications and test data, per the standard Go project layout.
Small, deterministic projects that config tests, CLI tests, and evaluation
scenarios point at. Each fixture project is its own Go module with no
`go.work`, so `./...` from the repository root never builds, lints, or tests
them.

| Fixture | Purpose |
| --- | --- |
| `sample-project/` | Valid project: one package, one test, `.ink/config.toml` that sets only ungated keys (`project.*`, `memory.*`). Its `[project.commands]` (`test`, `build`) apply only after `ink trust` on that file — untrusted runs drop them and cannot verify. |
| `scripts/` | Scripted mock-provider transcripts for `ink run --script` and the eval runner: `add-farewell.json` (happy path) and `forbidden-rm.json` (denied destructive command). |
| `scenarios/` | Evaluation scenarios for `ink eval run`; `fixture` paths are relative to the scenario file. `001-add-farewell.json` runs the add-farewell script on a copy of `sample-project/`. |
| `invalid-project/` | `.ink/config.toml` that must fail validation with exactly one error (`evals.gate = "loud"` is not an accepted value) while also tripping the trust gate: `sandbox.provider` is gated, so the loader records the attempt as rejected and never applies it. |

Rule: no secret-shaped literals in this directory, ever. Tests that need one
construct it from fragments at runtime.
