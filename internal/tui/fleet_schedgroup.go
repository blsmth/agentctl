package tui

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/cronedit"
	"github.com/blsmth/agentctl/internal/fleet"
)

// Schedules tabs: each cron job belongs to the project group it runs in, and
// a group with at least one job gets "Sessions N · Schedules M" tabs on its
// header. The selected tab (Sessions by default, per project, persisted in
// hubViewState.SchedTabs by block key) decides what the group body lists:
// the agent rows or the jobs, never both. A job belongs to the first group
// whose project dir is the job's dir or contains it (a subdir, or a worktree
// under <project>/.worktrees/), longest project dir first, with both sides
// cleaned and symlink-resolved. Jobs no live group claims go in a trailing
// "Schedules · other projects (N)" block at the bottom of the list, collapsed
// by default, which also carries the global warnings.

// fleetSchedOtherKey is the block key of the bottom "other projects" block.
// Project block keys are absolute paths, so it cannot collide with one.
const fleetSchedOtherKey = ":other"

// fleetSchedHeadSel prefixes the selSchedule key of a block header.
const fleetSchedHeadSel = "\x00head:"

// fleetEvalSymlinks resolves a path's symlinks; a seam so tests can count
// the calls the cache saves.
var fleetEvalSymlinks = filepath.EvalSymlinks

// fleetDirCache memoizes resolved dirs: rebuildLines places every job on
// every rebuild, and EvalSymlinks stats each path component. Paths that do
// not resolve are cached too. A pointer, so the model's copies share it.
type fleetDirCache struct {
	m map[string]string
}

func newFleetDirCache() *fleetDirCache {
	return &fleetDirCache{m: make(map[string]string)}
}

// resolve returns dir cleaned and with symlinks resolved. A dir that does
// not exist (a removed worktree) resolves its longest existing ancestor and
// keeps the rest as written. A nil cache resolves without memoizing.
func (c *fleetDirCache) resolve(dir string) string {
	if dir == "" {
		return ""
	}
	dir = filepath.Clean(dir)
	if c != nil {
		if r, ok := c.m[dir]; ok {
			return r
		}
	}
	r := resolveDir(dir)
	if c != nil {
		c.m[dir] = r
	}
	return r
}

// resolveDir is resolve without the cache.
func resolveDir(dir string) string {
	rest := ""
	for d := dir; ; {
		if r, err := fleetEvalSymlinks(d); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(d)
		if parent == d {
			return dir
		}
		rest = filepath.Join(filepath.Base(d), rest)
		d = parent
	}
}

// pathWithin reports whether p is root or below it, on a path boundary.
func pathWithin(p, root string) bool {
	if p == root {
		return true
	}
	if !strings.HasSuffix(root, string(filepath.Separator)) {
		root += string(filepath.Separator)
	}
	return strings.HasPrefix(p, root)
}

// fleetSchedPlacement is where rebuildLines draws each job.
type fleetSchedPlacement struct {
	// keys is the block key of each group index that hosts a block: the
	// first group of each distinct resolved project dir.
	keys map[int]string
	// byGroup is the jobs of each hosting group, in display order.
	byGroup map[int][]cronedit.Status
	// other is the jobs no group claims.
	other []cronedit.Status
}

// placeSchedules assigns the loaded jobs to groups (nothing before the
// first load).
func (m *FleetModel) placeSchedules() fleetSchedPlacement {
	var p fleetSchedPlacement
	if m.schedDirs == nil {
		m.schedDirs = newFleetDirCache()
	}
	p.keys = m.schedGroupKeys()
	if !m.sched.loaded {
		return p
	}
	p.byGroup = make(map[int][]cronedit.Status)
	for _, st := range m.sched.ov.Jobs {
		dir := m.schedDirs.resolve(st.Job.Dir)
		best, bestLen := -1, -1
		for i := range m.groups {
			key, ok := p.keys[i]
			if !ok || dir == "" || !pathWithin(dir, key) {
				continue
			}
			if len(key) > bestLen { // strictly longer: the first group wins ties
				best, bestLen = i, len(key)
			}
		}
		if best < 0 {
			p.other = append(p.other, st)
			continue
		}
		p.byGroup[best] = append(p.byGroup[best], st)
	}
	return p
}

