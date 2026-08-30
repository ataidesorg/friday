<p align="center">
  <img src="assets/logo.png" alt="Ink" width="112">
</p>

<h1 align="center">Ink</h1>

<p align="center">
  A local-first agent for making things.
</p>

<p align="center">
  <a href="https://github.com/ataidesorg/ink/actions/workflows/ci.yml"><img src="https://github.com/ataidesorg/ink/actions/workflows/ci.yml/badge.svg?branch=main" alt="CI"></a>
  <a href="https://pkg.go.dev/github.com/ataidesorg/ink"><img src="https://pkg.go.dev/badge/github.com/ataidesorg/ink.svg" alt="Go Reference"></a>
  <a href="https://github.com/ataidesorg/ink/releases/latest"><img src="https://img.shields.io/github/v/release/ataidesorg/ink?display_name=tag" alt="Release"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-blue.svg" alt="License: Apache-2.0"></a>
</p>

<p align="center">
  <a href="#install">Install</a> ·
  <a href="#start">Start</a> ·
  <a href="#what-ink-does">What Ink Does</a> ·
  <a href="#safety-model">Safety</a> ·
  <a href="#development">Development</a>
</p>

Ink is a Go CLI and fullscreen TUI. You make things; Ink puts the marks down — code, a plan, a reply, a file — inside a local repository. It reads the project, talks to your configured model provider, asks before risky actions, edits files through typed tools, runs validation commands, stores redacted session history locally, and keeps the conversation usable while all of that is happening.

