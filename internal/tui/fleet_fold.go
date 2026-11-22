package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/logging"
)

// ── Attention filter ─────────────────────────────────────────────────────────

// fleetFilter is the hub's attention filter: which agent rows the list shows.
// `f` cycles all → active → needs you → all.
type fleetFilter int

const (
	fleetFilterAll    fleetFilter = iota // every row
	fleetFilterActive                    // working and blocked
	fleetFilterNeeds                     // blocked and done-unseen
)

// label is the filter's name as shown in the title tag and notices.
func (f fleetFilter) label() string {
	switch f {
	case fleetFilterActive:
		return "active"
	case fleetFilterNeeds:
		return "needs you"
	}
	return "all"
}

// key is the filter's persisted spelling ("" for all).
func (f fleetFilter) key() string {
	switch f {
	case fleetFilterActive:
		return "active"
	case fleetFilterNeeds:
		return "needs"
	}
	return ""
}

// parseFleetFilter is the inverse of key; anything unknown is all.
func parseFleetFilter(s string) fleetFilter {
	switch s {
	case "active":
		return fleetFilterActive
	case "needs":
		return fleetFilterNeeds
	}
	return fleetFilterAll
}

// next is the filter `f` switches to.
func (f fleetFilter) next() fleetFilter {
	switch f {
	case fleetFilterAll:
		return fleetFilterActive
	case fleetFilterActive:
		return fleetFilterNeeds
	}
	return fleetFilterAll
}

// matches reports whether row r passes the filter on its own (ancestor
// retention is filterRows' job).
func (f fleetFilter) matches(r fleet.Row) bool {
	w := rowWeight(r)
	switch f {
	case fleetFilterActive:
		return w == fleetWeightWorking || w == fleetWeightBlocked
	case fleetFilterNeeds:
		return w == fleetWeightBlocked || w == fleetWeightDone
	}
	return true
}

// filterRows returns the rows of one group the filter keeps, in their
// original order, plus the window IDs kept only as context: the ancestors
// (by Parent, within the group) of a matching row, so the tree still makes
// sense. Under fleetFilterAll it returns rows unchanged and no context.
func filterRows(rows []fleet.Row, f fleetFilter) (kept []fleet.Row, context map[string]bool) {
	if f == fleetFilterAll {
		return rows, nil
	}
	idx := make(map[string]int, len(rows))
	for i, r := range rows {
		if r.WindowID != "" {
			if _, dup := idx[r.WindowID]; !dup {
				idx[r.WindowID] = i
			}
		}
	}
	match := make([]bool, len(rows))
	keep := make([]bool, len(rows))
	for i, r := range rows {
		match[i] = f.matches(r)
		keep[i] = match[i]
	}
	for i := range rows {
		if !match[i] {
			continue
		}
		// Walk up the parent chain; the step bound breaks parent cycles.
		cur := i
		for step := 0; step < len(rows); step++ {
			p, ok := idx[rows[cur].Parent]
			if rows[cur].Parent == "" || !ok || p == cur {
				break
			}
			keep[p] = true
			cur = p
		}
	}
	for i, r := range rows {
		if !keep[i] {
			continue
		}
		kept = append(kept, r)
		if !match[i] && r.WindowID != "" {
			if context == nil {
				context = make(map[string]bool)
			}
			context[r.WindowID] = true
		}
	}
	return kept, context
}

// ── Fold state ───────────────────────────────────────────────────────────────

// isFolded reports whether the group for session is folded.
func (m FleetModel) isFolded(session string) bool {
	return m.folded[session]
}

// setFolded folds or unfolds the group for session.
func (m *FleetModel) setFolded(session string, fold bool) {
	if fold {
		if m.folded == nil {
			m.folded = make(map[string]bool)
		}
		m.folded[session] = true
		return
	}
	delete(m.folded, session)
}

// groupIsQuiet reports whether g has no blocked and no working agent: the
// groups focus mode folds.
func groupIsQuiet(g fleet.Group) bool {
	for _, r := range g.Rows {
		if w := rowWeight(r); w == fleetWeightBlocked || w == fleetWeightWorking {
			return false
		}
	}
	return true
}

