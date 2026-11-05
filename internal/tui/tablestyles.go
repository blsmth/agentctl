package tui

import (
	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/theme"
)

// TableStyles holds the style set shared by all table-based viewers
// (sessions, tickets, agents). Viewer-specific styles — column colors,
// token coloring, selection accents, etc. — are defined as local vars in
// each viewer's own file.
type TableStyles struct {
	// ColHeader is the bold, muted style used for column header labels.
	ColHeader lipgloss.Style

	// Rule is the muted separator line style (╌ repeated).
	Rule lipgloss.Style

	// Dim is the muted foreground style for de-emphasised text.
	Dim lipgloss.Style

	// DetailLabel is the label column inside the detail pane.
	// No fixed width is set; callers apply .Width(N) as needed.
	DetailLabel lipgloss.Style

	// DetailValue is the value column inside the detail pane.
	DetailValue lipgloss.Style

	// DetailSection is used for section headings inside the detail pane
	// (Bold + Text color). Viewers that use a different section color
	// (e.g. sessions using Sky) keep a local override.
	DetailSection lipgloss.Style

	// Subtitle is the italic, muted subtitle / filter-indicator line.
	Subtitle lipgloss.Style

	// HintBar is the centred, muted hint bar at the bottom of the view.
	// Width is applied at render time by each viewer.
	HintBar lipgloss.Style

	// RowSeparator is the muted style for thin row-separator lines (╌).
	RowSeparator lipgloss.Style

	// DetailBox is the rounded-border container wrapping the detail pane.
	DetailBox lipgloss.Style
}

// tableStyles is the package-level shared style instance. It is populated by
// initTableStyles(), which must be called before any individual viewer's init
// function. InitStyles() guarantees this ordering.
var tableStyles TableStyles

// initTableStyles populates tableStyles from the active theme. It must be
// called before initSessionStyles and initTicketStyles
// so that those functions can reference the shared styles.
func initTableStyles() {
	t := theme.Current()

	tableStyles = TableStyles{
		ColHeader: lipgloss.NewStyle().
			Foreground(lipgloss.Color(t.Muted)).
			Bold(true),

		Rule: lipgloss.NewStyle().Foreground(lipgloss.Color(t.Surface)),

		Dim: lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted)),

		DetailLabel: lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted)),

		DetailValue: lipgloss.NewStyle().Foreground(lipgloss.Color(t.Text)),

		DetailSection: lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color(t.Text)),

		Subtitle: lipgloss.NewStyle().
			Foreground(lipgloss.Color(t.Muted)).
			Italic(true),

		HintBar: lipgloss.NewStyle().
			Align(lipgloss.Center).
			MarginTop(1).
			Foreground(lipgloss.Color(t.Muted)),

		RowSeparator: lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted)),

		DetailBox: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color(t.Surface)).
			Padding(1, 2),
	}
}