// schedGroupKeys maps each group index that hosts a schedules block to its
// block key. Groups without a project dir host none; a group sharing its
// resolved project dir with an earlier group hosts none either.
func (m FleetModel) schedGroupKeys() map[int]string {
	keys := make(map[int]string)
	seen := make(map[string]bool)
	for i, g := range m.groups {
		if g.ProjectDir == "" {
			continue
		}
		key := m.schedDirs.resolve(g.ProjectDir)
		if seen[key] {
			continue
		}
		seen[key] = true
		keys[i] = key
	}
	return keys
}

// fleetSchedCounts tallies a block's jobs by row glyph (see schedGlyph).
type fleetSchedCounts struct {
	total, running, paused, failed int
}

func tallySched(jobs []cronedit.Status) fleetSchedCounts {
	c := fleetSchedCounts{total: len(jobs)}
	for _, st := range jobs {
		switch glyph, _ := schedGlyph(st); glyph {
		case fleetSchedGlyphRunning:
			c.running++
		case fleetSchedGlyphPaused:
			c.paused++
		case fleetSchedGlyphFailed:
			c.failed++
		}
	}
	return c
}

// isSchedCollapsed reports whether the block key is collapsed: only the
// bottom block collapses, and it does by default.
func (m FleetModel) isSchedCollapsed(key string) bool {
	return key == fleetSchedOtherKey && !m.schedOtherOpen
}

// fleetGroupTab is the tab state of a group header with jobs.
type fleetGroupTab struct {
	key      string // the project's block key
	sessions int    // agent rows (shells excluded)
	jobs     int
	sched    bool // the Schedules tab is selected
}

// buildGroupTab records g's tab info for the header renderer (tabbed is false
// for a group with no jobs or no block key: no tabs, header unchanged).
func (m *FleetModel) buildGroupTab(g fleet.Group, key string, jobs int) (fleetGroupTab, bool) {
	if key == "" || jobs == 0 {
		return fleetGroupTab{}, false
	}
	sessions := 0
	for _, r := range g.Rows {
		if !r.Shell {
			sessions++
		}
	}
	t := fleetGroupTab{key: key, sessions: sessions, jobs: jobs, sched: m.schedTabs[key]}
	if m.groupTabs == nil {
		m.groupTabs = make(map[string]fleetGroupTab)
	}
	m.groupTabs[g.Session] = t
	return t, true
}

// groupTab is g's header tabs, when it has any.
func (m FleetModel) groupTab(g fleet.Group) (fleetGroupTab, bool) {
	t, ok := m.groupTabs[g.Session]
	return t, ok
}

// setSchedTab selects the Schedules (true) or Sessions tab of the block key.
func (m *FleetModel) setSchedTab(key string, sched bool) {
	if key == "" {
		return
	}
	if sched {
		if m.schedTabs == nil {
			m.schedTabs = make(map[string]bool)
		}
		m.schedTabs[key] = true
		return
	}
	delete(m.schedTabs, key)
}

// tabGroupOfLine is the tabbed group owning line idx (its header, an agent
// row or a job row), or "" and false.
func (m FleetModel) tabGroupOfLine(idx int) (fleet.Group, fleetGroupTab, bool) {
	if idx < 0 || idx >= len(m.lines) {
		return fleet.Group{}, fleetGroupTab{}, false
	}
	l := m.lines[idx]
	session := ""
	switch l.kind {
	case fleetLineHeader, fleetLinePlaceholder, fleetLineSchedule, fleetLineSubheader:
		session = l.group.Session
	case fleetLineRow:
		session = l.row.Session
	}
	if session == "" {
		return fleet.Group{}, fleetGroupTab{}, false
	}
	for _, g := range m.groups {
		if g.Session == session {
			t, ok := m.groupTab(g)
			return g, t, ok
		}
	}
	return fleet.Group{}, fleetGroupTab{}, false
}

