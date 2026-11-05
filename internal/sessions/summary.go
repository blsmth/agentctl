package sessions

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// FormatDailySummary produces a human-readable daily recap of sessions for a
// project on the given date. metas must already be filtered to the correct
// project and date (e.g. via FindMirroredSessions).
func FormatDailySummary(projectDir string, date time.Time, metas []SessionMeta) string {
	if len(metas) == 0 {
		return fmt.Sprintf("No sessions found for %s on %s.",
			abbreviateHome(projectDir), formatDate(date))
	}

	// Determine padding width for agent names (max 20, truncate with ellipsis).
	const maxAgentWidth = 20
	agentWidth := 0
	for _, m := range metas {
		w := utf8.RuneCountInString(m.Agent)
		if w > agentWidth {
			agentWidth = w
		}
	}
	if agentWidth > maxAgentWidth {
		agentWidth = maxAgentWidth
	}

	var sb strings.Builder

	// Header.
	sb.WriteString(fmt.Sprintf("Sessions for %s — %s\n",
		abbreviateHome(projectDir), formatDate(date)))
	sb.WriteString("\n")

	var totalDuration time.Duration
	var totalCost float64

	for i, meta := range metas {
		cost := EstimateCost(meta)
		dur := sessionDuration(meta)
		totalDuration += dur
		totalCost += cost

		agentName := meta.Agent
		agentRunes := []rune(agentName)
		if len(agentRunes) > maxAgentWidth {
			agentName = string(agentRunes[:maxAgentWidth-1]) + "…"
		}

		// Pad agent name.
		padded := agentName + strings.Repeat(" ", agentWidth-utf8.RuneCountInString(agentName))

		startStr := formatTime12h(meta.StartTime)
		endStr := "ongoing"
		if !meta.EndTime.IsZero() {
			endStr = formatTime12h(meta.EndTime)
		}

		durStr := formatSessionDuration(dur)

		sb.WriteString(fmt.Sprintf("  %s  %s – %s  %s  $%.2f  %d turns\n",
			padded, startStr, endStr, durStr, cost, meta.Turns))

		// First user message.
		if meta.FirstUserMessage != "" {
			msg := meta.FirstUserMessage
			runes := []rune(msg)
			if len(runes) > 60 {
				msg = string(runes[:60]) + "…"
			}
			sb.WriteString(fmt.Sprintf("    %q\n", msg))
		}

		// Files edited.
		if len(meta.FilesEdited) > 0 {
			basenames := fileBasenames(meta.FilesEdited, 5)
			sb.WriteString(fmt.Sprintf("    Files edited: %s\n", basenames))
		}

		// Blank line between sessions.
		if i < len(metas)-1 {
			sb.WriteString("\n")
		}
	}

	// Footer.
	sb.WriteString("\n")
	separator := strings.Repeat("─", 53)
	sb.WriteString(fmt.Sprintf("  %s\n", separator))
	sb.WriteString(fmt.Sprintf("  %d session%s · %s active · $%.2f estimated\n",
		len(metas), pluralS(len(metas)),
		formatSessionDuration(totalDuration), totalCost))

	return sb.String()
}

// sessionDuration returns the duration for a session. Ongoing sessions
// (zero EndTime) use elapsed time since StartTime.
func sessionDuration(meta SessionMeta) time.Duration {
	if meta.EndTime.IsZero() {
		return time.Since(meta.StartTime)
	}
	return meta.EndTime.Sub(meta.StartTime)
}

// formatSessionDuration formats a duration as per the spec:
// < 1 minute: (<1m), < 1 hour: (Xm), >= 1 hour: (XhYm).
func formatSessionDuration(d time.Duration) string {
	if d < time.Minute {
		return "(<1m)"
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h == 0 {
		return fmt.Sprintf("(%dm)", m)
	}
	return fmt.Sprintf("(%dh%dm)", h, m)
}

// formatTime12h formats a time.Time in 12-hour local format (e.g. "10:14am").
func formatTime12h(t time.Time) string {
	local := t.In(time.Local)
	h, min, _ := local.Clock()
	ampm := "am"
	if h >= 12 {
		ampm = "pm"
	}
	if h == 0 {
		h = 12
	} else if h > 12 {
		h -= 12
	}
	return fmt.Sprintf("%d:%02d%s", h, min, ampm)
}

// formatDate formats a date as "Sun Mar 30, 2026".
func formatDate(t time.Time) string {
	return t.Format("Mon Jan 2, 2006")
}

// fileBasenames returns a comma-separated list of file basenames.
// If there are more than max files, it appends "+N more".
func fileBasenames(files []string, max int) string {
	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, filepath.Base(f))
	}
	if len(names) <= max {
		return strings.Join(names, ", ")
	}
	more := len(names) - max
	return strings.Join(names[:max], ", ") + fmt.Sprintf(", +%d more", more)
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
