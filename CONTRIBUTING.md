# Contributing to Friday

Friday is pre-release and moving quickly. Contributions are welcome when they
respect the product line: local-first, explicit side effects, no fake support,
and a TUI that earns daily use.

## Development Setup

Requirements:

- Go 1.27.0, pinned in `go.mod`.
- `staticcheck`, `golangci-lint` v2, `govulncheck`, and `gitleaks` for strict
  local checks.
- Optional: `just`, `actionlint`, and `goreleaser` v2 for release packaging.

Install the pinned local gate tools:

```console
go install honnef.co/go/tools/cmd/staticcheck@v0.8.1
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.1
go install golang.org/x/vuln/cmd/govulncheck@v1.7.0
go install github.com/zricethezav/gitleaks/v8@v8.30.1
```

## Workflow

1. Branch from `main`. Branch naming, merge strategy, and release flow:
   [docs/git-strategy.md](docs/git-strategy.md).
2. Write or update tests before the implementation when behavior changes.
3. Keep files small, names direct, and package boundaries intact.
4. Run the relevant focused tests while iterating.
5. Run the full gate before opening a PR.

```console
scripts/check.sh --strict
```

When changing chat behavior, run these separately:

```console
go test -race -count=1 ./internal/tui/
go test -race -count=1 ./internal/cli/
```

## Code Style

Follow [Effective Go](https://go.dev/doc/effective_go). The gate enforces the
mechanical parts, so a clean `scripts/check.sh --strict` means these already
hold:

- `gofmt` decides all formatting. Do not hand-align.
- Every package carries a package comment. Every exported identifier's doc
  comment starts with that identifier's own name.
- Initialisms stay capitalized: `ID`, `URL`, `HTTP` — never `Id`, `Url`, `Http`.
- One consistent receiver name per type.
- Error strings are lowercase and unpunctuated, and wrap with `%w` when a
  caller may need `errors.Is` or `errors.As`.
- Getters drop the `Get` prefix.
- Return errors; do not `panic` in library code.

`.golangci.yml` sets staticcheck to `checks: ["all"]`, which re-enables the
ST1000/ST1003/ST1016/ST1020-22 style checks. golangci-lint leaves those off by
default, and they are exactly the ones that encode the conventions above.

## Commit Style

Use conventional commits:

```text
feat: add provider setup wizard
fix: keep prompt queue stable when empty
docs: document release packaging
```

Allowed types: `feat`, `fix`, `refactor`, `docs`, `test`, `chore`, `perf`,
`ci`.

## Review Checklist

- Tests cover the behavior that changed.
- No secret-shaped literals are present in code, docs, fixtures, or tests.
- No new third-party dependency appears without an ADR and archtest update.
- `friday run` non-TTY output still works when TUI code changes.
- Risky actions remain policy-checked and attributable.
- Docs are updated when commands, config, install, release, or user-visible
  behavior changes.
- TUI changes are checked in no-color mode and at a narrow terminal width.

## Architecture Guardrails

The import direction is enforced by `internal/archtest/deps_test.go`.

- `internal/core` stays I/O-free.
- `internal/config` owns layered config and trust decisions.
- `internal/runtime` owns the agent loop.
- `internal/tui` owns presentation and input, but must preserve plain-output
  parity for `friday run`.
- `internal/cli` wires packages together; `cmd/friday` is only the entrypoint.

Allowed direct third-party modules are intentionally small. Adding one needs a
documented decision, not just `go get`.

## Security

Do not put secrets in project config, docs, prompts, snapshots, logs, tests, or
git history. Use env, keychain, or Friday's encrypted secret store.

Read [SECURITY.md](SECURITY.md) and
[docs/security/threat-model.md](docs/security/threat-model.md) before working on
auth, providers, sandboxing, tools, MCP, or logging.

## Releases

Maintainers publish releases from tags. See [docs/git-strategy.md](docs/git-strategy.md)
for the branching and tagging model, and [docs/releasing.md](docs/releasing.md) for
the checklist.

## License

Apache-2.0. By contributing, you agree your contribution is licensed under the
same terms. See [LICENSE](LICENSE) and [NOTICE](NOTICE).

## Code of Conduct

See [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).
