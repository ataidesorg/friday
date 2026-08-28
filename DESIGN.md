# Design

## Theme

Friday is a focused terminal workbench for a developer in a long coding session. The default should feel native to the terminal, with a polished light theme available for daytime use and a quiet dark theme for dim rooms.

## Color

Use restrained color. Accent color is for the active selection, Friday's voice, progress, and primary decisions. Warnings, failures, and approvals use semantic color only when they carry meaning.

Palette intent:

- Default: adaptive terminal-native neutrals with a single confident accent.
- Light: warm paper canvas, not pure white.
- Dark: quiet low-glare background, not neon.
- ANSI: structural fallback for restricted terminals.

Never rely on color alone. Every state must have text, placement, or shape as a backup.

## Typography

Use the terminal's own font. Hierarchy comes from spacing, weight where available, concise labels, and stable placement. Avoid decorative ASCII art beyond the restrained Friday wordmark and structural frames.

Transcript prose should wrap cleanly. Chrome labels should be short enough to survive narrow terminals.

## Layout

The primary shape is:

1. Header: workspace, branch, context.
2. Scrollback: conversation first, tool activity only when useful.
3. Composer: the place for prompts, queued work, approvals, questions, and mode/model/usage labels.
4. Footer: only keys that work in the current state.

Overlays and panes should feel like focused instruments, not documentation dumps. They must be searchable or dismissible, fit narrow terminals, and keep the conversation intact behind them.

## Components

- Composer: rounded terminal frame, one clear input area, route and mode label in the frame.
- Palette: grouped, searchable, ranked by task value; avoid exposing every command at equal weight.
- Approval card: action, target, consequence, and choices inside the composer.
- Dashboard: session roster with clear state groups and safe destructive actions.
- Queue strip: queued prompts and their order, anchored above the composer and expandable without changing the queue.
- Management panes: installed items first, local add/remove instructions second.

## Interaction

Keyboard is the primary input. Required affordances:

- Enter sends unless multiline mode is active.
- Alt+Enter inserts a newline unless multiline mode swaps it.
- Ctrl+P opens the command palette.
- Shift+Tab cycles mode.
- Ctrl+C and Ctrl+Q require a second press before quitting.
- Esc closes transient surfaces before it clears or rewinds; while a turn is running, Esc cancels that turn.
- Transcript copy is app-managed: drag selects a framed message body and copies it without trailing padding. Wheel scrolling uses mouse cell motion. Native terminal selection is not available while the chat program is capturing the mouse.
- Multiline paste should show a compact `[Pasted +N lines]` marker in the composer while preserving the full submitted text.

No interaction should silently discard a prompt, a session, a tool decision, or a queued item.

## Copy

Use short product language. Prefer verbs over explanations: "Resume session", "Delete session", "Queued prompts", "Connect provider". Implementation details belong in docs, not the main surface, unless they are needed for trust.

## Production Checks

Before calling a TUI slice production-ready:

- Test no-color, light, dark, and narrow terminals.
- Test long paths, long model ids, long session titles, many sessions, and empty states.
- Confirm transcript rows copy cleanly without trailing padding.
- Run `go test -race -count=1 ./internal/tui/` and `go test -race -count=1 ./internal/cli/` separately.
- Do not claim a stage gate without the full gate battery and owner approval.
