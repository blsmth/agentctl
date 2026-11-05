package chromebar

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/theme"
	"github.com/blsmth/agentctl/internal/tmux"
)

// Tick pushes chrome content to header/footer panes for all agents.
// It is called at the end of each monitor tick with the in-memory agent state,
// avoiding the serialize→write→read→deserialize roundtrip through state.json.
func Tick(agents []state.AgentState, lastHeader, lastFooter map[string]string) {
	for _, a := range agents {
		if a.HeaderPane == "" || a.FooterPane == "" {
			continue
		}

		// Enforce chrome pane heights on every tick by growing the main pane.
		// Resizing chrome panes individually causes ping-pong inflation when
		// adjacent chrome panes trade space; growing the main pane forces all
		// chrome panes to 1 row without that side-effect.
		tmux.EnforceMainPaneSize(a.WindowID)

		headerWidth := tmux.PaneWidth(a.HeaderPane)
		footerWidth := tmux.PaneWidth(a.FooterPane)

		header := renderChromeHeader(a, headerWidth)
		if header != lastHeader[a.HeaderPane] {
			if err := tmux.WriteToPaneDirect(a.HeaderPane, header); err == nil {
				lastHeader[a.HeaderPane] = header
			}
		}

		footer := renderChromeFooter(a, footerWidth)
		if footer != lastFooter[a.FooterPane] {
			if err := tmux.WriteToPaneDirect(a.FooterPane, footer); err == nil {
				lastFooter[a.FooterPane] = footer
			}
		}
	}
}

// Chrome rendering styles — initialized lazily on first use.
var (
	chromeStylesInit   bool
	chromeNameStyle    lipgloss.Style
	chromeSepStyle     lipgloss.Style
	chromeWorkingStyle lipgloss.Style
	chromeIdleStyle    lipgloss.Style
	chromeBlockedStyle lipgloss.Style
	chromeDoneStyle    lipgloss.Style
	chromeFooterLabel  lipgloss.Style
	chromeFooterValue  lipgloss.Style
	chromeFooterSep    lipgloss.Style
)

func ensureChromeStyles() {
	if chromeStylesInit {
		return
	}
	chromeStylesInit = true
	t := theme.Current()

	chromeNameStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(t.Text))
	chromeSepStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Overlay))
	chromeWorkingStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(t.Green))
	chromeIdleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted))
	chromeBlockedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(t.Yellow))
	chromeDoneStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(t.Accent))
	chromeFooterLabel = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Subtle))
	chromeFooterValue = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Text))
	chromeFooterSep = lipgloss.NewStyle().Foreground(lipgloss.Color(t.Overlay))
}

// renderChromeHeader renders a single-line header: agent name + state badge, left-aligned.
func renderChromeHeader(a state.AgentState, width int) string {
	ensureChromeStyles()

	name := a.Name
	if name == "" {
		name = a.AgentName
	}
	if name == "" {
		name = "agent"
	}

	badge := chromeStateBadge(a.State, a.Done)

	return chromeNameStyle.Render(" "+name+" ") +
		chromeSepStyle.Render(" ─ ") +
		badge
}

// chromeStateBadge returns a styled state badge string.
func chromeStateBadge(agentState string, done bool) string {
	ensureChromeStyles()
	s := agentState
	if done {
		s = "done"
	}
	switch s {
	case "working":
		return chromeWorkingStyle.Render(" ● Working ")
	case "blocked":
		return chromeBlockedStyle.Render(" ⏸ Blocked ")
	case "done":
		return chromeDoneStyle.Render(" ✓ Done ")
	default:
		return chromeIdleStyle.Render(" ○ Idle ")
	}
}

// renderChromeFooter renders a single-line footer: metrics right-aligned with space padding.
func renderChromeFooter(a state.AgentState, width int) string {
	ensureChromeStyles()
	if width < 20 {
		width = 20
	}

	var metrics strings.Builder

	if a.ContextPct > 0 {
		bar := chromeBatteryBar(a.ContextPct)
		pctStr := chromeFooterValue.Render(fmt.Sprintf("%d%%", a.ContextPct))
		label := chromeFooterLabel.Render(" ctx")
		metrics.WriteString(bar + " " + pctStr + label)
	}

	totalTokens := a.InputTokens + a.OutputTokens
	if totalTokens > 0 {
		if metrics.Len() > 0 {
			metrics.WriteString(chromeFooterSep.Render(" · "))
		}
		tokStr := chromeFooterValue.Render(
			fmt.Sprintf("%s↑ %s↓", chromeFormatTokensK(a.InputTokens), chromeFormatTokensK(a.OutputTokens)),
		)
		metrics.WriteString(tokStr + chromeFooterLabel.Render(" tokens"))
	}

	if a.TurnCount > 0 {
		if metrics.Len() > 0 {
			metrics.WriteString(chromeFooterSep.Render(" · "))
		}
		metrics.WriteString(
			chromeFooterValue.Render(fmt.Sprintf("%d", a.TurnCount)) +
				chromeFooterLabel.Render(" turns"),
		)
	}

	if a.MailboxPending > 0 {
		if metrics.Len() > 0 {
			metrics.WriteString(chromeFooterSep.Render(" · "))
		}
		metrics.WriteString(chromeFooterValue.Render(fmt.Sprintf("✉ %d", a.MailboxPending)))
	}

	if a.StartTime > 0 {
		elapsed := time.Since(time.Unix(a.StartTime, 0))
		if elapsed >= time.Second {
			if metrics.Len() > 0 {
				metrics.WriteString(chromeFooterSep.Render(" · "))
			}
			metrics.WriteString(chromeFooterValue.Render(chromeFormatDuration(elapsed)))
		}
	}

	metricsStr := metrics.String()
	metricsW := lipgloss.Width(metricsStr)

	pad := width - metricsW
	if pad < 0 {
		pad = 0
	}
	return strings.Repeat(" ", pad) + metricsStr
}

// chromeBatteryBar renders a 20-block bar with color based on percentage.
func chromeBatteryBar(pct int) string {
	ensureChromeStyles()
	const total = 20
	filled := pct * total / 100
	t := theme.Current()
	color := t.Green
	if pct > 75 {
		color = t.Red
	} else if pct > 50 {
		color = t.Yellow
	}
	style := lipgloss.NewStyle().Foreground(lipgloss.Color(color))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(t.Overlay))
	var bar strings.Builder
	for i := 0; i < total; i++ {
		if i < filled {
			bar.WriteString(style.Render("█"))
		} else {
			bar.WriteString(dimStyle.Render("░"))
		}
	}
	return bar.String()
}

// chromeFormatTokensK formats a token count with a "k" suffix for thousands.
func chromeFormatTokensK(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprintf("%d", n)
}

// chromeFormatDuration formats a duration as "Xh Ym", "Xm Ys", or "Xs".
func chromeFormatDuration(d time.Duration) string {
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