// toggleGroupTab is fleetSchedulesKey: flip the Sessions / Schedules tab of
// the group the cursor is in.
func (m *FleetModel) toggleGroupTab() tea.Cmd {
	idx := m.splitPane.Cursor()
	g, t, ok := m.tabGroupOfLine(idx)
	if !ok {
		if idx >= 0 && idx < len(m.lines) && m.lines[idx].kind == fleetLineSchedHead {
			return nil
		}
		m.jumpErr = ""
		m.setSuccess("no schedules in this project · " + fleetCronKey + " adds one")
		return nil
	}
	return m.switchTab(g, t, !t.sched, m.lines[idx].kind != fleetLineHeader)
}

// switchTab selects a tab of g. The cursor lands on the header, or with
// firstRow on the first line of the new tab.
func (m *FleetModel) switchTab(g fleet.Group, t fleetGroupTab, sched, firstRow bool) tea.Cmd {
	if sched == t.sched {
		return nil
	}
	m.jumpErr = ""
	m.pendingFocus = nil
	m.schedConfirm = nil
	m.setSchedTab(t.key, sched)
	// Park the selection on the header so the rebuild never leaves it on a
	// line the old tab hid, then pick the final line.
	m.selSession, m.selWindowID, m.selIsHeader, m.selSchedule = g.Session, "", true, ""
	m.relayout()
	if firstRow {
		start := -1
		for i, l := range m.lines {
			if l.kind == fleetLineHeader && l.group.Session == g.Session {
				start = i
				break
			}
		}
		for i := start + 1; start >= 0 && i < len(m.lines); i++ {
			l := m.lines[i]
			if l.kind == fleetLineHeader || l.kind == fleetLineDivider {
				break
			}
			if (l.kind == fleetLineRow || l.kind == fleetLineSchedule) && !l.schedNew {
				m.selectLine(i)
				break
			}
		}
	}
	return tea.Batch(m.saveViewCmd(), m.maybeShowCmd())
}

// fleetSchedHider is what hides a schedule line: a collapsed block's header
// (head, its selection key) or a folded group (session).
type fleetSchedHider struct {
	head    string
	session string
}

// hideSched records that the lines of jobs (and the block's "+ new" line)
// are hidden by h.
func (m *FleetModel) hideSched(blockKey string, jobs []cronedit.Status, h fleetSchedHider) {
	if m.schedHidden == nil {
		m.schedHidden = make(map[string]fleetSchedHider)
	}
	for _, st := range jobs {
		m.schedHidden[st.Name] = h
	}
	if blockKey != "" && blockKey != fleetSchedOtherKey {
		m.schedHidden[schedNewSel(blockKey)] = h
	}
	if h.head == "" && blockKey != "" {
		m.schedHidden[fleetSchedHeadSel+blockKey] = h
	}
}

// noteHiddenSched records the schedule lines a folded group, or its
// Sessions tab, hides: the cursor falls back to the group header.
func (m *FleetModel) noteHiddenSched(g fleet.Group, key string, jobs []cronedit.Status) {
	if key == "" {
		return
	}
	m.hideSched(key, jobs, fleetSchedHider{session: g.Session})
}

// schedHiddenLine is the line that hides the schedule line sel (its
// collapsed block's header, or its folded group's header).
func (m FleetModel) schedHiddenLine(sel string) (int, bool) {
	h, ok := m.schedHidden[sel]
	if !ok {
		return 0, false
	}
	for i, l := range m.lines {
		switch {
		case h.head != "" && l.kind == fleetLineSchedHead && schedLineKey(l) == h.head:
			return i, true
		case h.session != "" && l.kind == fleetLineHeader && l.group.Session == h.session:
			return i, true
		}
	}
	return 0, false
}

// schedNewSel is the selection key of a project block's "+ new" line.
func schedNewSel(blockKey string) string {
	return fleetSchedNewSel + ":" + blockKey
}

