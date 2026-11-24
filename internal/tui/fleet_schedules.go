package tui

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/cron"
	"github.com/blsmth/agentctl/internal/cronedit"
	"github.com/blsmth/agentctl/internal/dispatchplan"
	"github.com/blsmth/agentctl/internal/fleet"
)

// The hub's schedules: the `agentctl cron` jobs, each listed on the
// Schedules tab of the project group it runs in (or in a collapsed block at
// the bottom for projects with no session; see fleet_schedgroup.go), read
// through the shared editing core (internal/cronedit) on hub start and on
// the poll tick. Selecting
// a job shows its card as a drawer sliding in from the right edge of the
// list pane; the editing form (see fleet_schedform.go) is the same drawer, so
// the list stays visible to its left. The live viewer pane is never touched.
//
// Keys on a schedule row are context-sensitive (scheduleKey): ⏎ card, e
// edit, space pause/resume, g run now, d duplicate, x delete, n new. Every
// other line keeps its usual bindings. Writes and run-now run as tea.Cmds,
// so a save waiting on the tick lock never freezes the hub.

// fleetSchedulesKey flips the Sessions / Schedules tab of the cursor's group.
const fleetSchedulesKey = "T"

// fleetSchedNewSel is selSchedule for the "+ new schedule" line.
const fleetSchedNewSel = "\x00new"

// fleetSchedReload is how often the section re-reads the jobs and the run
// history on the poll tick (runs.ndjson can be large).
const fleetSchedReload = 5 * time.Second

// fleetSchedPaneMin is the narrowest the card/form drawer is drawn.
const fleetSchedPaneMin = 34

// Row glyphs: the same as the phone.
const (
	fleetSchedGlyphOn      = "⏲"
	fleetSchedGlyphPaused  = "⏸"
	fleetSchedGlyphRunning = "●"
	fleetSchedGlyphFailed  = "!"
)

// Seams so tests never touch the real jobs file, spawn `agentctl cron run`,
// or open an editor.
var (
	// fleetSchedStore is the editing core; validate is the injected agent
	// check (dispatch.ValidateAgentName in the hub).
	fleetSchedStore = func(validate func(name, dir string) error) cronedit.Store {
		return cronedit.Default(validate)
	}
	// fleetSchedRun fires a job now (plain `cron run`, never --force).
	fleetSchedRun cronedit.RunFunc = cronedit.RunSubprocess
	// fleetSchedPermMode is the permission mode a fire of agent launches
	// with, for the review step.
	fleetSchedPermMode = func(agent string) string {
		return dispatchplan.PermissionMode(true, config.Load().Agents[agent].PermissionMode, "")
	}
)

// WithScheduleAgentCheck sets the agent check the Schedules form validates
// with (dispatch.ValidateAgentName; internal/tui cannot import it).
func (m FleetModel) WithScheduleAgentCheck(fn func(name, dir string) error) FleetModel {
	m.schedAgentCheck = fn
	return m
}

// WithScheduleAgents sets the agent lister the Schedules form's agent
// selector offers (dispatch.AgentNames; internal/tui cannot import it).
func (m FleetModel) WithScheduleAgents(fn func(dir string) []string) FleetModel {
	m.schedAgentNames = fn
	return m
}

// fleetSchedState is the section's data.
type fleetSchedState struct {
	ov       cronedit.Overview
	loaded   bool
	err      string
	loading  bool
	lastLoad time.Time
	// detail is the expanded card (⏎) of detailName.
	detail     *cronedit.Detail
	detailName string
	// busy is the job a write or run is in flight for ("" = none).
	busy string
}

// fleetSchedConfirm is an armed y/n: run now or delete.
type fleetSchedConfirm struct {
	kind string // "run" or "delete"
	name string
	dir  string
}

// Messages.
type (
	fleetSchedLoadedMsg struct {
		ov  cronedit.Overview
		err error
	}
	fleetSchedDetailMsg struct {
		name string
		d    cronedit.Detail
		err  error
	}
	// fleetSchedWroteMsg reports a pause, resume or delete.
	fleetSchedWroteMsg struct {
		action, name string
		job          cron.Job
		err          error
	}
	fleetSchedRanMsg struct {
		name  string
		entry cron.Entry
		err   error
	}
)

// schedStore is the core with the hub's seams: the injected agent check
// and the running windows from the last fleet load.
func (m FleetModel) schedStore() cronedit.Store {
	st := fleetSchedStore(m.schedAgentCheck)
	var wins []cron.Window
	for _, g := range m.groups {
		wins = append(wins, cronedit.WindowsFromRows(g.Rows)...)
	}
	st.Windows = func([]cron.Job) ([]cron.Window, error) { return wins, nil }
	return st
}

// schedLoadCmd reads the jobs in the background; nil while a read is in
// flight.
func (m *FleetModel) schedLoadCmd() tea.Cmd {
	if m.sched.loading {
		return nil
	}
	m.sched.loading = true
	m.sched.lastLoad = fleetNow()
	st := m.schedStore()
	return func() tea.Msg {
		ov, err := st.Overview()
		if err == nil {
			cronedit.SortForDisplay(ov.Jobs)
		}
		return fleetSchedLoadedMsg{ov: ov, err: err}
	}
}

