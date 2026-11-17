# Monitor & Bubble Tea Architecture

Technical reference for the agentctl monitor, hook system, transition handlers, and TUI.

## The Monitor — A Headless Bubble Tea Program

The monitor (`monitor.go`) is the heart of agentctl. It's a **Bubble Tea `tea.Model` running headless** — `tea.WithoutRenderer()` with stdin wired to `/dev/null`. It has `Init()`, `Update()`, and `View()` (returns `""`), but never renders to a terminal. Instead it uses Bubble Tea purely as a **message loop and timer scheduler**. A single monitor runs per project, guarded by a pidfile.

## Hot Reload

The monitor is long-lived, so it picks up a reinstalled binary on its own. At startup `cmdMonitor` resolves its executable (`os.Executable` plus `EvalSymlinks`) and writes its identity (path, dev, inode, size, mtime, pid) to `<runtimeDir>/monitor.build` (`internal/buildid`). At the top of each `processTick`, after the session identity and pidfile checks, `maybeReexec` stats that path. It calls `syscall.Exec` when the identity differs, the file is a regular executable, and its mtime is at least 1 second old (`exeChanged`). Exec keeps the PID, so `monitor.pid`, the registry entry and the pidfile identity check stay valid; deferred cleanup does not run. A failed stat does nothing, a failed exec is logged and retried at most every 30 seconds, and ephemeral binaries (under `os.TempDir`, e.g. `go run`) are skipped.

Monitors that predate this mechanism never wrote `monitor.build`. The hook watchdog (`internal/hooks/watchdog.go`, run from SessionStart and UserPromptSubmit) treats a live monitor as stale when `monitor.build` is missing or unreadable, its pid differs from `monitor.pid`, or it records the same path as the running hook binary but that file's identity changed. A stale monitor is killed and respawned, under the same 30 second rate limit as dead-monitor respawn. A recorded path that differs from the hook binary's path is never stale, so a dev build and an installed build do not kill each other.

Known consequences of a re-exec, since in-memory state resets:

- The done-unseen ✓ is unaffected: `Done` and `Seen` are persisted in windowstate and window names are written by the state writers, not the monitor.
- Prometheus counters restart from zero.
- Dispatch completion is not affected by a re-exec: it is hook-driven (see below), not detected by the monitor.

The chrome bar (`chrome-bar`) and the HUD (`hud`) panes are long-lived Bubble Tea processes and hot-reload the same way through `internal/selfexec`, as does the hub list. Each captures its identity at startup (on a `Capture` error hot reload is disabled for that process and logged). A chrome bar checks every 3 seconds and the HUD every 3 seconds on its 500ms poll tick. When `CheckChanged` reports a change, the pane waits a deterministic 0-2s jitter derived from its window ID (so dozens of panes do not re-exec in the same second), quits the program with `tea.Quit` so the terminal is restored, and `cmd.runWithReload` execs the new binary with the same args only after `Run` has returned. No state crosses the exec: the bar rebuilds from `tmux list-windows` plus windowstate, and the HUD re-reads its log. If the exec fails, the pane keeps running the old image with hot reload off rather than exiting, because nothing respawns a dead chrome bar (windowstate still records `chrome_bar_pane`, so `LaunchChromeBar` would refuse to create another).

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

