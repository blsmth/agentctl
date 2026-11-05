---
description: Open the agentctl restart dialog (relaunches the session with the running binary)
---

Run the following as a Bash tool call:

```bash
agentctl restart-menu
```

This opens the interactive restart confirmation dialog. If confirmed it relaunches the running agentctl binary in the tmux session, restoring all agents from saved state. This is equivalent to the restart option reachable via the agentctl keybinding or tmux menu.