// relayout rebuilds the line list after a fold or filter change and
// re-resolves everything that is keyed by line index.
func (m *FleetModel) relayout() {
	m.rebuildLines()
	m.validateNew()
	m.resolveSelection()
	m.refreshHover()
}

// toggleFoldAt is `z` (and a click on a header's project name): fold or
// unfold the group owning line idx. The cursor lands on the group header,
// which is all a folded group shows.
func (m *FleetModel) toggleFoldAt(idx int) tea.Cmd {
	g := m.groupOfLine(idx)
	if g == nil {
		return nil
	}
	session := g.Session
	m.setFolded(session, !m.isFolded(session))
	m.pendingFocus = nil
	m.selSession, m.selWindowID, m.selIsHeader = session, "", true
	m.relayout()
	return m.saveViewCmd()
}

// toggleFocusMode is `Z`: the first press folds every group with no blocked
// and no working agent; the next press unfolds everything.
func (m *FleetModel) toggleFocusMode() tea.Cmd {
	m.jumpErr = ""
	if m.focusMode {
		m.focusMode = false
		m.folded = nil
		m.setSuccess("unfolded all projects")
	} else {
		m.focusMode = true
		n := 0
		for _, g := range m.groups {
			if groupIsQuiet(g) {
				m.setFolded(g.Session, true)
				n++
			}
		}
		m.setSuccess(fmt.Sprintf("focus mode: folded %d quiet project%s", n, plural(n)))
		// A row the cursor was on may now be hidden: fall back to its header.
		if !m.selIsHeader && m.isFolded(m.selSession) {
			m.selWindowID, m.selIsHeader = "", true
		}
	}
	m.relayout()
	return m.saveViewCmd()
}

// sortRecentKey is the persisted spelling of the recent-activity row order.
const sortRecentKey = "recent"

// sortKey is the persisted spelling of the row order ("" = window order).
func sortKey(recent bool) string {
	if recent {
		return sortRecentKey
	}
	return ""
}

// toggleSort is `o`: switch rows within each group between window order and
// most-recent-activity-first. The selection follows its window ID.
func (m *FleetModel) toggleSort() tea.Cmd {
	m.sortRecent = !m.sortRecent
	m.jumpErr = ""
	if m.sortRecent {
		m.setSuccess("sort: recent activity")
	} else {
		m.setSuccess("sort: window order")
	}
	m.relayout()
	return m.saveViewCmd()
}

// fleetShellsKey shows or hides the plain shell rows.
const fleetShellsKey = "$"

// toggleShells is fleetShellsKey: show or hide the shell rows. A selected
// shell row that disappears hands the cursor on like any vanished row.
func (m *FleetModel) toggleShells() tea.Cmd {
	m.setShowShells(!m.showShells)
	m.jumpErr = ""
	if m.showShells {
		m.setSuccess("shells shown")
	} else {
		m.setSuccess("shells hidden")
	}
	return m.saveViewCmd()
}

// setShowShells shows or hides the shell rows and re-lays out the list.
func (m *FleetModel) setShowShells(show bool) {
	m.showShells = show
	m.relayout()
}

// cycleFilter is `f`: all → active → needs you → all.
func (m *FleetModel) cycleFilter() tea.Cmd {
	m.filter = m.filter.next()
	m.jumpErr = ""
	m.setSuccess("filter: " + m.filter.label())
	m.relayout()
	return m.saveViewCmd()
}

// rowExists reports whether a loaded group has the row (session, windowID),
// whether or not the list currently shows it.
func (m *FleetModel) rowExists(session, windowID string) bool {
	g := m.groupForSession(session)
	return g != nil && slices.ContainsFunc(g.Rows, func(r fleet.Row) bool { return r.WindowID == windowID })
}

