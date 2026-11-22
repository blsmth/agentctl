package tui

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/cron"
	"github.com/blsmth/agentctl/internal/cronedit"
	"github.com/blsmth/agentctl/internal/fleet"
)

// The Schedules form: create, edit or duplicate a cron job in the right
// pane. Field order, the Advanced fold and the schedule picker match the
// phone. The form edits a copy of the stored job, so fields it does not
// show (harness, mode, enabled, a prompt_key left alone) and fields left
// untouched (an unchanged custom schedule, prompt or text value) are saved
// exactly as they were. Every change runs a preview (cronedit Preview) in
// the background; ctrl+s opens the review, y saves through a tea.Cmd.

type schedFormMode int

const (
	schedFormNew schedFormMode = iota
	schedFormEdit
	schedFormDup
)

// Schedule picker kinds, in segment order.
const (
	schedKindHourly = iota
	schedKindDaily
	schedKindWeekdays
	schedKindDays
	schedKindCustom
)

var schedKindLabels = []string{"Hourly", "Daily", "Weekdays", "Days…", "Custom"}

var schedKindNames = []string{cronedit.KindHourly, cronedit.KindDaily, cronedit.KindWeekdays, cronedit.KindDays}

// schedDayLetters are the day chips, Monday first; schedDayCron maps a chip
// to cron's weekday number (0 = Sunday).
var (
	schedDayLetters = []string{"M", "T", "W", "T", "F", "S", "S"}
	schedDayCron    = []int{1, 2, 3, 4, 5, 6, 0}
)

var (
	schedOverlaps = []string{cron.OverlapSkip, cron.OverlapAllow}
	schedCatchups = []string{cron.CatchupLatest, cron.CatchupNone}
)

// schedStop is one focusable field.
type schedStop int

const (
	stopName schedStop = iota
	stopDir
	stopAgent
	stopPrompt
	stopKind
	stopClock
	stopDays
	stopCustom
	stopAdvanced
	stopWorktree
	stopModel
	stopEffort
	stopBackend
	stopAccount
	stopTimeout
	stopOverlap
	stopCatchup
	stopUsage5h
	stopUsage7d
)

// fleetSchedFormHints are the footer hints while the form is open.
var (
	fleetSchedFormHints = []fleetHint{
		{"tab", "next"}, {"←/→", "choose"}, {"space", "toggle"}, {"^e", "editor"}, {"^s", "review"}, {"esc", "cancel"},
	}
	fleetSchedReviewHints = []fleetHint{{"y", "save"}, {"n", "back"}}
	fleetSchedYesNoHints  = []fleetHint{{"y", "yes"}, {"n", "no"}}
)

// Seams: the editor runs through tea.ExecProcess (tests replace it).
var fleetSchedExec = tea.ExecProcess

// fleetSchedForm is the open form.
type fleetSchedForm struct {
	mode     schedFormMode
	original string // stored name (edit) or the duplicated job's name
	base     cron.Job

	name, agent, clock, custom             textinput.Model
	model, effort, backend, account, tmout textinput.Model
	u5, u7                                 textinput.Model
	prompt                                 textarea.Model
	// init holds each text field's value as opened: an untouched field
	// saves the stored value verbatim.
	init map[schedStop]string
	// promptKey is the stored prompt_key, kept while the prompt is empty.
	promptKey string

	dirs   []string
	dirIdx int

	kind         int
	days         [7]bool // by chip, Monday first
	dayCur       int
	origSchedule string
	schedTouched bool

	advOpen                  bool
	worktree                 bool
	overlap, catchup         int
	overlapInit, catchupInit int
	focus                    schedStop
	gen, previewGen          int
	preview                  *cronedit.Preview
	localErrs                []cron.FieldError
	review, discard, saving  bool
	wantReview               bool
	err                      string
	fingerprint              string
	permMode                 string
}

// Messages.
type (
	fleetSchedPreviewMsg struct {
		gen int
		p   cronedit.Preview
	}
	fleetSchedSavedMsg struct {
		mode    schedFormMode
		name    string
		job     cron.Job
		updated cronedit.Updated
		err     error
	}
	fleetSchedEditorMsg struct {
		path string
		err  error
	}
)

func (m FleetModel) schedFormOpen() bool { return m.schedForm != nil }

func newSchedInput(value, placeholder string) textinput.Model {
	in := textinput.New()
	in.Prompt = ""
	in.Placeholder = placeholder
	in.CharLimit = 0
	in.SetValue(value)
	return in
}

