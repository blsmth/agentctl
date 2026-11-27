package tui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/cron"
	"github.com/blsmth/agentctl/internal/cronedit"
	"github.com/blsmth/agentctl/internal/fleet"
)

// schedNow is a Monday morning, local time.
var schedNow = time.Date(2026, 11, 23, 6, 0, 0, 0, time.Local)

// schedFixture is a temp jobs file and state dir behind the store seam, with
// run-now, the viewer and the editor stubbed.
type schedFixture struct {
	mu         sync.Mutex
	jobsPath   string
	stateDir   string
	project    string
	project2   string
	installed  bool
	enabled    bool
	agentErr   error
	runEntry   cron.Entry
	runErr     error
	ran        []string
	shown      []string
	promptKeys map[string]string // context keys the store resolves, when set
}

// schedWork counts form previews in flight (see schedWorkTrack).
var schedWork = newPreviewGate()

// previewGate is a counter with a Wait that, unlike sync.WaitGroup, may be
// called while the count is rising from zero.
type previewGate struct {
	mu sync.Mutex
	c  *sync.Cond
	n  int
}

func newPreviewGate() *previewGate {
	g := &previewGate{}
	g.c = sync.NewCond(&g.mu)
	return g
}

func (g *previewGate) Add() { g.mu.Lock(); g.n++; g.mu.Unlock() }
func (g *previewGate) Done() {
	g.mu.Lock()
	g.n--
	g.c.Broadcast()
	g.mu.Unlock()
}
func (g *previewGate) Wait() {
	g.mu.Lock()
	for g.n > 0 {
		g.c.Wait()
	}
	g.mu.Unlock()
}

// Store-backed commands outlive a test that drops them (a drive timeout),
// so the tracker is installed once and never swapped, which would race with
// their goroutines. schedDrive waits on it instead of guessing how long a
// loaded machine needs.
func init() {
	schedWorkTrack = func() func() { schedWork.Add(); return schedWork.Done }
}

func newSchedFixture(t *testing.T) *schedFixture {
	t.Helper()
	InitStyles()
	// Slide frames answer at once: schedDrive settles them, so no test
	// depends on a real timer beating its drive timeout.
	savedTick := schedDrawerTick
	schedDrawerTick = func(gen int) tea.Cmd {
		return func() tea.Msg { return schedDrawerTickMsg{gen: gen} }
	}
	t.Cleanup(func() { schedDrawerTick = savedTick })
	dir := t.TempDir()
	f := &schedFixture{
		jobsPath:  filepath.Join(dir, "schedules.json"),
		stateDir:  filepath.Join(dir, "state", "cron"),
		project:   filepath.Join(dir, "code", "acme-api"),
		project2:  filepath.Join(dir, "code", "billing"),
		installed: true,
		enabled:   true,
	}
	for _, d := range []string{f.project, f.project2} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	saved := []any{fleetSchedStore, fleetSchedRun, fleetNow, showLiveViewer, hubWindowWidth, hubZoomSync, fleetSchedPermMode}
	t.Cleanup(func() {
		fleetSchedStore = saved[0].(func(func(string, string) error) cronedit.Store)
		fleetSchedRun = saved[1].(cronedit.RunFunc)
		fleetNow = saved[2].(func() time.Time)
		showLiveViewer = saved[3].(func(string, string, int) error)
		hubWindowWidth = saved[4].(func() (int, error))
		hubZoomSync = saved[5].(func(bool))
		fleetSchedPermMode = saved[6].(func(string) string)
	})
	fleetNow = func() time.Time { return schedNow }
	fleetSchedStore = func(validate func(string, string) error) cronedit.Store {
		st := f.base()
		st.ValidateAgent = func(name, dir string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.agentErr != nil {
				return f.agentErr
			}
			if validate != nil {
				return validate(name, dir)
			}
			return nil
		}
		return st
	}
	fleetSchedRun = func(_ context.Context, name string) (cron.Entry, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.ran = append(f.ran, name)
		e := f.runEntry
		e.Job = name
		return e, f.runErr
	}
	fleetSchedPermMode = func(string) string { return "auto" }
	showLiveViewer = func(session, window string, _ int) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.shown = append(f.shown, session+"|"+window)
		return nil
	}
	hubWindowWidth = func() (int, error) { return 0, errors.New("no tmux in tests") }
	hubZoomSync = func(bool) {}
	return f
}

func (f *schedFixture) base() cronedit.Store {
	return cronedit.Store{
		JobsPath: func() (string, error) { return f.jobsPath, nil },
		StateDir: func() (string, error) { return f.stateDir, nil },
		Now:      func() time.Time { return schedNow },
		Config: func() config.CronConfig {
			en := f.enabled
			return config.CronConfig{Enabled: &en, MinInterval: "15m"}
		},
		SchedulerInstalled: func() bool { return f.installed },
		ResolvePromptKey: func(key string) (string, error) {
			if v, ok := f.promptKeys[key]; ok {
				return v, nil
			}
			return cronedit.ResolvePromptKey(key)
		},
	}
}

func (f *schedFixture) create(t *testing.T, j cron.Job) {
	t.Helper()
	if j.Dir == "" {
		j.Dir = f.project
	}
	if j.Agent == "" {
		j.Agent = "go-engineer"
	}
	if j.Prompt == "" && j.PromptKey == "" {
		j.Prompt = "Triage new issues."
	}
	// A prompt_key job is legacy (Create would pin it): save it with a
	// placeholder prompt, then write the key back as an older binary did.
	key := j.PromptKey
	if key != "" {
		j.Prompt, j.PromptKey = "placeholder", ""
	}
	if _, err := f.base().Create(j); err != nil {
		t.Fatalf("seeding %s: %v", j.Name, err)
	}
	if key == "" {
		return
	}
	path, _ := f.base().JobsPath()
	jobs, err := cron.LoadJobs(path)
	if err != nil {
		t.Fatal(err)
	}
	i := jobs.Find(j.Name)
	jobs.Jobs[i].Prompt, jobs.Jobs[i].PromptKey = "", key
	if err := cron.SaveJobs(path, jobs); err != nil {
		t.Fatal(err)
	}
}

func (f *schedFixture) run(t *testing.T, e cron.Entry) {
	t.Helper()
	if err := cron.AppendRun(f.stateDir, e); err != nil {
		t.Fatal(err)
	}
}

func (f *schedFixture) job(t *testing.T, name string) cron.Job {
	t.Helper()
	j, err := f.base().Get(name)
	if err != nil {
		t.Fatalf("get %s: %v", name, err)
	}
	return j
}

