package hooks

import (
	"testing"

	"github.com/blsmth/agentctl/internal/tui"
)

// TestPreviewKindConstants_MatchTUI pins the fact that internal/tui's
// untyped string constants (PreviewKindDispatch / PreviewKindBash) stay in
// sync with internal/hooks.PreviewKind's constants of the same name. The two
// packages can't share a single defined type -- tui.WithKind takes a plain
// string precisely so tui doesn't have to import hooks -- so the only thing
// keeping the args written by hooks and the args read by tui in agreement is
// a doc comment. This test makes that agreement self-enforcing: hooks can
// import tui, so it's the one side of the pair that can actually check.
func TestPreviewKindConstants_MatchTUI(t *testing.T) {
	if string(PreviewKindDispatch) != tui.PreviewKindDispatch {
		t.Errorf("PreviewKindDispatch = %q, tui.PreviewKindDispatch = %q, want equal", PreviewKindDispatch, tui.PreviewKindDispatch)
	}
	if string(PreviewKindBash) != tui.PreviewKindBash {
		t.Errorf("PreviewKindBash = %q, tui.PreviewKindBash = %q, want equal", PreviewKindBash, tui.PreviewKindBash)
	}
}
