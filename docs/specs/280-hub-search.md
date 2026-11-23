# Hub search: type to narrow the list

## Goal

In `agentctl hub`, press `/`, start typing something you remember ("flaky", "artifacts", "#278", "schedform") and the list narrows on every keystroke to the sessions that match. Rows that don't match are hidden, the cursor goes to the best match, and the live viewer follows it. Enter keeps the filter and returns to normal list navigation. Esc clears it.

## Decisions (Brendan, 2026-11-23)

- **Match scope: metadata only.** Matching runs over fields already in memory on `fleet.Row` (plus the closed ledger record and cron job). No transcript reads. A full-text transcript search is a later PR (see Follow-ups).
- **Closed rows: only when `C` is on.** The search filters the Closed section like any other section, and never shows it by itself.
- **Viewer follows the top match**, debounced so typing doesn't thrash tmux.

## Non-goals (v1)

- Transcript or pane-content search.
- Phone/web search (`web/`). Separate follow-up.
- Fuzzy/subsequence matching or regex. Plain case-insensitive substring terms only, because it's predictable.
- Re-ranking rows. Group order and tree order are unchanged. Search only hides rows.
- Persisting a query across hub restarts. It does carry across a hot reload (below).

## Code orientation

- `internal/tui/fleet.go`: `handleKey` (the modal pre-handlers for rename, new-session, schedule form, menu and the sweep/close/new arms, then `runKey`), `rebuildLines` (folds, attention filter, tree, collapse, Closed section, Schedules; also feeds `m.except` column widths from drawn rows), `resolveSelection` (handles hidden rows by falling back to the group header), `maybeShowCmd`/`liveShowCmd` (viewer follow), and the title tags around `titleLeft` (~L2157-2260).
- `internal/tui/fleet_fold.go`: `fleetFilter` + `filterRows` (keeps matches plus their ancestors as dimmed "context" rows), `hubViewState` + `HubViewEnv` (fold/filter state across a hot reload, also persisted to `<stateDir>/hub-view.json`).
- `internal/tui/fleet_rename.go`: the existing in-hub `textinput.Model` editor (open/handleKey/paste/blink wiring). Search reuses that pattern.
- `internal/tui/fleet_footer.go`: `footerBody`, `footerHints`, `openHelp` (the `?` key reference).
- `internal/tui/fleet_closed.go`: `closedLines`. `internal/tui/fleet_schedgroup.go` / `fleet_schedules.go`: schedule lines.
- `/` is currently unbound in `runKey`.

## Behaviour

### States

1. **Off**: no query. The hub behaves exactly as today.
2. **Editing**: the search input is open and has focus. Every keystroke re-filters.
3. **Committed**: the input is closed but the query still filters. Normal hub keys work on the narrowed list.

### Keys

| State | Key | Effect |
|---|---|---|
| Off / Committed | `/` | Open the input. In Committed, prefill it with the current query, cursor at the end. Remember the current selection as the "pre-search selection" (only when coming from Off). |
| Editing | printable text, paste | Edit the query. Pasted text has newlines/tabs collapsed to spaces and is capped at 200 runes. |
| Editing | `backspace` | Delete a rune. Backspace on an **empty** query closes search (back to Off, pre-search selection restored), like less/fzf. |
| Editing | `ctrl+u` / `ctrl+w` | Clear the query / delete the last word (textinput defaults are fine if they behave this way; check). |
| Editing | `down`/`ctrl+n`, `up`/`ctrl+p` | Move the cursor between selectable lines of the filtered list without leaving the input. `j`/`k` are text, not motion. |
| Editing | `enter` | Commit. The input closes and the filter stays. An empty query commits to Off. Enter does not also focus the live pane; a second Enter does that, as usual. |
| Editing | `esc` | Cancel: clear the query, back to Off, restore the pre-search selection if that row still exists (else the normal `resolveSelection` fallback). |
| Editing | `ctrl+c` | Quit the hub, as today. |
| Committed | `esc` | Clear the query (back to Off) and keep the **current** selection (the user navigated to it on purpose). Only the next `esc`/`q` leaves the hub. `q` still leaves immediately. |
| Committed | everything else | Unchanged (`j/k`, `enter`, `x`, `R`, `z`, `f`, `C`, ...). |

While Editing, no other single-letter hub binding fires. The input is a modal pre-handler in `handleKey`, placed alongside rename/new-session. Opening another modal (rename, new session, a menu) from Committed is fine. While Editing those can't be reached, apart from mouse (below).

### Matching

