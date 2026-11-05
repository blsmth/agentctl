package sessions

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// AgentUsage holds aggregated token usage for a single agent.
type AgentUsage struct {
	Agent         string
	Sessions      int
	Turns         int
	InputTokens   int
	OutputTokens  int
	CacheRead     int
	CacheCreate   int
	TotalDuration time.Duration
}

// TotalTokens returns the sum of input and output tokens.
func (u *AgentUsage) TotalTokens() int {
	return u.InputTokens + u.OutputTokens
}

// AggregateUsage filters sessions by time range and sums token usage per agent.
// Sessions that started after [to] are skipped. Sessions that ended before [from]
// are skipped. Active sessions (zero EndTime) spanning the range are included.
func AggregateUsage(byAgent map[string][]Session, from, to time.Time) []AgentUsage {
	usageMap := make(map[string]*AgentUsage)

	for agent, sess := range byAgent {
		for _, s := range sess {
			// Skip sessions that started after the range.
			if s.StartTime.After(to) {
				continue
			}
			// Skip sessions that ended before the range.
			if !s.EndTime.IsZero() && s.EndTime.Before(from) {
				continue
			}

			sum, err := ParseSummary(s)
			if err != nil || sum.TurnCount == 0 {
				continue
			}

			u, ok := usageMap[agent]
			if !ok {
				u = &AgentUsage{Agent: agent}
				usageMap[agent] = u
			}
			u.Sessions++
			u.Turns += sum.TurnCount
			u.InputTokens += sum.TotalInputTokens
			u.OutputTokens += sum.TotalOutputTokens
			u.CacheRead += sum.CacheReadTokens
			u.CacheCreate += sum.CacheCreateTokens
			u.TotalDuration += s.Duration()
		}
	}

	// Sort by total tokens descending.
	result := make([]AgentUsage, 0, len(usageMap))
	for _, u := range usageMap {
		result = append(result, *u)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].TotalTokens() > result[j].TotalTokens()
	})
	return result
}

// WriteUsageTable writes a formatted usage table into b for the given agent usage slice.
func WriteUsageTable(b *strings.Builder, usage []AgentUsage) {
	if len(usage) == 0 {
		b.WriteString("    No usage data.\n")
		return
	}

	// Find max agent name width.
	maxName := 5 // "Agent"
	for _, u := range usage {
		if len(u.Agent) > maxName {
			maxName = len(u.Agent)
		}
	}

	// Header
	b.WriteString(fmt.Sprintf("    %-*s  %8s  %8s  %8s  %5s  %5s\n",
		maxName, "Agent", "Input", "Output", "Total", "Turns", "Sess"))
	b.WriteString(fmt.Sprintf("    %s  %s  %s  %s  %s  %s\n",
		strings.Repeat("─", maxName),
		strings.Repeat("─", 8),
		strings.Repeat("─", 8),
		strings.Repeat("─", 8),
		strings.Repeat("─", 5),
		strings.Repeat("─", 5)))

	var totInput, totOutput, totTurns, totSess int
	for _, u := range usage {
		b.WriteString(fmt.Sprintf("    %-*s  %8s  %8s  %8s  %5d  %5d\n",
			maxName, u.Agent,
			FormatTokensShort(u.InputTokens),
			FormatTokensShort(u.OutputTokens),
			FormatTokensShort(u.TotalTokens()),
			u.Turns, u.Sessions))
		totInput += u.InputTokens
		totOutput += u.OutputTokens
		totTurns += u.Turns
		totSess += u.Sessions
	}

	// Totals row
	b.WriteString(fmt.Sprintf("    %s  %s  %s  %s  %s  %s\n",
		strings.Repeat("─", maxName),
		strings.Repeat("─", 8),
		strings.Repeat("─", 8),
		strings.Repeat("─", 8),
		strings.Repeat("─", 5),
		strings.Repeat("─", 5)))
	b.WriteString(fmt.Sprintf("    %-*s  %8s  %8s  %8s  %5d  %5d\n",
		maxName, "TOTAL",
		FormatTokensShort(totInput),
		FormatTokensShort(totOutput),
		FormatTokensShort(totInput+totOutput),
		totTurns, totSess))
}

// FormatTokensShort formats token counts with k/M suffixes for table display.
// e.g. 1_409_000 → "1.4M", 142_381 → "142k", 42_381 → "42.4k", 890 → "890".
func FormatTokensShort(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 100_000 {
		return fmt.Sprintf("%dk", n/1_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}
