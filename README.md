<p align="center">
  <img src="assets/wordmark.svg" alt="agentctl" width="480"/>
</p>

<p align="center"><i>tmux-native agent wrangling.</i></p>

<p align="center">
  <a href="https://github.com/blsmth/agentctl/actions/workflows/ci.yaml"><img src="https://github.com/blsmth/agentctl/actions/workflows/ci.yaml/badge.svg" alt="CI"/></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/blsmth/agentctl" alt="License: MIT"/></a>
  <a href="https://github.com/blsmth/agentctl/releases"><img src="https://img.shields.io/github/v/release/blsmth/agentctl?include_prereleases" alt="Release"/></a>
</p>

agentctl is a tmux-native command center for running many coding agents at once: [Claude Code](https://docs.anthropic.com/en/docs/claude-code) by default, and [pi](https://github.com/earendil-works/pi). Every agent gets its own tmux window. Hooks report whether each agent is working, blocked, or idle, and agentctl surfaces that in the window names on the tmux status line, in desktop notifications, in a cross-session hub, and on your phone. It can also turn a GitHub issue into an isolated worktree, a running agent, and a pull request.

tmux is the runtime: it already provides process isolation, persistence, `capture-pane`, and `send-keys`. There is no daemon to keep alive; the per-machine pieces (a phone web app server, a cron ticker, a metrics exporter) are opt-in.

## Requirements

- tmux 3.4+
- [Claude Code](https://docs.anthropic.com/en/docs/claude-code)
- git
- [`gh`](https://cli.github.com/), authenticated, for `dispatch` and ticket commands
- Optional: [pi](https://github.com/earendil-works/pi) 1.0+, to run agents on pi
- Optional: [`alerter`](https://github.com/vjeantet/alerter) (macOS) or `notify-send` (Linux) for desktop notifications
- Go 1.26+, only to build from source

## Install

```bash
go install github.com/blsmth/agentctl@latest
```

Or download a release tarball for darwin/linux (amd64/arm64) from the [releases page](https://github.com/blsmth/agentctl/releases) and put `agentctl` on your `PATH`.

Or build from source:

```bash
git clone https://github.com/blsmth/agentctl.git
cd agentctl
go build -o agentctl .
```

Check it with `agentctl version`.

## First run

Run these once. Each is idempotent.

```bash
agentctl init                    # initialize agentctl in the current project
agentctl hooks-install --global  # install Claude Code hooks (omit --global for this project only)
agentctl statusline install      # use agentctl as Claude Code's status line
agentctl install-templates       # starter agents: architect, devops-engineer, go-engineer, python-engineer, terraform-engineer
```

Hooks are what drive agent state, so `hooks-install` is the important one. Run `agentctl <command> --help` for flags. The phone app and scheduled runs have their own one-time setup: `agentctl serve install` then `agentctl serve pair` ([details](docs/design/mobile-web-app.md)), and `agentctl cron install`.

Slash commands (`/agentctl:dispatch`, `context`, `status`, `sweep`, `logs`, `restart`, `artifact`) and the `artifacts` skill (teaches agents to publish reports, charts and mockups with `agentctl artifact`, with HTML templates) install as a Claude Code plugin, from inside Claude Code:

```
/plugin marketplace add blsmth/agentctl
/plugin install agentctl@agentctl
```

## Quickstart

Inside your project:

```bash
agentctl                         # start (or attach to) the tmux session for this project
```

Launch an agent from the keybinding menu (`ctrl+b a`), or dispatch one at a GitHub issue:

```bash
agentctl dispatch --issue 42
agentctl dispatch --issue 43 --agent python-engineer
```

Dispatch fetches the issue, creates a worktree at `.worktrees/agent-<name>-issue-<N>/` on branch `agent/<name>/issue-<N>`, launches the agent in its own window, and lets it work toward a PR. Other modes include `triage`, `review`, and `discuss`; see [docs/dispatch.md](docs/dispatch.md).

Check on everything from a shell:

```bash
agentctl status                  # every agent in every live session
agentctl status --blocked        # only the ones waiting on you
agentctl hub                     # cross-session dashboard (ctrl+b g)
agentctl open [dir]              # start or switch to a project's session
```

## How it works

Each agent is a tmux window. Claude Code hooks write per-window state (`working`, `blocked`, `idle`) to a small JSON file, rename the window with a state glyph (`●` working, `⏸` blocked, `✓` done), and fire notifications. Everything else (the hub, `agentctl status`, the phone app) reads those files plus `tmux list-windows`. Session JSONL logs are read only for tokens, cost and summaries, and the Claude Code status line (`agentctl statusline`) shows model, context, rate limits and cost. See [docs/architecture.md](docs/architecture.md).

## Features

- **Dispatch**: an issue, ad-hoc prompt or PR review becomes a worktree, an agent and a PR; orchestrator agents coordinate workers, and dispatches by agents need your approval in a popup. [Dispatch](docs/dispatch.md)
- **Fleet hub**: every agent in every project in one tmux window, as a parent/child tree with a live agent pane, short AI titles, sweep, closed agents with resume, and per-project Schedules tabs. [Keybindings](docs/keybindings.md#fleet-hub)
- **Phone web app**: `agentctl serve` shows the fleet on your phone over your LAN, with a live terminal, replies, approvals, AskUserQuestion answers, dispatch and push notifications. [Mobile web app](docs/design/mobile-web-app.md)
- **Scheduled runs**: `agentctl cron` launches agents on a schedule with usage gates and run history. [cron](docs/configuration.md#cron)
- **Accounts, backends, harnesses**: run agents under several Claude logins, against Ollama or Nous Portal, or on pi. [Configuration](docs/configuration.md#accounts-multiple-claude-logins)
- **Cost**: `agentctl cost` prices every session from one table, including agentctl's own background LLM calls. [Pricing and cost](docs/configuration.md#pricing-and-cost)
- **Safety**: per-agent deny lists, approval popups, tamper rules and an audit trail of every tool call. [Permissions](docs/permissions.md)
- **Also**: cleanup and sweep of finished worktrees and windows, TUI browsers for agents, sessions, issues and the context store, session restore after a relaunch, `agentctl race` ([replay a session against a human](docs/commands.md#race)), optional Prometheus metrics, and self-improvement sessions.

Every action is a keybinding off `ctrl+b` ([docs/keybindings.md](docs/keybindings.md)), and every command is listed in [docs/commands.md](docs/commands.md).

## Supported platforms

macOS and Linux. Windows is not supported (WSL may work but is untested).

## Documentation

| | |
|---|---|
| [Commands](docs/commands.md) | Every subcommand, grouped by job |
| [Configuration](docs/configuration.md) | Every config key, its default and scope, layering |
| [Dispatch](docs/dispatch.md) | Ticket-to-PR orchestration |
| [Permissions](docs/permissions.md) | Per-agent command safety |
| [Keybindings](docs/keybindings.md) | Keybinding reference |
| [Architecture](docs/architecture.md) | System design and source layout |
| [All docs](docs/README.md) | Full index, including design records |

Contributing: [CONTRIBUTING.md](CONTRIBUTING.md). Security: [SECURITY.md](SECURITY.md). Changes: [CHANGELOG.md](CHANGELOG.md).

## License

[MIT](LICENSE)
