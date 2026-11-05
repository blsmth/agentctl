## agentctl Reference

Quick reference for agents that need to understand agentctl's architecture when triaging tickets, writing specs, or coordinating work, without the full dev guide.

### What it is

agentctl is a tmux-native command center for managing multiple Claude Code agents. tmux is the runtime: each agent runs in its own window, Claude Code hooks write per-window state (working/blocked/idle), and a background monitor reads that state and reacts to transitions.

### Key packages

| Package | Purpose |
|---------|---------|
| `main.go` | Entry point: tiered init, hook fast path, Kong dispatch |
| `cli.go` | Kong `CLI` struct: every subcommand and its `Run()` method |
| `monitor.go` | Background monitor loop (500ms tick), state resolution, transition handlers |
| `internal/tmux/` | All tmux interactions (sessions, windows, menus, popups, keybindings) |
| `internal/tui/` | Bubble Tea models (chrome bar, popups, browsers) |
| `internal/hooks/` | Claude Code hook handlers: windowstate, notifications, Bash validation |
| `internal/windowstate/` | Per-window hook state (`.agentctl/windows/<id>.json`) |
| `internal/state/` | `state.json` snapshot written by the monitor |
| `internal/dispatch/`, `internal/improve/` | Transition handlers and dispatch/self-improvement orchestration |
| `internal/agent/` | Agent discovery from `~/.claude/agents/*.md` and project `.claude/agents/` |
| `internal/sessions/` | Claude session log parsing, summaries, cross-context |
| `internal/config/` | Layered JSON settings (see `docs/configuration.md`) |
| `internal/statusline/` | `agentctl statusline`, Claude Code's statusLine data source |
| `internal/llm/` | Single-turn text completion (ollama, claude providers) |

### Data flow

1. `agentctl` starts a tmux session and runs the monitor in the background.
2. Claude Code hooks write each window's state (`working`, `blocked`, `idle`) to its windowstate file.
3. The monitor loops every 500ms, reads that state, and resolves it to an `AgentStatus`. JSONL logs are read for metrics only.
4. State transitions call handlers directly (`dispatch.HandleComplete`, `improve.HandleTransition`, metrics tracker). There is no plugin registry.
5. The monitor renames tmux windows to reflect state and writes `state.json`; the chrome bar re-renders from it.

### Per-agent settings via `--settings`

agentctl generates a per-agent Claude Code settings file at `.agentctl/settings/<name>.json` and passes it via `claude --settings <path>` at launch. Claude Code merges `--settings` **additively** on top of the global `~/.claude/settings.json`, so per-agent rules layer cleanly.

**What gets injected:**
- **Hooks**: all agentctl hooks (session start/end, pre/post tool, notifications, etc.)
- **`permissions.deny`**: from `blocked_commands` in the agent's config (substring becomes a `Bash(*cmd*)` glob)
- **`permissions.allow`**: from `allowed_permissions` in the agent's config (raw permission globs passed through as-is)

**Configuring allowed permissions** in `~/.config/agentctl/config.json`:
```json
{
  "agents": {
    "researcher": {
      "allowed_permissions": ["WebFetch(*)", "WebSearch(*)"]
    }
  }
}
```

**Common permission globs:**
- `WebFetch(*)`: allow fetching any URL without prompting
- `WebSearch(*)`: allow web searches without prompting
- `Bash(git *)`: allow all git commands
- `Read(~/some/path/*)`: allow reading specific directories

The `--settings` flag is a Claude Code feature; the per-agent file generation and config schema are agentctl's.
