---
description: Show formatted session logs for an agent in a tmux pane
---

Parse $ARGUMENTS to extract the agent name, then run:

```bash
agentctl logs <agent-name>
```

Replace `<agent-name>` with the agent name from $ARGUMENTS. If no agent name is provided, show the logs menu:

```bash
agentctl logs-menu
```

This opens a formatted session log report via `less` in a temporary tmux window.
