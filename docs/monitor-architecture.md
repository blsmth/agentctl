# Monitor & Bubble Tea Architecture

Technical reference for the agentctl monitor, hook system, transition handlers, and TUI.

## The Monitor — A Headless Bubble Tea Program

The monitor (`monitor.go`) is the heart of agentctl. It's a **Bubble Tea `tea.Model` running headless** — `tea.WithoutRenderer()` with stdin wired to `/dev/null`. It has `Init()`, `Update()`, and `View()` (returns `""`), but never renders to a terminal. Instead it uses Bubble Tea purely as a **message loop and timer scheduler**. A single monitor runs per project, guarded by a pidfile.

## Hot Reload

The monitor is long-lived, so it picks up a reinstalled binary on its own. At startup `cmdMonitor` resolves its executable (`os.Executable` plus `EvalSymlinks`) and writes its identity (path, dev, inode, size, mtime, pid) to `<runtimeDir>/monitor.build` (`internal/buildid`). At the top of each `processTick`, after the session identity and pidfile checks, `maybeReexec` stats that path. It calls `syscall.Exec` when the identity differs, the file is a regular executable, and its mtime is at least 1 second old (`exeChanged`). Exec keeps the PID, so `monitor.pid`, the registry entry and the pidfile identity check stay valid; deferred cleanup does not run. A failed stat does nothing, a failed exec is logged and retried at most every 30 seconds, and ephemeral binaries (under `os.TempDir`, e.g. `go run`) are skipped.

Monitors that predate this mechanism never wrote `monitor.build`. The hook watchdog (`internal/hooks/watchdog.go`, run from SessionStart and UserPromptSubmit) treats a live monitor as stale when `monitor.build` is missing or unreadable, its pid differs from `monitor.pid`, or it records the same path as the running hook binary but that file's identity changed. A stale monitor is killed and respawned, under the same 30 second rate limit as dead-monitor respawn. A recorded path that differs from the hook binary's path is never stale, so a dev build and an installed build do not kill each other.

Known consequences of a re-exec, since in-memory state resets:

- The done-unseen check-mark glyph can reappear on windows that were already viewed.
- Prometheus counters restart from zero.
- A done edge landing during the roughly one second gap is not observed. The `hasPrev` gate in `detectAndFireTransitions` already prevents re-firing old edges.

## Two Message Loops

The monitor runs on two interleaved timers:

1. **`tickMsg` (500ms)** — The main poll cycle. Reloads config, discovers tmux windows, reads hook-driven state, fires transition handlers, parses JSONL for metrics, persists state to disk, and refreshes the chrome bar.

2. **`spinner.TickMsg` (~80ms)** — Drives the spinner animation. On each tick the working-state windows get an animated name update.

## State Detection — Hook-Driven, Not Poll-Driven

State is **not** inferred from JSONL log growth. Claude Code hooks write state into a **per-window JSON file** at `.agentctl/windows/<windowID>.json` (the `windowstate` package), updated atomically under an advisory lock. The monitor **reads** that state each tick — it does not set it.

`resolveState()` maps the hook state string to an `AgentStatus`:

| windowstate `State` | Set by | AgentStatus |
|---------------------|--------|-------------|
| `working` | `hook-pre-tool` (PreToolUse), `hook-post-tool` (PostToolUse), `hook-user-prompt` | `StatusWorking` |
| `blocked` | `hook-notify` (permission_prompt) | `StatusBlocked` |
| `idle` | `hook-notify` (idle_prompt), `hook-stop-failure`, `hook-post-tool-failure` | `StatusIdle` |
| _(none)_ | window not hook-configured or just launched | `StatusIdle` (default) |

`StatusGone` is detected structurally when a window ID disappears from `tmux list-windows` (see `reconcileWindows`), not from any hook.

JSONL parsing still happens, but only to enrich metrics and transition detail — context-window %, token counts, and the last tool name/detail/text (via `logstream.StateTracker`). It does not author state.

## Per-Window State (`instanceState`)

Each tmux window gets an `instanceState` struct tracking:

- **Identity**: instance name (`go-engineer~2`), base name (`go-engineer`)
- **Log linkage**: JSONL log path (from windowstate `LogPath`, or a filesystem-scan fallback), a persistent `logstream.Stream`, and file-size tracking
- **State machine**: previous state, `hasPrev` flag for first-tick suppression, `done` flag
- **Metrics cache**: ctx%, input/output tokens, cumulative counts
- **Visual dedup**: last rendered window name and displays
- **Attention tracking**: `seen` flag (clears when the user focuses the window)

## Log Discovery

The monitor needs each agent's JSONL session log. Two strategies:

1. **Primary — windowstate `LogPath`**: Written by `hook-session-start` when Claude reports its session and cwd. The monitor reads it with zero scanning.

2. **Fallback — `sessions.FindLatestLog()` filesystem scan**: Walks `~/.claude/projects/*/`, finds recent `.jsonl` files, and matches the agent. Used for old windows, `--fork-session` resumes, and post-`/clear` rotation.

**`/clear` detection**: When the current log has been stable and a newer log with a later birth time appears, the monitor resets that window's state so re-discovery kicks in.

