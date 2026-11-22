package cronedit

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Simple schedule kinds: the picker's segments other than Custom.
const (
	KindHourly   = "hourly"
	KindDaily    = "daily"
	KindWeekdays = "weekdays"
	KindDays     = "days"
)

// SimpleSchedule is the schedule picker's model. Days are weekday numbers,
// 0 = Sunday through 6 = Saturday (cron's numbering), used by KindDays only;
// Hour is ignored for KindHourly.
type SimpleSchedule struct {
	Kind   string `json:"kind"`
	Minute int    `json:"minute"`
	Hour   int    `json:"hour"`
	Days   []int  `json:"days,omitempty"`
}

// Spec renders the cron expression. It emits only `M * * * *` (hourly) and
// `M H * * D` shapes, D being `*` (daily), `1-5` (weekdays) or an ascending
// comma list (days).
func (s SimpleSchedule) Spec() (string, error) {
	if s.Minute < 0 || s.Minute > 59 {
		return "", fmt.Errorf("minute %d: want 0-59", s.Minute)
	}
	if s.Kind == KindHourly {
		return fmt.Sprintf("%d * * * *", s.Minute), nil
	}
	if s.Hour < 0 || s.Hour > 23 {
		return "", fmt.Errorf("hour %d: want 0-23", s.Hour)
	}
	var dow string
	switch s.Kind {
	case KindDaily:
		dow = "*"
	case KindWeekdays:
		dow = "1-5"
	case KindDays:
		if len(s.Days) == 0 {
			return "", fmt.Errorf("pick at least one day")
		}
		days := slices.Clone(s.Days)
		slices.Sort(days)
		days = slices.Compact(days)
		parts := make([]string, len(days))
		for i, d := range days {
			if d < 0 || d > 6 {
				return "", fmt.Errorf("day %d: want 0 (Sunday) to 6 (Saturday)", d)
			}
			parts[i] = strconv.Itoa(d)
		}
		dow = strings.Join(parts, ",")
	default:
		return "", fmt.Errorf("schedule kind %q: want hourly, daily, weekdays or days", s.Kind)
	}
	return fmt.Sprintf("%d %d * * %s", s.Minute, s.Hour, dow), nil
}

// ParseSimple maps a cron expression onto the picker. It returns ok=false
// for anything Spec would not emit exactly (names, ranges other than 1-5,
// steps, leading zeros, @descriptors, unsorted day lists, ...), so the UI
// shows such a schedule in Custom mode and never rewrites an expression the
// user did not touch: for ok=true, Spec() reproduces spec's fields.
func ParseSimple(spec string) (SimpleSchedule, bool) {
	f := strings.Fields(spec)
	if len(f) != 5 || f[2] != "*" || f[3] != "*" {
		return SimpleSchedule{}, false
	}
	minute, ok := canonicalInt(f[0], 59)
	if !ok {
		return SimpleSchedule{}, false
	}
	var s SimpleSchedule
	switch {
	case f[1] == "*" && f[4] == "*":
		s = SimpleSchedule{Kind: KindHourly, Minute: minute}
	case f[1] == "*":
		return SimpleSchedule{}, false
	default:
		hour, ok := canonicalInt(f[1], 23)
		if !ok {
			return SimpleSchedule{}, false
		}
		s = SimpleSchedule{Minute: minute, Hour: hour}
		switch f[4] {
		case "*":
			s.Kind = KindDaily
		case "1-5":
			s.Kind = KindWeekdays
		default:
			s.Kind = KindDays
			for _, p := range strings.Split(f[4], ",") {
				d, ok := canonicalInt(p, 6)
				if !ok {
					return SimpleSchedule{}, false
				}
				s.Days = append(s.Days, d)
			}
		}
	}
	out, err := s.Spec()
	if err != nil || out != strings.Join(f, " ") {
		return SimpleSchedule{}, false
	}
	return s, true
}

// canonicalInt parses a plain decimal 0..max with no sign or leading zero.
func canonicalInt(s string, max int) (int, bool) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > max || strconv.Itoa(n) != s {
		return 0, false
	}
	return n, true
}
