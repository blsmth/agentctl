# Design: the herd view (central hub)

Status: Phase 1 implemented; sidebar + main panel layout implemented;
link-window (open-in-hub) and sidebar group dividers implemented
Date: 2026-08-07 (Phase 1 landed 2026-08-13; sidebar + main panel landed
2026-08-14; link-window + sidebar dividers landed 2026-08-14)

## Motivation

[Herdr](https://herdr.dev/) solves almost exactly the problem agentctl solves —
running many coding agents in parallel without losing track of them — and its
headline idea is a single view where every agent shows a state (working /
blocked / idle / done) and you jump straight to whichever one needs you. The
pitch is "you never hunt for the stuck one."

agentctl already has the hard part: a monitor that classifies every agent's
state and writes it to `state.json`, plus a chrome bar that renders live state
within a session. What's missing is a view *across* sessions. agentctl runs
**one tmux session per project** (`agentctl-<dir>`), and each session's monitor
only knows about — and only writes state for — its own project. So if you have
three projects going, you have three separate sessions and no single place to
see the whole picture.

An earlier draft of this design embedded a herdr-style sidebar into every
window. That fits the mirror-in-every-room model herdr uses, but it fights
agentctl's per-project architecture: to show all sessions, every window of every
session would carry a duplicate cross-session view, each needing to read every
other session's state and jump into it. This document proposes the inverse and
simpler model: **one central "herd view" — a dedicated hub session that
aggregates every agentctl session on the machine** into a single live dashboard
you switch into, glance at, and jump from.

## The core idea

The hub is a **read-only aggregator**. It doesn't own any agents and doesn't
change how the per-project sessions work. It discovers every running agentctl
session, reads each one's `state.json`, merges them into one list tagged by
session, and renders a full-screen view. Selecting a row jumps you into that
agent's window in its own session via `tmux switch-client`.

This inverts the sidebar model's costs. There is one thing to build and maintain
instead of a copy in every window; no per-window screen real estate is spent;
and — critically for the multi-machine goal — a remote machine later becomes
just "another source of rows" feeding the same aggregator, not a rewrite.

It also has precedent in the codebase: the `agents`, `sessions`, and `tickets`
browsers are already full-screen Bubble Tea views (opened via
`OpenOrFocus*Window`). The herd view is the same pattern, scoped across sessions
instead of within one.

## Goals

Glance at one view and answer, across every project: which agent is blocked and
waiting on me, which are still working, which are done and ready to review — then
jump to any of them with one key. The hub is a persistent home base you can
always switch back to. It spans all agentctl sessions on the machine, and is
designed so a later phase can fold in agents on remote machines without
restructuring.

Non-goals for the first cut: driving agents from the hub (send-input, approving
permission prompts), and any per-window sidebar. Both are possible follow-ups.

## How the current state pipeline works

The design leans on the existing pipeline, so it's worth stating. `docs/architecture.md`
and `docs/monitor-architecture.md` are the current references; the short version:

State is **hook-driven**. Claude Code hooks write a per-window state string to
`.agentctl/windows/<windowID>.json` (`windowstate`). The monitor ticks every
500ms, resolves each window's state to an `AgentStatus` (`working` / `idle` /
`blocked` / `gone`), fires transition handlers, renames the window with a state
glyph, and writes the authoritative snapshot to `state.json` under
`paths.RuntimeDir()` (which is `<project>/.agentctl/`). UI panes read `state.json`
via `state.LoadSnapshot()`. There is one `state.json` per project/session, and
`AgentState` carries a window ID but no session or project key.

The hub reads N of those snapshots instead of one.

## Design

### The hub session

`agentctl hub` creates (or attaches to) a dedicated tmux session — say
`agentctl-hub` — running a single full-screen Bubble Tea aggregator. It is
persistent: it survives independently of any project session, and it's the thing
you `switch-client` back to. A global keybind (bound in every agentctl session's
`applyConfig`) switches you to the hub from wherever you are; the hub itself
binds a key to switch back to your previous session.

