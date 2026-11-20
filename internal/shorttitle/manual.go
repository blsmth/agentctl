package shorttitle

import (
	"strings"
	"time"
	"unicode"

	"github.com/blsmth/agentctl/internal/windowstate"
)

// MaxManualRunes caps a human-set title (the hub's rename).
const MaxManualRunes = 60

// CleanManual normalizes a title a human typed: control characters (newlines
// and tabs included) become spaces, whitespace is collapsed and trimmed, and
// the result is capped at MaxManualRunes runes. Unlike Sanitize it keeps the
// text as typed otherwise (punctuation, quotes, case).
func CleanManual(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == unicode.ReplacementChar {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > MaxManualRunes {
		s = strings.TrimRight(string(r[:MaxManualRunes]), " ")
	}
	return s
}

// SetManual records title (already cleaned) as the window's human title at
// now: frozen (Final) and Manual, so no automatic path replaces it, with the
// existing kind kept. An empty title clears the title state entirely, so
// automatic titling resumes from the window's task or first prompt. Either
// way any in-flight request's lease is dropped, so its result is discarded.
func SetManual(st *windowstate.ShortTitle, title string, now time.Time) {
	if title == "" {
		*st = windowstate.ShortTitle{}
		return
	}
	*st = windowstate.ShortTitle{
		Title:  title,
		Kind:   st.Kind,
		At:     now,
		Final:  true,
		Manual: true,
	}
}
