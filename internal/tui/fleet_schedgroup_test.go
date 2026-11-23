package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/cron"
	"github.com/blsmth/agentctl/internal/cronedit"
	"github.com/blsmth/agentctl/internal/fleet"
)

// twoGroups is the fixture hub with acme-api and billing sessions.
func (f *schedFixture) twoGroups(t *testing.T) FleetModel {
	t.Helper()
	m := f.model(t)
	b := testRow("python-engineer", "agentctl-billing", "idle", "")
	b.WindowID, b.Project = "@20", "billing"
	m.groups = append(m.groups, fleet.Group{Session: "agentctl-billing", Project: "billing", ProjectDir: f.project2, Rows: []fleet.Row{b}})
	m.rebuildLines()
	m.resolveSelection()
	return m
}

// schedBlocks maps each job to the session whose block lists it ("" for the
// bottom block), from the drawn lines.
func schedBlocks(m FleetModel) map[string]string {
	out := map[string]string{}
	for _, l := range m.lines {
		if l.kind == fleetLineSchedule && !l.schedNew {
			out[l.sched.Name] = l.group.Session
		}
	}
	return out
}

func schedLineIdx(t *testing.T, m FleetModel, key string) int {
	t.Helper()
	for i, l := range m.lines {
		if (l.kind == fleetLineSchedule || l.kind == fleetLineSchedHead) && schedLineKey(l) == key {
			return i
		}
	}
	t.Fatalf("no line %q in %v", key, schedKeys(m))
	return -1
}

func TestSchedulePlacement(t *testing.T) {
	f := newSchedFixture(t)
	root := filepath.Dir(f.project)
	link := filepath.Join(root, "acme-link")
	if err := os.Symlink(f.project, link); err != nil {
		t.Fatal(err)
	}
	sibling := filepath.Join(root, "acme-api-old")
	nested := filepath.Join(f.project, "services", "web")
	for _, d := range []string{sibling, nested, filepath.Join(f.project, ".worktrees", "agent-x")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	row := func(session, id string) fleet.Row {
		r := testRow("go-engineer", session, "idle", "")
		r.WindowID = id
		return r
	}
	cases := []struct {
		name, dir string
		want      string // the session whose block lists it; "" = bottom
	}{
		{"equal", f.project, "agentctl-acme-api"},
		{"trailing slash", f.project + "/", "agentctl-acme-api"},
		{"subdir", filepath.Join(f.project, "cmd"), "agentctl-acme-api"},
		{"worktree", filepath.Join(f.project, ".worktrees", "agent-x"), "agentctl-acme-api"},
		{"removed worktree", filepath.Join(link, ".worktrees", "gone"), "agentctl-acme-api"},
		{"symlinked", link, "agentctl-acme-api"},
		{"longest prefix", filepath.Join(nested, "api"), "agentctl-web"},
		{"path boundary", sibling, ""},
		{"no group", f.project2, ""},
	}
	m := f.model(t)
	// A second session on acme-api (spelled through the symlink) and one on
	// a nested project.
	m.groups = append(m.groups,
		fleet.Group{Session: "agentctl-acme-api-2", Project: "acme-api", ProjectDir: link, Rows: []fleet.Row{row("agentctl-acme-api-2", "@30")}},
		fleet.Group{Session: "agentctl-web", Project: "web", ProjectDir: nested, Rows: []fleet.Row{row("agentctl-web", "@40")}},
	)
	m.showSchedules = true
	m.sched.loaded = true
	m.sched.ov = cronedit.Overview{SchedulerInstalled: true, Enabled: true}
	for _, c := range cases {
		m.sched.ov.Jobs = append(m.sched.ov.Jobs, cronedit.Status{Name: c.name, Enabled: true, Job: cron.Job{Name: c.name, Dir: c.dir, Schedule: "0 9 * * *"}})
	}
	m.rebuildLines()
	got := schedBlocks(m)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if s, ok := got[c.name]; !ok || s != c.want {
				t.Errorf("%s (%s) listed under %q (listed %v), want %q", c.name, c.dir, s, ok, c.want)
			}
		})
	}
	// The second acme-api session hosts no block of its own.
	for _, l := range m.lines {
		if (l.kind == fleetLineSchedHead || l.kind == fleetLineSchedule) && l.group.Session == "agentctl-acme-api-2" {
			t.Errorf("duplicate project group drew schedule line %q", schedLineKey(l))
		}
	}
}

