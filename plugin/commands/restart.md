---
description: Ask the operator to restart agentctl via a confirmation popup in tmux
---

Run the following as a Bash tool call:

```bash
agentctl ctl restart
```

This opens the restart confirmation dialog as a tmux popup on the operator's active agentctl window and blocks until they answer. If confirmed, agentctl relaunches the running binary in the tmux session and restores all agents from saved state, which will also end this session, so say anything you need to say first.

Requires a running agentctl tmux session. If it prints "no agentctl session running", tell the user to start one with `agentctl`. Do not run `agentctl restart` or `agentctl restart-menu` instead: the first kills the session with no confirmation and the second is an interactive TUI that fails without a TTY. The user can also restart themselves with `prefix` + `R` inside tmux.
