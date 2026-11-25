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

func TestScheduleGuidanceByVariant(t *testing.T) {
	core := []string{"agentctl cron add <name> --schedule", "--prompt-key", "--prompt-file", "approval popup", "cron runs <name>", "--timeout", "no run-once job", "agentctl cron rm <name>", "docs/configuration.md#cron"}
	msg := preambleForHarness(RoleOrchestrator, true)
	for _, w := range append([]string{"### Schedule", "CronCreate", "cloud `schedule` skill"}, core...) {
		if !strings.Contains(msg, w) {
			t.Errorf("orchestrator preamble missing %q", w)
		}
	}
	if strings.Contains(msg, "Anything that must outlive the session: `agentctl cron`, not") {
		t.Error("redundant Wait line still present")
	}
	if !strings.Contains(preambleForHarnessFor(RoleOrchestrator, true, "off"), "### Schedule") {
		t.Error("pr_review variant lost the Schedule section")
	}

	if strings.Contains(orchScheduleNoMessagingSection, "skill") {
		t.Error("no-messaging Schedule section names skills")
	}
	nomsg := preambleForHarness(RoleOrchestrator, false)
	for _, w := range core {
		if !strings.Contains(nomsg, w) {
			t.Errorf("no-messaging orchestrator missing %q", w)
		}
	}
	for _, w := range []string{"CronCreate", "/loop", "cloud `schedule`"} {
		if strings.Contains(nomsg, w) {
			t.Errorf("no-messaging orchestrator names %q", w)
		}
	}

	for _, messaging := range []bool{true, false} {
		w := preambleForHarness(RoleWorker, messaging)
		if strings.Contains(w, "agentctl cron add") {
			t.Errorf("worker (messaging=%v) got the orchestrator Schedule text", messaging)
		}
		if !strings.Contains(w, "Never create `agentctl cron` jobs") {
			t.Errorf("worker (messaging=%v) missing no-cron line", messaging)
		}
	}
	for _, w := range []string{"--overlap skip", "--template"} {
		if !strings.Contains(nomsg, w) {
			t.Errorf("no-messaging orchestrator missing %q", w)
		}
	}
}

func TestLaunchFactsScheduledRun(t *testing.T) {
	facts := launchFacts{WorkDir: "/w", Branch: "b", InWorktree: true, Window: "w"}
	got := launchFactsBlock(Request{Mode: ModeAdhoc, ScheduledBy: "nightly"}, facts)
	for _, w := range []string{"Scheduled run of agentctl cron job `nightly`", "nobody is watching", "do not wait on a human answer"} {
		if !strings.Contains(got, w) {
			t.Errorf("scheduled launch facts missing %q:\n%s", w, got)
		}
	}
	for _, w := range []string{"CronCreate", "skill", "SendMessage"} {
		if strings.Contains(scheduledRunNote("x"), w) {
			t.Errorf("scheduled note names %q", w)
		}
	}
	if got := launchFactsBlock(Request{Mode: ModeAdhoc}, facts); strings.Contains(got, "Scheduled run") {
		t.Errorf("normal launch carries the note:\n%s", got)
	}
}
