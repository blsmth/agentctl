# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

agentctl is a tmux-native command center for managing multiple Claude Code agents. It treats tmux as the runtime — each agent runs in its own window, Claude Code hooks write per-window state (working/blocked/idle) that a background monitor reads each tick, and transition handlers react to state changes.

## Build & Run

```bash
go build -o agentctl .    # build the binary
./agentctl                 # start or attach to the tmux session
go test ./...              # run the full test suite
golangci-lint run          # lint (config in .golangci.yml)
```

Tests live alongside the code — both at the repo root (`cli_test.go`, `monitor_test.go`, `smoke_test.go`, etc.) and throughout `internal/**`. Linter config is in `.golangci.yml`.

## Architecture

The binary is a single Go module (`github.com/blsmth/agentctl`). Subcommand routing uses [Kong](https://github.com/alecthomas/kong): the `CLI` struct in `cli.go` declares every subcommand and its `Run()` method. `main.go` runs a tiered bootstrap and dispatches Claude Code hook subcommands on a fast path before Kong.

### Root-level files (package main)

- **main.go** — Entry point, tiered init, hook fast-path, Kong dispatch, default session start/attach.
- **cli.go** — The Kong `CLI` struct: all subcommands (including hidden internal ones) and their `Run()` methods.
- **monitor.go** — Background monitor loop (headless Bubble Tea, 500ms tick). Reads hook-driven per-window state, resolves `AgentStatus`, fires transition handlers, parses JSONL for metrics, renames tmux windows with state glyphs, and persists `state.json`.
- **birthtime_*.go** — Per-OS file birth-time helpers used for log-rotation (`/clear`) detection.

Most former root-level helpers now live under `internal/` (`internal/improve`, `internal/hooks`, `internal/cmd`, etc.).

### internal/ packages

- **tmux/** — All tmux interactions (sessions, windows, panes, keybindings, `display-popup`/`display-menu`). The native status bar is off; chrome is a two-row Bubble Tea pane docked at the bottom of every window. Session name is `tmux.SessionName` (`agentctl-<dir>`).
- **windowstate/** — Authoritative per-window hook state at `.agentctl/windows/<windowID>.json`. Written by hooks, read by the monitor.
- **state/** — The `state.json` snapshot the monitor writes each tick; read by panes and popups via `LoadSnapshot()`.
- **agent/** — Agent discovery from `~/.claude/agents/*.md` and project-local `.claude/agents/` (local shadows global); `Extends` composes agents.
- **sessions/** — Parses Claude Code's JSONL logs in `~/.claude/projects/` for summaries, metrics, and session metadata sidecars.
- **config/** — Layered settings: project-local `.agentctl/config.json` over global `~/.config/agentctl/config.json`, reloaded every monitor tick. See [docs/configuration.md](docs/configuration.md); `internal/config` structs are the source of truth.
- **dispatch/**, **improve/** — Transition handlers (`dispatch.HandleComplete`, `improve.HandleTransition`) plus dispatch/worktree orchestration and self-improvement launching.
- **hooks/** — Claude Code hook handlers (`hooks.Dispatch`): write windowstate, fire notifications, validate Bash, derive done state.
- **tui/** — Bubble Tea models: the fsnotify-fed chrome bar, menus, wizards, and full-screen browsers.
- **render/** — Leaf package of presentation helpers shared by the chrome bar and the statusline so both render the same numbers.
- **statusline/** — Handles `agentctl statusline`, the command Claude Code pushes its JSON payload to (fast path before Kong). Renders `rich`, `complementary`, `full`, or `minimal` per `statusline_format` and records pushed rate-limit and context data. Git branch is read from `.git/HEAD` with no subprocess; only the dirty count shells out, under a 150ms timeout. Right-alignment (`statusline_align`) pads with U+2800 because Claude Code strips leading whitespace; see `padRune` and `rightAlignReserve` in `statusline.go`.
- **llm/** — Single-turn `llm.Completer` keyed on `llm.provider` (`ollama`, `claude`; empty = disabled). The Ollama client always sends `think:false`, sets `num_ctx`, and errors rather than return a blank or truncated completion. Its only consumer is the work journal; every failure is non-fatal.
- **claude/** — Persistent `claude` CLI subprocess client (NDJSON over stdio), retained as a non-default `llm` provider selectable only with `llm.provider: "claude"`. Kept deliberately as the basis for any future Go agent integration.
- **worktree/** — Git worktree create/remove/list/prune for isolated dispatch, under `<repo>/.worktrees/<slug>/`.
- **cleanup/** — Classifies worktrees (`empty`, `landed`, dirty, unpushed, …) so `agentctl cleanup` and SessionEnd reclaim only what is safe.
- **herd/** — Cross-session aggregation behind `agentctl hub` and `agentctl status`, including pane classification (`ClassifyPane`).
- **mailbox/** — Per-window mailboxes behind `agentctl send`, `inbox`, and `wake`.
- **notify/** — Desktop notifications from `notify_cmd` (`alerter` on macOS, `notify-send` on Linux).

### Behavior notes

- **Notifications** are hook-driven (`internal/hooks/`), not monitor-driven. Settings: see [docs/configuration.md](docs/configuration.md#notifications).
- **Auto-opening PRs**: when a dispatched agent finishes with a PR, the monitor opens it via `gh pr view --web` (config `open_pr_in_browser`), once per PR per monitor process.
- **Worktree reclaim on SessionEnd**: on a terminal `SessionEnd` the hook runs `cleanup.Classify` against the window's own worktree and reclaims it (with its window) only when the verdict is `empty` or `landed`. `git worktree prune` always runs. Opt out with `auto_reclaim_worktree: false`.
- **`bash_preview`**: `bashPreviewGate` (`internal/hooks/preview_gate.go`) only shows the popup when a human is attached **and** looking at the issuing window (`tmuxHasAttachedClient`); otherwise the command passes through. That backstop prevents unattended dispatches from deadlocking on an invisible popup.
- **Command-type config keys** (`notify_cmd`, `hooks.*`, `dispatch.on_complete`) are read only from the global config, never from project-local `.agentctl/config.json`.

### Key data flow

1. `agentctl` starts a tmux session → runs the monitor as a background process (pidfile-guarded)
2. Claude Code hooks write per-window state to `.agentctl/windows/<windowID>.json` (`working`/`blocked`/`idle`)
3. Monitor loops every 500ms, reads each window's hook state, and resolves it to an `AgentStatus`; `Gone` is detected when a window disappears. JSONL is parsed only for metrics/detail.
4. On a state change the monitor builds a `Transition` and calls handlers directly — metrics `transitionTracker`, `dispatch.HandleComplete`, `improve.HandleTransition` (no plugin registry)
5. Monitor renames tmux windows to reflect state (● working, ⏸ blocked, ✓ done-unseen, bare name idle) and writes `state.json`; the chrome bar pane watches `state.json` via fsnotify and re-renders

### Dispatch & Worktrees

`agentctl dispatch --issue N` fetches a GitHub issue, creates an isolated git worktree, and launches an agent on it. This is the primary way tickets get assigned to worker agents.

**Worktree layout:**
- Created at `.worktrees/agent-<name>-issue-<N>/` (gitignored)
- Branch: `agent/<agent-name>/issue-<N>`
- Falls back to shared repo dir if worktree creation fails
- Opt out with `--no-worktree`

**Dispatch flow** (in `dispatchIssue()`, `internal/cmd/dispatch.go`):
1. Fetch issue via `gh issue view`
2. Infer agent from issue labels (or use `--agent` flag, default: `go-engineer`)
3. Resolve working directory (from agent config or `--dir`)
4. Create worktree + branch
5. Build prompt with issue body, worktree path, and autonomous execution instructions
6. Launch agent in tmux window via `tmux.LaunchAgent()`

**Worktree package** (`internal/worktree/`): `Create`, `Remove`, `List`, and `Prune` (`git worktree prune`, which cannot delete live work). `agentctl cleanup` and the SessionEnd hook both call `Prune` and treat failure as non-fatal; see "Worktree reclaim on SessionEnd" above.

**Permission mode:** worker dispatches (`--issue`, `--prompt`, `--prompt-key`) launch claude with `--permission-mode auto` by default; interactive modes get no flag. Override per agent with `permission_mode` (see [docs/configuration.md](docs/configuration.md#per-agent-settings)).

**Cleanup:** `agentctl cleanup` (`internal/cleanup`) classifies every worktree under `.worktrees/` and reclaims the safe ones (see `Classify`'s doc comment). Unknown directories under `.worktrees/` are reported as `skip` and never deleted.

**`--prompt-key <key>`:** `agentctl dispatch --prompt-key <key> --agent <name>` reads the ad-hoc prompt from the context store instead of `--prompt`. The approval popup sees the raw, unexpanded command, so `--prompt "$(cat spec.md)"` would show a reviewer nothing useful; a context key can be resolved and shown. It is mutually exclusive with `--prompt` and `--issue`.

### Querying agent status (`agentctl status`)

`agentctl status` is the scriptable counterpart to `agentctl hub` (same `internal/herd` aggregation). Use it instead of hand-joining `tmux list-windows` with `jq` over windowstate files. Subcommands: `status [<window>]` (table or detail; `--json`, `--session`, `--state`, `--blocked`, `--verify`), `status peek <window>`, and `status whoami`.

Three behaviors are deliberate:

- **`PARENT` is blank for bare windows.** Only dispatched windows record a dispatcher; bare windows would otherwise report whichever window was created last (`herd.Meta.Parent()` returns `""` rather than a plausible wrong answer).
- **Stale snapshots are flagged on stderr** (`herd.SnapshotStaleAfter`), since a wedged monitor freezes `state.json` instead of removing it.
- **`--verify` trusts the pane over the record** via `herd.ClassifyPane`. `⏸` is not a blocked signal (it is in every pane's footer); classification matches dialog text.

## Adding a transition handler

There is no plugin registry. Add your reactive logic as a function and call it from `detectAndFireTransitions()` in `monitor.go`, alongside the existing `dispatch.HandleComplete` / `improve.HandleTransition` calls. It receives the `state.Transition` and the agent's `config.AgentConfig`.

## Adding a new subcommand

Add a field to the `CLI` struct in `cli.go` with a `Run()` method (use the `hidden:""` tag for internal-only commands). If it needs a tmux menu, follow the `display-menu`/`display-popup` patterns in `internal/tmux`. If it needs a TUI popup, add a Bubble Tea model in `internal/tui/`.

Issue labels and PR conventions are in [CONTRIBUTING.md](CONTRIBUTING.md).

## Design Notes & Patterns

Development conventions (tmux patterns, popup architecture, TUI conventions) are in [docs/development.md](docs/development.md); the other docs are indexed in [docs/README.md](docs/README.md).

### Session identity

The window's `SessionID` (written by `hook-session-start`, including after `/clear`) is authoritative for log linking. When set, `linkLogs` links the recorded `LogPath` or derives the log from the session ID (`linkBySessionID`, which calls `sessions.FindLogBySessionIDIn`), and never scans `~/.claude/projects/` for the newest log. Only windows with no SessionID fall back to a scan, confined to the window's own project directory. The statusline drops snapshot data whose log does not match the payload's `session_id`.

## Dependencies

- Go 1.26+ (build only), tmux 3.4+; optional `alerter` (macOS) / `notify-send` (Linux) for notifications, configurable via `notify_cmd`
- Bubble Tea / Lip Gloss for TUI popups
- `gopkg.in/yaml.v3` for agent frontmatter parsing
