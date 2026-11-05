package render

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
)

// ── Battery ──────────────────────────────────────────────────────────────────

func TestBattery(t *testing.T) {
	tests := []struct {
		pct       int
		wantColor string // color description
	}{
		{25, "green"},  // <=50%
		{50, "green"},  // exactly 50%
		{51, "yellow"}, // >50%
		{75, "yellow"}, // exactly 75%
		{76, "red"},    // >75%
		{100, "red"},
	}
	for _, tt := range tests {
		bar := Battery(tt.pct)
		if bar == "" {
			t.Errorf("Battery(%d) returned empty string", tt.pct)
		}
		// Bar should contain both filled and unfilled characters (unless 100%).
		if tt.pct < 100 && !strings.Contains(bar, "░") {
			t.Errorf("Battery(%d) missing empty block '░'", tt.pct)
		}
		if tt.pct > 0 && !strings.Contains(bar, "█") {
			t.Errorf("Battery(%d) missing filled block '█'", tt.pct)
		}
	}
}

func TestBatteryFilledRatio(t *testing.T) {
	// At 50%, exactly 10 of 20 blocks should be filled.
	bar := Battery(50)
	filled := strings.Count(bar, "█")
	empty := strings.Count(bar, "░")
	if filled != 10 {
		t.Errorf("Battery(50) should have 10 filled blocks, got %d", filled)
	}
	if empty != 10 {
		t.Errorf("Battery(50) should have 10 empty blocks, got %d", empty)
	}
}

func TestBatteryTotalIs20(t *testing.T) {
	for _, pct := range []int{0, 25, 50, 75, 100} {
		bar := Battery(pct)
		total := strings.Count(bar, "█") + strings.Count(bar, "░")
		if total != 20 {
			t.Errorf("Battery(%d) should have 20 total blocks, got %d", pct, total)
		}
	}
}

func TestBatteryCaps(t *testing.T) {
	for _, pct := range []int{0, 25, 50, 75, 100} {
		bar := Battery(pct)
		if !strings.Contains(bar, "▐") {
			t.Errorf("Battery(%d) missing left cap '▐'", pct)
		}
		if !strings.Contains(bar, "▌") {
			t.Errorf("Battery(%d) missing right cap '▌'", pct)
		}
	}
}

// ── AbbreviateModel ──────────────────────────────────────────────────────────

func TestAbbreviateModel(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{"claude-opus-4-20250514", "opus-4"},
		{"claude-sonnet-4-20250514", "sonnet-4"},
		{"claude-haiku-3-5-20251022", "haiku-3.5"},
		{"claude-3-5-sonnet-20241022", "3-5-sonnet"},
		{"claude-3-opus-20240229", "3-opus"},
		{"unknown-model", "unknown-model"},
		{"", ""},
	}
	for _, tt := range tests {
		got := AbbreviateModel(tt.model)
		if got != tt.want {
			t.Errorf("AbbreviateModel(%q) = %q, want %q", tt.model, got, tt.want)
		}
	}
}

// ── TokensK ──────────────────────────────────────────────────────────────────

func TestTokensK(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{1500, "1k"},
		{850, "850"},
		{0, "0"},
		{1000, "1k"},
		{9999, "9k"},
		{10000, "10k"},
		{999, "999"},
	}
	for _, tt := range tests {
		got := TokensK(tt.n)
		if got != tt.want {
			t.Errorf("TokensK(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

// ── Duration ─────────────────────────────────────────────────────────────────

func TestDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{5 * time.Second, "5s"},
		{90 * time.Second, "1m 30s"},
		{90 * time.Minute, "1h 30m"},
		{0, "0s"},
	}
	for _, tt := range tests {
		got := Duration(tt.d)
		if got != tt.want {
			t.Errorf("Duration(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

// ── ThresholdStyle ───────────────────────────────────────────────────────────

func TestThresholdStyle(t *testing.T) {
	green := "#a6e3a1"
	yellow := "#f9e2af"
	red := "#f38ba8"

	thresholds := []Threshold{
		{50, green},
		{75, yellow},
		{100, red},
	}

	greenRender := lipgloss.NewStyle().Foreground(lipgloss.Color(green)).Render("X")
	yellowRender := lipgloss.NewStyle().Foreground(lipgloss.Color(yellow)).Render("X")
	redRender := lipgloss.NewStyle().Foreground(lipgloss.Color(red)).Render("X")

	tests := []struct {
		name string
		val  int
		want string
	}{
		{"zero", 0, greenRender},
		{"below_first", 25, greenRender},
		{"at_first", 50, greenRender},
		{"between", 60, yellowRender},
		{"at_second", 75, yellowRender},
		{"above_second", 80, redRender},
		{"at_max", 100, redRender},
		{"over_max", 150, redRender},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ThresholdStyle(tt.val, thresholds).Render("X")
			if got != tt.want {
				t.Errorf("ThresholdStyle(%d).Render(\"X\") = %q, want %q", tt.val, got, tt.want)
			}
		})
	}
}

func TestThresholdStyleEmpty(t *testing.T) {
	got := ThresholdStyle(42, nil).Render("X")
	want := lipgloss.NewStyle().Render("X")
	if got != want {
		t.Errorf("ThresholdStyle with nil thresholds = %q, want %q", got, want)
	}
}

// ── ResetTime ────────────────────────────────────────────────────────────────

func TestResetTime(t *testing.T) {
	tests := []struct {
		name     string
		resetsAt int64
		want     string
	}{
		{"zero", 0, ""},
		{"past", time.Now().Add(-1 * time.Hour).Unix(), "resets now"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResetTime(tt.resetsAt)
			if got != tt.want {
				t.Errorf("ResetTime(%d) = %q, want %q", tt.resetsAt, got, tt.want)
			}
		})
	}

	t.Run("relative under 1h", func(t *testing.T) {
		resetsAt := time.Now().Add(30 * time.Minute).Unix()
		got := ResetTime(resetsAt)
		if !strings.Contains(got, "resets") || !strings.Contains(got, "m") {
			t.Errorf("expected 'resets Xm' format, got: %q", got)
		}
	})

	t.Run("clock over 1h", func(t *testing.T) {
		resetsAt := time.Now().Add(3 * time.Hour).Unix()
		got := ResetTime(resetsAt)
		if !strings.Contains(got, "resets") {
			t.Errorf("expected 'resets Xpm' format, got: %q", got)
		}
	})
}
