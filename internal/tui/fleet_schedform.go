package tui

import (
	"fmt"
	"os"
	"path/filepath"
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
	"github.com/blsmth/agentctl/internal/launchopts"
	"github.com/blsmth/agentctl/internal/prompttemplate"
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
	stopSource
	stopPrompt
	stopTemplate
	stopStartTmpl
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
	stopTimeoutCustom
)

// fleetSchedFormHints are the footer hints while the form is open.
var (
	// ^s review comes first: the footer drops hints from the middle, so the
	// way forward and the way out always stay.
	fleetSchedFormHints = []fleetHint{
		{"^s", "review"}, {"tab", "next"}, {"←/→", "choose"}, {"esc", "cancel"},
	}
	fleetSchedReviewHints = []fleetHint{{"y", "save"}, {"n", "back"}}
	fleetSchedYesNoHints  = []fleetHint{{"y", "yes"}, {"n", "no"}}
)

// Seams: the editor runs through tea.ExecProcess (tests replace it), and
// the templates a project sees come from prompttemplate.List.
var (
	fleetSchedExec      = tea.ExecProcess
	fleetSchedTemplates = func(dir string) []prompttemplate.Template {
		list, _, err := prompttemplate.List(dir)
		if err != nil {
			return nil
		}
		return list
	}
)

// Prompt sources, in segment order.
const (
	schedSourcePrompt = iota
	schedSourceTemplate
)

var schedSourceLabels = []string{"Own prompt", "Template (linked, not editable)"}

// fleetSchedForm is the open form.
type fleetSchedForm struct {
	mode     schedFormMode
	original string // stored name (edit) or the duplicated job's name
	base     cron.Job

	name, clock, custom, tmout textinput.Model
	// The cyclers: agent, the launch pins, the timeout presets (tmout is the
	// custom text) and the two usage thresholds.
	agent, model, effort, backend, account, tmoutSel, u5, u7 schedCycler
	prompt                                                   textarea.Model
	// source is the prompt source (schedSourcePrompt or schedSourceTemplate);
	// tmpl picks the template among templates, the ones the project sees.
	source    int
	tmpl      schedCycler
	templates []prompttemplate.Template
	// start picks the template copied into an own prompt (Enter applies it);
	// startAsk is the template waiting on a y/n because the prompt already has
	// text; startFrom is the last one applied; fromTmpl the fields it filled,
	// which a project change must not reset to the default.
	start     schedCycler
	startAsk  string
	startFrom string
	fromTmpl  map[schedStop]bool
	// init holds each text field's value as opened: an untouched field
	// saves the stored value verbatim.
	init map[schedStop]string
	env  schedEnv
	// touched is the fields the user changed; their errors show at once, all
	// of them after ctrl+s (or from the start on an edit).
	touched    map[schedStop]bool
	showErrs   bool
	agentNames func(dir string) []string
	groups     map[string]string
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
	var save tea.Cmd
	if !m.sched.loaded {
		save = m.schedLoadCmd()
	}
	f := &fleetSchedForm{
		mode: mode, init: map[schedStop]string{}, touched: map[schedStop]bool{}, fromTmpl: map[schedStop]bool{},
		env: fleetSchedEnvLoad(), agentNames: m.schedAgentNames, showErrs: mode == schedFormEdit,
	}
	f.groups = m.schedGroupAgents()
	var job cron.Job
	switch {
	case st != nil:
		job = cloneJob(st.Job)
		f.original = st.Name
		if dir == "" {
			dir = job.Dir
		}
	default:
		job = cron.Job{}
	}
	f.dirs = m.schedProjectDirs(dir)
	if st == nil {
		job.Agent = f.defaultAgent(f.dirs, 0)
	}
	if mode == schedFormDup {
		job.Name = dupName(job.Name)
		off := false
		job.Enabled = &off
	}
	job.Normalize()
	f.base = job

	f.name = newSchedInput(job.Name, "nightly-triage")
	f.prompt = newSchedPrompt(job.Prompt)
	f.promptKey = job.PromptKey
	if job.Template != "" {
		f.source = schedSourceTemplate
	}
	f.templates = fleetSchedTemplates(f.curDir())
	f.setupCyclers(job)
	f.worktree = job.UseWorktree()
	f.overlap = max(slices.Index(schedOverlaps, job.Overlap), 0)
	f.catchup = max(slices.Index(schedCatchups, job.Catchup), 0)
	f.overlapInit, f.catchupInit = f.overlap, f.catchup

	f.clock = newSchedInput("", "07:30")
	f.custom = newSchedInput(job.Schedule, "*/30 9-17 * * 1-5")
	f.origSchedule = job.Schedule
	f.setSchedule(job.Schedule, mode == schedFormNew)

	for stop, in := range map[schedStop]*textinput.Model{stopName: &f.name, stopTimeoutCustom: &f.tmout} {
		f.init[stop] = in.Value()
	}
	for _, stop := range schedCyclerStops {
		f.init[stop] = f.cyclerFor(stop).value()
	}
	f.init[stopPrompt] = f.prompt.Value()
	f.advOpen = advancedSummary(job) != ""
	f.focus = f.stops()[0]
	f.applyFocus()
	f.fingerprint = f.state()
	m.schedForm = f
	m.jumpErr = ""
	return tea.Batch(save, m.schedPreviewCmd())
}