// openSchedForm opens the form: a new job (in dir, when set), an edit of
// st, or a duplicate of st (named <name>-copy, saved paused).
func (m *FleetModel) openSchedForm(mode schedFormMode, st *cronedit.Status, dir string) tea.Cmd {
	m.cancelNewSession()
	m.cancelRename()
	m.cancelClose()
	m.cancelNew()
	m.cancelSweep()
	m.closeMenu()
	m.schedConfirm = nil
	if !m.showSchedules {
		m.showSchedules = true
		m.relayout()
	}
	f := &fleetSchedForm{mode: mode, init: map[schedStop]string{}}
	var job cron.Job
	switch {
	case st != nil:
		job = cloneJob(st.Job)
		f.original = st.Name
		if dir == "" {
			dir = job.Dir
		}
	default:
		job = cron.Job{Agent: m.schedDefaultAgent(dir)}
	}
	if mode == schedFormDup {
		job.Name = dupName(job.Name)
		off := false
		job.Enabled = &off
	}
	job.Normalize()
	f.base = job

	f.name = newSchedInput(job.Name, "nightly-triage")
	f.agent = newSchedInput(job.Agent, "agent name")
	f.prompt = newSchedPrompt(job.Prompt)
	f.promptKey = job.PromptKey
	if f.promptKey != "" {
		f.prompt.Placeholder = "context key " + f.promptKey + " (type or ^e to replace it with an inline prompt)"
	}
	f.model = newSchedInput(job.Model, "agent default")
	f.effort = newSchedInput(job.Effort, "default")
	f.backend = newSchedInput(job.Backend, "none")
	f.account = newSchedInput(job.Account, "default")
	f.tmout = newSchedInput(job.Timeout, "none (e.g. 45m)")
	var u5, u7 string
	if t := job.SkipIfUsageAbove; t != nil {
		u5, u7 = fmtPct(t.FiveHour), fmtPct(t.SevenDay)
	}
	f.u5 = newSchedInput(u5, "global")
	f.u7 = newSchedInput(u7, "global")
	f.worktree = job.UseWorktree()
	f.overlap = max(slices.Index(schedOverlaps, job.Overlap), 0)
	f.catchup = max(slices.Index(schedCatchups, job.Catchup), 0)
	f.overlapInit, f.catchupInit = f.overlap, f.catchup

	f.dirs = m.schedProjectDirs(dir)
	f.clock = newSchedInput("", "07:30")
	f.custom = newSchedInput(job.Schedule, "*/30 9-17 * * 1-5")
	f.origSchedule = job.Schedule
	f.setSchedule(job.Schedule, mode == schedFormNew)

	for stop, in := range map[schedStop]*textinput.Model{
		stopName: &f.name, stopAgent: &f.agent, stopModel: &f.model, stopEffort: &f.effort,
		stopBackend: &f.backend, stopAccount: &f.account, stopTimeout: &f.tmout, stopUsage5h: &f.u5, stopUsage7d: &f.u7,
	} {
		f.init[stop] = in.Value()
	}
	f.init[stopPrompt] = f.prompt.Value()
	f.advOpen = advancedSummary(job) != ""
	f.focus = f.stops()[0]
	f.applyFocus()
	f.fingerprint = f.state()
	m.schedForm = f
	m.jumpErr = ""
	return m.schedPreviewCmd()
}

func newSchedPrompt(value string) textarea.Model {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.MaxHeight = 0
	ta.MaxContentHeight = 100000
	ta.Placeholder = "What the agent does on every run…"
	ta.Prompt = ""
	ta.SetHeight(4)
	ta.SetValue(value)
	return ta
}

// schedDefaultAgent prefills a new job's agent: the selected row's agent,
// else the first agent of dir's group, else the configured default.
func (m *FleetModel) schedDefaultAgent(dir string) string {
	if r := m.selectedRow(); r != nil && !r.Shell {
		return fleet.BaseName(*r)
	}
	for _, g := range m.groups {
		if dir == "" || g.ProjectDir != dir {
			continue
		}
		for _, r := range g.Rows {
			if !r.Shell {
				return fleet.BaseName(r)
			}
		}
	}
	return config.Load().DefaultAgent
}

// dupName is a duplicate's default name, kept within the 48-character rule.
func dupName(name string) string {
	const suffix = "-copy"
	if len(name)+len(suffix) > 48 {
		name = name[:48-len(suffix)]
	}
	return name + suffix
}

