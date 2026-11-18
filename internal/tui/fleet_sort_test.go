package tui

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// setActivity stamps LastActivity on the fold fixture's rows by window ID.
func setActivity(m *FleetModel, byWin map[string]int64) {
	for gi := range m.groups {
		for ri := range m.groups[gi].Rows {
			m.groups[gi].Rows[ri].LastActivity = byWin[m.groups[gi].Rows[ri].WindowID]
		}
	}
}

func TestSortToggleKeepsSelectionAndPersists(t *testing.T) {
	m := foldModel(t)
	setActivity(&m, map[string]int64{"@1": 10, "@3": 50})
	m.viewPath = filepath.Join(t.TempDir(), "hub-view.json")
	m.selSession, m.selWindowID, m.selIsHeader = "s1", "@3", false
	m.resolveSelection()

	m, cmd := pressHub(m, "o")
	if !m.sortRecent {
		t.Fatal("o did not enable recent sort")
	}
	if cmd == nil {
		t.Error("toggle returned no save command")
	}
	keys := lineKeys(m)
	if i, j := slices.Index(keys, "R:x"), slices.Index(keys, "R:p"); i > j {
		t.Errorf("x (newer) should precede p: %v", keys)
	}
	if m.selWindowID != "@3" {
		t.Errorf("selection moved to %q, want @3", m.selWindowID)
	}
	if m.viewState().Sort != "recent" {
		t.Errorf("viewState.Sort = %q", m.viewState().Sort)
	}

	// A poll tick that reorders rows must not move the cursor.
	setActivity(&m, map[string]int64{"@1": 99, "@3": 5})
	m.groups = append(m.groups[:0:0], m.groups...)
	m.rebuildLines()
	m.resolveSelection()
	if m.selWindowID != "@3" || m.lines[m.splitPane.Cursor()].row.WindowID != "@3" {
		t.Errorf("cursor left @3 after reorder: sel=%q", m.selWindowID)
	}

	m, _ = pressHub(m, "o")
	if m.sortRecent || m.selWindowID != "@3" {
		t.Errorf("toggle off: sortRecent=%v sel=%q", m.sortRecent, m.selWindowID)
	}
}

func TestSortViewStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub-view.json")
	if err := writeHubView(path, hubViewState{Sort: "recent"}); err != nil {
		t.Fatal(err)
	}
	m := foldModel(t)
	m.applyViewState(readHubView(path))
	if !m.sortRecent {
		t.Error("sort not restored from file")
	}
	s, ok := parseHubView([]byte(`{"folded":["s1"],"filter":"active"}`))
	if !ok || s.Sort != "" {
		t.Fatalf("old file: ok=%v sort=%q", ok, s.Sort)
	}
	m.applyViewState(s)
	if m.sortRecent {
		t.Error("old file without sort must mean window order")
	}
	if err := os.WriteFile(path, []byte(`{"focus":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if readHubView(path).Sort != "" {
		t.Error("missing field should be window order")
	}
}