// schedGroupLines is group g's Schedules tab body (key is its block key, ""
// when g hosts none): one row per job and a "+ new schedule" line (not while
// searching, when it is no match).
func (m *FleetModel) schedGroupLines(g fleet.Group, key string, jobs []cronedit.Status) []fleetLine {
	if !m.sched.loaded || key == "" || len(jobs) == 0 {
		return nil
	}
	var lines []fleetLine
	for _, st := range jobs {
		lines = append(lines, fleetLine{kind: fleetLineSchedule, group: g, schedBlock: key, sched: st})
	}
	if m.searching() {
		return lines
	}
	return append(lines, fleetLine{kind: fleetLineSchedule, group: g, schedBlock: key, schedDir: g.ProjectDir, schedNew: true})
}

// schedBottomLines is the block at the bottom of the list: the jobs no group
// claims under a "Schedules · other projects (N)" header (collapsed by
// default, one line), with the global warnings (a load error, scheduler not
// installed, cron off) under it when expanded and a ⚠ on the header when
// collapsed. With no other-project jobs the warnings are one dim line, only
// if any job exists anywhere. With no jobs at all nothing is drawn.
func (m *FleetModel) schedBottomLines(p fleetSchedPlacement) []fleetLine {
	searching := m.searching()
	if searching {
		// Only matching jobs; no warnings while searching.
		if !m.sched.loaded {
			return nil
		}
		p.other = m.searchJobs(p.other)
		if len(p.other) == 0 {
			return nil
		}
	}
	label := func(text string, warn bool) fleetLine {
		return fleetLine{kind: fleetLineSchedLabel, label: text, schedWarn: warn}
	}
	if !m.sched.loaded {
		if m.sched.err != "" {
			return []fleetLine{label("Schedules · "+m.sched.err, true)}
		}
		return nil
	}
	ov := m.sched.ov
	var warns []string
	if !searching {
		if m.sched.err != "" {
			warns = append(warns, m.sched.err)
		}
		if !ov.SchedulerInstalled {
			warns = append(warns, "scheduler not installed: run agentctl cron install")
		}
		if !ov.Enabled {
			warns = append(warns, "cron is off (cron.enabled: false): nothing fires")
		}
	}
	if len(p.other) == 0 {
		if len(ov.Jobs) == 0 || len(warns) == 0 {
			return nil
		}
		return []fleetLine{label("Schedules · "+strings.Join(warns, " · "), false)}
	}
	text := fmt.Sprintf("Schedules · other projects (%d)", len(p.other))
	head := fleetLine{
		kind: fleetLineSchedHead, schedBlock: fleetSchedOtherKey, label: text,
		collapsed: m.isSchedCollapsed(fleetSchedOtherKey) && !searching, schedSum: tallySched(p.other),
	}
	if head.collapsed {
		if len(warns) > 0 {
			head.label += " ⚠"
		}
		m.hideSched(fleetSchedOtherKey, p.other, fleetSchedHider{head: schedLineKey(head)})
		return []fleetLine{head}
	}
	lines := []fleetLine{head}
	for _, w := range warns {
		lines = append(lines, label(w, true))
	}
	for _, st := range p.other {
		lines = append(lines, fleetLine{kind: fleetLineSchedule, schedBlock: fleetSchedOtherKey, sched: st})
	}
	return lines
}

// selectedSchedHead is the selected block header line, or nil.
func (m FleetModel) selectedSchedHead() *fleetLine {
	idx := m.splitPane.Cursor()
	if idx < 0 || idx >= len(m.lines) || m.lines[idx].kind != fleetLineSchedHead {
		return nil
	}
	l := m.lines[idx]
	return &l
}

// toggleSchedCollapseAt collapses or expands the block whose header is line
// idx. The cursor stays on the header (selSchedule is its key).
func (m *FleetModel) toggleSchedCollapseAt(idx int) tea.Cmd {
	if idx < 0 || idx >= len(m.lines) || m.lines[idx].kind != fleetLineSchedHead {
		return nil
	}
	m.schedOtherOpen = !m.schedOtherOpen
	m.relayout()
	return m.saveViewCmd()
}

