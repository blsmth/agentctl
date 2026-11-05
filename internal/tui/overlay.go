package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// compositeOverlay renders fg centered over bg in a termW×termH canvas.
// For each terminal row, the overlay line replaces the corresponding portion
// of the background line; background content remains visible on both sides.
func compositeOverlay(bg, fg string, termW, termH int) string {
	bgLines := strings.Split(bg, "\n")
	fgLines := strings.Split(fg, "\n")

	// Measure overlay visual dimensions.
	fgW := 0
	for _, l := range fgLines {
		if w := lipgloss.Width(l); w > fgW {
			fgW = w
		}
	}
	fgH := len(fgLines)

	// Compute centering offsets.
	hOffset := (termW - fgW) / 2
	vOffset := (termH - fgH) / 2
	if hOffset < 0 {
		hOffset = 0
	}
	if vOffset < 0 {
		vOffset = 0
	}

	result := make([]string, termH)
	for row := 0; row < termH; row++ {
		bgLine := ""
		if row < len(bgLines) {
			bgLine = bgLines[row]
		}

		fgRow := row - vOffset
		if fgRow >= 0 && fgRow < fgH {
			fgLine := fgLines[fgRow]

			// Left portion: first hOffset visible chars of bgLine.
			left := ansi.Truncate(bgLine, hOffset, "")
			// Pad left to exactly hOffset if bgLine is shorter.
			if leftW := lipgloss.Width(left); leftW < hOffset {
				left += strings.Repeat(" ", hOffset-leftW)
			}

			// Right portion: bgLine chars starting at hOffset+fgW.
			right := ansi.TruncateLeft(bgLine, hOffset+fgW, "")

			result[row] = left + fgLine + right
		} else {
			result[row] = bgLine
		}
	}
	return strings.Join(result, "\n")
}
