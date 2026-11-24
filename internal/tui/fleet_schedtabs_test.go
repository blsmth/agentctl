package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/cron"
	"github.com/blsmth/agentctl/internal/cronedit"
	"github.com/blsmth/agentctl/internal/fleet"
)

// tabbedModel is twoGroups with jobs loaded and every project on Sessions.
func tabbedModel(t *testing.T, f *schedFixture) FleetModel {
	t.Helper()
	f.seedFour(t)
	m := f.twoGroups(t)
	return schedDrive(t, m, m.schedLoadCmd())
}

func headerText(t *testing.T, m FleetModel, session string, cw int) string {
	t.Helper()
	for i, l := range m.lines {
		if l.kind == fleetLineHeader && l.group.Session == session {
			return ansi.Strip(m.renderLine(i, cw))
		}
	}
	t.Fatalf("no header for %s", session)
	return ""
}

func lineKinds(m FleetModel, session string) (rows, jobs int) {
	for _, l := range m.lines {
		switch {
		case l.kind == fleetLineRow && l.row.Session == session:
			rows++
		case l.kind == fleetLineSchedule && l.group.Session == session:
			jobs++
		}
	}
	return rows, jobs
}

func TestGroupTabsDefaultToSessionsAndNeverMix(t *testing.T) {
	f := newSchedFixture(t)
	m := tabbedModel(t, f)
	// acme-api has 3 jobs and 2 agent rows; billing's only job (nightly) is
	// in its own dir too, so both are tabbed.
	if got := headerText(t, m, "agentctl-acme-api", 100); !strings.Contains(got, "Sessions 2 · Schedules 3") {
		t.Errorf("header = %q", got)
	}
	rows, jobs := lineKinds(m, "agentctl-acme-api")
	if rows != 2 || jobs != 0 {
		t.Errorf("Sessions tab: rows=%d jobs=%d", rows, jobs)
	}
	setAllTabs(&m, true)
	rows, jobs = lineKinds(m, "agentctl-acme-api")
	if rows != 0 || jobs != 4 { // three jobs and "+ new schedule"
		t.Errorf("Schedules tab: rows=%d jobs=%d", rows, jobs)
	}
	if got := headerText(t, m, "agentctl-acme-api", 100); !strings.Contains(got, "Sessions 2 · Schedules 3") {
		t.Errorf("header on the Schedules tab = %q", got)
	}
}

func TestProjectWithoutJobsHasNoTabs(t *testing.T) {
	f := newSchedFixture(t)
	f.create(t, cron.Job{Name: "only", Schedule: "0 6 * * *"})
	m := f.twoGroups(t)
	m.groups[1].RuntimeDir = filepath.Join(f.project2, ".agentctl")
	m = schedDrive(t, m, m.schedLoadCmd())
	if got := headerText(t, m, "agentctl-billing", 100); strings.Contains(got, "Sessions") || strings.Contains(got, "Schedules") {
		t.Errorf("billing header = %q", got)
	}
	// T there says so; t opens the form for that dir.
	for i, l := range m.lines {
		if l.kind == fleetLineHeader && l.group.Session == "agentctl-billing" {
			m.selectLine(i)
		}
	}
	m = schedPress(t, m, fleetSchedulesKey)
	if len(m.schedTabs) != 0 {
		t.Errorf("tabs = %v", m.schedTabs)
	}
	m = schedPress(t, m, fleetCronKey)
	if fm := formOf(t, m); fm.dirs[fm.dirIdx] != f.project2 {
		t.Errorf("form dir = %q", fm.dirs[fm.dirIdx])
	}
}

func TestTabKeyFlipsAndLandsOnFirstRow(t *testing.T) {
	f := newSchedFixture(t)
	m := tabbedModel(t, f)
	// From the header: stays on the header.
	m = selectAgent(t, m, "@1")
	m.selectLine(schedLineHeaderIdx(t, m, "agentctl-acme-api"))
	m = schedPress(t, m, fleetSchedulesKey)
	if !m.schedTabs[resolveDir(f.project)] {
		t.Fatal("T did not switch to Schedules")
	}
	if l := m.lines[m.splitPane.Cursor()]; l.kind != fleetLineHeader {
		t.Errorf("cursor on %v after T from the header", l.kind)
	}
	// From a job row, flipping back lands on the first agent row.
	m = selectSched(t, m, "upcoming")
	m = schedPress(t, m, fleetSchedulesKey)
	if m.schedTabs[resolveDir(f.project)] {
		t.Fatal("T did not switch back to Sessions")
	}
	l := m.lines[m.splitPane.Cursor()]
	if l.kind != fleetLineRow || l.row.WindowID != "@1" || m.selSchedule != "" || m.selSession != "agentctl-acme-api" || m.selIsHeader {
		t.Errorf("cursor = %v %q sel=%q/%q header=%v", l.kind, l.row.WindowID, m.selSession, m.selSchedule, m.selIsHeader)
	}
	// From an agent row: first job row.
	m = schedPress(t, m, fleetSchedulesKey)
	if l := m.lines[m.splitPane.Cursor()]; l.kind != fleetLineSchedule || l.sched.Name != "babysit" {
		t.Errorf("cursor after T from a row = %v %q", l.kind, l.sched.Name)
	}
	// The other project is untouched.
	if m.schedTabs[resolveDir(f.project2)] {
		t.Error("T flipped another project")
	}
}

