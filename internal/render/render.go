// Package render holds the presentation-layer helpers shared between the
// chrome bar (internal/tui) and the Claude Code status line
// (internal/statusline). It is a leaf package -- it may import
// lipgloss, internal/theme, and internal/tokens, but it MUST NOT import
// internal/tui, bubbletea, or anything else that pulls a TUI event loop into
// the statusline's ~25ms-per-invocation hot path.
//
// Before this package existed, the chrome bar and the statusline each had
// their own copy of these renderers, and the copies drifted (the
// statusline showed 22% directly above a chrome bar showing 25% for the same
// number). Every caller that needs to render a battery bar, a model badge, a
// token count, a duration, a threshold-colored value, or a rate-limit reset
// time goes through here so the two surfaces can never disagree again.
package render

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/theme"
	"github.com/blsmth/agentctl/internal/tokens"
)

// Threshold pairs a cutoff value with a color, used by ThresholdStyle. Values
// at or below Cutoff get Color; ascending thresholds must be sorted by
// Cutoff.
type Threshold struct {
	Cutoff int
	Color  string
}

// ThresholdStyle returns a lipgloss.Style with the foreground color for val
// based on ascending thresholds. Each threshold is a (cutoff, color) pair.
// Values at or below the first cutoff get the first color; values above the
// last cutoff get the last color. Returns an unstyled Style when thresholds
// is empty.
func ThresholdStyle(val int, thresholds []Threshold) lipgloss.Style {
	if len(thresholds) == 0 {
		return lipgloss.NewStyle()
	}
	for _, t := range thresholds {
		if val <= t.Cutoff {
			return lipgloss.NewStyle().Foreground(lipgloss.Color(t.Color))
		}
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(thresholds[len(thresholds)-1].Color))
}

// SepStyle returns the style for the " · " segment separator used by both
// the chrome bar's metrics row (a since-retired row) and the rich
// statusline. Foreground is theme.Overlay. A function, not a package var,
// because theme.Current() is dynamic -- callers should invoke this once per
// render pass and reuse the returned Style across every separator in that
// pass, not once per segment (see internal/statusline's hot-path note).
func SepStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Current().Overlay))
}

// LabelStyle returns the style for unit labels (" ctx", " cached", " turns",
// the model badge, "5h "/"7d "/"spend "). Foreground is theme.Subtle.
func LabelStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Current().Subtle))
}

// ValueStyle returns the style for plain values that aren't threshold-colored
// (cost, duration). Foreground is theme.Text.
func ValueStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Current().Text))
}

// MutedStyle returns the style for de-emphasized figures (token counts, the
// rate-limit reset time). Foreground is theme.Muted.
func MutedStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Current().Muted))
}

// Battery renders a 20-block battery bar with rounded caps and Lip Gloss
// color. Color: green (<=50%), yellow (<=75%), red (>75%). The bar is wrapped
// in ▐ (left cap) and ▌ (right cap) for a rounded appearance.
func Battery(pct int) string {
	const total = 20
	filled := pct * total / 100
	t := theme.Current()
	color := t.Green
	if pct > 75 {
		color = t.Red
	} else if pct > 50 {
		color = t.Yellow
	}
	filledStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(color))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(t.Overlay))
	capStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted))

	var bar strings.Builder
	bar.WriteString(capStyle.Render("▐"))
	for i := 0; i < total; i++ {
		if i < filled {
			bar.WriteString(filledStyle.Render("█"))
		} else {
			bar.WriteString(dimStyle.Render("░"))
		}
	}
	bar.WriteString(capStyle.Render("▌"))
	return bar.String()
}

// AbbreviateModel returns a short human-readable name for a model string.
// Thin wrapper over tokens.Abbreviate so call sites don't churn.
func AbbreviateModel(model string) string {
	return tokens.Abbreviate(model)
}

// AbbreviateModelFamily returns the bare family badge ("opus"/"sonnet"/
// "haiku"/"fable") for a model string, ignoring Label. Used as a
// narrow-width fallback for AbbreviateModel when the full label doesn't fit.
func AbbreviateModelFamily(model string) string {
	return tokens.Lookup(model).Short
}

// TokensK formats a token count with a "k" suffix for thousands.
func TokensK(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprintf("%d", n)
}

// Duration formats a duration as "Xh Ym", "Xm Ys", or "Xs".
func Duration(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

// ResetTime formats a Unix timestamp as a human-readable reset time.
// If less than 1 hour away, shows relative time (e.g. "resets 45m").
// Otherwise shows clock time (e.g. "resets 5pm"). Returns "" for a zero
// timestamp so callers can omit the segment entirely rather than render a
// placeholder.
func ResetTime(resetsAt int64) string {
	if resetsAt == 0 {
		return ""
	}
	resetTime := time.Unix(resetsAt, 0)
	remaining := time.Until(resetTime)
	if remaining <= 0 {
		return "resets now"
	}
	if remaining < time.Hour {
		m := int(remaining.Minutes())
		if m < 1 {
			m = 1
		}
		return fmt.Sprintf("resets %dm", m)
	}
	return "resets " + resetTime.Format("3pm")
}