// schedTickCmd is the poll tick's reload, at most every fleetSchedReload.
func (m *FleetModel) schedTickCmd() tea.Cmd {
	if fleetNow().Sub(m.sched.lastLoad) < fleetSchedReload {
		return nil
	}
	return m.schedLoadCmd()
}

func (m *FleetModel) applySchedLoaded(msg fleetSchedLoadedMsg) {
	m.sched.loading = false
	if msg.err != nil {
		// A failed read keeps the last good list (if any) under the error.
		m.sched.err = msg.err.Error()
	} else {
		m.sched.err = ""
		m.sched.ov = msg.ov
		m.searchReset()
		m.sched.loaded = true
		m.sched.detailName = "" // re-read the card with the list
	}
	m.relayout()
}

// schedLineKey is the selSchedule value of a schedule line.
// A block header is fleetSchedHeadSel plus its block key; a project
// block's "+ new schedule" line is schedNewSel of its block key, the bottom
// one plain fleetSchedNewSel; a job row is the job's name.
func schedLineKey(l fleetLine) string {
	switch {
	case l.kind == fleetLineSchedHead:
		return fleetSchedHeadSel + l.schedBlock
	case l.schedNew && l.schedBlock != "" && l.schedBlock != fleetSchedOtherKey:
		return schedNewSel(l.schedBlock)
	case l.schedNew:
		return fleetSchedNewSel
	}
	return l.sched.Name
}

// isSchedJobSel reports whether a selSchedule key names a job (not a "+ new"
// line or a block header).
func isSchedJobSel(sel string) bool {
	return sel != "" && !strings.HasPrefix(sel, fleetSchedNewSel) && !strings.HasPrefix(sel, fleetSchedHeadSel)
}

// selectedSchedLine is the selected schedule line, or nil.
func (m *FleetModel) selectedSchedLine() *fleetLine {
	idx := m.splitPane.Cursor()
	if idx < 0 || idx >= len(m.lines) || m.lines[idx].kind != fleetLineSchedule {
		return nil
	}
	l := m.lines[idx]
	return &l
}

// ── Drawer ───────────────────────────────────────────────────────────────────

// schedPaneActive reports whether the card or the form is showing. A block
// header or a "+ new schedule" line selected on its own shows no drawer: the
// new line opens the form straight from Enter/n or a click.
func (m FleetModel) schedPaneActive() bool {
	return m.schedCardShowing() || m.schedForm != nil
}

// schedCardShowing reports whether the selected job's card is the open drawer.
func (m FleetModel) schedCardShowing() bool {
	return isSchedJobSel(m.selSchedule) && m.selSchedule != m.schedCardHidden
}

// fleetViewerMu serializes the hub's viewer pane changes (every show in
// liveShowFor and every hide, through hideViewerLocked): two shows racing
// to create a missing viewer pane would each kill the other's viewer
// session.
var fleetViewerMu sync.Mutex

// hideViewerLocked runs a viewer hide under fleetViewerMu.
func hideViewerLocked(hide func() error) {
	fleetViewerMu.Lock()
	defer fleetViewerMu.Unlock()
	_ = hide()
}

// ── Keys ─────────────────────────────────────────────────────────────────────

// scheduleKey runs key on a selected schedule line. handled is false for
// keys that keep their usual meaning.
func (m *FleetModel) scheduleKey(key string) (cmd tea.Cmd, handled bool) {
	if h := m.selectedSchedHead(); h != nil {
		return m.schedHeadKey(*h, key)
	}
	l := m.selectedSchedLine()
	if l == nil {
		return nil, false
	}
	if l.schedNew {
		switch key {
		case "enter", "n", "e":
			return m.openSchedForm(schedFormNew, nil, l.schedDir), true
		case "tab", "space", "g", "d", "x":
			return nil, true
		}
		return nil, false
	}
	st := l.sched
	switch key {
	case "esc":
		// The card is the innermost layer here: esc closes it back to the
		// list instead of falling through to the hub's leave binding.
		if m.schedCardShowing() {
			m.schedCardHidden = m.selSchedule
			return nil, true
		}
		return nil, false
	case "enter", "e":
		return m.openSchedForm(schedFormEdit, &st, ""), true
	case "d":
		return m.openSchedForm(schedFormDup, &st, ""), true
	case "n":
		return m.openSchedForm(schedFormNew, nil, st.Job.Dir), true
	case "space":
		return m.schedSetEnabled(st), true
	case "g":
		m.armSchedConfirm("run", st)
		return nil, true
	case "x":
		m.armSchedConfirm("delete", st)
		return nil, true
	case "tab":
		return nil, true // nothing live to focus
	}
	return nil, false
}

// handleSchedConfirmKey answers an armed y/n. Any other key cancels; keys
// other than n/esc then run as usual.
func (m *FleetModel) handleSchedConfirmKey(key string) (tea.Cmd, bool) {
	c := m.schedConfirm
	m.schedConfirm = nil
	switch key {
	case "y", "Y":
		if c.kind == "run" {
			return m.schedRunNow(c.name), true
		}
		return m.schedDelete(c.name), true
	case "n", "N", "esc":
		return nil, true
	}
	return nil, false
}

