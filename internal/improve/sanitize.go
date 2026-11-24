package improve

import "strings"

// stripInvisible removes characters that render as nothing from a launch
// prompt: zero-width space and word joiner, the invisible math operators, BOM,
// soft hyphen, bidi controls, the Braille blank U+2800 that statusline padding
// leaves in captured text, and the Hangul fillers. Claude Code strips these
// itself and prints a notice at launch, so the evidence is cleaned first.
// Joiners (U+200C, U+200D) are kept: they matter in emoji sequences and
// Persian text. Newlines and tabs are kept.
func stripInvisible(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == 0x200B, r == 0x00AD, r == 0xFEFF, r == 0x180E, r == 0x061C,
			r == 0x200E, r == 0x200F,
			r >= 0x202A && r <= 0x202E,
			r >= 0x2060 && r <= 0x2064,
			r >= 0x2066 && r <= 0x2069,
			r == 0x2800, r == 0x115F, r == 0x1160, r == 0x3164, r == 0xFFA0:
			return -1
		}
		return r
	}, s)
}
