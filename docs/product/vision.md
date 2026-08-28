# Product vision

## Who this is for

One developer, running Friday on their own machine, against their own repositories (and, from
Stage 6, their own calendar, mail, and notes). Not a team tool. Not a hosted product. Not
multi-user. If a decision would only make sense for a shared deployment, it is out of scope —
see [non-goals.md](non-goals.md).

## Two harnesses, one core

Friday defines one harness type, `core.HarnessKind` (`internal/core/agent.go`), with two values:

- `core.HarnessCode` (`"code"`) — plans, edits, runs commands, and validates inside a project
  workspace. This is the current Friday CLI/TUI harness.
- `core.HarnessAssistant` (`"assistant"`) — the same lifecycle applied to personal tasks
  (calendar, mail, notes, reminders) through typed integration tools with declared scopes. The
  Stage 6 target: see tasks/stage-6-personal-assistant.md.

Both harnesses share one `core.Task` / `core.Run` lifecycle, one policy engine, one event trail.
`core.NewTask` takes a `HarnessKind` as a required argument, not an optional flavour bolted on
after the fact (`internal/core/task.go`; exercised by `TestNewTask`,
`internal/core/domain_test.go`). The code harness runs today (`friday` chat and `friday run`).
The assistant harness is Stage 6.

## Principles, stated as things you can check

Every claim below points at a concrete identifier and the test that enforces it, not a promise.

1. **Every side effect is an event.** `core.EventKind` enumerates 16 payload types
   (`internal/core/event_payloads.go`); encode/decode round-tripping and redaction are enforced by
   `internal/core/events_test.go` (`TestEventRoundTrip`, `TestEventTrailReplay`).

2. **Nothing is allowed by default.** The policy engine used until one is configured is
   `core.DenyAll`, which denies every request — including read-only ones
   (`internal/core/defaults.go`; `internal/core/defaults_test.go:TestDenyAll`). The shipped
   default profile keeps that posture: `tools.default_effect = "deny"` in
   `internal/config/defaults.toml`, with read-only tools explicitly allow-listed.

3. **An unimplemented feature fails loudly, never silently.** `core.NotImplementedError{Feature}`
   (`internal/core/errors.go`) is returned wherever a path is not built — the `acp` wire
   (`internal/cli/provider.go`), sandbox snapshots (`internal/sandbox/process`), the
   `memory_written` eval expectation (`internal/evals/checks.go`) — and
   the same honesty reaches the CLI: `friday run` exits 6 instead of fabricating a result
   (`internal/cli/run.go`; `internal/cli/run_test.go:TestRunExitCodes`).

4. **Secrets never enter config, logs, or memory.** `internal/redact` scrubs built-in secret
   shapes (private keys, OpenAI/GitHub/Slack/AWS/Google keys, JWTs, bearer tokens, generic
   `key=value` assignments) plus any literal at least `MinLiteralLen` (8) bytes long
   (`internal/redact/redact.go`; `internal/redact/redact_test.go:TestLiteralSecrets`,
   `TestRedactIsIdempotent`). `core.NewMemoryCandidate` refuses secret-classified content
   (`internal/core/domain_test.go:TestNewMemoryCandidate`). `internal/config/validate.go` rejects
   any secret-shaped value anywhere in a config file (`internal/config/validate_test.go`). And
   `core.RedactingSink` never forwards a raw event — a redaction failure surfaces as a `Warning`
   event, never a silent drop (`internal/core/events_test.go:TestRedactingSink`,
   `TestRedactedLiteralNeverSurvives`).

5. **Self-improvement never auto-promotes.** `core.DefaultReleaseGate().RequiresHumanApproval`
   is `true` (`internal/core/evaluation.go`; verify with `go doc ./internal/core
   DefaultReleaseGate`), asserted by `internal/core/domain_test.go:TestUsageAddAndGate`.

6. **Privacy only ever gets stricter on fallback.** `core.PrivacyClass.AllowsFallbackTo` refuses
   to fall back from a private route to a less private one, and fails closed — returns `false` —
   when either class is unrecognised (`internal/core/model.go`;
   `internal/core/domain_test.go:TestPrivacyFallback`).

7. **A repository cannot reach the network or run commands on its own say-so.** The
   project config layer (`.friday/config.toml`) merges freely except for the keys that
   can do real damage — `providers.*`, `mcp.*`, `lsp.*`, `tools.*`, `sandbox.*`,
   `budgets.*`, `telemetry.*` (`config.ProjectLayerGatedPrefixes`) and
   `project.commands.*`. Those merge only after a yes — at the startup prompt or via
   `friday trust` — on that exact file content; a no binds to that content too, so an
   edit asks again. Until then they are dropped and recorded as a rejected provenance entry
   (`Entry.Rejected`) rather than silently ignored or applied. `test/invalid-project`
   exists specifically to trip this — see
   `internal/config/validate_test.go:TestValidateInvalidFixture`.

## What "local-first" means, concretely

- **User config** lives at the first of `$FRIDAY_CONFIG_DIR`, `$XDG_CONFIG_HOME/friday`, or
  `$HOME/.config/friday` that is set (`config.Dir`, `internal/config/paths.go`;
  `internal/config/load_test.go:TestDir`).
- **Project config** lives at `.friday/config.toml` under the project root. The project root
  defaults to the current working directory unless `--project` or workspace mode points
  Friday elsewhere (`internal/config/paths.go`; `internal/cli/config.go`).
- **No service, no account.** The boundaries Friday talks to are named once, in
  `internal/core/contracts.go`: a model provider, a sandbox, typed tools, a memory store, a
  policy engine, an evaluation runner. Each is either a local implementation or an explicit
  `Unavailable*` stub. The default `[providers]` table is empty until the user connects
  or configures a provider, and the default sandbox is the local `process` provider
  (`internal/config/defaults.toml`).
- **Telemetry stays on the machine.** The default is `telemetry.mode = "local"`
  (`internal/config/defaults.toml`); Friday has no hosted telemetry export path.
- **Runtime state** — event logs, sessions, compaction state, and local run data —
  lives under `.friday/local/` or the Friday home directory and is excluded from
  version control (`.gitignore`).