func (m *FleetModel) armSchedConfirm(kind string, st cronedit.Status) {
	if kind == "run" && st.Invalid != "" {
		m.jumpErr = "cannot run " + st.Name + ": " + st.Invalid
		return
	}
	m.schedConfirm = &fleetSchedConfirm{kind: kind, name: st.Name, dir: st.Job.Dir}
}

// schedConfirmPrompt is the footer line of an armed y/n.
func (m FleetModel) schedConfirmPrompt() string {
	c := m.schedConfirm
	if c.kind == "run" {
		return fmt.Sprintf("run %s now in %s? y/n", c.name, tildify(c.dir))
	}
	return fmt.Sprintf("delete %s? its run history is kept and a running window is left alone. y/n", c.name)
}

// schedBusy refuses a second write while one is in flight.
func (m *FleetModel) schedBusy() bool {
	if m.sched.busy == "" {
		return false
	}
	m.jumpErr = "a schedule change for " + m.sched.busy + " is still running"
	return true
}

// schedSetEnabled is space: pause or resume without confirmation.
func (m *FleetModel) schedSetEnabled(st cronedit.Status) tea.Cmd {
	if m.schedBusy() {
		return nil
	}
	enable := !st.Enabled
	action := "pause"
	if enable {
		action = "resume"
	}
	m.sched.busy = st.Name
	m.setProgress(action + " " + st.Name + "…")
	store, name := m.schedStore(), st.Name
	return func() tea.Msg {
		job, err := store.SetEnabled(name, enable)
		return fleetSchedWroteMsg{action: action, name: name, job: job, err: err}
	}
}

// schedDelete removes a job (after y).
func (m *FleetModel) schedDelete(name string) tea.Cmd {
	if m.schedBusy() {
		return nil
	}
	m.sched.busy = name
	m.setProgress("deleting " + name + "…")
	store := m.schedStore()
	return func() tea.Msg {
		return fleetSchedWroteMsg{action: "delete", name: name, err: store.Delete(name)}
	}
}

// schedRunNow fires a job through the RunFunc seam (plain `cron run`: the
// overlap, max_concurrent and usage rules apply, never --force).
func (m *FleetModel) schedRunNow(name string) tea.Cmd {
	if m.schedBusy() {
		return nil
	}
	m.sched.busy = name
	m.setProgress("running " + name + "…")
	run := fleetSchedRun
	return func() tea.Msg {
		e, err := run(context.Background(), name)
		return fleetSchedRanMsg{name: name, entry: e, err: err}
	}
}

func (m *FleetModel) applySchedWrote(msg fleetSchedWroteMsg) tea.Cmd {
	m.sched.busy = ""
	if msg.err != nil {
		m.clearInfo()
		m.jumpErr = msg.action + " " + msg.name + ": " + msg.err.Error()
		return m.schedLoadCmd()
	}
	switch msg.action {
	case "pause":
		m.setSuccess("paused " + msg.name + " · space resumes")
	case "resume":
		text := "resumed " + msg.name
		if next, err := cronedit.NextFire(msg.job, fleetNow()); err == nil && !next.IsZero() {
			text += " · next " + next.Format("Mon 15:04")
		}
		m.setSuccess(text)
	case "delete":
		m.setSuccess("deleted " + msg.name + " (run history kept)")
	}
	m.applySchedJob(msg)
	return m.schedLoadCmd()
}

// applySchedJob reflects a write in the loaded list at once, before the
// reload lands.
func (m *FleetModel) applySchedJob(msg fleetSchedWroteMsg) {
	jobs := m.sched.ov.Jobs
	for i := range jobs {
		if jobs[i].Name != msg.name {
			continue
		}
		if msg.action == "delete" {
			m.sched.ov.Jobs = append(jobs[:i:i], jobs[i+1:]...)
		} else {
			jobs[i].Enabled = msg.job.IsEnabled()
			jobs[i].Job.Enabled = msg.job.Enabled
			if !jobs[i].Enabled {
				jobs[i].NextFire = nil
			}
		}
		break
	}
	m.relayout()
}

func (m *FleetModel) applySchedRan(msg fleetSchedRanMsg) tea.Cmd {
	m.sched.busy = ""
	switch {
	case msg.err != nil:
		m.clearInfo()
		m.jumpErr = "run " + msg.name + ": " + msg.err.Error()
	case msg.entry.Result == cron.ResultFired:
		text := msg.name + " fired"
		if msg.entry.WindowID != "" && msg.entry.Session != "" {
			text += " in " + strings.TrimPrefix(msg.entry.Session, "agentctl-")
		}
		m.setSuccess(text)
	default:
		m.clearInfo()
		m.jumpErr = msg.name + " did not fire: " + runResultText(msg.entry)
	}
	// The poll tick reloads the fleet, which lists a fired window.
	return m.schedLoadCmd()
}

// runResultText is a run entry for people: "skipped: usage (5h 82% ≥ 80%)".
func runResultText(e cron.Entry) string {
	r := e.Result
	if kind, reason, ok := strings.Cut(r, ":"); ok {
		r = kind + ": " + reason
	}
	if e.Note != "" {
		r += " (" + e.Note + ")"
	}
	return r
}

