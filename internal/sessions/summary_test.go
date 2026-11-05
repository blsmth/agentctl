package sessions

import (
	"strings"
	"testing"
	"time"
)

// fixedTime returns a deterministic time.Time in local timezone for tests.
func fixedTime(hour, min int) time.Time {
	return time.Date(2026, 3, 30, hour, min, 0, 0, time.Local)
}

// makeDate returns a date-only time.Time at noon local time (avoids
// timezone boundary issues when the date is compared in local tz).
func makeDate(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 12, 0, 0, 0, time.Local)
}

func TestFormatDailySummary(t *testing.T) {
	date := makeDate(2026, 3, 30)
	projectDir := "/home/user/code/agentctl"

	metas := []SessionMeta{
		{
			Agent:             "agentctl-agent",
			StartTime:         fixedTime(10, 14),
			EndTime:           fixedTime(11, 47),
			Turns:             14,
			InputTokens:       100000,
			OutputTokens:      5000,
			CacheReadTokens:   50000,
			CacheCreateTokens: 20000,
			ModelName:         "claude-sonnet-4",
			FirstUserMessage:  "hey where do we stand on audit-cmd...",
			FilesEdited:       []string{"hooks.go", "internal/tui/sessions.go"},
			Project:           projectDir,
		},
		{
			Agent:             "go-engineer",
			StartTime:         fixedTime(11, 52),
			EndTime:           fixedTime(12, 30),
			Turns:             22,
			InputTokens:       80000,
			OutputTokens:      4000,
			CacheReadTokens:   30000,
			CacheCreateTokens: 10000,
			ModelName:         "claude-sonnet-4",
			FirstUserMessage:  "Implement sessions summary CLI command...",
			FilesEdited:       []string{"main.go", "internal/sessions/summary.go"},
			Project:           projectDir,
		},
		{
			Agent:             "researcher",
			StartTime:         fixedTime(14, 15),
			EndTime:           fixedTime(14, 28),
			Turns:             6,
			InputTokens:       20000,
			OutputTokens:      1000,
			CacheReadTokens:   5000,
			CacheCreateTokens: 2000,
			ModelName:         "claude-sonnet-4",
			FirstUserMessage:  "How does tmux capture-pane handle wide unicode...",
			Project:           projectDir,
		},
	}

	got := FormatDailySummary(projectDir, date, metas)

	checks := []string{
		"agentctl-agent",
		"go-engineer",
		"researcher",
		"10:14am",
		"11:47am",
		"11:52am",
		"12:30pm",
		"14 turns",
		"22 turns",
		"6 turns",
		"hooks.go",
		"sessions.go",
		"main.go",
		"summary.go",
		"3 sessions",
		"$",
	}

	for _, want := range checks {
		if !strings.Contains(got, want) {
			t.Errorf("FormatDailySummary output missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestFormatDailySummary_NoSessions(t *testing.T) {
	date := makeDate(2026, 3, 30)
	got := FormatDailySummary("/home/user/project", date, nil)
	if !strings.Contains(got, "No sessions found") {
		t.Errorf("expected 'No sessions found', got: %s", got)
	}
}

func TestFormatDailySummary_OngoingSession(t *testing.T) {
	date := time.Now()
	metas := []SessionMeta{
		{
			Agent:     "go-engineer",
			StartTime: time.Now().Add(-30 * time.Minute),
			// EndTime is zero — ongoing
			Turns:   5,
			Project: "/tmp/project",
		},
	}

	got := FormatDailySummary("/tmp/project", date, metas)
	if !strings.Contains(got, "ongoing") {
		t.Errorf("expected 'ongoing' for zero EndTime, got:\n%s", got)
	}
}

func TestFormatDailySummary_ManyFilesEdited(t *testing.T) {
	date := makeDate(2026, 3, 30)
	files := []string{
		"/a/f1.go", "/a/f2.go", "/a/f3.go", "/a/f4.go",
		"/a/f5.go", "/a/f6.go", "/a/f7.go", "/a/f8.go",
	}
	metas := []SessionMeta{
		{
			Agent:       "go-engineer",
			StartTime:   fixedTime(10, 0),
			EndTime:     fixedTime(11, 0),
			Turns:       5,
			FilesEdited: files,
			Project:     "/tmp/project",
		},
	}

	got := FormatDailySummary("/tmp/project", date, metas)
	if !strings.Contains(got, "+3 more") {
		t.Errorf("expected '+3 more' for 8 files, got:\n%s", got)
	}
}

func TestFormatDailySummary_SingleSession(t *testing.T) {
	date := makeDate(2026, 3, 30)
	metas := []SessionMeta{
		{
			Agent:            "go-engineer",
			StartTime:        fixedTime(9, 0),
			EndTime:          fixedTime(9, 45),
			Turns:            12,
			FirstUserMessage: "single session test",
			Project:          "/tmp/project",
		},
	}

	got := FormatDailySummary("/tmp/project", date, metas)
	if !strings.Contains(got, "1 session") {
		t.Errorf("expected '1 session', got:\n%s", got)
	}
	if !strings.Contains(got, "go-engineer") {
		t.Errorf("expected agent name in output, got:\n%s", got)
	}
}

func TestFormatDailySummary_DurationFormatting(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		want     string
	}{
		{"less than 1 minute", 30 * time.Second, "(<1m)"},
		{"38 minutes", 38 * time.Minute, "(38m)"},
		{"1h33m", 93 * time.Minute, "(1h33m)"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := formatSessionDuration(tc.duration)
			if got != tc.want {
				t.Errorf("formatDuration(%v) = %q, want %q", tc.duration, got, tc.want)
			}
		})
	}
}

func TestFormatDailySummary_DurationOngoing(t *testing.T) {
	// Ongoing: zero EndTime → duration computed from start time.
	meta := SessionMeta{
		StartTime: time.Now().Add(-45 * time.Minute),
		// EndTime is zero.
	}
	dur := sessionDuration(meta)
	if dur < 44*time.Minute || dur > 46*time.Minute {
		t.Errorf("ongoing duration = %v, expected ~45m", dur)
	}
}
