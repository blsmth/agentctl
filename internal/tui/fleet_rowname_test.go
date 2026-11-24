package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/fleet"
)

// TestRowNameFallback: the agent name is on no row that has something else to
// show; a row with no title and no recap text shows it instead of going blank.
func TestRowNameFallback(t *testing.T) {
	InitStyles()
	fixedFleetNow(t)
	titled := sampleRow("s", "proj", "@1", "go-engineer:1", "go-engineer", "idle", false, fleet.LogInfo{}, nil)
	titled.Recap = fleet.RecapLine{Text: "full recap", Source: fleet.RecapTitle, Title: "Fixing Hub Rows"}
	role := titled
	role.WindowID, role.ShortRole = "@2", "review"
	role.Recap.Role = "review"
	untitled := sampleRow("s", "proj", "@3", "go-engineer:3", "go-engineer", "idle", false, fleet.LogInfo{}, nil)
	shell := sampleRow("s", "proj", "@4", "shell", "shell", "idle", false, fleet.LogInfo{}, func(r *fleet.Row) { r.Shell = true })

	tests := []struct {
		name         string
		row          fleet.Row
		wantContains []string
		wantAgent    bool
	}{
		{"titled row has no agent name", titled, []string{"Fixing Hub Rows"}, false},
		{"role row has the role, not the agent name", role, []string{"review", "Fixing Hub Rows"}, false},
		{"untitled row falls back to the agent name", untitled, []string{"go-engineer"}, true},
		{"shell row falls back to its name", shell, []string{"shell"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newFleetModel()
			line := ansi.Strip(strings.Split(m.renderRow(tt.row, false, 80), "\n")[0])
			for _, w := range tt.wantContains {
				if !strings.Contains(line, w) {
					t.Errorf("row %q lacks %q", line, w)
				}
			}
			if got := strings.Contains(line, "go-engineer"); got != tt.wantAgent && tt.row.AgentName == "go-engineer" {
				t.Errorf("agent name shown = %v, want %v: %q", got, tt.wantAgent, line)
			}
			if strings.TrimSpace(strings.Trim(line, "▎▸✕–○●⏸✓ ")) == "" {
				t.Errorf("row is blank: %q", line)
			}
		})
	}
}

// TestRowNameFallbackIsTheTitleSpan: the fallback text is the row's title
// span, so the rename click still lands on it.
func TestRowNameFallbackIsTheTitleSpan(t *testing.T) {
	InitStyles()
	fixedFleetNow(t)
	r := sampleRow("s", "proj", "@3", "go-engineer:3", "go-engineer", "idle", false, fleet.LogInfo{}, nil)
	m := newFleetModel()
	line, span := m.treeRowLayout(r, "", false, fleetHoverNone, 80)
	plain := ansi.Strip(line)
	if span[1] <= span[0] {
		t.Fatalf("no title span on the fallback row: %q", plain)
	}
	if !strings.HasPrefix(ansi.Strip(ansi.Cut(line, span[0], span[1])), "go-engineer") {
		t.Errorf("title span covers %q, want the agent name first", ansi.Strip(ansi.Cut(line, span[0], span[1])))
	}
}
