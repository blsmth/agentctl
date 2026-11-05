package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/blsmth/agentctl/internal/theme"
)

// PopupDimensions calculates tmux display-popup -w and -h from rendered content.
// Adds 2 to each dimension for the tmux popup border.
func PopupDimensions(rendered string) PopupSize {
	lines := strings.Split(rendered, "\n")
	maxWidth := 0
	for _, line := range lines {
		w := lipgloss.Width(line)
		if w > maxWidth {
			maxWidth = w
		}
	}
	return PopupSize{
		Width:  maxWidth + 2,
		Height: len(lines) + 2,
	}
}

// Styles is a set of pre-configured styles for a popup of a given tmux width.
type Styles struct {
	Box     lipgloss.Style // borderless container with padding
	Title   lipgloss.Style // full-width purple title bar
	HintBar lipgloss.Style // centered hint bar with top margin
	Row     lipgloss.Style // full-width row (for lists)
	RowSel  lipgloss.Style // highlighted row
	Content int            // usable content width in columns
}

// NewStyles creates popup styles for a given tmux popup width.
// Math: tmux border = 2 cols, padding = 4 cols → content = tmuxWidth - 6.
func NewStyles(tmuxWidth int) Styles {
	content := tmuxWidth - 6

	t := theme.Current()
	return Styles{
		Content: content,
		Box:     lipgloss.NewStyle().Padding(1, 2),
		Title: lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color(t.Bright)).
			Background(lipgloss.Color(t.Accent)).
			Padding(0, 2).
			Width(content).
			Align(lipgloss.Center),
		HintBar: lipgloss.NewStyle().
			Width(content).
			Align(lipgloss.Center).
			MarginTop(1).
			Foreground(lipgloss.Color(t.Muted)),
		Row:    lipgloss.NewStyle().Width(content),
		RowSel: lipgloss.NewStyle().Background(lipgloss.Color(t.Surface)).Width(content),
	}
}
