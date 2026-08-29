# Changelog

All notable changes to Friday are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and Friday uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Until `v1.0.0`, config keys, session formats, and internal APIs may change in
a minor release. Breaking changes are always listed under **Changed**.

## [Unreleased]

### Added

- Session-scoped goals (`/goal`, `friday run --goal`) that stay active until
  `goal_complete` records command, test, file, or eval evidence. Prose "done"
  does not complete a goal. Turn, no-progress, and token caps pause automatic
  continuation; `goal_blocked` and `goal_wait` stop the loop for a reason.
- Fullscreen TUI chat and a headless `friday run` mode for scripts and CI.
- Typed file, search, and command tools behind a policy and approval layer.
- Process sandbox with path confinement, environment scrubbing, and timeouts;
  optional container sandbox over Docker or Podman with `--network none`.
- Provider adapters, model routing, rotation, and per-route cost caps that fail
  closed when pricing is unknown. Fireworks is the owner-verified path.
- Auth via environment, OS keychain, device code, OAuth2, Azure, Copilot, and
  external CLIs; secrets never travel in command arguments.
- Local JSONL session store with redaction, replay, compaction, and metrics.
- Skills and saved slash commands, discovered from the user home and the repository.
- Layered configuration with per-repository trust gating for `.friday/config.toml`.
- MCP client, LSP integration, git worktree workflows, and an evals harness.
- `install.sh` with checksum verification, and reproducible cross-platform
  release archives built by GoReleaser.

[Unreleased]: https://github.com/ataidesorg/friday/commits/main
