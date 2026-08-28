# Non-goals

What Friday refuses to be, and the one reason each. See [vision.md](vision.md) for what it is.

1. **Not a hosted platform.** Friday is a CLI binary (`cmd/friday`) that reads and writes local
   files; there is no server, no listener, and the default telemetry mode is `local` — nothing is
   sent anywhere (`internal/config/defaults.toml`).

2. **No multi-tenant model.** Nothing in `internal/core` models an account, an organisation, or a
   tenant; `core.Principal` (`internal/core/task.go`) identifies an actor on one machine, not a
   customer of a shared service.

3. **No autonomous self-modification.** `core.DefaultReleaseGate().RequiresHumanApproval` is
   `true` — verify with `go doc ./internal/core DefaultReleaseGate` — and asserted by
   `internal/core/domain_test.go:TestUsageAddAndGate`. The Stage 4 plan requires an interactive
   human approval before any improvement proposal promotes
   (tasks/stage-4-evals-improvement-lab.md).

4. **No "better than X" claims without a controlled comparison.** Any comparative or performance
   claim must come from `evals.Runner` scenarios against a recorded baseline — see
   [evaluation strategy](../evals/evaluation-strategy.md). There is no baseline store yet, so
   no comparative claim is made at all.

5. **Not an IDE fork.** Friday is a CLI and terminal UI that edits files and
   runs commands in a workspace. It does not embed or wrap an existing editor.
   Optional LSP diagnostics after edits come from user-configured stdio
   language servers (`[lsp.NAME]`), not an IDE.

6. **Not a cloud sandbox service.** The default is a local `os/exec` process
   sandbox, documented as **not** a security boundary and **not** OS network
   isolation. An optional local Docker/Podman container provider exists;
   Friday does not ship a hosted sandbox runtime.

7. **No auto-synthesised skills.** Skills are reviewed files treated as
   context, never as executable instructions. Friday does not generate and
   install new skills on its own.
