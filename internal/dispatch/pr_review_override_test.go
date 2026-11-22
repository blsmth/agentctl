package dispatch

import (
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
)

func TestEffectivePRReview(t *testing.T) {
	tests := []struct {
		name, configured, override, want string
	}{
		{"empty keeps config always", config.PRReviewAlways, "", config.PRReviewAlways},
		{"empty keeps config optional", config.PRReviewOptional, "", config.PRReviewOptional},
		{"override wins over always", config.PRReviewAlways, config.PRReviewOff, config.PRReviewOff},
		{"override wins over optional", config.PRReviewOptional, config.PRReviewOff, config.PRReviewOff},
		{"off stays off", config.PRReviewOff, config.PRReviewOff, config.PRReviewOff},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := effectivePRReview(tt.configured, tt.override)
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
			p := preambleForHarnessFor(RoleOrchestrator, true, got)
			off := strings.Contains(p, "Do not dispatch reviewers")
			if off != (tt.want == config.PRReviewOff) {
				t.Errorf("off-mode Review section present = %v for %q", off, tt.want)
			}
		})
	}
}

func TestAdhocRequestCarriesPRReview(t *testing.T) {
	got := stubAdhoc(t, "o/r", nil)
	if _, err := LaunchAdhoc(AdhocRequest{Prompt: "p", AgentName: "a", WorkDir: t.TempDir(), PRReview: config.PRReviewOff}); err != nil {
		t.Fatal(err)
	}
	if got.PRReviewOverride != config.PRReviewOff {
		t.Errorf("override = %q", got.PRReviewOverride)
	}
}
