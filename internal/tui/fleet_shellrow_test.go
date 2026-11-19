package tui

import (
	"encoding/json"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/fleet"
)

func TestShellOpenedFocusesNewShellRow(t *testing.T) {
	m := mouseTestModel(t, 80, 24)
	if m.showShells {
		t.Fatal("shell rows must be hidden by default")
	}
	mm, cmd := m.Update(fleetShellOpenedMsg{session: "s1", windowID: "@9"})
	got := mm.(FleetModel)
	if got.pendingFocus == nil || got.pendingFocus.windowID != "@9" || cmd == nil {
		t.Fatalf("pendingFocus=%+v cmd=%v", got.pendingFocus, cmd)
	}
	if !got.showShells {
		t.Fatal("opening a shell must show the shell rows so it can be focused")
	}
	groups := append([]fleet.Group(nil), got.groups...)
	r := testRow("shell", "s1", "", "")
	r.WindowID, r.Shell = "@9", true
	groups[0].Rows = append(append([]fleet.Row(nil), groups[0].Rows...), r)
	mm, _ = got.Update(fleetLoadedMsg{groups: groups})
	got = mm.(FleetModel)
	if selectedWindow(&got) != "@9" || got.pendingFocus != nil {
		t.Fatalf("sel=%q pending=%v", selectedWindow(&got), got.pendingFocus)
	}
	if !rowCanClose(r) {
		t.Fatal("shell row must be closable")
	}
}

func TestRenderShellRow(t *testing.T) {
	InitStyles()
	r := testRow("shell", "agentctl-p", "", "")
	r.Shell = true
	out := FleetModel{}.renderRow(r, false, 60)
	if w := lipgloss.Width(out); w > 60 {
		t.Fatalf("width %d", w)
	}
	for _, bad := range []string{"○", "●", "✓", "⏸", "%"} {
		if strings.Contains(out, bad) {
			t.Errorf("shell row shows %q: %q", bad, out)
		}
	}
	if !strings.Contains(out, "shell") || !strings.Contains(out, "$") {
		t.Errorf("missing shell label/glyph: %q", out)
	}
	// The name says "shell"; the recap does not repeat it.
	if plain := ansi.Strip(out); strings.Count(plain, "shell") != 1 {
		t.Errorf("shell row repeats its name as the recap: %q", plain)
	}
}

func TestShellRowsToggle(t *testing.T) {
	InitStyles()
	shell := testRow("shell", "s1", "", "")
	shell.WindowID, shell.Shell = "@9", true
	agent := testRow("a", "s1", "idle", "")
	agent.WindowID = "@1"
	onlyShell := testRow("shell", "s2", "", "")
	onlyShell.WindowID, onlyShell.Shell = "@3", true
	m := newFleetModel()
	m.loaded = true
	m.groups = []fleet.Group{
		{Session: "s1", Project: "p1", Rows: []fleet.Row{agent, shell}},
		{Session: "s2", Project: "p2", Rows: []fleet.Row{onlyShell}},
	}
	m.rebuildLines()
	tests := []struct {
		name  string
		shown bool
		want  []string
	}{
		{"hidden by default", false, []string{"H:s1", "R:a", "-", "H:s2", "P:s2"}},
		{"shown", true, []string{"H:s1", "R:a", "R:shell", "-", "H:s2", "R:shell"}},
		{"hidden again", false, []string{"H:s1", "R:a", "-", "H:s2", "P:s2"}},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if i > 0 {
				next, cmd := m.Update(tea.KeyPressMsg{Code: '$', Text: fleetShellsKey})
				m = next.(FleetModel)
				_ = cmd
			}
			if m.showShells != tt.shown {
				t.Fatalf("showShells = %v, want %v", m.showShells, tt.shown)
			}
			if got := lineKeys(m); strings.Join(got, " ") != strings.Join(tt.want, " ") {
				t.Errorf("lines = %v, want %v", got, tt.want)
			}
			if s := m.viewState(); s.Shells != tt.shown {
				t.Errorf("view state Shells = %v", s.Shells)
			}
		})
	}
}

func TestShellRowsViewStatePersists(t *testing.T) {
	for _, shown := range []bool{false, true} {
		m := newFleetModel()
		m.showShells = shown
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
		if back.showShells != shown {
			t.Errorf("round trip of shells=%v gave %v (%s)", shown, back.showShells, data)
		}
	}
}

func TestHelpListsShellsKey(t *testing.T) {
	for _, it := range fleetHelpItems() {
		if it.keys == fleetShellsKey && it.action == fleetShellsKey {
			return
		}
	}
	t.Errorf("? reference lacks %q", fleetShellsKey)
}
