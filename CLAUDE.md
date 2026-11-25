# CLAUDE.md

Loaded into every agent session: keep under 15KB. One line per package; details belong in docs/ (indexed in [docs/README.md](docs/README.md)). A test fails above 16KB.

## What this is

agentctl is a tmux-native command center for managing multiple coding agents: Claude Code by default, and pi (see `internal/harness`). tmux is the runtime: each agent runs in its own window, Claude Code hooks write per-window state (working/blocked/idle) that a background monitor reads each tick.

## Build & Run

```bash
go build -o agentctl .    # build the binary
./agentctl                 # start or attach to the tmux session
go test ./...              # run the full test suite
golangci-lint run          # lint (config in .golangci.yml)
```

Tests live alongside the code, at the repo root and throughout `internal/**`. Issue labels and PR conventions: [CONTRIBUTING.md](CONTRIBUTING.md).

## Architecture

One Go module (`github.com/blsmth/agentctl`). [Kong](https://github.com/alecthomas/kong) routes subcommands: the `CLI` struct in `cli.go` declares every subcommand and its `Run()`. `main.go` does a tiered bootstrap and handles hook subcommands on a fast path before Kong. Root files: `main.go`, `cli.go`, `monitor.go` (headless Bubble Tea monitor, 500ms tick), `birthtime_*.go`. Source layout: [docs/architecture.md](docs/architecture.md). Full per-package text: [docs/package-reference.md](docs/package-reference.md).

### Package map (`internal/`)

- **tmux** — all tmux interactions; chrome bar gated by `chrome_bar`.
- **windowstate** — authoritative per-window hook state, `.agentctl/windows/<id>.json`.
- **winname** — window name (glyph + instance), renamed inside the writer's transaction.
- **state** — `state.json` snapshot, `AgentState`, restore builders.
- **agent** — agent discovery from `~/.claude/agents` and project `.claude/agents`.
- **sessions** — JSONL log parsing, session metrics, cost (`CostOfSummary`, `ClassifyBilling`).
- **claudehome** — per-account Claude config dirs (`CLAUDE_CONFIG_DIR`); [config](docs/configuration.md#accounts-multiple-claude-logins).
- **config** — layered settings, project over global; [docs/configuration.md](docs/configuration.md).
- **dispatch**, **dispatchplan**, **dispatchapproval** — dispatch/worktree orchestration, precedence rules, single-use approval markers; [docs/dispatch.md](docs/dispatch.md).
- **improve**, **improverun** — manual self-improvement sessions and their run records.
- **harness** — agent program per window (Claude or pi); [config](docs/configuration.md#harnesses-claude-code-and-pi).
- **transcript** — normalizes non-Claude transcripts into Claude JSONL shape.
- **hooks** — Claude Code hook handlers, gates, tamper rules; [docs/claude-code-hooks.md](docs/claude-code-hooks.md), [docs/permissions.md](docs/permissions.md).
- **tui**, **render**, **statusline** — Bubble Tea UIs, shared presentation helpers, `agentctl statusline`.
- **llm**, **llmusage**, **pricecache**, **claude** — single-turn completers, the LLM-call ledger, price cache, `claude` CLI client.
- **shorttitle** — 2-5 word row titles; [docs/configuration.md#titles](docs/configuration.md#titles).
- **closedlog**, **verdicts** — machine-wide NDJSON ledgers of closed windows and PR review verdicts.
- **worktree**, **cleanup** — git worktree ops; safe-reclaim classification.
- **fleet**, **mailbox** — cross-session aggregation (hub, `status`); agentctl notices.
- **metrics**, **metricsserve**, **metricsproc** — Prometheus; [docs/monitor-architecture.md](docs/monitor-architecture.md#metrics-without-the-monitor).
- **notify**, **launchd** — desktop notifications; LaunchAgent plumbing.
- **serve**, **servectl** — HTTPS server for the phone app; [docs/design/mobile-web-app.md](docs/design/mobile-web-app.md).
- **cron**, **cronedit** — scheduled runs and their one editing path; [docs/design/cron.md](docs/design/cron.md), [docs/configuration.md#cron](docs/configuration.md#cron).
- **agenthistory** — hidden git history of agent/config dirs.
- **prompttemplate** — prompt templates; [docs/configuration.md#prompt-templates](docs/configuration.md#prompt-templates).
- **race**, **sessionaudit** — `agentctl race` model; `audit report` grader ([docs/permissions.md](docs/permissions.md#session-audit-reports)).
- **artifacts** — machine-wide store of agent-published artifacts; [docs/design/artifacts.md](docs/design/artifacts.md).
- **artifactpage**, **artifactpdf** — shared markdown/text/image artifact pages; `artifact pdf` export via headless Chrome ([PDF export](docs/design/artifacts.md#pdf-export)).
- **demo** — screenshot harness, never linked into the binary; [docs/development.md](docs/development.md#demo-harness-and-screenshots).

## Key data flow

1. `agentctl` starts a tmux session and a pidfile-guarded background monitor (`monitor: false` disables it).
2. Hooks write per-window state to `.agentctl/windows/<windowID>.json`; state writers rename windows via `internal/winname`.
3. The monitor ticks every 500ms, resolves `AgentStatus`, and writes `state.json` (token enrichment only). Readers (chrome bar, hub, `status`) join `tmux list-windows` with windowstate via `fleet.LoadAgents`, not the monitor.
4. Gone windows are handled by the monitor or the monitor-free `agentctl reconcile`; dispatch completion is hook-driven (`dispatch-complete`).
5. Session restore reads `<runtimeDir>/restore.json`, built from windowstate.
6. Details: [docs/behavior-notes.md](docs/behavior-notes.md), [docs/monitor-architecture.md](docs/monitor-architecture.md).

## Dispatch & worktrees

`agentctl dispatch --issue N` fetches an issue, creates a worktree and launches an agent. Layout: `.worktrees/agent-<name>-issue-<N>/` (gitignored), branch `agent/<agent-name>/issue-<N>`; falls back to the shared dir on failure; `--no-worktree` opts out. Modes: issue, ad-hoc prompt, `--review-pr N`, bare/orchestrator; worker dispatches launch with `--permission-mode auto`. Details: [docs/dispatch.md](docs/dispatch.md), [docs/prompt-architecture.md](docs/prompt-architecture.md), [docs/behavior-notes.md](docs/behavior-notes.md).

**`--prompt-key <key>` ordering rule:** run `agentctl context set <key> ...` in a **separate Bash call before** the dispatch, never chained with `&&`: the PreToolUse gate runs before any part of the command executes and must be able to resolve and show the prompt. An unresolvable key denies the dispatch.

## Session identity

The window ID and pane ID (`main_pane`) are identity; names are not (Claude Code renames duplicates, and the tmux session prefix in a ListAgents location changes). Link logs by the window's `SessionID`; parent/child linkage uses `spawned_by` plus `spawned_by_session`, never a name. See [docs/behavior-notes.md](docs/behavior-notes.md#session-identity).

## Load-bearing rules

- **Gated commands are security boundaries.** `dispatch`, `cron add|edit|resume|run --force` and template edits go through the approval popup; the `dispatch_approval`, tamper (cron/template/approval/artifacts/config/serve) and `go run` guards must not be weakened. Read [docs/permissions.md](docs/permissions.md) before touching them.
- **Command-type config keys** (`notify_cmd`, `hooks.*`, `dispatch.on_complete`) are read only from the global config, never project-local.
- **Never resolve home-derived paths at package init**; a new test package needs a `testenv` `TestMain`.
- **New detach/`os.Executable` spawns must be stubbed in tests** (see `launchwatch.SetSpawnForTest`, `launchtest.Capture`).
- **New `web/*.js` files must be registered in `assets.go`.**

## Where to read before touching X

| Touching | Read |
|---|---|
| Hooks, gated commands, tamper rules | [docs/permissions.md](docs/permissions.md), [docs/claude-code-hooks.md](docs/claude-code-hooks.md) |
| Dispatch, prompts, worktrees, sweep | [docs/dispatch.md](docs/dispatch.md), [docs/prompt-architecture.md](docs/prompt-architecture.md) |
| Serve, phone app, remote approvals | [docs/design/mobile-web-app.md](docs/design/mobile-web-app.md) |
| Cron | [docs/design/cron.md](docs/design/cron.md), [docs/configuration.md#cron](docs/configuration.md#cron) |
| Titles | [docs/configuration.md#titles](docs/configuration.md#titles) |
| Monitor, reconcile, restore, metrics | [docs/monitor-architecture.md](docs/monitor-architecture.md) |
| Config keys, accounts, backends, pricing | [docs/configuration.md](docs/configuration.md) |
| Hub, keys | [docs/keybindings.md](docs/keybindings.md), [docs/design/fleet-view-design.md](docs/design/fleet-view-design.md) |
| Any package in detail, `status` semantics | [docs/package-reference.md](docs/package-reference.md), [docs/behavior-notes.md](docs/behavior-notes.md) |
| TUI/tmux conventions, tests, demo | [docs/development.md](docs/development.md) |

## Adding a transition handler

No plugin registry and no handler is registered today. Set `transitionHandlerFn` in `monitor.go` (called by `fireHandlers` under `safeHandle`); the Gone path has the optional `reconcile.Deps.Handle`.

## Adding a new subcommand

Add a field to the `CLI` struct in `cli.go` with a `Run()` method (`hidden:""` for internal-only). Tmux menus follow the `display-menu`/`display-popup` patterns in `internal/tmux`; TUI popups are Bubble Tea models in `internal/tui/`.

## Dependencies

- Go 1.26+ (build only), tmux 3.4+; optional `alerter` (macOS) / `notify-send` (Linux), configurable via `notify_cmd`
- Bubble Tea / Lip Gloss for TUI popups
- `gopkg.in/yaml.v3` for agent frontmatter parsing
