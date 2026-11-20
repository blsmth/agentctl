package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/closedlog"
	"github.com/blsmth/agentctl/internal/fleet"
)

// closedFixture stubs the ledger read and the resume helper. calls records
// every resume; loads counts ledger reads.
type closedFixture struct {
	mu    sync.Mutex
	recs  []closedlog.Record
	loads int
	calls []closedlog.Record
}

func newClosedFixture(t *testing.T, recs []closedlog.Record) *closedFixture {
	t.Helper()
	f := &closedFixture{recs: recs}
	oldL, oldR, oldN := fleetClosedLoad, fleetResumeRun, fleetNow
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	fleetNow = func() time.Time { return now }
	fleetClosedLoad = func(_ *closedlog.Cache, since time.Time) ([]closedlog.Record, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.loads++
		if !since.Equal(now.Add(-fleetClosedWindow)) {
			t.Errorf("since = %v", since)
		}
		return f.recs, nil
	}
	fleetResumeRun = func(r closedlog.Record, env []string) (string, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, r)
		joined := strings.Join(env, "\n")
		if !strings.Contains(joined, "AGENTCTL_RUNTIME_DIR="+filepath.Join(r.ProjectDir, ".agentctl")) {
			t.Errorf("resume env not bound to the project: %v", env)
		}
		return "@99\n", nil
	}
	t.Cleanup(func() { fleetClosedLoad, fleetResumeRun, fleetNow = oldL, oldR, oldN })
	return f
}

func closedRecs(t *testing.T) []closedlog.Record {
	t.Helper()
	log := filepath.Join(t.TempDir(), "u1.jsonl")
	if err := os.WriteFile(log, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	return []closedlog.Record{
		{ClosedAt: now.Add(-2 * time.Hour), WindowID: "@5", Session: "agentctl-p1", ProjectDir: "/x/p1", Agent: "go-engineer", Instance: "go-engineer-2", SessionID: "u1", LogPath: log, ShortTitle: "Fix login", PRURL: "https://x/pull/1"},
		{ClosedAt: now.Add(-3 * time.Hour), WindowID: "@6", Session: "agentctl-p2", ProjectDir: "/x/p2", Agent: "reviewer", Instance: "reviewer", SessionID: "u2"},
		{ClosedAt: now.Add(-4 * time.Hour), WindowID: "@7", Session: "agentctl-p1", ProjectDir: "/x/p1", Agent: "go-engineer", Instance: "go-engineer", ShortTitle: "No session"},
	}
}

func closedModel(t *testing.T) FleetModel {
	t.Helper()
	InitStyles()
	m := newFleetModel()
	m.termWidth, m.termHeight = 80, 40
	m.configureSplitPane()
	m.loaded = true
	a := testRow("a", "s1", "idle", "")
	a.WindowID = "@1"
	m.groups = []fleet.Group{{Session: "s1", Project: "p1", Rows: []fleet.Row{a}}}
	m.rebuildLines()
	m.resolveSelection()
	return m
}

// closedKeys is lineKeys plus the Closed section's lines.
func closedKeys(m FleetModel) []string {
	var out []string
	for _, l := range m.lines {
		switch l.kind {
		case fleetLineClosedLabel:
			out = append(out, "CL:"+l.group.Project)
		case fleetLineClosed:
			out = append(out, "C:"+l.closed.WindowID)
		case fleetLineHeader:
			out = append(out, "H:"+l.group.Session)
		case fleetLineRow:
			out = append(out, "R:"+l.row.Name)
		case fleetLineDivider:
			out = append(out, "-")
		}
	}
	return out
}

func toggleClosedOn(t *testing.T, m FleetModel) FleetModel {
	t.Helper()
	next, _ := m.Update(tea.KeyPressMsg{Code: 'C', Text: fleetClosedKey})
	m = next.(FleetModel)
	if !m.showClosed {
		t.Fatal("toggle did not show the Closed section")
	}
	cmd := m.closedLoadCmd()
	if cmd == nil {
		t.Fatal("no ledger read while shown")
	}
	next, _ = m.Update(cmd())
	return next.(FleetModel)
}

func TestClosedToggleRendersSection(t *testing.T) {
	f := newClosedFixture(t, closedRecs(t))
	m := closedModel(t)
	if m.closedLoadCmd() != nil {
		t.Fatal("ledger read while the section is hidden")
	}
	m = toggleClosedOn(t, m)
	want := "H:s1 R:a - CL: CL:p1 C:@5 C:@7 CL:p2 C:@6"
	if got := strings.Join(closedKeys(m), " "); got != want {
		t.Errorf("lines = %q, want %q", got, want)
	}
	if f.loads != 1 {
		t.Errorf("loads = %d", f.loads)
	}
	out := ansi.Strip(m.renderList())
	for _, s := range []string{"Closed (3)", fleetClosedGlyph + " go-engineer-2", "Fix log", "closed 2h ago"} {
		if !strings.Contains(out, s) {
			t.Errorf("list missing %q:\n%s", s, out)
		}
	}
	for i := range m.lines {
		if w := lipgloss.Width(m.renderLine(i, 80)); w > 80 {
			t.Errorf("line %d is %d wide", i, w)
		}
	}
	if s := m.viewState(); !s.Closed {
		t.Error("view state does not record the toggle")
	}

	// Off again: the section and the cached records go.
	next, _ := m.Update(tea.KeyPressMsg{Code: 'C', Text: fleetClosedKey})
	m = next.(FleetModel)
	if got := strings.Join(closedKeys(m), " "); got != "H:s1 R:a" {
		t.Errorf("lines after hiding = %q", got)
	}
	if m.closedLoadCmd() != nil {
		t.Error("ledger read after hiding")
	}
}

func TestClosedSectionEmptyLedger(t *testing.T) {
	newClosedFixture(t, nil)
	m := toggleClosedOn(t, closedModel(t))
	if got := strings.Join(closedKeys(m), " "); got != "H:s1 R:a - CL:" {
		t.Errorf("lines = %q", got)
	}
	if !strings.Contains(ansi.Strip(m.renderList()), "none in the last 7 days") {
		t.Error("empty section not labelled")
	}
	// The trailing label is never a cursor stop.
	m.moveCursor(1)
	m.moveCursor(1)
	if l := m.lines[m.splitPane.Cursor()]; !l.selectable() {
		t.Errorf("cursor rests on a %v line", l.kind)
	}
}

func selectClosed(t *testing.T, m *FleetModel, windowID string) {
	t.Helper()
	for i, l := range m.lines {
		if l.kind == fleetLineClosed && l.closed.WindowID == windowID {
			m.selectLine(i)
			return
		}
	}
	t.Fatalf("no closed line %s", windowID)
}

func TestClosedRowSelectionNeverShowsLive(t *testing.T) {
	newClosedFixture(t, closedRecs(t))
	m := toggleClosedOn(t, closedModel(t))
	selectClosed(t, &m, "@5")
	if m.selSession != "" || m.selWindowID != "" || m.selClosed == "" {
		t.Fatalf("selection = %q %q %q", m.selSession, m.selWindowID, m.selClosed)
	}
	if m.maybeShowCmd() != nil {
		t.Error("a closed row was handed to the live viewer")
	}
	if m.SelectionEnv() != "" {
		t.Error("a closed row is carried as a live selection")
	}
	// tab has nothing to focus.
	old := focusLivePane
	focusLivePane = func() error { t.Error("tab focused the live pane on a closed row"); return nil }
	t.Cleanup(func() { focusLivePane = old })
	m.runKey("tab")

	// A reload keeps the closed row selected.
	next, _ := m.Update(fleetLoadedMsg{groups: m.groups})
	m = next.(FleetModel)
	if r := m.selectedClosed(); r == nil || r.WindowID != "@5" {
		t.Errorf("after reload selected = %+v", r)
	}
}

func TestClosedEnterResumes(t *testing.T) {
	f := newClosedFixture(t, closedRecs(t))
	m := toggleClosedOn(t, closedModel(t))
	selectClosed(t, &m, "@5")
	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(FleetModel)
	if cmd == nil {
		t.Fatalf("no resume command (err %q)", m.jumpErr)
	}
	msg, ok := cmd().(fleetLaunchedMsg)
	if !ok || msg.err != nil || msg.windowID != "@99" || msg.session != "agentctl-p1" {
		t.Fatalf("msg = %+v", msg)
	}
	if len(f.calls) != 1 || f.calls[0].SessionID != "u1" || f.calls[0].Agent != "go-engineer" || f.calls[0].ProjectDir != "/x/p1" {
		t.Errorf("resume calls = %+v", f.calls)
	}
}

func TestClosedEnterRefusesUnresumable(t *testing.T) {
	recs := closedRecs(t)
	missing := recs[1]
	missing.LogPath = "/nonexistent/u2.jsonl"
	tests := []struct {
		name string
		rec  closedlog.Record
		want string
	}{
		{"no session id", recs[2], "no session id"},
		{"log missing", missing, "missing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newClosedFixture(t, []closedlog.Record{tt.rec})
			m := toggleClosedOn(t, closedModel(t))
			selectClosed(t, &m, tt.rec.WindowID)
			next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			m = next.(FleetModel)
			if cmd != nil {
				cmd()
			}
			if len(f.calls) != 0 {
				t.Errorf("resumed anyway: %+v", f.calls)
			}
			if !strings.Contains(m.jumpErr, tt.want) {
				t.Errorf("jumpErr = %q, want %q", m.jumpErr, tt.want)
			}
		})
	}
}