// syncSchedDetail loads the full card (prompt_key text, more runs) of the
// selected job; the card always shows it. Called after every Update. The
// previous detail of the same job stays up while a refresh loads.
func (m *FleetModel) syncSchedDetail() tea.Cmd {
	name := m.selSchedule
	if !isSchedJobSel(name) || m.schedForm != nil || m.sched.detailName == name {
		return nil
	}
	m.sched.detailName = name
	store := m.schedStore()
	return func() tea.Msg {
		d, err := store.Detail(name)
		return fleetSchedDetailMsg{name: name, d: d, err: err}
	}
}

func (m *FleetModel) applySchedDetail(msg fleetSchedDetailMsg) {
	if msg.name != m.sched.detailName {
		return
	}
	if msg.err != nil {
		m.jumpErr = "schedule " + msg.name + ": " + msg.err.Error()
		return
	}
	d := msg.d
	m.sched.detail = &d
}

// schedMenuItems is the row menu for a schedule line.
func schedMenuItems(l fleetLine) []fleetMenuItem {
	if l.schedNew {
		return []fleetMenuItem{{"new schedule", "n"}}
	}
	toggle := "pause"
	if !l.sched.Enabled {
		toggle = "resume"
	}
	return []fleetMenuItem{
		{"edit", "enter"}, {toggle, "space"}, {"run now", "g"},
		{"duplicate", "d"}, {"delete", "x"},
	}
}

// schedHints are the footer hints on a schedule row.
func (m FleetModel) schedHints() []fleetHint {
	if h := m.selectedSchedHead(); h != nil {
		return m.schedHeadHints(*h)
	}
	l := m.selectedSchedLine()
	if l == nil {
		return nil
	}
	if l.schedNew {
		return []fleetHint{{"j/k", "move"}, {"⏎", "new schedule"}, {fleetSchedulesKey, "sessions"}, {"?", "more"}}
	}
	toggle := "pause"
	if !l.sched.Enabled {
		toggle = "resume"
	}
	return []fleetHint{
		{"j/k", "move"}, {"⏎", "edit"}, {"space", toggle}, {"g", "run now"},
		{"d", "duplicate"}, {"x", "delete"}, {"n", "new"}, {fleetSchedulesKey, "sessions"}, {"?", "more"},
	}
}

// ── List rendering ───────────────────────────────────────────────────────────

// renderSchedLabel renders the section label or a warning line.
func renderSchedLabel(l fleetLine, cw int) string {
	if l.schedWarn {
		return fleetFitLine("    "+fleetBlockedStyle.Render("⚠ "+truncateToWidth(l.label, cw-7)), false, cw)
	}
	return fleetFitLine("  "+fleetDimStyle.Render(fleetSchedGlyphOn+" "+l.label), false, cw)
}

// schedGlyph is a row's glyph and style: ! failed last, ● running,
// ⏸ paused, ⏲ otherwise.
func schedGlyph(st cronedit.Status) (string, lipgloss.Style) {
	switch {
	case !st.Enabled:
		return fleetSchedGlyphPaused, fleetDimStyle
	case st.FailedLast || st.Invalid != "":
		return fleetSchedGlyphFailed, fleetErrStyle
	case st.Running:
		return fleetSchedGlyphRunning, fleetWorkingStyle
	}
	return fleetSchedGlyphOn, fleetIdleStyle
}

// schedNextText is the next-fire column: relative under a day, else
// "Mon 07:31"; "paused" for a paused job.
func schedNextText(st cronedit.Status, now time.Time) string {
	switch {
	case !st.Enabled:
		return "paused"
	case st.ScheduleError != "" || st.Invalid != "":
		return "invalid"
	case st.NextFire == nil:
		return "never"
	}
	d := st.NextFire.Sub(now)
	switch {
	case d < time.Minute:
		return "in <1m"
	case d < time.Hour:
		return fmt.Sprintf("in %dm", int(d.Minutes()))
	case d < 24*time.Hour:
		h := int(d.Hours())
		return fmt.Sprintf("in %dh%02dm", h, int(d.Minutes())-60*h)
	}
	return st.NextFire.In(time.Local).Format("Mon 15:04")
}

// schedLastText is the exception column: the last result unless it fired
// ("" when it never ran or fired).
func schedLastText(st cronedit.Status) string {
	if st.Last == nil {
		return ""
	}
	switch r := st.Last.Result; {
	case r == cron.ResultFired:
		return "" // the healthy result is not worth a column
	case cron.IsFailed(r):
		return "failed"
	default:
		return strings.Replace(r, ":", " · ", 1)
	}
}

// schedWords is the schedule in words, or the expression.
func schedWords(st cronedit.Status) string {
	if st.Words != "" {
		return st.Words
	}
	return st.Job.Schedule
}

// projectOf is a job dir's project name (the repo root, not a worktree).
func projectOf(dir string) string {
	if dir == "" {
		return ""
	}
	return filepath.Base(dir)
}