// revealRow makes the row (session, windowID) visible when it exists but is
// hidden: its group is unfolded, its collapsed ancestors are expanded and,
// if the filter would still hide it, the filter is cleared. It reports whether anything changed (the line list was
// then rebuilt, but the selection was not moved).
func (m *FleetModel) revealRow(session, windowID string) bool {
	g := m.groupForSession(session)
	if g == nil {
		return false
	}
	var row *fleet.Row
	for i := range g.Rows {
		if g.Rows[i].WindowID == windowID {
			row = &g.Rows[i]
			break
		}
	}
	if row == nil {
		return false
	}
	changed := false
	if m.isFolded(session) {
		m.setFolded(session, false)
		changed = true
	}
	if m.expandAncestors(session, windowID, g.Rows) {
		changed = true
	}
	if row.Shell && !m.showShells {
		m.showShells = true
		changed = true
	}
	if m.filter != fleetFilterAll {
		if kept, _ := filterRows(g.Rows, m.filter); !slices.ContainsFunc(kept, func(r fleet.Row) bool { return r.WindowID == windowID }) {
			m.filter = fleetFilterAll
			changed = true
		}
	}
	if changed {
		m.viewDirty = true
		m.rebuildLines()
		m.validateNew()
	}
	return changed
}

// ── Persistence ──────────────────────────────────────────────────────────────

// hubViewState is the fold set, focus mode and filter: persisted in the
// state dir (hubViewFile) and carried across a hot reload in HubViewEnv.
type hubViewState struct {
	Folded []string `json:"folded,omitempty"`
	// Collapsed is the collapsed parent rows, as "session|windowID".
	Collapsed []string `json:"collapsed,omitempty"`
	Focus     bool     `json:"focus,omitempty"`
	Filter    string   `json:"filter,omitempty"`
	// Sort is the in-group row order: "recent" or "" for window order.
	Sort string `json:"sort,omitempty"`
	// Shells shows the plain shell rows (hidden by default).
	Shells bool `json:"shells,omitempty"`
	// Closed shows the Closed section (hidden by default).
	Closed bool `json:"closed,omitempty"`
	// Schedules shows the Schedules section (hidden by default).
	Schedules bool `json:"schedules,omitempty"`
	// SchedCollapsed is the collapsed schedules blocks by block key: a
	// project's resolved dir, or ":other" for the bottom block.
	SchedCollapsed []string `json:"sched_collapsed,omitempty"`
	// ActivityPane is the open activity pane's tmux pane ID ("" = closed),
	// so a hot reload reattaches to it; ActivityWidth is the width the user
	// dragged it to (0 = the default).
	ActivityPane  string `json:"activity_pane,omitempty"`
	ActivityWidth int    `json:"activity_width,omitempty"`
}

// HubViewEnv carries the hub's fold set, focus mode and filter (JSON, see
// hubViewState) across a hot reload re-exec. It is consumed once at startup
// and wins over the state file.
const HubViewEnv = "AGENTCTL_HUB_VIEW"

// hubViewFile is the state file's name in the agentctl state dir.
const hubViewFile = "hub-view.json"

// hubViewPath is the state file's path; a seam for tests.
var hubViewPath = func() (string, error) {
	dir, err := config.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, hubViewFile), nil
}

// viewState snapshots the model's fold set, focus mode and filter. Folded
// sessions whose group has disappeared are dropped here (lazily, when the
// state is written) as long as the hub has a loaded, non-empty list to
// compare against; a failed or empty load keeps everything.
func (m FleetModel) viewState() hubViewState {
	live := make(map[string]bool, len(m.groups))
	for _, g := range m.groups {
		live[g.Session] = true
	}
	prune := m.loaded && m.loadErr == nil && len(m.groups) > 0
	var folded []string
	for s, ok := range m.folded {
		if ok && (!prune || live[s]) {
			folded = append(folded, s)
		}
	}
	slices.Sort(folded)
	return hubViewState{
		Folded: folded, Collapsed: m.liveCollapsed(prune), Focus: m.focusMode, Filter: m.filter.key(), Sort: sortKey(m.sortRecent), Shells: m.showShells, Closed: m.showClosed, Schedules: m.showSchedules,
		SchedCollapsed: m.liveSchedCollapsed(prune), ActivityPane: m.activity.pane, ActivityWidth: m.activity.userWidth,
	}
}

// liveCollapsed is the sorted collapsed set; with prune, entries whose
// window is no longer a row of its group are dropped (see viewState).
func (m FleetModel) liveCollapsed(prune bool) []string {
	live := make(map[string]bool)
	if prune {
		for _, g := range m.groups {
			for _, r := range g.Rows {
				live[collapseKey(g.Session, r.WindowID)] = true
			}
		}
	}
	var out []string
	for key, ok := range m.collapsed {
		if ok && (!prune || live[key]) {
			out = append(out, key)
		}
	}
	slices.Sort(out)
	return out
}