func TestScheduleBlockLayout(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	f.create(t, cron.Job{Name: "invoices", Schedule: "0 6 * * *", Dir: f.project2})
	m := showSchedules(t, f.twoGroups(t))
	// Each block follows its group's agent rows (after the scheduled bucket);
	// nothing is left for the bottom block.
	var seq []string
	for _, l := range m.lines {
		switch l.kind {
		case fleetLineHeader:
			seq = append(seq, "H:"+l.group.Project)
		case fleetLineRow:
			seq = append(seq, "R:"+l.row.WindowID)
		case fleetLineSubheader:
			seq = append(seq, "B:"+l.label)
		case fleetLineSchedHead:
			seq = append(seq, "SH:"+l.label)
		case fleetLineSchedule:
			seq = append(seq, "S:"+strings.TrimPrefix(schedLineKey(l), "\x00"))
		case fleetLineSchedLabel:
			seq = append(seq, "SL:"+l.label)
		}
	}
	want := []string{
		"H:acme-api", "R:@1", "B:scheduled (1)", "R:@9", "SH:schedules (3)", "S:babysit", "S:upcoming", "S:weekly", "S:new:" + resolveDir(f.project),
		"H:billing", "R:@20", "SH:schedules (2)", "S:nightly", "S:invoices", "S:new:" + resolveDir(f.project2),
	}
	if !slices.Equal(seq, want) {
		t.Errorf("lines =\n%v\nwant\n%v", seq, want)
	}
	// Headers and rows hang off their group's bar.
	for i, l := range m.lines {
		if l.kind == fleetLineSchedHead || l.kind == fleetLineSchedule {
			if out := ansi.Strip(m.renderLine(i, m.contentWidth())); !strings.HasPrefix(out, "▎") {
				t.Errorf("line %q has no project bar: %q", schedLineKey(l), out)
			}
		}
	}
	// "+ new" in billing's block starts the form in billing.
	m = selectSched(t, m, schedNewSel(resolveDir(f.project2)))
	m = schedPress(t, m, "enter")
	if fm := formOf(t, m); fm.dirs[fm.dirIdx] != f.project2 {
		t.Errorf("form dir = %q, want %q", fm.dirs[fm.dirIdx], f.project2)
	}
}

func TestScheduleWarningsRenderOnce(t *testing.T) {
	f := newSchedFixture(t)
	f.installed, f.enabled = false, false
	f.create(t, cron.Job{Name: "a", Schedule: "0 6 * * *"})
	f.create(t, cron.Job{Name: "b", Schedule: "0 6 * * *", Dir: f.project2})
	m := showSchedules(t, f.twoGroups(t))
	out := ansi.Strip(m.renderList())
	for _, w := range []string{"scheduler not installed", "cron is off"} {
		if n := strings.Count(out, w); n != 1 {
			t.Errorf("%q drawn %d times:\n%s", w, n, out)
		}
	}
	// With no bottom block the warnings are the last lines.
	if last := m.lines[len(m.lines)-1]; last.kind != fleetLineSchedLabel || !last.schedWarn {
		t.Errorf("last line = %+v", last)
	}

	// With a bottom block they go under its label.
	m.groups = m.groups[:1]
	m.relayout()
	want := []string{"SH:schedules (1)", "S:a", "S:+new", "SH:Schedules · other projects (1)",
		"SL:scheduler not installed: run agentctl cron install", "SL:cron is off (cron.enabled: false): nothing fires", "S:b"}
	if got := schedKeys(m); !slices.Equal(got, want) {
		t.Errorf("lines = %v, want %v", got, want)
	}
}

func TestScheduleLoadingLabelAtBottom(t *testing.T) {
	f := newSchedFixture(t)
	m := f.twoGroups(t)
	next, _ := m.Update(schedKeyMsg(fleetSchedulesKey)) // the load is not driven
	m = next.(FleetModel)
	if got := schedKeys(m); !slices.Equal(got, []string{"SL:Schedules · loading…"}) {
		t.Errorf("lines while loading = %v", got)
	}
}

