package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/cron"
	"github.com/blsmth/agentctl/internal/cronedit"
	"github.com/blsmth/agentctl/internal/dispatchplan"
	"github.com/blsmth/agentctl/internal/tmux"
)

// The hub's Schedules section (fleetSchedulesKey): the `agentctl cron` jobs
// at the bottom of the list, read through the shared editing core
// (internal/cronedit) in process, only while the section is shown. Selecting
// a job shows its card to the right of the list; the editing form (see
// fleet_schedform.go) takes the same place, so the list stays visible. While
// either is showing, the live viewer pane is closed so the hub's own pane
// has the room; moving back to an agent row brings the viewer back.
//
// Keys on a schedule row are context-sensitive (scheduleKey): ⏎ card, e
// edit, space pause/resume, g run now, d duplicate, x delete, n new. Every
// other line keeps its usual bindings. Writes and run-now run as tea.Cmds,
// so a save waiting on the tick lock never freezes the hub.

// fleetSchedulesKey shows or hides the Schedules section.
const fleetSchedulesKey = "T"

// fleetSchedNewSel is selSchedule for the "+ new schedule" line.
const fleetSchedNewSel = "\x00new"

// fleetSchedReload is how often the section re-reads the jobs and the run
// history on the poll tick (runs.ndjson can be large).
const fleetSchedReload = 5 * time.Second

// fleetSchedPaneMin is the narrowest right pane the card or form is drawn
// in beside the list. Below it, the form replaces the list and the card is
// not drawn.
const fleetSchedPaneMin = 34

// Row glyphs: the same as the phone.
const (
	fleetSchedGlyphOn      = "⏲"
	fleetSchedGlyphPaused  = "⏸"
	fleetSchedGlyphRunning = "●"
	fleetSchedGlyphFailed  = "!"
)

