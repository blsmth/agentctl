# Changelog

All notable changes to this project are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project aims to follow [Semantic Versioning](https://semver.org/).

## [Unreleased]

First public release.

### Removed

- The experimental macOS companion app (`macapp/`). The hub and the mobile web app (`agentctl serve`) replace it.

### Fixed

- A Go test binary can no longer spawn itself. `selfpath.Spawnable` (used by every exec/spawn of the running binary) refuses under `go test`, `detach.Start` refuses to start the test binary, and a source-scan test forbids direct `os.Executable` calls. This follows an incident where a detached `hooks.test` reran the suite recursively and exhausted the per-user process limit. `detach.Start` also caps nesting with `AGENTCTL_DETACH_DEPTH` (refuses at depth 3), and `internal/notify` never runs the default notifier, `alerter` or `notify-send` under test.

### Added

- tmux-native management of multiple Claude Code agents: one window per agent, hook-driven state (working, blocked, idle), a two-row chrome bar, window state glyphs, and a cross-session hub (`agentctl hub`, `agentctl status`).
- `agentctl dispatch --issue N`: fetch a GitHub issue, create an isolated git worktree, launch an agent, and work toward a PR. Accepts `--repo owner/name` to override the repo (`AGENTCTL_REPO` is exported to the dispatched agent for information only) and errors if the repo cannot be determined. Also `--prompt` and `--prompt-key` for ad-hoc work.
- Worktree cleanup (`agentctl cleanup`) and automatic reclaim of empty or landed worktrees on session exit.
- Claude Code status line integration (`agentctl statusline`, formats `rich`, `complementary`, `full`, `minimal`) showing rate limits, context, tokens, cost, and git state.
- Cross-agent context store and session search. Agents message each other with Claude Code native cross-session messaging (`SendMessage`/`ListAgents`): every session launches with a stable `-n` name, dispatched workers are told their orchestrator, and agentctl adds short factual `agentctl notice:` lines when a worker goes idle or blocks.
- Per-agent permissions, a dispatch approval popup, and optional `bash_preview` for every Bash call.
- Notifications via `alerter` (macOS) or `notify-send` (Linux); `notify_cmd` customizes or disables them.
- Optional work journal backed by a local LLM (`llm.provider: ollama`), with a durable journal and JSONL sidecar.
- Optional Prometheus metrics and dashboards.
- agentctl slash commands (`/agentctl:dispatch`, `context`, `status`, `logs`, `restart`) install via the Claude Code plugin marketplace (`/plugin marketplace add blsmth/agentctl`, then `/plugin install agentctl@agentctl`); `agentctl install-plugin` no longer copies files and just prints those commands. `agentctl version` prints a semver.
- Hooks and popups invoke the running binary (`os.Executable()`), so `go install github.com/blsmth/agentctl@latest` and release tarballs both work.
- Release builds for darwin and linux on amd64 and arm64 (goreleaser).
- Experimental macOS companion app (`macapp/`).

### Security

- Command-type config keys (`notify_cmd`, `hooks.*`, `dispatch.on_complete`) are honored only from the global config, never from a project-local `.agentctl/config.json`.
- The global config honors `XDG_CONFIG_HOME`.

### Platforms

- macOS and Linux. Windows is not supported.