`StatusGone` is detected structurally when a window ID disappears from `tmux list-windows`, not from any hook. The monitor notices after two absent ticks (`reconcileWindows`); without it, see [Gone and housekeeping without the monitor](#gone-and-housekeeping-without-the-monitor). Both paths use the same claim (`reconcile.Claim` / `reconcile.Finish`), so the handlers fire once per window.

JSONL parsing still happens, but only to enrich metrics and transition detail — context-window %, token counts, and the last tool name/detail/text (via `logstream.StateTracker`). It does not author state.

## Per-Window State (`instanceState`)

Each tmux window gets an `instanceState` struct tracking:

- **Identity**: instance name (`go-engineer~2`), base name (`go-engineer`)
- **Log linkage**: JSONL log path (from windowstate `LogPath`, or a filesystem-scan fallback), a persistent `logstream.Stream`, and file-size tracking
- **State machine**: previous state, `hasPrev` flag for first-tick suppression, `done` flag
- **Metrics cache**: ctx%, input/output tokens, cumulative counts
- **Attention tracking**: `Seen` is a persisted windowstate field (set by `set-active-window`, cleared by the state writers)

## Log Discovery

The monitor needs each agent's JSONL session log. Two strategies:

1. **Primary — windowstate `LogPath`**: Written by `hook-session-start` when Claude reports its session and cwd. The monitor reads it with zero scanning.

2. **Fallback — `sessions.FindLatestLog()` filesystem scan**: Walks `~/.claude/projects/*/`, finds recent `.jsonl` files, and matches the agent. Used for old windows, `--fork-session` resumes, and post-`/clear` rotation.

**`/clear` detection**: When the current log has been stable and a newer log with a later birth time appears, the monitor resets that window's state so re-discovery kicks in.

## Transition Handlers (No Plugin Registry)

There is **no** plugin interface or registry. On a detected state change, `detectAndFireTransitions()` builds a `state.Transition` (agent, window ID, from/to states, active flag, last tool name/detail/text, context %, hook-handled/done flags, dispatch mode, ticket number, branch, work dir) and calls the concrete handlers directly:

1. **`m.transitionTracker.RecordTransition(t)`** — Prometheus metrics (`internal/metrics`): agents spawned, sessions completed, state transitions, durations, monitor uptime, etc. Exposed on a configurable HTTP port when metrics are enabled.

2. **`improve.HandleTransition(t, agentCfg)`** (`internal/improve`) — Auto-launches an improvement session on the relevant transition, gated by per-agent `auto_improve` config, with a recursion guard for improve windows.

To add new reactive behavior, add a handler call here rather than registering a plugin.

### Dispatch completion is hook-driven

The Stop hook (`hooks.CheckDone`) and `agentctl signal done` set `Done=true` in a `windowstate.Transact` that also stamps `CompleteFiredAt` when the window is autonomous (dispatch/adhoc) and no completion has been claimed since the last re-arm. The winner spawns a fully detached hidden `agentctl dispatch-complete <windowID>` (own session, stdio on /dev/null, never waited on), so the hook never blocks on gh or git. That process rebuilds the `Transition` from windowstate and runs `dispatch.RunComplete` synchronously: PR lookup, label swap, deferral while an approval popup is pending (up to ~2 minutes), `gh pr view --web`, and `dispatch_complete` hooks. Each completion is its own process, so PR opening is deduped with O_EXCL marker files at `<runtimeDir>/opened-prs/<repo-slug>-<pr>` (markers older than 7 days are removed when one is written). `UserPromptSubmit` and non-done `signal` states clear `Done` and `CompleteFiredAt`, so a working-then-done cycle completes again. If the spawn fails, the stamp is cleared so a later Stop retries. The monitor takes no part in this.

## Window Names — written by the state writers

The monitor does not rename tmux windows. Every hook or command that changes `State` or `Done` (`internal/hooks`, `agentctl signal`) goes through `internal/winname`, which runs inside the same `windowstate` transaction: it clears `Seen` when State changes or Done goes true (or sets it at once if the window is the active one), then `SyncName` renames the window when `winname.Compute` differs from the persisted `RenderedName`. The name carries the glyph — `● name` (working), `⏸ name` (blocked), `✓ name` (done and unseen), or the bare name (idle). `agentctl set-active-window` (tmux after-select-window) sets `Seen=true` and syncs, clearing the ✓; `Done` itself is never cleared by focus. Because `Seen`, `Done` and `RenderedName` are persisted, the ✓ survives a monitor restart or hot reload. A rename failure is logged at debug and leaves `RenderedName` unchanged, so the next writer retries. The monitor's remaining per-tick work is resolving ctx% in memory (a fresh statusline push wins, otherwise its derived value; nothing is written back to windowstate) and persisting `state.json`.

## State Persistence

Every tick, `persistState()` writes `state.json` under `paths.RuntimeDir()` (`$CWD/.agentctl/state.json`) with all agent states. **Nothing in agentctl requires it any more**: the macapp reads it (its format is unchanged), and it carries the JSONL-derived fields only the monitor computes (cumulative and last-turn token counts, start time, pending-mail count).

Every other reader builds its view with the shared builder in `internal/herd` (`LoadAgents` / `BuildAgents`): `tmux list-windows` for the session (ID, order, name) joined with `windowstate.AllFrom(<runtime dir>/windows)`. tmux decides which windows exist, so a record with no window is ignored and a window with no record is listed as untracked (idle, named from tmux). `Done` is `ws.Done && !ws.Seen`; ctx%, context tokens/window, model and cost are the last values the statusline pushed. State, identity and the window list never depend on `state.json`. When `state.json` exists and is fresh (`herd.EnrichmentMaxAge`, 3s) the monitor-only token fields are merged in by window ID as optional enrichment; otherwise they stay zero and the UI hides them. The statusline takes tokens and duration from the Claude Code payload first and falls back to that enrichment only when a payload field is null; turns come from `ws.TurnCount`.

The chrome bar watches `windows/` and the `active-window` file with fsnotify, debounces events (100ms), rebuilds through the builder, and re-renders only when a rendered field changed. `tmux list-windows` is re-run only when a record file is created or removed, or on the 30s slow tick. The prefix indicator polls `#{client_prefix}` every 250ms in the active window's bar only. If fsnotify is unavailable it polls once a second.

### Turning the monitor off (`monitor: false`)

The global config key `monitor` (default `true`) is the kill switch. When `false`, session start / `applyConfig` does not start a monitor, the hook watchdog neither respawns nor replaces one, and `agentctl health` reports "monitor disabled" instead of a missing-monitor problem. Flipping the flag never kills a running monitor; the next relaunch just does not start one. With no monitor, `state.json` stops updating (the macapp loses its feed) and the `improve.HandleTransition` / metrics transition handlers do not run, but the chrome bar, hub, `agentctl status`, popups and statusline keep working from tmux and windowstate.

### Gone and housekeeping without the monitor

`internal/reconcile` holds the code that finishes a vanished window, and the hidden `agentctl reconcile` runs it with no long-lived process:

- **Claim.** `windowstate.Claim` removes a window's record under its per-window flock, writes the `.gone` tombstone and reports whether this caller did it; only that caller builds the Gone `Transition` (`From` = the record's state), runs `improve.HandleTransitionSync`, and removes the window's mailbox. shell and `improve~*` windows are claimed but get no handlers. The monitor's `reconcileWindows` calls the same `Claim`/`Finish`, so whichever notices first wins. A window removed by `cleanup`/`sweep`/SessionEnd reclaim has already had its record removed, so no Gone handler runs for it (the monitor used to fire one from memory).
- **Safety.** `reconcile` takes a non-blocking flock on `<runtimeDir>/reconcile.lock` (held: exit). A failed `list-windows`, or a session that no longer exists, does nothing (teardown is restore territory). A record is reaped only if its window is absent from two listings 300ms apart and from a third taken under the record's lock, so a recycled `@N` whose new window exists is never removed. It runs inline: every spawner is already detached, and the auto-improve launch must finish before the process exits.
- **Triggers.** A detached `reconcile` is spawned after `tmux.KillWindow` and the SessionEnd kill succeed; a session-scoped `window-unlinked` tmux hook (set by `applyConfig`/`apply-config`) runs it whenever tmux destroys a window, including when the agent just exits (it does not fire on `kill-session`, so teardown never mass-removes records); and the janitor.
- **Janitor.** `SessionStart` and `UserPromptSubmit` call `janitor()` next to the watchdog. A flock-guarded stamp (`internal/stamp`, `<runtimeDir>/janitor.stamp`, 60s) lets exactly one concurrent hook spawn `agentctl reconcile --housekeeping`, which reconciles and then runs `reaper.ReapOrphans`, `approval-pending/` marker GC, and the mailbox and session-log prunes (5 min stamps each; protected session logs are built from windowstate only). The watchdog uses the same atomic stamp for its respawn throttle. Readers never reconcile.
- **Chrome sizes.** `client-resized` and `client-attached` session hooks run the hidden `agentctl enforce-chrome`. The monitor's 5-tick enforcement stays while the monitor is on, as do its own `pruneTick`/reaper (both are idempotent next to the janitor).
- **`agentctl apply-config`** re-applies the session hooks, options and keybindings to the running session without stopping or starting the monitor or restoring anything, so an existing session picks up new hooks after `just install`.

