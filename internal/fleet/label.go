package fleet

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/closedlog"
	"github.com/blsmth/agentctl/internal/state"
)

// labelPromptWidth is how much of a first prompt a label keeps.
const labelPromptWidth = 40

// Scope tells Label which parts of a label the surrounding view already
// shows, so it can drop them. The zero value is an unscoped view.
type Scope struct {
	// Job is set in a view of one cron job's runs (the schedule card's Last
	// runs): a run of that job has no label, since its line already shows
	// the fire time.
	Job string
}

var reviewPRBranch = regexp.MustCompile(`review-pr-(\d+)`)

// Label is the human-readable name of a session, built from where it came
// from. The first matching rule wins:
//
//   - a title a human set (hub rename);
//   - a ticket dispatch: "#N · <title>";
//   - a PR review: "review #N";
//   - a cron run: "<job> · Tue 07:01" (empty in that job's own scope);
//   - anything else: the short title, the task line, the first prompt cut at
//     a word boundary, else "<agent> · started HH:MM".
//
// It is pure; callers truncate the result to their width with TruncateWords.
func Label(r Row, scope Scope) string {
	title := strings.TrimSpace(r.ShortTitle)
	if r.ShortManual && title != "" {
		return title
	}
	if n := rowTicket(r); n > 0 {
		t := firstNonBlank(title, r.TicketTitle)
		if t == "" {
			return "#" + strconv.Itoa(n)
		}
		return fmt.Sprintf("#%d · %s", n, oneLine(t))
	}
	if r.Mode == "review-pr" {
		if n := reviewNumber(r); n != "" {
			return "review #" + n
		}
		return "review"
	}
	if r.ScheduledBy != "" {
		if scope.Job == r.ScheduledBy {
			return ""
		}
		if r.ScheduledAt.IsZero() {
			return r.ScheduledBy
		}
		return r.ScheduledBy + " · " + r.ScheduledAt.Local().Format("Mon 15:04")
	}
	if title != "" {
		return oneLine(title)
	}
	if t := firstNonBlank(r.Task, r.FirstPrompt); t != "" {
		return TruncateWords(oneLine(t), labelPromptWidth)
	}
	agent := RowDisplayName(r)
	if r.AgentName != "" {
		agent = r.AgentName
	}
	if agent == r.WindowID { // never a raw window ID
		agent = "session"
	}
	if r.StartTime > 0 {
		return agent + " · started " + time.Unix(r.StartTime, 0).Format("15:04")
	}
	return agent
}

// RecordRow is the Row view of a closed-window record, enough for Label.
func RecordRow(rec closedlog.Record) Row {
	ticket, _ := strconv.Atoi(strings.TrimPrefix(rec.Ticket, "#"))
	return Row{
		Session: rec.Session,
		Project: rec.Project(),
		AgentState: state.AgentState{
			Name:        rec.Instance,
			AgentName:   rec.Agent,
			SessionID:   rec.SessionID,
			WindowID:    rec.WindowID,
			Mode:        rec.Mode,
			Ticket:      ticket,
			TicketRef:   rec.Ticket,
			TicketTitle: rec.TicketTitle,
			FirstPrompt: rec.FirstPrompt,
			ShortTitle:  rec.ShortTitle,
			Branch:      rec.Branch,
			WorktreeDir: rec.Worktree,
			ScheduledBy: rec.ScheduledBy,
		},
	}
}

func rowTicket(r Row) int {
	if r.Ticket > 0 {
		return r.Ticket
	}
	n, _ := strconv.Atoi(strings.TrimPrefix(r.TicketRef, "#"))
	return max(n, 0)
}

// reviewNumber is the PR number of a review window, which is only recorded
// in its branch name (agent/<agent>/review-pr-<N>-<id>).
func reviewNumber(r Row) string {
	for _, s := range []string{r.Branch, r.WorktreeDir, r.WorktreePath} {
		if m := reviewPRBranch.FindStringSubmatch(s); m != nil {
			return m[1]
		}
	}
	return ""
}

func firstNonBlank(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// oneLine collapses all whitespace runs, newlines included, to one space.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// UniqueLabels disambiguates one rendered list: the second and later
// occurrence of a label get " ·2", " ·3". Empty labels are left alone. The
// input is not modified and nothing is remembered between calls, so a label
// is only ever suffixed in the view that rendered the clash.
func UniqueLabels(labels []string) []string {
	out := make([]string, len(labels))
	seen := make(map[string]int, len(labels))
	for i, l := range labels {
		if l == "" {
			continue
		}
		seen[l]++
		if n := seen[l]; n > 1 {
			l = fmt.Sprintf("%s ·%d", l, n)
		}
		out[i] = l
	}
	return out
}
