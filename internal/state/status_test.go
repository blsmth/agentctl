package state

import "testing"

func TestAgentStatusString(t *testing.T) {
	tests := []struct {
		status AgentStatus
		want   string
	}{
		{StatusWorking, "working"},
		{StatusIdle, "idle"},
		{StatusBlocked, "blocked"},
		{StatusGone, "gone"},
		{AgentStatus(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := tt.status.String()
			if got != tt.want {
				t.Errorf("AgentStatus(%d).String() = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}
