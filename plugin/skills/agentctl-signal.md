## agentctl start: startup banner (manual use only)

`agentctl start` reads identity metadata entirely from environment variables (no arguments needed) and prints a branded ASCII art startup banner showing agent name, ticket, mode, working directory, and start time.

`agentctl start` is not run automatically at launch; call it manually if you want the banner in your session. The launch identity is also written to the agentctl runtime log.

This is distinct from `agentctl signal start` — they do completely different things.

---

## agentctl signal — state management

`agentctl signal <subcommand>` sets the state of the current agent window so the chrome bar and window name update immediately (rather than waiting for the next poll cycle):

```bash
agentctl signal start          # mark window as "working" (spinner on)
agentctl signal done           # mark window as "idle" + done flag
agentctl signal state blocked  # mark as blocked (shows ⏸ prefix)
agentctl signal state idle     # mark as idle
agentctl signal state working  # same as signal start (longhand)
```

### How it works

Resolution order for finding the current window:
1. `AGENTCTL_SESSION_ID` env → scans tmux windows for matching `@agentctl_session_id` option
2. `TMUX_PANE` env → `tmux display-message` to get `#{window_id}`
3. `AGENTCTL_WINDOW_ID` env (written to `CLAUDE_ENV_FILE` by `hook-session-start`)

If all three fail, exits 1 with `cannot determine window ID` on stderr.

### `signal start` produces no stdout — this is intentional

`signal start` is intentionally silent. Output goes nowhere when called in background mode.

State transitions (working, idle, blocked) are driven by Claude Code hooks, not by `signal`: `PreToolUse` sets working, notification hooks set blocked or idle, and the Stop hook derives done. `agentctl signal` is an optional manual or early signal.

**When debugging a silent/broken `signal start`:**

1. **Check the debug log** — all signal state changes are logged:
   ```bash
   just logs
   # or:
   tail .agentctl/logs/agentctl.log
   ```
   Look for lines like `signal state: resolved window_id=@5 via session_id=...` or `signal state: cannot determine window ID`.

2. **Verify env vars are present** in the Bash tool environment:
   ```bash
   env | grep -E 'AGENTCTL|TMUX_PANE'
   ```
   You need at least one of: `AGENTCTL_SESSION_ID`, `TMUX_PANE`, or `AGENTCTL_WINDOW_ID`.

3. **Capture stderr manually** when testing:
   ```bash
   agentctl signal start 2>&1
   ```

4. **After code changes**: always `just install` before testing — you must be running the newly built binary, not the old one.

### Working on internal/signal or hooks

- `just install && just logs` is your test loop — the log is the primary output channel for signal commands.
- `signal start` and `signal done` are shorthands; `signal state <value>` is the canonical form.
- The rename switch in `signal.Apply` (`internal/signal/signal.go`) intentionally skips `working` — the monitor owns visual updates for the working state (spinner, tab style). Only `blocked`, `idle`, and `done` trigger immediate window renames.