func (f *schedFixture) rawJob(t *testing.T, name string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(f.jobsPath)
	if err != nil {
		t.Fatal(err)
	}
	var file struct{ Jobs []map[string]any }
	if err := jsonUnmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	for _, j := range file.Jobs {
		if j["name"] == name {
			return j
		}
	}
	t.Fatalf("no job %s in the file", name)
	return nil
}

// seedFour creates a failed, a running, an upcoming and a paused job.
func (f *schedFixture) seedFour(t *testing.T) {
	t.Helper()
	f.create(t, cron.Job{Name: "upcoming", Schedule: "0 9 * * 1-5"})
	f.create(t, cron.Job{Name: "nightly", Schedule: "30 2 * * *", Dir: f.project2, Agent: "python-engineer"})
	f.create(t, cron.Job{Name: "babysit", Schedule: "5 * * * *"})
	off := false
	f.create(t, cron.Job{Name: "weekly", Schedule: "0 8 * * 1", Enabled: &off})
	f.run(t, cron.Entry{Time: schedNow.Add(-3 * time.Hour), Job: "nightly", Result: cron.Failed("launch"), Note: "tmux: no server"})
	f.run(t, cron.Entry{Time: schedNow.Add(-55 * time.Minute), Job: "babysit", Result: cron.ResultFired, WindowID: "@9"})
	f.run(t, cron.Entry{Time: schedNow.Add(-26 * time.Hour), Job: "upcoming", Result: cron.ResultSkippedUsage, Note: "5h 82% ≥ 80%"})
}

// schedModel is a hub with one project group (an agent row and a running
// cron window) at a width that leaves room for the right pane.
func (f *schedFixture) model(t *testing.T) FleetModel {
	t.Helper()
	m := newFleetModel()
	m.termWidth, m.termHeight = 150, 45
	m.liveLeft, m.liveRight = 150, 0 // a roomy list pane: the drawer is 80% of it
	m.configureSplitPane()
	m.loaded = true
	a := testRow("go-engineer", "agentctl-acme-api", "idle", "")
	a.WindowID = "@1"
	c := testRow("go-engineer:2", "agentctl-acme-api", "working", "")
	c.WindowID, c.ScheduledBy = "@9", "babysit"
	m.groups = []fleet.Group{{Session: "agentctl-acme-api", Project: "acme-api", ProjectDir: f.project, RuntimeDir: filepath.Join(f.project, ".agentctl"), Rows: []fleet.Row{a, c}}}
	m.rebuildLines()
	m.resolveSelection()
	return m
}

// drive feeds cmd's messages back into m, recursing into batches. Commands
// that do not answer quickly (cursor blink ticks) are dropped.
func schedDrive(t *testing.T, m FleetModel, cmd tea.Cmd) FleetModel {
	t.Helper()
	if cmd == nil {
		return m
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-ch:
	case <-time.After(40 * time.Millisecond):
		// A cursor blink or slide tick never answers in time and is dropped.
		// A write in flight does, just later on a loaded machine (-race):
		// wait for it, or the test sees the form still saving.
		// A preview in flight is waited for (it ends in a message); anything
		// still silent after that is a blink and is dropped.
		schedWork.Wait()
		select {
		case msg = <-ch:
		default:
			if m.schedForm == nil || !m.schedForm.saving && m.sched.busy == "" {
				return m
			}
			select {
			case msg = <-ch:
			case <-time.After(10 * time.Second):
				t.Fatal("a schedule write never answered")
			}
		}
	}
	switch msg := msg.(type) {
	case nil:
		return m
	case tea.BatchMsg:
		for _, c := range msg {
			m = schedDrive(t, m, c)
		}
		return m
	case schedDrawerTickMsg:
		// Frames are not run on the wall clock in tests: settle the slide.
		m.schedAnimOn, m.schedAnimP = false, 1
		return m
	case fleetSchedLoadedMsg, fleetSchedDetailMsg, fleetSchedWroteMsg, fleetSchedRanMsg,
		fleetSchedPreviewMsg, fleetSchedSavedMsg, fleetLiveMsg:
		next, c := m.Update(msg)
		return schedDrive(t, next.(FleetModel), c)
	}
	return m
}

func schedKeyMsg(k string) tea.KeyPressMsg {
	switch k {
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "ctrl+s":
		return tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}
	case "ctrl+e":
		return tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl}
	case "ctrl+u":
		return tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}
	}
	return tea.KeyPressMsg{Code: []rune(k)[0], Text: k}
}

// press sends keys one by one and drives what they return.
func schedPress(t *testing.T, m FleetModel, keys ...string) FleetModel {
	t.Helper()
	for _, k := range keys {
		next, cmd := m.Update(schedKeyMsg(k))
		m = schedDrive(t, next.(FleetModel), cmd)
	}
	return m
}

// typeText types s into the focused field.
func schedType(t *testing.T, m FleetModel, s string) FleetModel {
	t.Helper()
	for _, r := range s {
		next, cmd := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = schedDrive(t, next.(FleetModel), cmd)
	}
	return m
}

// clearField empties the focused text input.
func schedClear(t *testing.T, m FleetModel) FleetModel {
	t.Helper()
	return schedPress(t, m, "ctrl+u")
}

// showSchedules loads the jobs (as the hub does on start) and switches every
// tabbed project to its Schedules tab, the view most schedule tests drive.
func showSchedules(t *testing.T, m FleetModel) FleetModel {
	t.Helper()
	// Run the load to the end: on a loaded machine (-race) it can take longer
	// than schedDrive waits for a command.
	if cmd := m.schedLoadCmd(); cmd != nil {
		next, c := m.Update(cmd())
		m = schedDrive(t, next.(FleetModel), c)
	}
	if !m.sched.loaded {
		t.Fatalf("schedules loaded=%v err=%q", m.sched.loaded, m.sched.err)
	}
	for _, key := range m.schedGroupKeys() {
		m.setSchedTab(key, true)
	}
	m.schedOtherOpen = true
	m.relayout()
	return m
}

// setAllTabs puts every tabbed project on its Schedules (or Sessions) tab.
func setAllTabs(m *FleetModel, sched bool) {
	for _, key := range m.schedGroupKeys() {
		m.setSchedTab(key, sched)
	}
	m.relayout()
}

func schedKeys(m FleetModel) []string {
	var out []string
	for _, l := range m.lines {
		switch l.kind {
		case fleetLineSchedLabel:
			out = append(out, "SL:"+l.label)
		case fleetLineSchedHead:
			out = append(out, "SH:"+l.label)
		case fleetLineSchedule:
			if l.schedNew {
				out = append(out, "S:+new")
			} else {
				out = append(out, "S:"+l.sched.Name)
			}
		}
	}
	return out
}

