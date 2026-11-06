---
description: Open formatted session logs for an agent in a temporary tmux window
---

Parse $ARGUMENTS to extract the agent name or window ID (e.g. `go-engineer` or `@41`), then run:

```bash
agentctl logs <agent-name>
```

Substitute the real name. This builds a report of the agent's five most recent sessions and opens it in `less` in a new tmux window named `logs-<agent>` in the agentctl session, then returns immediately; it does not print the report to the Bash tool. The user reads it in that window (`q` quits and closes it). It requires an agentctl tmux session.

If no agent name is given, do not run anything: `agentctl logs-menu` is an interactive picker that cannot work from the Bash tool (no TTY). Tell the user to press `o` in the agentctl tmux keybinding menu for the focused agent, or re-run `/agentctl:logs <agent>` with a name (`/agentctl:status` lists agent names).