The renderer follows the existing full-screen browser pattern (`HudModel` /
`agents.go` / `sessions.go`): alt-screen, its own refresh loop, viewport/scroll,
selection. Given it reads several files across several directories, a simple
timer poll (500ms–1s) is the pragmatic refresh mechanism — matching `HudModel`'s
existing poll — with per-directory fsnotify as a later optimization.

### Discovering sessions — tmux is primary, the registry is enrichment

**Correction (Phase 1, post-ship):** an earlier draft of this section claimed
that enumerating `tmux list-sessions` and filtering `agentctl-*` "can't
recover each session's project directory... without a mapping, which is
exactly what the registry provides." **That claim is false and is retracted.**
tmux exposes each session's working directory directly via
`#{session_path}` — `tmux list-sessions -F '#{session_name}|#{session_path}'`
gives you the project directory for every live session with zero registry
involvement, and `<session_path>/.agentctl/state.json` is exactly where that
session's snapshot lives. tmux alone is sufficient for complete local
discovery. Do not re-derive the registry-as-sole-discovery design from the
retracted claim above — it does not hold.

**tmux is the primary discovery mechanism.** The hub enumerates
`tmux list-sessions`, filters to `agentctl-*` (excluding the hub's own
session, which owns no agents), and for each one derives
`<session_path>/.agentctl` as the runtime dir to read `state.json` from. This
means every running session shows up in the hub immediately, regardless of
whether its monitor is new enough to know about the registry, whether it
ever successfully registered, or whether it crashed after registering —
discovery no longer depends on the registry's liveness at all.

**The registry is enrichment, not discovery.** A registry entry for a
session tmux already found overlays a couple of fields the tmux-derived
values can't provide: an authoritative `RuntimeDir` (a session's cwd can
differ from its project root, so `<session_path>/.agentctl` is a good
default but not always correct) and diagnostics (`PID`, `StartedAt`). The
merge is keyed by session name and pure (`herd.MergeSessions`), independent
of any tmux or filesystem I/O, which is what makes it table-driven testable.

**The registry is also the Phase 3 remote seam.** A registry entry whose
session name has no matching *local* tmux session is either stale (a local
entry with no live session — dropped here; `registry.Prune`, which the hub
already runs for its own housekeeping, removes the file) or a remote
session (`Host != "local"`) with no local tmux session to match against by
definition — those are kept. This is the one thing the registry still
uniquely provides: a remote host publishes its sessions into (a view of)
the registry, and the hub merges those rows in exactly the same way it
always has, without changing the merge's shape.

Each agentctl monitor still registers itself on startup and refreshes a
heartbeat/last-seen timestamp on its tick, and deregisters on clean
shutdown, unchanged — the registry's write side isn't going anywhere, it's
just no longer load-bearing for local discovery.

### Aggregated rows and rollups

Each row shows a state glyph, the agent's instance name, its session/project, and
a short activity line. Rows are grouped by session (top level), and within a
session ordered so the things needing attention float up: **blocked first, then
done (unseen), then working, then idle.** Each session's group header carries a
rolled-up state — blocked if any member is blocked, else working if any is
working, else done if any is done-unseen, else idle — so with a dozen agents you
scan headers first and only expand the session that's blocked. This is herdr's
rollup idea applied at the session level.

To tag rows by session, either add a `Session`/`Project` field to
`state.AgentState`, or tag at load time in the hub from the registry entry the
snapshot came from. Tagging at load keeps the per-project monitors unchanged and
is the lighter first step.

### Activity summaries