// schedCyclerStops are the fields drawn as cyclers.
var schedCyclerStops = []schedStop{stopAgent, stopTemplate, stopModel, stopEffort, stopBackend, stopAccount, stopTimeout, stopUsage5h, stopUsage7d}

func (f *fleetSchedForm) cyclerFor(stop schedStop) *schedCycler {
	switch stop {
	case stopAgent:
		return &f.agent
	case stopTemplate:
		return &f.tmpl
	case stopStartTmpl:
		return &f.start
	case stopModel:
		return &f.model
	case stopEffort:
		return &f.effort
	case stopBackend:
		return &f.backend
	case stopAccount:
		return &f.account
	case stopTimeout:
		return &f.tmoutSel
	case stopUsage5h:
		return &f.u5
	case stopUsage7d:
		return &f.u7
	}
	return nil
}

func plainModel(v string) bool {
	b := baseModelName(v)
	return strings.HasPrefix(b, "claude-") || cronedit.ExtraAnthropicAliases[b] || v == "inherit"
}

// baseModelName drops a context-size suffix such as "opus[1m]".
func baseModelName(m string) string {
	if i := strings.Index(m, "["); i > 0 && strings.HasSuffix(m, "]") {
		return m[:i]
	}
	return m
}

func schedAgentOpts(names []string) []schedOpt {
	out := make([]schedOpt, len(names))
	for i, n := range names {
		out[i] = schedOpt{value: n, label: n}
	}
	return out
}

// agentOpts are the agent cycler's options: the project's agents, led by
// "from template" (value "", the template's agent) for a template job.
func (f *fleetSchedForm) agentOpts(dir string) []schedOpt {
	opts := schedAgentOpts(f.agentList(dir))
	if f.source != schedSourceTemplate {
		return opts
	}
	label := "from template"
	if t := f.template(); t != nil && t.Agent != "" {
		label += " (" + t.Agent + ")"
	}
	return append([]schedOpt{{value: "", label: label}}, opts...)
}

// templateOpts are the template cycler's options.
func (f *fleetSchedForm) templateOpts() []schedOpt {
	out := make([]schedOpt, len(f.templates))
	for i, t := range f.templates {
		out[i] = schedOpt{value: t.Name, label: t.Name}
	}
	return out
}

// startOpts are the "start from template" cycler's options: nothing chosen,
// then the templates.
func (f *fleetSchedForm) startOpts() []schedOpt {
	return append([]schedOpt{{value: "", label: "choose a template"}}, f.templateOpts()...)
}

// templateNamed is the listed template called name, nil when none.
func (f *fleetSchedForm) templateNamed(name string) *prompttemplate.Template {
	for i := range f.templates {
		if f.templates[i].Name == name {
			return &f.templates[i]
		}
	}
	return nil
}

// template is the selected template, nil when none (or not found).
func (f *fleetSchedForm) template() *prompttemplate.Template {
	name := f.tmpl.value()
	for i := range f.templates {
		if f.templates[i].Name == name {
			return &f.templates[i]
		}
	}
	return nil
}

// setupCyclers builds every cycler on the job as stored.
func (f *fleetSchedForm) setupCyclers(job cron.Job) {
	dir := f.curDir()
	f.tmpl = newCycler(f.templateOpts(), job.Template, false, nil)
	f.start = newCycler(f.startOpts(), "", false, nil)
	f.agent = newCycler(f.agentOpts(dir), job.Agent, true, plainAgent)
	f.model = newCycler(f.env.modelOpts(job.Agent, job.Backend), job.Model, true, plainModel)
	f.effort = newCycler(f.env.effortOpts(job.Agent), job.Effort, false, nil)
	f.backend = newCycler(f.env.backendOpts(job.Agent), job.Backend, false, nil)
	f.account = newCycler(f.env.accountOpts(job.Agent, dir), job.Account, false, nil)
	cur := job.Timeout
	f.tmout = newSchedInput("", "45m")
	if cur != "" && !slices.Contains(schedTimeoutPresets, cur) {
		f.tmout.SetValue(cur)
		cur = schedTimeoutCustom
	}
	f.tmoutSel = newCycler(timeoutOpts(), cur, false, nil)
	var v5, v7 string
	if t := job.SkipIfUsageAbove; t != nil {
		v5, v7 = usageValue(t.FiveHour), usageValue(t.SevenDay)
	}
	f.u5 = newCycler(usageOpts(f.env.usage.FiveHour, v5), v5, false, nil)
	f.u7 = newCycler(usageOpts(f.env.usage.SevenDay, v7), v7, false, nil)
}

// curDir is the project the form is set to ("" without one).
func (f *fleetSchedForm) curDir() string {
	if len(f.dirs) == 0 {
		return ""
	}
	return f.dirs[f.dirIdx]
}

func (f *fleetSchedForm) agentList(dir string) []string {
	if f.agentNames == nil {
		return nil
	}
	return f.agentNames(dir)
}

// defaultAgent is a new job's agent: the first agent running in the project
// at dirs[i], else the configured default_agent, else the first listed one.
func (f *fleetSchedForm) defaultAgent(dirs []string, i int) string {
	if i < len(dirs) {
		if a := f.groups[schedDirKey(dirs[i])]; a != "" {
			return a
		}
	}
	if f.env.defaultAgent != "" {
		return f.env.defaultAgent
	}
	if i < len(dirs) {
		if names := f.agentList(dirs[i]); len(names) > 0 {
			return names[0]
		}
	}
	return ""
}