- The query is split on whitespace into terms. A row matches when **every** term (AND) is a case-insensitive substring of the row's haystack. Use `strings.ToLower` on both sides. There's no need for Unicode case folding beyond that.
- A term starting with `#` matches the ticket number or PR number exactly (`#278` matches `Ticket==278`, `TicketRef` ending in `#278`, or `PRNumber==278`). `#27` must not match 278. Any other `#...` is treated as plain text.
- **Live row haystack** (join with a separator that can't create a cross-field match, e.g. `\x00`): `Recap.Title`, `Recap.Text`, `Recap.Role`, `FirstPrompt`, `Task`, `TicketTitle`, `TicketRef`, `Branch`, `Name`, `AgentName`, `Project`, `SessionName`, `ModelName`, `Backend`, `Account`, `LastText`, `LastToolDetail`, the worktree dir basename. Leave out the full `Prompt`: ad-hoc dispatch prompts are long specs that would match almost any term. `Task` covers its first line. Note this decision in a code comment.
- **Shell rows**: match on the window name only, and only when shells are shown (`shownRows` already drops them otherwise).
- **Closed rows** (only when `C` is on): `ShortTitle`, `FirstPrompt`, `TicketTitle`, `Ticket`, `Branch`, `Agent`, `Instance`, `SessionName`, `Model`, plus the project basename. `#N` matching uses `Ticket`.
- **Schedule rows** (only when Schedules are shown): job name, agent, prompt text if it's inline (not a prompt_key lookup), template name, dir basename.
- Build each row's haystack once per reload, not per keystroke: a `map[key]string` rebuilt when groups/closed/jobs reload, keyed by session|windowID (and the closed record key and job ID). With about 100 rows any approach is fast, but keep it out of the render path.
- Put the match logic in a small pure function (`searchMatch(q parsedQuery, haystack string, nums ...int) bool`) with table tests. Put the query parsing and haystack builders in a new file, `internal/tui/fleet_search.go`.

### What the filtered list shows

- **Composition with the attention filter (`f`)**: AND. A row must pass both. The title shows both tags.
- **Ancestors**: generalize `filterRows` to take a predicate (`func(fleet.Row) bool`) so search reuses the same ancestor retention. A non-matching parent of a matching child is kept and dimmed (`context`), exactly as the attention filter does it. A matching parent does **not** pull in its non-matching children.
- **Folded groups (`z`/`Z`) and collapsed subtrees (`z` on a parent)**: while a query is active, ignore folds and collapses for display only, so any group or subtree with a match opens. Saved fold/collapse state is not changed, and clearing the query restores the folded layout. (Otherwise a match inside a folded group is invisible, which defeats the purpose.)
- **Groups with no matching rows are hidden entirely**, header included. That differs from the attention filter, which keeps headers. Their divider goes too. A group whose header text matches (project/session name) but whose rows don't: show the header plus all its rows. Typing a project name should show that project.
- **Closed / Schedules sections**: filter their rows the same way. Hide a section's label lines when no rows in it match.
- **No matches anywhere**: a single non-selectable placeholder line: `no matches for "<query>"` (muted). The cursor has nowhere to go, so the selection stays as it was internally and the viewer keeps showing whatever it last showed. Don't blank it.
- **Highlight**: in each drawn row's title/recap column, underline or bold-accent the substring that matched each term (first occurrence per term, case-insensitive), using the existing theme accent. If the match is only in a field the row doesn't draw (branch, prompt), there's no highlight. That's acceptable for v1 (the status pane shows the details). Highlighting has to survive truncation: highlight after `TruncateWords`, never split a styled run mid-rune, and keep the column widths from `computeExceptions` unchanged. If this turns out messy, it can go to a second PR. Say so in the PR rather than hacking it.
- `m.except` (column widths) is already computed from drawn rows only, so it adapts automatically. Check that it does.

### Selection and viewer

- After each re-filter, if the current selection is still visible, keep it. Otherwise move to the **first selectable matching line** in list order (skip context/dimmed ancestor rows when a real match exists). Don't use `resolveSelection`'s "stay on the group header of a hidden row" fallback while a query is active, because headers of non-matching groups are hidden.
- Viewer follow while Editing: after a re-filter or an up/down move changes the selected agent row, schedule a show through a `tea.Tick(150ms)` carrying a sequence number. Show only when the seq is still the latest when it fires. Closed and schedule selections never drive the viewer, as today. Committed state uses the normal immediate `maybeShowCmd`.
- `publishSelection` (follow-hub HUD) and the status pane follow the selection as usual.

### Rendering the input

- While Editing, the footer line is replaced by the input: `/ <query>▏` on the left and the match count right-aligned and muted (`3 matches`, `1 match`, `no matches`). The count is matching rows (not context rows) across all visible sections. On a very narrow hub, drop the count first.
- While Committed, the title row gets a tag like the filter tag: `/"<query>"`, truncated with an ellipsis at ~16 runes. When space runs out, include it in the existing tag-shortening steps (~L2176) **before** the counts are dropped.
- The footer hints (`footerHints`) gain `/ search` in Off. In Committed, the `esc` hint reads `esc clear search`. The `?` key reference (`openHelp`) gets a `/` line. Make sure the clickable help entry for `/` opens the input (it runs `runKey`).

### Mouse

- Clicking a list row while Editing commits the query (same as Enter) and then selects the row as a normal click does.
- Clicking the footer input area while Committed reopens Editing. Double-click rename and right-click menus behave as they do today, after the commit.
- Wheel scrolling works in every state.

### Reloads, hot reload, persistence

- Group, closed and schedule reloads re-run `rebuildLines` with the query applied. New windows that match appear, rows that stop matching disappear, and selection follows the rules above.
- The binary-change re-exec (`exeChanged`) must not fire while Editing (same guard as `renameOpen()`/`schedFormOpen()` at ~L880).
- A committed query carries across a hot reload: add `Query string` to the env handoff (`HubViewEnv`/`AGENTCTL_HUB_VIEW`) but **not** to `<stateDir>/hub-view.json`. A search shouldn't greet you on tomorrow's hub. Check how `hubViewState` is written to both places and keep the query out of the file (a separate field or struct, or blank it before the disk write). Add a test that the file never contains it.

### Edge cases to cover

- Zero agents / empty hub: `/` still opens, and the placeholder shows `no matches`.
- Only shell rows with shells hidden: no matches.
- Query of only spaces is the same as an empty query.
- Parent-cycle protection in `filterRows` is kept (the step bound).
- `revealRow` (used by jump-to-blocked `b`, follow targets, launch focus): if the target row is hidden by the query, clear the query (back to Off) and show a brief info line `search cleared`. Don't leave the user on an invisible selection. Apply the same to the `prefix+g` / launch-focus paths that call it.
- Hub window narrower than the input: the textinput scrolls horizontally, and nothing wraps.
- A rename/new-session/menu opened from Committed, then closed, returns to Committed with the query intact.

## Tests (package `internal/tui`; no real tmux, use the existing seams/stubs in the tui TestMain)

- Table tests for query parsing and `searchMatch`: AND terms, case, `#N` exact vs prefix, `#abc` as text, whitespace-only.
- Haystack builders: each listed field matches, the full `Prompt` does not, and a cross-field match (end of one field + start of the next) does not.
- `rebuildLines` with a query: hidden non-matching groups and their dividers; ancestor kept dimmed; folded group and collapsed subtree opened while searching and restored afterwards; composition with `f`; group header text match shows the whole group; Closed shown/hidden per `C`; schedules filtered; no-match placeholder.
- Key flow: `/` → type → `enter` → Committed → `esc` clears and keeps the selection; `/` → type → `esc` restores the pre-search selection; backspace on empty closes; `j`/`k` are text while Editing; `up`/`down` move while Editing; `q` in Committed leaves.
- Viewer debounce: two quick keystrokes produce one show (assert on the seq/stubbed show seam).
- Hot-reload handoff round-trips `Query`; the hub-view.json write never includes it.
- `revealRow` on a hidden target clears the query.
- Mouse click while Editing commits and selects.
- Run `go test ./internal/tui/ ./internal/testenv/` and `go vet ./internal/tui/`. Grep `cli_test.go` if any CLI help string changes. Keep tui tests free of new flakiness: no wall-clock sleeps, drive the debounce by delivering the tick message directly.

## Docs

- `docs/design/fleet-view-design.md` keybinding table (~L345): add `/`, and the Editing/Committed `esc` behaviour.
- `CLAUDE.md` `tui/` bullet: one clause noting hub search (`fleet_search.go`).
- Archive this spec to `docs/specs/<issue>-hub-search.md` in the PR.

## Shipping

One PR (`go-engineer`, Sonnet). If highlighting gets fiddly, ship filtering first and open highlighting as a second PR. Review on Sonnet (TUI only, no hooks/concurrency).

## Follow-ups (not in this ticket)

1. Transcript full-text search, perhaps a `//` prefix: a background, mtime-cached scan of each live window's JSONL user/assistant text, capped per file.
2. Phone search box on the windows list (`web/`), same matching rules (share the field list).
3. A "matched in: branch" hint for rows whose match is in a field they don't draw.