The data for a good one-line summary already exists but isn't persisted. Each
tick, `logstream.StateTracker` captures the last assistant message's `LastText`,
`ToolName`, and `ToolDetail` (e.g. "Editing monitor.go"), but these live only in
memory. The minimal enabling change is to persist `LastText` and
`LastToolDetail` onto `state.AgentState` in `persistState`, so any consumer of
`state.json` (the hub, and the chrome bar too) can show them. Small, isolated,
independently useful — do it first. For a stable "what is this agent working on"
line, the session's first user message is available cheaply from the `.meta.json`
sidecar (`sessions.SessionMeta.FirstUserMessage`).

### Jump to agent

Selecting a row switches you into that agent's window in its own session:
`tmux switch-client -t <session>:@windowID`. This is the one new tmux primitive
(existing `SwitchToWindow` only targets the current session's `SessionName`; the
hub needs a session-qualified target). A "jump to next blocked agent" binding —
across all sessions — is the single highest-value shortcut and the literal
answer to "never hunt for the stuck one." The hub also binds a key to return to
the previously focused session.

## Suggested phasing

**Phase 1 — done.** Local hub. `agentctl hub` session + the session registry +
the aggregator view (grouping by session, rollups, ordering) + cross-session
jump + a global summon keybind. Persist `LastText`/`LastToolDetail` to
`state.json` for activity lines. This delivers the full "one view into every
local session, jump to the blocked one" value.

Shipped as: the session registry + `LastText`/`LastToolDetail` persistence
(the groundwork PR), then the hub session, `internal/herd` aggregator, `internal/tui`
view, jump primitives, `jump-next-blocked`, and the `prefix+g` / `prefix+b`
keybinds (this PR).

Phase 1.5 — done. Sidebar + main panel layout, replacing the flat single-list
render for wide terminals. See "Sidebar + main panel layout" below.

Phase 2 — polish. Per-directory fsnotify instead of poll; recently-closed /
history rows; richer activity lines; a small "N blocked elsewhere" hint in each
session's existing chrome bar so you know when to check the hub.

Phase 3 — remote. Extend the registry with a host dimension and a read-only
state endpoint per machine; the hub merges remote sessions as additional row
sources. No change to the view model — remote is just more rows.

## Sidebar + main panel layout

The flat single-list render (Phase 1) becomes a **sidebar + main panel**
layout on any terminal at least `herdMinSplitWidth` (80) columns wide: the
grouped session/agent list narrows into a left-hand sidebar, and a main panel
on the right shows live detail for whatever the cursor is on. Below that
width a sidebar is unusable — names and activity truncate into mush — so the
hub falls back to the original full-width flat list unchanged.

**Built on `SplitPane`, not a hand-rolled two-column layout.** `SplitPane`
(`internal/tui/splitpane.go`) already backed the Memory/Tickets/Sessions/
Agents browsers' list-on-top-detail-on-bottom layout. Rather than write a
second component, it gained an `Orientation` field: `OrientVertical` (the
existing behaviour, and the zero value — so every prior caller is
byte-for-byte unaffected) or `OrientHorizontal`, which places the list and
detail panes side by side instead of stacked. `ListWidth`/`DetailWidth` give
the column split (reusing `ListRatio`/`MinListRows` as column counts rather
than row counts in this orientation); `ListHeight`/`DetailHeight` both
return the full terminal height, since side-by-side panes share one height
rather than dividing it. The hub is `SplitPane`'s first `OrientHorizontal`
consumer.

**Every sidebar line is selectable, not just agent rows.** Landing the
cursor on a session's group header (or its "no agents" placeholder) selects
that *session* rather than any one agent, and the main panel shows a
session-level summary instead of agent detail — project path, agent count,
and the state-breakdown counts (blocked/working/done/idle) across the
session's agents. This is new: Phase 1's flat list only ever let the cursor
land on an agent row. `Enter` still only acts on an agent-row selection
(jump); it's a no-op on a header/placeholder selection, since there's no
single window to jump to.