// refreshOptions rebuilds the option lists that depend on the project, agent
// and backend, keeping each selection (an unavailable one becomes unknown).
func (f *fleetSchedForm) refreshOptions() {
	dir := f.curDir()
	f.tmpl.setOpts(f.templateOpts(), f.tmpl.value(), nil)
	startCur := f.start.value()
	if f.templateNamed(startCur) == nil {
		startCur = ""
	}
	f.start.setOpts(f.startOpts(), startCur, nil)
	switch {
	case f.source == schedSourceTemplate && f.agent.value() == "":
		f.agent.setOpts(f.agentOpts(dir), "", plainAgent)
	case f.mode != schedFormEdit && !f.touched[stopAgent] && !f.fromTmpl[stopAgent]:
		if a := f.defaultAgent(f.dirs, f.dirIdx); a != "" {
			f.agent.setOpts(f.agentOpts(dir), a, plainAgent)
		}
	default:
		f.agent.setOpts(f.agentOpts(dir), f.agent.value(), plainAgent)
	}
	agent := f.agent.value()
	// The backend first: the model list is the one of the backend as it is
	// after the refresh.
	f.backend.setOpts(f.env.backendOpts(agent), f.backend.value(), nil)
	f.model.setOpts(f.env.modelOpts(agent, f.backend.value()), f.model.value(), plainModel)
	f.effort.setOpts(f.env.effortOpts(agent), f.effort.value(), nil)
	f.account.setOpts(f.env.accountOpts(agent, dir), f.account.value(), nil)
}

// fitModelToBackend runs after the user changes the backend: a model the new
// backend does not offer goes back to the agent default, one it offers stays.
// Never called when the form opens, so an edit shows its stored model as is.
func (f *fleetSchedForm) fitModelToBackend() {
	cur := f.model.value()
	if cur == "" {
		return
	}
	agent := f.agent.value()
	opts := f.env.modelOpts(agent, f.backend.value())
	if slices.ContainsFunc(opts, func(o schedOpt) bool { return o.value == cur }) {
		return
	}
	if f.env.effectiveBackend(agent, f.backend.value()) == launchopts.BackendAnthropic && plainModel(cur) {
		return // a full Anthropic model id the list does not spell out
	}
	f.model.setOpts(opts, "", plainModel)
}

// schedDirKey is a project dir with symlinks resolved, so a group's dir and
// a job's dir match whichever spelling each uses.
func schedDirKey(d string) string {
	d = filepath.Clean(d)
	if r, err := filepath.EvalSymlinks(d); err == nil {
		return r
	}
	return d
}

// schedGroupAgents maps each project dir to the first agent running in it.
func (m FleetModel) schedGroupAgents() map[string]string {
	out := map[string]string{}
	for _, g := range m.groups {
		for _, r := range g.Rows {
			if !r.Shell {
				out[schedDirKey(g.ProjectDir)] = fleet.BaseName(r)
				break
			}
		}
	}
	return out
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
	out = append(out, stopDir, stopAgent, stopSource)
	if f.source == schedSourceTemplate {
		out = append(out, stopTemplate)
	} else {
		if len(f.templates) > 0 {
			out = append(out, stopStartTmpl)
		}
		out = append(out, stopPrompt)
	}
	out = append(out, stopKind)
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
		out = append(out, stopWorktree)
		if f.showBackend() {
			out = append(out, stopBackend)
		}
		out = append(out, stopModel, stopEffort)
		if f.showAccount() {
			out = append(out, stopAccount)
		}
		out = append(out, stopTimeout)
		if f.timeoutCustom() {
			out = append(out, stopTimeoutCustom)
		}
		out = append(out, stopOverlap, stopCatchup, stopUsage5h, stopUsage7d)
	}
	return out
}

// showBackend: the row is hidden when no backend is configured (and the job
// pins none).
func (f *fleetSchedForm) showBackend() bool {
	return len(f.env.opts.Backends) > 0 || f.backend.value() != ""
}

// showAccount: hidden when no account is configured (and the job pins none).
func (f *fleetSchedForm) showAccount() bool {
	return len(f.env.opts.Accounts) > 0 || f.account.value() != ""
}

func (f *fleetSchedForm) timeoutCustom() bool { return f.tmoutSel.value() == schedTimeoutCustom }

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
	case stopClock:
		return &f.clock
	case stopCustom:
		return &f.custom
	case stopTimeoutCustom:
		return &f.tmout
	}
	return nil
}

