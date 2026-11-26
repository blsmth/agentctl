# Keybindings

All keybindings use the tmux prefix (`ctrl+b`), then the key. Re-apply them to a running session with `agentctl apply-config` (no restart). With [`chrome_bar`](configuration.md#top-level-keys) on, the bar at the bottom of each window shows the main keys as a reminder; with it off (the default) the native tmux status line shows the window list.

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
| `p` | **Theme picker** — choose the color theme (`theme` in config), used by the hub, popups, browsers and the chrome bar |

## Self-improvement & context

| Key | Action |
|-----|--------|
| `i` | **Self-improve** — launches a session in which the agent reviews short summaries of its recent sessions and rewrites its own instructions |
| `C` | **Context push** — pick a context-store entry to push to the focused agent |
| `X` | **Context viewer** — open the context window (`agentctl context --tui`): browse entries with a scrollable preview, `/` filter, `d` delete (y/n), `r` reload, `q`/`esc` back to the previous window |

## Phone web app

| Key | Action |
|-----|--------|
| `W` | **Toggle phone access** — turn the phone off (no notifications, no phone approvals, the phone app paused) or back on (`agentctl serve toggle`); serve keeps running, so artifact links keep working. With serve down it starts it. The result shows as a tmux message, e.g. "phone access off: …" or "serve started at https://…". Not installed: the message says to run `agentctl serve install`. `agentctl serve stop --all` stops the server itself. See [the mobile web app design](design/mobile-web-app.md#start-stop-and-the-toggle-internalservectl). |

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

These two keys are bound server-globally (tmux `bind-key` has no session scope), so they work identically from inside the hub itself and from every per-project session.

### Prefix keys inside the hub

Bindings are server-global, so a key pressed in the hub would resolve to the hub window itself. Instead, every agentctl binding passes the window it was pressed in (`AGENTCTL_KEY_WINDOW`), and `agentctl` (see `internal/hubtarget`) redirects hub keypresses to the agent shown in the live pane:

- **Window-scoped** (`x` kill, `o` logs, `s` settings, `i` improve, `h`/`H` HUD, `C` context push) act on the viewed agent's window.
- **Project-scoped** (`a` menu, `A` launch, `c` create, `T`/`Q` tickets, `P` permissions, `m`/`t`/`S`/`X` windows) run as if pressed in the viewed agent's project session; new windows land there.
- `q` closes the hub (back to the previous session). `R` (restart) and `K` (kill all) are refused in the hub.
- With no agent viewed yet, these keys show "select an agent in the hub first".
- Popups may open over the hub window; only their target changes.
- `prefix+Left` / `prefix+Right` (tmux defaults) move focus between the list and the live pane (the status pane above it hands focus on to the live pane).

The hub window has no chrome bar or per-window HUD of its own (prefix+`h` acts on the viewed agent; the hub's `v` activity pane is a separate, hub-wide HUD). With `chrome_bar` on, the agent's chrome bar is hidden in the live pane by default, since the hub list already shows every agent: the hub zooms the viewed window's main pane while it shows it and unzooms it when it moves on, closes, or you switch to that project session (a zoom you made yourself is never undone). Attaching to a project session directly always shows its chrome bar (when `chrome_bar` is on).

### Hub list keys

| Key | Action |
|-----|--------|
| `j`/`k` | Move the selection; the live pane follows. |
| `enter`/`tab` | Focus the live pane. |
| `J` | Jump to the selected agent's home session. |
| `n` / `x` | Launch a new agent / close the selected one. A group header's `+ new` button appears on the selected or hovered header only. `n` arms `s` shell, `a` agent, or `t`/`c` cron (the schedule form for a new cron job in that project, see below); the armed selector draws `shell | cron | agent` with agent highlighted as the default (Enter); a very narrow list drops the counts, then `cron`. `t` outside the selector does the same for the selected project, and the right-click menu has `[new cron]`. |
| `N` | **New session** (also the `+ open` button on the title row, after the 📱 indicator; dropped on narrow panes) — open an agent in another directory as its own agentctl project. See below. |
| `b` | Select the next blocked agent. |
| `w` | **Sweep** the project group under the cursor. See below. |
| `z` | On a row with sub-agents (marked `▾`), **collapse** its whole subtree (grandchildren too); `z` again expands it. A collapsed parent is marked `▸` and shows a summary of what it hides after its title: `+N` rows, then the ⏸ ● ✓ counts, so a blocked child still shows as blocked. Clicking the `▾`/`▸` marker toggles it too. On a `schedules (N)` block header (shown with `T`) `z`, `⏎` or a click collapses or expands that block the same way: collapsed, it shows `+N` jobs and the `●` running, `!` failed and `⏸` paused counts. Anywhere else (a header, a row without sub-agents) `z` **folds** the project group (the cursor moves to the header): a folded group shows only its header line, with its counts and the ⏸ flag. Clicking a header's project name toggles the fold. |
| `shift+↑` / `shift+↓` | **Reorder** the project group under the cursor one slot up or down (the selection goes with it). You can also drag a group header: press it, move the pointer off the header row, and release where it should go; a rule marks the drop spot, and `esc` or releasing outside the list cancels. A plain click is still a click. |
| `Z` | **Focus mode** — fold every project with no blocked (⏸) and no working (●) agent; press again to unfold everything. |
| `f` | **Filter** — cycle `all` → `active` (working + blocked) → `needs you` (blocked + needs attention `!` + done-unseen). The title shows the active filter. Parents of a matching agent stay visible, dimmed, so the tree still reads; a project with nothing matching shows its header only. |
| `p` | Open the selected agent's PR. |
| `o` | **Sort** — toggle the rows within each group between window order (default) and most recent activity first. |
| `A` | On a row with a dim `◆N` chip (the window has published N [artifacts](design/artifacts.md)): **open the newest** in the browser (`agentctl artifact open`; also in the row menu). No-op on other rows. |
| `R` | **Rename** the selected agent's title. See below. |
| `$` | **Shells** — show or hide plain `shell` windows (hidden by default; opening a shell from the hub shows them so it can be selected). |
| `C` | **Closed agents** — show or hide a Closed section after the live projects: agent windows closed in the last 7 days (from the closed ledger, read only while shown and re-read when it changes), grouped by project, newest first, dimmed with `✗` and "closed 2h ago". A closed row never drives the live pane. `⏎` (or the row menu's **resume**) relaunches its agent in the project's session, resuming (forked) the recorded session in the directory it ran in; when no session ID was recorded or its transcript is gone, the hub says so instead. `p` opens its PR. The phone app has the same list under Fleet → Recently closed. |
| `T` | **Sessions / Schedules / Artifacts tab** — cycle the tab of the project group the cursor is in (Sessions → Schedules → Artifacts → Sessions; a tab with nothing in it is skipped and not drawn). A project with at least one `agentctl cron` job or published artifact shows tabs next to its name (`▾ agentctl   Sessions 4 · Schedules 2 · Artifacts 3`). **Artifacts tab:** lists the project's artifacts (`agentctl artifact`), pinned (`★`) first, then the store's order (newest first unless reordered); each row shows the title, kind, `rN` when it has more than one revision, `✎N` comments and the time since the last update. `⏎` or a click opens the artifact in the laptop browser by its direct URL; with `serve` not running the footer says so instead of opening a dead URL. `p` pins or unpins it. `x` deletes it after an inline confirmation on the row (`delete? y/n`, or `delete pinned? y/n` for a pinned artifact): `y` deletes, `n`/`esc` cancels, any other key cancels and then runs as usual; moving the cursor, switching tab or a refresh that drops the artifact also cancels, and only one confirmation (this or a window close) is armed at a time. The cursor moves to the next row (the previous one when it was last, the group header when the tab empties). An artifact belongs to a group the way a job does (its project directory is the group's directory or below it); artifacts with no project, or whose project has no session in the hub, are not listed there (`agentctl artifact ls` shows them). The listing is cached and re-read only when the artifact store changes. **Schedules tab and the rest of this key:** A project with at least one `agentctl cron` job shows tabs next to its name; the selected tab decides what the group body lists, the agent rows (Sessions, the default) or that project's jobs (Schedules), never both. Click a tab label to switch too. The choice is per project, kept in `hub-view.json` (by resolved project directory) and across a hot reload; a project with no jobs shows no tabs (`T` there says so; `t` on its header still opens the new-schedule form for that directory). A job belongs to the first group whose project directory is its `dir` or contains it (a subdirectory, or a worktree under `.worktrees/`; symlinks are resolved; the longest matching project wins; a second group on the same directory hosts no tab). The Schedules tab ends in `+ new schedule` (`⏎`, `n` or a click opens the form in that project). Jobs whose project has no session go in a `Schedules · other projects (N)` block at the bottom, collapsed to one line by default (`z`, `⏎` or a click expands it); the warnings (ticker not installed, `cron.enabled` false) sit under it, or as one dim line at the bottom when there are no other-project jobs but some job exists. With no jobs at all nothing schedule-related is drawn. The jobs are read in process through the same editing core as `agentctl cron` and the phone (on hub start, every 5 seconds and after each change; serve need not run). Rows sort and mark like the phone: `!` failed last run (red), `●` running, `⏲` enabled, `⏸` paused (dimmed, last); each shows the name, the schedule in words, the last result, the project (bottom block only), and the next fire right-aligned (relative under a day, else `Mon 07:31`; `paused`). A narrow list drops the project, then the last result, never the name or next fire; a narrow header cuts its tabs before the project name. Folding a group hides both tabs' bodies; the `f` filter does not hide a group's jobs. `/` search matches sessions and jobs together and, while a query is active, shows a group's matching jobs whatever its tab. See below. |
| `v` | **Activity pane** — open or close a pane at the right of the hub window showing the selected agent's tool activity as a terminal feed (`agentctl hud --follow-hub`): each Bash command as `$ cmd` with its last output lines, other tools as one dim line. It follows the hub selection, slides in, takes about 35% of the window (at least 40 columns) and leaves the live pane at least 60; on a window too narrow for all three it takes the live pane's place until it closes. A width you drag it to is kept. The title row's `[activity]` button does the same (it is dropped when the title has no room). Inside it the HUD keys apply (`t` switches the render mode, `j`/`k`/`enter` move and expand, the wheel scrolls); `q` in it closes it. |
| `c` | **Toggle chrome** — show or hide the agent's chrome bar in the live pane for this hub (starts from the `hub_chrome` config value, default hidden). With chrome shown, clicking a tab in the live pane switches the live viewer and the hub list follows. |
| `r` | Refresh. |
| `S` | **Toggle phone access**, like prefix+`W`; the outcome flashes in the footer. |
| `?` | Show every key and gesture. |
| `q`/`esc` | Leave the hub. |

The title row shows phone access after "agentctl hub": `📱 on` (green) when serve runs with phone access on, `📱 off` (dim) when phone access is off (serve still serves artifact links), `📱 down` (dim, struck through) when no serve process runs, and `📱 stuck` (yellow) when its process is alive but its control socket refuses connections (phone approvals are not offered). It is refreshed on the hub's poll tick and does not need the monitor. Clicking it toggles phone access, like `S`.

Selecting a schedule row closes the live viewer pane and shows the job's card in its place, so the list stays visible: status, the schedule in words and as cron, agent, directory and worktree mode, non-default settings, the next 3 fires, any running window, the prompt and the last 5 runs. Moving back to an agent row brings the viewer back. On a schedule row (and only there) the keys are: `⏎` full card (20 runs, a legacy unpinned `prompt_key` and its current text, marked UNPINNED with the command that pins it; a pinned prompt shows the key it was copied from), `e` edit, `space` pause or resume (no confirmation; space again undoes it), `g` run now (asks `y/n`; plain `cron run` semantics, never `--force`: overlap, `max_concurrent` and the usage gate apply, and a skip shows its reason), `d` duplicate (`<name>-copy`, saved paused), `x` delete (asks `y/n`; run history is kept and a running window is left alone), `n` new. The row's right-click menu has the same actions. Every other row keeps its usual keys; the footer shows the schedule keys only on a schedule row.

The form (new, edit, duplicate) takes the card's place, or the list's on a terminal too narrow for both. Fields follow the phone: Name (fixed after create), Project (`←`/`→` through the hub's projects), Agent, Prompt, Schedule, and a folded Advanced section (worktree, backend, model, effort, account, timeout, overlap, catch-up, usage gate; changing the backend reloads the model list and puts a model the new backend does not offer back to the agent default) whose summary lists the non-defaults. The schedule picker is `Hourly | Daily | Weekdays | Days… | Custom` (`←`/`→`); Hourly takes a minute, the others a `HH:MM` time, Days also seven day chips (`←`/`→` move, `space` toggles), Custom a raw cron expression. A stored schedule opens in the picker only when it maps onto it exactly, else in Custom, and a schedule you do not touch is saved as it was. A live line under the schedule shows it in words, the time zone and the next 3 fires; problems show under their field. `tab`/`shift+tab` (and `↑`/`↓` outside the prompt) move between fields, `ctrl+e` opens the prompt in a full-pane editor in the form's place (title `Prompt — <job>`, line numbers, soft wrap, a status line with line:col, line and character counts and a `●` when changed; `alt`/`ctrl`+arrows move by word, `home`/`end`, `ctrl+home`/`ctrl+end`, `pgup`/`pgdn`, `ctrl+z` undo, `ctrl+y` or `ctrl+shift+z` redo, `tab` inserts spaces, pastes of any size; a click places the cursor and the wheel scrolls; `ctrl+s` returns the text to the form, `esc` closes it when nothing changed and asks `discard changes? y/n` otherwise; a legacy `prompt_key` job opens with the key text and becomes an inline prompt once saved with text, while leaving the prompt empty keeps the key (unpinned); editing a pinned prompt drops its source key; not offered for a template-source job), `ctrl+s` opens the review, `esc` cancels (asking first when something changed). The review states what runs, where, when, unattended and with which permission mode (`Runs go-engineer in ~/code/x (a worktree per fire), weekdays 07:30 EST, unattended, permission mode auto, until paused.`, the wording of the cron approval popup and the phone), the next 3 fires and the prompt (on an edit, only the changed fields); `y` saves, `n` goes back. Fields the form does not show (harness, mode, enabled) are kept on an edit. Saves and every other change run in the background, so a save waiting on a running tick never freezes the hub. The hub does not hot-reload while the form is open. Every control also takes the mouse: a click on a field's label or text box focuses it, on a cycler's value steps it forward (its `‹` steps back, and the wheel steps the focused one), on a segment or day chip chooses or toggles it, on Advanced or Worktree flips it, on `^e open editor` opens the prompt editor (whose `^s save`/`esc cancel`/`^z undo`/`^y redo` hints are buttons too), and on the review's `y`/`n` or a footer hint (`^s review`, `tab`, `esc`, `y`/`n`) presses that key. Clicks elsewhere in the hub are ignored while the form is open.

`R`, `[rename]` in a row's right-click menu, or a double click on a row's title text (two left clicks within 400ms; the first still selects the row) opens an inline editor in place of the title, prefilled with the title as shown (empty for a row with no title yet, which shows its recap sentence) and the cursor at the end. A right click on a row with sub-agents (on its detail lines too, but not its ✕) collapses or expands them; on any other row or on a header it opens the context menu. While the editor is open the cursor blinks. While the editor is open every key goes to it. `enter` saves, `esc` or a click anywhere else cancels, and saving unchanged text does nothing. The text is trimmed, stripped of control characters and capped at 60 characters, then written to the window's windowstate `short_title` in its own project's runtime dir, marked `manual` (and `final`, kind kept). The row shows it at once, and the phone shows it too, since it reads the same record. A manual title is never replaced by the automatic titles, and `/clear` keeps it (see [titles](configuration.md#titles)). Saving an empty title clears it so automatic titles resume. Shell rows cannot be renamed, and a window with no agentctl record reports an error instead of getting one. A poll that reorders the list keeps the editor on its window, and the editor closes if the window goes away. The hub does not hot-reload while the editor is open.

`N` (or the `+ open` button) opens a dropdown that slides in (about 130ms) over the list just below the title row, anchored under the `+ open` button (clamped to the pane width, at the left edge when the button is hidden, full width on very narrow panes). It holds the directory input, prefilled with the directory `agentctl hub` was last run from (recorded in `hub-origin` in the agentctl state dir on every `agentctl hub`; `$HOME` when there is none). `~`, `~/...` and paths relative to the prefill are accepted; `enter` confirms and `esc` cancels. When a project in the list already has that directory (symlinks resolved), the hub launches an agent there, like its `+ new`. Otherwise it starts the directory's own session (`agentctl-<dir>`, detached, as if `agentctl` were run there) and launches an agent in it with `agentctl launch --background --permission-mode auto`; a directory without `.claude/agents/` is initialized first, without starter templates and without the hooks or default-agent prompts of `agentctl init`. The hub refuses `$HOME` (it would create the global `~/.claude/agents`), `/`, files and missing directories; the error shows under the input and the dropdown stays open; the footer shows its hints. A click outside the dropdown (or on `+ open` again) closes it. Below the input the dropdown lists up to 8 recent project directories (`~`-shortened): the hub's origin dir plus the project dirs of windows closed in the last 30 days, deduped by resolved path, leaving out dirs that already have a group in the hub, `.worktrees/` paths and missing dirs. `↑`/`↓` highlight one and `enter` (or a click) opens it; typing filters the list by substring, and with nothing highlighted `enter` opens the typed path. The same flow from a shell is `agentctl open [dir]`: it resolves `dir` (default cwd; `~` and relative paths expanded; a repo subdirectory or `.worktrees/` checkout opens the main repo root), initializes and starts the session detached when needed (same same-name refusal), then switches the tmux client to it, or attaches outside tmux; `--detach` only prints the session name. It launches no extra agent. A git repository is not required. Two directories with the same name map to the same session name; when that name is already taken by another directory's session, the hub reports it rather than launching there.

Project order is saved globally in `group-order.json` in the same state dir (keyed by project directory); projects not in it follow alphabetically, and `agentctl status` and `jump-next-blocked` use the same order; the phone list uses the same order (and reorders it by long-press and drag). Fold and collapse state, the filter, the sort, the shell, closed and schedules toggles and the activity pane's dragged width are kept per hub across reloads, hot reloads and restarts, in `hub-view.json` in the agentctl state dir (`$XDG_STATE_HOME/agentctl`, default `~/.local/state/agentctl`). Jumps that target a hidden agent (`b`, a just-launched agent, a tab click in the live pane) unfold its group and expand its collapsed parents first, and clear the filter if it would still hide it. The one-line footer has no room for `N`, `R`, `v`, `o`, `A`, `z`, `Z`, `f`, `$`, `C`, `T` and `S`; they are listed under `?`.

`w`, the `sweep N` button on a group header (shown only when N > 0 of the group's agents are done, left of `+ new`; dropped on headers too narrow to keep four columns of project name, and hidden while the `shell | cron | agent` selector is armed; N counts done rows, so sweep's worktree and PR checks may close fewer), or `[sweep]` in a header's right-click menu previews [`agentctl sweep`](dispatch.md) for that project's tmux session. The hub runs `agentctl sweep --session <name> --dry-run --json` as a subprocess, pinned to the project's runtime dir, and shows the plan in the footer: `sweep <project>: close 3 (a, b, c), 2 need attention, 1 in progress  y/n`. `y` applies it (`agentctl sweep --session <name> --json`) and then reports `swept N` from what actually closed, since sweep re-checks every window at apply time; any other key, a click elsewhere, or moving the cursor off the line cancels. With nothing to close the footer says `nothing to sweep` and nothing is armed. While a sweep runs the footer shows `sweeping <project>…` and further sweeps are ignored. A failed sweep (session gone, non-zero exit) shows its error. Sweep's own rules decide what closes (`shell`, `improve~*`, untracked and bare windows never do; the hub does not pass `--include-bare`), and the hub's own window is the sweep's self, so it is never closed. Groups that are not agentctl project sessions have no button.

### Hub list tree

Within each project group the hub list is a parent/child tree. An agent dispatched by another agent renders directly under its dispatcher, indented with `├─` / `└─` / `│` connectors (nesting is recursive). Bare windows with no dispatcher are roots, in their usual order. Dispatched agents whose dispatcher is unknown, untrusted (no recorded session), or not in the same group are listed under a trailing `dispatched (N)` label. Windows launched by `agentctl cron` have no dispatcher: they show a muted `⏲ <job>` marker before their recap and, unless they have a parent in the group, sit under a `scheduled (N)` label after that. Every row stays selectable, and cursor movement, close-neighbour selection and jump-to-blocked all follow the displayed order.

### Hub list rows

A row is the state glyph, the title (a sub-agent's role label first; the agent name only when a row has no title and no recap text), then right-aligned columns: the PR, and the time in the current state (else since the last activity or start; `–` when unknown). The model is not on the row; the context use shows only from `models.context_warn_pct` (warn colour, error colour from the critical threshold), and when it does not apply its width goes to the title. Narrow rows drop the age first, then the PR; a context warning is never dropped. The hover card and the live viewer's statusline show the model and the context use. An idle row whose sub-agents are still working or blocked shows ◐ (muted accent) instead of ○/✓, and its detail says `waiting on <name> ●/⏸, …` (up to three names, then `+N`); precedence is ⏸ > ● > ! > ◐ > ✓ > ○. It is display only, like the turn-end ✓: counts, state filters and sweep treat the row as idle. A child counts when it was dispatched by that window's current session (not cron), in any tmux session. A done (✓) row more than 24 hours old dims to idle weight but keeps its green ✓ and its place in the ✓ counts. A title's leading issue number (`#21 …`) is dim. The selected row's detail is its full recap, its last activity, and a chip line only for what the row does not show: the issue, the agent name, a non-default backend or account. While the live pane is up it shows in the **status pane**, a 6-row pane stacked above the live pane (`agentctl hub-status`): the state glyph, title and project, the recap wrapped to the pane's width, the activity line and the chips; a header, closed or schedule line (or no selection) shows a muted placeholder. The status pane opens and closes with the live pane, so with no live pane (Schedules section, a window too narrow for it, the activity pane in its place, a failed show) or in a window under 24 rows the detail expands beneath the selected row instead (up to three lines plus chips, the activity left out while the live pane shows that window). Focus, keys and clicks in the status pane go on to the live pane.

## Notes

The keybinding layout is inspired by [BitchX](https://en.wikipedia.org/wiki/BitchX)'s layered key maps — direct keys for the most common actions, a prefix mode for window management, and muscle-memory-friendly mnemonics (a=agent, t=ticket, s=settings, etc.).

With `chrome_bar` on, the bottom bar lights up keybind hints when you press the tmux prefix, so you don't need to memorize the full table.
