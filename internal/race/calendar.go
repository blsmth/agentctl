package race

import (
	"fmt"
	"time"
)

// Real-life calendar model: converts nonstop human effort into elapsed
// calendar time. Pure and deterministic; nothing here reads the clock.
//
// The schedule starts Monday 09:00. Each working day is a fixed template that
// fits FocusHours of work into four equal blocks around two 15-minute coffee
// breaks and a 1-hour lunch:
//
//	work · coffee 15m · work · lunch 1h · work · coffee 15m · work
//
// so the default 6 focus hours run 09:00-10:30, 10:45-12:15, 13:15-14:45 and
// 15:00-16:30. After the last block the person stops for the evening and
// sleeps; the next day starts at 09:00 again (a 24h cycle, stretched to day
// length plus 8h of sleep when FocusHours is large enough that they would
// overlap). Only the first DaysPerWeek days of every 7 are working days; the
// rest are weekend days, spent entirely off. A larger FocusHours just makes
// each block, and so the day, longer: the breaks stay the same size and never
// overlap.
const (
	lunchBreak   = time.Hour
	coffeeBreak  = 15 * time.Minute
	sleepTime    = 8 * time.Hour
	dayCycle     = 24 * time.Hour
	dayStart     = 9 * time.Hour // 09:00
	blocksPerDay = 4
)

// Schedule is the working pattern of the simulated person.
type Schedule struct {
	FocusHours  float64 // hours of real keyboard work per working day
	DaysPerWeek int     // working days per 7-day week, starting Monday
}

// DefaultSchedule is 6 focus hours, 5 days a week.
func DefaultSchedule() Schedule { return Schedule{FocusHours: 6, DaysPerWeek: 5} }

func (s Schedule) focus() time.Duration {
	return time.Duration(s.FocusHours * float64(time.Hour))
}

func (s Schedule) days() int {
	return max(1, min(7, s.DaysPerWeek))
}

// Calendar is the result of laying effort onto a schedule.
type Calendar struct {
	Elapsed     time.Duration // calendar time from Monday 09:00 to the finish
	Nights      int           // nights slept in between
	WeekendDays int           // whole non-working days in between
	Weekends    int           // weekends passed
	Breaks      time.Duration // lunch and coffee time taken
	Finish      string        // e.g. "Wed 11:40"
}

// CalendarFor returns the calendar time a person on schedule s needs to do
// effort of nonstop work. Whole days are computed arithmetically, so huge
// efforts are as fast as small ones.
func CalendarFor(effort time.Duration, s Schedule) Calendar {
	focus := s.focus()
	if effort <= 0 || focus <= 0 {
		return Calendar{Finish: finishLabel(0)}
	}
	// Whole working days finished before the day the effort ends on. Effort
	// ending exactly at the end of a day finishes that day: no night added.
	full := int64((effort - 1) / focus)
	rem := effort - time.Duration(full)*focus // in (0, focus]

	off, breaks := withinDay(rem, focus)

	perDay := breaksPerDay()
	dayLen := focus + perDay
	cycle := max(dayCycle, dayLen+sleepTime)

	week, idx := full/int64(s.days()), full%int64(s.days())
	calDay := week*7 + idx

	c := Calendar{
		Elapsed:     time.Duration(calDay)*cycle + off,
		Nights:      int(calDay),
		WeekendDays: int(calDay - full),
		Breaks:      time.Duration(full)*perDay + breaks,
	}
	if s.days() < 7 {
		c.Weekends = int(week)
	}
	c.Finish = finishLabel(c.Elapsed)
	return c
}

func breaksPerDay() time.Duration { return 2*coffeeBreak + lunchBreak }

// blockBreaks are the breaks after blocks 1..3 of the day.
var blockBreaks = [blocksPerDay - 1]time.Duration{coffeeBreak, lunchBreak, coffeeBreak}

// withinDay places rem (0 < rem <= focus) of work on the day template and
// returns the offset from the day's start and the break time passed. Effort
// that ends exactly at the end of a block stops there, before the break.
func withinDay(rem, focus time.Duration) (off, breaks time.Duration) {
	block := focus / blocksPerDay
	for i := 0; i < blocksPerDay; i++ {
		size := block
		if i == blocksPerDay-1 {
			size = focus - 3*block
		}
		if rem <= size {
			return off + rem, breaks
		}
		rem -= size
		off += size
		if i < len(blockBreaks) {
			off += blockBreaks[i]
			breaks += blockBreaks[i]
		}
	}
	return off, breaks
}

// finishLabel is the weekday and clock time at elapsed after Monday 09:00.
func finishLabel(elapsed time.Duration) string {
	abs := dayStart + elapsed
	days := int(abs / dayCycle)
	clock := abs % dayCycle
	wd := time.Weekday((int(time.Monday) + days) % 7)
	return fmt.Sprintf("%s %02d:%02d", wd.String()[:3], int(clock/time.Hour), int(clock%time.Hour/time.Minute))
}

// FormatCalendar formats elapsed calendar time: "45m", "4h 30m", "2 days 3h",
// "2.4 weeks". Unlike Friendly it never counts 8-hour workdays.
func FormatCalendar(d time.Duration) string {
	const day = 24 * time.Hour
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Round(time.Second)/time.Second))
	}
	d = d.Round(time.Minute)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < day:
		return hoursMinutes(d)
	case d < 7*day:
		days := int(d / day)
		out := fmt.Sprintf("%d day", days)
		if days != 1 {
			out += "s"
		}
		if h := int(d % day / time.Hour); h > 0 {
			out += fmt.Sprintf(" %dh", h)
		}
		return out
	}
	return plural(float64(d)/float64(7*day), "week")
}

func hoursMinutes(d time.Duration) string {
	h, m := int(d/time.Hour), int(d%time.Hour/time.Minute)
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh %dm", h, m)
}
