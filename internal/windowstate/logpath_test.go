package windowstate

import "testing"

func TestLogPathConsistentAcceptsPiSessionFiles(t *testing.T) {
	tests := []struct {
		log, sid string
		want     bool
	}{
		{"/p/sid.jsonl", "sid", true},
		{"/s/--w--/2026-11-17T20-08-01-304Z_sid.jsonl", "sid", true},
		{"/s/--w--/2026-11-17T20-08-01-304Z_other.jsonl", "sid", false},
		{"/p/other.jsonl", "sid", false},
		{"", "sid", true},
	}
	for _, tt := range tests {
		if got := logPathConsistent(tt.log, tt.sid); got != tt.want {
			t.Errorf("logPathConsistent(%q, %q) = %v, want %v", tt.log, tt.sid, got, tt.want)
		}
	}
}
