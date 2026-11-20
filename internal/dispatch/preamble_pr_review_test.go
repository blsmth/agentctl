package dispatch

import (
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
)

var prReviewOrch = Orchestrator{WindowID: "@26", Pane: "%77"}

func TestPRReviewAlwaysUnchanged(t *testing.T) {
	for _, mode := range []string{"", "always", "bogus"} {
		if got := preambleForHarnessFor(RoleOrchestrator, true, mode); got != OrchestratorPreamble {
			t.Errorf("mode %q: orchestrator preamble differs from OrchestratorPreamble", mode)
		}
		if got := preambleForHarnessFor(RoleOrchestrator, false, mode); got != preambleForHarness(RoleOrchestrator, false) {
			t.Errorf("mode %q: no-messaging orchestrator preamble changed", mode)
		}
		if got := preambleWithOrchestratorFor(RoleWorker, prReviewOrch, mode); got != preambleWithOrchestrator(RoleWorker, prReviewOrch) {
			t.Errorf("mode %q: worker preamble changed", mode)
		}
	}
	if !strings.Contains(OrchestratorPreamble, "Every PR gets a review:") {
		t.Error("default orchestrator preamble lost the always-review line")
	}
}

func TestPRReviewOrchestratorModes(t *testing.T) {
	tests := []struct {
		mode    string
		want    []string
		notWant []string
	}{
		{config.PRReviewAlways, []string{"Every PR gets a review:", "VERDICT"}, nil},
		{config.PRReviewOptional,
			[]string{"### Review", "large or risky PRs", "security-relevant", "may be skipped", "gh pr checks <N>", "VERDICT", "--review-pr <N> --agent code-reviewer"},
			[]string{"Every PR gets a review"}},
		{config.PRReviewOff,
			[]string{"### Review", "Do not dispatch reviewers", "gh pr checks <N>", "escalate to the human"},
			[]string{"Every PR gets a review", "--review-pr", "VERDICT"}},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			got := preambleForHarnessFor(RoleOrchestrator, true, tt.mode)
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q", w)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(got, w) {
					t.Errorf("unexpected %q", w)
				}
			}
		})
	}
}

func TestPRReviewNoMessagingHasNoReviewSection(t *testing.T) {
	base := preambleForHarness(RoleOrchestrator, false)
	for _, mode := range []string{"always", "optional", "off"} {
		got := preambleForHarnessFor(RoleOrchestrator, false, mode)
		if got != base {
			t.Errorf("mode %s: no-messaging preamble differs from default", mode)
		}
		if strings.Contains(got, "### Review") {
			t.Errorf("mode %s: no-messaging preamble has a Review section", mode)
		}
	}
}

func TestPRReviewWorkerSection(t *testing.T) {
	const reviewer = "A reviewer may message you about it."
	const responsive = "stay responsive until it merges"
	for _, mode := range []string{"always", "optional"} {
		got := preambleWithOrchestratorFor(RoleWorker, prReviewOrch, mode)
		if !strings.Contains(got, reviewer) || !strings.Contains(got, responsive) {
			t.Errorf("mode %s: worker lost reviewer sentences", mode)
		}
	}
	got := preambleWithOrchestratorFor(RoleWorker, prReviewOrch, "off")
	if strings.Contains(got, "reviewer") {
		t.Error("off: worker preamble still mentions a reviewer")
	}
	if !strings.Contains(got, responsive) {
		t.Error("off: worker lost the stay-responsive sentence")
	}
	if !strings.Contains(got, "Your orchestrator is the session in") {
		t.Error("off: worker lost the orchestrator section")
	}
}

func TestAllPreamblesCoversPRReviewModes(t *testing.T) {
	all := AllPreambles()
	for _, k := range []string{"OrchestratorPreamble(pr_review optional)", "OrchestratorPreamble(pr_review off)", "orchestratorSection(pr_review off)"} {
		if all[k] == "" {
			t.Errorf("AllPreambles missing %q", k)
		}
	}
}
