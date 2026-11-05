// Package textutil holds small, dependency-free string helpers shared across
// agentctl's packages.
package textutil

// Truncate shortens s to at most max runes. A string that already fits is
// returned unchanged; otherwise it is cut to its leading runes followed by
// ellipsis (which may be empty), so the result -- ellipsis included -- is
// never longer than max runes. It operates on runes, never splitting a
// multi-byte character.
func Truncate(s string, max int, ellipsis string) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	ell := []rune(ellipsis)
	keep := max - len(ell)
	if keep < 0 {
		return string(ell[:max])
	}
	return string(runes[:keep]) + ellipsis
}