func schedLineHeaderIdx(t *testing.T, m FleetModel, session string) int {
	t.Helper()
	for i, l := range m.lines {
		if l.kind == fleetLineHeader && l.group.Session == session {
			return i
		}
	}
	t.Fatalf("no header %s", session)
	return -1
}

func TestTabClickSwitches(t *testing.T) {
	f := newSchedFixture(t)
	m := tabbedModel(t, f)
	idx := schedLineHeaderIdx(t, m, "agentctl-acme-api")
	spans := m.pointerHeaderSpans(m.groups[0], m.listColWidth())
	if spans.tabSessions[1] == 0 || spans.tabSched[1] == 0 {
		t.Fatalf("no tab spans: %+v", spans)
	}
	y := -1
	for yy := 0; yy < m.termHeight; yy++ {
		if got, _, ok := m.lineAtSub(4, yy); ok && got == idx {
			y = yy
			break
		}
	}
	// Hover first: the pointer region is the tab.
	if h := m.hoverAt(spans.tabSched[0], y); h.reg != fleetHoverTabSched {
		t.Errorf("hover = %v", h.reg)
	}
	next, cmd := m.Update(tea.MouseClickMsg{X: spans.tabSched[0], Y: y, Button: tea.MouseLeft})
	m = schedDrive(t, next.(FleetModel), cmd)
	if !m.schedTabs[resolveDir(f.project)] {
		t.Fatal("click on the Schedules tab did not switch")
	}
	if l := m.lines[m.splitPane.Cursor()]; l.kind != fleetLineHeader {
		t.Errorf("cursor on %v, want the header", l.kind)
	}
	spans = m.pointerHeaderSpans(m.groups[0], m.listColWidth())
	next, cmd = m.Update(tea.MouseClickMsg{X: spans.tabSessions[0], Y: y, Button: tea.MouseLeft})
	m = schedDrive(t, next.(FleetModel), cmd)
	if m.schedTabs[resolveDir(f.project)] {
		t.Error("click on the Sessions tab did not switch back")
	}
	// A click on the project name still folds.
	if got := m.onHeaderName(m.groups[0], spans.name[0]); !got {
		t.Error("name span lost")
	}
}

func TestTabsTruncateBeforeProjectName(t *testing.T) {
	f := newSchedFixture(t)
	m := tabbedModel(t, f)
	// Wide: everything. Narrower: the tabs get cut. The project name stays
	// whole until the tabs are gone.
	for cw := 100; cw >= 40; cw -= 4 {
		m.termWidth, m.liveLeft = cw, cw
		out := headerText(t, m, "agentctl-acme-api", cw)
		name := strings.Contains(out, "acme-api")
		tabs := strings.Contains(out, "Sessions")
		if tabs && !name {
			t.Errorf("cw %d: tabs without the whole name: %q", cw, out)
		}
		if lw := lipgloss.Width(out); lw > cw {
			t.Errorf("cw %d: header is %d wide: %q", cw, lw, out)
		}
		if !name && cw >= 40 {
			t.Errorf("cw %d: name lost: %q", cw, out)
		}
	}
	m.termWidth, m.liveLeft = 40, 40
	full := headerText(t, m, "agentctl-acme-api", 100)
	cut := headerText(t, m, "agentctl-acme-api", 40)
	if strings.Contains(full, "Sessions 2 · Schedules 3") && strings.Contains(cut, "Schedules 3") {
		t.Errorf("tabs not truncated at 40: %q", cut)
	}
}

func TestSameDirGroupsHostOneTab(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	m := f.model(t)
	a := testRow("go-engineer", "agentctl-acme-api-2", "idle", "")
	a.WindowID = "@31"
	m.groups = append(m.groups, fleet.Group{Session: "agentctl-acme-api-2", Project: "acme-api", ProjectDir: f.project, Rows: []fleet.Row{a}})
	m = schedDrive(t, m, m.schedLoadCmd())
	if _, ok := m.groupTab(m.groups[0]); !ok {
		t.Error("first group has no tabs")
	}
	if _, ok := m.groupTab(m.groups[1]); ok {
		t.Error("duplicate project group hosts a tab")
	}
	if got := headerText(t, m, "agentctl-acme-api-2", 100); strings.Contains(got, "Schedules") {
		t.Errorf("duplicate header = %q", got)
	}
}

