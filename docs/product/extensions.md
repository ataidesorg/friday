# Extensions

Ink extends through local files, not a marketplace. There are three kinds,
and every one of them is a directory or a file you can read.

| Concept | Meaning | Surface |
| --- | --- | --- |
| Skill | Reusable instructions an agent can follow | `/skills`, skill tool |
| Command | Saved prompt launched with `/name` | slash popup |
| MCP server | Local stdio bridge from user config | `[mcp.NAME]` |

## Location

Skills and commands are found in the user home first, then the project. A
project entry replaces a user entry with the same name.

| Kind | User | Project |
| --- | --- | --- |
| Skill | `~/.ink/skills/<name>/SKILL.md` | `<repo>/skills/<name>/SKILL.md` |
| Command | `~/.ink/commands/<name>.md` | `<repo>/.ink/commands/<name>.md` |

There is no install step, no manifest, and no enable/disable state file: a
skill exists because its directory exists. Delete the directory to remove it.

MCP servers are the exception — they are configuration, not files on a search
path, and they come from user-layer `[mcp.NAME]` tables. They never come from
a project's config, because a repository must not be able to point Ink at a
server of its choosing.

## Why No Plugin Layer

Ink used to have plugins: a `module.toml` manifest, `~/.ink/modules/`
and `.ink/modules/` roots, and gitignored enable state per layer. It bought
nothing. A plugin was only a pair of extra search paths for the same `skills/`
and `commands/` directories that already load, plus a second way to turn a
skill off, plus a name that implied code execution that never happened. It
was removed. Copy the skill directory instead.

## Mapped, Not Vendored

| Product | Why it is not in tree | How to use it |
| --- | --- | --- |
| Vercel Passport | Hosted IdP gate; Ink is one local principal | `ink trust` and `/connect` |
| Vercel Connect | Hosted token broker | User-layer MCP or `/connect` |
| Unabyss, Memori, Composio | Network services and their SDKs | `[mcp.*]` user config |
| HelixDB | Second datastore; ADR-0002 is SQLite-first | Optional user MCP |
| Strix, PentAGI, Piolium | Pentest runtimes that execute attack code | Out of tree |
| LobeHub, OpenUI | Web chat or generative UI | Out of scope for a terminal agent |

## Product Rule

The UI never shows a remote marketplace it does not have. `/skills` lists what
is on disk, says where it came from, and tells you the path to add another.
