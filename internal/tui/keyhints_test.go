package tui

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/blsmth/agentctl/internal/theme"
)

// TestRenderKeybindHintsContainsAllHints verifies that a wide line includes
// the ctrl+b badge and every keybinding key and label.
func TestRenderKeybindHintsContainsAllHints(t *testing.T) {
	out := renderKeybindHints(200, defaultStatusBarHints, false, theme.Current())
	if !strings.Contains(out, "ctrl+b") {
		t.Errorf("missing ctrl+b badge, got: %q", out)
	}
	for _, h := range defaultStatusBarHints {
		if !strings.Contains(out, h.Key) {
			t.Errorf("missing key hint %q", h.Key)
		}
		if !strings.Contains(out, h.Label) {
			t.Errorf("missing label %q", h.Label)
		}
	}
	if strings.Contains(out, "\n") {
		t.Error("keybind line should be a single line")
	}
}

// TestRenderKeybindHintsDimVsLit verifies the rendering differs between
// prefix-inactive (dim) and prefix-active (lit) states. Forces TrueColor so
// Lip Gloss emits ANSI escape codes even in non-TTY tests.
func TestRenderKeybindHintsDimVsLit(t *testing.T) {
	lipgloss.Writer.Profile = colorprofile.TrueColor

	dim := renderKeybindHints(200, defaultStatusBarHints, false, theme.Current())
	lit := renderKeybindHints(200, defaultStatusBarHints, true, theme.Current())
	if dim == lit {
		t.Error("output should differ between prefixActive=false and prefixActive=true")
	}
}

// TestRenderKeybindHintsFillsWidth verifies the line fills exactly to the
// requested width, both above the natural content length and when hints
// must be dropped to fit.
func TestRenderKeybindHintsFillsWidth(t *testing.T) {
	for _, w := range []int{80, 100, 120, 160, 200, 240} {
		t.Run(fmt.Sprintf("width_%d", w), func(t *testing.T) {
			out := renderKeybindHints(w, defaultStatusBarHints, false, theme.Current())
			if got := lipgloss.Width(out); got != w {
				t.Errorf("width = %d, want %d", got, w)
			}
		})
	}
}
