// Package textutil holds small, dependency-free string helpers shared across
// agentctl's packages.
package textutil

import (
	"regexp"
	"strings"
)

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

// TaskLine summarises a markdown prompt as one short line: the first line
// that is not blank, a heading, a rule, front matter or a code fence, with
// list/quote markers, emphasis, inline code and link targets stripped,
// whitespace collapsed, and the result truncated to max runes with "…".
// Lines inside a fenced code block are skipped. Returns "" when no line
// qualifies.
func TaskLine(prompt string, max int) string {
	inFence := false
	for _, raw := range strings.Split(prompt, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence || line == "" || strings.HasPrefix(line, "#") || isRule(line) || strings.HasPrefix(line, "<!--") {
			continue
		}
		if s := stripMarkdown(line); s != "" {
			return Truncate(s, max, "…")
		}
	}
	return ""
}

// isRule reports whether line is a markdown thematic break or front-matter
// delimiter ("---", "***", "___", "===").
func isRule(line string) bool {
	if len(line) < 3 {
		return false
	}
	c := line[0]
	if c != '-' && c != '*' && c != '_' && c != '=' {
		return false
	}
	return strings.Trim(line, string(c)+" ") == ""
}

var (
	listMarker = regexp.MustCompile(`^(?:[-*+>]\s+|\d+[.)]\s+|\[[ xX]\]\s+)+`)
	mdLink     = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
	mdEmphasis = strings.NewReplacer("**", "", "__", "", "`", "")
)

// stripMarkdown removes inline markdown from one line.
func stripMarkdown(line string) string {
	line = listMarker.ReplaceAllString(line, "")
	line = mdLink.ReplaceAllString(line, "$1")
	line = mdEmphasis.Replace(line)
	return strings.Join(strings.Fields(line), " ")
}
