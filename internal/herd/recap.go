package herd

import (
	"fmt"
	"strings"
	"time"
)

// RecapSource says which signal a RecapLine came from, strongest first.
type RecapSource int

const (
	RecapNone     RecapSource = iota // nothing to show
	RecapSummary                     // Claude Code's away_summary recap
	RecapTask                        // the dispatched ticket or ad-hoc task line
	RecapTitle                       // Claude Code's ai-title
	RecapActivity                    // last tool detail / assistant text
)

// RecapLine is the one-line "what is this agent working on" for a row.
type RecapLine struct {
	Text   string
	Source RecapSource
}

// Recap picks the row's recap line, first non-empty wins: the newest
// away_summary from the log, then the task (ticket title, prefixed "#N ", or
// the ad-hoc task line recorded at dispatch), then the log's AI title, then
// ActivityLine as the weakest signal.
func Recap(r Row, info LogInfo) RecapLine {
	if info.Recap != "" {
		return RecapLine{Text: info.Recap, Source: RecapSummary}
	}
	if t := TaskText(r); t != "" {
		return RecapLine{Text: t, Source: RecapTask}
	}
	if info.AITitle != "" {
		return RecapLine{Text: info.AITitle, Source: RecapTitle}
	}
	if a := ActivityLine(r); a != "" {
		return RecapLine{Text: a, Source: RecapActivity}
	}
	return RecapLine{}
}

// TaskText is the row's task: "#N title" for a ticket dispatch, else the
// ad-hoc task line, else "".
func TaskText(r Row) string {
	if title := flattenWhitespace(r.TicketTitle); title != "" {
		if n := strings.TrimPrefix(strings.TrimSpace(r.TicketRef), "#"); n != "" {
			return "#" + n + " " + title
		}
		if r.Ticket > 0 {
			return fmt.Sprintf("#%d %s", r.Ticket, title)
		}
		return title
	}
	return flattenWhitespace(r.Task)
}

// TagMeta sets the windowstate-derived row fields in place from per-window
// launch metadata: Dispatched, Parent and StateSince. A window with no record
// in meta counts as bare with no parent and an unknown state age.
func TagMeta(rows []Row, meta map[string]Meta) {
	for i := range rows {
		m := meta[rows[i].WindowID]
		rows[i].Dispatched = m.Dispatched()
		rows[i].Parent = m.Parent()
		rows[i].StateSince = m.stateSince(rows[i].State)
	}
}

// stateSince is when the window entered state: BlockedSince for blocked
// (falling back to StateSince), StateSince otherwise. Zero when unknown.
func (m Meta) stateSince(state string) time.Time {
	if state == "blocked" && !m.BlockedSince.IsZero() {
		return m.BlockedSince
	}
	return m.StateSince
}

// TagLogs fills each row's Recap and PR link from its session log (see
// ReadLogInfo; cheap on an unchanged log).
func TagLogs(rows []Row) {
	for i := range rows {
		info := ReadLogInfo(rows[i].LogPath)
		rows[i].Recap = Recap(rows[i], info)
		rows[i].PRNumber, rows[i].PRURL = info.PRNumber, info.PRURL
	}
}

// BaseName is the row's agent name without its instance suffix: AgentName
// when recorded, else Name with a trailing ":N" or "~N" removed.
func BaseName(r Row) string {
	if r.AgentName != "" {
		return r.AgentName
	}
	return StripInstanceSuffix(r.Name)
}

// StripInstanceSuffix removes a trailing ":N" or "~N" (N all digits) from an
// instance name. Other names are returned unchanged.
func StripInstanceSuffix(name string) string {
	i := strings.LastIndexAny(name, ":~")
	if i <= 0 || i == len(name)-1 {
		return name
	}
	for _, c := range name[i+1:] {
		if c < '0' || c > '9' {
			return name
		}
	}
	return name[:i]
}

// CompactAge formats d as a short age: "45s", "4m", "2h", "3d". Negative
// durations count as zero.
func CompactAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		if d < 0 {
			d = 0
		}
		return fmt.Sprintf("%ds", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	}
}
