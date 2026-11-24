package improve

import (
	"strings"
	"unicode"
)

// stripInvisible removes characters that render as nothing from a launch
// prompt: Unicode format characters (zero-width space/joiner, BOM, soft
// hyphen, bidi controls, ...), the Braille blank U+2800 that statusline
// padding leaves in captured text, and the Hangul fillers (U+115F, U+1160,
// U+3164, U+FFA0). Claude Code strips them itself and prints a notice at
// launch, so the evidence is cleaned first. Newlines and tabs are kept.
func stripInvisible(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case unicode.Is(unicode.Cf, r),
			r == 0x2800, r == 0x115F, r == 0x1160, r == 0x3164, r == 0xFFA0:
			return -1
		}
		return r
	}, s)
}
