package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestScanZones(t *testing.T) {
	InitStyles()
	btn := zoneMark("btn", "[ Go ]")
	styled := lipgloss.NewStyle().Foreground(lipgloss.Color("#ff0000")).Render("ab" + btn)
	tests := []struct {
		name   string
		frame  string
		id     string
		wantX0 int
		wantX1 int
		wantY  int
	}{
		{"plain", "xx" + btn, "btn", 2, 8, 0},
		{"second line", "row0\n  " + btn + " tail", "btn", 2, 8, 1},
		{"styled", styled, "btn", 2, 8, 0},
		{"padded width", lipgloss.NewStyle().Width(20).Render("ab" + btn), "btn", 2, 8, 0},
		{"joined right", lipgloss.JoinHorizontal(lipgloss.Top, "left", "  ", btn), "btn", 6, 12, 0},
		{"inside border", lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Render(btn), "btn", 1, 7, 1},
		{"wide runes before", "日本" + btn, "btn", 4, 10, 0},
		{"truncated end", ansi.Truncate("ab"+btn, 5, ""), "btn", 2, 5, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, zones := scanZones(tt.frame)
			if strings.Contains(out, "z") && zoneMarkerRE.MatchString(out) {
				t.Fatalf("markers left in output: %q", out)
			}
			r, ok := zones.find(tt.id)
			if !ok {
				t.Fatalf("zone %q not found in %q", tt.id, out)
			}
			if r.x0 != tt.wantX0 || r.x1 != tt.wantX1 || r.y != tt.wantY {
				t.Errorf("zone = [%d,%d) y=%d, want [%d,%d) y=%d", r.x0, r.x1, r.y, tt.wantX0, tt.wantX1, tt.wantY)
			}
		})
	}
}

func TestZoneListAtPrefersInnermost(t *testing.T) {
	frame := zoneMark("row:1", "  #1  "+zoneMark("label:bug", "bug")+"  title")
	_, zones := scanZones(frame)
	if got := zones.at(7, 0); got != "label:bug" {
		t.Errorf("at(label) = %q, want label:bug", got)
	}
	if got := zones.at(1, 0); got != "row:1" {
		t.Errorf("at(row) = %q, want row:1", got)
	}
	if got := zones.at(40, 0); got != "" {
		t.Errorf("at(outside) = %q, want empty", got)
	}
	if got := zones.atPrefix(7, 0, "row:"); got != "row:1" {
		t.Errorf("atPrefix(row:) = %q, want row:1", got)
	}
}