// renderSchedRow renders one job: glyph and name, then the schedule in
// words, the last result and (outside a project block) the project, with
// the next fire right-aligned. Narrow widths drop the project, then the
// last result, then cut the words; the name and next fire always show. In
// a project block the row hangs off the group's project bar, indented under
// the block header.
func (m FleetModel) renderSchedRow(l fleetLine, selected bool, cw int) string {
	cursor := "  "
	if selected {
		cursor = cursorActiveStyle.Render("▸") + " "
	}
	lead := "  " + cursor
	inGroup := l.group.Session != ""
	if inGroup {
		lead = fleetProjectBar(l.group.Project) + cursor + "  "
	}
	if l.schedNew {
		return fleetFitLine(lead+fleetNewHoverStyle.Render("+ new schedule")+fleetDimStyle.Render("  n"), selected, cw)
	}
	st := l.sched
	glyph, gst := schedGlyph(st)
	nameSt := fleetNameDoneStyle
	if !st.Enabled {
		nameSt = fleetDimStyle
	}
	left := lead + gst.Render(glyph) + " " + nameSt.Render(st.Name)
	nextSt := fleetCountStyle
	if st.Enabled && st.Invalid != "" {
		nextSt = fleetErrStyle
	}
	right := nextSt.Render(schedNextText(st, fleetNow()))
	inner := cw - fleetSidebarRightGutter
	room := inner - lipgloss.Width(left) - lipgloss.Width(right) - 2
	if room < 0 {
		// Even the name does not fit: cut it, never the next fire.
		nameW := max(inner-lipgloss.Width(right)-1-lipgloss.Width(lead+glyph+" "), 1)
		left = lead + gst.Render(glyph) + " " + nameSt.Render(truncateToWidth(st.Name, nameW))
		room = inner - lipgloss.Width(left) - lipgloss.Width(right) - 1
	}
	mid := schedRowMiddle(st, room, !inGroup)
	body := left + mid
	gap := inner - lipgloss.Width(body) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return fleetFitLine(body+strings.Repeat(" ", gap)+right, selected, cw)
}

// schedRowMiddle is the part between the name and the next fire, in room
// columns ("" when there is none).
func schedRowMiddle(st cronedit.Status, room int, withProject bool) string {
	const sep = " · "
	words, last := schedWords(st), schedLastText(st)
	project := ""
	if withProject {
		project = projectOf(st.Job.Dir)
	}
	lastSt := fleetDimStyle
	if st.FailedLast {
		lastSt = fleetErrStyle
	} else if st.Last != nil && st.Last.Result != cron.ResultFired {
		lastSt = fleetBlockedStyle
	}
	type seg struct {
		text string
		st   lipgloss.Style
	}
	// Order: words, project, exception. Narrow rows drop the project first,
	// then the exception.
	all := []seg{{words, fleetRecapIdleStyle}, {project, fleetDimStyle}, {last, lastSt}}
	var segs []seg
	for _, s := range all {
		if s.text != "" {
			segs = append(segs, s)
		}
	}
	width := func(ss []seg) int {
		w := 2
		for i, s := range ss {
			if i > 0 {
				w += len(sep)
			}
			w += lipgloss.Width(s.text)
		}
		return w
	}
	for len(segs) > 1 && width(segs) > room {
		drop := len(segs) - 1
		if len(segs) == 3 {
			drop = 1
		}
		segs = slices.Delete(segs, drop, drop+1)
	}
	if len(segs) == 0 || room < 6 {
		return ""
	}
	if width(segs) > room {
		segs[0].text = truncateToWidth(segs[0].text, room-2)
	}
	parts := make([]string, len(segs))
	for i, s := range segs {
		parts[i] = s.st.Render(s.text)
	}
	return "  " + strings.Join(parts, fleetDimStyle.Render(sep))
}

// ── Card ─────────────────────────────────────────────────────────────────────

