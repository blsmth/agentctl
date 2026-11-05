package hooks

import (
	"testing"

	"github.com/blsmth/agentctl/internal/tui"
)

// withFakeClientDimensions overrides clientDimensions for the duration of a
// test and restores the original afterward.
func withFakeClientDimensions(t *testing.T, width, height int, ok bool) {
	t.Helper()
	orig := clientDimensions
	clientDimensions = func() (int, int, bool) { return width, height, ok }
	t.Cleanup(func() { clientDimensions = orig })
}

func TestClampPopupToClient_DerivedSmallerThanTerminal(t *testing.T) {
	withFakeClientDimensions(t, 200, 100, true)

	derived := tui.PopupSize{Width: 80, Height: 30}
	got := clampPopupToClient(derived)

	if got != derived {
		t.Errorf("clampPopupToClient(%+v) = %+v, want unchanged (client is bigger)", derived, got)
	}
}

func TestClampPopupToClient_DerivedLargerThanTerminal(t *testing.T) {
	withFakeClientDimensions(t, 80, 30, true)

	derived := tui.PopupSize{Width: 200, Height: 100}
	got := clampPopupToClient(derived)

	want := tui.PopupSize{Width: 80 - popupMargin, Height: 30 - popupMargin}
	if got != want {
		t.Errorf("clampPopupToClient(%+v) = %+v, want %+v", derived, got, want)
	}
}

// TestClampPopupToClient_QueryFailsFallsThroughUnclamped covers the
// "failure or unparseable output" case named in the spec: in production,
// tmux not running, no attached client, or garbage tmux output (empty
// string, non-numeric, zero, negative) all make tmuxDisplayInt/
// tmuxClientDimensions return ok=false. That is exactly what the injected
// clientDimensions fake below simulates -- the seam is at the
// client-dimensions level (see clampPopupToClient's doc comment), not at the
// raw tmux output, so this is where the contract is tested rather than
// re-testing exec.Command plumbing.
func TestClampPopupToClient_QueryFailsFallsThroughUnclamped(t *testing.T) {
	withFakeClientDimensions(t, 0, 0, false)

	derived := tui.PopupSize{Width: 200, Height: 100}
	got := clampPopupToClient(derived)

	if got != derived {
		t.Errorf("clampPopupToClient(%+v) = %+v, want unchanged (query failed)", derived, got)
	}
}

func TestClampPopupToClient_OnlyClampsTheOversizedDimension(t *testing.T) {
	// Width fits, height doesn't -- only height should move.
	withFakeClientDimensions(t, 200, 30, true)

	derived := tui.PopupSize{Width: 80, Height: 100}
	got := clampPopupToClient(derived)

	want := tui.PopupSize{Width: 80, Height: 30 - popupMargin}
	if got != want {
		t.Errorf("clampPopupToClient(%+v) = %+v, want %+v", derived, got, want)
	}
}

// --- dispatchPreviewFit ------------------------------------------------------

func TestDispatchPreviewFit_TinyTerminalShrinksToFloor(t *testing.T) {
	withFakeClientDimensions(t, 50, 20, true)

	contentWidth, vpHeight := dispatchPreviewFit()

	if contentWidth != dpMinContentWidth {
		t.Errorf("contentWidth = %d, want the floor %d", contentWidth, dpMinContentWidth)
	}
	if vpHeight != dpMinVPHeight {
		t.Errorf("vpHeight = %d, want the floor %d", vpHeight, dpMinVPHeight)
	}
}

func TestDispatchPreviewFit_HugeTerminalCapsAtReadabilityMax(t *testing.T) {
	withFakeClientDimensions(t, 500, 300, true)

	contentWidth, vpHeight := dispatchPreviewFit()

	if contentWidth != dpMaxContentWidth {
		t.Errorf("contentWidth = %d, want the readability cap %d", contentWidth, dpMaxContentWidth)
	}
	if vpHeight != dpMaxVPHeightCap {
		t.Errorf("vpHeight = %d, want the height cap %d", vpHeight, dpMaxVPHeightCap)
	}
}

