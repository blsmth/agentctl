package race

import (
	"testing"
	"time"
)

func TestCalendarFor(t *testing.T) {
	h := time.Hour
	m := time.Minute
	def := DefaultSchedule()
	tests := []struct {
		name    string
		effort  time.Duration
		s       Schedule
		elapsed time.Duration
		nights  int
		weekend int
		weekEnd int
		breaks  time.Duration
		finish  string
	}{
		{"zero", 0, def, 0, 0, 0, 0, 0, "Mon 09:00"},
		{"under a block", h, def, h, 0, 0, 0, 0, "Mon 10:00"},
		{"across coffee", 2 * h, def, 2*h + 15*m, 0, 0, 0, 15 * m, "Mon 11:15"},
		{"exactly one day of focus", 6 * h, def, 7*h + 30*m, 0, 0, 0, 0, "Mon 16:30"},
		{"one minute into day two", 6*h + m, def, 24*h + m, 1, 0, 0, 90 * m, "Tue 09:01"},
		{"exactly a work week", 30 * h, def, 4*24*h + 7*h + 30*m, 4, 0, 0, 0, "Fri 16:30"},
		{"spans a weekend", 30*h + m, def, 7*24*h + m, 7, 2, 1, 5 * 90 * m, "Mon 09:01"},
		{"multi-week", 60 * h, def, 11*24*h + 7*h + 30*m, 11, 2, 1, 10 * 90 * m, "Fri 16:30"},
		{"seven days a week", 12*h + m, Schedule{6, 7}, 2*24*h + m, 2, 0, 0, 3 * h, "Wed 09:01"},
		{"12 focus hours, one day", 12 * h, Schedule{12, 5}, 13*h + 30*m, 0, 0, 0, 0, "Mon 22:30"},
		{"12 focus hours, next day", 13 * h, Schedule{12, 5}, 25 * h, 1, 0, 0, 90 * m, "Tue 10:00"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := CalendarFor(tt.effort, tt.s)
			if c.Elapsed != tt.elapsed || c.Nights != tt.nights || c.WeekendDays != tt.weekend || c.Weekends != tt.weekEnd || c.Finish != tt.finish {
				t.Errorf("got %+v, want elapsed %v nights %d weekend days %d weekends %d finish %s", c, tt.elapsed, tt.nights, tt.weekend, tt.weekEnd, tt.finish)
			}
			if tt.breaks != 0 && c.Breaks != tt.breaks {
				t.Errorf("breaks = %v, want %v", c.Breaks, tt.breaks)
			}
		})
	}
}

func TestCalendarForMonotonicAndFast(t *testing.T) {
	var prev time.Duration
	for e := time.Duration(0); e < 200*time.Hour; e += 7 * time.Minute {
		c := CalendarFor(e, DefaultSchedule())
		if c.Elapsed < prev {
			t.Fatalf("elapsed went backwards at effort %v: %v < %v", e, c.Elapsed, prev)
		}
		if c.Elapsed < e {
			t.Fatalf("elapsed %v shorter than effort %v", c.Elapsed, e)
		}
		prev = c.Elapsed
	}
	c := CalendarFor(100000*time.Hour, DefaultSchedule())
	if c.Nights < 1000 {
		t.Errorf("huge effort: %+v", c)
	}
}

func TestFormatCalendar(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "30s"},
		{45 * time.Minute, "45m"},
		{4*time.Hour + 30*time.Minute, "4h 30m"},
		{4 * time.Hour, "4h"},
		{24 * time.Hour, "1 day"},
		{51 * time.Hour, "2 days 3h"},
		{48 * time.Hour, "2 days"},
		{7 * 24 * time.Hour, "1.0 week"},
		{17 * 24 * time.Hour, "2.4 weeks"},
	}
	for _, tt := range tests {
		if got := FormatCalendar(tt.d); got != tt.want {
			t.Errorf("FormatCalendar(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestSummarizeRealLife(t *testing.T) {
	r := &Race{Params: DefaultParams(), AgentTotal: time.Hour, HumanTotal: 7 * time.Hour}
	if s := r.Summarize(""); s.RealLife != nil {
		t.Error("real_life present without the flag")
	}
	r.Params.RealLife = true
	s := r.Summarize("")
	if s.RealLife == nil || s.RealLife.Nights != 1 || s.RealLife.Finish != "Tue 10:00" {
		t.Fatalf("real_life = %+v", s.RealLife)
	}
	if want := "6 focus hours a day, 5 days a week, starts Mon 09:00"; !contains(r.HumanAssumptions(), want) {
		t.Errorf("assumptions lack %q: %s", want, r.HumanAssumptions())
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