// schedCardLines renders the selected job's card in w columns: summary,
// next fires, prompt, recent runs and the keys.
func (m FleetModel) schedCardLines(w int) []string {
	l := m.selectedSchedLineValue()
	if l == nil {
		return nil
	}
	if l.schedNew {
		return nil // the new line has no card; it opens the form
	}
	st := l.sched
	var detail *cronedit.Detail
	if m.sched.detail != nil && m.sched.detail.Name == st.Name {
		detail = m.sched.detail
	}
	glyph, gst := schedGlyph(st)
	status := "enabled"
	switch {
	case !st.Enabled:
		status = "paused"
	case st.Invalid != "":
		status = "invalid"
	case st.Running:
		status = "running"
	case st.FailedLast:
		status = "last run failed"
	}
	var out []string
	add := func(s ...string) { out = append(out, s...) }
	add(gst.Render(glyph)+" "+fleetTitleStyle.Render(truncateToWidth(st.Name, w-14))+"  "+gst.Render(status), "")
	words := schedWords(st)
	if st.Words != "" {
		words += fleetDimStyle.Render("  (" + st.Job.Schedule + ")")
	}
	add(wrapText(words, w))
	wt := schedWorktreeOn
	if !st.Job.UseWorktree() {
		wt = schedWorktreeOff
	}
	agent := st.Job.Agent
	if agent == "" && st.Job.Template != "" {
		agent = "template's agent"
		if detail != nil && detail.TemplateResolved != nil && detail.TemplateResolved.Agent != "" {
			agent = detail.TemplateResolved.Agent + " (from template)"
		}
	}
	add(wrapText(fmt.Sprintf("%s in %s · %s", fleetGroupNameStyle.Render(agent), tildify(st.Job.Dir), wt), w))
	if st.Invalid != "" {
		add(fleetErrStyle.Render(wrapText("invalid: "+st.Invalid, w)))
	}
	if adv := advancedSummary(st.Job); adv != "" {
		add(fleetDimStyle.Render(wrapText("Advanced: "+adv, w)))
	}
	add("")
	if st.Enabled {
		var texts []string
		if detail != nil {
			texts = detail.NextFiresText
		} else {
			for _, t := range cronedit.NextFires(st.Job.Schedule, st.Name, fleetNow(), cronedit.PreviewFires) {
				texts = append(texts, t.In(time.Local).Format(cron.NextFireFormat))
			}
		}
		if len(texts) > 0 {
			add(fleetCountStyle.Render("Next"), wrapText(strings.Join(texts, " · "), w))
		}
	} else {
		add(fleetCountStyle.Render("Next"), fleetDimStyle.Render("paused: nothing fires until resumed"))
	}
	for _, rw := range st.Windows {
		add(fleetWorkingStyle.Render(truncateToWidth("● running: "+m.windowLabel(rw.Session, rw.WindowID), w)))
	}
	add("", fleetCountStyle.Render("Prompt"))
	prompt, maxLines := st.Job.Prompt, 4
	if detail != nil {
		maxLines = 12
	}
	if st.Job.Template != "" {
		add(fleetDimStyle.Render(truncateToWidth("template "+st.Job.Template+" (read at every fire)", w)))
		prompt = ""
		if detail != nil {
			if t := detail.TemplateResolved; t != nil {
				prompt = t.Prompt
			}
			if detail.TemplateError != "" {
				add(fleetErrStyle.Render(truncateToWidth(detail.TemplateError, w)))
			}
		}
	}
	if st.Job.PromptKey != "" {
		add(fleetDimStyle.Render(truncateToWidth("context key "+st.Job.PromptKey+" (read at every fire)", w)))
		prompt = ""
		if detail != nil {
			prompt = detail.PromptResolved
			if detail.PromptKeyError != "" {
				add(fleetErrStyle.Render(truncateToWidth(detail.PromptKeyError, w)))
			}
		}
	}
	add(clampLines(wrapText(prompt, w), maxLines)...)
	runs := st.Recent
	if detail != nil {
		runs = detail.Runs
	}
	add("", fleetCountStyle.Render("Last runs"))
	if len(runs) == 0 {
		add(fleetDimStyle.Render("never ran"))
	}
	for _, e := range runs {
		add(renderRunLine(e, w, m.runIsLive))
	}
	return out
}

// selectedSchedLineValue is selectedSchedLine on a value receiver.
func (m FleetModel) selectedSchedLineValue() *fleetLine {
	return m.selectedSchedLine()
}

// windowLabel names a live window by its session: agent, label and handle
// (`go-engineer "esc fix in hub" (a3f9) in acme-api`), never its window ID.
func (m FleetModel) windowLabel(session, windowID string) string {
	for _, g := range m.groups {
		for _, r := range g.Rows {
			if r.Session == session && r.WindowID == windowID {
				return fleet.Who(fleet.NewIndex(m.groups, m.closed), r) + " in " + g.Project
			}
		}
	}
	return "a session in " + strings.TrimPrefix(session, "agentctl-")
}

// runIsLive reports whether the window a run launched is still in the hub.
func (m FleetModel) runIsLive(e cron.Entry) bool {
	for _, g := range m.groups {
		for _, r := range g.Rows {
			if r.WindowID == e.WindowID && (e.Session == "" || r.Session == e.Session) {
				return true
			}
		}
	}
	return false
}

// renderRunLine is one run: time, result, live/closed state or note.
func renderRunLine(e cron.Entry, w int, live func(cron.Entry) bool) string {
	ts := e.Time.In(time.Local).Format("Mon 2 Jan 15:04")
	st := fleetDimStyle
	switch {
	case e.Result == cron.ResultFired:
		st = fleetSuccessStyle
	case cron.IsFailed(e.Result):
		st = fleetErrStyle
	case strings.HasPrefix(e.Result, "skipped"):
		st = fleetBlockedStyle
	}
	extra := ""
	if e.WindowID != "" {
		extra = "closed"
		if live != nil && live(e) {
			extra = "live"
		}
	}
	if e.Note != "" {
		extra = e.Note
	}
	if e.Manual {
		extra = strings.TrimSpace("manual " + extra)
	}
	line := fleetDimStyle.Render(ts) + "  " + st.Render(strings.Replace(e.Result, ":", " · ", 1))
	if extra != "" {
		line += "  " + fleetDimStyle.Render(extra)
	}
	return truncateToWidth(line, w)
}