**Main panel content for an agent row**: metadata (state, model, context %,
cumulative input/output tokens, ticket number if any, activity line) above a
live mirror of that agent's terminal, via a new `tmux.CapturePane(session,
windowID, lines)` helper (`internal/tmux/hub.go`) wrapping `tmux capture-pane
-p -t <session>:<windowID>`, trimmed of the trailing blank lines tmux pads
its output with. **Only ever called for the currently-selected row** — not
once per row per tick — so the hub shells out to tmux at most once per ~1s
poll tick, exactly like the jump primitives it already had. A dead pane
(window closed between polls) surfaces as an inline "preview unavailable"
message in the main panel, never a crash.

**`tmux join-pane` was tried and explicitly rejected — do not retry it.** An
early spike considered joining the live agent pane into the hub's main panel
directly, instead of a read-only mirror. Tested on a throwaway server with a
marker process to observe pane lifecycle across a join: the joined pane is
**destroyed** when the hub session dies (`just relaunch`, `agentctl quit`,
etc.) rather than returning home — `join-pane` moves a pane, it doesn't
share it, so killing the window that now contains it kills the agent's real
pane too. It also split the wrong pane in practice and left the agent's home
window rendering as a full-screen chrome bar on return, wrecking the 45/4
chrome geometry the per-session hooks maintain. `capture-pane` is read-only
and non-destructive by construction, which is why it's the only primitive
used here.

**`tmux link-window` (Enter opens the agent inside the hub, instead of
switching away) shipped as a follow-up.** Unlike `join-pane`, `link-window`
genuinely is safe — a linked window is *shared* between the two sessions,
not moved, so killing the hub session leaves the agent's window alive (tmux
only destroys a window when its last link goes away). This was re-verified
on a throwaway server (`tmux -L linktest`) specifically for this change:
linking a window into a second session, then killing that session, left the
source window and its pane's process running untouched. See "Link-window:
open an agent inside the hub" below for the shipped design.

## Link-window: open an agent inside the hub

`Enter` on an agent row links that agent's window into the hub session and
selects it — the caller lands in the real, fully interactive agent
terminal, without leaving the hub session. This replaces `Enter`'s old
behaviour of switching the client away to the agent's home session; that
plain-jump behaviour is still available under a separate key (`J`) for
workflows that specifically want the project's own session context.

**Why link-window, not join-pane.** See "`tmux join-pane` was tried and
explicitly rejected" above: `join-pane` moves a pane, so killing the window
that now contains it kills the agent's real pane too. `link-window` shares a
window between sessions instead of moving it — tmux only destroys a window
when its *last* link goes away — so killing the hub session (`agentctl
quit`, `just relaunch`) leaves a linked agent's window, and its process,
completely untouched in its home session. Verified directly on a throwaway
server (`tmux -L linktest`): link a window into a second session, kill that
session, confirm the source window and its pane's process are still alive.

**Command shape:**
```
tmux link-window -s <homeSession>:<windowID> -t <HubSessionName>:
tmux select-window -t <HubSessionName>:<windowID>
```
A linked window keeps the *same* window ID in both sessions, which is what
makes the operation idempotent: before linking, the hub checks whether that
window ID is already present in its own window list
(`tmux list-windows -t <hub> -F '#{window_id}'`) and just selects it if so.
This check is required, not just defensive — verified on the throwaway
server that `tmux link-window` itself is **not** idempotent: linking the
same window a second time silently produces a duplicate entry in the
destination session's window list rather than erroring or no-op'ing.

**Unlinking (`u`)** detaches the selected agent's window from the hub,
leaving it exactly as-is in its home session, via plain
`tmux unlink-window -t <hub>:<windowID>` — **never** `-k`, which would
destroy the window outright once the hub's link was its last one, killing a
live agent. tmux itself refuses to unlink a window that is only linked to
one session (`"window only linked to one session"`, verified on the
throwaway server) — the hub treats that refusal as a normal, non-fatal
outcome: it's surfaced as a dim inline message (not the red error styling
used for real failures) and does not trigger a reload.

**Link state in the sidebar.** An agent currently linked into the hub is
marked with a dim `↗` glyph after its name. This is computed fresh every
poll tick from `tmux.ListHubWindowIDs()` — tmux's own window list for the
hub session — never from any state the hub stores itself, since a linked
window can be unlinked, or simply vanish (its home window closed), at any
time.

**Returning to the list.** The herd-list window (the one running the
`HerdModel` aggregator) is pinned to a stable tmux window name,
`HerdListWindowName` (`"herd-list"`), specifically so it can be targeted
reliably once other windows — linked-in agents — start sharing the hub
session. `agentctl hub` (and therefore the `prefix+g` summon key, which is
server-global and so live inside every window of the hub session too, linked
ones included) now checks whether the caller is *already* attached to the
hub session: if so, it selects the herd-list window instead of issuing a
no-op `switch-client` to the session it's already in. That gives one
consistent "back to the list" gesture from anywhere — including from inside
a window linked in via `Enter`.

**Safety invariants**, all enforced in `internal/tmux/hub.go`:
- `LinkWindowIntoHub` refuses a window whose home session is the hub itself,
  and refuses an empty window ID, before shelling out to tmux at all.
- `UnlinkWindowFromHub` never passes `-k`.
- A tmux failure (e.g. the target window vanished between the last poll and
  the keypress) surfaces as an inline message in the hub view and triggers a
  reload — never a panic, never left silently stale.

### Keymap (herd-list window)

| Key | Action |
|-----|--------|
| `j` / `down` | Move cursor down (skips dividers) |
| `k` / `up` | Move cursor up (skips dividers) |
| `enter` | Link the selected agent's window into the hub and select it |
| `J` | Jump to the selected agent's window in its own home session |
| `u` | Unlink the selected agent's window from the hub |
| `b` | Jump to the next blocked agent anywhere (home session) |
| `r` | Refresh |
| `q` / `esc` | Back to the previous tmux session (`switch-client -l`) |

`prefix+g` (bound server-wide, live in every agentctl session including the
hub) is the summon key: from outside the hub it switches into the herd-list
window; from inside the hub — including from inside a window linked in via
`Enter` — it selects the herd-list window, i.e. "back to the list" from
anywhere.

## Sidebar group dividers

A non-selectable horizontal rule is inserted between adjacent groups in the
sidebar (both the split layout and the narrow-terminal flat fallback, which
share the same `renderLine`), so the groups read as distinct blocks. There
is never a divider before the first group or after the last. The divider
spans the sidebar's content width (measured with `lipgloss.Width`, never
`len`) and reuses the existing dim/muted style (`tableStyles.Dim`, via
`herdDimStyle`) — no new palette entry.

The line-model gains a fourth `herdLineKind`, `herdLineDivider`, alongside
the existing header/placeholder/row kinds. Three places need to explicitly
skip it, since the pre-divider design assumed every line was selectable:
cursor movement (`moveCursor`/`nextSelectable`, which steps past a divider
in whichever direction it's moving rather than landing on it), selection
re-resolution after a reload (`resolveSelection`, whose row/header matching
loops already skip anything that isn't a `herdLineRow` or
`herdLineHeader`/`herdLinePlaceholder`, so dividers fall through for free),
and `syncSelectionFromCursor` (defensively — the cursor should never
actually land on a divider given the above, but it explicitly no-ops on one
rather than adopting a divider's zero-value `Group` as the selection).

## Decisions made (Phase 1)

The "Open questions" from the original draft, resolved by what actually shipped:

- **Registry location and format** — a directory of per-session files,
  `~/.local/state/agentctl/registry/<session-name>.json`, not a single locked
  file. This landed ahead of the hub itself (the groundwork PR) precisely because it
  mirrors `windowstate`'s per-window-file approach: N monitors
  registering/heartbeating concurrently never contend on a single file.
  `internal/registry` owns `Register`/`Heartbeat`/`Deregister`/`List`/`Prune`
  and the `StaleAfter` constant (30s).
- **Hub auto-start** — explicit only. `agentctl hub` (bound to `prefix+g` in
  every session) creates the `agentctl-hub` session if absent and switches
  into it; nothing auto-starts it on its own. In practice this makes the
  summon key "always work" anyway, since it creates-then-switches in one step.
- **Return-from-hub behavior** — `tmux switch-client -l` (return to whichever
  session tmux considers "last"), not a session picker. Simple, and it's
  exactly what a human would do by hand.
- **Row grouping** — strictly by session. Session-then-worktree/branch was
  considered but dropped for Phase 1: dispatch worktrees already show up as
  distinct sessions in practice (one agentctl session per project directory,
  and a worktree is a project directory), so the extra grouping level didn't
  earn its complexity yet.
- **Write actions** — the hub stays strictly read-only + jump for Phase 1, as
  proposed. No send-input, no kill-from-hub, no approving prompts remotely.

## Deviations from the design

- **Activity line is narrower than originally sketched.** The design floated
  falling back to the session's first user message (from the `.meta.json`
  sidecar) for a "stable" summary line. Phase 1 ships the simpler two-level
  fallback only — `LastToolDetail`, then `LastText` — both already persisted
  onto `state.AgentState` by the groundwork PR. Pulling in
  `.meta.json` as a third fallback is a cheap follow-up if the two-level
  version proves too jumpy in practice.
- **A registry self-healing fix rode along with this PR, not called out in
  the original design.** The hub is the thing that prunes stale registry
  entries. That means a live monitor whose heartbeat merely lagged past
  `registry.StaleAfter` (laptop sleep, a long GC pause, a busy tick) would
  get pruned and then have every subsequent heartbeat fail forever — the
  session would vanish from the hub until agentctl was restarted. The
  monitor now re-`Register`s from its retained `registry.Entry` whenever a
  `Heartbeat` call fails, closing that gap.
- **A second staleness threshold was added that the design doc didn't
  anticipate.** `registry.StaleAfter` (30s) governs when the hub *prunes* a
  session outright. `herd.SnapshotStaleAfter` (5s) is new and smaller: it
  governs when a *present* session's `state.json` is old enough that its
  group should render as degraded (`Stale: true`). A session can fail this
  check well before it fails the registry's own staleness check.
- **Discovery inverted: tmux primary, registry enrichment (see the corrected
  "Discovering sessions" section above).** The original Phase 1 implementation
  used the registry as the sole discovery source, which meant a session whose
  monitor predated the registry feature — or crashed after registering — was
  permanently invisible to the hub. `herd.MergeSessions` now merges tmux's
  live `agentctl-*` sessions (via `tmux.ListSessionsWithPaths`, primary) with
  registry entries (enrichment + the Phase 3 remote seam), keyed by session
  name.
- **A stale group now keeps its last-known rows instead of rendering
  empty.** The original implementation dropped `Rows` entirely once a
  snapshot went stale, so a briefly-wedged monitor made a whole project's
  agents vanish from the hub. `LoadGroup` now only produces an empty,
  row-less group when the snapshot is missing or unreadable; a snapshot that
  merely went stale keeps rendering its last-known rows, dimmed, with a
  "stale" marker on the group header — and those rows stay jumpable, since
  the tmux window is very likely still there even though the snapshot is
  momentarily out of date.
- **Row order and group rollup deliberately use different precedence**,
  exactly as scoped in the implementation ticket but worth restating since
  it's easy to miss on a skim: within a group, rows sort blocked → done
  (unseen) → working → idle, but a *group's* rolled-up status is blocked →
  working → done → idle. Working outranks done-unseen at the group level (a
  session with any agent still working is "busier" than one that's merely
  finished-and-unseen) even though an individual done-unseen row floats
  above a working row within its own group.

## Key code references

Where things ended up:

- Session registry: `internal/registry/` (`Register`, `Heartbeat`, `Deregister`,
  `List`, `Prune`, `StaleAfter`). Monitor registration, heartbeat, and the
  self-healing re-register-on-failed-heartbeat logic: `monitor.go`
  (`registry.Register`/`heartbeatOrReregister`).
- Aggregator data layer (pure logic, no TTY): `internal/herd/herd.go` —
  `Row`, `Group`, `RollupStatus`, `Load`/`LoadGroup`, `MergeSessions`,
  `TagRows`, `SortRows`, `RollupRows`, `OrderGroups`, `FirstBlocked`,
  `ActivityLine`.
- Hub session lifecycle and jump primitives: `internal/tmux/hub.go` —
  `HubSessionName`, `HerdListWindowName`, `HubSessionExists`,
  `StartHubSession`, `SwitchToHub`, `SwitchToSessionWindow`,
  `SwitchToLastSession`, `SelectHerdListWindow`, `CurrentSessionName`,
  `ListSessions`, `ListSessionsWithPaths` (the primary discovery source —
  reads `#{session_path}` directly), `CapturePane` (the main panel's
  read-only terminal mirror; see "Sidebar + main panel layout" above for why
  it's `capture-pane`, not `join-pane`).
