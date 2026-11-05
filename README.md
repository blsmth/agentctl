<p align="center">
  <img src="assets/wordmark.svg" alt="agentctl" width="480"/>
</p>

<p align="center"><i>tmux-native agent wrangling.</i></p>

<p align="center">
  <a href="https://github.com/blsmth/agentctl/actions/workflows/ci.yaml"><img src="https://github.com/blsmth/agentctl/actions/workflows/ci.yaml/badge.svg" alt="CI"/></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/blsmth/agentctl" alt="License: MIT"/></a>
  <a href="https://github.com/blsmth/agentctl/releases"><img src="https://img.shields.io/github/v/release/blsmth/agentctl?include_prereleases" alt="Release"/></a>
</p>

agentctl is a tmux-native command center for running many [Claude Code](https://docs.anthropic.com/en/docs/claude-code) agents at once. Every agent gets its own tmux window. Claude Code hooks report whether each agent is working, blocked, or idle, and agentctl surfaces that in a two-row bar at the bottom of every window, in desktop notifications, and in a cross-session hub. It can also turn a GitHub issue into an isolated worktree, a running agent, and a pull request.

There is no daemon, orchestrator, or web app. tmux already provides process isolation, persistence, `capture-pane`, and `send-keys`.

## Requirements

- tmux 3.4+
- [Claude Code](https://docs.anthropic.com/en/docs/claude-code)
- git
- [`gh`](https://cli.github.com/), authenticated, for `dispatch` and ticket commands
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
agentctl install-plugin          # install the agentctl Claude Code plugin (embedded in the binary)
```

Hooks are what drive agent state, so `hooks-install` is the important one. Run `agentctl <command> --help` for flags.

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
```

## How it works

Each agent is a tmux window. Claude Code hooks write per-window state (`working`, `blocked`, `idle`) and a background monitor reads it every 500ms, renames windows with state glyphs, fires notifications, and writes a snapshot the chrome bar renders. State comes from hooks; session JSONL logs are read only for metrics. Token, context, and rate-limit figures are shown by the Claude Code status line (`agentctl statusline`).

You also get TUI browsers for agents, skills, memory files, issues, and session history; per-agent permissions; a cross-agent context store and mailboxes (`context`, `send`, `inbox`, `wake`); optional Prometheus metrics; and self-improvement sessions. Every action is a keybinding off `ctrl+b`; see [docs/keybindings.md](docs/keybindings.md).

## Supported platforms

macOS and Linux. Windows is not supported (WSL may work but is untested).

`macapp/` contains an **experimental**, macOS-only companion app; see [macapp/README.md](macapp/README.md).

## Documentation

| | |
|---|---|
| [Configuration](docs/configuration.md) | Every config key, layering, and the global-only command keys |
| [Dispatch](docs/dispatch.md) | Ticket-to-PR orchestration |
| [Permissions](docs/permissions.md) | Per-agent command safety |
| [Keybindings](docs/keybindings.md) | Keybinding reference |
| [Architecture](docs/architecture.md) | System design and source layout |
| [All docs](docs/README.md) | Full index, including design records |

Contributing: [CONTRIBUTING.md](CONTRIBUTING.md). Security: [SECURITY.md](SECURITY.md). Changes: [CHANGELOG.md](CHANGELOG.md).

## License

[MIT](LICENSE)