// selectSched selects the schedule line whose key is name; fleetSchedNewSel
// matches the first "+ new schedule" line of any block.
func selectSched(t *testing.T, m FleetModel, name string) FleetModel {
	t.Helper()
	for pass := 0; pass < 2; pass++ {
		if pass == 1 {
			setAllTabs(&m, true) // the job's tab was not showing
		}
		for i, l := range m.lines {
			key := schedLineKey(l)
			if l.kind == fleetLineSchedule && (key == name || (name == fleetSchedNewSel && l.schedNew)) {
				m.selectLine(i)
				next, cmd := m.Update(nil) // runs the post-update drawer sync
				return schedDrive(t, next.(FleetModel), cmd)
			}
		}
	}
	t.Fatalf("no schedule line %q in %v", name, schedKeys(m))
	return m
}

func selectAgent(t *testing.T, m FleetModel, windowID string) FleetModel {
	t.Helper()
	for pass := 0; pass < 2; pass++ {
		if pass == 1 {
			setAllTabs(&m, false) // the row's tab was not showing
		}
		for i, l := range m.lines {
			if l.kind == fleetLineRow && l.row.WindowID == windowID {
				m.selectLine(i)
				next, cmd := m.Update(nil)
				return schedDrive(t, next.(FleetModel), cmd)
			}
		}
	}
	t.Fatalf("no row %s", windowID)
	return m
}

func TestSchedulesLoadAndSort(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	m := f.model(t)
	// Jobs load without any toggle (the hub does it on start).
	m = showSchedules(t, m)
	m.schedOtherOpen = false
	m.relayout()
	// acme-api's jobs go on its Schedules tab; nightly runs in billing, which
	// has no session, so it goes in the bottom block, collapsed.
	want := []string{"S:babysit", "S:upcoming", "S:weekly", "S:+new", "SH:Schedules · other projects (1)"}
	if got := schedKeys(m); !slices.Equal(got, want) {
		t.Errorf("lines = %v, want %v (running, by next fire, paused; failed elsewhere)", got, want)
	}
	m.schedOtherOpen = true
	m.relayout()
	out := ansi.Strip(m.renderList())
	for _, s := range []string{fleetSchedGlyphFailed + " nightly", fleetSchedGlyphRunning + " babysit", fleetSchedGlyphOn + " upcoming", fleetSchedGlyphPaused + " weekly", "paused", "in 3h"} {
		if !strings.Contains(out, s) {
			t.Errorf("list missing %q:\n%s", s, out)
		}
	}
	if got := m.viewState().SchedTabs; !slices.Equal(got, []string{resolveDir(f.project)}) {
		t.Errorf("view state tabs = %v", got)
	}
	if !strings.Contains(m.viewEnv(), `"sched_tabs"`) || strings.Contains(m.viewEnv(), `"schedules"`) {
		t.Errorf("hot reload env = %s", m.viewEnv())
	}
	// T flips the group back to Sessions: no job line in it.
	m = selectSched(t, m, "upcoming")
	m = schedPress(t, m, fleetSchedulesKey)
	if got := schedKeys(m); !slices.Equal(got, []string{"SH:Schedules · other projects (1)", "S:nightly"}) {
		t.Errorf("after T = %v", got)
	}
}

func TestSchedulesViewStateSurvivesReexec(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	m := showSchedules(t, f.model(t))
	s, ok := parseHubView([]byte(strings.TrimPrefix(m.viewEnv(), HubViewEnv+"=")))
	if !ok || !slices.Equal(s.SchedTabs, []string{resolveDir(f.project)}) {
		t.Fatalf("view env = %q", m.viewEnv())
	}
	m2 := f.model(t)
	m2.applyViewState(s)
	m2 = schedDrive(t, m2, m2.schedLoadCmd())
	if want := []string{"S:babysit", "S:upcoming", "S:weekly", "S:+new", "SH:Schedules · other projects (1)", "S:nightly"}; !slices.Equal(schedKeys(m2), want) {
		t.Errorf("a restored hub lists %v, want %v", schedKeys(m2), want)
	}
}

func TestSchedulesZeroJobsMissingFileAndHints(t *testing.T) {
	f := newSchedFixture(t)
	f.installed, f.enabled = false, false
	m := showSchedules(t, f.model(t))
	if _, err := os.Stat(f.jobsPath); !os.IsNotExist(err) {
		t.Fatal("the fixture should have no schedules.json")
	}
	// No job anywhere: nothing schedule-related is drawn, not even the
	// warnings, and the header shows no tabs.
	if got := schedKeys(m); len(got) != 0 {
		t.Errorf("lines = %v, want none", got)
	}
	if _, ok := m.groupTab(m.groups[0]); ok {
		t.Error("a project with no jobs shows tabs")
	}
	if out := ansi.Strip(m.renderList()); strings.Contains(out, "Schedules") {
		t.Errorf("list mentions Schedules:\n%s", out)
	}
	// t on the project's header still opens the new-schedule form there.
	m.selectLine(0)
	m = schedPress(t, m, fleetCronKey)
	if m.schedForm == nil || m.schedForm.mode != schedFormNew {
		t.Fatal("t on the header should open the form")
	}
	if fm := m.schedForm; fm.dirs[fm.dirIdx] != f.project {
		t.Errorf("form dir = %q, want the group's project %q", fm.dirs[fm.dirIdx], f.project)
	}
	// T on a project with no jobs says so and changes nothing.
	m.schedForm = nil
	m = schedPress(t, m, fleetSchedulesKey)
	if !strings.Contains(m.infoMsg, "no schedules") {
		t.Errorf("info = %q", m.infoMsg)
	}
}

