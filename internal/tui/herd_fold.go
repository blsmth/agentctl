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
	"github.com/blsmth/agentctl/internal/herd"
	"github.com/blsmth/agentctl/internal/logging"
)

// ── Attention filter ─────────────────────────────────────────────────────────

// herdFilter is the hub's attention filter: which agent rows the list shows.
// `f` cycles all → active → needs you → all.
type herdFilter int

const (
	herdFilterAll    herdFilter = iota // every row
	herdFilterActive                   // working and blocked
	herdFilterNeeds                    // blocked and done-unseen
)

// label is the filter's name as shown in the title tag and notices.
func (f herdFilter) label() string {
	switch f {
	case herdFilterActive:
		return "active"
	case herdFilterNeeds:
		return "needs you"
	}
	return "all"
}

// key is the filter's persisted spelling ("" for all).
func (f herdFilter) key() string {
	switch f {
	case herdFilterActive:
		return "active"
	case herdFilterNeeds:
		return "needs"
	}
	return ""
}

// parseHerdFilter is the inverse of key; anything unknown is all.
func parseHerdFilter(s string) herdFilter {
	switch s {
	case "active":
		return herdFilterActive
	case "needs":
		return herdFilterNeeds
	}
	return herdFilterAll
}

// next is the filter `f` switches to.
func (f herdFilter) next() herdFilter {
	switch f {
	case herdFilterAll:
		return herdFilterActive
	case herdFilterActive:
		return herdFilterNeeds
	}
	return herdFilterAll
}

// matches reports whether row r passes the filter on its own (ancestor
// retention is filterRows' job).
func (f herdFilter) matches(r herd.Row) bool {
	w := rowWeight(r)
	switch f {
	case herdFilterActive:
		return w == herdWeightWorking || w == herdWeightBlocked
	case herdFilterNeeds:
		return w == herdWeightBlocked || w == herdWeightDone
	}
	return true
}

// filterRows returns the rows of one group the filter keeps, in their
// original order, plus the window IDs kept only as context: the ancestors
// (by Parent, within the group) of a matching row, so the tree still makes
// sense. Under herdFilterAll it returns rows unchanged and no context.
func filterRows(rows []herd.Row, f herdFilter) (kept []herd.Row, context map[string]bool) {
	if f == herdFilterAll {
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
func (m HerdModel) isFolded(session string) bool {
	return m.folded[session]
}

// setFolded folds or unfolds the group for session.
func (m *HerdModel) setFolded(session string, fold bool) {
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
func groupIsQuiet(g herd.Group) bool {
	for _, r := range g.Rows {
		if w := rowWeight(r); w == herdWeightBlocked || w == herdWeightWorking {
			return false
		}
	}
	return true
}

// relayout rebuilds the line list after a fold or filter change and
// re-resolves everything that is keyed by line index.
func (m *HerdModel) relayout() {
	m.rebuildLines()
	m.validateNew()
	m.resolveSelection()
	m.refreshHover()
}

// toggleFoldAt is `z` (and a click on a header's project name): fold or
// unfold the group owning line idx. The cursor lands on the group header,
// which is all a folded group shows.
func (m *HerdModel) toggleFoldAt(idx int) tea.Cmd {
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
func (m *HerdModel) toggleFocusMode() tea.Cmd {
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

// cycleFilter is `f`: all → active → needs you → all.
func (m *HerdModel) cycleFilter() tea.Cmd {
	m.filter = m.filter.next()
	m.jumpErr = ""
	m.setSuccess("filter: " + m.filter.label())
	m.relayout()
	return m.saveViewCmd()
}

// rowExists reports whether a loaded group has the row (session, windowID),
// whether or not the list currently shows it.
func (m *HerdModel) rowExists(session, windowID string) bool {
	g := m.groupForSession(session)
	return g != nil && slices.ContainsFunc(g.Rows, func(r herd.Row) bool { return r.WindowID == windowID })
}

// revealRow makes the row (session, windowID) visible when it exists but is
// hidden: its group is unfolded and, if the filter would still hide it, the
// filter is cleared. It reports whether anything changed (the line list was
// then rebuilt, but the selection was not moved).
func (m *HerdModel) revealRow(session, windowID string) bool {
	g := m.groupForSession(session)
	if g == nil {
		return false
	}
	var row *herd.Row
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
	if m.filter != herdFilterAll {
		if kept, _ := filterRows(g.Rows, m.filter); !slices.ContainsFunc(kept, func(r herd.Row) bool { return r.WindowID == windowID }) {
			m.filter = herdFilterAll
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
	Focus  bool     `json:"focus,omitempty"`
	Filter string   `json:"filter,omitempty"`
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
func (m HerdModel) viewState() hubViewState {
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
	return hubViewState{Folded: folded, Focus: m.focusMode, Filter: m.filter.key()}
}

// applyViewState replaces the model's fold set, focus mode and filter.
func (m *HerdModel) applyViewState(s hubViewState) {
	m.folded = nil
	for _, sess := range s.Folded {
		if sess != "" {
			m.setFolded(sess, true)
		}
	}
	m.focusMode = s.Focus
	m.filter = parseHerdFilter(s.Filter)
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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+hubViewFile+".*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod %s: %w", tmp.Name(), err)
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("renaming to %s: %w", path, err)
	}
	return nil
}

// saveViewCmd persists the current view state in the background. A model
// without a state path (tests, or no resolvable state dir) persists nothing.
// Failures are logged, never shown: the state is a convenience.
func (m HerdModel) saveViewCmd() tea.Cmd {
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
func (m HerdModel) viewEnv() string {
	data, err := json.Marshal(m.viewState())
	if err != nil {
		return ""
	}
	return HubViewEnv + "=" + string(data)
}

// loadViewState sets the persisted view state at startup: the state file,
// overridden by HubViewEnv after a hot reload (consumed once).
func (m *HerdModel) loadViewState() {
	if path, err := hubViewPath(); err != nil {
		logging.Log("hub: view state not persisted: %v", err)
	} else {
		m.viewPath = path
		m.applyViewState(readHubView(path))
	}
	if v, ok := os.LookupEnv(HubViewEnv); ok {
		_ = os.Unsetenv(HubViewEnv)
		if s, ok := parseHubView([]byte(strings.TrimSpace(v))); ok {
			m.applyViewState(s)
		}
	}
}

// flushView persists a view change revealRow made, once (nil when none).
func (m *HerdModel) flushView() tea.Cmd {
	if !m.viewDirty {
		return nil
	}
	m.viewDirty = false
	return m.saveViewCmd()
}
