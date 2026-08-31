# Changelog

All notable changes to Ink are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and Ink uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Until `v1.0.0`, config keys, session formats, and internal APIs may change in
a minor release. Breaking changes are always listed under **Changed**.

## [Unreleased]

### Changed

- Renamed the product from Friday to Ink. The binary is `ink`, the module is
  `github.com/ataidesorg/ink`, project config lives under `.ink/`, and env
  overrides use the `INK__` prefix (`INK_HOME`, `INK_CONFIG_DIR`,
  `INK_STATE_DIR`). The default theme is `ink`: quiet black, graphite accent,
  no pink. The Friday avatar is gone; the mark is a drop of ink
  (`assets/logo.png`), including the TUI welcome wordmark. This is a breaking
  pre-release change; old `FRIDAY_*`
  variables and `.friday` paths are not read.
- Profile `style` (`concise` | `detailed`) is injected into the system prompt.
- Removed unused config: `[memory]`, `profiles.*.harness`,
  `profiles.*.memory_namespace`, `profiles.*.sensitivity_cap`, the
  `[profiles.assistant]` built-in, `telemetry.mode`, and
  `telemetry.retention_days`. The event trail is always local; set
  `telemetry.privacy` only.
- `/` typeahead, Ctrl+P, and slash dispatch share one named command table,
  including custom commands. The slash menu groups commands, ranks by name,
  highlights the selection, windows to the terminal, and wraps at the ends.
  Custom command files are reserved against that table, not a parallel name
  list. Display toggles from the palette persist in the Ink home.

### Added

- Built-in themes `carbon`, `sepia`, `moss`, `wine`, and `sea`. The theme
  picker shows a short label next to each name.
- Session-scoped goals (`/goal`, `ink run --goal`) that stay active until
  `goal_complete` records command, test, file, or eval evidence. Prose "done"
  does not complete a goal. Turn, no-progress, and token caps pause automatic
  continuation; `goal_blocked` and `goal_wait` stop the loop for a reason.
  File evidence is checked by the harness: the path must have been written
  this run, not merely claimed.
- Approval card in the composer with the action, preview, and a selectable
  list (allow once / this session / reject). A denied write is not retried
  in the same run and does not land on disk.
- `/advisories` hides unpriced-model and unverified-result warnings for the
  session (`tui.hide_advisories` in config for the default).
- A chat session with no turns is discarded on quit and on `/new`, so opening
  Ink and leaving does not leave an empty session behind.

## [0.1.0] - 2026-08-28

### Added

- Fullscreen TUI chat and a headless `ink run` mode for scripts and CI.
- Typed file, search, and command tools behind a policy and approval layer.
- Process sandbox with path confinement, environment scrubbing, and timeouts;
  optional container sandbox over Docker or Podman with `--network none`.
- Provider adapters, model routing, rotation, and per-route cost caps that fail
  closed when pricing is unknown. Fireworks is the owner-verified path.
- Auth via environment, OS keychain, device code, OAuth2, Azure, Copilot, and
  external CLIs; secrets never travel in command arguments.
- Local JSONL session store with redaction, replay, compaction, and metrics.
- Skills and saved slash commands, discovered from the user home and the repository.
- Layered configuration with per-repository trust gating for `.ink/config.toml`.
- MCP client, LSP integration, git worktree workflows, and an evals harness.
- `install.sh` with checksum verification, and reproducible cross-platform
  release archives built by GoReleaser.

[Unreleased]: https://github.com/ataidesorg/ink/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/ataidesorg/ink/releases/tag/v0.1.0
