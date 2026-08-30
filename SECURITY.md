# Security Policy

Ink is a local-first agent. It can read files, edit files, run
commands, call model providers, and load local extensions when configured to do
so. Treat it like a powerful developer tool, not a sandbox boundary.

## Reporting a Vulnerability

Use GitHub private vulnerability reporting:

1. Open the repository's **Security** tab.
2. Choose **Report a vulnerability**.
3. Include the affected command or file, a minimal reproduction, and the commit
   or release you tested.

Do not open a public issue for a vulnerability report.

This is a single-maintainer pre-release project. Reports are handled best
effort, with priority given to issues that can expose secrets, bypass policy,
write outside the intended workspace, run commands unexpectedly, or corrupt
session history.

## Supported Versions

| Version | Supported |
| --- | --- |
| Latest tagged pre-release | Security fixes when practical |
| Older tags | Best effort only |
| Untagged local builds | Not supported |

## In Scope

- Secret redaction, credential storage, and auth resolution.
- Repository trust and config layering.
- Tool approval and policy enforcement.
- File write, patch, and command execution boundaries.
- MCP, custom tools, and skills when loaded by Ink.
- Session store redaction, local event trails, and transcript export.
- Release artifacts, install script behavior, and checksums.

## Out of Scope

- A compromised local machine, terminal, shell, keychain, or editor.
- Malicious model-provider infrastructure.
- User-approved commands doing what they were allowed to do.
- Third-party tools invoked by user config after approval.
- Skills, commands, or MCP servers installed from untrusted sources.

## Known Posture

- Ink does not ship a hosted control plane.
- Local telemetry is redacted and stored on disk; nothing is sent to a Ink
  service.
- Model output, tool output, repository content, webpages, and retrieved memory
  are untrusted inputs.
- Cost caps fail closed when pricing is unknown.
- Some provider and OAuth code paths are implemented but not owner-verified.
  Unverified integrations should not be treated as stable security boundaries.

Read the full [threat model](docs/security/threat-model.md) for architecture
details and accepted risks.
