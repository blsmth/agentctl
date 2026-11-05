package sessions

import (
	"fmt"
	"strings"
	"time"
)

// ANSI color helpers for report colorization.
const (
	colorReset   = "\033[0m"
	colorBold    = "\033[1m"
	colorDim     = "\033[2m"
	colorCyan    = "\033[36m"
	colorYellow  = "\033[33m"
	colorGreen   = "\033[32m"
	colorMagenta = "\033[35m"
	colorBlue    = "\033[34m"
	colorWhite   = "\033[37m"
)

// BuildReport formats sessions into a human-readable ANSI-colored report.
func BuildReport(agentName string, sess []Session) string {
	var b strings.Builder
	loc := time.Now().Location()

	for i, s := range sess {
		sum, err := ParseSummary(s)
		if err != nil {
			continue
		}
		if i > 0 {
			b.WriteString("\n\n")
		}

		// Header
		b.WriteString(fmt.Sprintf("%s%s═══ %s — Session Log ═══%s\n\n", colorBold, colorCyan, agentName, colorReset))
		b.WriteString(fmt.Sprintf("%sSession:%s  %s\n", colorDim, colorReset, s.ID))
		b.WriteString(fmt.Sprintf("%sProject:%s  %s\n", colorDim, colorReset, abbreviateHome(s.Project)))
		b.WriteString(fmt.Sprintf("%sStarted:%s  %s\n", colorDim, colorReset, s.StartTime.In(loc).Format("Mon Jan 2 3:04 PM")))
		if !s.EndTime.IsZero() {
			b.WriteString(fmt.Sprintf("%sEnded:%s    %s\n", colorDim, colorReset, s.EndTime.In(loc).Format("Mon Jan 2 3:04 PM")))
		}
		b.WriteString(fmt.Sprintf("%sDuration:%s %s\n", colorDim, colorReset, formatDuration(s.Duration())))
		b.WriteString("\n")

		if sum.TurnCount > 0 {
			b.WriteString(fmt.Sprintf("%s%s── Token Usage ──%s\n", colorBold, colorYellow, colorReset))
			b.WriteString(fmt.Sprintf("  %sInput:%s        %s tokens\n", colorDim, colorReset, FormatTokenCount(sum.TotalInputTokens)))
			b.WriteString(fmt.Sprintf("  %sOutput:%s       %s tokens\n", colorDim, colorReset, FormatTokenCount(sum.TotalOutputTokens)))
			b.WriteString(fmt.Sprintf("  %sCache write:%s  %s tokens\n", colorDim, colorReset, FormatTokenCount(sum.CacheCreateTokens)))
			b.WriteString(fmt.Sprintf("  %sCache read:%s   %s tokens\n", colorDim, colorReset, FormatTokenCount(sum.CacheReadTokens)))
			b.WriteString(fmt.Sprintf("  %sTurns:%s        %d\n", colorDim, colorReset, sum.TurnCount))
			b.WriteString("\n")
		}

		if len(sum.UserMsgs) > 0 {
			b.WriteString(fmt.Sprintf("%s%s── User Messages ──%s\n", colorBold, colorGreen, colorReset))
			for _, msg := range sum.UserMsgs {
				b.WriteString(fmt.Sprintf("  %s›%s %s\n", colorGreen, colorReset, msg))
			}
			b.WriteString("\n")
		}

		if len(sum.FilesEdited) > 0 {
			b.WriteString(fmt.Sprintf("%s%s── Files Changed ──%s\n", colorBold, colorMagenta, colorReset))
			for _, f := range sum.FilesEdited {
				b.WriteString(fmt.Sprintf("  %s✎%s %s\n", colorMagenta, colorReset, abbreviateHome(f)))
			}
			b.WriteString("\n")
		}

		if len(sum.FilesRead) > 0 {
			b.WriteString(fmt.Sprintf("%s%s── Files Read ──%s\n", colorBold, colorBlue, colorReset))
			for _, f := range sum.FilesRead {
				b.WriteString(fmt.Sprintf("  %s◉%s %s\n", colorBlue, colorReset, abbreviateHome(f)))
			}
			b.WriteString("\n")
		}

		if len(sum.ToolCalls) > 0 {
			b.WriteString(fmt.Sprintf("%s%s── Tool Calls ──%s\n", colorBold, colorYellow, colorReset))
			for _, tc := range sum.ToolCalls {
				ts := ""
				if !tc.Timestamp.IsZero() {
					ts = colorDim + tc.Timestamp.In(loc).Format("3:04:05 PM") + colorReset + " "
				}
				detail := ""
				if tc.Detail != "" {
					detail = colorDim + " — " + tc.Detail + colorReset
				}
				b.WriteString(fmt.Sprintf("  %s%s%s%s%s\n", ts, colorWhite, tc.Name, colorReset, detail))
			}
			b.WriteString("\n")
		}

		if len(sum.TextBlocks) > 0 {
			b.WriteString(fmt.Sprintf("%s%s── Assistant Output ──%s\n", colorBold, colorCyan, colorReset))
			for _, text := range sum.TextBlocks {
				b.WriteString(fmt.Sprintf("  %s\n\n", text))
			}
		}
	}

	return b.String()
}

// FormatTokenCount formats an integer with comma separators for readability.
// e.g. 142381 → "142,381", 850 → "850".
func FormatTokenCount(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	// Insert commas from right to left every 3 digits.
	result := make([]byte, 0, len(s)+len(s)/3)
	rem := len(s) % 3
	if rem == 0 {
		rem = 3
	}
	result = append(result, s[:rem]...)
	for i := rem; i < len(s); i += 3 {
		result = append(result, ',')
		result = append(result, s[i:i+3]...)
	}
	return string(result)
}

// GetSessionPreview returns a short preview of the first user message in a session.
func GetSessionPreview(s Session) string {
	sum, err := ParseSummary(s)
	if err != nil || len(sum.UserMsgs) == 0 {
		return ""
	}
	preview := sum.UserMsgs[0]
	if len(preview) > 55 {
		preview = truncateBytes(preview, 55) + "..."
	}
	return preview
}

// formatDuration renders d compactly: "45s", "12m", "3h", or "3h5m".
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh%dm", h, m)
}
