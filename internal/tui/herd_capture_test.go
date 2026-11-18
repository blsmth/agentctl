package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/herd"
)

// captureSidebar is the fixture behind the PR's before/after capture: the
// sample sidebar plus a dotted project whose session name is sanitized, a
// four-digit PR, and a row with no recap at all.
func captureSidebar(t *testing.T, width, height int) HerdModel {
	t.Helper()
	m := sampleSidebar(t, width)
	ago := func(d time.Duration) time.Time { return sidebarNow.Add(-d) }
	dotted := []herd.Row{
		sampleRow("agentctl-energy-solutions-ops", "energy-solutions.ops", "@9", "terraform-engineer:1", "terraform-engineer", "idle", true,
			herd.LogInfo{AITitle: "Rotate the RDS credentials", PRNumber: 1025}, func(r *herd.Row) {
				r.StateSince = ago(3 * time.Hour)
			}),
		sampleRow("agentctl-energy-solutions-ops", "energy-solutions.ops", "@10", "claude:2", "claude", "idle", false,
			herd.LogInfo{}, func(r *herd.Row) {
				r.StartTime = ago(4 * time.Minute).Unix()
				r.StateSince = ago(4 * time.Minute)
			}),
	}
	m.groups = append(m.groups, herd.Group{
		Session: "agentctl-energy-solutions-ops", Project: "energy-solutions.ops", Rollup: herd.RollupDone, Rows: dotted,
		ProjectDir: "/src/energy-solutions.ops", RuntimeDir: "/src/energy-solutions.ops/.agentctl",
	})
	for i := range m.groups {
		if m.groups[i].ProjectDir == "" {
			m.groups[i].ProjectDir = "/src/" + m.groups[i].Project
			m.groups[i].RuntimeDir = m.groups[i].ProjectDir + "/.agentctl"
		}
	}
	m.termHeight = height
	m.configureSplitPane()
	m.rebuildLines()
	m.resolveSelection()
	return m
}

// TestHubCapture logs the whole hub column as plain text (go test -run
// TestHubCapture -v) for before/after comparisons, and checks every line fits.
func TestHubCapture(t *testing.T) {
	for _, w := range []int{44, 56} {
		m := captureSidebar(t, w, 30)
		out := ansi.Strip(m.render())
		t.Logf("hub capture (cw=%d):\n%s", m.contentWidth(), out)
		for _, l := range strings.Split(out, "\n") {
			if ansi.StringWidth(l) > m.contentWidth() {
				t.Errorf("line wider than %d: %q", m.contentWidth(), l)
			}
		}
	}
}
