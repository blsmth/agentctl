package tui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
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
	hidden     int
	shown      []string
	editorPath string
	editorDone tea.ExecCallback
}

func newSchedFixture(t *testing.T) *schedFixture {
	t.Helper()
	InitStyles()
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
	saved := []any{fleetSchedStore, fleetSchedRun, fleetSchedHideViewer, fleetSchedExec, fleetNow, showLiveViewer, hubWindowWidth, hubZoomSync, tkEditorCmd, fleetSchedPermMode}
	t.Cleanup(func() {
		fleetSchedStore = saved[0].(func(func(string, string) error) cronedit.Store)
		fleetSchedRun = saved[1].(cronedit.RunFunc)
		fleetSchedHideViewer = saved[2].(func() error)
		fleetSchedExec = saved[3].(func(*exec.Cmd, tea.ExecCallback) tea.Cmd)
		fleetNow = saved[4].(func() time.Time)
		showLiveViewer = saved[5].(func(string, string, int) error)
		hubWindowWidth = saved[6].(func() (int, error))
		hubZoomSync = saved[7].(func(bool))
		tkEditorCmd = saved[8].(func(string) *exec.Cmd)
		fleetSchedPermMode = saved[9].(func(string) string)
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
	fleetSchedHideViewer = func() error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.hidden++
		return nil
	}
	fleetSchedExec = func(c *exec.Cmd, cb tea.ExecCallback) tea.Cmd {
		f.editorPath, f.editorDone = c.Args[len(c.Args)-1], cb
		return nil
	}
	tkEditorCmd = func(path string) *exec.Cmd {
		// Built, never started.
		return &exec.Cmd{Path: "/nonexistent/editor", Args: []string{"editor", path}}
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
	if _, err := f.base().Create(j); err != nil {
		t.Fatalf("seeding %s: %v", j.Name, err)
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
		return m
	}
	switch msg := msg.(type) {
	case nil:
		return m
	case tea.BatchMsg:
		for _, c := range msg {
			m = schedDrive(t, m, c)
		}
		return m
	case fleetSchedLoadedMsg, fleetSchedDetailMsg, fleetSchedWroteMsg, fleetSchedRanMsg,
		fleetSchedPreviewMsg, fleetSchedSavedMsg, fleetSchedEditorMsg, fleetSchedViewerHiddenMsg, fleetLiveMsg:
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

func showSchedules(t *testing.T, m FleetModel) FleetModel {
	t.Helper()
	m = schedPress(t, m, fleetSchedulesKey)
	if !m.showSchedules || !m.sched.loaded {
		t.Fatalf("section shown=%v loaded=%v err=%q", m.showSchedules, m.sched.loaded, m.sched.err)
	}
	return m
}

func schedKeys(m FleetModel) []string {
	var out []string
	for _, l := range m.lines {
		switch l.kind {
		case fleetLineSchedLabel:
			out = append(out, "SL:"+l.label)
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

func selectSched(t *testing.T, m FleetModel, name string) FleetModel {
	t.Helper()
	for i, l := range m.lines {
		if l.kind == fleetLineSchedule && schedLineKey(l) == name {
			m.selectLine(i)
			next, cmd := m.Update(fleetSchedViewerHiddenMsg{}) // runs the post-update pane sync
			return schedDrive(t, next.(FleetModel), cmd)
		}
	}
	t.Fatalf("no schedule line %q in %v", name, schedKeys(m))
	return m
}

func selectAgent(t *testing.T, m FleetModel, windowID string) FleetModel {
	t.Helper()
	for i, l := range m.lines {
		if l.kind == fleetLineRow && l.row.WindowID == windowID {
			m.selectLine(i)
			next, cmd := m.Update(fleetSchedViewerHiddenMsg{})
			return schedDrive(t, next.(FleetModel), cmd)
		}
	}
	t.Fatalf("no row %s", windowID)
	return m
}

func TestSchedulesToggleLoadsAndSorts(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	m := f.model(t)
	if m.schedLoadCmd() != nil {
		t.Fatal("schedules read while the section is hidden")
	}
	m = showSchedules(t, m)
	want := []string{"SL:Schedules (4)", "S:nightly", "S:babysit", "S:upcoming", "S:weekly"}
	if got := schedKeys(m); !slices.Equal(got, want) {
		t.Errorf("lines = %v, want %v (failed, running, by next fire, paused)", got, want)
	}
	out := ansi.Strip(m.renderList())
	for _, s := range []string{fleetSchedGlyphFailed + " nightly", fleetSchedGlyphRunning + " babysit", fleetSchedGlyphOn + " upcoming", fleetSchedGlyphPaused + " weekly", "paused", "in 3h"} {
		if !strings.Contains(out, s) {
			t.Errorf("list missing %q:\n%s", s, out)
		}
	}
	if !m.viewState().Schedules {
		t.Error("view state does not record the section")
	}
	if !strings.Contains(m.viewEnv(), `"schedules":true`) {
		t.Errorf("hot reload env drops the section: %s", m.viewEnv())
	}
	m = schedPress(t, m, fleetSchedulesKey)
	if len(schedKeys(m)) != 0 || m.schedLoadCmd() != nil {
		t.Errorf("hidden section still listed or read: %v", schedKeys(m))
	}
}

func TestSchedulesViewStateSurvivesReexec(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	m := showSchedules(t, f.model(t))
	s, ok := parseHubView([]byte(strings.TrimPrefix(m.viewEnv(), HubViewEnv+"=")))
	if !ok || !s.Schedules {
		t.Fatalf("view env = %q", m.viewEnv())
	}
	m2 := f.model(t)
	m2.applyViewState(s)
	if !m2.showSchedules || m2.schedLoadCmd() == nil {
		t.Error("a restored hub does not show and load the section")
	}
}

func TestSchedulesZeroJobsMissingFileAndHints(t *testing.T) {
	f := newSchedFixture(t)
	f.installed, f.enabled = false, false
	m := showSchedules(t, f.model(t))
	if _, err := os.Stat(f.jobsPath); !os.IsNotExist(err) {
		t.Fatal("the fixture should have no schedules.json")
	}
	got := schedKeys(m)
	want := []string{"SL:Schedules · none yet", "SL:scheduler not installed: run agentctl cron install",
		"SL:cron is off (cron.enabled: false): nothing fires", "S:+new"}
	if !slices.Equal(got, want) {
		t.Errorf("lines = %v, want %v", got, want)
	}
	m = selectSched(t, m, fleetSchedNewSel)
	if out := ansi.Strip(m.render()); !strings.Contains(out, "No cron jobs yet") {
		t.Errorf("right pane missing the empty card:\n%s", out)
	}
	m = schedPress(t, m, "enter")
	if m.schedForm == nil || m.schedForm.mode != schedFormNew {
		t.Fatal("enter on + new schedule should open the form")
	}
}

func TestSchedulesLoadErrorShown(t *testing.T) {
	f := newSchedFixture(t)
	if err := os.WriteFile(f.jobsPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := schedPress(t, f.model(t), fleetSchedulesKey)
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
		if x.kind == fleetLineSchedule {
			l = x
		}
	}
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
	// The scheduled bucket points at the section while it is hidden.
	m = schedPress(t, m, fleetSchedulesKey)
	m.rebuildLines()
	for i, l := range m.lines {
		if l.kind == fleetLineSubheader && strings.HasPrefix(l.label, "scheduled") {
			if !strings.Contains(ansi.Strip(m.renderLine(i, 80)), "T schedules") {
				t.Errorf("bucket header = %q", ansi.Strip(m.renderLine(i, 80)))
			}
			return
		}
	}
	t.Error("no scheduled bucket header")
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
	if !strings.Contains(m.infoMsg, "upcoming fired: window @12") {
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

func TestScheduleCardInRightPaneAndViewerHandoff(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	m := showSchedules(t, f.model(t))
	m = selectAgent(t, m, "@1")
	m.liveShown = "agentctl-acme-api|@1"
	m = selectSched(t, m, "nightly")
	if f.hidden != 1 || m.liveShown != "" {
		t.Fatalf("viewer hidden %d times, liveShown %q", f.hidden, m.liveShown)
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
	if !strings.Contains(ansi.Strip(m.render()), "running: go-engineer:2 in acme-api") {
		t.Errorf("running window not shown:\n%s", ansi.Strip(m.render()))
	}

	// The card always carries the full detail, no key needed.
	if m.sched.detail == nil || m.sched.detail.Name != "babysit" {
		t.Fatal("selecting a job did not load the full card")
	}

	// Back on an agent row the viewer returns.
	f.shown = nil
	m = selectAgent(t, m, "@1")
	if m.schedPaneOn || !slices.Contains(f.shown, "agentctl-acme-api|@1") {
		t.Errorf("viewer not restored: paneOn=%v shown=%v", m.schedPaneOn, f.shown)
	}
	if m.sched.detail != nil {
		t.Error("the full card outlived its selection")
	}
}

func TestScheduleCardSkippedOnNarrowTerminal(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	m := showSchedules(t, f.model(t))
	m.termWidth = m.contentWidth() + 10
	m = selectSched(t, m, "nightly")
	if strings.Contains(ansi.Strip(m.render()), "Last runs") {
		t.Error("no room: the card should not be drawn")
	}
	// The form takes the list's place instead.
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
		t.Error("the form should take the viewer's place")
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
	m = selectSched(t, m, fleetSchedNewSel)
	m = schedPress(t, m, "enter")
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
	out := ansi.Strip(m.render())
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
	if !m.showSchedules || m.quitting || m.closeArmed() || formOf(t, m).prompt.Value() != "Triage new issues. qxTCzrJ" {
		t.Errorf("typed text leaked: prompt=%q", formOf(t, m).prompt.Value())
	}
	if m.exeChanged = func() bool { return true }; true {
		next, _ := m.Update(fleetTickMsg{})
		if next.(FleetModel).reexec {
			t.Error("a hot reload would drop the open form")
		}
	}
}

func TestScheduleFormEditor(t *testing.T) {
	f := newSchedFixture(t)
	f.create(t, cron.Job{Name: "keyed", Schedule: "0 7 * * *", PromptKey: "triage-prompt"})
	m := showSchedules(t, f.model(t))
	m = selectSched(t, m, "keyed")
	m = schedPress(t, m, "e", "ctrl+e")
	if f.editorDone == nil {
		t.Fatalf("editor not started: %q", formOf(t, m).err)
	}
	if err := os.WriteFile(f.editorPath, []byte("Inline now.\nSecond line.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m = schedDrive(t, m, func() tea.Msg { return f.editorDone(nil) })
	if _, err := os.Stat(f.editorPath); !os.IsNotExist(err) {
		t.Error("the draft file was left behind")
	}
	fm := formOf(t, m)
	if fm.prompt.Value() != "Inline now.\nSecond line." || fm.promptKey != "" {
		t.Errorf("prompt %q key %q", fm.prompt.Value(), fm.promptKey)
	}
	m = schedPress(t, m, "ctrl+s", "y")
	j := f.job(t, "keyed")
	if j.Prompt != "Inline now.\nSecond line." || j.PromptKey != "" {
		t.Errorf("converted job = %+v", j)
	}

	// No $EDITOR: say so, keep the form.
	tkEditorCmd = func(string) *exec.Cmd { return nil }
	m = schedPress(t, m, "e", "ctrl+e")
	if !strings.Contains(formOf(t, m).err, "set $EDITOR") {
		t.Errorf("err = %q", formOf(t, m).err)
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
	if strings.Contains(out, "no agentctl sessions running") || !strings.Contains(out, "Schedules (4)") {
		t.Errorf("with no sessions the section should still show:\n%s", out)
	}
	m = selectSched(t, m, "upcoming")
	m = schedPress(t, m, "e")
	if fm := formOf(t, m); len(fm.dirs) == 0 || fm.dirs[fm.dirIdx] != f.project {
		t.Errorf("edit form dirs = %v", fm.dirs)
	}
}

func TestScheduleIgnoresViewerRacesWhilePaneIsUp(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	m := showSchedules(t, f.model(t))
	m.liveLeft, m.liveWinWidth = 62, 150
	m = selectSched(t, m, "nightly")
	hidden := f.hidden
	// A show started before the selection moved lands late.
	next, cmd := m.Update(fleetLiveMsg{key: "agentctl-acme-api|@1", left: 40, right: 109, win: 150})
	m = schedDrive(t, next.(FleetModel), cmd)
	if f.hidden != hidden+1 || m.liveShown != "" || m.liveLeft != 62 {
		t.Errorf("late show: hidden %d->%d liveShown %q liveLeft %d", hidden, f.hidden, m.liveShown, m.liveLeft)
	}
	// The hub pane widening is not a divider drag.
	next, _ = m.Update(fleetProbeMsg{pane: 120, win: 150})
	if m = next.(FleetModel); m.userLeft != 0 || m.liveLeft != 62 {
		t.Errorf("probe while the pane is up: userLeft %d liveLeft %d", m.userLeft, m.liveLeft)
	}
}

func TestScheduleNewFromHeaderShowsAndPersistsSection(t *testing.T) {
	f := newSchedFixture(t)
	m := f.model(t)
	dir := t.TempDir()
	m.viewPath = filepath.Join(dir, "hub-view.json")
	m.selectLine(0)
	m = schedPress(t, m, "n")
	next, cmd := m.Update(schedKeyMsg("t"))
	m = next.(FleetModel)
	if !m.showSchedules || m.schedForm == nil {
		t.Fatal("n, t should open the form and show the section")
	}
	m = schedDrive(t, m, cmd)
	if !readHubView(m.viewPath).Schedules {
		t.Error("turning the section on from n, t was not persisted")
	}
	if !m.sched.loaded {
		t.Error("the section was not loaded")
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
