package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/sessions"
)

// RunUsage prints a token usage report covering today and this week.
func RunUsage() {
	done := logging.Timing("cmdUsage")
	defer done()

	now := time.Now()
	loc := now.Location()

	// Start of today and start of this week (Monday).
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	weekday := now.Weekday()
	if weekday == time.Sunday {
		weekday = 7
	}
	weekStart := todayStart.AddDate(0, 0, -int(weekday-time.Monday))

	byAgent, err := sessions.FindSessionsByAgent()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error finding sessions: %v\n", err)
		os.Exit(1)
	}

	todayUsage := sessions.AggregateUsage(byAgent, todayStart, now)
	weekUsage := sessions.AggregateUsage(byAgent, weekStart, now)

	var b strings.Builder

	b.WriteString(fmt.Sprintf("  Usage Report — %s\n\n", now.Format("Mon Jan 2, 2006")))

	b.WriteString("  ── Today ──\n")
	sessions.WriteUsageTable(&b, todayUsage)
	b.WriteString("\n")

	b.WriteString(fmt.Sprintf("  ── This Week (since %s) ──\n", weekStart.Format("Mon Jan 2")))
	sessions.WriteUsageTable(&b, weekUsage)

	fmt.Print(b.String())
}