var schedTextStops = []schedStop{stopName, stopClock, stopCustom, stopTimeoutCustom}

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
	parts := []string{f.prompt.Value(), strconv.Itoa(f.source), strconv.Itoa(f.dirIdx), strconv.Itoa(f.kind), fmt.Sprint(f.days),
		strconv.FormatBool(f.worktree), strconv.Itoa(f.overlap), strconv.Itoa(f.catchup)}
	for _, s := range schedTextStops {
		parts = append(parts, f.inputFor(s).Value())
	}
	for _, s := range schedCyclerStops {
		parts = append(parts, f.cyclerFor(s).value())
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
		if f.base.Dir != "" && filepath.Clean(f.base.Dir) == j.Dir {
			j.Dir = f.base.Dir // the stored spelling ("…/x/" stays): not a change
		}
	}
	j.Agent = strings.TrimSpace(f.agent.value())
	j.Template = ""
	switch p := f.prompt.Value(); {
	case f.source == schedSourceTemplate:
		j.Prompt, j.PromptKey, j.Template = "", "", f.tmpl.value()
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
	j.Model = f.model.value()
	j.Effort = f.effort.value()
	j.Backend = f.backend.value()
	j.Account = f.account.value()
	j.Timeout = f.timeoutValue()
	if f.overlap != f.overlapInit {
		j.Overlap = schedOverlaps[f.overlap]
	}
	if f.catchup != f.catchupInit {
		j.Catchup = schedCatchups[f.catchup]
	}
	if f.u5.value() != f.init[stopUsage5h] || f.u7.value() != f.init[stopUsage7d] {
		var t config.CronUsageThresholds
		t.FiveHour, _ = strconv.ParseFloat(f.u5.value(), 64)
		t.SevenDay, _ = strconv.ParseFloat(f.u7.value(), 64)
		j.SkipIfUsageAbove = nil
		if t.FiveHour != 0 || t.SevenDay != 0 {
			j.SkipIfUsageAbove = &t
		}
	}
	return j, errs
}

