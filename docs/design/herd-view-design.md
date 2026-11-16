# Design: the herd view (central hub)

Status: Phase 1 implemented; the live agent pane is the hub's only view
(the capture-pane preview and link-window open-in-hub were removed);
sidebar group dividers implemented
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
session ordered by tmux window index (stable; state never reorders rows).
Groups are alphabetical by project name; use `b` to jump to blocked agents.
Each session's group header carries a
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

Phase 1.5 — done (since superseded by the live pane). Sidebar + main panel
layout; see "Live agent pane" below.

Phase 2 — polish. Per-directory fsnotify instead of poll; recently-closed /
history rows; richer activity lines; a small "N blocked elsewhere" hint in each
session's existing chrome bar so you know when to check the hub.

Phase 3 — remote. Extend the registry with a host dimension and a read-only
state endpoint per machine; the hub merges remote sessions as additional row
sources. No change to the view model — remote is just more rows.

## Live agent pane

The hub window is two panes. The left pane runs the `HerdModel` list (the
grouped session/agent list, narrow: roughly 30% of the window, clamped to
44–56 columns by default). The right pane is a **live, fully interactive view
of the selected agent**: a nested tmux client attached to a *grouped viewer
session* that shares windows with the agent's home session
(`internal/tmux/hubview.go`). There is no other mode: the earlier
capture-pane preview panel, the `v` toggle, and the link-window "open in hub"
flow were all removed in favour of this.

**Selection drives the pane.** Moving the cursor onto an agent row (keys,
click, or a reload that moves the selection) hands that agent to the viewer
once per selection change (`liveShowCmd`). Selecting a group header or
placeholder, or having zero agents, leaves the pane on the last viewed agent;
until an agent has been viewed the list shows a one-line hint. If the viewer
fails to start, the error appears in the status line and the next selection
change retries; there is no fallback view.

**Safety.** Agent panes are never joined or moved. `tmux join-pane` was tried
early and rejected: a joined pane is destroyed when the hub dies and wrecks the
agent's chrome geometry. The viewer only *shares* the window via a grouped
session, so killing the viewer pane, the viewer session, or the whole hub
leaves every agent untouched (verified on a private tmux server).

**Focus.** `enter` (on an agent row) and `tab` move keyboard focus into the
live pane; `prefix+Left` / `prefix+Right` (tmux's default `select-pane`
bindings, which agentctl does not rebind) move focus between list and live pane
(`prefix+o` is agentctl's logs key). The list pane's mouse clicks only
select; they never open anything.

**Resizing.** The split is only re-applied when the hub *window* width
changes (compared against the last applied window width). A list-pane resize
at an unchanged window width is the user dragging the divider: the dragged
list width is remembered (clamped to 30 columns minimum, and so the live pane
keeps at least 20) and preserved across later window resizes and selection
changes rather than snapping back to the default ratio
(`decideLiveResize`).

**Returning to the list.** The herd-list window is pinned to the stable name
`HerdListWindowName`; `agentctl hub` (and so the `prefix+g` summon key)
selects it when the caller is already inside the hub session, otherwise it
switches into the hub.

### Prefix keys and tabs act on the viewed agent

The hub window carries no chrome-bar or HUD pane (`LaunchChromeBar` and
`ToggleHud` skip it); the agent's chrome bar inside the live pane is the only
tab bar. Every agentctl prefix binding sets `AGENTCTL_KEY_WINDOW=#{window_id}`
and `agentctl` calls `hubtarget.Apply` once, before paths are resolved. If that
window belongs to `agentctl-hub`, it resolves the current `agentctl-hubview*`
session, its active window, its home session (the non-viewer member of the
session group) and that session's `AGENTCTL_RUNTIME_DIR`, then substitutes the
window-id argument and the runtime dir; the usual cwd pin then lands in the
home project. `q` closes the hub, `R`/`K` are refused, and with no viewer a
"select an agent in the hub first" message is shown. The pure core is
`hubtarget.Resolve`.

Tab clicks in a chrome bar that is being viewed through the hub select the
window in the viewer session (`tmux.ViewerForWindow`); the hub polls the
viewer's active window each tick and moves its selection when it changed
without the hub causing it (`viewerSyncTarget`).

### Keymap (herd-list window)

| Key | Action |
|-----|--------|
| `j` / `down` | Move cursor down (skips dividers) |
| `k` / `up` | Move cursor up (skips dividers) |
| `enter` / `tab` | Focus the live pane (`enter` only on an agent row) |
| `prefix+←` / `prefix+→` | Move focus between the list and the live pane |
| `J` | Jump to the selected agent's window in its own home session |
| `n` / `x` | New agent in the selected group / close the selected agent |
| `b` | Select the next blocked agent and focus its live pane (stays in the hub) |
| `r` | Refresh |
| `q` / `esc` | Back to the previous tmux session (`switch-client -l`) |

Right-click on a row opens a menu: focus, close, home, new. Right-click on a
header offers new.

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
- **Ordering is stable, not attention-ranked.** Groups sort alphabetically
  by project (case-insensitive), rows by window index. A group's rollup status
  (blocked → working → done → idle) is display-only and never reorders.

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
  reads `#{session_path}` directly), `CapturePane` (plain-text pane capture
  for sweep/status; the hub itself no longer captures panes).
- Live pane primitives: `internal/tmux/hubview.go` — `ShowLiveViewer`,
  `HideLiveViewer`, `FocusLivePane`, `HubWindowWidth`, `ResizeLiveViewer`.
- The view: `internal/tui/herd.go` (`HerdModel`), following the existing
  full-screen browser pattern (`hud.go`, `agents.go`, `sessions.go`). List
  rendering: `renderList`, `renderLine`, `renderGroupHeader`, `renderRow`,
  `renderHerdDivider`. Cursor movement that skips dividers:
  `moveCursor`/`nextSelectable`. Live pane: `maybeShowCmd`/`liveShowCmd`,
  `focusLive`, `decideLiveResize`. Home jump: `jumpToSelectedHome`.
- `internal/tui/splitpane.go` (`SplitPane`) is used by the hub only for cursor
  and scroll bookkeeping; the other browsers use it for their layouts.
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