// applyViewState replaces the model's fold set, focus mode and filter.
func (m *FleetModel) applyViewState(s hubViewState) {
	m.folded = nil
	for _, sess := range s.Folded {
		if sess != "" {
			m.setFolded(sess, true)
		}
	}
	m.collapsed = nil
	for _, key := range s.Collapsed {
		if session, win, ok := strings.Cut(key, "|"); ok && session != "" && win != "" {
			m.setCollapsed(session, win, true)
		}
	}
	m.focusMode = s.Focus
	m.filter = parseFleetFilter(s.Filter)
	m.sortRecent = s.Sort == sortRecentKey
	m.showShells = s.Shells
	m.showClosed = s.Closed
	if !m.showClosed {
		m.closed = nil
	}
	m.showSchedules = s.Schedules
	m.schedCollapsed = nil
	for _, key := range s.SchedCollapsed {
		m.setSchedCollapsed(key, true)
	}
	if !m.showSchedules {
		m.sched = fleetSchedState{}
		m.schedForm, m.schedConfirm = nil, nil
	}
	// The recorded pane is confirmed or dropped by the first pane poll.
	m.activity = fleetActivity{pane: s.ActivityPane, userWidth: max(s.ActivityWidth, 0)}
}

// parseHubView decodes a hubViewState; ok is false for anything that is not
// valid JSON of that shape.
func parseHubView(data []byte) (hubViewState, bool) {
	var s hubViewState
	if err := json.Unmarshal(data, &s); err != nil {
		return hubViewState{}, false
	}
	return s, true
}

// readHubView reads the state file at path. A missing, unreadable or corrupt
// file yields the zero state: the hub then starts unfolded and unfiltered.
func readHubView(path string) hubViewState {
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			logging.Log("hub: reading %s: %v", path, err)
		}
		return hubViewState{}
	}
	s, ok := parseHubView(data)
	if !ok {
		logging.Log("hub: ignoring corrupt %s", path)
	}
	return s
}

// writeHubView writes s to path (0600, via a temp file and rename so a crash
// never leaves a torn file).
func writeHubView(path string, s hubViewState) error {
	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("encoding hub view: %w", err)
	}
	return writeFileAtomic(path, append(data, '\n'))
}

// saveViewCmd persists the current view state in the background. A model
// without a state path (tests, or no resolvable state dir) persists nothing.
// Failures are logged, never shown: the state is a convenience.
func (m FleetModel) saveViewCmd() tea.Cmd {
	if m.viewPath == "" {
		return nil
	}
	path, s := m.viewPath, m.viewState()
	return func() tea.Msg {
		if err := writeHubView(path, s); err != nil {
			logging.Log("hub: saving view state: %v", err)
		}
		return nil
	}
}

// viewEnv is the HubViewEnv entry for ReexecEnv.
func (m FleetModel) viewEnv() string {
	data, err := json.Marshal(m.viewState())
	if err != nil {
		return ""
	}
	return HubViewEnv + "=" + string(data)
}

// loadViewState sets the persisted view state at startup: the state file,
// overridden by HubViewEnv after a hot reload (consumed once).
func (m *FleetModel) loadViewState() {
	if path, err := groupOrderPath(); err != nil {
		logging.Log("hub: group order not persisted: %v", err)
	} else {
		m.orderPath = path
	}
	if path, err := hubViewPath(); err != nil {
		logging.Log("hub: view state not persisted: %v", err)
	} else {
		m.viewPath = path
		s := readHubView(path)
		// A pane ID in the file is from a previous hub process, whose exit
		// killed the pane: only a hot reload (HubViewEnv, below) carries a
		// live one. The dragged width is kept.
		s.ActivityPane = ""
		m.applyViewState(s)
	}
	if v, ok := os.LookupEnv(HubViewEnv); ok {
		_ = os.Unsetenv(HubViewEnv)
		if s, ok := parseHubView([]byte(strings.TrimSpace(v))); ok {
			m.applyViewState(s)
		}
	}
}

// flushView persists a view change revealRow made, once (nil when none).
func (m *FleetModel) flushView() tea.Cmd {
	if !m.viewDirty {
		return nil
	}
	m.viewDirty = false
	return m.saveViewCmd()
}