func TestScheduleBlockCollapse(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	m := showSchedules(t, f.model(t))
	m.viewPath = filepath.Join(t.TempDir(), "hub-view.json")
	headKey := fleetSchedHeadSel + resolveDir(f.project)
	m = selectSched(t, m, "upcoming")

	// z on a job row does not collapse anything.
	m = schedPress(t, m, "z")
	if len(m.schedCollapsed) != 0 {
		t.Fatalf("z on a job row collapsed %v", m.schedCollapsed)
	}

	// The header is selectable and has no drawer.
	m.selectLine(schedLineIdx(t, m, headKey))
	if m.schedPaneActive() {
		t.Error("a block header should not open the drawer")
	}
	if foot := ansi.Strip(fitHints(m.footerHints(), 200)); !strings.Contains(foot, "z collapse") {
		t.Errorf("header footer = %q", foot)
	}
	next, cmd := m.Update(schedKeyMsg("z"))
	m = next.(FleetModel)
	if cmd != nil {
		cmd()
	}
	want := []string{"SH:schedules (3)", "SH:Schedules · other projects (1)", "S:nightly"}
	if got := schedKeys(m); !slices.Equal(got, want) {
		t.Fatalf("collapsed lines = %v, want %v", got, want)
	}
	if m.selSchedule != headKey {
		t.Errorf("selection after collapse = %q", m.selSchedule)
	}
	head := ansi.Strip(m.renderLine(m.splitPane.Cursor(), m.contentWidth()))
	for _, s := range []string{fleetFoldedGlyph + " schedules (3)", "+3", fleetSchedGlyphRunning + "1", fleetSchedGlyphPaused + "1"} {
		if !strings.Contains(head, s) {
			t.Errorf("collapsed header %q lacks %q", head, s)
		}
	}
	if !slices.Equal(readHubView(m.viewPath).SchedCollapsed, []string{resolveDir(f.project)}) {
		t.Errorf("persisted = %v", readHubView(m.viewPath).SchedCollapsed)
	}

	// The collapse survives a rebuild and a jobs reload.
	m.relayout()
	m = schedDrive(t, m, m.schedLoadCmd())
	if got := schedKeys(m); !slices.Equal(got, want) {
		t.Errorf("after reload = %v", got)
	}

	// ... and a restart, from the file and from the hot-reload env.
	orig := hubViewPath
	hubViewPath = func() (string, error) { return m.viewPath, nil }
	t.Cleanup(func() { hubViewPath = orig })
	m2 := f.model(t)
	m2.loadViewState()
	m2 = schedDrive(t, m2, m2.schedLoadCmd())
	if got := schedKeys(m2); !slices.Equal(got, want) {
		t.Errorf("after restart = %v", got)
	}
	env := strings.TrimPrefix(m.viewEnv(), HubViewEnv+"=")
	if !strings.Contains(env, `"sched_collapsed"`) {
		t.Fatalf("hot reload env drops the collapse: %s", env)
	}
	m3 := f.model(t)
	hubViewPath = func() (string, error) { return filepath.Join(t.TempDir(), "none.json"), nil }
	t.Setenv(HubViewEnv, env)
	m3.loadViewState()
	m3 = schedDrive(t, m3, m3.schedLoadCmd())
	if got := schedKeys(m3); !slices.Equal(got, want) {
		t.Errorf("after hot reload = %v", got)
	}

	// A click on the header expands it again; so does the bottom block's.
	m = schedClickLine(t, m, schedLineIdx(t, m, headKey))
	if m.isSchedCollapsed(resolveDir(f.project)) || !slices.Contains(schedKeys(m), "S:upcoming") {
		t.Errorf("click did not expand: %v", schedKeys(m))
	}
	m = schedClickLine(t, m, schedLineIdx(t, m, fleetSchedHeadSel+fleetSchedOtherKey))
	if !m.isSchedCollapsed(fleetSchedOtherKey) || slices.Contains(schedKeys(m), "S:nightly") {
		t.Errorf("click did not collapse the bottom block: %v", schedKeys(m))
	}
}

// schedClickLine left-clicks the first column of line idx.
func schedClickLine(t *testing.T, m FleetModel, idx int) FleetModel {
	t.Helper()
	for y := 0; y < m.termHeight; y++ {
		if got, _, ok := m.lineAtSub(4, y); ok && got == idx {
			next, cmd := m.Update(tea.MouseClickMsg{X: 4, Y: y, Button: tea.MouseLeft})
			return schedDrive(t, next.(FleetModel), cmd)
		}
	}
	t.Fatalf("line %d not on screen", idx)
	return m
}

func TestScheduleCollapsedPrunedWithItsProject(t *testing.T) {
	f := newSchedFixture(t)
	m := f.twoGroups(t)
	m.setSchedCollapsed(resolveDir(f.project), true)
	m.setSchedCollapsed(resolveDir(f.project2), true)
	m.setSchedCollapsed(fleetSchedOtherKey, true)
	m.groups = m.groups[:1]
	got := m.viewState().SchedCollapsed
	want := []string{":other", resolveDir(f.project)}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("pruned = %v, want %v", got, want)
	}
	// An empty load prunes nothing.
	m.groups = nil
	if got := m.viewState().SchedCollapsed; len(got) != 3 {
		t.Errorf("empty load pruned: %v", got)
	}
}