// Seams so tests never touch the real jobs file, spawn `agentctl cron run`,
// kill the real viewer pane or open an editor.
var (
	// fleetSchedStore is the editing core; validate is the injected agent
	// check (dispatch.ValidateAgentName in the hub).
	fleetSchedStore = func(validate func(name, dir string) error) cronedit.Store {
		return cronedit.Default(validate)
	}
	// fleetSchedRun fires a job now (plain `cron run`, never --force).
	fleetSchedRun cronedit.RunFunc = cronedit.RunSubprocess
	// fleetSchedHideViewer closes the live viewer pane.
	fleetSchedHideViewer = tmux.HideLiveViewer
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
	fleetSchedViewerHiddenMsg struct{}
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

// schedLoadCmd reads the jobs in the background; nil while the section is
// hidden or a read is in flight.
func (m *FleetModel) schedLoadCmd() tea.Cmd {
	if !m.showSchedules || m.sched.loading {
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
	if !m.showSchedules || fleetNow().Sub(m.sched.lastLoad) < fleetSchedReload {
		return nil
	}
	return m.schedLoadCmd()
}

func (m *FleetModel) applySchedLoaded(msg fleetSchedLoadedMsg) {
	m.sched.loading = false
	if !m.showSchedules {
		return
	}
	if msg.err != nil {
		// A failed read keeps the last good list (if any) under the error.
		m.sched.err = msg.err.Error()
	} else {
		m.sched.err = ""
		m.sched.ov = msg.ov
		m.sched.loaded = true
	}
	m.relayout()
}

// toggleSchedules is fleetSchedulesKey.
func (m *FleetModel) toggleSchedules() tea.Cmd {
	m.showSchedules = !m.showSchedules
	m.jumpErr = ""
	if m.showSchedules {
		m.setSuccess("schedules shown")
	} else {
		m.setSuccess("schedules hidden")
		m.sched = fleetSchedState{}
		m.schedForm, m.schedConfirm = nil, nil
	}
	m.relayout()
	return tea.Batch(m.saveViewCmd(), m.schedLoadCmd())
}

// schedLines is the section for rebuildLines: the label, warnings, then one
// line per job (or "+ new schedule" when there are none).
func (m FleetModel) schedLines() []fleetLine {
	if !m.showSchedules {
		return nil
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
	head := fmt.Sprintf("Schedules (%d)", len(ov.Jobs))
	if len(ov.Jobs) == 0 {
		head = "Schedules · none yet"
	}
	lines := []fleetLine{label(head, false)}
	if m.sched.err != "" {
		lines = append(lines, label(m.sched.err, true))
	}
	if !ov.SchedulerInstalled {
		lines = append(lines, label("scheduler not installed: run agentctl cron install", true))
	}
	if !ov.Enabled {
		lines = append(lines, label("cron is off (cron.enabled: false): nothing fires", true))
	}
	if len(ov.Jobs) == 0 {
		return append(lines, fleetLine{kind: fleetLineSchedule, schedNew: true})
	}
	for _, st := range ov.Jobs {
		lines = append(lines, fleetLine{kind: fleetLineSchedule, sched: st})
	}
	return lines
}

// schedLineKey is the selSchedule value of a schedule line.
func schedLineKey(l fleetLine) string {
	if l.schedNew {
		return fleetSchedNewSel
	}
	return l.sched.Name
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

// ── Viewer hand-off ──────────────────────────────────────────────────────────

// schedPaneActive reports whether the card or the form wants the right side.
func (m FleetModel) schedPaneActive() bool {
	return m.showSchedules && (m.selSchedule != "" || m.schedForm != nil)
}

// syncSchedPane closes the live viewer when the card or form becomes active
// and brings it back when it goes away. Called after every Update.
func (m *FleetModel) syncSchedPane() tea.Cmd {
	active := m.schedPaneActive()
	switch {
	case active && !m.schedPaneOn:
		m.schedPaneOn = true
		m.schedPrevLive = m.liveShown
		m.liveShown = ""
		return fleetSchedHideCmd()
	case !active && m.schedPaneOn:
		m.schedPaneOn = false
		return m.restoreViewerCmd()
	}
	return nil
}

// fleetViewerMu serializes the hub's viewer pane changes: two shows racing
// to create a missing viewer pane (as after the schedules pane closed it)
// would each kill the other's viewer session.
var fleetViewerMu sync.Mutex

// fleetSchedHideCmd closes the live viewer pane.
func fleetSchedHideCmd() tea.Cmd {
	return func() tea.Msg {
		fleetViewerMu.Lock()
		_ = fleetSchedHideViewer()
		fleetViewerMu.Unlock()
		return fleetSchedViewerHiddenMsg{}
	}
}

// restoreViewerCmd shows the selected agent again, or the one that was
// showing before the card took its place.
func (m *FleetModel) restoreViewerCmd() tea.Cmd {
	if cmd := m.maybeShowCmd(); cmd != nil {
		return cmd
	}
	if m.selIsHeader || m.selSession == "" || m.selWindowID == "" {
		if session, win, ok := parseHubSelect(m.schedPrevLive); ok && m.rowExists(session, win) {
			return m.liveShowFor(session, win)
		}
	}
	return nil
}

// applySchedViewerHidden re-shows the viewer when the selection left the
// schedules while the pane was being closed.
func (m *FleetModel) applySchedViewerHidden() tea.Cmd {
	if m.schedPaneActive() {
		return nil
	}
	m.liveShown = ""
	return m.restoreViewerCmd()
}

// ── Keys ─────────────────────────────────────────────────────────────────────

// scheduleKey runs key on a selected schedule line. handled is false for
// keys that keep their usual meaning.
func (m *FleetModel) scheduleKey(key string) (cmd tea.Cmd, handled bool) {
	l := m.selectedSchedLine()
	if l == nil {
		return nil, false
	}
	if l.schedNew {
		switch key {
		case "enter", "n", "e":
			return m.openSchedForm(schedFormNew, nil, ""), true
		case "tab", "space", "g", "d", "x":
			return nil, true
		}
		return nil, false
	}
	st := l.sched
	switch key {
	case "enter":
		return m.openSchedCard(st.Name), true
	case "e":
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
		if msg.entry.WindowID != "" {
			text += ": window " + msg.entry.WindowID
			if msg.entry.Session != "" {
				text += " in " + msg.entry.Session
			}
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

// openSchedCard is ⏎: load the full card (prompt_key text, more runs).
func (m *FleetModel) openSchedCard(name string) tea.Cmd {
	m.sched.detailName = name
	m.sched.detail = nil
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
		m.sched.detailName = ""
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
		{"card", "enter"}, {"edit", "e"}, {toggle, "space"}, {"run now", "g"},
		{"duplicate", "d"}, {"delete", "x"},
	}
}

// schedHints are the footer hints on a schedule row.
func (m FleetModel) schedHints() []fleetHint {
	l := m.selectedSchedLine()
	if l == nil {
		return nil
	}
	if l.schedNew {
		return []fleetHint{{"j/k", "move"}, {"⏎", "new schedule"}, {fleetSchedulesKey, "hide"}, {"?", "more"}}
	}
	toggle := "pause"
	if !l.sched.Enabled {
		toggle = "resume"
	}
	return []fleetHint{
		{"j/k", "move"}, {"e", "edit"}, {"space", toggle}, {"g", "run now"},
		{"d", "duplicate"}, {"x", "delete"}, {"n", "new"}, {"⏎", "card"}, {"?", "more"},
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

// schedLastText is the last-result column ("" when it never ran).
func schedLastText(st cronedit.Status) string {
	if st.Last == nil {
		return ""
	}
	switch r := st.Last.Result; {
	case r == cron.ResultFired:
		return "fired"
	case cron.IsFailed(r):
		return "failed"
	default:
		kind, reason, ok := strings.Cut(r, ":")
		if ok {
			return kind + ": " + reason
		}
		return r
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
// words, the last result, project and agent, with the next fire
// right-aligned. Narrow widths drop the agent, then the project, then the
// last result, then cut the words; the name and next fire always show.
func (m FleetModel) renderSchedRow(l fleetLine, selected bool, cw int) string {
	cursor := "  "
	if selected {
		cursor = cursorActiveStyle.Render("▸") + " "
	}
	if l.schedNew {
		return fleetFitLine("  "+cursor+fleetNewHoverStyle.Render("+ new schedule")+fleetDimStyle.Render("  n"), selected, cw)
	}
	st := l.sched
	glyph, gst := schedGlyph(st)
	nameSt := fleetNameDoneStyle
	if !st.Enabled {
		nameSt = fleetDimStyle
	}
	left := "  " + cursor + gst.Render(glyph) + " " + nameSt.Render(st.Name)
	nextSt := fleetCountStyle
	if st.Enabled && st.Invalid != "" {
		nextSt = fleetErrStyle
	}
	right := nextSt.Render(schedNextText(st, fleetNow()))
	inner := cw - fleetSidebarRightGutter
	room := inner - lipgloss.Width(left) - lipgloss.Width(right) - 2
	if room < 0 {
		// Even the name does not fit: cut it, never the next fire.
		nameW := max(inner-lipgloss.Width(right)-1-lipgloss.Width("  "+cursor+glyph+" "), 1)
		left = "  " + cursor + gst.Render(glyph) + " " + nameSt.Render(truncateToWidth(st.Name, nameW))
		room = inner - lipgloss.Width(left) - lipgloss.Width(right) - 1
	}
	mid := schedRowMiddle(st, room)
	body := left + mid
	gap := inner - lipgloss.Width(body) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return fleetFitLine(body+strings.Repeat(" ", gap)+right, selected, cw)
}

// schedRowMiddle is the part between the name and the next fire, in room
// columns ("" when there is none).
func schedRowMiddle(st cronedit.Status, room int) string {
	const sep = " · "
	words, last := schedWords(st), schedLastText(st)
	project, agent := projectOf(st.Job.Dir), st.Job.Agent
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
	// Dropped from the end: agent, then project, then the last result.
	all := []seg{{words, fleetRecapIdleStyle}, {last, lastSt}, {project, fleetDimStyle}, {agent, fleetDimStyle}}
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
		segs = segs[:len(segs)-1]
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
		return []string{
			fleetTitleStyle.Render("Schedules"),
			"",
			wrapText("No cron jobs yet. A schedule runs an agent unattended at set times, each run in its own window.", w),
			"",
			fleetDimStyle.Render("enter or n: new schedule (or n, then t, on a project header)"),
		}
	}
	st := l.sched
	var detail *cronedit.Detail
	if m.sched.detail != nil && m.sched.detailName == st.Name {
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
	add(wrapText(fmt.Sprintf("%s in %s · %s", fleetGroupNameStyle.Render(st.Job.Agent), tildify(st.Job.Dir), wt), w))
	if st.Invalid != "" {
		add(fleetErrStyle.Render(wrapText("invalid: "+st.Invalid, w)))
	}
	if adv := advancedSummary(st.Job); adv != "" {
		add(fleetDimStyle.Render(wrapText(adv, w)))
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
		add(renderRunLine(e, w))
	}
	if detail == nil {
		add("", fleetDimStyle.Render(truncateToWidth("enter full card · e edit · space pause/resume · g run now", w)))
	}
	return out
}

// selectedSchedLineValue is selectedSchedLine on a value receiver.
func (m FleetModel) selectedSchedLineValue() *fleetLine {
	return m.selectedSchedLine()
}

// windowLabel names a live window by its row ("go-engineer-2 in acme-api").
func (m FleetModel) windowLabel(session, windowID string) string {
	for _, g := range m.groups {
		for _, r := range g.Rows {
			if r.Session == session && r.WindowID == windowID {
				return r.Name + " in " + g.Project
			}
		}
	}
	return windowID + " in " + session
}

// renderRunLine is one run: time, result, window or note.
func renderRunLine(e cron.Entry, w int) string {
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
	extra := e.WindowID
	if e.Note != "" {
		extra = e.Note
	}
	if e.Manual {
		extra = strings.TrimSpace("manual " + extra)
	}
	line := fleetDimStyle.Render(ts) + "  " + st.Render(e.Result)
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

// schedPaneWidth is the width the right pane gets beside the list (0 when
// it does not fit).
func (m FleetModel) schedPaneWidth() int {
	w := m.termWidth - m.contentWidth() - 3
	if w < fleetSchedPaneMin {
		return 0
	}
	return w
}

// schedFormReplacesList reports whether the open form is drawn in place of
// the list (no room beside it).
func (m FleetModel) schedFormReplacesList() bool {
	return m.showSchedules && m.schedForm != nil && m.schedPaneWidth() == 0
}

// withSchedPane draws the card or form to the right of the rendered list.
// Without room for it the card is not drawn (the form replaces the list
// instead, see renderList).
func (m FleetModel) withSchedPane(list string) string {
	pw := m.schedPaneWidth()
	if !m.schedPaneActive() || pw == 0 {
		return list
	}
	left := strings.Split(list, "\n")
	h := len(left)
	var pane []string
	if m.schedForm != nil {
		pane = m.schedFormLines(pw, h-2)
	} else {
		pane = m.schedCardLines(pw)
	}
	pane = fitPane(pane, pw+2, h, 1)
	cw := m.contentWidth()
	sep := fleetDimStyle.Render("│")
	out := make([]string, h)
	for i, l := range left {
		if pad := cw - lipgloss.Width(l); pad > 0 {
			l += strings.Repeat(" ", pad)
		}
		p := ""
		if i < len(pane) {
			p = pane[i]
		}
		out[i] = l + sep + p
	}
	return strings.Join(out, "\n")
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
		out = append(out, truncateToWidth(pad+l, w))
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

// tildify shows a path under the home directory as ~/...
func tildify(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || p == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if rel, ok := strings.CutPrefix(p, home+string(filepath.Separator)); ok {
		return "~/" + rel
	}
	return p
}
