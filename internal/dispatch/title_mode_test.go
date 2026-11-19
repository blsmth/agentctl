package dispatch

import (
	"testing"

	"github.com/blsmth/agentctl/internal/shorttitle"
)

// shorttitle cannot import this package (import cycle through hooks), so it
// carries the review-pr mode string; keep the two equal.
func TestShortTitleReviewPRModeMatches(t *testing.T) {
	if string(ModeReviewPR) != shorttitle.ReviewPRMode {
		t.Fatalf("shorttitle.ReviewPRMode = %q, want %q", shorttitle.ReviewPRMode, ModeReviewPR)
	}
}