func TestSchedulesLoadErrorShown(t *testing.T) {
	f := newSchedFixture(t)
	if err := os.WriteFile(f.jobsPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := schedDrive(t, f.model(t), nil)
	m = schedDrive(t, m, m.schedLoadCmd())
	if got := schedKeys(m); len(got) != 1 || !strings.Contains(got[0], "parsing") || m.selectedSchedLine() != nil {
		t.Errorf("lines = %v, want the parse error", got)
	}
}

func TestScheduleRowNarrowTruncation(t *testing.T) {
	f := newSchedFixture(t)
	f.create(t, cron.Job{Name: "nightly-dependency-bumps", Schedule: "30 2 * * *", Agent: "python-engineer"})
	f.run(t, cron.Entry{Time: schedNow.Add(-time.Hour), Job: "nightly-dependency-bumps", Result: cron.ResultSkippedOverlap})
	m := showSchedules(t, f.model(t))
	var l fleetLine
	for _, x := range m.lines {
		if x.kind == fleetLineSchedule && !x.schedNew {
			l = x
		}
	}
	// In its project's block the row hangs off the project bar and does not
	// repeat the project name.
	if in := ansi.Strip(m.renderSchedRow(l, false, 150)); !strings.HasPrefix(in, "▎    "+fleetSchedGlyphOn+" nightly") || strings.Contains(in, "acme-api") {
		t.Errorf("in-group row = %q", in)
	}
	// The widths below are the bottom block's row, which names the project.
	l.group, l.schedBlock = fleet.Group{}, fleetSchedOtherKey
	type want struct {
		has, hasNot []string
	}
	cases := map[int]want{
		150: {has: []string{"daily 02:30", "skipped · overlap", "acme-api"}, hasNot: []string{"python-engineer"}},
		85:  {has: []string{"daily 02:30", "skipped · overlap"}, hasNot: []string{"python-engineer", "acme-api"}},
		75:  {has: []string{"daily 02:30"}, hasNot: []string{"python-engineer", "acme-api"}},
		62:  {has: []string{"daily 02:30"}, hasNot: []string{"skipped", "python-engineer"}},
		50:  {has: []string{"daily"}, hasNot: []string{"skipped", "acme-api"}},
		44:  {hasNot: []string{"skipped", "acme-api"}},
	}
	for cw, w := range cases {
		out := m.renderSchedRow(l, false, cw)
		plain := ansi.Strip(out)
		if lipgloss.Width(out) > cw {
			t.Errorf("cw %d: row is %d wide: %q", cw, lipgloss.Width(out), plain)
		}
		for _, s := range append([]string{"nightly-dependency-bumps", "in 20h3"}, w.has...) {
			if !strings.Contains(plain, s) {
				t.Errorf("cw %d: row %q lacks %q", cw, plain, s)
			}
		}
		for _, s := range w.hasNot {
			if strings.Contains(plain, s) {
				t.Errorf("cw %d: row %q should have dropped %q", cw, plain, s)
			}
		}
	}
}

func TestScheduleKeysLeaveAgentRowsAlone(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	m := showSchedules(t, f.model(t))
	m = selectAgent(t, m, "@1")
	before := f.job(t, "upcoming")
	for _, k := range []string{"e", "space", "g", "d"} {
		m = schedPress(t, m, k)
		if m.schedForm != nil || m.schedConfirm != nil || m.sched.busy != "" {
			t.Fatalf("%q on an agent row touched the schedules", k)
		}
	}
	m = schedPress(t, m, "x")
	if !m.closeArmed() || m.schedConfirm != nil {
		t.Error("x on an agent row should still arm close")
	}
	m = schedPress(t, m, "esc")
	m = schedPress(t, m, "n")
	if !m.newArmed() {
		t.Error("n on an agent row should still arm new")
	}
	if !strings.Contains(ansi.Strip(m.renderFooter(m.contentWidth())), "t cron") {
		t.Error("the new menu lacks the cron entry")
	}
	m = schedPress(t, m, "esc")
	if got := f.job(t, "upcoming"); got.IsEnabled() != before.IsEnabled() {
		t.Error("a key on an agent row changed a job")
	}

	// On a schedule row x is delete, not close.
	m = selectSched(t, m, "upcoming")
	m = schedPress(t, m, "x")
	if m.closeArmed() || m.schedConfirm == nil || m.schedConfirm.kind != "delete" {
		t.Errorf("x on a schedule row: close=%v confirm=%+v", m.closeArmed(), m.schedConfirm)
	}
	if out := ansi.Strip(m.renderFooter(m.contentWidth())); !strings.Contains(out, "delete upcoming?") || !strings.Contains(out, "history is kept") {
		t.Errorf("footer = %q", out)
	}
	m = schedPress(t, m, "n")
	if m.schedConfirm != nil {
		t.Error("n should cancel the delete")
	}
	if _, err := f.base().Get("upcoming"); err != nil {
		t.Error("cancelled delete removed the job")
	}
}

func TestScheduleFooterAndHelp(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	m := showSchedules(t, f.model(t))
	m = selectSched(t, m, "upcoming")
	m.termWidth = 300 // room for every hint
	foot := ansi.Strip(fitHints(m.schedHints(), 200))
	for _, s := range []string{"⏎ edit", "space pause", "g run now", "d duplicate", "x delete", "n new"} {
		if !strings.Contains(foot, s) {
			t.Errorf("schedule footer %q lacks %q", foot, s)
		}
	}
	m = selectSched(t, m, "weekly")
	if !strings.Contains(ansi.Strip(fitHints(m.schedHints(), 200)), "space resume") {
		t.Error("a paused row's footer should offer resume")
	}
	var keys []string
	for _, it := range fleetHelpItems() {
		keys = append(keys, it.keys)
	}
	if !slices.Contains(keys, fleetSchedulesKey) {
		t.Error("? reference lacks T")
	}
	// A schedule row's footer offers the way back to Sessions.
	if !strings.Contains(ansi.Strip(fitHints(m.schedHints(), 200)), "T sessions") {
		t.Error("schedule footer lacks T sessions")
	}
	// On the Sessions tab the group's footer offers Schedules.
	m = schedPress(t, m, fleetSchedulesKey)
	if !strings.Contains(ansi.Strip(fitHints(m.footerHints(), 300)), "T schedules") {
		t.Errorf("sessions footer = %q", ansi.Strip(fitHints(m.footerHints(), 300)))
	}
}

func TestSchedulePauseResume(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	m := showSchedules(t, f.model(t))
	m = selectSched(t, m, "upcoming")
	next, cmd := m.Update(schedKeyMsg("space"))
	m = next.(FleetModel)
	if m.sched.busy != "upcoming" || cmd == nil {
		t.Fatal("pause should run as a command")
	}
	if !f.job(t, "upcoming").IsEnabled() {
		t.Fatal("pause wrote before its command ran")
	}
	m = schedDrive(t, m, cmd)
	if f.job(t, "upcoming").IsEnabled() {
		t.Fatal("space did not pause the job")
	}
	if !strings.Contains(m.infoMsg, "paused upcoming") {
		t.Errorf("info = %q", m.infoMsg)
	}
	if m.selSchedule != "upcoming" {
		t.Errorf("selection moved to %q", m.selSchedule)
	}
	m = schedPress(t, m, "space")
	if !f.job(t, "upcoming").IsEnabled() || !strings.Contains(m.infoMsg, "resumed upcoming · next Mon 09:") {
		t.Errorf("resume: enabled=%v info=%q", f.job(t, "upcoming").IsEnabled(), m.infoMsg)
	}
}

func TestScheduleRunNowConfirmsAndShowsSkip(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	m := showSchedules(t, f.model(t))
	m = selectSched(t, m, "upcoming")
	m = schedPress(t, m, "g")
	if out := ansi.Strip(m.renderFooter(m.contentWidth())); !strings.Contains(out, "run upcoming now in") {
		t.Errorf("footer = %q", out)
	}
	m = schedPress(t, m, "n")
	if len(f.ran) != 0 || m.schedConfirm != nil {
		t.Fatal("n should cancel run now")
	}
	f.runEntry = cron.Entry{Result: cron.ResultSkippedOverlap, Note: "babysit-2 is still running"}
	m = schedPress(t, m, "g", "y")
	if !slices.Equal(f.ran, []string{"upcoming"}) {
		t.Fatalf("runs = %v", f.ran)
	}
	if !strings.Contains(m.jumpErr, "skipped: overlap") || !strings.Contains(m.jumpErr, "still running") {
		t.Errorf("skip reason not shown: %q", m.jumpErr)
	}
	f.runEntry = cron.Entry{Result: cron.ResultFired, WindowID: "@12", Session: "agentctl-acme-api"}
	m = schedPress(t, m, "g", "y")
	if !strings.Contains(m.infoMsg, "upcoming fired in acme-api") {
		t.Errorf("info = %q", m.infoMsg)
	}
}

func TestScheduleDelete(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	m := showSchedules(t, f.model(t))
	m = selectSched(t, m, "upcoming")
	m = schedPress(t, m, "x", "y")
	if _, err := f.base().Get("upcoming"); !errors.Is(err, cronedit.ErrNotFound) {
		t.Fatalf("job still there: %v", err)
	}
	if slices.Contains(schedKeys(m), "S:upcoming") {
		t.Error("deleted job still listed")
	}
	if l := m.selectedSchedLine(); l == nil {
		t.Error("the cursor left the section after a delete")
	}
	runs, _ := cron.ReadRuns(f.stateDir, "upcoming")
	if len(runs) == 0 {
		t.Error("delete dropped the run history")
	}
}

func TestScheduleCardDrawerLeavesViewerAlone(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	m := showSchedules(t, f.model(t))
	m = selectAgent(t, m, "@1")
	m.liveShown = "agentctl-acme-api|@1"
	m = selectSched(t, m, "nightly")
	if m.liveShown != "agentctl-acme-api|@1" {
		t.Fatalf("the card changed the viewer: liveShown %q", m.liveShown)
	}
	out := ansi.Strip(m.render())
	for _, s := range []string{"last run failed", "daily 02:30", "python-engineer in", "Next", "Last runs", "failed · launch", "tmux: no server", "Prompt", "Triage new issues."} {
		if !strings.Contains(out, s) {
			t.Errorf("card missing %q:\n%s", s, out)
		}
	}
	for _, l := range strings.Split(m.render(), "\n") {
		if w := lipgloss.Width(l); w > m.termWidth {
			t.Errorf("line is %d wide (terminal %d): %q", w, m.termWidth, ansi.Strip(l))
		}
	}
	if !strings.Contains(out, "go-engineer") || !strings.Contains(out, "│") {
		t.Error("the list should stay visible beside the card")
	}
	if got, want := strings.Count(m.render(), "\n"), strings.Count(m.renderList(), "\n"); got != want {
		t.Errorf("the card changed the frame height: %d rows, list %d", got+1, want+1)
	}

	// The running job's card names its live window.
	m = selectSched(t, m, "babysit")
	if !strings.Contains(ansi.Strip(m.render()), `running: go-engineer:2 "babysit" in acme-api`) {
		t.Errorf("running window not shown:\n%s", ansi.Strip(m.render()))
	}

	// The card always carries the full detail, no key needed.
	if m.sched.detail == nil || m.sched.detail.Name != "babysit" {
		t.Fatal("selecting a job did not load the full card")
	}

	// Back on an agent row the drawer closes; the viewer was never hidden.
	m = selectAgent(t, m, "@1")
	if m.schedPaneOn || m.liveShown != "agentctl-acme-api|@1" {
		t.Errorf("paneOn=%v liveShown=%q", m.schedPaneOn, m.liveShown)
	}
	if m.sched.detail != nil {
		t.Error("the full card outlived its selection")
	}
}

func TestScheduleDrawerFullWidthOnNarrowPane(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	m := showSchedules(t, f.model(t))
	m.liveLeft, m.termWidth = 40, 40
	m.relayout()
	m = selectSched(t, m, "nightly")
	if m.schedDrawerWidth() != m.contentWidth() {
		t.Fatalf("drawer %d wide in a %d pane, want all of it", m.schedDrawerWidth(), m.contentWidth())
	}
	if !strings.Contains(ansi.Strip(m.render()), "Last runs") {
		t.Error("narrow pane: the card should still be drawn")
	}
	m = schedPress(t, m, "e")
	out := ansi.Strip(m.render())
	if !strings.Contains(out, "Edit nightly") {
		t.Errorf("narrow form not drawn:\n%s", out)
	}
	for _, l := range strings.Split(m.render(), "\n") {
		if w := lipgloss.Width(l); w > m.contentWidth() {
			t.Errorf("narrow form line is %d wide: %q", w, ansi.Strip(l))
		}
	}
}

func formOf(t *testing.T, m FleetModel) *fleetSchedForm {
	t.Helper()
	if m.schedForm == nil {
		t.Fatal("no form open")
	}
	return m.schedForm
}

// focus tabs to stop.
func focusStopT(t *testing.T, m FleetModel, stop schedStop) FleetModel {
	t.Helper()
	for range 30 {
		if formOf(t, m).focus == stop {
			return m
		}
		m = schedPress(t, m, "tab")
	}
	t.Fatalf("never reached stop %d", stop)
	return m
}

func TestScheduleCreateViaPickerFromProjectHeader(t *testing.T) {
	f := newSchedFixture(t)
	m := showSchedules(t, f.model(t))
	m.selectLine(0) // the acme-api header
	m = schedPress(t, m, "n", "t")
	fm := formOf(t, m)
	if fm.mode != schedFormNew || fm.dirs[fm.dirIdx] != f.project {
		t.Fatalf("form mode %d dir %v", fm.mode, fm.dirs)
	}
	if !m.schedPaneOn {
		t.Error("the form should open the drawer")
	}
	m = schedType(t, m, "triage")
	m = focusStopT(t, m, stopPrompt)
	m = schedType(t, m, "Triage new issues")
	m = focusStopT(t, m, stopKind)
	if formOf(t, m).kind != schedKindWeekdays {
		t.Fatalf("new form kind = %d, want weekdays", formOf(t, m).kind)
	}
	m = focusStopT(t, m, stopClock)
	m = schedClear(t, m)
	m = schedType(t, m, "07:30")
	out := ansi.Strip(m.render())
	if !strings.Contains(out, "weekdays 07:30") || !strings.Contains(out, "next Mon 23 Nov 07:3") {
		t.Errorf("live preview missing:\n%s", out)
	}
	m = schedPress(t, m, "ctrl+s")
	if !formOf(t, m).review {
		t.Fatalf("ctrl+s did not open the review: err=%q errors=%v", formOf(t, m).err, formOf(t, m).errors())
	}
	if !strings.Contains(ansi.Strip(m.render()), "Review new schedule triage") {
		t.Errorf("review not drawn:\n%s", ansi.Strip(m.render()))
	}
	// The review wraps at the pane width, and where it wraps depends on the
	// temp dir's length: check its words, not its line breaks.
	review := strings.Join(strings.Fields(ansi.Strip(strings.Join(m.schedFormLines(m.schedPaneWidth(), m.termHeight), "\n"))), " ")
	for _, s := range []string{"Review new schedule triage", "Runs go-engineer in", "(a worktree per fire)", "When weekdays 07:30", "unattended, permission mode auto, until paused.", "Next fires", "Triage new issues", "y schedule it"} {
		if !strings.Contains(review, s) {
			t.Errorf("review missing %q:\n%s", s, review)
		}
	}
	next, cmd := m.Update(schedKeyMsg("y"))
	m = next.(FleetModel)
	if !formOf(t, m).saving || cmd == nil {
		t.Fatal("y should save through a command")
	}
	if _, err := f.base().Get("triage"); err == nil {
		t.Fatal("saved before the command ran")
	}
	m = schedDrive(t, m, cmd)
	if m.schedForm != nil {
		t.Fatalf("form still open: %q", m.schedForm.err)
	}
	j := f.job(t, "triage")
	if j.Schedule != "30 7 * * 1-5" || j.Dir != f.project || j.Prompt != "Triage new issues" || !j.IsEnabled() {
		t.Errorf("saved job = %+v", j)
	}
	if m.selSchedule != "triage" || !strings.Contains(m.infoMsg, "scheduled triage · next Mon 07:3") {
		t.Errorf("selection %q info %q", m.selSchedule, m.infoMsg)
	}
}

func TestScheduleFormDaysAndHourlyPicker(t *testing.T) {
	f := newSchedFixture(t)
	m := showSchedules(t, f.model(t))
	m = schedPress(t, m, fleetCronKey)
	m = schedType(t, m, "picks")
	m = focusStopT(t, m, stopPrompt)
	m = schedType(t, m, "x")
	m = focusStopT(t, m, stopKind)
	m = schedPress(t, m, "right") // Days…
	if formOf(t, m).kind != schedKindDays {
		t.Fatalf("kind = %d", formOf(t, m).kind)
	}
	m = focusStopT(t, m, stopDays)
	// Monday to Friday start on; drop Tuesday, add Sunday.
	m = schedPress(t, m, "right", "space", "right", "right", "right", "right", "right", "space")
	j, errs := formOf(t, m).job()
	if len(errs) > 0 || j.Schedule != "0 9 * * 0,1,3,4,5" {
		t.Errorf("days spec = %q %v", j.Schedule, errs)
	}
	m = focusStopT(t, m, stopKind)
	m = schedPress(t, m, "left", "left", "left")
	if formOf(t, m).kind != schedKindHourly {
		t.Fatalf("kind = %d", formOf(t, m).kind)
	}
	m = focusStopT(t, m, stopClock)
	m = schedClear(t, m)
	m = schedType(t, m, "15")
	if j, _ := formOf(t, m).job(); j.Schedule != "15 * * * *" {
		t.Errorf("hourly spec = %q", j.Schedule)
	}
	m = schedClear(t, m)
	m = schedType(t, m, "75")
	if errs := formOf(t, m).errors(); len(errs) == 0 || errs[0].Field != cron.FieldSchedule {
		t.Errorf("minute 75 errors = %v", errs)
	}
	if !strings.Contains(ansi.Strip(m.render()), "minute past the hour") {
		t.Error("the bad minute is not shown inline")
	}
}

func TestScheduleEditRoundTripsHiddenAndUntouchedFields(t *testing.T) {
	f := newSchedFixture(t)
	wt := false
	f.create(t, cron.Job{
		Name: "custom", Schedule: "*/15  9-17 * * 1-5", Prompt: "line one\n\tline two\n",
		Model: "claude-sonnet-5-5", Timeout: "45m", Overlap: cron.OverlapAllow, Worktree: &wt,
		SkipIfUsageAbove: &config.CronUsageThresholds{FiveHour: 72.5},
	})
	before := f.rawJob(t, "custom")
	stateBefore, _ := os.ReadFile(filepath.Join(f.stateDir, "state.json"))
	m := showSchedules(t, f.model(t))
	m = selectSched(t, m, "custom")
	m = schedPress(t, m, "e")
	fm := formOf(t, m)
	if fm.kind != schedKindCustom || slices.Contains(fm.stops(), stopName) {
		t.Fatalf("edit form: kind %d stops %v", fm.kind, fm.stops())
	}
	// Change only the agent.
	m = focusStopT(t, m, stopAgent)
	m = schedClear(t, m)
	m = schedType(t, m, "python-engineer")
	m = schedPress(t, m, "ctrl+s")
	// The review pane only: the list beside it has a "+ new schedule" line.
	out := ansi.Strip(strings.Join(m.schedFormLines(m.schedPaneWidth(), m.termHeight), "\n"))
	flat := strings.Join(strings.Fields(out), " ")
	if !strings.Contains(out, "Review changes to custom") || !strings.Contains(flat, "agent go-engineer → python-engineer") || strings.Contains(out, "schedule ") {
		t.Errorf("edit review should list only the agent:\n%s", out)
	}
	m = schedPress(t, m, "y")
	if m.schedForm != nil {
		t.Fatalf("not saved: %q", m.schedForm.err)
	}
	after := f.rawJob(t, "custom")
	for k, v := range before {
		if k == "agent" {
			continue
		}
		if !equalJSON(after[k], v) {
			t.Errorf("%s changed: %v -> %v", k, v, after[k])
		}
	}
	if after["agent"] != "python-engineer" {
		t.Errorf("agent = %v", after["agent"])
	}
	stateAfter, _ := os.ReadFile(filepath.Join(f.stateDir, "state.json"))
	if string(stateAfter) != string(stateBefore) {
		t.Error("an edit that kept the schedule re-seeded the fire state")
	}
}

func TestScheduleEditKeepsPausedAndPromptKey(t *testing.T) {
	f := newSchedFixture(t)
	off := false
	f.create(t, cron.Job{Name: "keyed", Schedule: "0 7 * * *", PromptKey: "triage-prompt", Enabled: &off})
	m := showSchedules(t, f.model(t))
	m = selectSched(t, m, "keyed")
	m = schedPress(t, m, "e")
	m = focusStopT(t, m, stopAdvanced)
	m = schedPress(t, m, "space") // open Advanced
	m = focusStopT(t, m, stopTimeout)
	m = schedType(t, m, "30m")
	m = schedPress(t, m, "ctrl+s", "y")
	if m.schedForm != nil {
		t.Fatalf("not saved: %q %v", m.schedForm.err, m.schedForm.errors())
	}
	j := f.job(t, "keyed")
	if j.PromptKey != "triage-prompt" || j.Prompt != "" || j.IsEnabled() || j.Timeout != "30m" || j.Schedule != "0 7 * * *" {
		t.Errorf("job = %+v", j)
	}
}

func TestScheduleDuplicateSavesPaused(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	m := showSchedules(t, f.model(t))
	m = selectSched(t, m, "upcoming")
	m = schedPress(t, m, "d")
	fm := formOf(t, m)
	if fm.mode != schedFormDup || fm.name.Value() != "upcoming-copy" {
		t.Fatalf("dup form: mode %d name %q", fm.mode, fm.name.Value())
	}
	m = schedPress(t, m, "ctrl+s")
	if !strings.Contains(ansi.Strip(m.render()), "once resumed") {
		t.Errorf("dup review should say it is saved paused:\n%s", ansi.Strip(m.render()))
	}
	schedPress(t, m, "y")
	j := f.job(t, "upcoming-copy")
	if j.IsEnabled() || j.Schedule != "0 9 * * 1-5" || j.Prompt != "Triage new issues." {
		t.Errorf("duplicate = %+v", j)
	}
}

func TestScheduleFormInlineErrorsBlockReview(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	f.agentErr = errors.New(`unknown agent "nobody" for this project`)
	m := showSchedules(t, f.model(t))
	m = selectSched(t, m, "upcoming")
	m = schedPress(t, m, "d")
	m = focusStopT(t, m, stopAgent)
	m = schedClear(t, m)
	m = schedType(t, m, "nobody")
	m = schedPress(t, m, "ctrl+s")
	fm := formOf(t, m)
	if fm.review || !strings.Contains(fm.err, "fix 1 problem") || fm.focus != stopAgent {
		t.Errorf("review=%v err=%q focus=%d", fm.review, fm.err, fm.focus)
	}
	if !strings.Contains(ansi.Strip(m.render()), `unknown agent "nobody"`) {
		t.Error("agent error not shown under the field")
	}
	// A name that is taken is reported too.
	m = focusStopT(t, m, stopName)
	m = schedClear(t, m)
	m = schedType(t, m, "weekly")
	if !strings.Contains(ansi.Strip(m.render()), `"weekly" already exists`) {
		t.Error("duplicate name not reported")
	}
}

func TestScheduleFormEscAsksWhenDirty(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	m := showSchedules(t, f.model(t))
	m = selectSched(t, m, "upcoming")
	m = schedPress(t, m, "e", "esc")
	if m.schedForm != nil {
		t.Fatal("esc on a clean form should close it")
	}
	m = schedPress(t, m, "e")
	m = focusStopT(t, m, stopAgent)
	m = schedType(t, m, "x")
	m = schedPress(t, m, "tab", "esc")
	if !formOf(t, m).discard || !strings.Contains(ansi.Strip(m.render()), "discard changes? y/n") {
		t.Fatal("esc on a dirty form should ask")
	}
	m = schedPress(t, m, "n")
	if formOf(t, m).discard {
		t.Fatal("n should keep editing")
	}
	m = schedPress(t, m, "esc", "y")
	if m.schedForm != nil || m.selSchedule != "upcoming" {
		t.Errorf("form %v selection %q", m.schedForm != nil, m.selSchedule)
	}
	if f.job(t, "upcoming").Agent != "go-engineer" {
		t.Error("a discarded edit was saved")
	}
}

func TestScheduleFormKeysNeverReachTheHub(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	m := showSchedules(t, f.model(t))
	m = selectSched(t, m, "upcoming")
	m = schedPress(t, m, "e")
	m = focusStopT(t, m, stopPrompt)
	m = schedType(t, m, " qxTCzrJ")
	if m.quitting || m.closeArmed() || formOf(t, m).prompt.Value() != "Triage new issues. qxTCzrJ" {
		t.Errorf("typed text leaked: prompt=%q", formOf(t, m).prompt.Value())
	}
	if m.exeChanged = func() bool { return true }; true {
		next, _ := m.Update(fleetTickMsg{})
		if next.(FleetModel).reexec {
			t.Error("a hot reload would drop the open form")
		}
	}
}

func TestScheduleSaveErrorKeepsForm(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	m := showSchedules(t, f.model(t))
	m = selectSched(t, m, "upcoming")
	m = schedPress(t, m, "d", "ctrl+s")
	// Someone else takes the name between the review and the save.
	f.create(t, cron.Job{Name: "upcoming-copy", Schedule: "0 9 * * *"})
	m = schedPress(t, m, "y")
	fm := formOf(t, m)
	if fm.review || fm.saving || !strings.Contains(ansi.Strip(m.render()), "already exists") {
		t.Errorf("review=%v saving=%v err=%q", fm.review, fm.saving, fm.err)
	}
}

func TestScheduleRowMenu(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	m := showSchedules(t, f.model(t))
	m = selectSched(t, m, "weekly")
	items := m.fleetMenuItems(m.splitPane.Cursor())
	var labels []string
	for _, it := range items {
		labels = append(labels, it.label)
	}
	if !slices.Equal(labels, []string{"edit", "resume", "run now", "duplicate", "delete"}) {
		t.Errorf("menu = %v", labels)
	}
	m.openMenu(m.splitPane.Cursor())
	m = schedDrive(t, m, m.runMenuKey("space"))
	if !f.job(t, "weekly").IsEnabled() {
		t.Error("menu resume did not resume")
	}
}

func jsonUnmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }

