package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/theme"
)

func TestPaletteExcludesStateColours(t *testing.T) {
	InitStyles()
	th := theme.Current()
	for _, c := range fleetPalette() {
		if c == th.Accent || c == th.Green {
			t.Errorf("palette contains state colour %s", c)
		}
	}
}

func TestHeaderChipSpanCoversName(t *testing.T) {
	InitStyles()
	g := fleet.Group{Session: "agentctl-proj", Project: "proj", Rows: []fleet.Row{testRow("a", "agentctl-proj", "idle", "")}}
	for _, cw := range []int{40, 47, 48, 60} {
		m := newFleetModel()
		m.groups = []fleet.Group{g}
		line, spans := m.groupHeaderLayout(g, false, fleetHoverNone, cw)
		plain := ansi.Strip(line)
		end := spans.name[1]
		if got := strings.TrimRight(ansi.TruncateLeft(ansi.Truncate(plain, end, ""), spans.name[0], ""), " "); got != "▾ proj" && got != "▾  proj" {
			t.Errorf("cw=%d name span %v covers %q", cw, spans.name, got)
		}
		wantPad := cw >= fleetChipMinWidth
		if padded := strings.Contains(plain, "▾  proj "); padded != wantPad {
			t.Errorf("cw=%d chip padding = %v, want %v: %q", cw, padded, wantPad, plain)
		}
	}
}

func TestHeaderSelectedKeepsBarAndMarker(t *testing.T) {
	InitStyles()
	g := fleet.Group{Session: "agentctl-proj", Project: "proj"}
	m := newFleetModel()
	line, _ := m.groupHeaderLayout(g, true, fleetHoverNone, 60)
	if !strings.HasPrefix(ansi.Strip(line), "▎▸") {
		t.Errorf("selected header lost bar or marker: %q", ansi.Strip(line))
	}
}

func TestGroupTabsLabels(t *testing.T) {
	InitStyles()
	text, sess, sched := renderGroupTabs(fleetGroupTab{sessions: 17, jobs: 1}, fleetHoverNone, 0, 40)
	if got := ansi.Strip(text); got != "Sessions · Schedules 1" {
		t.Errorf("tabs = %q", got)
	}
	if sess != [2]int{0, 8} || sched[0] != 11 {
		t.Errorf("spans sess=%v sched=%v", sess, sched)
	}
}

func TestSweepButtonBrightensAtFive(t *testing.T) {
	InitStyles()
	mk := func(n int) fleet.Group {
		g := fleet.Group{Session: "s"}
		for i := 0; i < n; i++ {
			r := testRow("a", "s", "idle", "")
			r.Done = true
			g.Rows = append(g.Rows, r)
		}
		return g
	}
	m := newFleetModel()
	if got := m.sweepButtonStyle(mk(4), fleetHoverNone).GetForeground(); got == lipgloss.Color(theme.Current().Text) {
		t.Error("4 sweepable should stay dim")
	}
	if got := m.sweepButtonStyle(mk(5), fleetHoverNone).GetForeground(); got != lipgloss.Color(theme.Current().Text) {
		t.Errorf("5 sweepable foreground = %v, want Text", got)
	}
}