// advancedSummary lists a job's non-default advanced settings ("" when all
// are defaults).
func advancedSummary(j cron.Job) string {
	var parts []string
	if !j.UseWorktree() {
		parts = append(parts, "no worktree")
	}
	for _, kv := range [][2]string{{"model", j.Model}, {"effort", j.Effort}, {"backend", j.Backend}, {"account", j.Account}, {"timeout", j.Timeout}} {
		if kv[1] != "" {
			parts = append(parts, kv[0]+" "+kv[1])
		}
	}
	if j.Overlap != "" && j.Overlap != cron.OverlapSkip {
		parts = append(parts, "overlap "+j.Overlap)
	}
	if j.Catchup != "" && j.Catchup != cron.CatchupLatest {
		parts = append(parts, "catch-up "+j.Catchup)
	}
	if t := j.SkipIfUsageAbove; t != nil && (t.FiveHour > 0 || t.SevenDay > 0) {
		parts = append(parts, "usage gate "+usageText(*t))
	}
	if j.Harness != "" && j.Harness != cron.HarnessClaude {
		parts = append(parts, "harness "+j.Harness)
	}
	return strings.Join(parts, " · ")
}

func usageText(t config.CronUsageThresholds) string {
	var p []string
	if t.FiveHour > 0 {
		p = append(p, fmt.Sprintf("5h %g%%", t.FiveHour))
	}
	if t.SevenDay > 0 {
		p = append(p, fmt.Sprintf("7d %g%%", t.SevenDay))
	}
	return strings.Join(p, " ")
}

// wrapText wraps s to w columns.
func wrapText(s string, w int) string {
	if w <= 0 {
		return s
	}
	return lipgloss.NewStyle().Width(w).Render(s)
}

// clampLines splits s into lines and keeps at most n (the last one says
// how many were cut).
func clampLines(s string, n int) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return lines
	}
	more := len(lines) - n + 1
	return append(lines[:n-1], fleetDimStyle.Render(fmt.Sprintf("… %d more lines", more)))
}

// ── Layout ───────────────────────────────────────────────────────────────────

// The card and the form are a drawer: a right-anchored block drawn over the
// hub list pane (contentWidth), under the header and above the footer, with
// a left border. The live viewer pane is never touched. It slides in from the
// right edge when it opens (schedDrawerTick); closing is instant.

const (
	// fleetSchedDrawerPct is the drawer's share of the pane width.
	fleetSchedDrawerPct = 80
	// fleetSchedDrawerSliver is the least list the drawer leaves visible;
	// with less than that the drawer takes the full width.
	fleetSchedDrawerSliver = 8
	// fleetSchedSlideDur is the slide-in duration; fleetSchedSlideEvery the
	// frame interval.
	fleetSchedSlideDur   = 180 * time.Millisecond
	fleetSchedSlideEvery = 16 * time.Millisecond
)

// schedDrawerTickMsg is one frame of the slide-in. gen guards against ticks
// of an earlier open.
type schedDrawerTickMsg struct{ gen int }

// schedDrawerWidth is the drawer's width including its border: 80% of the
// pane, at least fleetSchedPaneMin, all of it when the list would be left
// with less than a sliver.
func (m FleetModel) schedDrawerWidth() int {
	cw := m.contentWidth()
	w := max(cw*fleetSchedDrawerPct/100, fleetSchedPaneMin)
	if w > cw-fleetSchedDrawerSliver {
		w = cw
	}
	return w
}

// schedPaneWidth is the width the card or form lines are formatted at: the
// drawer less its border, left indent and right margin.
func (m FleetModel) schedPaneWidth() int {
	return max(m.schedDrawerWidth()-3, 1)
}

// schedDrawerOffset is how many columns the drawer is still off the right
// edge (0 once settled).
func (m FleetModel) schedDrawerOffset() int {
	if !m.schedAnimOn {
		return 0
	}
	return int(math.Round(float64(m.schedDrawerWidth()) * (1 - m.schedAnimP)))
}

// syncSchedDrawer starts the slide-in when the card or form becomes active
// and drops the animation when it goes away. Only open and close count:
// moving between schedule rows leaves the drawer as it is. Called after
// every Update.
func (m *FleetModel) syncSchedDrawer() tea.Cmd {
	active := m.schedPaneActive()
	switch {
	case active && !m.schedPaneOn:
		m.schedPaneOn = true
		m.schedAnimGen++
		m.schedAnimOn = true
		m.schedAnimP = 0
		m.schedAnimStart = fleetNow()
		return schedDrawerTick(m.schedAnimGen)
	case !active && m.schedPaneOn:
		m.schedPaneOn = false
		m.schedAnimGen++ // late ticks of the open are stale
		m.schedAnimOn = false
	}
	return nil
}

func schedDrawerTick(gen int) tea.Cmd {
	return tea.Tick(fleetSchedSlideEvery, func(time.Time) tea.Msg { return schedDrawerTickMsg{gen: gen} })
}

