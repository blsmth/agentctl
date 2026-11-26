package hooks

import (
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/cron"
	"github.com/blsmth/agentctl/internal/remoteapproval"
)

func TestBuildCronPreviewAt(t *testing.T) {
	isolateContextStore(t)
	withCronNow(t) // 2026-11-17 12:00 local
	done := cron.Job{Name: "done", At: cron.FormatAt(time.Date(2026, 11, 16, 9, 0, 0, 0, time.Local)), Agent: "ops", Dir: "/srv",
		Prompt: "ping", State: cron.StateCompleted, CompletedAt: time.Date(2026, 11, 16, 9, 1, 0, 0, time.Local), Result: cron.ResultFired}
	withCronJobs(t,
		cron.Job{Name: "daily", Schedule: "0 9 * * *", Agent: "ops", Dir: "/srv", Prompt: "stored"},
		done,
	)
	tests := []struct {
		name         string
		command      string
		wantDeny     string
		wantAction   string
		wantSchedule string
		wantBody     []string
	}{
		{
			name:         "add at",
			command:      `agentctl cron add remind --at "2026-12-01 09:00" -a ops --dir /x -p "check the deploy"`,
			wantAction:   cronActionAdd,
			wantSchedule: "once, Tue 1 Dec 09:00",
			wantBody:     []string{"**Schedule:** once, Tue 1 Dec 09:00", "check the deploy"},
		},
		{
			name:         "add at=",
			command:      `agentctl cron add remind --at=2026-12-01T09:00 -a ops --dir /x -p hi`,
			wantAction:   cronActionAdd,
			wantSchedule: "once, Tue 1 Dec 09:00",
		},
		{name: "add at command substitution", command: `agentctl cron add remind --at "$(date -v+1d '+%Y-%m-%d 09:00')" -a ops --dir /x -p hi`, wantDeny: "--at uses shell expansion"},
		{name: "add at variable", command: `agentctl cron add remind --at "$WHEN" -a ops --dir /x -p hi`, wantDeny: "--at uses shell expansion"},
		{name: "add at and schedule", command: `agentctl cron add remind --at "2026-12-01 09:00" --schedule @daily -a ops --dir /x -p hi`, wantDeny: "exactly one of --schedule"},
		{name: "add at in the past", command: `agentctl cron add remind --at "2026-11-17 11:59" -a ops --dir /x -p hi`, wantDeny: "in the past"},
		{name: "add at garbage", command: `agentctl cron add remind --at tomorrow -a ops --dir /x -p hi`, wantDeny: "YYYY-MM-DD HH:MM"},
		{
			name:         "edit to a one-shot",
			command:      `agentctl cron edit daily --at "2026-12-01 09:00"`,
			wantAction:   cronActionEdit,
			wantSchedule: "once, Tue 1 Dec 09:00",
			wantBody:     []string{"Edit changes: schedule, at.", "becomes a one-shot"},
		},
		{
			name:         "edit resets a done one-shot",
			command:      `agentctl cron edit done --at "2026-12-02 10:00"`,
			wantAction:   cronActionEdit,
			wantSchedule: "once, Wed 2 Dec 10:00",
			wantBody:     []string{"state"},
		},
		{name: "edit at expansion", command: `agentctl cron edit daily --at "$(date)"`, wantDeny: "cron edit: --at uses shell expansion"},
		{name: "edit at and schedule", command: `agentctl cron edit daily --at "2026-12-01 09:00" --schedule @daily`, wantDeny: "mutually exclusive"},
		{name: "resume a done one-shot", command: `agentctl cron resume done`, wantDeny: "cron edit done --at"},
		{name: "force-run a done one-shot", command: `agentctl cron run done --force`, wantDeny: "already ran"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !isAgentctlCronGated(tt.command) {
				t.Fatalf("not gated: %s", tt.command)
			}
			p, deny := buildCronPreview(tt.command)
			if tt.wantDeny != "" {
				if !strings.Contains(deny, tt.wantDeny) {
					t.Fatalf("deny = %q, want containing %q", deny, tt.wantDeny)
				}
				return
			}
			if deny != "" {
				t.Fatalf("unexpected deny: %s", deny)
			}
			if p.action != tt.wantAction || p.schedule != tt.wantSchedule {
				t.Errorf("action/schedule = %q/%q, want %q/%q", p.action, p.schedule, tt.wantAction, tt.wantSchedule)
			}
			for _, want := range tt.wantBody {
				if !strings.Contains(p.body, want) {
					t.Errorf("body missing %q:\n%s", want, p.body)
				}
			}
			// The phone approval carries the same summary.
			n := cronPopupNotice("orch", tt.command, p)
			if !strings.Contains(n.Detail, tt.wantSchedule) || n.Remote == nil || n.Remote.Kind != remoteapproval.KindCron ||
				!strings.Contains(n.Remote.Prompt, "**Schedule:** "+tt.wantSchedule) {
				t.Errorf("notice = %+v remote = %+v", n, n.Remote)
			}
		})
	}
}

// TestCronSegmentParseAt: --at takes a value, so the word after it is
// never a positional or a help flag.
func TestCronSegmentParseAt(t *testing.T) {
	segs := cronSegments(`agentctl cron add r --at --help -a ops`)
	if len(segs) != 1 {
		t.Fatalf("segments = %+v", segs)
	}
	a := segs[0].parse()
	if a.value("at") != "--help" || len(a.positional) != 1 || len(a.unknown) != 0 {
		t.Errorf("parse = %+v", a)
	}
	if !segs[0].gated() {
		t.Error("--at --help read as a help lookup")
	}
}
