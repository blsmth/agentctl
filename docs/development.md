## agentctl Development Guide

- Install for development: `just install` (builds to `~/.local/bin/agentctl`; override with `BINDIR`)
- Build only: `just build` (local `./agentctl`)
- Full restart: `just restart` (install + kill tmux session + relaunch)
- Debug logs: `just logs` (tails `.agentctl/logs/agentctl.log` for the current project)
- Go 1.26+, tmux 3.4+, Bubble Tea for TUI popups

### Working Directory

- **You are already in the repository (or your worktree). Never `cd` anywhere.**
- **Never chain `cd` into Bash commands.** Write `go build ./...`, not `cd <repo> && go build ./...`.
- Use paths relative to the repo root or absolute paths in all tool calls.

### Code Style

- Subcommand routing uses [Kong](https://github.com/alecthomas/kong): add a field to the `CLI` struct in `cli.go` with a `Run()` method (`hidden:""` for internal-only commands)
- Wrap timing-sensitive calls with `logging.Timing("label")` / `done()`
- Log significant actions with `logging.Log(...)` for debug traceability
- Return errors from library code, wrapped with context (`fmt.Errorf("doing X: %w", err)`); command `Run()` methods return the error to Kong
- Tmux interactions go in `internal/tmux/`, never in root-level files
- There is no plugin registry. Reactive logic is a plain function called from `detectAndFireTransitions()` in `monitor.go`

### Bubble Tea TUI Pattern

All popups follow the same pattern in `internal/tui/`:
1. Define a `FooResult` struct for output and a `FooModel` with Bubble Tea's `Init/Update/View`
2. `NewFooModel(...)` constructor, `Result()` accessor
3. The calling command runs `tea.NewProgram(model)`, then reads the result after `p.Run()`
4. Table-based viewers share `tableStyles` (`internal/tui/tablestyles.go`); reuse it rather than redefining column, rule, dim, detail, subtitle, and hint-bar styles. Popup-only styles such as `selectedStyle` are defined locally in the tui package (e.g. `improve.go`)
5. Popups are launched via `tmux display-popup -E` with the running agentctl binary (`os.Executable()`)

### Dispatch command

`agentctl dispatch` is the non-interactive entry point for launching an agent on a GitHub issue.

```bash
agentctl dispatch --issue 42 --agent go-engineer       # explicit agent
agentctl dispatch --issue 42                            # auto-detect from labels
agentctl dispatch --issue 42 --dir ~/code/other/repo    # explicit working directory
agentctl dispatch --issue 42 --repo owner/name          # explicit GitHub repo
```

The implementation is `dispatchIssue()` in `internal/cmd/dispatch.go`, reusable from both CLI and TUI flows.

**When to use dispatch vs ticket-launch:**
- `dispatch` is non-interactive, for scripts and automation. You already know the issue number.
- `ticket-launch` is the interactive TUI, for humans browsing and picking issues.

### State is hook-driven

Claude Code hooks write each window's state to `.agentctl/windows/<windowID>.json` (`internal/windowstate`). The monitor reads it every tick. JSONL logs are parsed only for metrics. Config reference: `docs/configuration.md`.

### tmux patterns

Tmux interactions all live in `internal/tmux/`. Read the existing popup, menu, and keybinding code there before adding new ones.
