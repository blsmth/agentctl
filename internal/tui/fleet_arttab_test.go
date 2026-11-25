package tui

import (
	"errors"
	"fmt"
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
	case fleetArtOpenedMsg, fleetArtPinnedMsg, fleetArtDeletedMsg:
		next, _ = m.Update(msg)
		m = next.(FleetModel)
	}
	return m
}

// stubArtRemove replaces the delete seam and records the ids it was asked to
// remove; err is returned for every call.
func stubArtRemove(t *testing.T, err error) *[]string {
	t.Helper()
	orig := fleetArtifactRemove
	t.Cleanup(func() { fleetArtifactRemove = orig })
	var calls []string
	fleetArtifactRemove = func(id string) (artifacts.Meta, error) {
		calls = append(calls, id)
		return artifacts.Meta{ID: id}, err
	}
	return &calls
}

// artDeleteFixture is a model on the Artifacts tab with n artifacts, cursor
// on the first.
func artDeleteFixture(t *testing.T, titles ...string) (FleetModel, []artifacts.Meta) {
	t.Helper()
	f := newSchedFixture(t)
	s := artStore(t)
	var metas []artifacts.Meta
	for _, title := range titles {
		metas = append(metas, addArt(t, s, f.project, title))
	}
	m := withArtifacts(t, f.twoGroups(t), s)
	m = schedPress(t, m, fleetSchedulesKey)
	if l := m.selectedArtLine(); l == nil {
		t.Fatal("cursor not on an artifact row")
	}
	return m, metas
}

func artRowText(m FleetModel, id string) string {
	for _, l := range m.lines {
		if l.kind == fleetLineArtifact && l.art.ID == id {
			return ansi.Strip(m.renderArtRow(l, true, fleetHoverNone, 80))
		}
	}
	return ""
}

func TestArtifactDeleteConfirm(t *testing.T) {
	m, metas := artDeleteFixture(t, "only")
	calls := stubArtRemove(t, nil)
	id := m.selectedArtLine().art.ID
	_ = metas

	m = artPress(t, m, "x")
	if !m.artDeleteArmed() || len(*calls) != 0 {
		t.Fatalf("x should arm without deleting: armed=%v calls=%v", m.artDeleteArmed(), *calls)
	}
	if got := artRowText(m, id); !strings.Contains(got, "delete? y/n") {
		t.Errorf("armed row = %q", got)
	}
	var keys []string
	for _, h := range m.footerHints() {
		keys = append(keys, h.key)
	}
	if strings.Join(keys, "") != "yn" {
		t.Errorf("armed hints = %v", keys)
	}
	m = artPress(t, m, "y")
	if len(*calls) != 1 || (*calls)[0] != id {
		t.Fatalf("stub calls = %v, want [%s]", *calls, id)
	}
	if m.artDeleteArmed() || len(m.artList) != 0 {
		t.Errorf("armed=%v artList=%d", m.artDeleteArmed(), len(m.artList))
	}
	if !strings.Contains(m.infoMsg, "deleted artifact "+artifacts.ShortID(id)) || strings.Contains(m.infoMsg, id) {
		t.Errorf("footer = %q", m.infoMsg)
	}
}

func TestArtifactDeleteCancel(t *testing.T) {
	for _, key := range []string{"n", "N", "esc"} {
		t.Run(key, func(t *testing.T) {
			m, _ := artDeleteFixture(t, "only")
			calls := stubArtRemove(t, nil)
			m = artPress(t, m, "x")
			m = artPress(t, m, key)
			if m.artDeleteArmed() || len(*calls) != 0 || len(m.artList) != 1 {
				t.Errorf("armed=%v calls=%v list=%d", m.artDeleteArmed(), *calls, len(m.artList))
			}
		})
	}
	t.Run("other key cancels and runs", func(t *testing.T) {
		m, _ := artDeleteFixture(t, "one", "two")
		calls := stubArtRemove(t, nil)
		m = artPress(t, m, "x")
		first := m.selectedArtLine().art.ID
		m = artPress(t, m, "j")
		if m.artDeleteArmed() || len(*calls) != 0 {
			t.Fatalf("armed=%v calls=%v", m.artDeleteArmed(), *calls)
		}
		if l := m.selectedArtLine(); l == nil || l.art.ID == first {
			t.Error("j did not move the cursor")
		}
	})
	t.Run("other key runs as usual", func(t *testing.T) {
		m, _ := artDeleteFixture(t, "one")
		stubArtRemove(t, nil)
		pinned := false
		orig := fleetArtifactPin
		t.Cleanup(func() { fleetArtifactPin = orig })
		fleetArtifactPin = func(id string, p bool) (artifacts.Meta, error) {
			pinned = p
			return artifacts.Meta{ID: id, Pinned: p}, nil
		}
		m = artPress(t, m, "x")
		m = artPress(t, m, fleetArtPinKey)
		if m.artDeleteArmed() || !pinned {
			t.Errorf("armed=%v pinned=%v", m.artDeleteArmed(), pinned)
		}
	})
	t.Run("tab switch", func(t *testing.T) {
		m, _ := artDeleteFixture(t, "one")
		m = artPress(t, m, "x")
		m = schedPress(t, m, fleetSchedulesKey)
		if m.artDeleteArmed() {
			t.Error("still armed after a tab switch")
		}
	})
	t.Run("list refresh without the id", func(t *testing.T) {
		m, _ := artDeleteFixture(t, "one", "two")
		m = artPress(t, m, "x")
		m.applyArtifacts(fleetArtifactsMsg{stamp: time.Now(), list: m.artList[:0:0]})
		if m.artDeleteArmed() {
			t.Error("still armed after the artifact vanished")
		}
	})
}

