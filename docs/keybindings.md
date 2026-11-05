# Keybindings

All keybindings use the tmux prefix (`ctrl+b`), then the key. The bottom row of the status bar shows available keybinds as a reminder.

## Agent management

| Key | Action |
|-----|--------|
| `a` | **Launch agent** — popup menu listing available agents |
| `A` | **Quick-launch** — launch the default agent (`default_agent` in config) immediately, skipping the menu |
| `m` | **Agents window** — open the agent browser window |
| `c` | **Create** — popup menu for creating a new agent definition |
| `x` | **Kill agent** — confirm-and-kill menu for the focused agent's window |
| `K` | **Kill all** — confirm-and-kill every running agent |
| `q` | **Quit** — confirm, then shut down agentctl |
| `R` | **Restart** — confirm, then relaunch the running agentctl binary (no rebuild) and kill this session |

## Navigation

agentctl binds only the keys below; tmux's defaults (`n` next window, `0`–`9` jump to window by number, etc.) are untouched. Note that `p` is **not** previous window — agentctl rebinds it to the theme picker.

| Key | Action |
|-----|--------|
| `[` / `]` | Previous / next window |
| `<` / `>` | Resize the pane left / right by 5 columns (repeatable) |
| `Alt+Left` / `Alt+Right` | Previous / next window, no prefix (Cmd+`[`/`]` in terminals that send these sequences, e.g. Ghostty) |
| `Alt+Up` / `Alt+Down` | Select the pane to the left / right, no prefix |

## Session & logs

| Key | Action |
|-----|--------|
| `o` | **Session logs** — formatted conversation history for the focused agent, shown via `less` |
| `S` | **Sessions window** — browse and switch between conversation sessions |

## Tickets & dispatch

| Key | Action |
|-----|--------|
| `t` | **Ticket browser** — open the tickets window to browse GitHub issues |
| `T` | **Ticket launcher** — pick an issue and dispatch an agent on it (see [dispatch.md](dispatch.md#dispatch-modes)) |
| `Q` | **Ticket menu** — create a new GitHub issue |

## Settings & permissions

| Key | Action |
|-----|--------|
| `s` | **Agent settings** — per-agent config for the focused window |
| `P` | **Permissions** — manage project-level permission allow lists |
| `p` | **Theme picker** — choose a chrome-bar theme |

## Self-improvement & context

| Key | Action |
|-----|--------|
| `i` | **Self-improve** — agent analyzes its session logs and rewrites its own instructions |
| `C` | **Context push** — pick a context-store entry to push to the focused agent |

## HUD

| Key | Action |
|-----|--------|
| `h` | **Toggle HUD** — show or hide the HUD pane beside the focused window |
| `H` | **HUD popup** — open the HUD as a popup for the focused window |

## Herd hub

| Key | Action |
|-----|--------|
| `g` | **Summon hub** — open the herd hub (`agentctl hub`), a cross-session dashboard of every agentctl session on the machine. Creates the `agentctl-hub` tmux session if it doesn't exist yet. |
| `b` | **Jump to next blocked** — switch straight to the first blocked agent across every session (`agentctl jump-next-blocked`), from anywhere — not just the hub. |

These two keys are bound server-globally (tmux `bind-key` has no session
scope), so they work identically from inside the hub itself and from every
per-project session.

## Notes

The keybinding layout is inspired by [BitchX](https://en.wikipedia.org/wiki/BitchX)'s layered key maps — direct keys for the most common actions, a prefix mode for window management, and muscle-memory-friendly mnemonics (a=agent, t=ticket, s=settings, etc.).

The bottom chrome bar lights up keybind hints when you press the tmux prefix, so you don't need to memorize the full table.
