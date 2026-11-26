package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// NextFireFormat is the layout `cron add` and the approval preview use for a
// fire time, e.g. "Tue 18 Nov 07:30".
const NextFireFormat = "Mon 2 Jan 15:04"

// Describe renders the common schedule shapes in words: "weekdays 07:30",
// "daily 09:00", "Mondays 08:15", "hourly at :05". It returns "" for anything
// it does not recognize (the expression itself is then the only
// description); it never guesses.
func Describe(spec string) string {
	s := strings.TrimSpace(spec)
	switch s {
	case "@hourly":
		return "hourly at :00"
	case "@daily", "@midnight":
		return "daily 00:00"
	case "@weekly":
		return "Sundays 00:00"
	case "@monthly":
		return "monthly on the 1st, 00:00"
	case "@yearly", "@annually":
		return "yearly on 1 Jan, 00:00"
	}
	f := strings.Fields(s)
	if len(f) != 5 || f[2] != "*" || f[3] != "*" {
		return ""
	}
	minute, ok := clockField(f[0], 59)
	if !ok {
		return ""
	}
	if f[1] == "*" {
		if f[4] != "*" {
			return ""
		}
		return fmt.Sprintf("hourly at :%02d", minute)
	}
	hour, ok := clockField(f[1], 23)
	if !ok {
		return ""
	}
	days := describeDays(f[4])
	if days == "" {
		return ""
	}
	return fmt.Sprintf("%s %02d:%02d", days, hour, minute)
}

// clockField parses a single numeric cron field no larger than max.
func clockField(s string, max int) (int, bool) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > max {
		return 0, false
	}
	return n, true
}

var dayNames = map[string]time.Weekday{
	"0": time.Sunday, "1": time.Monday, "2": time.Tuesday, "3": time.Wednesday,
	"4": time.Thursday, "5": time.Friday, "6": time.Saturday, "7": time.Sunday,
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

// describeDays renders a day-of-week field: "daily", "weekdays", "weekends",
// "Mondays" or "Mon, Wed, Fri"; "" when it is not one of those shapes.
func describeDays(field string) string {
	switch strings.ToLower(field) {
	case "*":
		return "daily"
	case "1-5", "mon-fri":
		return "weekdays"
	case "0,6", "6,0", "6,7", "sat,sun", "sun,sat":
		return "weekends"
	}
	parts := strings.Split(strings.ToLower(field), ",")
	days := make([]string, 0, len(parts))
	for _, p := range parts {
		d, ok := dayNames[p]
		if !ok {
			return ""
		}
		days = append(days, d.String()[:3])
	}
	if len(days) == 1 {
		return dayNames[parts[0]].String() + "s"
	}
	return strings.Join(days, ", ")
}

// Summary is the one-line schedule description shown before a job is
// approved: the expression, its words when Describe knows them, and the next
// fire including the job's jitter, e.g.
// "30 7 * * 1-5 (weekdays 07:30), next: Tue 18 Nov 07:31". An invalid
// expression yields the parse error instead of a next fire.
func Summary(spec, name string, now time.Time) (string, error) {
	sched, err := ParseSchedule(spec)
	if err != nil {
		return "", err
	}
	out := strings.TrimSpace(spec)
	if words := Describe(spec); words != "" {
		out += " (" + words + ")"
	}
	if next := NextFire(sched, now, Jitter(name)); !next.IsZero() {
		out += ", next: " + next.Format(NextFireFormat)
	}
	return out, nil
}

// OnceSummary describes a one-shot job: "once, Tue 1 Dec 09:00" while
// pending, "done, fired Tue 1 Dec 09:00" (or "done, failed ...") once its
// launch was attempted, "expired, missed Tue 1 Dec 09:00" when the time
// passed without a launch. Times are local; an invalid at is reported as
// such rather than guessed.
func OnceSummary(j Job) string {
	at, err := j.AtTime()
	if err != nil {
		return "once, invalid time " + strings.TrimSpace(j.At)
	}
	when := at.In(time.Local).Format(NextFireFormat)
	switch j.State {
	case StateCompleted:
		fired := when
		if !j.CompletedAt.IsZero() {
			fired = j.CompletedAt.In(time.Local).Format(NextFireFormat)
		}
		if IsFailed(j.Result) {
			return "done, failed " + fired
		}
		return "done, fired " + fired
	case StateExpired:
		return "expired, missed " + when
	}
	return "once, " + when
}

// JobSummary is Summary for a job: the schedule summary of a recurring
// job, OnceSummary of a one-shot.
func JobSummary(j Job, now time.Time) (string, error) {
	if j.IsOnce() {
		if _, err := j.AtTime(); err != nil {
			return "", err
		}
		return OnceSummary(j), nil
	}
	return Summary(j.Schedule, j.Name, now)
}

// Words is the job's schedule in words: Describe of a recurring schedule
// ("" when it has none), OnceSummary of a one-shot.
func Words(j Job) string {
	if j.IsOnce() {
		return OnceSummary(j)
	}
	return Describe(j.Schedule)
}

// ScheduleText is what a one-line view prints for the schedule: the
// expression of a recurring job, OnceSummary of a one-shot.
func ScheduleText(j Job) string {
	if j.IsOnce() {
		return OnceSummary(j)
	}
	return j.Schedule
}

// NextOnce is a pending one-shot's fire time (jitter included); zero when
// it is done or its at does not parse.
func NextOnce(j Job) time.Time {
	if j.IsDone() {
		return time.Time{}
	}
	at, err := j.AtTime()
	if err != nil {
		return time.Time{}
	}
	return at.Add(Jitter(j.Name))
}
