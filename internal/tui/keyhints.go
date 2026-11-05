package tui

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/theme"
)

// KeyHint represents a single keybinding hint displayed in the chrome bar.
type KeyHint struct {
	Key   string
	Label string
}

// defaultStatusBarHints is the canonical list of keybinding hints shown on
// the chrome bar's keybind line, most important first.
var defaultStatusBarHints = []KeyHint{
	{"a", "launch"},
	{"x", "kill"},
	{"K", "kill all"},
	{"o", "logs"},
	{"s", "settings"},
	{"P", "perms"},
	{"c", "create"},
	{"i", "improve"},
	{"h", "hud"},
	{"t", "tickets"},
	{"T", "tkt-launch"},
	{"Q", "quick-tkt"},
	{"m", "agents"},
	{"A", "agent"},
	{"q", "quit"},
	{"R", "restart"},
	{"n/p", "switch"},
}

// renderKeybindHints renders the ctrl+b badge followed by keybinding hints,
// dropping hints from the end (least important first) until the result fits
// within w visible columns. Segments are measured with lipgloss.Width
// (ANSI-safe) and dropped as whole units, never truncated
// mid-escape-sequence, so the keybind line degrades gracefully at narrow
// widths.
func renderKeybindHints(w int, hints []KeyHint, prefixActive bool, t theme.Theme) string {
	// Badge: vivid when prefix active, dim when inactive.
	badgeStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Subtle)).
		Background(lipgloss.Color(t.Surface))
	if prefixActive {
		badgeStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color(t.Bright)).
			Background(lipgloss.Color(t.Accent))
	}

	// Key and label hint styles: vivid when active, muted when inactive.
	// Use Subtle/Muted (one tier brighter than Overlay/Muted) to meet readable contrast.
	keyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(t.Subtle))
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted))
	if prefixActive {
		keyStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(t.Accent))
		labelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Subtle))
	}

	fillStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted))

	badge := badgeStyle.Render(" ctrl+b ")
	badgeW := lipgloss.Width(badge)

	// Build each hint as a droppable segment: " key label".
	type segment struct {
		text string
		w    int
	}
	segments := make([]segment, 0, len(hints))
	for _, h := range hints {
		s := " " + keyStyle.Render(h.Key) + " " + labelStyle.Render(h.Label)
		segments = append(segments, segment{text: s, w: lipgloss.Width(s)})
	}

	// Drop hints from the end until the whole line fits within w. The badge
	// is never dropped.
	for len(segments) > 0 {
		total := badgeW
		for _, s := range segments {
			total += s.w
		}
		if total <= w {
			break
		}
		segments = segments[:len(segments)-1]
	}

	var sb strings.Builder
	sb.WriteString(badge)
	for _, s := range segments {
		sb.WriteString(s.text)
	}

	content := sb.String()
	contentW := lipgloss.Width(content)
	remaining := w - contentW
	if remaining > 0 {
		content += fillStyle.Render(strings.Repeat(" ", remaining))
	}
	return content
}
