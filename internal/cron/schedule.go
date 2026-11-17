package cron

import (
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	rc "github.com/robfig/cron/v3"
)

// parser is robfig/cron's standard 5-field parser plus @descriptors.
var parser = rc.NewParser(rc.Minute | rc.Hour | rc.Dom | rc.Month | rc.Dow | rc.Descriptor)

// ParseSchedule parses a 5-field cron expression or one of @hourly, @daily,
// @weekly, @monthly, @yearly (and their aliases), in local time. @every and
// TZ prefixes are rejected: schedules are wall-clock, local time.
func ParseSchedule(spec string) (rc.Schedule, error) {
	return parseScheduleIn(spec, time.Local)
}

// parseScheduleIn is ParseSchedule in an explicit location (tests).
func parseScheduleIn(spec string, loc *time.Location) (rc.Schedule, error) {
	s := strings.TrimSpace(spec)
	if s == "" {
		return nil, fmt.Errorf("schedule is required")
	}
	if strings.HasPrefix(s, "TZ=") || strings.HasPrefix(s, "CRON_TZ=") {
		return nil, fmt.Errorf("schedule %q: time zone prefixes are not supported; schedules use local time", spec)
	}
	if strings.HasPrefix(s, "@every") {
		return nil, fmt.Errorf("schedule %q: @every is not supported; use a cron expression", spec)
	}
	sched, err := parser.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("invalid schedule %q: %w", spec, err)
	}
	ss, ok := sched.(*rc.SpecSchedule)
	if !ok {
		return nil, fmt.Errorf("schedule %q: unsupported form", spec)
	}
	ss.Location = loc
	return ss, nil
}

// minIntervalSamples is how many consecutive fires CheckMinInterval measures.
const minIntervalSamples = 50

// CheckMinInterval rejects a schedule whose fires, measured over the next
// several from start, come closer together than min.
func CheckMinInterval(sched rc.Schedule, start time.Time, min time.Duration) error {
	prev := sched.Next(start)
	if prev.IsZero() {
		return fmt.Errorf("schedule never fires")
	}
	for i := 0; i < minIntervalSamples; i++ {
		next := sched.Next(prev)
		if next.IsZero() {
			return nil
		}
		if gap := next.Sub(prev); gap < min {
			return fmt.Errorf("schedule fires every %s (at %s), more often than cron.min_interval %s",
				gap, prev.Format("Mon 15:04"), min)
		}
		prev = next
	}
	return nil
}

// MaxJitter bounds the per-job start delay.
const MaxJitter = 90 * time.Second

// Jitter is a deterministic 0-90s delay for a job, from a hash of its name,
// so jobs on the same schedule do not all start in the same second.
func Jitter(name string) time.Duration {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	return time.Duration(h.Sum32()%uint32(MaxJitter/time.Second+1)) * time.Second
}

// maxDueScan caps how many missed fires DueFires walks (a year of a
// 15-minute schedule is 35k); beyond it the oldest are not counted.
const maxDueScan = 100000

// Due is the result of DueFires: the newest scheduled time that is due, and
// how many older due fires it supersedes.
type Due struct {
	Latest time.Time
	Missed int
}

// DueFires returns the fires scheduled after since that are due at now. A
// fire at t is due once now >= t + jitter. since is the last scheduled time
// already handled; a zero since yields nothing (the caller seeds it).
//
// A fixed-hour schedule (not every hour) fires once in the repeated hour of a
// DST fall-back, like vixie cron: robfig/cron yields 01:30 EDT and 01:30 EST,
// and the second is not a new fire. A spring-forward skips a fire whose
// wall-clock time does not exist that day (robfig/cron behavior).
func DueFires(sched rc.Schedule, since, now time.Time, jitter time.Duration) (Due, bool) {
	if since.IsZero() {
		return Due{}, false
	}
	var d Due
	found := false
	t := since
	for i := 0; i < maxDueScan; i++ {
		next := sched.Next(t)
		if next.IsZero() || now.Before(next.Add(jitter)) {
			break
		}
		prev := t
		t = next
		if isFallBackRepeat(sched, prev, next) {
			continue
		}
		if found {
			d.Missed++
		}
		d.Latest = next
		found = true
	}
	return d, found
}

// isFallBackRepeat reports whether next repeats since's local wall-clock
// time within the doubled hour of a DST fall-back transition, for a schedule
// that does not fire every hour.
func isFallBackRepeat(sched rc.Schedule, since, next time.Time) bool {
	if ss, ok := sched.(*rc.SpecSchedule); !ok || ss.Hour&allHours == allHours {
		return false
	}
	gap := next.Sub(since)
	if gap <= 0 || gap > 2*time.Hour {
		return false
	}
	a, b := since.In(next.Location()), next
	return a.Hour() == b.Hour() && a.Minute() == b.Minute() && a.YearDay() == b.YearDay()
}

// NextFire returns the next fire after now including jitter, or zero.
func NextFire(sched rc.Schedule, now time.Time, jitter time.Duration) time.Time {
	t := sched.Next(now.Add(-jitter))
	if t.IsZero() {
		return t
	}
	return t.Add(jitter)
}

// allHours is a SpecSchedule.Hour mask with every hour 0-23 set.
const allHours = 1<<24 - 1