func equalJSON(a, b any) bool { return reflect.DeepEqual(a, b) }

func TestSchedulesWithNoSessions(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	m := f.model(t)
	m.groups = nil
	m.rebuildLines()
	m = showSchedules(t, m)
	out := ansi.Strip(m.renderList())
	if strings.Contains(out, "no agentctl sessions running") || !strings.Contains(out, "Schedules · other projects (4)") {
		t.Errorf("with no sessions the section should still show:\n%s", out)
	}
	m = selectSched(t, m, "upcoming")
	m = schedPress(t, m, "e")
	if fm := formOf(t, m); len(fm.dirs) == 0 || fm.dirs[fm.dirIdx] != f.project {
		t.Errorf("edit form dirs = %v", fm.dirs)
	}
}

func TestScheduleDrawerNeverHidesViewer(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	m := showSchedules(t, f.model(t))
	m.liveLeft, m.liveWinWidth = 62, 150
	m.liveShown = "agentctl-acme-api|@1"
	m = selectSched(t, m, "nightly")
	m = schedPress(t, m, "e")
	if m.liveShown != "agentctl-acme-api|@1" || m.liveLeft != 62 {
		t.Errorf("drawer touched the viewer: liveShown %q liveLeft %d", m.liveShown, m.liveLeft)
	}
	// A divider drag while the drawer is up is still a divider drag.
	next, _ := m.Update(fleetProbeMsg{pane: 120, win: 150})
	if m = next.(FleetModel); m.liveLeft == 0 {
		t.Errorf("probe ignored: liveLeft %d", m.liveLeft)
	}
}

