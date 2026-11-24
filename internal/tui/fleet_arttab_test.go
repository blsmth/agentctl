package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/artifacts"
	"github.com/blsmth/agentctl/internal/cron"
)

// artStore is an artifact store under a temp dir.
func artStore(t *testing.T) *artifacts.Store {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "artifacts")
	return &artifacts.Store{Dir: func() (string, error) { return dir, nil }}
}

// addArt publishes a one-file artifact for projectDir ("" = no project).
func addArt(t *testing.T, s *artifacts.Store, projectDir, title string) artifacts.Meta {
	t.Helper()
	p := filepath.Join(t.TempDir(), "r.md")
	if err := os.WriteFile(p, []byte("# "+title+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	o := artifacts.Origin{ProjectDir: projectDir}
	if projectDir != "" {
		o.Project = filepath.Base(projectDir)
	}
	m, err := s.Add(artifacts.AddOptions{Source: artifacts.Source{Path: p}, Title: title, Origin: o})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// withArtifacts loads the store's listing into the model's cache, the way
// the poll does, and lays the lines out again.
func withArtifacts(t *testing.T, m FleetModel, s *artifacts.Store) FleetModel {
	t.Helper()
	metas, err := s.List(artifacts.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	m.applyArtifacts(fleetArtifactsMsg{stamp: time.Now(), byWindow: indexArtifacts(metas), list: metas})
	return m
}

func artRows(m FleetModel, session string) []fleetLine {
	var out []fleetLine
	for _, l := range m.lines {
		if l.kind == fleetLineArtifact && l.group.Session == session {
			out = append(out, l)
		}
	}
	return out
}

func tabKey(m FleetModel, session string) fleetTab {
	_, t, ok := m.tabGroupOfLine(schedLineHeaderIdx2(m, session))
	if !ok {
		return -1
	}
	return t.tab
}

func schedLineHeaderIdx2(m FleetModel, session string) int {
	for i, l := range m.lines {
		if l.kind == fleetLineHeader && l.group.Session == session {
			return i
		}
	}
	return -1
}

func TestArtifactTabsAppearOnlyWithItems(t *testing.T) {
	f := newSchedFixture(t)
	m := f.twoGroups(t)
	// No jobs, no artifacts: no tabs.
	if got := headerText(t, m, "agentctl-acme-api", 100); strings.Contains(got, "Sessions") {
		t.Fatalf("header = %q", got)
	}
	s := artStore(t)
	addArt(t, s, f.project, "Q4 report")
	addArt(t, s, "", "orphan") // no project: not listed anywhere
	m = withArtifacts(t, m, s)
	got := headerText(t, m, "agentctl-acme-api", 100)
	if !strings.Contains(got, "Sessions · Artifacts 1") || strings.Contains(got, "Schedules") {
		t.Errorf("header = %q, want Sessions · Artifacts 1 and no Schedules tab", got)
	}
	if got := headerText(t, m, "agentctl-billing", 100); strings.Contains(got, "Sessions") {
		t.Errorf("billing has nothing: header = %q", got)
	}
}

func TestTabCycling(t *testing.T) {
	f := newSchedFixture(t)
	f.create(t, cron.Job{Name: "babysit", Schedule: "0 6 * * *", Dir: f.project})
	s := artStore(t)
	addArt(t, s, f.project, "Q4 report")
	m := f.twoGroups(t)
	m = schedDrive(t, m, m.schedLoadCmd())

	// Both non-empty: Sessions → Schedules → Artifacts → Sessions.
	m = withArtifacts(t, m, s)
	want := []fleetTab{tabKindSched, tabKindArtifacts, tabKindSessions}
	for i, w := range want {
		m = schedPress(t, m, fleetSchedulesKey)
		if got := tabKey(m, "agentctl-acme-api"); got != w {
			t.Fatalf("press %d: tab = %d, want %d", i+1, got, w)
		}
	}
	// Only artifacts: Sessions ↔ Artifacts.
	m2 := f.twoGroups(t)
	m2 = withArtifacts(t, m2, s)
	for i, w := range []fleetTab{tabKindArtifacts, tabKindSessions, tabKindArtifacts} {
		m2 = schedPress(t, m2, fleetSchedulesKey)
		if got := tabKey(m2, "agentctl-acme-api"); got != w {
			t.Fatalf("artifacts-only press %d: tab = %d, want %d", i+1, got, w)
		}
	}
	// A group with neither stays untabbed and T only says so.
	if got := tabKey(m2, "agentctl-billing"); got != -1 {
		t.Errorf("billing tab = %d", got)
	}
}

func TestSelectedTabFallsBackWhenEmpty(t *testing.T) {
	f := newSchedFixture(t)
	s := artStore(t)
	a := addArt(t, s, f.project, "Q4 report")
	m := withArtifacts(t, f.twoGroups(t), s)
	m = schedPress(t, m, fleetSchedulesKey)
	if len(artRows(m, "agentctl-acme-api")) != 1 {
		t.Fatal("artifact row missing on the Artifacts tab")
	}
	if _, err := s.Remove(a.ID); err != nil {
		t.Fatal(err)
	}
	m = withArtifacts(t, m, s)
	if rows, _ := lineKinds(m, "agentctl-acme-api"); rows != 2 {
		t.Errorf("agent rows after the last artifact went = %d, want the Sessions tab back", rows)
	}
}

func TestArtifactRowsPinnedFirstAndMatching(t *testing.T) {
	f := newSchedFixture(t)
	s := artStore(t)
	sub := filepath.Join(f.project, "sub")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	first := addArt(t, s, f.project, "first")
	addArt(t, s, sub, "in a subdir")
	addArt(t, s, f.project2, "billing doc")
	if _, err := s.Pin(first.ID, true); err != nil {
		t.Fatal(err)
	}
	m := withArtifacts(t, f.twoGroups(t), s)
	m.setTab(resolveDir(f.project), tabKindArtifacts)
	m.relayout()
	rows := artRows(m, "agentctl-acme-api")
	if len(rows) != 2 || rows[0].art.ID != first.ID {
		t.Fatalf("rows = %v, want pinned first of 2", rows)
	}
	if got := len(artRows(m, "agentctl-billing")); got != 0 {
		t.Errorf("billing rows on Sessions tab = %d", got)
	}
}

func TestArtifactRowNarrow(t *testing.T) {
	f := newSchedFixture(t)
	s := artStore(t)
	long := "世界世界世界 a very long artifact title that cannot fit in forty columns"
	a := addArt(t, s, f.project, long)
	a.Pinned, a.Kind = true, "html"
	a.Revs = append(a.Revs, a.Revs[0])
	a.Revs[1].N = 2
	a.Comments = make([]artifacts.Comment, 3)
	m := f.twoGroups(t)
	m.groups[0].RuntimeDir = ""
	for _, cw := range []int{40, 34, 28} {
		line := fleetLine{kind: fleetLineArtifact, group: m.groups[0], art: a}
		out := ansi.Strip(m.renderArtRow(line, false, fleetHoverNone, cw))
		if w := ansi.StringWidth(out); w > cw {
			t.Errorf("cw=%d: row is %d wide: %q", cw, w, out)
		}
		if !strings.Contains(out, "★") {
			t.Errorf("cw=%d: no pin marker: %q", cw, out)
		}
	}
	out := ansi.Strip(m.renderArtRow(fleetLine{kind: fleetLineArtifact, group: m.groups[0], art: a}, false, fleetHoverNone, 40))
	for _, want := range []string{"html", "r2", "✎3"} {
		if !strings.Contains(out, want) {
			t.Errorf("row %q lacks %q", out, want)
		}
	}
	// A legacy 10-character id with no title falls back to the id.
	legacy := artifacts.Meta{ID: "abcdefghij", Kind: "html", Updated: time.Now(), Revs: []artifacts.Revision{{N: 1}}}
	out = ansi.Strip(m.renderArtRow(fleetLine{kind: fleetLineArtifact, group: m.groups[0], art: legacy}, false, fleetHoverNone, 40))
	if !strings.Contains(out, "abcdefghij") && !strings.Contains(out, artifacts.ShortID("abcdefghij")) {
		t.Errorf("legacy row = %q", out)
	}
}

func TestArtifactPinToggle(t *testing.T) {
	f := newSchedFixture(t)
	s := artStore(t)
	a := addArt(t, s, f.project, "Q4 report")
	orig := fleetArtifactPin
	t.Cleanup(func() { fleetArtifactPin = orig })
	fleetArtifactPin = s.Pin
	m := withArtifacts(t, f.twoGroups(t), s)
	m = schedPress(t, m, fleetSchedulesKey) // Artifacts tab, cursor on its first row
	if l := m.selectedArtLine(); l == nil || l.art.ID != a.ID {
		t.Fatalf("cursor not on the artifact: %+v", m.lines[m.splitPane.Cursor()].kind)
	}
	m = artPress(t, m, fleetArtPinKey)
	got, err := s.Get(a.ID)
	if err != nil || !got.Pinned {
		t.Fatalf("not pinned in the store: %+v %v", got, err)
	}
	if l := m.selectedArtLine(); l == nil || !l.art.Pinned {
		t.Error("row not pinned after the toggle")
	}
	m = artPress(t, m, fleetArtPinKey)
	if got, _ := s.Get(a.ID); got.Pinned {
		t.Error("second p did not unpin")
	}
}

func TestArtifactOpen(t *testing.T) {
	f := newSchedFixture(t)
	s := artStore(t)
	a := addArt(t, s, f.project, "Q4 report")
	origServe, origOpen := fleetArtifactServe, fleetArtOpenURL
	t.Cleanup(func() { fleetArtifactServe, fleetArtOpenURL = origServe, origOpen })
	var opened []string
	fleetArtOpenURL = func(u string) error { opened = append(opened, u); return nil }

	m := withArtifacts(t, f.twoGroups(t), s)
	m = schedPress(t, m, fleetSchedulesKey)

	// Serve down: a hint, no browser.
	fleetArtifactServe = func() (string, bool) { return "https://mac.local:8443", false }
	m = artPress(t, m, "enter")
	if len(opened) != 0 {
		t.Errorf("opened %v with serve down", opened)
	}
	if !strings.Contains(m.jumpErr, "serve is not running") {
		t.Errorf("footer error = %q", m.jumpErr)
	}

	// Serve up: the direct URL opens, by key and by click.
	fleetArtifactServe = func() (string, bool) { return "https://mac.local:8443", true }
	m = artPress(t, m, "enter")
	want := "https://mac.local:8443/a/" + a.ID + "/latest/"
	if len(opened) != 1 || opened[0] != want {
		t.Fatalf("opened %v, want [%s]", opened, want)
	}
	idx := -1
	for i, l := range m.lines {
		if l.kind == fleetLineArtifact {
			idx = i
		}
	}
	y := -1
	for yy := 0; yy < m.termHeight; yy++ {
		if got, _, ok := m.lineAtSub(10, yy); ok && got == idx {
			y = yy
			break
		}
	}
	if y < 0 {
		t.Fatal("artifact row not on screen")
	}
	next, cmd := m.Update(tea.MouseClickMsg{X: 10, Y: y, Button: tea.MouseLeft})
	_ = schedDrive(t, next.(FleetModel), cmd)
	if len(opened) != 2 {
		t.Errorf("click did not open: %v", opened)
	}
}

func TestArtifactTabClickAndKeyHints(t *testing.T) {
	f := newSchedFixture(t)
	s := artStore(t)
	addArt(t, s, f.project, "Q4 report")
	m := withArtifacts(t, f.twoGroups(t), s)
	idx := schedLineHeaderIdx(t, m, "agentctl-acme-api")
	spans := m.pointerHeaderSpans(m.groups[0], m.listColWidth())
	if spans.tabArts[1] == 0 || spans.tabSched[1] != 0 {
		t.Fatalf("spans = %+v", spans)
	}
	y := -1
	for yy := 0; yy < m.termHeight; yy++ {
		if got, _, ok := m.lineAtSub(4, yy); ok && got == idx {
			y = yy
			break
		}
	}
	if h := m.hoverAt(spans.tabArts[0], y); h.reg != fleetHoverTabArts {
		t.Errorf("hover = %v", h.reg)
	}
	next, cmd := m.Update(tea.MouseClickMsg{X: spans.tabArts[0], Y: y, Button: tea.MouseLeft})
	m = schedDrive(t, next.(FleetModel), cmd)
	if tabKey(m, "agentctl-acme-api") != tabKindArtifacts {
		t.Fatal("click on the Artifacts tab did not switch")
	}
	m = schedPress(t, m, "j")
	var keys []string
	for _, h := range m.footerHints() {
		keys = append(keys, h.key+" "+h.label)
	}
	if got := strings.Join(keys, "|"); !strings.Contains(got, "⏎ open") || !strings.Contains(got, "p pin") {
		t.Errorf("hints = %s", got)
	}
}

func TestArtifactTabPersists(t *testing.T) {
	f := newSchedFixture(t)
	s := artStore(t)
	addArt(t, s, f.project, "Q4 report")
	m := withArtifacts(t, f.twoGroups(t), s)
	m = schedPress(t, m, fleetSchedulesKey)
	st := m.viewState()
	if len(st.ArtTabs) != 1 || len(st.SchedTabs) != 0 {
		t.Fatalf("view state = %+v", st)
	}
	m2 := f.twoGroups(t)
	m2.applyViewState(st)
	if !m2.artTabs[resolveDir(f.project)] {
		t.Error("Artifacts tab not restored")
	}
}

func TestArtifactsMissingStoreIsNoTabs(t *testing.T) {
	f := newSchedFixture(t)
	s := artStore(t) // dir never created
	m := withArtifacts(t, f.twoGroups(t), s)
	if got := headerText(t, m, "agentctl-acme-api", 100); strings.Contains(got, "Artifacts") {
		t.Errorf("header = %q", got)
	}
}

// artPress sends a key and feeds the resulting artifact message back.
func artPress(t *testing.T, m FleetModel, k string) FleetModel {
	t.Helper()
	next, cmd := m.Update(schedKeyMsg(k))
	m = next.(FleetModel)
	if cmd == nil {
		return m
	}
	switch msg := cmd().(type) {
	case fleetArtOpenedMsg, fleetArtPinnedMsg:
		next, _ = m.Update(msg)
		m = next.(FleetModel)
	}
	return m
}
