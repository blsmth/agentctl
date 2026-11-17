package cron

import (
	"strings"
	"testing"
	"time"
)

func TestParseSchedule(t *testing.T) {
	tests := []struct {
		spec    string
		wantErr string
	}{
		{spec: "30 7 * * 1-5"},
		{spec: "*/15 * * * *"},
		{spec: "@hourly"},
		{spec: "@daily"},
		{spec: "@weekly"},
		{spec: "", wantErr: "required"},
		{spec: "0 30 7 * * 1-5", wantErr: "invalid schedule"},
		{spec: "61 * * * *", wantErr: "invalid schedule"},
		{spec: "@every 5m", wantErr: "@every"},
		{spec: "CRON_TZ=UTC 0 * * * *", wantErr: "time zone"},
	}
	for _, tt := range tests {
		t.Run(tt.spec, func(t *testing.T) {
			_, err := ParseSchedule(tt.spec)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestCheckMinInterval(t *testing.T) {
	start := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		spec    string
		min     time.Duration
		wantErr bool
	}{
		{"* * * * *", 15 * time.Minute, true},
		{"*/15 * * * *", 15 * time.Minute, false},
		{"*/10 * * * *", 15 * time.Minute, true},
		// Only some gaps are short: caught by sampling several fires.
		{"0,5 * * * *", 15 * time.Minute, true},
		{"@hourly", time.Hour, false},
		{"30 7 * * 1-5", 15 * time.Minute, false},
	}
	for _, tt := range tests {
		t.Run(tt.spec, func(t *testing.T) {
			s, err := parseScheduleIn(tt.spec, time.UTC)
			if err != nil {
				t.Fatal(err)
			}
			err = CheckMinInterval(s, start, tt.min)
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestJitter(t *testing.T) {
	for _, name := range []string{"a", "issue-triage", "nightly-deps", "x_y-z"} {
		j := Jitter(name)
		if j < 0 || j > MaxJitter {
			t.Errorf("Jitter(%q) = %v out of range", name, j)
		}
		if j != Jitter(name) {
			t.Errorf("Jitter(%q) not deterministic", name)
		}
		if j%time.Second != 0 {
			t.Errorf("Jitter(%q) = %v not whole seconds", name, j)
		}
	}
	if Jitter("issue-triage") == Jitter("nightly-deps") && Jitter("a") == Jitter("x_y-z") {
		t.Error("jitter does not vary by name")
	}
}

func TestDueFires(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("no tzdata: %v", err)
	}
	at := func(y int, mo time.Month, d, h, mi int) time.Time { return time.Date(y, mo, d, h, mi, 0, 0, ny) }
	tests := []struct {
		name       string
		spec       string
		since, now time.Time
		jitter     time.Duration
		wantOK     bool
		wantLatest time.Time
		wantMissed int
	}{
		{
			name: "no state yields nothing", spec: "*/15 * * * *",
			now: at(2026, 11, 17, 9, 0),
		},
		{
			name: "not yet due", spec: "30 7 * * *",
			since: at(2026, 11, 17, 7, 30), now: at(2026, 11, 18, 7, 29),
		},
		{
			name: "due on time", spec: "30 7 * * *",
			since: at(2026, 11, 17, 7, 30), now: at(2026, 11, 18, 7, 31),
			wantOK: true, wantLatest: at(2026, 11, 18, 7, 30),
		},
		{
			name: "jitter delays the fire", spec: "30 7 * * *",
			since: at(2026, 11, 17, 7, 30), now: at(2026, 11, 18, 7, 31), jitter: 90 * time.Second,
		},
		{
			name: "jitter elapsed", spec: "30 7 * * *",
			since: at(2026, 11, 17, 7, 30), now: at(2026, 11, 18, 7, 32), jitter: 90 * time.Second,
			wantOK: true, wantLatest: at(2026, 11, 18, 7, 30),
		},
		{
			name: "sleep gap coalesces to the latest", spec: "0 * * * *",
			since: at(2026, 11, 17, 0, 0), now: at(2026, 11, 17, 10, 5),
			wantOK: true, wantLatest: at(2026, 11, 17, 10, 0), wantMissed: 9,
		},
		{
			name: "DST fall back fires a fixed-hour job once", spec: "30 1 * * *",
			since: at(2026, 10, 31, 1, 30), now: time.Date(2026, 11, 1, 6, 40, 0, 0, time.UTC), // 01:40 EST
			wantOK: true, wantLatest: time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC), // 01:30 EDT
		},
		{
			name: "DST fall back: repeated hour after firing is not a new fire", spec: "30 1 * * *",
			since: time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC).In(ny), now: time.Date(2026, 11, 1, 6, 40, 0, 0, time.UTC),
		},
		{
			name: "DST fall back keeps an hourly job hourly", spec: "0 * * * *",
			since: time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC).In(ny), now: time.Date(2026, 11, 1, 6, 5, 0, 0, time.UTC),
			wantOK: true, wantLatest: time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC),
		},
		{
			name: "DST spring forward skips a nonexistent time", spec: "30 2 * * *",
			since: at(2026, 3, 7, 2, 30), now: at(2026, 3, 8, 12, 0),
		},
		{
			name: "DST spring forward resumes next day", spec: "30 2 * * *",
			since: at(2026, 3, 7, 2, 30), now: at(2026, 3, 9, 3, 0),
			wantOK: true, wantLatest: at(2026, 3, 9, 2, 30),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := parseScheduleIn(tt.spec, ny)
			if err != nil {
				t.Fatal(err)
			}
			got, ok := DueFires(s, tt.since, tt.now, tt.jitter)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v (latest %v), want %v", ok, got.Latest, tt.wantOK)
			}
			if !ok {
				return
			}
			if !got.Latest.Equal(tt.wantLatest) {
				t.Errorf("Latest = %v, want %v", got.Latest, tt.wantLatest)
			}
			if got.Missed != tt.wantMissed {
				t.Errorf("Missed = %d, want %d", got.Missed, tt.wantMissed)
			}
		})
	}
}

func TestNextFire(t *testing.T) {
	s, _ := parseScheduleIn("0 * * * *", time.UTC)
	now := time.Date(2026, 11, 17, 9, 0, 30, 0, time.UTC)
	// Jitter 60s: the 09:00 fire is still pending until 09:01.
	if got, want := NextFire(s, now, time.Minute), time.Date(2026, 11, 17, 9, 1, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("NextFire = %v, want %v", got, want)
	}
	if got, want := NextFire(s, now, 0), time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("NextFire = %v, want %v", got, want)
	}
}
