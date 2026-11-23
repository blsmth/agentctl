package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/artifacts"
)

func stubArtifacts(t *testing.T, metas []artifacts.Meta, stamp time.Time) (lists *int, opened *[]string) {
	t.Helper()
	oldS, oldL, oldO := fleetArtifactStamp, fleetArtifactList, fleetArtifactOpen
	t.Cleanup(func() { fleetArtifactStamp, fleetArtifactList, fleetArtifactOpen = oldS, oldL, oldO })
	n := 0
	var got []string
	fleetArtifactStamp = func() (time.Time, bool) { return stamp, true }
	fleetArtifactList = func() ([]artifacts.Meta, error) { n++; return metas, nil }
	fleetArtifactOpen = func(id string) error { got = append(got, id); return nil }
	return &n, &got
}

func TestIndexArtifacts(t *testing.T) {
	got := indexArtifacts([]artifacts.Meta{
		{ID: "new", Session: "s", WindowID: "@1"},
		{ID: "old", Session: "s", WindowID: "@1"},
		{ID: "other", Session: "t", WindowID: "@1"},
		{ID: "bare"},
	})
	if g := got["s|@1"]; g.n != 2 || g.newest != "new" {
		t.Errorf("s|@1 = %+v", g)
	}
	if g := got["t|@1"]; g.n != 1 {
		t.Errorf("t|@1 = %+v", g)
	}
	if len(got) != 2 {
		t.Errorf("artifacts without an origin window must be skipped: %v", got)
	}
}

func TestFleetArtifactChipAndOpen(t *testing.T) {
	m := sampleSidebar(t, 120)
	row := m.lines[m.splitPane.Cursor()].row
	stamp := time.Unix(1000, 0)
	lists, opened := stubArtifacts(t, []artifacts.Meta{
		{ID: "bbbbbbbbbb", Session: row.Session, WindowID: row.WindowID},
		{ID: "aaaaaaaaaa", Session: row.Session, WindowID: row.WindowID},
	}, stamp)

	run := func(cmd tea.Cmd) {
		t.Helper()
		if cmd == nil {
			return
		}
		nm, _ := m.Update(cmd())
		m = nm.(FleetModel)
	}
	if out := ansi.Strip(m.renderTreeRow(row, "", false, 120)); strings.Contains(out, "◆") {
		t.Fatalf("chip before any load: %q", out)
	}
	run(m.artifactsLoadCmd())
	if *lists != 1 {
		t.Fatalf("lists = %d", *lists)
	}
	if out := ansi.Strip(m.renderTreeRow(row, "", false, 120)); !strings.Contains(out, "◆2") {
		t.Errorf("want ◆2 chip: %q", out)
	}
	// Same directory mtime: no re-read.
	run(m.artifactsLoadCmd())
	if *lists != 1 {
		t.Errorf("unchanged store re-read: lists = %d", *lists)
	}
	if m.artifactsLoading {
		t.Error("loading flag stuck")
	}

	// Footer hint and menu entry appear on the row.
	found := false
	for _, h := range m.footerHints() {
		found = found || h.key == fleetArtifactKey
	}
	if !found {
		t.Error("footer lacks the o hint")
	}
	found = false
	for _, it := range m.fleetMenuItems(m.splitPane.Cursor()) {
		found = found || it.key == fleetArtifactKey
	}
	if !found {
		t.Error("menu lacks artifact")
	}

	_, cmd := m.handleKey(tea.KeyPressMsg{Code: 'o', Text: "o"})
	if cmd == nil {
		t.Fatal("o returned no command")
	}
	run(cmd)
	if len(*opened) != 1 || (*opened)[0] != "bbbbbbbbbb" {
		t.Errorf("opened = %v", *opened)
	}
	if m.sortRecent {
		t.Error("o on an artifact row must not toggle the sort")
	}
}

func TestFleetArtifactKeyFallsBackToSort(t *testing.T) {
	m := sampleSidebar(t, 120)
	_, opened := stubArtifacts(t, nil, time.Unix(1, 0))
	before := m.sortRecent
	m.handleKey(tea.KeyPressMsg{Code: 'o', Text: "o"})
	if m.sortRecent == before {
		t.Error("o on a row without artifacts should toggle the sort")
	}
	if len(*opened) != 0 {
		t.Errorf("opened = %v", *opened)
	}
}