The shape is inspired by tools like [Grok Build](https://github.com/xai-org/grok-build) and [OpenCode](https://github.com/anomalyco/opencode), but Ink's line is local control: no Ink server, no Ink account, no hidden remote state.

## Pre-release Warning

> **Pre-release.** Ink is not stable. Commands, config, session formats, provider support, and the TUI can change at any moment without prior notice. Do not treat the current surface as a contract.

The TUI daily-driver slice is gate-green locally, but nothing is frozen until the first stable tag.

## Install

Once releases are published, install the latest binary:

```console
curl -fsSL https://raw.githubusercontent.com/ataidesorg/ink/main/install.sh | bash
ink version
```

Install a specific version:

```console
INK_VERSION=v0.1.0 curl -fsSL https://raw.githubusercontent.com/ataidesorg/ink/main/install.sh | bash
```

Choose the install directory:

```console
INK_INSTALL_DIR=/usr/local/bin curl -fsSL https://raw.githubusercontent.com/ataidesorg/ink/main/install.sh | bash
```

The installer looks for a release archive matching your OS and architecture, verifies `checksums.txt` when `sha256sum` or `shasum` is available, then installs `ink` into:

1. `$INK_INSTALL_DIR`
2. `$XDG_BIN_DIR`
3. `$HOME/bin`
4. `$HOME/.ink/bin`

Build from source:

```console
go install github.com/ataidesorg/ink/cmd/ink@latest
```

Build from a checkout:

```console
git clone https://github.com/ataidesorg/ink.git
cd ink
go build -o bin/ink ./cmd/ink
./bin/ink version
```

See [docs/installation.md](docs/installation.md) for provider setup, upgrades, and manual release downloads.

## Start

Open Ink in a repository:

```console
cd your-project
ink
```

Useful first prompts:

```text
What does this repo do?
Review the current diff.
```

Shell commands need an argv prefix in `tools.commands.allowed` (or a trusted `project.commands.*` entry). Writes still go through the approval UI unless you pass `--yes` or switch to always-approve.

Headless mode for scripts and CI-style checks:

```console
ink run --yes "explain the architecture"
ink run --no-tui --yes "review the current diff"
```

Common TUI controls:

| Key or command | Action |
| --- | --- |
| `Ctrl+P` | Command palette |
| `Shift+Tab` | Cycle mode: normal, plan, auto, always-approve, always-ask |
| `Ctrl+b` | Prompt queue |
| `/model` | Switch route |
| `/agent` | Switch agent profile |
| `/usage` | Toggle context and configured spend caps near the model |
| `/tools` | Show or hide tool activity |
| `/thinking` | Show or hide the live thinking indicator |
| `Esc` | Cancel a running turn or close transient UI |
| `Ctrl+C`, `Ctrl+Q` | Press twice to quit |

## What Ink Does

- Fullscreen terminal chat with boxed user and assistant messages.
- Plain `ink run` mode for automation and non-TTY output.
- Typed tools for reading, writing, patching, searching, asking questions, and running allowed commands.
- Approval UI for file writes, patches, shell commands, MCP tools, deletes, and exits.
- Session persistence with resume, rename, delete, compaction, prompt history, and local event trails.
- Provider routing with named routes, fallbacks, pricing, budgets, keychain/env/secret-store auth, and `/connect`.
- Extensions through skills, commands, MCP servers, custom tools, formatters, and agent profiles.
- Worktree sessions for isolated changes outside the primary checkout.
- Image attachments by path, LSP diagnostics after edits, and copy-clean transcript selection.

## Safety Model

Ink assumes the local machine is trusted and the model is not.

- Repository config is not fully trusted until you accept the trust prompt or run `ink trust`. An answer binds to that exact file content, so editing the file asks again.
- Secrets are resolved from env, keychain, or Ink's encrypted secret store, never from command arguments.
- Logs and session files are local and redacted.
- Unknown tool output, repo content, webpages, and model output are treated as untrusted input.
- Risky side effects go through policy and approval unless the user explicitly runs with `--yes` or a permissive mode. `always-approve` auto-approves remaining policy asks for the session; `auto` auto-approves local writes and allow-listed commands and still asks for destructive work.
- The default process sandbox confines paths, scrubs the command environment, and enforces timeouts. It does not isolate the OS network. Container sandboxing (`sandbox.provider = "container"`) uses Docker or Podman with `--network none` when you opt in.
- Route `max_cost_usd` fails closed when model pricing is unknown. Default session/task budgets warn and continue when a model has no price.

Read [SECURITY.md](SECURITY.md) and [docs/security/threat-model.md](docs/security/threat-model.md) before using Ink on sensitive repositories.

## Extensions

Ink has two user-facing extension concepts, both plain files on disk:

| Concept | What it is | Where it shows up |
| --- | --- | --- |
| Skill | Reusable instructions the agent can follow for a workflow or domain | `/skills`, skill tool |
| Command | Saved prompt you run with `/name` | slash popup |

Skills live in `skills/<name>/SKILL.md` and commands in `.ink/commands/<name>.md`, under your Ink home first and then the repository; a project entry replaces a user entry with the same name. There is no install step and no marketplace. MCP servers are configured separately in user-layer `[mcp.NAME]` tables — never from a repository's config.

## Configuration

Create project config:

```console
ink init
```

Inspect effective config:

```console
ink config show
ink config validate
ink config explain models.routing.default
```

Connect a provider interactively:

```console
ink
/connect
```

Declarative provider and route example:

```toml
[providers.fireworks]
kind = "openai_compatible"
base_url = "https://api.fireworks.ai/inference/v1"
privacy = "public_cloud"

[providers.fireworks.auth]
source = "env"
name = "FIREWORKS_API_KEY"

[models.routes.fast]
provider = "fireworks"
model = "accounts/fireworks/models/deepseek-v4-flash-0731"

[models.routing]
default = "fast"
```

## Development

Requirements:

- Go 1.27, from `go.mod`.
- `staticcheck`, `golangci-lint`, `govulncheck`, and `gitleaks` for strict local gates.
- Optional: `just`, `actionlint`.

Common commands:

```console
just build
just test
just check
scripts/check.sh --strict
```

Run TUI and CLI race suites separately when iterating on chat:

```console
go test -race -count=1 ./internal/tui/
go test -race -count=1 ./internal/cli/
```

## Releases

Release archives are produced by [GoReleaser](https://goreleaser.com) from [.goreleaser.yaml](.goreleaser.yaml) and published by [.github/workflows/release.yml](.github/workflows/release.yml) when a `v*` tag is pushed.

```console
GORELEASER_CURRENT_TAG=v0.1.0 goreleaser release --snapshot --clean
git tag v0.1.0
git push origin v0.1.0
```

See [docs/git-strategy.md](docs/git-strategy.md) for the branching and tagging
model, and [docs/releasing.md](docs/releasing.md) for the full checklist.

## License

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE). Release history lives in [CHANGELOG.md](CHANGELOG.md).
