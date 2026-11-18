# Keybindings

All keybindings use the tmux prefix (`ctrl+b`), then the key. The bottom row of the status bar shows available keybinds as a reminder.

## Agent management

| Key | Action |
|-----|--------|
| `a` | **Launch agent** — filter-first popup: type to filter, `enter` launch, `tab` options, `ctrl+r` resume, `ctrl+g` go to running, `esc` clear/close |
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

## Phone web app

| Key | Action |
|-----|--------|
| `W` | **Toggle `agentctl serve`** — stop the phone web app server if it runs, else start it (`agentctl serve toggle`); the result shows as a tmux message, e.g. "serve started at https://…". Stop leaves the LaunchAgent installed, so serve comes back at the next login. Not installed: the message says to run `agentctl serve install`. See [the mobile web app design](design/mobile-web-app.md#start-stop-and-the-toggle-internalservectl). |

## HUD

| Key | Action |
|-----|--------|
| `h` | **Toggle HUD** — show or hide the HUD pane beside the focused window |
| `H` | **HUD popup** — open the HUD as a popup for the focused window |

## Fleet hub

| Key | Action |
|-----|--------|
| `g` | **Summon hub** — open the fleet hub (`agentctl hub`), a cross-session dashboard of every agentctl session on the machine. Creates the `agentctl-hub` tmux session if it doesn't exist yet. |
| `b` | **Jump to next blocked** — switch straight to the first blocked agent across every session (`agentctl jump-next-blocked`), from anywhere. Inside the hub it stays in the hub: the blocked row is selected and focus moves to the live pane. |

These two keys are bound server-globally (tmux `bind-key` has no session
scope), so they work identically from inside the hub itself and from every
per-project session.

### Prefix keys inside the hub

Bindings are server-global, so a key pressed in the hub would resolve to the
hub window itself. Instead, every agentctl binding passes the window it was
pressed in (`AGENTCTL_KEY_WINDOW`), and `agentctl` (see `internal/hubtarget`)
redirects hub keypresses to the agent shown in the live pane:

- **Window-scoped** (`x` kill, `o` logs, `s` settings, `i` improve, `h`/`H` HUD, `C` context push) act on the viewed agent's window.
- **Project-scoped** (`a` menu, `A` launch, `c` create, `T`/`Q` tickets, `P` permissions, `m`/`t`/`S` windows) run as if pressed in the viewed agent's project session; new windows land there.
- `q` closes the hub (back to the previous session). `R` (restart) and `K` (kill all) are refused in the hub.
- With no agent viewed yet, these keys show "select an agent in the hub first".
- Popups may open over the hub window; only their target changes.
- `prefix+Left` / `prefix+Right` (tmux defaults) move focus between the list and the live pane.

The hub window has no chrome bar or HUD of its own. By default the agent's chrome bar is hidden in the live pane too, since the hub list already shows every agent: the hub zooms the viewed window's main pane while it shows it and unzooms it when it moves on, closes, or you switch to that project session (a zoom you made yourself is never undone). Attaching to a project session directly always shows its chrome bar.

### Hub list keys

| Key | Action |
|-----|--------|
| `j`/`k` | Move the selection; the live pane follows. |
| `enter`/`tab` | Focus the live pane. |
| `J` | Jump to the selected agent's home session. |
| `n` / `x` | Launch a new agent / close the selected one. |
| `N` | **New session** — open an agent in another directory as its own agentctl project. See below. |
| `b` | Select the next blocked agent. |
| `z` | **Fold** the project group under the cursor (on its header or any of its rows; the cursor moves to the header). A folded group shows only its header line, with its counts and the ⏸ flag. Clicking a header's project name toggles it too. |
| `shift+↑` / `shift+↓` | **Reorder** the project group under the cursor one slot up or down (the selection goes with it). You can also drag a group header: press it, move the pointer off the header row, and release where it should go; a rule marks the drop spot, and `esc` or releasing outside the list cancels. A plain click is still a click. |
| `Z` | **Focus mode** — fold every project with no blocked (⏸) and no working (●) agent; press again to unfold everything. |
| `f` | **Filter** — cycle `all` → `active` (working + blocked) → `needs you` (blocked + done-unseen). The title shows the active filter. Parents of a matching agent stay visible, dimmed, so the tree still reads; a project with nothing matching shows its header only. |
| `p` | Open the selected agent's PR. |
| `c` | **Toggle chrome** — show or hide the agent's chrome bar in the live pane for this hub (starts from the `hub_chrome` config value, default hidden). With chrome shown, clicking a tab in the live pane switches the live viewer and the hub list follows. |
| `r` | Refresh. |
| `S` | **Toggle `agentctl serve`**, like prefix+`W`; the outcome flashes in the footer. |
| `q`/`esc` | Leave the hub. |

The title row shows `agentctl serve`'s state after "agentctl hub": `📱 serve` (green) when it runs, `📱 off` (dim) when it does not, and `📱 stuck` (yellow) when its process is alive but its control socket refuses connections (phone approvals are not offered). It is refreshed on the hub's poll tick and does not need the monitor. Clicking it toggles serve, like `S`.

`N`, or the `+ new session` button on the title row (shortened to `+ session` or `+` when the list is narrow), opens a directory prompt in the footer, prefilled with the directory `agentctl hub` was last run from (recorded in `hub-origin` in the agentctl state dir on every `agentctl hub`; `$HOME` when there is none). `~`, `~/...` and paths relative to the prefill are accepted; `enter` confirms and `esc` cancels. When a project in the list already has that directory (symlinks resolved), the hub launches an agent there, like its `+ new`. Otherwise it starts the directory's own session (`agentctl-<dir>`, detached, as if `agentctl` were run there) and launches an agent in it with `agentctl launch --background --permission-mode auto`; a directory without `.claude/agents/` is initialized first, without starter templates and without the hooks or default-agent prompts of `agentctl init`. The hub refuses `$HOME` (it would create the global `~/.claude/agents`), `/`, files and missing directories; the error shows in the footer and the prompt stays open. A git repository is not required. Two directories with the same name map to the same session name; when that name is already taken by another directory's session, the hub reports it rather than launching there.

Project order is saved globally in `group-order.json` in the same state dir (keyed by project directory); projects not in it follow alphabetically, and `agentctl status` and `jump-next-blocked` use the same order; the phone list uses it among projects of equal urgency. Fold state and the filter are kept per hub across reloads, hot reloads and restarts, in `hub-view.json` in the agentctl state dir (`$XDG_STATE_HOME/agentctl`, default `~/.local/state/agentctl`). Jumps that target a hidden agent (`b`, a just-launched agent, a tab click in the live pane) unfold its group first, and clear the filter if it would still hide it. The one-line footer has no room for `N`, `z`, `Z`, `f` and `S`; they are listed under `?`.

### Hub list tree

Within each project group the hub list is a parent/child tree. An agent dispatched by another agent renders directly under its dispatcher, indented with `├─` / `└─` / `│` connectors (nesting is recursive). Bare windows with no dispatcher are roots, in their usual order. Dispatched agents whose dispatcher is unknown, untrusted (no recorded session), or not in the same group are listed under a trailing `dispatched (N)` label. Windows launched by `agentctl cron` have no dispatcher: they show a muted `⏲ <job>` marker before their recap and, unless they have a parent in the group, sit under a `scheduled (N)` label after that. Every row stays selectable, and cursor movement, close-neighbour selection and jump-to-blocked all follow the displayed order.

## Notes

The keybinding layout is inspired by [BitchX](https://en.wikipedia.org/wiki/BitchX)'s layered key maps — direct keys for the most common actions, a prefix mode for window management, and muscle-memory-friendly mnemonics (a=agent, t=ticket, s=settings, etc.).

The bottom chrome bar lights up keybind hints when you press the tmux prefix, so you don't need to memorize the full table.