### Session restore (`restore.json`)

Restore does not depend on the monitor. `agentctl restore` reads `<runtimeDir>/restore.json`, which is built from the per-window hook state (`windowstate`: instance, agent name, session ID, log path, work dir, backend) by `state.BuildRestoreAgents` and written (0600, same safefs discipline as `state.json`) at every point that is about to destroy windows: `tmux.KillSession` (quit, restart), relaunch-prep, the hidden `save-state` command, and `StartSession` immediately before `windowstate.RemoveAll()`. The `StartSession` write covers a tmux server crash or reboot, where the windows are gone but the records survive. A write that finds no agent entries is skipped, so a clean start never clobbers a good file. Tombstoned (removed) windows have no record and so never appear; shell, hub, utility and `improve~*` windows are skipped.

Entry order is the tab order: the live `tmux list-windows` position when the window still exists, otherwise the `WindowIndex` that `hook-session-start` recorded (it can be slightly stale after left-insertion; ties fall back to window ID order). `restore_max_age` is applied to the `restore.json` timestamp, and the file is cleared after a restore attempt along with `state.json`.

If `restore.json` is missing, restore falls back to `state.json` (one-release compatibility; removed with the monitor).

## TUI

The `internal/tui/` package holds many standalone Bubble Tea models. Most interactive UIs (launcher, launch options, kill/quit/restart confirmations, create wizard, improve picker, settings, permissions, ticket launch, agent manager) run as their own `tea.Program` inside a `tmux display-popup`; each follows the same pattern: resolve context → create model → run program → read result → act.

