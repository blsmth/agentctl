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

// splice replaces the columns [x, x+fgW) of bgLine with fgLine, cutting the
// background by display width so styled lines and wide glyphs survive. A wide
// glyph straddling either edge is replaced by spaces rather than half drawn.
func splice(bgLine, fgLine string, x, fgW int) string {
	bgW := lipgloss.Width(bgLine)
	left := ansi.Truncate(bgLine, x, "")
	if leftW := lipgloss.Width(left); leftW < x {
		left += strings.Repeat(" ", x-leftW)
	}
	right := ""
	if want := bgW - (x + fgW); want > 0 {
		right = ansi.TruncateLeft(bgLine, x+fgW, "")
		if lipgloss.Width(right) > want { // a wide glyph straddles the edge and was kept whole
			right = ansi.TruncateLeft(bgLine, x+fgW+1, "")
		}
		if got := lipgloss.Width(right); got < want {
			right = strings.Repeat(" ", want-got) + right
		}
	}
	return left + "\x1b[m" + fgLine + "\x1b[m" + right
}

// compositeOverlayAt draws fg over bg with its top-left cell at column x, row
// y. Rows of bg outside the overlay are returned unchanged and the result has
// as many lines as bg; the overlay is clipped to the termW×termH canvas.
func compositeOverlayAt(bg, fg string, x, y, termW, termH int) string {
	bgLines := strings.Split(bg, "\n")
	fgLines := strings.Split(fg, "\n")
	fgW := 0
	for _, l := range fgLines {
		fgW = max(fgW, lipgloss.Width(l))
	}
	if x < 0 {
		x = 0
	}
	if x+fgW > termW {
		x = max(termW-fgW, 0)
	}
	for i, fgLine := range fgLines {
		row := y + i
		if row < 0 || row >= len(bgLines) || row >= termH {
			continue
		}
		if w := lipgloss.Width(fgLine); w < fgW {
			fgLine += strings.Repeat(" ", fgW-w)
		}
		bgLines[row] = splice(bgLines[row], fgLine, x, fgW)
	}
	return strings.Join(bgLines, "\n")
}
