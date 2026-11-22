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

### Inside the ticket browser

The browser (`agentctl tickets --tui`) works with the keyboard alone; the mouse is optional. `?` lists every key and gesture.

| Key | Action |
|-----|--------|
| `enter` / `esc` | Open the detail / go back (`esc` in the list clears the search, then quits) |
| `e` | Edit in place: from the list it opens the detail with the title focused, in the detail it focuses the body. `ctrl+s` saves (`enter` in the title), `tab` switches field, `ctrl+e` hands the body to `$EDITOR`, `esc` cancels and asks first if you changed something. A save is blocked when the same field changed on GitHub since you opened it |
| `E` | Edit in the browser (`gh issue edit --web`) |
| `l` / `a` | Pick the issue's labels / assignees (type to filter, `space` toggles, `enter` applies at once) |
| `f` | Filter the list by a label |
| `s` / `S` / `/` | Cycle the sort / cycle open, closed and all issues / search titles |
| `c` / `d` | New issue / close the issue (confirms) |
| `o` / `p` / `y` | Open the issue / its linked PR in the browser / copy its URL to the tmux buffer |
| `t` / `T` | Launch a triage agent / pick an agent to dispatch |
| `P` | Autopilot on (asks first: it is a global setting) or off |

With the mouse: click a row to select it and double-click to open it. Label chips filter, `#`/`AGE` headers sort, `#N↗` opens the linked PR, and the chips, sidebar buttons, autopilot pill and every hint at the bottom do what their key does. `✎`, or a double-click on the title or body, starts an edit. A click outside a dialog cancels it, except an edit with unsaved changes. The wheel scrolls the pane under the pointer. To select text natively, hold Shift (Option in iTerm2).

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
| `X` | **Context viewer** — open the context window (`agentctl context --tui`): browse entries with a scrollable preview, `/` filter, `d` delete (y/n), `r` reload, `q`/`esc` back to the previous window |

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
- **Project-scoped** (`a` menu, `A` launch, `c` create, `T`/`Q` tickets, `P` permissions, `m`/`t`/`S`/`X` windows) run as if pressed in the viewed agent's project session; new windows land there.
- `q` closes the hub (back to the previous session). `R` (restart) and `K` (kill all) are refused in the hub.
- With no agent viewed yet, these keys show "select an agent in the hub first".
- Popups may open over the hub window; only their target changes.
- `prefix+Left` / `prefix+Right` (tmux defaults) move focus between the list and the live pane.

