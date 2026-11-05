---
description: Send a state signal to the agentctl monitor (done, blocked, idle, working)
---

Parse $ARGUMENTS to extract the signal subcommand and run it:

```bash
agentctl signal <subcommand>
```

Available subcommands:
- `done` — mark the current window as idle and done (fires completion notifications)
- `start` — mark the current window as working (spinner on)
- `state working` — same as `signal start`
- `state blocked` — mark as blocked (shows ⏸ prefix in tmux)
- `state idle` — mark as idle

If no subcommand is given in $ARGUMENTS, default to `done`.

Examples:
- `/agentctl:signal done` → `agentctl signal done`
- `/agentctl:signal state blocked` → `agentctl signal state blocked`