- Link-window primitives (same file): `LinkWindowIntoHub`,
  `UnlinkWindowFromHub`, `SelectHerdWindow`, `ListHubWindowIDs`, plus the
  pure argument builders `linkWindowArgs`/`unlinkWindowArgs`/
  `selectHubWindowArgs`/`hubWindowTarget` that make the exact tmux command
  shapes table-driven testable without a live tmux binary. See "Link-window:
  open an agent inside the hub" above.
- The view: `internal/tui/herd.go` (`HerdModel`), following the existing
  full-screen browser pattern (`hud.go`, `agents.go`, `sessions.go`). Layout
  branch: `useSplitLayout`/`renderFlat`/`renderSplit`. Sidebar rendering
  (shared with the flat fallback, just narrower): `renderLine`,
  `renderGroupHeader`, `renderRow`, `renderHerdDivider`. Cursor movement that
  skips dividers: `moveCursor`/`nextSelectable`. Link/unlink/home-jump
  actions: `linkSelectedIntoHub`, `unlinkSelected`, `jumpToSelectedHome`.
  Link-state glyph: `rowIsLinked`, driven by `herdLinkedCmd` polling
  `tmux.ListHubWindowIDs()` every tick. Main-panel content mapping (pure, no
  TTY): `mainPanelContent`, `renderAgentDetail`, `renderGroupSummary`,
  `stateBreakdown`.
- The reusable split-layout component: `internal/tui/splitpane.go`
  (`SplitPane`, `Orientation`/`OrientVertical`/`OrientHorizontal`,
  `ListWidth`, `DetailWidth`). Used in `OrientVertical` (the original,
  default) by the Memory/Tickets/Sessions/Agents browsers, and in
  `OrientHorizontal` by the hub.
- Commands: `internal/cmd/hub.go` (`RunHub`, `RunHubView`,
  `RunJumpNextBlocked`), wired up in `cli.go` as `agentctl hub`,
  `agentctl hub-view` (hidden), `agentctl jump-next-blocked` (hidden).
- Summon + jump keybinds: `internal/tmux/tmux.go`'s `applyConfig`
  (`prefix+g`, `prefix+b`).
- State types / snapshot: `internal/state/status.go`, `internal/state/state.go`
  (`AgentState`, `LoadSnapshotFrom`).
- Activity data source: `LastToolDetail`/`LastText` on `state.AgentState`,
  captured by `logstream.StateTracker` and persisted each tick in
  `monitor.go`'s `persistState`.