The hub window has no chrome bar or per-window HUD of its own (prefix+`h` acts on the viewed agent; the hub's `v` activity pane is a separate, hub-wide HUD). By default the agent's chrome bar is hidden in the live pane too, since the hub list already shows every agent: the hub zooms the viewed window's main pane while it shows it and unzooms it when it moves on, closes, or you switch to that project session (a zoom you made yourself is never undone). Attaching to a project session directly always shows its chrome bar.

### Hub list keys

| Key | Action |
|-----|--------|
| `j`/`k` | Move the selection; the live pane follows. |
| `enter`/`tab` | Focus the live pane. |
| `J` | Jump to the selected agent's home session. |
| `n` / `x` | Launch a new agent / close the selected one. A group header's `+ new` button appears on the selected or hovered header only. `n` arms `s` shell, `a` agent, or `t` schedule (a new cron job in that project, see below). |
| `N` | **New session** — open an agent in another directory as its own agentctl project. See below. |
| `b` | Select the next blocked agent. |
| `w` | **Sweep** the project group under the cursor. See below. |
| `z` | On a row with sub-agents (marked `▾`), **collapse** its whole subtree (grandchildren too); `z` again expands it. A collapsed parent is marked `▸` and shows a summary of what it hides after its title: `+N` rows, then the ⏸ ● ✓ counts, so a blocked child still shows as blocked. Clicking the `▾`/`▸` marker toggles it too. Anywhere else (a header, a row without sub-agents) `z` **folds** the project group (the cursor moves to the header): a folded group shows only its header line, with its counts and the ⏸ flag. Clicking a header's project name toggles the fold. |
| `shift+↑` / `shift+↓` | **Reorder** the project group under the cursor one slot up or down (the selection goes with it). You can also drag a group header: press it, move the pointer off the header row, and release where it should go; a rule marks the drop spot, and `esc` or releasing outside the list cancels. A plain click is still a click. |
| `Z` | **Focus mode** — fold every project with no blocked (⏸) and no working (●) agent; press again to unfold everything. |
| `f` | **Filter** — cycle `all` → `active` (working + blocked) → `needs you` (blocked + done-unseen). The title shows the active filter. Parents of a matching agent stay visible, dimmed, so the tree still reads; a project with nothing matching shows its header only. |
| `p` | Open the selected agent's PR. |
| `R` | **Rename** the selected agent's title. See below. |
| `$` | **Shells** — show or hide plain `shell` windows (hidden by default; opening a shell from the hub shows them so it can be selected). |
| `C` | **Closed agents** — show or hide a Closed section after the live projects: agent windows closed in the last 7 days (from the closed ledger, read only while shown and re-read when it changes), grouped by project, newest first, dimmed with `✗` and "closed 2h ago". A closed row never drives the live pane. `⏎` (or the row menu's **resume**) relaunches its agent in the project's session, resuming (forked) the recorded session in the directory it ran in; when no session ID was recorded or its transcript is gone, the hub says so instead. `p` opens its PR. |
| `T` | **Schedules** — show or hide a Schedules section at the bottom of the list: the `agentctl cron` jobs, read in process through the same editing core as `agentctl cron` and the phone (only while shown, re-read every 5 seconds and after each change; serve need not run). Rows sort and mark like the phone: `!` failed last run (red), `●` running, `⏲` enabled, `⏸` paused (dimmed, last); each shows the name, the schedule in words, the last result, project and agent, and the next fire right-aligned (relative under a day, else `Mon 07:31`; `paused`). A narrow list drops the agent, then the project, then the last result, never the name or next fire. Warnings show when the ticker is not installed (`run agentctl cron install`) or `cron.enabled` is false; with no jobs the section offers `+ new schedule`. While the section is hidden, a project's `scheduled (N)` bucket label says `T schedules`. See below. |
| `v` | **Activity pane** — open or close a pane at the right of the hub window showing the selected agent's tool activity as a terminal feed (`agentctl hud --follow-hub`): each Bash command as `$ cmd` with its last output lines, other tools as one dim line. It follows the hub selection, slides in, takes about 35% of the window (at least 40 columns) and leaves the live pane at least 60; on a window too narrow for all three it takes the live pane's place until it closes. A width you drag it to is kept. The title row's `[activity]` button does the same (it is dropped when the title has no room). Inside it the HUD keys apply (`t` switches the render mode, `j`/`k`/`enter` move and expand, the wheel scrolls); `q` in it closes it. |
| `c` | **Toggle chrome** — show or hide the agent's chrome bar in the live pane for this hub (starts from the `hub_chrome` config value, default hidden). With chrome shown, clicking a tab in the live pane switches the live viewer and the hub list follows. |
| `r` | Refresh. |
| `S` | **Toggle `agentctl serve`**, like prefix+`W`; the outcome flashes in the footer. |
| `q`/`esc` | Leave the hub. |

The title row shows `agentctl serve`'s state after "agentctl hub": `📱 serve` (green) when it runs, `📱 off` (dim) when it does not, and `📱 stuck` (yellow) when its process is alive but its control socket refuses connections (phone approvals are not offered). It is refreshed on the hub's poll tick and does not need the monitor. Clicking it toggles serve, like `S`.

Selecting a schedule row closes the live viewer pane and shows the job's card in its place, so the list stays visible: status, the schedule in words and as cron, agent, directory and worktree mode, non-default settings, the next 3 fires, any running window, the prompt and the last 5 runs. Moving back to an agent row brings the viewer back. On a schedule row (and only there) the keys are: `⏎` full card (20 runs, a `prompt_key`'s current text), `e` edit, `space` pause or resume (no confirmation; space again undoes it), `g` run now (asks `y/n`; plain `cron run` semantics, never `--force`: overlap, `max_concurrent` and the usage gate apply, and a skip shows its reason), `d` duplicate (`<name>-copy`, saved paused), `x` delete (asks `y/n`; run history is kept and a running window is left alone), `n` new. The row's right-click menu has the same actions. Every other row keeps its usual keys; the footer shows the schedule keys only on a schedule row.

The form (new, edit, duplicate) takes the card's place, or the list's on a terminal too narrow for both. Fields follow the phone: Name (fixed after create), Project (`←`/`→` through the hub's projects), Agent, Prompt, Schedule, and a folded Advanced section (worktree, model, effort, backend, account, timeout, overlap, catch-up, usage gate) whose summary lists the non-defaults. The schedule picker is `Hourly | Daily | Weekdays | Days… | Custom` (`←`/`→`); Hourly takes a minute, the others a `HH:MM` time, Days also seven day chips (`←`/`→` move, `space` toggles), Custom a raw cron expression. A stored schedule opens in the picker only when it maps onto it exactly, else in Custom, and a schedule you do not touch is saved as it was. A live line under the schedule shows it in words, the time zone and the next 3 fires; problems show under their field. `tab`/`shift+tab` (and `↑`/`↓` outside the prompt) move between fields, `ctrl+e` edits the prompt in `$EDITOR` (a `prompt_key` job then gets the key's text as an inline prompt; leaving the prompt empty keeps the key), `ctrl+s` opens the review, `esc` cancels (asking first when something changed). The review states what runs, where, when, unattended and with which permission mode (`Runs go-engineer in ~/code/x (a worktree per fire), weekdays 07:30 EST, unattended, permission mode auto, until paused.`, the wording of the cron approval popup and the phone), the next 3 fires and the prompt (on an edit, only the changed fields); `y` saves, `n` goes back. Fields the form does not show (harness, mode, enabled) are kept on an edit. Saves and every other change run in the background, so a save waiting on a running tick never freezes the hub. The hub does not hot-reload while the form is open.

`R`, `[rename]` in a row's right-click menu, or a right click on a row's title text opens an inline editor in place of the title, prefilled with the title as shown (empty for a row with no title yet, which shows its recap sentence) and the cursor at the end. A right click anywhere else on a row, on its detail lines, or on a header opens the context menu as before. While the editor is open every key goes to it. `enter` saves, `esc` or a click anywhere else cancels, and saving unchanged text does nothing. The text is trimmed, stripped of control characters and capped at 60 characters, then written to the window's windowstate `short_title` in its own project's runtime dir, marked `manual` (and `final`, kind kept). The row shows it at once, and the phone shows it too, since it reads the same record. A manual title is never replaced by the automatic titles, and `/clear` keeps it (see [titles](configuration.md#titles)). Saving an empty title clears it so automatic titles resume. Shell rows cannot be renamed, and a window with no agentctl record reports an error instead of getting one. A poll that reorders the list keeps the editor on its window, and the editor closes if the window goes away. The hub does not hot-reload while the editor is open.

`N` opens a directory prompt in the footer, prefilled with the directory `agentctl hub` was last run from (recorded in `hub-origin` in the agentctl state dir on every `agentctl hub`; `$HOME` when there is none). `~`, `~/...` and paths relative to the prefill are accepted; `enter` confirms and `esc` cancels. When a project in the list already has that directory (symlinks resolved), the hub launches an agent there, like its `+ new`. Otherwise it starts the directory's own session (`agentctl-<dir>`, detached, as if `agentctl` were run there) and launches an agent in it with `agentctl launch --background --permission-mode auto`; a directory without `.claude/agents/` is initialized first, without starter templates and without the hooks or default-agent prompts of `agentctl init`. The hub refuses `$HOME` (it would create the global `~/.claude/agents`), `/`, files and missing directories; the error shows in the footer and the prompt stays open. A git repository is not required. Two directories with the same name map to the same session name; when that name is already taken by another directory's session, the hub reports it rather than launching there.

Project order is saved globally in `group-order.json` in the same state dir (keyed by project directory); projects not in it follow alphabetically, and `agentctl status` and `jump-next-blocked` use the same order; the phone list uses the same order (and reorders it by long-press and drag). Fold and collapse state, the filter, the sort, the shell, closed and schedules toggles and the activity pane's dragged width are kept per hub across reloads, hot reloads and restarts, in `hub-view.json` in the agentctl state dir (`$XDG_STATE_HOME/agentctl`, default `~/.local/state/agentctl`). Jumps that target a hidden agent (`b`, a just-launched agent, a tab click in the live pane) unfold its group and expand its collapsed parents first, and clear the filter if it would still hide it. The one-line footer has no room for `N`, `R`, `v`, `z`, `Z`, `f`, `$`, `C`, `T` and `S`; they are listed under `?`.

`w`, the `sweep N` button on a group header (shown only when N > 0 of the group's agents are done, left of `+ new`; dropped on headers too narrow to keep four columns of project name, and hidden while the `shell | agent` selector is armed; N counts done rows, so sweep's worktree and PR checks may close fewer), or `[sweep]` in a header's right-click menu previews [`agentctl sweep`](dispatch.md) for that project's tmux session. The hub runs `agentctl sweep --session <name> --dry-run --json` as a subprocess, pinned to the project's runtime dir, and shows the plan in the footer: `sweep <project>: close 3 (a, b, c), 2 need attention, 1 in progress  y/n`. `y` applies it (`agentctl sweep --session <name> --json`) and then reports `swept N` from what actually closed, since sweep re-checks every window at apply time; any other key, a click elsewhere, or moving the cursor off the line cancels. With nothing to close the footer says `nothing to sweep` and nothing is armed. While a sweep runs the footer shows `sweeping <project>…` and further sweeps are ignored. A failed sweep (session gone, non-zero exit) shows its error. Sweep's own rules decide what closes (`shell`, `improve~*`, untracked and bare windows never do; the hub does not pass `--include-bare`), and the hub's own window is the sweep's self, so it is never closed. Groups that are not agentctl project sessions have no button.

### Hub list tree

Within each project group the hub list is a parent/child tree. An agent dispatched by another agent renders directly under its dispatcher, indented with `├─` / `└─` / `│` connectors (nesting is recursive). Bare windows with no dispatcher are roots, in their usual order. Dispatched agents whose dispatcher is unknown, untrusted (no recorded session), or not in the same group are listed under a trailing `dispatched (N)` label. Windows launched by `agentctl cron` have no dispatcher: they show a muted `⏲ <job>` marker before their recap and, unless they have a parent in the group, sit under a `scheduled (N)` label after that. Every row stays selectable, and cursor movement, close-neighbour selection and jump-to-blocked all follow the displayed order.

### Hub list rows

A row is the state glyph, the agent name, the title, then right-aligned columns: the PR, and the time in the current state (else since the last activity or start; `–` when unknown). The model shows only when it differs from the model most agents in the hub run (a tie shows every model), and the context use only from `models.context_warn_pct` (warn colour, error colour from the critical threshold); when neither applies their width goes to the title. Narrow rows drop the age first, then the model, then the PR; a context warning is never dropped. The hover card and the live viewer's statusline always show both. A done (✓) row more than 24 hours old dims to idle weight but keeps its green ✓ and its place in the ✓ counts. A title's leading issue number (`#21 …`) is dim. The selected row's detail lines are its recap, its last activity (left out while the live pane shows that window), and a chip line only for what the row does not show: the issue, the agent name when truncated, a non-default backend or account.

## Notes

The keybinding layout is inspired by [BitchX](https://en.wikipedia.org/wiki/BitchX)'s layered key maps — direct keys for the most common actions, a prefix mode for window management, and muscle-memory-friendly mnemonics (a=agent, t=ticket, s=settings, etc.).

The bottom chrome bar lights up keybind hints when you press the tmux prefix, so you don't need to memorize the full table.
