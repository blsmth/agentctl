---
description: Show agent status across every agentctl session (list, detail, peek, whoami)
---

Parse $ARGUMENTS and run `agentctl status` as a Bash tool call. It is a read-only query against agentctl's snapshot of every live session.

Mapping:
- No arguments → `agentctl status`
- `blocked` → `agentctl status --blocked`
- `working`, `idle`, `done` → `agentctl status --state <word>`
- `verify` → `agentctl status --verify` (captures each pane and flags any that contradict the recorded state)
- `json` → add `--json` to whichever form is selected
- `peek <window>` → `agentctl status peek <window>` (optionally `--lines <N>`, default 40)
- `whoami` → `agentctl status whoami`
- A window reference (`@275`, `275`, or an agent name) → `agentctl status <window>` for the detail view
- `--session <name>` limits the list to sessions matching that name or project

Flags the user typed (`--json`, `--state`, `--blocked`, `--verify`, `--session`, `--lines`) pass through unchanged. Several words may combine, e.g. `blocked json` → `agentctl status --blocked --json`.

Notes:
- `PARENT` is blank for windows that were not dispatched.
- A warning on stderr means the monitor's snapshot is stale.
- `status` lists agents from the monitor's snapshot, so it needs a running agentctl session; `whoami` must be run inside a tmux window.
