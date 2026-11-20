package shorttitle

import (
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/windowstate"
)

func TestCleanManual(t *testing.T) {
	tests := []struct{ in, want string }{
		{"  Fix the hub  ", "Fix the hub"},
		{"line\none\ttab", "line one tab"},
		{"bell\x07 and \x1b[31mred", "bell and [31mred"},
		{"keeps: \"quotes\" & punct!", "keeps: \"quotes\" & punct!"},
		{"   ", ""},
		{strings.Repeat("é", 70), strings.Repeat("é", MaxManualRunes)},
		{strings.Repeat("a", 59) + " b", strings.Repeat("a", 59)},
	}
	for _, tt := range tests {
		if got := CleanManual(tt.in); got != tt.want {
			t.Errorf("CleanManual(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSetManual(t *testing.T) {
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	st := windowstate.ShortTitle{Title: "Auto", Kind: "go", InputHash: "h", RetryAfter: now.Add(time.Minute), Failures: 2}
	SetManual(&st, "Mine", now)
	want := windowstate.ShortTitle{Title: "Mine", Kind: "go", At: now, Final: true, Manual: true}
	if st != want {
		t.Fatalf("SetManual = %+v, want %+v", st, want)
	}
	in := Input{Task: "t", LastText: "x"}
	if Due(st, in, now) {
		t.Error("a manual title must never be due")
	}
	if Due(windowstate.ShortTitle{Title: "Mine", Manual: true}, in, now) {
		t.Error("Manual alone must stop retitling")
	}
	Apply(&st, in, "Generated", "go", nil, now)
	if st != want {
		t.Errorf("Apply replaced a manual title: %+v", st)
	}

	SetManual(&st, "", now)
	if st != (windowstate.ShortTitle{}) {
		t.Errorf("clear = %+v, want zero", st)
	}
	if !Due(st, Input{Task: "t"}, now) {
		t.Error("after a clear the window is due an automatic title")
	}
}