func TestDispatchPreviewFit_MidSizeFitsExactly(t *testing.T) {
	// Chosen so neither dimension hits a floor or ceiling, and the height
	// fraction lands on a clean number: width=200 -> 200-8-2=190 (clamped to
	// 132); use a smaller width instead so it lands inside the band.
	withFakeClientDimensions(t, 130, 60, true)

	contentWidth, vpHeight := dispatchPreviewFit()

	wantContentWidth := 130 - 8 - popupMargin // 120, inside [60,132]
	if contentWidth != wantContentWidth {
		t.Errorf("contentWidth = %d, want %d", contentWidth, wantContentWidth)
	}

	budget := int(float64(60) * heightBudgetShare) // 36
	wantVPHeight := budget - 16 - popupMargin      // 18, inside [8,40]
	if vpHeight != wantVPHeight {
		t.Errorf("vpHeight = %d, want %d", vpHeight, wantVPHeight)
	}
}

func TestDispatchPreviewFit_QueryFailureReturnsFallback(t *testing.T) {
	withFakeClientDimensions(t, 0, 0, false)

	contentWidth, vpHeight := dispatchPreviewFit()

	if contentWidth != dpFallbackContentWidth {
		t.Errorf("contentWidth = %d, want the fallback %d", contentWidth, dpFallbackContentWidth)
	}
	if vpHeight != dpFallbackVPHeight {
		t.Errorf("vpHeight = %d, want the fallback %d", vpHeight, dpFallbackVPHeight)
	}
}

// --- filePreviewFit -----------------------------------------------------

func TestFilePreviewFit_TinyTerminalShrinksToFloor(t *testing.T) {
	withFakeClientDimensions(t, 50, 20, true)

	contentWidth, vpHeight := filePreviewFit()

	if contentWidth != fpMinContentWidth {
		t.Errorf("contentWidth = %d, want the floor %d", contentWidth, fpMinContentWidth)
	}
	if vpHeight != fpMinVPHeight {
		t.Errorf("vpHeight = %d, want the floor %d", vpHeight, fpMinVPHeight)
	}
}

func TestFilePreviewFit_HugeTerminalCapsAtReadabilityMax(t *testing.T) {
	withFakeClientDimensions(t, 500, 300, true)

	contentWidth, vpHeight := filePreviewFit()

	if contentWidth != fpMaxContentWidth {
		t.Errorf("contentWidth = %d, want the readability cap %d", contentWidth, fpMaxContentWidth)
	}
	if vpHeight != fpMaxVPHeightCap {
		t.Errorf("vpHeight = %d, want the height cap %d", vpHeight, fpMaxVPHeightCap)
	}
}

func TestFilePreviewFit_MidSizeFitsExactly(t *testing.T) {
	withFakeClientDimensions(t, 130, 60, true)

	contentWidth, vpHeight := filePreviewFit()

	wantContentWidth := 130 - 8 - popupMargin // 120, inside [60,140]
	if contentWidth != wantContentWidth {
		t.Errorf("contentWidth = %d, want %d", contentWidth, wantContentWidth)
	}

	wantVPHeight := 60 - 12 - popupMargin // 46, inside [8,50]
	if vpHeight != wantVPHeight {
		t.Errorf("vpHeight = %d, want %d", vpHeight, wantVPHeight)
	}
}

func TestFilePreviewFit_QueryFailureReturnsFallback(t *testing.T) {
	withFakeClientDimensions(t, 0, 0, false)

	contentWidth, vpHeight := filePreviewFit()

	if contentWidth != fpFallbackContentWidth {
		t.Errorf("contentWidth = %d, want the fallback %d", contentWidth, fpFallbackContentWidth)
	}
	if vpHeight != fpFallbackVPHeight {
		t.Errorf("vpHeight = %d, want the fallback %d", vpHeight, fpFallbackVPHeight)
	}
}

// --- fitToClient / clampInt --------------------------------------------------

func TestFitToClient_ClampsIntoBand(t *testing.T) {
	tests := []struct {
		name           string
		clientDim      int
		overhead       int
		minVal, maxVal int
		want           int
	}{
		{"below floor", 10, 8, 60, 132, 60},
		{"above ceiling", 1000, 8, 60, 132, 132},
		{"inside band", 140, 8, 60, 132, 140 - 8 - popupMargin},
		{"negative after overhead clamps to floor", 5, 8, 60, 132, 60},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fitToClient(tt.clientDim, tt.overhead, tt.minVal, tt.maxVal)
			if got != tt.want {
				t.Errorf("fitToClient(%d, %d, %d, %d) = %d, want %d",
					tt.clientDim, tt.overhead, tt.minVal, tt.maxVal, got, tt.want)
			}
		})
	}
}