func TestArtifactDeletePinnedText(t *testing.T) {
	m, _ := artDeleteFixture(t, "keeper")
	id := m.selectedArtLine().art.ID
	for i := range m.artList {
		m.artList[i].Pinned = true
	}
	m.relayout()
	stubArtRemove(t, nil)
	m = artPress(t, m, "x")
	if got := artRowText(m, id); !strings.Contains(got, "delete pinned? y/n") {
		t.Errorf("armed pinned row = %q", got)
	}
}

func TestArtifactDeleteErrors(t *testing.T) {
	t.Run("error", func(t *testing.T) {
		m, _ := artDeleteFixture(t, "one")
		stubArtRemove(t, errors.New("disk on fire"))
		m = artPress(t, m, "x")
		m = artPress(t, m, "y")
		if !strings.Contains(m.jumpErr, "delete: disk on fire") || len(m.artList) != 1 {
			t.Errorf("err=%q list=%d", m.jumpErr, len(m.artList))
		}
	})
	t.Run("not found", func(t *testing.T) {
		m, _ := artDeleteFixture(t, "one")
		id := m.selectedArtLine().art.ID
		stubArtRemove(t, fmt.Errorf("%w: gone", artifacts.ErrNotFound))
		m = artPress(t, m, "x")
		m = artPress(t, m, "y")
		if m.jumpErr != "" || len(m.artList) != 0 || !strings.Contains(m.infoMsg, "already deleted") || strings.Contains(m.infoMsg, id) {
			t.Errorf("err=%q info=%q list=%d", m.jumpErr, m.infoMsg, len(m.artList))
		}
	})
}

func TestArtifactDeleteCursorPlacement(t *testing.T) {
	t.Run("middle goes to next", func(t *testing.T) {
		m, _ := artDeleteFixture(t, "a", "b", "c")
		stubArtRemove(t, nil)
		ids := []string{}
		for _, l := range artRows(m, "agentctl-acme-api") {
			ids = append(ids, l.art.ID)
		}
		m = artPress(t, m, "x")
		m = artPress(t, m, "y")
		if l := m.selectedArtLine(); l == nil || l.art.ID != ids[1] {
			t.Errorf("cursor = %+v, want %s", l, ids[1])
		}
	})
	t.Run("last goes to previous", func(t *testing.T) {
		m, _ := artDeleteFixture(t, "a", "b")
		stubArtRemove(t, nil)
		rows := artRows(m, "agentctl-acme-api")
		m = artPress(t, m, "j")
		m = artPress(t, m, "x")
		m = artPress(t, m, "y")
		if l := m.selectedArtLine(); l == nil || l.art.ID != rows[0].art.ID {
			t.Errorf("cursor = %+v, want %s", l, rows[0].art.ID)
		}
	})
	t.Run("emptied tab goes to the header", func(t *testing.T) {
		m, _ := artDeleteFixture(t, "only")
		stubArtRemove(t, nil)
		m = artPress(t, m, "x")
		m = artPress(t, m, "y")
		if got := tabKey(m, "agentctl-acme-api"); got != -1 && got != tabKindSessions {
			t.Errorf("tab = %d, want the vanished tab gone", got)
		}
		idx := m.splitPane.Cursor()
		if idx != schedLineHeaderIdx2(m, "agentctl-acme-api") {
			t.Errorf("cursor line %d (%v), want the group header %d", idx, m.lines[idx].kind, schedLineHeaderIdx2(m, "agentctl-acme-api"))
		}
	})
}

func TestArtifactDeleteExclusiveWithClose(t *testing.T) {
	m, _ := artDeleteFixture(t, "one")
	stubArtRemove(t, nil)
	m.confirmSession, m.confirmWindow = "agentctl-acme-api", "@1"
	m = artPress(t, m, "x")
	if m.closeArmed() || !m.artDeleteArmed() {
		t.Errorf("close=%v del=%v", m.closeArmed(), m.artDeleteArmed())
	}
	// And the other way round.
	m.startClose(0)
	m.artDelID = "x"
	m.startClose(schedLineHeaderIdx2(m, "agentctl-acme-api") + 1)
	if m.artDeleteArmed() && m.closeArmed() {
		t.Error("both confirmations armed")
	}
}
