# Documentation

## Guides and reference

| Doc | Contents |
|---|---|
| [commands.md](commands.md) | Every user-facing subcommand, grouped by job, with links to the details |
| [configuration.md](configuration.md) | Every config key with its default and scope, layering, accounts, backends, harnesses, cron, serve, titles, pricing |
| [dispatch.md](dispatch.md) | Ticket-to-PR orchestration: modes, worktrees, PR review, orchestrator agents, cleanup and sweep |
| [permissions.md](permissions.md) | Per-agent command safety, what a project config may not set, built-in guards, audit trail |
| [keybindings.md](keybindings.md) | Prefix keybindings, ticket browser keys, and the fleet hub's keys, rows and sections |
| [architecture.md](architecture.md) | System overview, data flow and source layout |
| [monitor-architecture.md](monitor-architecture.md) | The optional monitor, hook-driven state, window names, reconcile and housekeeping, metrics, restore, TUI internals |
| [claude-code-hooks.md](claude-code-hooks.md) | Claude Code hook events and how agentctl uses each one |
| [prompt-architecture.md](prompt-architecture.md) | How dispatch system prompts and user messages are structured |
| [development.md](development.md) | Contributor conventions, phone app browser tests, the demo harness and screenshots |

## Design records

Written before or while each feature was built; the guides above describe current behavior.

| Doc | Contents |
|---|---|
| [design/tmux-as-agent-runtime.md](design/tmux-as-agent-runtime.md) | Why tmux is the runtime |
| [design/fleet-view-design.md](design/fleet-view-design.md) | The cross-session fleet hub (original design) |
| [design/mobile-web-app.md](design/mobile-web-app.md) | `agentctl serve` and the phone web app: API, push, live terminal, remote approvals |
| [design/cron.md](design/cron.md) | Scheduled agent runs (`agentctl cron`) |
| [design/accounts.md](design/accounts.md) | Multiple Claude accounts (one `CLAUDE_CONFIG_DIR` per login) |
| [design/multi-harness.md](design/multi-harness.md) | Research: running harnesses other than Claude Code (pi shipped; Codex CLI, opencode) |
| [design/agent-messaging-design.md](design/agent-messaging-design.md) | Agent-to-agent mailboxes (superseded by native cross-session messaging) |

## Other

- [brand/brand-guide.html](brand/brand-guide.html): logo and color guide
- [../demo/scenes/README.md](../demo/scenes/README.md): demo scene schema
- [../CONTRIBUTING.md](../CONTRIBUTING.md), [../SECURITY.md](../SECURITY.md), [../CHANGELOG.md](../CHANGELOG.md)