// schedHeadKey runs key on a selected block header; handled is false for
// keys that keep their usual meaning.
func (m *FleetModel) schedHeadKey(l fleetLine, key string) (tea.Cmd, bool) {
	switch key {
	case "enter", "space", "z":
		return m.toggleSchedCollapseAt(m.splitPane.Cursor()), true
	case "n":
		dir := ""
		if l.schedBlock != fleetSchedOtherKey {
			dir = l.group.ProjectDir
		}
		return m.openSchedForm(schedFormNew, nil, dir), true
	case "tab", "e", "g", "d", "x":
		return nil, true
	}
	return nil, false
}

// schedHeadHints are the footer hints on a block header.
func (m FleetModel) schedHeadHints(l fleetLine) []fleetHint {
	act := "collapse"
	if l.collapsed {
		act = "expand"
	}
	return []fleetHint{{"j/k", "move"}, {"z", act}, {"n", "new schedule"}, {"?", "more"}}
}

// renderSchedHead draws the bottom block's header: the ▾/▸ marker and the
// section label. A collapsed header adds the summary of what it hides.
func (m FleetModel) renderSchedHead(l fleetLine, selected bool, cw int) string {
	cursor := "  "
	if selected {
		cursor = cursorActiveStyle.Render("▸") + " "
	}
	left := cursor + collapseMarker(l.collapsed, false) + " " + fleetDimStyle.Render(fleetSchedGlyphOn+" "+l.label)
	if l.collapsed {
		room := cw - fleetSidebarRightGutter - lipgloss.Width(left) - 2
		if sum := schedSummary(l.schedSum, room); sum != "" {
			left += "  " + sum
		}
	}
	return fleetFitLine(left, selected, cw)
}

// schedSummary renders a collapsed block's summary in at most w columns: a
// dim "+N" (rows hidden), then the ● running, ! failed and ⏸ paused counts
// in the row glyphs' styles. As w shrinks it drops the paused count, then
// the "+N", keeping failed and running longest.
func schedSummary(c fleetSchedCounts, w int) string {
	if c.total == 0 || w <= 0 {
		return ""
	}
	type part struct {
		text string
		st   lipgloss.Style
	}
	total := []part{{fmt.Sprintf("+%d", c.total), fleetDimStyle}}
	var urgent, paused []part
	if c.running > 0 {
		urgent = append(urgent, part{fmt.Sprintf("%s%d", fleetSchedGlyphRunning, c.running), fleetWorkingStyle})
	}
	if c.failed > 0 {
		urgent = append(urgent, part{fmt.Sprintf("%s%d", fleetSchedGlyphFailed, c.failed), fleetErrStyle})
	}
	if c.paused > 0 {
		paused = append(paused, part{fmt.Sprintf("%s%d", fleetSchedGlyphPaused, c.paused), fleetDimStyle})
	}
	cat := func(groups ...[]part) []part {
		var out []part
		for _, g := range groups {
			out = append(out, g...)
		}
		return out
	}
	render := func(ps []part) (string, int) {
		texts := make([]string, len(ps))
		plain := make([]string, len(ps))
		for i, p := range ps {
			texts[i], plain[i] = p.st.Render(p.text), p.text
		}
		return strings.Join(texts, " "), lipgloss.Width(strings.Join(plain, " "))
	}
	var last []part
	for _, v := range [][]part{cat(total, urgent, paused), cat(total, urgent), urgent} {
		if len(v) == 0 {
			continue
		}
		if s, sw := render(v); sw <= w {
			return s
		}
		last = v
	}
	s, _ := render(last)
	return truncateToWidth(s, w)
}

// liveSchedTabs is the sorted set of projects on their Schedules tab; with
// prune, keys of projects no group hosts any more are dropped.
func (m FleetModel) liveSchedTabs(prune bool) []string {
	live := map[string]bool{}
	if prune {
		for _, key := range m.schedGroupKeys() {
			live[key] = true
		}
	}
	var out []string
	for key, ok := range m.schedTabs {
		if ok && (!prune || live[key]) {
			out = append(out, key)
		}
	}
	slices.Sort(out)
	return out
}
