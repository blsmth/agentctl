# Architecture

agentctl is a single Go binary that uses tmux as its UI runtime. Subcommand routing is handled by [Kong](https://github.com/alecthomas/kong) in `cli.go`, and interactive menus use [Bubble Tea](https://github.com/charmbracelet/bubbletea) inside `tmux display-popup`. By default the native tmux status line shows the window list; with the global `chrome_bar` key on, it is turned off and every window gets a two-row Bubble Tea chrome pane instead.

## Source layout

```
agentctl
├── main.go              # Entry point: init tiers, hook fast-path, Kong dispatch
├── cli.go               # Kong CLI struct — every subcommand and its Run() method
├── monitor.go           # Optional background monitor (headless Bubble Tea, 500ms tick)
├── monitor_reexec.go    # Monitor hot reload on binary change
├── birthtime_*.go       # Per-OS file birth-time helpers (log rotation detection)
└── internal/
    ├── cmd/             # Implementations behind Kong subcommands
    │
    │   # State and identity
    ├── windowstate/     # Per-window hook state at .agentctl/windows/<id>.json (the state bus)
    ├── winname/         # Window name (glyph + instance) synced inside the windowstate transaction
    ├── state/           # state.json snapshot, restore.json builders, AgentState/Transition types
    ├── reconcile/       # Gone handling for vanished windows, close_children cascade
    ├── closedlog/       # Machine-wide ledger of closed agent windows
    ├── signal/          # Window ID resolution and `agentctl signal`
    ├── registry/        # Live agentctl sessions on the machine
    ├── fleet/           # Cross-session aggregation behind the hub, status and serve
    ├── panestate/       # Classifies captured Claude Code pane text
    │
    │   # Hooks and launch
    ├── hooks/           # Claude Code hook handlers (state, notifications, gates, done)
    ├── harness/         # Agent program per window: Claude Code (default) or pi
    ├── transcript/      # Normalizes non-Claude transcripts (pi) into Claude's JSONL shape
    ├── dispatch/        # Launch pipeline, prompts, modes, completion, PR review
    ├── dispatchplan/    # Dispatch precedence rules (model, backend, permission mode, …)
    ├── dispatchapproval/# Single-use records of a human-approved dispatch
    ├── approval/        # Pending approval-popup markers
    ├── remoteapproval/  # Wire protocol between an approval hook and agentctl serve
    ├── agent/           # Agent discovery/composition from ~/.claude/agents/*.md
    ├── agentlabel/      # Issue label → agent name
    ├── agentname/       # Agent-name safety rules
    ├── claudehome/      # Claude config dirs per account (CLAUDE_CONFIG_DIR)
    ├── backendauth/     # Backend API keys (keychain / credential files)
    ├── launchopts/      # Shared launch-option vocabulary (models, efforts)
    ├── worktree/        # Git worktree create/remove/list/prune
    ├── cleanup/         # Worktree and window classification for cleanup/sweep
    ├── sweeprun/        # Runs `agentctl sweep` as a subprocess for the hub and serve
    ├── improve/         # Self-improvement launcher + Gone transition handler
    ├── mailbox/         # agentctl notices (done/blocked backstop) delivered by hooks
    ├── context/         # Cross-agent context store
    │
    │   # UI
    ├── tmux/            # All tmux interactions (sessions, windows, panes, keybindings, popups)
    ├── hubtarget/       # Redirects prefix keys pressed in the hub to the viewed agent
    ├── tui/             # Bubble Tea models (hub, chrome bar, HUD, menus, wizards, browsers)
    ├── render/          # Presentation helpers shared by the chrome bar and statusline
    ├── statusline/      # `agentctl statusline` (Claude Code's statusLine command)
    ├── phoneview/       # Which windows the phone is looking at (statusline mobile format)
    ├── theme/           # Color themes
    ├── highlight/       # Syntax/diff highlighting
    ├── selfexec/        # Hot reload for long-running panes
    │
    │   # Per-machine services
    ├── serve/           # `agentctl serve`: HTTPS server for the mobile web app
    ├── servectl/        # serve start/stop/toggle and status probes
    ├── uploads/         # Images attached from the phone
    ├── cron/            # `agentctl cron` scheduler and tick decisions
    ├── cronedit/        # The one editing path for cron jobs (CLI, hub, serve)
    ├── launchd/         # LaunchAgent plists for serve and cron
    ├── metrics/         # Prometheus registry, collectors, hook-written event log
    ├── metricsserve/    # `agentctl metrics serve`
    ├── metricsproc/     # Its pidfile/lock/start plumbing
    ├── notify/          # Desktop notifications via notify_cmd
    │
    │   # Sessions, cost and titles
    ├── sessions/        # JSONL log parsing, summaries, session cost and billing class
    ├── logstream/       # Incremental JSONL reader + StateTracker
    ├── tokens/          # Model registry and the single pricing table
    ├── pricecache/      # Machine-wide Nous price catalog cache
    ├── llm/             # Single-turn completions (Ollama, Claude, Nous for titles)
    ├── llmusage/        # Ledger of agentctl's own LLM calls
    ├── shorttitle/      # Short row titles for the hub and phone
    ├── ratelimit/       # 5h/7d rate-limit state pushed by the statusline
    ├── audit/           # Hook-sourced audit trail (`agentctl audit`)
    ├── race/            # Model behind `agentctl race`
    ├── dashboards/      # Embedded Grafana dashboards
    │
    │   # Plumbing
    ├── config/          # Layered config (global + project)
    ├── paths/           # Runtime paths under $CWD/.agentctl/ and XDG dirs
    ├── pidfile/, stamp/, detach/, safefs/, selfpath/, buildid/, reaper/, …
    ├── testenv/         # Isolates every test binary from the live machine
    └── demo/            # Screenshot harness (never linked into the binary)
```

`go list ./internal/...` is the complete list; small leaf helpers (`ids`, `shellquote`, `textutil`, `toolinput`, `winlink`, `cliout`, `gittracked`, `sessionname`, `prompt`) are omitted above.

## Subcommand dispatch

`main.go` runs a small tiered bootstrap (`paths` → `logging` → `config`/`state`/`windowstate` → `tmux.SetSessionName`), with fast paths that run **before** Kong: Claude Code hook subcommands (`hooks.Dispatch`, which need only minimal init), the statusline ingest (`agentctl statusline`), and legacy version flags. With no arguments, agentctl starts or attaches to the tmux session. Otherwise `kong.Parse(&cli)` matches a subcommand struct in `cli.go` and calls its `Run()` method. Hidden internal commands (the chrome-bar pane, `reconcile`, `dispatch-complete`, popup helpers) use Kong's `hidden:""` tag. See [commands.md](commands.md) for the user-facing commands.

## Key data flow

1. `agentctl` starts (or attaches to) a tmux session, installs session hooks and keybindings, and, unless `monitor: false`, starts the monitor as a background process guarded by a pidfile.
2. State is **hook-driven**. Claude Code hooks write a per-window state string to `.agentctl/windows/<windowID>.json` (`windowstate`): `working`, `blocked` or `idle`. The same writers rename the tmux window inside that transaction (`internal/winname`: `●` working, `⏸` blocked, `!` needs attention, `✓` done-unseen, bare name idle; a window that never gets `Done` also shows `✓` at a turn end via `TurnDone`, and `!` comes from windowstate `attention`, both display-only; see [monitor-architecture.md](monitor-architecture.md#needs-attention)) and fire desktop notifications.
3. Readers (chrome bar, hub, `agentctl status`, popups, `agentctl serve`) build their view from `tmux list-windows` joined with windowstate (`fleet.LoadAgents`). The chrome bar watches `.agentctl/windows/` and `active-window` via fsnotify.
4. Dispatch completion is hook-driven: the Stop hook (or `agentctl signal done`) sets `Done` and spawns a detached `agentctl dispatch-complete`, which finds the PR, opens it and runs `dispatch_complete` hooks.
5. A vanished window is finished by `agentctl reconcile` (run by a `window-unlinked` tmux hook, after kills, and by the hook janitor): it claims the record, fires Gone handlers, closes the window's dispatched children (`close_children`) and appends to the closed ledger.
6. Metrics come from a per-machine `agentctl metrics serve` that tails an event log the state writers append to.
7. The monitor, when running, ticks every 500ms, parses JSONL for token enrichment, and writes `state.json`, which readers merge in only when it is fresh and which restore falls back to when `restore.json` is missing.

See [monitor-architecture.md](monitor-architecture.md) for the detailed monitor, hook, reconcile, restore and TUI reference.

## Internal packages

### tmux/

All tmux interactions: session create/attach, window create/kill/rename, pane split/resize/select, keybindings, `display-popup`, and `display-menu`. The per-window chrome bar (keybind hints and an agent tab bar, see `tmux.ChromeBarRows`) is gated by the global `chrome_bar` key (`chromegate.go`, default off); `agentctl apply-config` switches existing windows. Rate limits and per-window context/token metrics are shown by the Claude Code status line (`internal/statusline`), which receives the pushed payload directly. The session name is the package var `tmux.SessionName`, set per-project to `agentctl-<dir>` at startup.

### windowstate/

The authoritative per-window state, written by hooks and the other state writers and read by everything else. One JSON file per tmux window at `.agentctl/windows/<windowID>.json` (with an advisory lock file), holding the hook state string, session ID and log path, dispatch mode and parent (`spawned_by` + `spawned_by_session`), account, backend, harness, short title, done/seen flags and more.

### state/

The `state.json` snapshot the monitor writes each tick under `paths.RuntimeDir()` (`$CWD/.agentctl/`). Only optional token enrichment (`fleet/view.go`, `EnrichmentMaxAge`) and the one-release restore fallback when `restore.json` is absent read it. It also holds the builders for `restore.json`, which is written from windowstate at every point that destroys windows.

### agent/

Discovers agents from the global registry `~/.claude/agents/*.md` and project-local `.claude/agents/` by parsing YAML frontmatter; local definitions shadow global, and `Extends` composes a local agent on top of a base. Frontmatter may set `role: orchestrator` and `harness:`. Agent creation writes new `.md` files.

### sessions/

Parses Claude Code's JSONL conversation logs in every account's `projects/` dir (`~/.claude/projects/` by default), and pi session files through `internal/transcript`. Provides summaries, per-session metadata sidecars, search, cross-agent context building, and session cost (`agentctl cost`).

### config/

Layered settings. A project-local `.agentctl/config.json` is overlaid on the global `~/.config/agentctl/config.json`; command, routing and machine-wide keys are global-only. There is no file watcher: `config.Load()` is called fresh where it is needed (every monitor tick, every hook), so edits take effect at once. See [configuration.md](configuration.md).

### dispatch/ & worktree/

`dispatch` is the launch pipeline: it fetches a GitHub issue or PR, infers the agent from labels, creates an isolated git worktree, builds the system prompt ([prompt-architecture.md](prompt-architecture.md)) and launches the agent through its harness. `RunComplete` handles post-completion actions (run by the hook-spawned `dispatch-complete` process). `worktree` does the git worktree create/remove/list/prune; worktrees live at `<repo>/.worktrees/<slug>/`. See [dispatch docs](dispatch.md).

### tui/

Bubble Tea models: the fleet hub, the chrome bar (`chrome_bar.go`, fsnotify-fed), the HUD, the race replay, popups (launcher, wizards, confirmations, settings, approval) that run as their own `tea.Program` inside `tmux display-popup`, and full-screen browsers (agents, sessions, tickets, context) that run as dedicated tmux windows.

### serve/ and cron/

`agentctl serve` is a per-machine HTTPS server for the mobile web app (fleet, live terminal, approvals, push); see [design/mobile-web-app.md](design/mobile-web-app.md). `agentctl cron` runs scheduled agents from a launchd ticker; see [configuration.md](configuration.md#cron) and [design/cron.md](design/cron.md).

## Notifications

Notifications are hook-driven: they fire from Claude Code hook handlers in `internal/hooks/`, not from the monitor. The notify helper reads `notify_cmd` from config (platform default: `alerter` on macOS, `notify-send` on Linux if present), substitutes `{{title}}` and `{{message}}` placeholders, and runs the command with a 30s timeout without blocking the hook.

Customize via config:

```json
{
  "notify_cmd": "terminal-notifier -title {{title}} -message {{message}}"
}
```

Omit `notify_cmd` to use the platform default. Set it to an empty string to disable. See [configuration.md](configuration.md#notifications). With `agentctl serve` running, the phone also gets web push.

## Dependencies

- tmux 3.4+; Go 1.26+ only to build from source
- `alerter` (macOS) or `notify-send` (Linux) for notifications — configurable via `notify_cmd` (see [configuration.md](configuration.md))
- Bubble Tea / Lip Gloss for TUI
- Kong for CLI dispatch
- `robfig/cron` for `agentctl cron` schedules
- `gopkg.in/yaml.v3` for agent frontmatter parsing
