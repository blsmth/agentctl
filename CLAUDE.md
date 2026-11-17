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
- **winname/** — Computes the window name (glyph + instance) from windowstate and renames the tmux window inside the writer's transaction (`winname.Update`/`Transact`, `SyncName`).
- **state/** — The `state.json` snapshot the monitor writes each tick (read by the macapp; agentctl readers build their view from `herd.LoadAgents` and use a fresh snapshot only as optional token enrichment), plus `AgentState`, the restore builders, and the snapshot types.
- **agent/** — Agent discovery from `~/.claude/agents/*.md` and project-local `.claude/agents/` (local shadows global); `Extends` composes agents.
- **sessions/** — Parses Claude Code's JSONL logs in `~/.claude/projects/` for summaries, metrics, and session metadata sidecars. Also owns session cost: `CostOfSummary` prices per request through `tokens.Lookup` (unknown model = unknown cost, never a Sonnet default) and `ClassifyBilling` assigns `metered`/`sub`/`free`; surfaced by `agentctl cost`. Pricing lives only in `internal/tokens`.
- **config/** — Layered settings: project-local `.agentctl/config.json` over global `~/.config/agentctl/config.json`, reloaded every monitor tick. See [docs/configuration.md](docs/configuration.md); `internal/config` structs are the source of truth.
- **dispatch/**, **improve/** — `improve.HandleTransition` (monitor transition handler) and `dispatch.RunComplete` (hook-spawned completion), plus dispatch/worktree orchestration and self-improvement launching.
- **hooks/** — Claude Code hook handlers (`hooks.Dispatch`): write windowstate, fire notifications, validate Bash, derive done state.
- **tui/** — Bubble Tea models: the fsnotify-fed chrome bar, menus, wizards, and full-screen browsers.
- **render/** — Leaf package of presentation helpers shared by the chrome bar and the statusline so both render the same numbers.
- **statusline/** — Handles `agentctl statusline`, the command Claude Code pushes its JSON payload to (fast path before Kong). Renders `rich`, `complementary`, `full`, or `minimal` per `statusline_format` and records pushed rate-limit and context data. Git branch is read from `.git/HEAD` with no subprocess; only the dirty count shells out, under a 150ms timeout. Right-alignment (`statusline_align`) pads with U+2800 because Claude Code strips leading whitespace; see `padRune` and `rightAlignReserve` in `statusline.go`.
- **llm/** — Single-turn `llm.Completer` keyed on `llm.provider` (`ollama`, `claude`; empty = disabled). The Ollama client always sends `think:false`, sets `num_ctx`, and errors rather than return a blank or truncated completion. Its only consumer is the work journal; every failure is non-fatal.
- **claude/** — Persistent `claude` CLI subprocess client (NDJSON over stdio), retained as a non-default `llm` provider selectable only with `llm.provider: "claude"`. Kept deliberately as the basis for any future Go agent integration.
- **worktree/** — Git worktree create/remove/list/prune for isolated dispatch, under `<repo>/.worktrees/<slug>/`.
- **cleanup/** — Classifies worktrees (`empty`, `landed`, dirty, unpushed, …) so `agentctl cleanup` and SessionEnd reclaim only what is safe.
- **herd/** — Cross-session aggregation behind `agentctl hub` and `agentctl status`, including pane classification (`ClassifyPane`).
- **mailbox/** — Per-window store for agentctl-originated notices (done/blocked backstop), delivered by the UserPromptSubmit/PostToolUse hooks. Agent-to-agent messaging is Claude Code's native `SendMessage`/`ListAgents`, not this package.
- **notify/** — Desktop notifications from `notify_cmd` (`alerter` on macOS, `notify-send` on Linux).

### Behavior notes

- **Monitor hot reload**: monitor-side changes go live within about one tick of `just install`, with no relaunch needed. The monitor records its binary identity in `<runtimeDir>/monitor.build` and re-execs itself (same PID) when the file changes (`monitor_reexec.go`); the hook watchdog replaces older monitors that never wrote that file. In-memory state resets on re-exec (see [docs/monitor-architecture.md](docs/monitor-architecture.md#hot-reload)). The hub list (`hub-view`) hot-reloads too: on its poll tick it quits cleanly when the binary changes, then execs the new one, passing the selection via `AGENTCTL_HUB_SELECT`. Shared logic lives in `internal/selfexec`.
- **Notifications** are hook-driven (`internal/hooks/`), not monitor-driven. Settings: see [docs/configuration.md](docs/configuration.md#notifications).
- **Auto-opening PRs**: when a dispatched agent finishes with a PR, completion runs from the Stop hook (or `agentctl signal done`), not the monitor: it spawns a detached `agentctl dispatch-complete <window>` that opens the PR via `gh pr view --web` (config `open_pr_in_browser`), once per PR, deduped across processes by marker files in `<runtimeDir>/opened-prs/`.
- **Worktree reclaim on SessionEnd**: on a terminal `SessionEnd` the hook runs `cleanup.Classify` against the window's own worktree and reclaims it (with its window) only when the verdict is `empty` or `landed`. `git worktree prune` always runs. Opt out with `auto_reclaim_worktree: false`.
- **`bash_preview`**: `bashPreviewGate` (`internal/hooks/preview_gate.go`) only shows the popup when a human is attached **and** looking at the issuing window (`tmuxHasAttachedClient`); otherwise the command passes through. That backstop prevents unattended dispatches from deadlocking on an invisible popup. Unanswered popups (dispatch and bash) deny after a deadline (`popupDeadline`, 9m against the 600s `timeout` registered on `hook-validate-bash`) and the popup is closed with `tmux display-popup -C`, rather than failing open when Claude Code kills the hook. While a popup is open the window is marked `blocked` (PendingTool `dispatch`/`Bash`) with a desktop notification (`approval_pending`, gated by `alert_config.blocked`) and a marker file in `<runtimeDir>/approval-pending/` that makes the monitor defer `gh pr view --web` auto-open (up to ~2 minutes, then skip). State is restored to `working` on every exit path.
- **Cost**: one pricing source, `internal/tokens` (config overrides, then the cached Nous catalog, then built-ins, then newest-generation family fallback). Tiered models (Haiku 5.5) are priced per request, so session totals are summed per turn, and the parser dedups JSONL lines by `message.id`. The statusline hides Claude Code's pushed `total_cost_usd` for non-`claude-*` models. See [docs/configuration.md](docs/configuration.md#pricing-and-cost).
- **Backends** (Ollama and Nous Portal via `backends` + `agents.*.backend`, global-only; `dispatch --backend`): `nous` delivers its API key through `apiKeyHelper` (`agentctl backend-token`), never through env or config. See [docs/configuration.md](docs/configuration.md#backends-ollama-and-nous-portal).
- **Session restore** reads `<runtimeDir>/restore.json`, built from per-window `windowstate` records at every point that destroys windows (`StartSession` before `RemoveAll`, `tmux.KillSession`, relaunch-prep, `save-state`), not from the monitor's `state.json`. `state.json` is only a one-release fallback when `restore.json` is absent. See [docs/monitor-architecture.md](docs/monitor-architecture.md#session-restore-restorejson).
- **Gone and housekeeping without the monitor**: hidden `agentctl reconcile` (flock-guarded, idempotent) claims records of vanished windows and fires the Gone handlers once. It is spawned detached after `tmux.KillWindow`/SessionEnd kills, run by a session `window-unlinked` tmux hook, and spawned by a 60s-stamped janitor on SessionStart/UserPromptSubmit (`--housekeeping` adds orphan reaping, mailbox/session-log pruning, approval-marker GC). `client-resized`/`client-attached` hooks run `enforce-chrome`. `agentctl apply-config` re-applies hooks and keybindings to a live session. See [docs/monitor-architecture.md](docs/monitor-architecture.md#gone-and-housekeeping-without-the-monitor).
- **Monitor kill switch**: global config `monitor: false` (default true) stops session start / `applyConfig` and the hook watchdog from starting or replacing a monitor, and `agentctl health` reports "monitor disabled". A running monitor is never killed by the flag. Like `auto_improve`, a project may switch it off but never back on.
- **Command-type config keys** (`notify_cmd`, `hooks.*`, `dispatch.on_complete`) are read only from the global config, never from project-local `.agentctl/config.json`.

### Key data flow

1. `agentctl` starts a tmux session → runs the monitor as a background process (pidfile-guarded)
2. Claude Code hooks write per-window state to `.agentctl/windows/<windowID>.json` (`working`/`blocked`/`idle`)
3. Monitor loops every 500ms, reads each window's hook state, and resolves it to an `AgentStatus`; `Gone` is detected when a window disappears (monitor after two absent ticks, or the monitor-free `agentctl reconcile` via the shared `internal/reconcile` claim). JSONL is parsed only for metrics/detail. Readers (chrome bar, hub, `status`, popups) do not depend on it: they join `tmux list-windows` with `windowstate.AllFrom` via `herd.LoadAgents`. Config `monitor: false` stops agentctl starting it.
4. On a state change the monitor builds a `Transition` and calls handlers directly — metrics `transitionTracker` and `improve.HandleTransition` (no plugin registry). Dispatch completion is hook-driven instead: the hook that sets `Done=true` (Stop hook, `signal done`) stamps `CompleteFiredAt` and spawns a detached `agentctl dispatch-complete <window>` (PR lookup, label swap, open PR, `dispatch_complete` hooks)
5. The state writers (hooks, `signal`, `set-active-window`) rename tmux windows via `internal/winname` inside their windowstate transaction (● working, ⏸ blocked, ✓ done-unseen, bare name idle); `Seen` is a persisted windowstate field. The monitor writes `state.json` for the macapp only; the chrome bar pane watches `.agentctl/windows/` and `active-window` via fsnotify, rebuilds through `herd.BuildAgents`, and re-renders only when a rendered field changed

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

**Sweep:** `agentctl sweep` is the window-centric counterpart, scoped to the *current* tmux session only (session from `$TMUX_PANE`, or `--session` outside tmux). It closes finished windows (including ones with no worktree), reuses `cleanup.Classify` for windows that own one, and lists the rest with the action each needs. Classification is the pure `cleanup.ClassifyWindow`; self, `shell`, `improve~*`, untracked windows, and bare windows (unless `--include-bare`) are never closed. `--dry-run` plans only.

**`--prompt-key <key>`:** `agentctl dispatch --prompt-key <key> --agent <name>` reads the ad-hoc prompt from the context store instead of `--prompt`. The approval popup sees the raw, unexpanded command, so `--prompt "$(cat spec.md)"` would show a reviewer nothing useful; a context key can be resolved and shown. It is mutually exclusive with `--prompt` and `--issue`. Run `agentctl context set <key> ...` in a **separate Bash call before** the dispatch, not chained with `&&`: the PreToolUse gate runs before any part of the command executes. As a fallback the gate reads a literal `context set <key> <value>` earlier in the same command line (single-quoted, or double-quoted without `$`/backticks); if the prompt still cannot be resolved, the dispatch is denied rather than shown with a placeholder.

### Querying agent status (`agentctl status`)

`agentctl status` is the scriptable counterpart to `agentctl hub` (same `internal/herd` aggregation). Use it instead of hand-joining `tmux list-windows` with `jq` over windowstate files. Subcommands: `status [<window>]` (table or detail; `--json`, `--session`, `--state`, `--blocked`, `--verify`), `status peek <window>`, and `status whoami`.

Rows come from `tmux list-windows` plus windowstate (`herd.LoadAgents`), so `status` works with the monitor off. Two behaviors are deliberate:

- **`PARENT` is blank for bare windows.** Only dispatched windows record a dispatcher; bare windows would otherwise report whichever window was created last (`herd.Meta.Parent()` returns `""` rather than a plausible wrong answer).
- **`--verify` trusts the pane over the record** via `herd.ClassifyPane`. `⏸` is not a blocked signal (it is in every pane's footer); classification matches dialog text.

## Adding a transition handler

There is no plugin registry. Add your reactive logic as a function and call it from `detectAndFireTransitions()` in `monitor.go`, alongside the existing `improve.HandleTransition` call. It receives the `state.Transition` and the agent's `config.AgentConfig`.

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