func TestScheduleSelectionHiddenByCollapseOrFold(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	m := showSchedules(t, f.model(t))
	m = selectSched(t, m, "upcoming")
	// Collapsing the block from elsewhere puts the cursor on its header.
	m.setSchedCollapsed(resolveDir(f.project), true)
	m.relayout()
	if m.selSchedule != fleetSchedHeadSel+resolveDir(f.project) {
		t.Errorf("selection = %q, want the block header", m.selSchedule)
	}
	m.setSchedCollapsed(resolveDir(f.project), false)
	m.relayout()

	// Folding the group hides its block; the cursor goes to the header.
	m = selectSched(t, m, "upcoming")
	m.setFolded("agentctl-acme-api", true)
	m.relayout()
	if got := schedKeys(m); !slices.Equal(got, []string{"SH:Schedules · other projects (1)", "S:nightly"}) {
		t.Errorf("folded lines = %v", got)
	}
	if !m.selIsHeader || m.selSession != "agentctl-acme-api" || m.selSchedule != "" {
		t.Errorf("selection = %q/%q header=%v", m.selSession, m.selSchedule, m.selIsHeader)
	}
}

func TestScheduleBlockSurvivesAttentionFilter(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	m := showSchedules(t, f.twoGroups(t))
	m.filter = fleetFilterNeeds // no row is blocked or done
	m.relayout()
	got := schedBlocks(m)
	if got["upcoming"] != "agentctl-acme-api" || got["nightly"] != "agentctl-billing" {
		t.Errorf("filter hid schedules: %v", got)
	}
	for _, l := range m.lines {
		if l.kind == fleetLineRow {
			t.Errorf("filter kept row %s", l.row.WindowID)
		}
	}
}

func TestScheduleSelectionFollowsJobAcrossGroups(t *testing.T) {
	f := newSchedFixture(t)
	f.create(t, cron.Job{Name: "mover", Schedule: "0 6 * * *"})
	f.create(t, cron.Job{Name: "stay", Schedule: "0 7 * * *"})
	m := showSchedules(t, f.twoGroups(t))
	m = selectSched(t, m, "mover")
	if schedBlocks(m)["mover"] != "agentctl-acme-api" {
		t.Fatalf("mover starts in %q", schedBlocks(m)["mover"])
	}
	// Someone moves the job to billing; the next reload lists it there.
	if _, err := f.base().Modify("mover", func(j *cron.Job) error { j.Dir = f.project2; return nil }); err != nil {
		t.Fatal(err)
	}
	m = schedDrive(t, m, m.schedLoadCmd())
	if schedBlocks(m)["mover"] != "agentctl-billing" {
		t.Fatalf("mover now in %q", schedBlocks(m)["mover"])
	}
	l := m.selectedSchedLine()
	if l == nil || l.sched.Name != "mover" || l.group.Session != "agentctl-billing" || m.selSchedule != "mover" {
		t.Errorf("selection = %q line %+v", m.selSchedule, l)
	}
}

func TestSchedulesHiddenDrawNoBlocks(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	m := showSchedules(t, f.twoGroups(t))
	m = schedPress(t, m, fleetSchedulesKey)
	for _, l := range m.lines {
		if l.kind == fleetLineSchedHead || l.kind == fleetLineSchedule || l.kind == fleetLineSchedLabel {
			t.Errorf("hidden section drew %q", schedLineKey(l))
		}
	}
}

func TestScheduleDirsResolvedOncePerPath(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	m := showSchedules(t, f.twoGroups(t))
	orig := fleetEvalSymlinks
	t.Cleanup(func() { fleetEvalSymlinks = orig })
	calls := 0
	fleetEvalSymlinks = func(p string) (string, error) {
		calls++
		return orig(p)
	}
	for range 5 {
		m.relayout()
		_ = m.render()
		_ = m.viewState()
	}
	if calls != 0 {
		t.Errorf("EvalSymlinks called %d times for already-resolved dirs", calls)
	}
}

func TestScheduleSummaryNarrows(t *testing.T) {
	InitStyles()
	c := fleetSchedCounts{total: 5, running: 1, failed: 2, paused: 2}
	cases := map[int]string{
		40: "+5 ●1 !2 ⏸2",
		9:  "+5 ●1 !2",
		6:  "●1 !2",
		0:  "",
	}
	for w, want := range cases {
		if got := ansi.Strip(schedSummary(c, w)); got != want {
			t.Errorf("w %d: %q, want %q", w, got, want)
		}
	}
}