// timeoutValue is the timeout the cycler (and its custom text) selects; an
// untouched custom text saves the stored value verbatim.
func (f *fleetSchedForm) timeoutValue() string {
	if !f.timeoutCustom() {
		return f.tmoutSel.value()
	}
	return f.text(stopTimeoutCustom, f.base.Timeout)
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
	case cron.FieldPrompt, cron.FieldTemplate:
		if f.source == schedSourceTemplate {
			return stopTemplate, true
		}
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
		if f.timeoutCustom() {
			return stopTimeoutCustom, true
		}
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
	m.schedCardHidden = "" // back to the card
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
	if f.startAsk != "" {
		name := f.startAsk
		f.startAsk = ""
		if t := f.templateNamed(name); t != nil && (key == "y" || key == "Y") {
			return m.applyStartTemplate(t)
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
		if c := f.cyclerFor(f.focus); c != nil && c.filter != "" {
			c.clearFilter()
			return nil
		}
		if f.dirty() {
			f.discard = true
		} else {
			m.closeSchedForm()
		}
		return nil
	case "ctrl+s":
		f.showErrs = true
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
		case "down", "up":
			if f.focus == stopClock && f.kind != schedKindCustom {
				f.stepClock(map[string]int{"up": 15, "down": -15}[key])
				return m.schedPreviewCmd()
			}
			f.move(map[string]int{"down": 1, "up": -1}[key])
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
	case stopSource:
		switch key {
		case "left":
			f.source = schedSourcePrompt
		case "right":
			f.source = schedSourceTemplate
		case "space":
			f.source = 1 - f.source
		case "enter":
			f.move(1)
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
	case stopStartTmpl:
		if key == "enter" {
			return m.schedStartTemplate()
		}
		if !f.cyclerKey(msg, key) {
			return nil
		}
	case stopAgent, stopTemplate, stopModel, stopEffort, stopBackend, stopAccount, stopTimeout, stopUsage5h, stopUsage7d:
		if !f.cyclerKey(msg, key) {
			return nil
		}
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
	return m.schedFormChanged(before, cmd)
}

// schedFormChanged is the tail of every change to the focused field, from a
// key or the mouse: when the form state moved off before, the field counts as
// touched, the options that depend on it are rebuilt and a preview runs.
func (m *FleetModel) schedFormChanged(before string, cmd tea.Cmd) tea.Cmd {
	f := m.schedForm
	if f.state() == before {
		return cmd
	}
	f.touched[f.focus] = true
	if f.focus == stopSource && f.source == schedSourceTemplate && f.mode != schedFormEdit && !f.touched[stopAgent] {
		// A new job switched to a template runs its agent unless one was
		// picked (an edit keeps the stored agent).
		f.agent.setOpts(f.agentOpts(f.curDir()), "", plainAgent)
	}
	switch f.focus {
	case stopDir:
		f.templates = fleetSchedTemplates(f.curDir()) // the prompt text stays
		f.refreshOptions()
	case stopAgent, stopSource, stopTemplate:
		f.refreshOptions()
	case stopBackend:
		f.refreshOptions()
		f.fitModelToBackend()
	}
	return tea.Batch(cmd, m.schedPreviewCmd())
}

// cyclerKey runs key on the focused cycler; false when it consumed the key
// without a change worth a preview (enter moved on).
func (f *fleetSchedForm) cyclerKey(msg tea.KeyPressMsg, key string) bool {
	c := f.cyclerFor(f.focus)
	switch key {
	case "left":
		c.step(-1)
	case "right", "space":
		c.step(1)
	case "enter":
		f.move(1)
		return false
	case "backspace":
		c.backspace()
	case "esc", "ctrl+u":
		c.clearFilter()
	default:
		if t := msg.Key().Text; t != "" && key != "ctrl+h" {
			c.typeText(t)
		}
	}
	return true
}

// stepClock moves the time by delta minutes (15-minute steps), wrapping; the
// hourly kind steps the minute past the hour.
func (f *fleetSchedForm) stepClock(delta int) {
	clock := strings.TrimSpace(f.clock.Value())
	if f.kind == schedKindHourly {
		n, err := strconv.Atoi(strings.TrimPrefix(clock, ":"))
		if err != nil {
			n = 0
		}
		f.clock.SetValue(strconv.Itoa(((n+delta)%60 + 60) % 60))
	} else {
		hs, ms, ok := strings.Cut(clock, ":")
		h, herr := strconv.Atoi(hs)
		mn, merr := strconv.Atoi(ms)
		total := 9 * 60
		if ok && herr == nil && merr == nil {
			total = h*60 + mn
		}
		total = ((total+delta)%(24*60) + 24*60) % (24 * 60)
		f.clock.SetValue(fmt.Sprintf("%02d:%02d", total/60, total%60))
	}
	f.schedTouched = true
	f.touched[stopClock] = true
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
	if f.review || f.saving || f.discard || f.startAsk != "" {
		return nil
	}
	var cmd tea.Cmd
	switch {
	case f.focus == stopPrompt:
		f.prompt, cmd = f.prompt.Update(msg)
	case f.cyclerFor(f.focus) != nil:
		f.cyclerFor(f.focus).typeText(strings.TrimSpace(msg.Content))
		f.touched[f.focus] = true
		cmd = nil
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

const schedLabelW = 14

// The worktree phrases of the cron approval popup and the phone.
const (
	schedWorktreeOn  = "a worktree per fire"
	schedWorktreeOff = "runs in the project checkout"
)

// fieldErrorsShown is the errors the form shows now: all of them after
// ctrl+s (and on an edit), else only those of fields the user changed.
func (f *fleetSchedForm) fieldErrorsShown() (byStop map[schedStop][]string, general []string) {
	byStop = map[schedStop][]string{}
	for _, e := range f.errors() {
		stop, ok := f.stopForField(e.Field)
		switch {
		case ok && (f.showErrs || f.touched[stop] || (stop == stopClock && f.schedTouched)):
			msg := e.Message
			if stop == stopAgent {
				// The cycler lists the agents: drop the validator's list.
				msg, _, _ = strings.Cut(msg, "; available agents")
			}
			byStop[stop] = append(byStop[stop], msg)
		case !ok && f.showErrs:
			general = append(general, e.Message)
		}
	}
	return byStop, general
}

// schedFormLines renders the form (or its review) in w columns, scrolled so
// the focused field is inside h lines. Fields are grouped What / When /
// Advanced.
func (m FleetModel) schedFormLines(w, h int) []string {
	return m.schedFormLayout(w, h).lines
}

// schedFormLayout is schedFormLines with the click targets on its lines: the
// one rendering both the screen and the mouse hit testing read.
func (m FleetModel) schedFormLayout(w, h int) schedLayout {
	f := m.schedForm
	if f == nil {
		return schedLayout{}
	}
	if f.review || f.saving {
		return m.schedReviewLayout(w)
	}
	var out schedLayout
	focusLine := 0
	fieldErrs, general := f.fieldErrorsShown()
	title := "New schedule"
	switch f.mode {
	case schedFormEdit:
		title = "Edit " + f.original
	case schedFormDup:
		title = "Duplicate of " + f.original + " (saved paused)"
	}
	out.add(fleetTitleStyle.Render(truncateToWidth(title, w)), "")
	for _, g := range general {
		out.add(fleetErrStyle.Render(wrapText(g, w)))
	}
	vw := max(w-schedLabelW-1, 8)
	indent := strings.Repeat(" ", schedLabelW+1)
	// row draws a field: its label, then value (one screen line per value
	// line). hits are value targets, their lines and columns relative to the
	// value; the rest of the row focuses the field (Advanced: folds it).
	row := func(stop schedStop, label, value string, hits ...schedHit) {
		marker := "  "
		lst := fleetCountStyle
		if stop == f.focus {
			marker = cursorActiveStyle.Render("▸") + " "
			lst = fleetGroupNameStyle
			focusLine = len(out.lines)
		}
		base := len(out.lines)
		lines := strings.Split(value, "\n")
		out.add(marker + lst.Render(padRight(label, schedLabelW-2)) + " " + truncateToWidth(lines[0], vw))
		for _, l := range lines[1:] {
			out.add(indent + l)
		}
		for _, ht := range hits {
			ht.stop = stop
			ht.line += base
			ht.x0 += schedLabelW + 1
			ht.x1 = min(ht.x1, vw) + schedLabelW + 1
			out.hits = append(out.hits, ht)
		}
		kind := schedHitFocus
		if stop == stopAdvanced {
			kind = schedHitToggle
		}
		for i := range lines {
			out.hits = append(out.hits, schedHit{line: base + i, x0: 0, x1: schedHitEnd, stop: stop, kind: kind})
		}
		for _, e := range fieldErrs[stop] {
			out.add(indent + fleetErrStyle.Render(truncateToWidth(e, vw)))
		}
	}
	input := func(stop schedStop) string {
		in := *f.inputFor(stop)
		in.SetWidth(max(vw-1, 1))
		return in.View()
	}
	// cyc draws a cycler after a prefix of pre columns: ‹ steps back, the
	// rest of it forward.
	cyc := func(stop schedStop, pre int) (string, []schedHit) {
		v := f.cyclerFor(stop).view(f.focus == stop)
		return v, arrowHits(pre, lipgloss.Width(v))
	}
	header := func(name string) { out.add("", "  "+fleetDimStyle.Render(name)) }

	header("What")
	if f.mode == schedFormEdit {
		out.add("  " + fleetCountStyle.Render(padRight("Name", schedLabelW-2)) + " " + f.original + fleetDimStyle.Render("  (cannot change)"))
	} else {
		row(stopName, "Name", input(stopName))
	}
	dir := fleetErrStyle.Render(errSchedNoProject.Error())
	var dirHits []schedHit
	if len(f.dirs) > 0 {
		d := f.dirs[f.dirIdx]
		dir = "‹ " + fleetGroupNameStyle.Render(projectOf(d)) + " › " + fleetDimStyle.Render(truncateToWidth(tildify(d), max(vw-lipgloss.Width(projectOf(d))-5, 4)))
		dirHits = arrowHits(0, lipgloss.Width(dir))
	}
	row(stopDir, "Project", dir, dirHits...)
	v, hs := cyc(stopAgent, 0)
	row(stopAgent, "Agent", v, hs...)
	v, hs = segmentedLayout(schedSourceLabels, f.source, f.focus == stopSource, vw)
	row(stopSource, "Source", v, hs...)
	if f.source == schedSourceTemplate {
		v, hs = cyc(stopTemplate, 0)
		row(stopTemplate, "Template", v, hs...)
		switch t := f.template(); {
		case len(f.templates) == 0:
			out.add(indent + fleetDimStyle.Render(truncateToWidth("no templates for this project (agentctl template add)", vw)))
		case t == nil:
			out.add(indent + fleetDimStyle.Render(truncateToWidth("pick a template (←/→ or type to filter)", vw)))
		default:
			if t.Description != "" {
				out.add(indent + fleetDimStyle.Render(truncateToWidth(t.Description, vw)))
			}
			for _, l := range clampLines(wrapText(t.Prompt, vw), 4) {
				out.add(indent + fleetDimStyle.Render(l))
			}
			out.add(indent + fleetDimStyle.Render(truncateToWidth("read at every fire; the agent, model and effort set here win", vw)))
		}
	} else {
		if len(f.templates) > 0 {
			v, hs = cyc(stopStartTmpl, 0)
			row(stopStartTmpl, "Start from", v, hs...)
			switch {
			case f.startAsk != "":
				out.add(indent + fleetBlockedStyle.Render(truncateToWidth("replace the prompt with "+f.startAsk+"? y/n", vw)))
			case f.focus == stopStartTmpl && f.start.value() != "":
				hint := "⏎ copy it into the prompt (you can edit it)"
				out.hits = append(out.hits, schedHit{line: len(out.lines), x0: schedLabelW + 1, x1: schedLabelW + 1 + lipgloss.Width(hint), stop: stopStartTmpl, kind: schedHitApply})
				out.add(indent + fleetDimStyle.Render(truncateToWidth(hint, vw)))
			case f.startFrom != "":
				out.add(indent + fleetDimStyle.Render(truncateToWidth("copied from template "+f.startFrom+"; saved as your own prompt", vw)))
			}
		}
		pr := f.prompt
		pr.SetWidth(max(vw, 8))
		row(stopPrompt, "Prompt", pr.View())
		switch {
		case f.promptKey != "" && strings.TrimSpace(f.prompt.Value()) == "":
			out.add(indent + fleetDimStyle.Render(truncateToWidth("from context key "+f.promptKey, vw)))
		case f.focus == stopPrompt:
			out.hits = append(out.hits, schedHit{line: len(out.lines), x0: schedLabelW + 1, x1: schedLabelW + 1 + lipgloss.Width(schedEditorHint), stop: stopPrompt, kind: schedHitEditor})
			out.add(indent + fleetDimStyle.Render(schedEditorHint))
		}
	}

	header("When")
	v, hs = segmentedLayout(schedKindLabels, f.kind, f.focus == stopKind, vw)
	row(stopKind, "Schedule", v, hs...)
	switch f.kind {
	case schedKindCustom:
		row(stopCustom, "  Cron", input(stopCustom))
	case schedKindDays:
		v, hs = m.dayChipsLayout()
		row(stopDays, "  Days", v, hs...)
		row(stopClock, "  Time", input(stopClock))
	case schedKindHourly:
		row(stopClock, "  Minute", input(stopClock)+fleetDimStyle.Render(" past each hour"))
	default:
		row(stopClock, "  Time", input(stopClock))
	}
	if f.focus == stopClock && f.kind != schedKindCustom {
		out.add(indent + fleetDimStyle.Render("↑/↓ ±15 min"))
	}
	out.add(indent + fleetDimStyle.Render(truncateToWidth(f.previewLine(), vw)))

	out.add("")
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
		row(stopWorktree, "  Worktree", wt, schedHit{x1: lipgloss.Width(wt), kind: schedHitToggle})
		if f.showBackend() {
			v, hs = cyc(stopBackend, 0)
			row(stopBackend, "  Backend", v, hs...)
		}
		v, hs = cyc(stopModel, 0)
		row(stopModel, "  Model", v, hs...)
		v, hs = cyc(stopEffort, 0)
		row(stopEffort, "  Effort", v, hs...)
		if f.showAccount() {
			v, hs = cyc(stopAccount, 0)
			row(stopAccount, "  Account", v, hs...)
		}
		v, hs = cyc(stopTimeout, 0)
		row(stopTimeout, "  Timeout", v, hs...)
		if f.timeoutCustom() {
			row(stopTimeoutCustom, "    Custom", input(stopTimeoutCustom)+fleetDimStyle.Render(" e.g. 90m"))
		}
		v, hs = segmentedLayout(schedOverlaps, f.overlap, f.focus == stopOverlap, vw)
		row(stopOverlap, "  Overlap", v+fleetDimStyle.Render("  while a run is live"), hs...)
		v, hs = segmentedLayout(schedCatchups, f.catchup, f.focus == stopCatchup, vw)
		row(stopCatchup, "  Catch-up", v+fleetDimStyle.Render("  after missed fires"), hs...)
		const usagePre = "5h "
		v, hs = cyc(stopUsage5h, lipgloss.Width(usagePre))
		row(stopUsage5h, "  Skip above", fleetDimStyle.Render(usagePre)+v, hs...)
		v, hs = cyc(stopUsage7d, lipgloss.Width(usagePre))
		row(stopUsage7d, "", fleetDimStyle.Render("7d ")+v, hs...)
	}
	out.add("")
	switch {
	case f.discard:
		out.add(fleetBlockedStyle.Render("discard changes? y/n"))
	case f.err != "":
		out.add(fleetErrStyle.Render(truncateToWidth(f.err, w)))
	}
	out.scroll(focusLine, h)
	return out
}

// schedEditorHint is the clickable line under the focused prompt.
const schedEditorHint = "^e editor"

// arrowHits are a cycler's targets drawn at column pre, w columns wide: its
// ‹ (and the space after it) steps back, anything right of it forward.
func arrowHits(pre, w int) []schedHit {
	if w <= 0 {
		return nil
	}
	return []schedHit{
		{x0: pre, x1: pre + 2, kind: schedHitPrev},
		{x0: pre + 2, x1: pre + w, kind: schedHitNext},
	}
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

// dayChipsLayout renders M T W T F S S with the chip cursor, plus one
// toggle target per chip.
func (m FleetModel) dayChipsLayout() (string, []schedHit) {
	f := m.schedForm
	parts := make([]string, 7)
	hits := make([]schedHit, 7)
	col := 0
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
		cw := lipgloss.Width(cell)
		hits[i] = schedHit{x0: col, x1: col + cw, kind: schedHitDay, arg: i}
		col += cw
	}
	return strings.Join(parts, ""), hits
}

// segmented renders choices with the chosen one marked: a ● prefix and bold
// (filled while focused), so it reads without colour. Segments that do not
// fit in w columns continue on the next line.
func segmented(labels []string, cur int, focused bool, w int) string {
	s, _ := segmentedLayout(labels, cur, focused, w)
	return s
}

// segmentedLayout is segmented plus one target per segment (its line and
// columns within the rendered text).
func segmentedLayout(labels []string, cur int, focused bool, w int) (string, []schedHit) {
	var lines []string
	var hits []schedHit
	var line string
	lw := 0
	for i, l := range labels {
		var seg string
		switch {
		case i == cur && focused:
			seg = cursorActiveStyle.Reverse(true).Render(" ●" + l + " ")
		case i == cur:
			seg = fleetGroupNameStyle.Bold(true).Render(" ●" + l + " ")
		default:
			seg = fleetDimStyle.Render("  " + l + " ")
		}
		sw := lipgloss.Width(seg)
		if lw > 0 && w > 0 && lw+sw > w {
			lines, line, lw = append(lines, line), "", 0
		}
		hits = append(hits, schedHit{line: len(lines), x0: lw, x1: lw + sw, kind: schedHitSegment, arg: i})
		line += seg
		lw += sw
	}
	return strings.Join(append(lines, line), "\n"), hits
}

func padRight(s string, w int) string {
	if d := w - lipgloss.Width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// scrollStart is the first of n lines a window of h lines keeping line focus
// inside it starts at, -1 when all fit.
func scrollStart(n, focus, h int) int {
	if h <= 0 || n <= h {
		return -1
	}
	start := 0
	if focus >= h-2 {
		start = min(focus-h+3, n-h)
	}
	return start
}

// schedReviewLayout is the review: what runs, where, when and how, the next
// fires, and the prompt (on an edit, only the changed fields as old → new),
// with its y/n buttons as targets.
func (m FleetModel) schedReviewLayout(w int) schedLayout {
	f := m.schedForm
	p := f.preview
	job := p.Job
	var out schedLayout
	add := out.add
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
	const lw = 6
	rw := max(w-lw-1, 10)
	kv := func(k, v string) {
		lines := strings.Split(wrapText(v, rw), "\n")
		add(fleetCountStyle.Render(padRight(k, lw)) + " " + lines[0])
		for _, l := range lines[1:] {
			add(strings.Repeat(" ", lw+1) + l)
		}
	}
	agent := job.Agent
	tmpl := f.template()
	if agent == "" && job.Template != "" {
		agent = "the template's agent"
		if tmpl != nil && tmpl.Agent != "" {
			agent = tmpl.Agent + " (from template " + job.Template + ")"
		}
	}
	kv("Runs", b.Render(agent)+" in "+b.Render(tildify(job.Dir))+" ("+wt+")")
	kv("When", b.Render(words))
	kv("Mode", fmt.Sprintf("unattended, permission mode %s, %s", perm, until))
	tz := p.TimeZone
	if tz == "" {
		tz = "local time"
	}
	add("", fleetCountStyle.Render("Next fires ("+tz+")"), wrapText(strings.Join(p.NextFiresText, " · "), w), "")
	prompt := job.Prompt
	switch {
	case job.Template != "":
		prompt = "(template " + job.Template + ", read from the project at every fire)"
		if tmpl != nil {
			prompt += "\n\n" + tmpl.Prompt
		}
	case job.PromptKey != "":
		prompt = "(context key " + job.PromptKey + ", read at every fire)"
	}
	promptChanged := slices.Contains(p.Changed, cron.FieldPrompt) || slices.Contains(p.Changed, "prompt_key") ||
		slices.Contains(p.Changed, cron.FieldTemplate)
	if f.mode == schedFormEdit {
		add(fleetCountStyle.Render("Changes"))
		for _, c := range p.Diff {
			if c.Field == cron.FieldPrompt || c.Field == "prompt_key" || c.Field == cron.FieldTemplate {
				continue
			}
			add(truncateToWidth("  "+padRight(c.Field, 10)+" "+f.reviewValue(c.Field, c.Old)+fleetDimStyle.Render(" → ")+f.reviewValue(c.Field, c.New), w))
		}
		if promptChanged {
			add("", fleetCountStyle.Render("Prompt (changed)"), wrapText(prompt, w))
		}
	} else {
		if adv := advancedSummary(job); adv != "" {
			add(fleetCountStyle.Render("Advanced"), wrapText(adv, w), "")
		}
		add(fleetCountStyle.Render("Prompt"), wrapText(prompt, w))
	}
	if len(p.Warnings) > 0 {
		add("")
		for _, wn := range p.Warnings {
			add(fleetBlockedStyle.Render(truncateToWidth("⚠ "+wn.Field+": "+wn.Message, w)))
		}
	}
	add("")
	if f.saving {
		add(fleetDimStyle.Render("saving…"))
		return out
	}
	yes := fleetHint{"y", "schedule it"}
	if f.mode == schedFormEdit {
		yes = fleetHint{"y", "save changes"}
	}
	no := fleetHint{"n", "back"}
	sep := fleetDimStyle.Render(" · ")
	yw, sw := lipgloss.Width(renderHint(yes)), lipgloss.Width(sep)
	line := len(out.lines)
	out.hits = append(out.hits,
		schedHit{line: line, x0: 0, x1: yw, kind: schedHitKey, key: "y"},
		schedHit{line: line, x0: yw + sw, x1: yw + sw + lipgloss.Width(renderHint(no)), kind: schedHitKey, key: "n"})
	add(renderHint(yes) + sep + renderHint(no))
	return out
}

// reviewValue is one side of a change as the review shows it: the name of
// the resolved default for an unset pin, words for a schedule.
func (f *fleetSchedForm) reviewValue(field, v string) string {
	if c := f.cyclerFor(map[string]schedStop{
		cron.FieldAgent: stopAgent, cron.FieldModel: stopModel, cron.FieldEffort: stopEffort,
		cron.FieldBackend: stopBackend, cron.FieldAccount: stopAccount,
	}[field]); c != nil && field != cron.FieldAgent {
		return c.labelOf(v)
	}
	switch field {
	case cron.FieldAgent:
		if v == "" {
			return "from template"
		}
	case cron.FieldSchedule:
		if w := cron.Describe(v); w != "" {
			return w
		}
	case cron.FieldDir:
		return tildify(v)
	case cron.FieldTimeout:
		return orNoneText(v)
	case cron.FieldSkipIfUsageAbove:
		if v == "" {
			return "global"
		}
		return strings.NewReplacer("5h 100%", "5h off", "7d 100%", "7d off").Replace(v)
	}
	return v
}

func orNoneText(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// schedStartTemplate is Enter on the "start from" picker: copy the chosen
// template into the prompt, after a y/n when the prompt already has text.
func (m *FleetModel) schedStartTemplate() tea.Cmd {
	f := m.schedForm
	t := f.templateNamed(f.start.value())
	if t == nil {
		f.err = "choose a template first (←/→ or type to filter)"
		return nil
	}
	cur := f.prompt.Value()
	if (strings.TrimSpace(cur) != "" && cur != t.Prompt) || (strings.TrimSpace(cur) == "" && f.promptKey != "") {
		f.startAsk = t.Name
		return nil
	}
	return m.applyStartTemplate(t)
}

// applyStartTemplate copies t's body into the prompt editor and fills the
// agent, model and effort from its frontmatter, but only fields the user has
// not touched (and, on an edit or duplicate, only ones that are still
// empty). The job saves as an ordinary own-prompt job.
func (m *FleetModel) applyStartTemplate(t *prompttemplate.Template) tea.Cmd {
	f := m.schedForm
	f.prompt.SetValue(t.Prompt)
	f.promptKey = ""
	f.startFrom = t.Name
	f.touched[stopPrompt] = true
	// free: the field may be overwritten by the template.
	free := func(stop schedStop, cur string) bool {
		return !f.touched[stop] && (f.mode == schedFormNew || cur == "")
	}
	if t.Agent != "" && free(stopAgent, f.agent.value()) {
		f.agent.setOpts(f.agentOpts(f.curDir()), t.Agent, plainAgent)
		f.fromTmpl[stopAgent] = true
	}
	f.refreshOptions()
	if t.Model != "" && free(stopModel, f.model.value()) {
		f.model.setOpts(f.env.modelOpts(f.agent.value(), f.backend.value()), t.Model, plainModel)
		f.advOpen = true
	}
	if t.Effort != "" && free(stopEffort, f.effort.value()) {
		f.effort.setOpts(f.env.effortOpts(f.agent.value()), t.Effort, nil)
		f.advOpen = true
	}
	f.focusStop(stopPrompt)
	return m.schedPreviewCmd()
}