A few full-screen browsers — **agents**, **sessions**, and **tickets** — run as dedicated tmux windows (opened via `OpenOrFocus*Window`) rather than popups, recording an origin window so they can restore focus on exit.

The **chrome bar** (`chrome_bar.go`) is different: a mouse-enabled pane docked in every window that watches the `windows/` directory and the sibling `active-window` file via fsnotify and re-renders when the built view changes (see [State Persistence](#state-persistence)).

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
2. **Transitions**: state change → `detectAndFireTransitions()` builds `Transition` → `RecordTransition` + `improve.HandleTransition`. Dispatch completion is not part of this loop.
3. **Window names**: each state writer → `winname.Update` → `tmux rename-window` (state glyph) inside the windowstate transaction; the monitor only resolves ctx% in memory for `state.json`.
4. **Log discovery**: `hook-session-start` → windowstate `LogPath` → monitor links `instanceState.logPath`; fallback `sessions.FindLatestLog()` scans the filesystem.
5. **Reader views**: tmux `list-windows` + `windowstate.AllFrom` → `herd.BuildAgents` → chrome bar (fsnotify on `windows/`), hub, `agentctl status`, popups. The monitor still writes `state.json` for the macapp only.

## See also

- [architecture.md](architecture.md) — system overview and source layout
