# Architecture

agentctl is a single Go binary that uses tmux as its UI runtime. Subcommand routing is handled by [Kong](https://github.com/alecthomas/kong) in `cli.go`, the native tmux status bar is disabled in favor of a Bubble Tea chrome pane, and interactive menus use [Bubble Tea](https://github.com/charmbracelet/bubbletea) inside `tmux display-popup`.

## Source layout

```
agentctl
├── main.go              # Entry point: init tiers, hook fast-path, Kong dispatch
├── cli.go               # Kong CLI struct — every subcommand and its Run() method
├── monitor.go           # Background monitor loop (headless Bubble Tea, 500ms tick)
├── birthtime_*.go       # Per-OS file birth-time helpers (log rotation detection)
└── internal/
    ├── agent/           # Agent discovery/composition from ~/.claude/agents/*.md
    ├── chromebar/       # Chrome bar helpers
    ├── claude/          # Retained claude CLI subprocess client (non-default llm provider)
    ├── claudecli/       # Claude CLI argument/flag construction
    ├── cmd/             # Implementations behind Kong subcommands (chrome, dispatch, …)
    ├── config/          # Layered config (global + project), reloaded each monitor tick
    ├── context/         # Cross-agent context store
    ├── dashboards/      # Metrics dashboard scaffolding
    ├── dispatch/        # Issue/ticket dispatch, worktrees, completion handling
    ├── highlight/       # Syntax/diff highlighting for logs and popups
    ├── hooks/           # Claude Code hook handlers (state, notifications, permissions)
    ├── ids/             # Stable ID helpers
    ├── improve/         # Self-improvement session launcher + transition handler
    ├── llm/             # Single-turn text completion (ollama, claude providers)
    ├── logging/         # Debug logging
    ├── logstream/       # Incremental JSONL reader + StateTracker (last tool/text)
    ├── metrics/         # Prometheus metrics + remote write
    ├── paths/           # Runtime paths under $CWD/.agentctl/
    ├── pidfile/         # Single-monitor pidfile guard
    ├── prompt/          # Prompt construction
    ├── ratelimit/       # Rate-limit gauge state
    ├── reaper/          # Reaps finished windows/processes
    ├── sessions/        # JSONL log parsing, summaries, session metadata sidecars
    ├── signal/          # Signal handling
    ├── state/           # state.json snapshot (AgentStatus, transitions, restore)
    ├── theme/           # Color themes
    ├── tmux/            # All tmux interactions (session, window, pane, keybinds)
    ├── tokens/          # Token/cost accounting
    ├── tui/             # Bubble Tea models (chrome bar, menus, wizards, browsers)
    ├── watcher/         # fsnotify helpers
    ├── windowstate/     # Per-window hook state at .agentctl/windows/<id>.json
    └── worktree/        # Git worktree create/remove/list
```

## Subcommand dispatch

`main.go` runs a small tiered bootstrap (`paths` → `logging` → `config`/`state`/`windowstate` → `tmux.SetSessionName`), with two fast paths that run **before** Kong: Claude Code hook subcommands (`hooks.Dispatch`, which need only minimal init) and legacy version flags. With no arguments, agentctl starts or attaches to the tmux session. Otherwise `kong.Parse(&cli)` matches a subcommand struct in `cli.go` and calls its `Run()` method. Hidden internal commands (the chrome-bar pane process, hook helpers) use Kong's `hidden:""` tag.

## Key data flow

1. `agentctl` starts (or attaches to) a tmux session, then runs the monitor as a background process guarded by a pidfile.
2. The monitor is a **headless Bubble Tea program** (`tea.WithoutRenderer()`) that ticks every 500ms. Each tick lists tmux windows and, per window, links logs, updates metrics, resolves state, fires transition handlers, and applies visuals.
3. State is **hook-driven**, not inferred from log growth. Claude Code hooks write a per-window state string to `.agentctl/windows/<windowID>.json` (`windowstate`). The monitor's `resolveState` maps it to an `AgentStatus`: `Working`, `Idle`, `Blocked`, or `Gone` (`Gone` is detected structurally when a window disappears).
4. On a state change the monitor builds a `Transition` and calls the concrete handlers directly — `dispatch.HandleComplete`, `improve.HandleTransition`, and the metrics `transitionTracker`. There is no plugin registry.
5. The monitor renames tmux windows to reflect state (`●` working, `⏸` blocked, `✓` done-unseen, bare name idle) and writes the authoritative snapshot to `state.json`.
6. UI panes (the chrome bar) watch `state.json` via fsnotify and re-read the snapshot to re-render — no data is pushed to them.

See [monitor-architecture.md](monitor-architecture.md) for the detailed monitor, hook, and TUI reference.

## Internal packages

### tmux/

All tmux interactions: session create/attach, window create/kill/rename, pane split/resize/select, keybindings, `display-popup`, and `display-menu`. The native status bar is disabled (`status off`); chrome is a single two-row Bubble Tea pane docked at the bottom of every window (keybind hints, agent tab bar — see `tmux.ChromeBarRows`). Rate limits and per-window context/token metrics moved to the Claude Code status line's `rich` format (`internal/statusline`) since that surface receives the same pushed payload directly. The session name is the package var `tmux.SessionName`, set per-project to `agentctl-<dir>` at startup.

### windowstate/

The authoritative per-window state, written by hooks and read by the monitor. One JSON file per tmux window at `.agentctl/windows/<windowID>.json` (with an advisory lock file), holding the hook state string, log path, dispatch mode, context %, and done flag. This replaced the older "hooks → tmux options → monitor" chain.

### state/

The machine-readable snapshot (`state.json`) the monitor writes each tick under `paths.RuntimeDir()` (`$CWD/.agentctl/`). Consumers (chrome bar, popups) read it via `state.LoadSnapshot()` instead of forking tmux, and it enables restart resume and staleness checks.

### agent/

Discovers agents from the global registry `~/.claude/agents/*.md` and project-local `.claude/agents/` by parsing YAML frontmatter; local definitions shadow global, and `Extends` composes a local agent on top of a base. Each agent's working directory is its frontmatter `Dir` (empty means portable). Agent creation writes new `.md` files.

### sessions/

Parses Claude Code's JSONL conversation logs from `~/.claude/projects/`. Provides `FindSessionsForAgent`, `ParseSummary`, per-session metadata sidecars, and cross-agent context building.

### config/

Layered per-agent settings (alerts, `auto_improve`, `cross_context`, dir, model, …). A project-local `.agentctl/config.json` is overlaid on the global `~/.config/agentctl/config.json`. There is no file watcher — `config.Load()` is called fresh on every monitor tick, so edits take effect within 500ms.

### dispatch/ & worktree/

`dispatch` fetches a GitHub issue, infers the agent from labels, creates an isolated git worktree, and launches the agent on it; it also handles post-completion actions via `HandleComplete`. `worktree` does the git worktree create/remove/list; worktrees live at `<repo>/.worktrees/<slug>/`. See [dispatch docs](dispatch.md).

### tui/

Bubble Tea models. The chrome bar (`chrome_bar.go`) is a mouse-enabled pane that watches `state.json` via fsnotify and renders the two-row chrome (keybinds + tabs). Left-clicking a tab on line 2 switches to that window and returns focus to the agent pane; clicks elsewhere only refocus the agent pane. Most other models run as their own `tea.Program` inside a `tmux display-popup` (launcher, wizards, confirmations, settings); a few full-screen browsers (agents, sessions, tickets) run as dedicated tmux windows.

## Notifications

Notifications are hook-driven — they fire from Claude Code hook handlers in `internal/hooks/`, not from the monitor polling loop. The notify helper reads `notify_cmd` from config (platform default: `alerter` on macOS, `notify-send` on Linux if present), substitutes `{{title}}` and `{{message}}` placeholders, and runs the command with a 30s timeout.

Customize via config:

```json
{
  "notify_cmd": "terminal-notifier -title {{title}} -message {{message}}"
}
```

Omit `notify_cmd` to use the platform default. Set it to an empty string to disable. See [configuration.md](configuration.md).

## Dependencies

- tmux 3.4+; Go 1.26+ only to build from source
- `alerter` (macOS) or `notify-send` (Linux) for notifications — configurable via `notify_cmd` (see [configuration.md](configuration.md))
- Bubble Tea / Lip Gloss for TUI
- Kong for CLI dispatch
- `gopkg.in/yaml.v3` for agent frontmatter parsing
