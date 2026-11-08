---
description: Close finished agent windows in the current tmux session (previews first)
---

Parse $ARGUMENTS and run `agentctl sweep` as a Bash tool call. Sweep closes finished windows in the *current* tmux session only, and reclaims their worktrees when that is safe. It never closes the calling window, `shell`, `improve~*` windows, or untracked windows.

Because it closes windows, preview first:

- No arguments → run `agentctl sweep --dry-run`, show the plan, and ask the user whether to proceed. Only on confirmation run `agentctl sweep`.
- `now` or `run` → skip the preview and run `agentctl sweep` (the user already asked for it explicitly).
- `bare` → add `--include-bare` (also closes done, idle bare/interactive windows). Preview first unless combined with `now`/`run`.
- `json` → add `--json`.
- `--session <name>` → pass through (required when not running inside tmux).

Examples: `bare now` → `agentctl sweep --include-bare`; `json` → `agentctl sweep --dry-run --json`.

After a real run, summarize what was closed and list the windows sweep reported as needing attention, with the action each needs.