## Transition Handlers (No Plugin Registry)

There is **no** plugin interface or registry. On a detected state change, `detectAndFireTransitions()` builds a `state.Transition` (agent, window ID, from/to states, active flag, last tool name/detail/text, context %, hook-handled/done flags, dispatch mode, ticket number, branch, work dir) and calls the concrete handlers directly:

1. **`m.transitionTracker.RecordTransition(t)`** — Prometheus metrics (`internal/metrics`): agents spawned, sessions completed, state transitions, durations, monitor uptime, etc. Exposed on a configurable HTTP port when metrics are enabled.

2. **`dispatch.HandleComplete(t, agentCfg)`** (`internal/dispatch`) — Post-dispatch completion actions for issue/ticket runs.

3. **`improve.HandleTransition(t, agentCfg)`** (`internal/improve`) — Auto-launches an improvement session on the relevant transition, gated by per-agent `auto_improve` config, with a recursion guard for improve windows.

To add new reactive behavior, add a handler call here rather than registering a plugin.

## Visual Output — `applyVisuals()`

A single function owns tmux visual updates for each window, called on every tick and spinner tick. The window name carries the state glyph — `● name` (working, animated spinner), `⏸ name` (blocked), `✓ name` (done and unseen), or the bare name (idle). Context % and token metrics are written back to windowstate for the chrome bar to render. All changes are **deduped** against last-known values to avoid unnecessary tmux IPC.

## State Persistence

Every tick, `persistState()` writes `state.json` under `paths.RuntimeDir()` (`$CWD/.agentctl/state.json`) with all agent states. This enables:

- **Fast lookups**: UI panes and popups read the snapshot via `state.LoadSnapshot()` instead of forking tmux.
- **Restart resume**: On restart the monitor restores log paths from the snapshot.
- **Staleness detection**: Consumers check staleness and fall back to live tmux queries.

The chrome bar and other panes watch `state.json` via fsnotify, so the flow is: **monitor tick → `state.Save(state.json)` → fsnotify → pane re-reads snapshot → re-render.**

## TUI

The `internal/tui/` package holds many standalone Bubble Tea models. Most interactive UIs (launcher, launch options, kill/quit/restart confirmations, create wizard, improve picker, settings, permissions, ticket launch, agent manager) run as their own `tea.Program` inside a `tmux display-popup`; each follows the same pattern: resolve context → create model → run program → read result → act.

A few full-screen browsers — **agents**, **sessions**, and **tickets** — run as dedicated tmux windows (opened via `OpenOrFocus*Window`) rather than popups, recording an origin window so they can restore focus on exit.

The **chrome bar** (`chrome_bar.go`) is different: a mouse-enabled pane docked in every window that watches `state.json` via fsnotify (plus a sibling `active-window` file for instant active-tab highlighting) and re-renders on change.

## Hook System (Separate Processes)

Hooks are **not** part of the monitor — they're separate `agentctl` subcommand invocations triggered by Claude Code's hook system and dispatched by `hooks.Dispatch` (`internal/hooks/`) on a fast path before Kong. They run in the agent's process context and communicate with the monitor by writing the window's `windowstate` file. Key hooks include:

| Hook | Lifecycle Event | Key Action |
|------|-----------------|------------|
| `hook-session-start` | SessionStart | Records log path / session id into windowstate |
| `hook-user-prompt` | UserPromptSubmit | Sets state `working` |
| `hook-pre-tool` / `hook-post-tool` | PreToolUse / PostToolUse | Sets state `working` |
| `hook-notify` | Notification | Sets `blocked` (permission) or `idle`, fires alerts |
| `hook-validate-bash` | PreToolUse (Bash) | Checks deny lists, can block commands |
| `hook-check-done` | Stop | Verifies clean git status / passing build, sets done |
| `hook-stop-failure` / `hook-post-tool-failure` | Stop / tool failure | Sets state `idle` |
| `hook-session-end` | SessionEnd | Clears to idle |

Additional hooks handle compaction, elicitation, worktree create/remove, subagent lifecycle, task events, and config/cwd changes. See `internal/hooks/` for the full set.

## Key Data Flows

1. **State detection**: Claude Code → hook (`pre-tool`/`notify`/…) → `windowstate.Update()` writes `.agentctl/windows/<id>.json` → monitor `resolveState()` reads it on tick.
2. **Transitions**: state change → `detectAndFireTransitions()` builds `Transition` → `RecordTransition` + `dispatch.HandleComplete` + `improve.HandleTransition`.
3. **Visual output**: monitor `applyVisuals()` → `tmux rename-window` (state glyph) + windowstate metrics for the chrome bar.
4. **Log discovery**: `hook-session-start` → windowstate `LogPath` → monitor links `instanceState.logPath`; fallback `sessions.FindLatestLog()` scans the filesystem.
5. **State persistence**: monitor tick → `state.Save()` → `state.json` → panes/popups read via `LoadSnapshot()`, chrome bar re-renders on fsnotify.

## See also

- [architecture.md](architecture.md) — system overview and source layout