func TestWorktreeJobCountsInProjectTab(t *testing.T) {
	f := newSchedFixture(t)
	wt := filepath.Join(f.project, ".worktrees", "agent-x")
	if err := os.MkdirAll(wt, 0o700); err != nil {
		t.Fatal(err)
	}
	f.create(t, cron.Job{Name: "in-wt", Schedule: "0 6 * * *", Dir: wt})
	m := f.model(t)
	m = schedDrive(t, m, m.schedLoadCmd())
	if got := headerText(t, m, "agentctl-acme-api", 100); !strings.Contains(got, "Schedules 1") {
		t.Errorf("header = %q", got)
	}
}

func TestOnlyOtherJobsWithNoGroups(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	m := f.model(t)
	m.groups = nil
	m = schedDrive(t, m, m.schedLoadCmd())
	if got := schedKeys(m); !slices.Equal(got, []string{"SH:Schedules · other projects (4)"}) {
		t.Fatalf("lines = %v, want the collapsed bottom block", got)
	}
	if !m.clickable() {
		t.Error("the bottom block is not drawn as a list")
	}
	// With no jobs and no groups the hub says so.
	m2 := f.model(t)
	m2.groups = nil
	m2.sched = fleetSchedState{loaded: true}
	m2.relayout()
	if s, ok := m2.renderStatusOrEmpty(); !ok || !strings.Contains(s, "no agentctl sessions") {
		t.Errorf("empty hub = %q", s)
	}
}

func TestTabStateSurvivesProjectDisappearing(t *testing.T) {
	f := newSchedFixture(t)
	m := tabbedModel(t, f)
	m.viewPath = filepath.Join(t.TempDir(), "hub-view.json")
	setAllTabs(&m, true)
	both := m.groups
	m.groups = m.groups[:1]
	m.relayout()
	// The project is gone: in memory the choice is kept ...
	if !m.schedTabs[resolveDir(f.project2)] {
		t.Fatal("the choice was dropped from memory")
	}
	// ... and comes back with the project.
	m.groups = both
	m.relayout()
	if rows, _ := lineKinds(m, "agentctl-billing"); rows != 0 {
		t.Errorf("billing came back on Sessions (%d rows)", rows)
	}
	// What is persisted while it is gone drops it (like fold state).
	m.groups = m.groups[:1]
	if got := m.viewState().SchedTabs; !slices.Equal(got, []string{resolveDir(f.project)}) {
		t.Errorf("persisted while gone = %v", got)
	}
}

func TestOldHubViewSchedulesFieldIgnored(t *testing.T) {
	s, ok := parseHubView([]byte(`{"folded":["s1"],"schedules":true,"sched_collapsed":["/p"]}`))
	if !ok {
		t.Fatal("old state file rejected")
	}
	f := newSchedFixture(t)
	m := f.model(t)
	m.applyViewState(s)
	if len(m.schedTabs) != 0 || m.schedOtherOpen || !m.isFolded("s1") {
		t.Errorf("tabs=%v otherOpen=%v folded=%v", m.schedTabs, m.schedOtherOpen, m.isFolded("s1"))
	}
	if strings.Contains(m.viewEnv(), `"schedules"`) {
		t.Errorf("env still writes the old field: %s", m.viewEnv())
	}
}

func TestHotReloadRoundTripKeepsTabs(t *testing.T) {
	f := newSchedFixture(t)
	m := tabbedModel(t, f)
	m = selectSched(t, m, "upcoming") // flips acme-api (and billing) to Schedules
	env := strings.TrimPrefix(m.viewEnv(), HubViewEnv+"=")
	s, ok := parseHubView([]byte(env))
	if !ok || len(s.SchedTabs) != 2 {
		t.Fatalf("env = %s", env)
	}
	m2 := f.twoGroups(t)
	m2.applyViewState(s)
	m2 = schedDrive(t, m2, m2.schedLoadCmd())
	if rows, jobs := lineKinds(m2, "agentctl-acme-api"); rows != 0 || jobs == 0 {
		t.Errorf("restored acme-api: rows=%d jobs=%d", rows, jobs)
	}
}

func TestSearchShowsJobsRegardlessOfTab(t *testing.T) {
	f := newSchedFixture(t)
	m := tabbedModel(t, f) // Sessions tab
	m = searchFor(t, m, "upcoming")
	_, jobs := lineKinds(m, "agentctl-acme-api")
	if jobs != 1 {
		t.Errorf("job matches shown = %d, want 1 on the Sessions tab", jobs)
	}
	// The saved tab is untouched.
	if len(m.schedTabs) != 0 {
		t.Errorf("search changed the tabs: %v", m.schedTabs)
	}
}

func TestTabCountsIgnoreShells(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	m := f.model(t)
	m.groups[0].Rows[0].Shell = true
	m = schedDrive(t, m, m.schedLoadCmd())
	if tab, _ := m.groupTab(m.groups[0]); tab.sessions != 1 {
		t.Errorf("sessions = %d, want 1 (shell excluded)", tab.sessions)
	}
}

var _ = cronedit.Status{}