func TestClosedRowMenuResumes(t *testing.T) {
	f := newClosedFixture(t, closedRecs(t))
	m := toggleClosedOn(t, closedModel(t))
	selectClosed(t, &m, "@5")
	m.openMenu(m.splitPane.Cursor())
	items := m.fleetMenuItems(m.menuLine)
	if len(items) != 2 || items[0].key != "enter" || items[0].label != "resume" || items[1].key != "p" {
		t.Fatalf("menu = %+v", items)
	}
	cmd := m.runMenuKey("enter")
	if cmd == nil {
		t.Fatal("menu resume returned no command")
	}
	cmd()
	if len(f.calls) != 1 {
		t.Errorf("resume calls = %d", len(f.calls))
	}
}

func TestClosedViewStateAndHelp(t *testing.T) {
	for _, shown := range []bool{false, true} {
		m := newFleetModel()
		m.showClosed = shown
		data, err := json.Marshal(m.viewState())
		if err != nil {
			t.Fatal(err)
		}
		s, ok := parseHubView(data)
		if !ok {
			t.Fatalf("parse %s", data)
		}
		var back FleetModel
		back.applyViewState(s)
		if back.showClosed != shown {
			t.Errorf("round trip of closed=%v gave %v (%s)", shown, back.showClosed, data)
		}
	}
	found := false
	for _, it := range fleetHelpItems() {
		if it.keys == fleetClosedKey && it.action == fleetClosedKey {
			found = true
		}
	}
	if !found {
		t.Errorf("? reference lacks %q", fleetClosedKey)
	}
}
