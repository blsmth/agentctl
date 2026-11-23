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

// Schedules blocks: while the Schedules section is shown (fleetSchedulesKey)
// each cron job is listed under the project group it runs in, after the
// group's agent rows, as a "schedules (N)" block. A job belongs to the first
// group whose project dir is the job's dir or contains it (a subdir, or a
// worktree under <project>/.worktrees/), longest project dir first, with
// both sides cleaned and symlink-resolved. Jobs no live group claims go in a
// trailing "Schedules · other projects (N)" block at the bottom of the list,
// which also carries the section's global warnings.
//
// Each block header can be collapsed (`z` or ⏎ on it, or a click): the
// header then shows the job count and a summary of the hidden rows. The
// collapsed set is keyed by block key (the resolved project dir, or
// fleetSchedOtherKey) and persisted in hubViewState.SchedCollapsed.

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

// placeSchedules assigns the loaded jobs to groups (nothing while the
// section is hidden or not loaded yet).
func (m *FleetModel) placeSchedules() fleetSchedPlacement {
	var p fleetSchedPlacement
	if !m.showSchedules {
		return p
	}
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

// isSchedCollapsed reports whether the block key is collapsed.
func (m FleetModel) isSchedCollapsed(key string) bool {
	return key != "" && m.schedCollapsed[key]
}

// setSchedCollapsed collapses or expands the block key.
func (m *FleetModel) setSchedCollapsed(key string, collapse bool) {
	if key == "" {
		return
	}
	if collapse {
		if m.schedCollapsed == nil {
			m.schedCollapsed = make(map[string]bool)
		}
		m.schedCollapsed[key] = true
		return
	}
	delete(m.schedCollapsed, key)
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

// noteFoldedSched records the schedule lines a folded group hides.
func (m *FleetModel) noteFoldedSched(g fleet.Group, key string, jobs []cronedit.Status) {
	if !m.showSchedules || key == "" {
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

// schedGroupLines is group g's schedules block (key is its block key, ""
// when g hosts none): the "schedules (N)" header, then unless it is
// collapsed one row per job and a "+ new schedule" line. A project with no
// jobs shows only the "+ new schedule" line.
func (m *FleetModel) schedGroupLines(g fleet.Group, key string, jobs []cronedit.Status) []fleetLine {
	if !m.showSchedules || !m.sched.loaded || key == "" {
		return nil
	}
	searching := m.searching()
	newLine := fleetLine{kind: fleetLineSchedule, group: g, schedBlock: key, schedDir: g.ProjectDir, schedNew: true}
	if len(jobs) == 0 {
		return []fleetLine{newLine}
	}
	head := fleetLine{
		kind: fleetLineSchedHead, group: g, schedBlock: key,
		label: fmt.Sprintf("schedules (%d)", len(jobs)), collapsed: m.isSchedCollapsed(key) && !searching, schedSum: tallySched(jobs),
	}
	if head.collapsed {
		m.hideSched(key, jobs, fleetSchedHider{head: schedLineKey(head)})
		return []fleetLine{head}
	}
	lines := []fleetLine{head}
	for _, st := range jobs {
		lines = append(lines, fleetLine{kind: fleetLineSchedule, group: g, schedBlock: key, sched: st})
	}
	if searching {
		return lines // "+ new schedule" is not a match
	}
	return append(lines, newLine)
}

// schedBottomLines is the block at the bottom of the list: the jobs no group
// claims under their own header, and the section's global warnings (loading,
// a load error, scheduler not installed, cron off), each drawn once. With no
// jobs and no project to put a block under, it keeps the "none yet" label
// and a "+ new schedule" line.
func (m *FleetModel) schedBottomLines(p fleetSchedPlacement) []fleetLine {
	if !m.showSchedules {
		return nil
	}
	searching := m.searching()
	if searching {
		// Only matching jobs; no warnings or "+ new" while searching.
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
	switch {
	case !m.sched.loaded && m.sched.err == "":
		return []fleetLine{label("Schedules · loading…", false)}
	case m.sched.err != "" && !m.sched.loaded:
		return []fleetLine{label("Schedules · "+m.sched.err, true)}
	}
	ov := m.sched.ov
	var warns []fleetLine
	if !searching {
		if m.sched.err != "" {
			warns = append(warns, label(m.sched.err, true))
		}
		if !ov.SchedulerInstalled {
			warns = append(warns, label("scheduler not installed: run agentctl cron install", true))
		}
		if !ov.Enabled {
			warns = append(warns, label("cron is off (cron.enabled: false): nothing fires", true))
		}
	}
	hosts := len(p.keys) > 0
	switch {
	case len(p.other) > 0:
		text := fmt.Sprintf("Schedules · other projects (%d)", len(p.other))
		if !hosts {
			text = fmt.Sprintf("Schedules (%d)", len(p.other))
		}
		head := fleetLine{
			kind: fleetLineSchedHead, schedBlock: fleetSchedOtherKey, label: text,
			collapsed: m.isSchedCollapsed(fleetSchedOtherKey) && !searching, schedSum: tallySched(p.other),
		}
		lines := append([]fleetLine{head}, warns...)
		if head.collapsed {
			m.hideSched(fleetSchedOtherKey, p.other, fleetSchedHider{head: schedLineKey(head)})
			return lines
		}
		for _, st := range p.other {
			lines = append(lines, fleetLine{kind: fleetLineSchedule, schedBlock: fleetSchedOtherKey, sched: st})
		}
		return lines
	case len(ov.Jobs) == 0 && !hosts:
		lines := append([]fleetLine{label("Schedules · none yet", false)}, warns...)
		return append(lines, fleetLine{kind: fleetLineSchedule, schedNew: true})
	}
	return warns
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
	key := m.lines[idx].schedBlock
	m.setSchedCollapsed(key, !m.isSchedCollapsed(key))
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
	return []fleetHint{{"j/k", "move"}, {"z", act}, {"n", "new schedule"}, {fleetSchedulesKey, "hide"}, {"?", "more"}}
}

// renderSchedHead draws a block header: in a group, the project bar, the
// ▾/▸ marker and "schedules (N)" aligned under the agent names; at the
// bottom, the section label. A collapsed header adds the summary of what it
// hides.
func (m FleetModel) renderSchedHead(l fleetLine, selected bool, cw int) string {
	cursor := "  "
	if selected {
		cursor = cursorActiveStyle.Render("▸") + " "
	}
	var left string
	if l.group.Session != "" {
		left = fleetProjectBar(l.group.Project) + cursor + collapseMarker(l.collapsed, false) + " " + fleetDimStyle.Render(l.label)
	} else {
		left = cursor + collapseMarker(l.collapsed, false) + " " + fleetDimStyle.Render(fleetSchedGlyphOn+" "+l.label)
	}
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

// liveSchedCollapsed is the sorted collapsed block set; with prune, keys of
// projects no group hosts any more are dropped (the bottom block's key is
// always kept).
func (m FleetModel) liveSchedCollapsed(prune bool) []string {
	live := map[string]bool{fleetSchedOtherKey: true}
	if prune {
		for _, key := range m.schedGroupKeys() {
			live[key] = true
		}
	}
	var out []string
	for key, ok := range m.schedCollapsed {
		if ok && (!prune || live[key]) {
			out = append(out, key)
		}
	}
	slices.Sort(out)
	return out
}
