package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestHudHeaderStaysOneLineAndTabClickLands(t *testing.T) {
	m := newTestHud()
	m.width = 30
	m.agentName = strings.Repeat("very-long-agent-name-", 6)
	m.title = strings.Repeat("a very long title ", 6)

	h := m.renderHeader(30)
	if strings.Contains(h, "\n") || lipgloss.Width(h) > 30 {
		t.Fatalf("header must be one line within 30 cols, got height %d width %d", lipgloss.Height(h), lipgloss.Width(h))
	}
	if got := m.tabAt(2, 1); got != tabStream {
		t.Errorf("tabAt(2,1) = %d, want %d", got, tabStream)
	}
	if got := m.tabAt(2, 2); got != -1 {
		t.Errorf("tabAt(2,2) = %d, want -1", got)
	}
}