func TestScheduleNewFromHeaderLoadsJobs(t *testing.T) {
	f := newSchedFixture(t)
	m := f.model(t)
	m.selectLine(0)
	m = schedPress(t, m, "n")
	next, cmd := m.Update(schedKeyMsg("t"))
	m = next.(FleetModel)
	if m.schedForm == nil {
		t.Fatal("n, t should open the form")
	}
	m = schedDrive(t, m, cmd)
	if !m.sched.loaded {
		t.Error("the jobs were not loaded")
	}
}

func TestScheduleEditTrailingSlashDirIsNoChange(t *testing.T) {
	f := newSchedFixture(t)
	f.create(t, cron.Job{Name: "slash", Schedule: "0 7 * * *"})
	// A hand-edited file may carry a trailing slash.
	data, _ := os.ReadFile(f.jobsPath)
	data = []byte(strings.Replace(string(data), `"dir": "`+f.project+`"`, `"dir": "`+f.project+`/"`, 1))
	if err := os.WriteFile(f.jobsPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	m := showSchedules(t, f.model(t))
	m = selectSched(t, m, "slash")
	m = schedPress(t, m, "e")
	if j, _ := formOf(t, m).job(); j.Dir != f.project+"/" {
		t.Errorf("untouched dir = %q, want the stored spelling", j.Dir)
	}
	m = schedPress(t, m, "ctrl+s")
	if m.schedForm != nil || !strings.Contains(m.infoMsg, "no changes to slash") {
		t.Errorf("form open=%v info=%q", m.schedForm != nil, m.infoMsg)
	}
}

// testSchedEnv is the form's option data in tests: no backends, no accounts,
// two agents, nothing read from the real config.
func testSchedEnv() schedEnv {
	return schedEnv{
		opts:    cronedit.Options{Models: map[string][]string{"anthropic": {"opus", "sonnet", "haiku"}}},
		usage:   config.CronUsageThresholds{FiveHour: 80, SevenDay: 90},
		efforts: []string{"low", "medium", "high", "xhigh", "max", "auto"},
	}
}

func TestRenderRunLineLiveClosed(t *testing.T) {
	e := cron.Entry{Time: schedNow, Job: "j", Result: cron.ResultFired, WindowID: "@1764"}
	for _, tc := range []struct {
		live bool
		want string
	}{{true, "live"}, {false, "closed"}} {
		got := ansi.Strip(renderRunLine(e, 80, func(cron.Entry) bool { return tc.live }))
		if !strings.Contains(got, tc.want) || strings.Contains(got, "@1764") {
			t.Errorf("live=%v: %q", tc.live, got)
		}
	}
}

func TestSchedulePinnedAndUnpinnedCards(t *testing.T) {
	f := newSchedFixture(t)
	f.promptKeys = map[string]string{"spec": "Pinned spec text."}
	f.create(t, cron.Job{Name: "legacy", Schedule: "0 7 * * *", PromptKey: "spec"})
	f.create(t, cron.Job{Name: "pinned", Schedule: "0 8 * * *", Prompt: "Pinned spec text.", PromptSourceKey: "spec"})
	m := showSchedules(t, f.model(t))

	m = selectSched(t, m, "legacy")
	if out := ansi.Strip(m.render()); !strings.Contains(out, "UNPINNED") {
		t.Errorf("legacy card does not say it is unpinned:\n%s", out)
	}
	m = selectSched(t, m, "pinned")
	if out := ansi.Strip(m.render()); !strings.Contains(out, "copied from context key spec") || strings.Contains(out, "UNPINNED") {
		t.Errorf("pinned card:\n%s", out)
	}

	// Editing the text drops the source key: it is no longer the key's copy.
	m = schedPress(t, m, "e")
	m = focusStopT(t, m, stopPrompt)
	m = schedType(t, m, " Edited.")
	m = schedPress(t, m, "ctrl+s", "y")
	if m.schedForm != nil {
		t.Fatalf("not saved: %q %v", m.schedForm.err, m.schedForm.errors())
	}
	if j := f.job(t, "pinned"); j.PromptSourceKey != "" || !strings.Contains(j.Prompt, "Edited.") {
		t.Errorf("edited job = %+v", j)
	}
}

func TestScheduleDuplicateOfLegacyKeyShowsPinnedText(t *testing.T) {
	f := newSchedFixture(t)
	f.promptKeys = map[string]string{"spec": "Text the save copies."}
	f.create(t, cron.Job{Name: "legacy", Schedule: "0 7 * * *", PromptKey: "spec"})
	m := showSchedules(t, f.model(t))
	m = selectSched(t, m, "legacy")
	m = schedPress(t, m, "d", "ctrl+s")
	out := ansi.Strip(m.render())
	if !strings.Contains(out, "copied from context key spec on save") || !strings.Contains(out, "Text the save copies.") || strings.Contains(out, "UNPINNED: read at every fire") {
		t.Errorf("duplicate review:\n%s", out)
	}
	schedPress(t, m, "y")
	if j := f.job(t, "legacy-copy"); j.Unpinned() || j.Prompt != "Text the save copies." || j.PromptSourceKey != "spec" {
		t.Errorf("duplicate = %+v", j)
	}
}