// applySchedDrawerTick advances the slide-in (ease-out) and schedules the
// next frame, or stops ticking once it has settled.
func (m *FleetModel) applySchedDrawerTick(msg schedDrawerTickMsg) tea.Cmd {
	if msg.gen != m.schedAnimGen || !m.schedAnimOn {
		return nil
	}
	t := float64(fleetNow().Sub(m.schedAnimStart)) / float64(fleetSchedSlideDur)
	if t >= 1 {
		m.schedAnimOn = false
		m.schedAnimP = 1
		return nil
	}
	t = math.Max(t, 0)
	m.schedAnimP = 1 - math.Pow(1-t, 3)
	return schedDrawerTick(msg.gen)
}

// schedDrawerRows is the screen row where the drawer starts and how many
// rows it spans (the list body, between the header and the footer line).
func (m FleetModel) schedDrawerRows(total int) (top, h int) {
	top = m.fleetListTop()
	return top, total - 1 - top
}

// schedDrawerX is the screen column of the settled drawer's left border.
func (m FleetModel) schedDrawerX() int {
	return m.contentWidth() - m.schedDrawerWidth()
}

// schedDrawerCovers reports whether screen cell (x, y) is under the settled
// or sliding drawer, so a click there is not a click on the list below it.
func (m FleetModel) schedDrawerCovers(x, y int) bool {
	if !m.schedPaneActive() {
		return false
	}
	total := len(strings.Split(m.renderList(), "\n"))
	top, h := m.schedDrawerRows(total)
	return y >= top && y < top+h && x >= m.schedDrawerX()+m.schedDrawerOffset()
}

// withSchedPane draws the card or form as a drawer over the rendered list.
func (m FleetModel) withSchedPane(list string) string {
	if !m.schedPaneActive() {
		return list
	}
	total := len(strings.Split(list, "\n"))
	top, h := m.schedDrawerRows(total)
	if h < 3 {
		return list
	}
	dw, pw := m.schedDrawerWidth(), m.schedPaneWidth()
	var pane []string
	if m.schedForm != nil {
		pane = m.schedFormLines(pw, h-2)
	} else {
		pane = m.schedCardLines(pw)
	}
	pane = fitPane(pane, dw-1, h, 1)
	vis := dw - m.schedDrawerOffset()
	if vis <= 0 {
		return list
	}
	border := fleetDimStyle.Render("│")
	rows := make([]string, len(pane))
	for i, l := range pane {
		rows[i] = ansi.Truncate(border+l, vis, "")
	}
	cw := m.contentWidth()
	return compositeOverlayAt(list, strings.Join(rows, "\n"), cw-vis, top, cw, total)
}

// padToWidth pads s with spaces to w columns, so a shorter line erases the
// cells of the longer one drawn before it.
func padToWidth(s string, w int) string {
	if d := w - lipgloss.Width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// fitPane splits pane lines on newlines, indents them, cuts them to w and
// keeps at most h of them below one blank line.
func fitPane(lines []string, w, h, indent int) []string {
	var flat []string
	for _, l := range lines {
		flat = append(flat, strings.Split(l, "\n")...)
	}
	out := []string{""}
	pad := strings.Repeat(" ", indent)
	for _, l := range flat {
		if len(out) >= h {
			break
		}
		out = append(out, padToWidth(truncateToWidth(pad+l, w), w))
	}
	for len(out) < h {
		out = append(out, strings.Repeat(" ", w))
	}
	return out
}

// errSchedNoProject is the form's error for a job dir it cannot offer.
var errSchedNoProject = errors.New("no project to schedule in: start a session first (N)")

// schedProjectDirs are the project dirs the form offers: every hub group's
// project, plus the jobs' dirs, sorted by project name.
func (m FleetModel) schedProjectDirs(extra string) []string {
	seen := map[string]bool{}
	var out []string
	addDir := func(d string) {
		if d == "" {
			return
		}
		d = filepath.Clean(d)
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	addDir(extra)
	for _, g := range m.groups {
		addDir(g.ProjectDir)
	}
	for _, st := range m.sched.ov.Jobs {
		addDir(st.Job.Dir)
	}
	first := ""
	if extra != "" {
		first = filepath.Clean(extra)
	}
	sortDirs(out, first)
	return out
}

// sortDirs orders dirs by base name, keeping first (when set) at the front.
func sortDirs(dirs []string, first string) {
	rank := func(d string) string {
		if d == first {
			return ""
		}
		return "\x01" + filepath.Base(d) + "\x00" + d
	}
	for i := 1; i < len(dirs); i++ {
		for j := i; j > 0 && rank(dirs[j]) < rank(dirs[j-1]); j-- {
			dirs[j], dirs[j-1] = dirs[j-1], dirs[j]
		}
	}
}

// tildify shows a path under the home directory as ~/... A symlinked home
// (macOS /var → /private/var) matches either spelling: both sides are
// compared as given and with symlinks resolved.
func tildify(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || p == "" {
		return p
	}
	real := func(s string) string {
		if r, err := filepath.EvalSymlinks(s); err == nil {
			return r
		}
		return s
	}
	for _, pair := range [][2]string{{p, home}, {real(p), real(home)}} {
		path, h := pair[0], pair[1]
		if path == h {
			return "~"
		}
		if rel, ok := strings.CutPrefix(path, h+string(filepath.Separator)); ok {
			return "~/" + rel
		}
	}
	return p
}
