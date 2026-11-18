package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/fleet"
)

func TestShellOpenedFocusesNewShellRow(t *testing.T) {
	m := mouseTestModel(t, 80, 24)
	mm, cmd := m.Update(fleetShellOpenedMsg{session: "s1", windowID: "@9"})
	got := mm.(FleetModel)
	if got.pendingFocus == nil || got.pendingFocus.windowID != "@9" || cmd == nil {
		t.Fatalf("pendingFocus=%+v cmd=%v", got.pendingFocus, cmd)
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
}
