package dispatch

import (
	"strings"
	"testing"
)

func TestWaitGuidanceByHarness(t *testing.T) {
	claudeOnly := []string{"CronCreate", "run_in_background", "Monitor tool", "notify_when_idle"}
	tests := []struct {
		name      string
		preamble  string
		wantClaud bool
		want      string
	}{
		{"worker", preambleForHarness(RoleWorker, true), true, "Never `sleep`"},
		{"orchestrator", preambleForHarness(RoleOrchestrator, true), true, "agentctl cron"},
		{"worker no messaging", preambleForHarness(RoleWorker, false), false, ""},
		{"orchestrator no messaging", preambleForHarness(RoleOrchestrator, false), false, "Do not poll"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.want != "" && !strings.Contains(tt.preamble, tt.want) {
				t.Errorf("missing %q", tt.want)
			}
			for _, w := range claudeOnly {
				if !tt.wantClaud && strings.Contains(tt.preamble, w) {
					t.Errorf("%q must not appear without messaging", w)
				}
			}
		})
	}
}