func fmtPct(v float64) string {
	if v == 0 {
		return ""
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func cloneJob(j cron.Job) cron.Job {
	if j.Worktree != nil {
		v := *j.Worktree
		j.Worktree = &v
	}
	if j.Enabled != nil {
		v := *j.Enabled
		j.Enabled = &v
	}
	if j.SkipIfUsageAbove != nil {
		v := *j.SkipIfUsageAbove
		j.SkipIfUsageAbove = &v
	}
	return j
}

// setSchedule puts spec in the picker when ParseSimple maps it exactly,
// else in Custom. fresh (a new job) starts at weekdays 09:00.
func (f *fleetSchedForm) setSchedule(spec string, fresh bool) {
	if fresh && strings.TrimSpace(spec) == "" {
		f.kind = schedKindWeekdays
		f.clock.SetValue("09:00")
		f.schedTouched = true
		return
	}
	ss, ok := cronedit.ParseSimple(spec)
	if !ok {
		f.kind = schedKindCustom
		return
	}
	f.kind = slices.Index(schedKindNames, ss.Kind)
	if ss.Kind == cronedit.KindHourly {
		f.clock.SetValue(strconv.Itoa(ss.Minute))
	} else {
		f.clock.SetValue(fmt.Sprintf("%02d:%02d", ss.Hour, ss.Minute))
	}
	for _, d := range ss.Days {
		f.days[slices.Index(schedDayCron, d)] = true
	}
}

// stops are the focusable fields in order, for the current kind and fold.
func (f *fleetSchedForm) stops() []schedStop {
	var out []schedStop
	if f.mode != schedFormEdit {
		out = append(out, stopName)
	}
	out = append(out, stopDir, stopAgent, stopPrompt, stopKind)
	switch f.kind {
	case schedKindCustom:
		out = append(out, stopCustom)
	case schedKindDays:
		out = append(out, stopDays, stopClock)
	default:
		out = append(out, stopClock)
	}
	out = append(out, stopAdvanced)
	if f.advOpen {
		out = append(out, stopWorktree, stopModel, stopEffort, stopBackend, stopAccount, stopTimeout,
			stopOverlap, stopCatchup, stopUsage5h, stopUsage7d)
	}
	return out
}

// move focuses the next (delta 1) or previous field, wrapping.
func (f *fleetSchedForm) move(delta int) {
	stops := f.stops()
	i := slices.Index(stops, f.focus)
	if i < 0 {
		i = 0
	}
	f.focus = stops[(i+delta+len(stops))%len(stops)]
	f.applyFocus()
}

// focusStop focuses stop, opening the Advanced fold when it is inside it.
func (f *fleetSchedForm) focusStop(stop schedStop) {
	if stop >= stopWorktree {
		f.advOpen = true
	}
	if !slices.Contains(f.stops(), stop) {
		return
	}
	f.focus = stop
	f.applyFocus()
}

func (f *fleetSchedForm) inputFor(stop schedStop) *textinput.Model {
	switch stop {
	case stopName:
		return &f.name
	case stopAgent:
		return &f.agent
	case stopClock:
		return &f.clock
	case stopCustom:
		return &f.custom
	case stopModel:
		return &f.model
	case stopEffort:
		return &f.effort
	case stopBackend:
		return &f.backend
	case stopAccount:
		return &f.account
	case stopTimeout:
		return &f.tmout
	case stopUsage5h:
		return &f.u5
	case stopUsage7d:
		return &f.u7
	}
	return nil
}

var schedTextStops = []schedStop{stopName, stopAgent, stopClock, stopCustom, stopModel, stopEffort,
	stopBackend, stopAccount, stopTimeout, stopUsage5h, stopUsage7d}

func (f *fleetSchedForm) applyFocus() {
	for _, s := range schedTextStops {
		in := f.inputFor(s)
		if s == f.focus {
			in.Focus()
		} else {
			in.Blur()
		}
	}
	if f.focus == stopPrompt {
		f.prompt.Focus()
	} else {
		f.prompt.Blur()
	}
}

// state is a fingerprint of everything the user can change, for "dirty".
func (f *fleetSchedForm) state() string {
	parts := []string{f.prompt.Value(), strconv.Itoa(f.dirIdx), strconv.Itoa(f.kind), fmt.Sprint(f.days),
		strconv.FormatBool(f.worktree), strconv.Itoa(f.overlap), strconv.Itoa(f.catchup)}
	for _, s := range schedTextStops {
		parts = append(parts, f.inputFor(s).Value())
	}
	return strings.Join(parts, "\x00")
}

func (f *fleetSchedForm) dirty() bool { return f.state() != f.fingerprint }

// text is a text field's value: the stored value while untouched, else the
// trimmed input.
func (f *fleetSchedForm) text(stop schedStop, stored string) string {
	v := f.inputFor(stop).Value()
	if v == f.init[stop] {
		return stored
	}
	return strings.TrimSpace(v)
}

// spec renders the picker (or the custom expression).
func (f *fleetSchedForm) spec() (string, error) {
	if f.kind == schedKindCustom {
		return strings.TrimSpace(f.custom.Value()), nil
	}
	ss := cronedit.SimpleSchedule{Kind: schedKindNames[f.kind]}
	clock := strings.TrimSpace(f.clock.Value())
	if f.kind == schedKindHourly {
		n, err := strconv.Atoi(strings.TrimPrefix(clock, ":"))
		if err != nil || n < 0 || n > 59 {
			return "", fmt.Errorf("minute past the hour: want 0-59, got %q", clock)
		}
		ss.Minute = n
	} else {
		hs, ms, ok := strings.Cut(clock, ":")
		h, herr := strconv.Atoi(hs)
		mn, merr := strconv.Atoi(ms)
		if !ok || herr != nil || merr != nil || h < 0 || h > 23 || mn < 0 || mn > 59 {
			return "", fmt.Errorf("time: want HH:MM (24-hour), got %q", clock)
		}
		ss.Hour, ss.Minute = h, mn
		for i, on := range f.days {
			if on {
				ss.Days = append(ss.Days, schedDayCron[i])
			}
		}
	}
	return ss.Spec()
}

// job builds the job the form describes from a copy of the stored one, plus
// the problems only the form can see (an unparsable time or usage value).
func (f *fleetSchedForm) job() (cron.Job, []cron.FieldError) {
	j := cloneJob(f.base)
	var errs []cron.FieldError
	add := func(field, msg string) { errs = append(errs, cron.FieldError{Field: field, Message: msg}) }
	if f.mode != schedFormEdit {
		j.Name = f.text(stopName, f.base.Name)
	}
	j.Dir = ""
	if len(f.dirs) > 0 {
		j.Dir = f.dirs[f.dirIdx]
	}
	j.Agent = f.text(stopAgent, f.base.Agent)
	switch p := f.prompt.Value(); {
	case p == f.init[stopPrompt] && f.base.PromptKey == "":
		j.Prompt = f.base.Prompt
	case strings.TrimSpace(p) == "" && f.promptKey != "":
		j.Prompt, j.PromptKey = "", f.promptKey
	default:
		j.Prompt, j.PromptKey = p, ""
	}
	if f.schedTouched {
		spec, err := f.spec()
		if err != nil {
			add(cron.FieldSchedule, err.Error())
		}
		j.Schedule = spec
	} else {
		j.Schedule = f.origSchedule
	}
	if f.worktree != f.base.UseWorktree() {
		v := f.worktree
		j.Worktree = &v
	}
	j.Model = f.text(stopModel, f.base.Model)
	j.Effort = f.text(stopEffort, f.base.Effort)
	j.Backend = f.text(stopBackend, f.base.Backend)
	j.Account = f.text(stopAccount, f.base.Account)
	j.Timeout = f.text(stopTimeout, f.base.Timeout)
	if f.overlap != f.overlapInit {
		j.Overlap = schedOverlaps[f.overlap]
	}
	if f.catchup != f.catchupInit {
		j.Catchup = schedCatchups[f.catchup]
	}
	if f.u5.Value() != f.init[stopUsage5h] || f.u7.Value() != f.init[stopUsage7d] {
		var t config.CronUsageThresholds
		for _, p := range []struct {
			in  *textinput.Model
			out *float64
			lbl string
		}{{&f.u5, &t.FiveHour, "5h"}, {&f.u7, &t.SevenDay, "7d"}} {
			v := strings.TrimSuffix(strings.TrimSpace(p.in.Value()), "%")
			if v == "" {
				continue
			}
			n, err := strconv.ParseFloat(v, 64)
			if err != nil {
				add(cron.FieldSkipIfUsageAbove, fmt.Sprintf("usage gate %s: want a percentage, got %q", p.lbl, p.in.Value()))
				continue
			}
			*p.out = n
		}
		j.SkipIfUsageAbove = nil
		if t.FiveHour != 0 || t.SevenDay != 0 {
			j.SkipIfUsageAbove = &t
		}
	}
	return j, errs
}

// errors is every field problem: the form's own, then the preview's
// (a schedule the form could not render is reported once).
func (f *fleetSchedForm) errors() []cron.FieldError {
	out := slices.Clone(f.localErrs)
	local := map[string]bool{}
	for _, e := range f.localErrs {
		local[e.Field] = true
	}
	if f.preview != nil {
		for _, e := range f.preview.Errors {
			if !local[e.Field] {
				out = append(out, e)
			}
		}
	}
	return out
}

// stopForField is the field a field error is shown under.
func (f *fleetSchedForm) stopForField(field string) (schedStop, bool) {
	switch field {
	case cron.FieldName:
		if f.mode == schedFormEdit {
			return 0, false
		}
		return stopName, true
	case cron.FieldDir:
		return stopDir, true
	case cron.FieldAgent:
		return stopAgent, true
	case cron.FieldPrompt:
		return stopPrompt, true
	case cron.FieldSchedule:
		if f.kind == schedKindCustom {
			return stopCustom, true
		}
		return stopClock, true
	case cron.FieldModel:
		return stopModel, true
	case cron.FieldEffort:
		return stopEffort, true
	case cron.FieldBackend:
		return stopBackend, true
	case cron.FieldAccount:
		return stopAccount, true
	case cron.FieldTimeout:
		return stopTimeout, true
	case cron.FieldOverlap:
		return stopOverlap, true
	case cron.FieldCatchup:
		return stopCatchup, true
	case cron.FieldSkipIfUsageAbove:
		return stopUsage5h, true
	}
	return 0, false
}

// schedPreviewCmd re-validates the form in the background. Each change
// bumps the generation; an older result is dropped.
func (m *FleetModel) schedPreviewCmd() tea.Cmd {
	f := m.schedForm
	if f == nil {
		return nil
	}
	f.gen++
	gen := f.gen
	job, local := f.job()
	f.localErrs = local
	original := ""
	if f.mode == schedFormEdit {
		original = f.original
	}
	store := m.schedStore()
	return func() tea.Msg {
		return fleetSchedPreviewMsg{gen: gen, p: store.Preview(job, original, nil)}
	}
}

func (m *FleetModel) applySchedPreview(msg fleetSchedPreviewMsg) {
	f := m.schedForm
	if f == nil || msg.gen != f.gen {
		return
	}
	p := msg.p
	f.preview, f.previewGen = &p, msg.gen
	if f.wantReview {
		f.wantReview = false
		m.schedReview()
	}
}

// previewCurrent reports whether the preview reflects the form as it is.
func (f *fleetSchedForm) previewCurrent() bool {
	return f.preview != nil && f.previewGen == f.gen
}

// schedReview is ctrl+s: open the review once the form is clean.
func (m *FleetModel) schedReview() {
	f := m.schedForm
	if !f.previewCurrent() {
		f.wantReview = true
		f.err = "checking…"
		return
	}
	if errs := f.errors(); len(errs) > 0 {
		f.err = fmt.Sprintf("fix %d problem%s first", len(errs), plural(len(errs)))
		if stop, ok := f.stopForField(errs[0].Field); ok {
			f.focusStop(stop)
		}
		return
	}
	if f.mode == schedFormEdit && len(f.preview.Changed) == 0 {
		m.schedForm = nil
		m.setSuccess("no changes to " + f.original)
		return
	}
	f.err = ""
	f.permMode = fleetSchedPermMode(f.preview.Job.Agent)
	f.review = true
}

// schedSaveCmd is y on the review: save through the core in the
// background (an edit keeps the stored enabled flag).
func (m *FleetModel) schedSaveCmd() tea.Cmd {
	f := m.schedForm
	job, _ := f.job()
	f.saving = true
	f.err = ""
	store, mode, original := m.schedStore(), f.mode, f.original
	return func() tea.Msg {
		if mode == schedFormEdit {
			job.Enabled = nil
			res, err := store.Update(original, job)
			return fleetSchedSavedMsg{mode: mode, name: original, job: res.Job, updated: res, err: err}
		}
		saved, err := store.Create(job)
		return fleetSchedSavedMsg{mode: mode, name: job.Name, job: saved, err: err}
	}
}

func (m *FleetModel) applySchedSaved(msg fleetSchedSavedMsg) tea.Cmd {
	f := m.schedForm
	if msg.err != nil {
		if f == nil {
			m.jumpErr = "saving " + msg.name + ": " + msg.err.Error()
			return nil
		}
		f.saving, f.review = false, false
		if fields := cronedit.FieldsOf(msg.err); fields != nil {
			p := cronedit.Preview{}
			if f.preview != nil {
				p = *f.preview
			}
			p.Errors, p.Valid = fields, false
			f.preview = &p
			f.err = "not saved: fix the problems below"
			if stop, ok := f.stopForField(fields[0].Field); ok {
				f.focusStop(stop)
			}
			return nil
		}
		f.err = "not saved: " + msg.err.Error()
		return nil
	}
	m.schedForm = nil
	switch msg.mode {
	case schedFormDup:
		m.setSuccess("saved " + msg.name + " paused · space resumes it")
	case schedFormEdit:
		text := "saved " + msg.name
		if len(msg.updated.Changed) > 0 {
			text += " (" + strings.Join(msg.updated.Changed, ", ") + ")"
		}
		m.setSuccess(text)
	default:
		text := "scheduled " + msg.name
		if next, err := cronedit.NextFire(msg.job, fleetNow()); err == nil && !next.IsZero() {
			text += " · next " + next.In(time.Local).Format("Mon 15:04")
		}
		m.setSuccess(text)
	}
	m.selSchedule = msg.name
	m.sched.detail, m.sched.detailName = nil, ""
	return m.schedLoadCmd()
}

// closeSchedForm closes the form without saving.
func (m *FleetModel) closeSchedForm() {
	m.schedForm = nil
	m.resolveSelection()
}

// handleSchedFormKey owns every key while the form is open, so typed text
// never triggers a hub binding.
func (m *FleetModel) handleSchedFormKey(msg tea.KeyPressMsg) tea.Cmd {
	f := m.schedForm
	key := msg.String()
	if key == "ctrl+c" {
		m.quitting = true
		return tea.Quit
	}
	if f.saving {
		return nil
	}
	if f.discard {
		f.discard = false
		if key == "y" || key == "Y" {
			m.closeSchedForm()
		}
		return nil
	}
	if f.review {
		switch key {
		case "y", "Y":
			return m.schedSaveCmd()
		case "n", "N", "esc":
			f.review = false
		}
		return nil
	}
	f.err = ""
	switch key {
	case "esc":
		if f.dirty() {
			f.discard = true
		} else {
			m.closeSchedForm()
		}
		return nil
	case "ctrl+s":
		m.schedReview()
		return nil
	case "ctrl+e":
		return m.schedOpenEditor()
	case "tab":
		f.move(1)
		return nil
	case "shift+tab":
		f.move(-1)
		return nil
	}
	if f.focus != stopPrompt {
		switch key {
		case "down":
			f.move(1)
			return nil
		case "up":
			f.move(-1)
			return nil
		}
	}
	before := f.state()
	var cmd tea.Cmd
	switch f.focus {
	case stopDir:
		if n := len(f.dirs); n > 0 {
			switch key {
			case "left":
				f.dirIdx = (f.dirIdx - 1 + n) % n
			case "right", "space":
				f.dirIdx = (f.dirIdx + 1) % n
			case "enter":
				f.move(1)
			}
		}
	case stopKind:
		switch key {
		case "left":
			f.setKind(max(f.kind-1, 0))
		case "right", "space":
			f.setKind(min(f.kind+1, schedKindCustom))
		case "enter":
			f.move(1)
		}
	case stopDays:
		switch key {
		case "left":
			f.dayCur = max(f.dayCur-1, 0)
		case "right":
			f.dayCur = min(f.dayCur+1, 6)
		case "space":
			f.days[f.dayCur] = !f.days[f.dayCur]
			f.schedTouched = true
		case "enter":
			f.move(1)
		}
	case stopAdvanced:
		if key == "space" || key == "enter" || key == "right" || key == "left" {
			f.advOpen = key == "right" || (key != "left" && !f.advOpen)
		}
	case stopWorktree:
		switch key {
		case "space", "left", "right":
			f.worktree = !f.worktree
		case "enter":
			f.move(1)
		}
	case stopOverlap, stopCatchup:
		v := &f.overlap
		if f.focus == stopCatchup {
			v = &f.catchup
		}
		switch key {
		case "left":
			*v = 0
		case "right":
			*v = 1
		case "space":
			*v = 1 - *v
		case "enter":
			f.move(1)
		}
	case stopPrompt:
		f.prompt, cmd = f.prompt.Update(msg)
	default:
		if key == "enter" {
			f.move(1)
			return nil
		}
		in := f.inputFor(f.focus)
		*in, cmd = in.Update(msg)
		if f.focus == stopClock || f.focus == stopCustom {
			f.schedTouched = f.schedTouched || in.Value() != f.initialSchedText(f.focus)
		}
	}
	if f.state() == before {
		return cmd
	}
	return tea.Batch(cmd, m.schedPreviewCmd())
}

// initialSchedText is the clock or custom field as opened.
func (f *fleetSchedForm) initialSchedText(stop schedStop) string {
	if stop == stopCustom {
		return f.origSchedule
	}
	if ss, ok := cronedit.ParseSimple(f.origSchedule); ok {
		if ss.Kind == cronedit.KindHourly {
			return strconv.Itoa(ss.Minute)
		}
		return fmt.Sprintf("%02d:%02d", ss.Hour, ss.Minute)
	}
	return ""
}

// setKind switches the picker segment. Leaving Custom for a picker kind
// starts from the picker's own fields; a raw spec is never rewritten unless
// the user changes the segment or the fields.
func (f *fleetSchedForm) setKind(kind int) {
	if kind == f.kind {
		return
	}
	f.schedTouched = true
	prev := f.kind
	f.kind = kind
	clock := strings.TrimSpace(f.clock.Value())
	switch {
	case kind == schedKindHourly && strings.Contains(clock, ":"):
		_, mins, _ := strings.Cut(clock, ":")
		f.clock.SetValue(strings.TrimLeft(mins, "0"))
		if f.clock.Value() == "" {
			f.clock.SetValue("0")
		}
	case kind != schedKindHourly && kind != schedKindCustom && (prev == schedKindHourly || clock == ""):
		n, err := strconv.Atoi(clock)
		if err != nil {
			n = 0
		}
		f.clock.SetValue(fmt.Sprintf("09:%02d", n%60))
	}
	if kind == schedKindCustom && strings.TrimSpace(f.custom.Value()) == "" {
		if spec, err := f.specFor(prev); err == nil {
			f.custom.SetValue(spec)
		}
	}
	if kind == schedKindDays && !slices.Contains(f.days[:], true) {
		for i := 0; i < 5; i++ {
			f.days[i] = true
		}
	}
	f.applyFocus()
}

// specFor renders the picker as kind would.
func (f *fleetSchedForm) specFor(kind int) (string, error) {
	cur := f.kind
	f.kind = kind
	defer func() { f.kind = cur }()
	return f.spec()
}

// schedOpenEditor is ctrl+e: edit the prompt in $EDITOR (a prompt_key job
// starts from the key's text and becomes an inline prompt).
func (m *FleetModel) schedOpenEditor() tea.Cmd {
	f := m.schedForm
	text := f.prompt.Value()
	if strings.TrimSpace(text) == "" && f.promptKey != "" {
		if t, err := m.schedStore().PromptKeyText(f.promptKey); err == nil {
			text = t
		}
	}
	tmp, err := os.CreateTemp("", "agentctl-schedule-*.md")
	if err != nil {
		f.err = "$EDITOR: " + err.Error()
		return nil
	}
	path := tmp.Name()
	_, werr := tmp.WriteString(text)
	cerr := tmp.Close()
	cmd := tkEditorCmd(path)
	if werr != nil || cerr != nil || cmd == nil {
		_ = os.Remove(path)
		if cmd == nil {
			f.err = "set $EDITOR to edit the prompt in your editor"
		} else {
			f.err = "$EDITOR: could not write the draft"
		}
		return nil
	}
	return fleetSchedExec(cmd, func(err error) tea.Msg { return fleetSchedEditorMsg{path: path, err: err} })
}

func (m *FleetModel) applySchedEditor(msg fleetSchedEditorMsg) tea.Cmd {
	data, rerr := os.ReadFile(msg.path)
	_ = os.Remove(msg.path)
	f := m.schedForm
	if f == nil {
		return nil
	}
	switch {
	case msg.err != nil:
		f.err = "$EDITOR: " + msg.err.Error()
		return nil
	case rerr != nil:
		f.err = "$EDITOR: " + rerr.Error()
		return nil
	}
	text := strings.TrimSuffix(string(data), "\n")
	if text == f.prompt.Value() {
		return nil
	}
	f.prompt.SetValue(text)
	if strings.TrimSpace(text) != "" {
		f.promptKey = "" // converted to an inline prompt
	}
	f.focusStop(stopPrompt)
	return m.schedPreviewCmd()
}

// pasteSchedForm routes a paste to the focused text field.
func (m *FleetModel) pasteSchedForm(msg tea.PasteMsg) tea.Cmd {
	f := m.schedForm
	if f.review || f.saving || f.discard {
		return nil
	}
	var cmd tea.Cmd
	switch {
	case f.focus == stopPrompt:
		f.prompt, cmd = f.prompt.Update(msg)
	case f.inputFor(f.focus) != nil:
		in := f.inputFor(f.focus)
		*in, cmd = in.Update(msg)
		if f.focus == stopClock || f.focus == stopCustom {
			f.schedTouched = true
		}
	default:
		return nil
	}
	return tea.Batch(cmd, m.schedPreviewCmd())
}

// ── Rendering ────────────────────────────────────────────────────────────────

const schedLabelW = 12

// The worktree phrases of the cron approval popup and the phone.
const (
	schedWorktreeOn  = "a worktree per fire"
	schedWorktreeOff = "runs in the project checkout"
)

// schedFormLines renders the form (or its review) in w columns, scrolled so
// the focused field is inside h lines.
func (m FleetModel) schedFormLines(w, h int) []string {
	f := m.schedForm
	if f == nil {
		return nil
	}
	if f.review || f.saving {
		return m.schedReviewLines(w)
	}
	var out []string
	focusLine := 0
	errs := f.errors()
	fieldErrs := map[schedStop][]string{}
	var general []string
	for _, e := range errs {
		if stop, ok := f.stopForField(e.Field); ok {
			fieldErrs[stop] = append(fieldErrs[stop], e.Message)
		} else {
			general = append(general, e.Message)
		}
	}
	title := "New schedule"
	switch f.mode {
	case schedFormEdit:
		title = "Edit " + f.original
	case schedFormDup:
		title = "Duplicate of " + f.original + " (saved paused)"
	}
	out = append(out, fleetTitleStyle.Render(truncateToWidth(title, w)), "")
	for _, g := range general {
		out = append(out, fleetErrStyle.Render(wrapText(g, w)))
	}
	vw := max(w-schedLabelW-1, 8)
	row := func(stop schedStop, label, value string) {
		marker := "  "
		lst := fleetCountStyle
		if stop == f.focus {
			marker = cursorActiveStyle.Render("▸") + " "
			lst = fleetGroupNameStyle
			focusLine = len(out)
		}
		lines := strings.Split(value, "\n")
		out = append(out, marker+lst.Render(padRight(label, schedLabelW-2))+" "+lines[0])
		for _, l := range lines[1:] {
			out = append(out, strings.Repeat(" ", schedLabelW+1)+l)
		}
		for _, e := range fieldErrs[stop] {
			out = append(out, strings.Repeat(" ", schedLabelW+1)+fleetErrStyle.Render(truncateToWidth(e, vw)))
		}
	}
	input := func(stop schedStop) string {
		in := *f.inputFor(stop)
		in.SetWidth(max(vw-1, 1))
		return in.View()
	}
	if f.mode == schedFormEdit {
		out = append(out, "  "+fleetCountStyle.Render(padRight("Name", schedLabelW-2))+" "+f.original+fleetDimStyle.Render("  (cannot change)"))
	} else {
		row(stopName, "Name", input(stopName))
	}
	dir := fleetErrStyle.Render(errSchedNoProject.Error())
	if len(f.dirs) > 0 {
		d := f.dirs[f.dirIdx]
		dir = "‹ " + fleetGroupNameStyle.Render(projectOf(d)) + " › " + fleetDimStyle.Render(truncateToWidth(tildify(d), max(vw-lipgloss.Width(projectOf(d))-5, 4)))
	}
	row(stopDir, "Project", dir)
	row(stopAgent, "Agent", input(stopAgent))
	pr := f.prompt
	pr.SetWidth(max(vw, 8))
	row(stopPrompt, "Prompt", pr.View())
	row(stopKind, "Schedule", segmented(schedKindLabels, f.kind, f.focus == stopKind))
	switch f.kind {
	case schedKindCustom:
		row(stopCustom, "  Cron", input(stopCustom))
	case schedKindDays:
		row(stopDays, "  Days", m.dayChips())
		row(stopClock, "  Time", input(stopClock))
	case schedKindHourly:
		row(stopClock, "  Minute", input(stopClock)+fleetDimStyle.Render(" past each hour"))
	default:
		row(stopClock, "  Time", input(stopClock))
	}
	out = append(out, strings.Repeat(" ", schedLabelW+1)+fleetDimStyle.Render(truncateToWidth(f.previewLine(), vw)))
	job, _ := f.job()
	adv := "▸ Advanced"
	if f.advOpen {
		adv = "▾ Advanced"
	}
	summary := advancedSummary(job)
	if summary == "" {
		summary = "defaults"
	}
	row(stopAdvanced, adv, fleetDimStyle.Render(truncateToWidth(summary, vw-2)))
	if f.advOpen {
		wt := "[x] " + schedWorktreeOn
		if !f.worktree {
			wt = "[ ] " + schedWorktreeOff
		}
		row(stopWorktree, "Worktree", wt)
		row(stopModel, "Model", input(stopModel))
		row(stopEffort, "Effort", input(stopEffort))
		row(stopBackend, "Backend", input(stopBackend))
		row(stopAccount, "Account", input(stopAccount))
		row(stopTimeout, "Timeout", input(stopTimeout))
		row(stopOverlap, "Overlap", segmented(schedOverlaps, f.overlap, f.focus == stopOverlap)+fleetDimStyle.Render("  while a run is live"))
		row(stopCatchup, "Catch-up", segmented(schedCatchups, f.catchup, f.focus == stopCatchup)+fleetDimStyle.Render("  after missed fires"))
		row(stopUsage5h, "Usage 5h", input(stopUsage5h))
		row(stopUsage7d, "Usage 7d", input(stopUsage7d))
	}
	out = append(out, "")
	switch {
	case f.discard:
		out = append(out, fleetBlockedStyle.Render("discard changes? y/n"))
	case f.err != "":
		out = append(out, fleetErrStyle.Render(truncateToWidth(f.err, w)))
	default:
		out = append(out, fleetDimStyle.Render(truncateToWidth("^s review · esc cancel", w)))
	}
	return scrollTo(out, focusLine, h)
}

// previewLine is the live line under the schedule: words · time zone ·
// next fires.
func (f *fleetSchedForm) previewLine() string {
	if f.preview == nil {
		return "…"
	}
	p := f.preview
	for _, e := range f.errors() {
		if e.Field == cron.FieldSchedule {
			return "—"
		}
	}
	words := p.Words
	if words == "" {
		words = p.Schedule
	}
	parts := []string{words, "local time"}
	if p.TimeZone != "" {
		parts[1] = p.TimeZone
	}
	if len(p.NextFiresText) > 0 {
		parts = append(parts, "next "+strings.Join(p.NextFiresText, ", "))
	}
	return strings.Join(parts, " · ")
}

// dayChips renders M T W T F S S with the chip cursor.
func (m FleetModel) dayChips() string {
	f := m.schedForm
	parts := make([]string, 7)
	for i, l := range schedDayLetters {
		st := fleetDimStyle
		if f.days[i] {
			st = fleetSuccessStyle.Bold(true)
		}
		cell := st.Render(l)
		if f.focus == stopDays && i == f.dayCur {
			cell = "[" + cell + "]"
		} else {
			cell = " " + cell + " "
		}
		parts[i] = cell
	}
	return strings.Join(parts, "")
}

// segmented renders choices with the chosen one highlighted.
func segmented(labels []string, cur int, focused bool) string {
	parts := make([]string, len(labels))
	for i, l := range labels {
		switch {
		case i == cur && focused:
			parts[i] = cursorActiveStyle.Reverse(true).Render(" " + l + " ")
		case i == cur:
			parts[i] = fleetGroupNameStyle.Underline(true).Render(" " + l + " ")
		default:
			parts[i] = fleetDimStyle.Render(" " + l + " ")
		}
	}
	return strings.Join(parts, "")
}

func padRight(s string, w int) string {
	if d := w - lipgloss.Width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// scrollTo keeps line focus inside a window of h lines.
func scrollTo(lines []string, focus, h int) []string {
	if h <= 0 || len(lines) <= h {
		return lines
	}
	start := 0
	if focus >= h-2 {
		start = min(focus-h+3, len(lines)-h)
	}
	return lines[start : start+h]
}

// schedReviewLines is the review: what runs, where, when, unattended, the
// next fires, and the prompt (on an edit, only the changed fields).
func (m FleetModel) schedReviewLines(w int) []string {
	f := m.schedForm
	p := f.preview
	job := p.Job
	var out []string
	add := func(s ...string) { out = append(out, s...) }
	title := "Review new schedule " + job.Name
	if f.mode == schedFormEdit {
		title = "Review changes to " + f.original
	}
	add(fleetTitleStyle.Render(truncateToWidth(title, w)), "")
	wt := schedWorktreeOn
	if !job.UseWorktree() {
		wt = schedWorktreeOff
	}
	words := p.Words
	if words == "" {
		words = p.Schedule
	}
	perm := f.permMode
	if perm == "" {
		perm = "default"
	}
	until := "until paused."
	if !job.IsEnabled() {
		until = "once resumed (it is saved paused)."
	}
	if p.TimeZone != "" {
		words += " " + p.TimeZone
	}
	b := fleetGroupNameStyle.Bold(true)
	sentence := fmt.Sprintf("Runs %s in %s (%s), %s, unattended, permission mode %s, %s",
		b.Render(job.Agent), b.Render(tildify(job.Dir)), wt, b.Render(words), perm, until)
	add(wrapText(sentence, w), "")
	tz := p.TimeZone
	if tz == "" {
		tz = "local time"
	}
	add(fleetCountStyle.Render("Next fires ("+tz+")"), wrapText(strings.Join(p.NextFiresText, " · "), w), "")
	prompt := job.Prompt
	if job.PromptKey != "" {
		prompt = "(context key " + job.PromptKey + ", read at every fire)"
	}
	if f.mode == schedFormEdit {
		add(fleetCountStyle.Render("Changes"))
		for _, c := range p.Changed {
			if c == cron.FieldPrompt || c == "prompt_key" {
				continue
			}
			add(truncateToWidth("  "+c+": "+fieldValue(job, c), w))
		}
		if slices.Contains(p.Changed, cron.FieldPrompt) || slices.Contains(p.Changed, "prompt_key") {
			add("", fleetCountStyle.Render("Prompt (changed)"), wrapText(prompt, w))
		}
	} else {
		if adv := advancedSummary(job); adv != "" {
			add(fleetCountStyle.Render("Advanced"), wrapText(adv, w), "")
		}
		add(fleetCountStyle.Render("Prompt"), wrapText(prompt, w))
	}
	add("")
	switch {
	case f.saving:
		add(fleetDimStyle.Render("saving…"))
	case f.mode == schedFormEdit:
		add(renderHint(fleetHint{"y", "save changes"}) + fleetDimStyle.Render(" · ") + renderHint(fleetHint{"n", "back"}))
	default:
		add(renderHint(fleetHint{"y", "schedule it"}) + fleetDimStyle.Render(" · ") + renderHint(fleetHint{"n", "back"}))
	}
	return out
}

// fieldValue is a job field as the review shows it.
func fieldValue(j cron.Job, field string) string {
	switch field {
	case cron.FieldSchedule:
		if w := cron.Describe(j.Schedule); w != "" {
			return w + " (" + j.Schedule + ")"
		}
		return j.Schedule
	case cron.FieldDir:
		return tildify(j.Dir)
	case cron.FieldAgent:
		return j.Agent
	case cron.FieldModel:
		return orNoneText(j.Model)
	case cron.FieldEffort:
		return orNoneText(j.Effort)
	case cron.FieldBackend:
		return orNoneText(j.Backend)
	case cron.FieldAccount:
		return orNoneText(j.Account)
	case cron.FieldTimeout:
		return orNoneText(j.Timeout)
	case cron.FieldOverlap:
		return j.Overlap
	case cron.FieldCatchup:
		return j.Catchup
	case cron.FieldSkipIfUsageAbove:
		if j.SkipIfUsageAbove == nil {
			return "global"
		}
		return usageText(*j.SkipIfUsageAbove)
	case "worktree":
		if j.UseWorktree() {
			return "on (" + schedWorktreeOn + ")"
		}
		return "off (" + schedWorktreeOff + ")"
	case "enabled":
		if j.IsEnabled() {
			return "enabled"
		}
		return "paused"
	}
	return ""
}

func orNoneText(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
