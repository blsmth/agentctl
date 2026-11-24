package hooks

import (
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/cron"
)

func TestBuildCronPreviewImprove(t *testing.T) {
	isolateContextStore(t)
	withCronNow(t)
	withCronJobs(t, cron.Job{Name: "fleet", Schedule: "0 22 * * *", Dir: "/srv", Mode: cron.ModeImprove, Model: "opus"})
	const summary = "Fleet improve, last 24h, agent fleet-improver, model opus"

	tests := []struct {
		name, command string
		wantBody      []string
		notBody       []string
	}{
		{
			name:     "add --improve-fleet needs no prompt",
			command:  `agentctl cron add fleet-improve --schedule "0 22 * * *" --dir /srv --improve-fleet --model opus`,
			wantBody: []string{summary, "**Mode:** improve", "Overlap:** skip"},
			notBody:  []string{"(no prompt)", "Worktree"},
		},
		{
			name:     "add --mode improve with a theme",
			command:  `agentctl cron add fleet-improve --schedule "0 22 * * *" --dir /srv --mode improve --model opus --prompt "focus on permission prompts"`,
			wantBody: []string{summary, "Added to the prompt", "focus on permission prompts"},
		},
		{
			name:     "edit a stored improve job",
			command:  `agentctl cron edit fleet --model sonnet`,
			wantBody: []string{"Fleet improve, last 24h, agent fleet-improver, model sonnet", "Edit changes: model"},
		},
		{
			name:     "resume shows the summary",
			command:  `agentctl cron resume fleet`,
			wantBody: []string{summary},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, deny := buildCronPreview(tt.command)
			if deny != "" {
				t.Fatalf("denied: %s", deny)
			}
			if p.agent != cron.ImproveAgent {
				t.Errorf("agent = %q, want %q", p.agent, cron.ImproveAgent)
			}
			for _, w := range tt.wantBody {
				if !strings.Contains(p.body, w) {
					t.Errorf("body missing %q:\n%s", w, p.body)
				}
			}
			for _, w := range tt.notBody {
				if strings.Contains(p.body, w) {
					t.Errorf("body has %q:\n%s", w, p.body)
				}
			}
		})
	}
}
