package shorttitle

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Title length caps applied by Sanitize.
const (
	MaxWords = 6
	MaxChars = 40
)

// labelPrefixes are lead-ins a model sometimes puts before the answer.
var labelPrefixes = []string{"title:", "short title:", "row title:"}

// edgeQuotes are stripped from both ends of the title and of each word;
// straight and curly double quotes are removed everywhere.
const edgeQuotes = "\"'`“”‘’«»"

// trailingPunct is removed from the end of the title.
const trailingPunct = ".,;:!?…-–—"

// Sanitize turns a model reply into a row title: the first non-empty line,
// with any "Title:" label, markdown, quotes and trailing punctuation removed,
// whitespace collapsed, and capped at MaxWords words and MaxChars characters.
// It returns "" when nothing title-like is left.
func Sanitize(s string) string {
	line := firstLine(s)
	line = strings.NewReplacer("**", "", "__", "", "`", "", "\"", "", "“", "", "”", "", "«", "", "»", "").Replace(line)
	line = strings.TrimLeft(line, "#*-•> \t")
	for _, p := range labelPrefixes {
		if len(line) >= len(p) && strings.EqualFold(line[:len(p)], p) {
			line = line[len(p):]
			break
		}
	}
	line = stripKindPrefix(line)
	line = strings.ReplaceAll(line, "|", " ")
	words := strings.Fields(line)
	out := make([]string, 0, len(words))
	for _, w := range words {
		w = strings.Trim(w, edgeQuotes+"*_")
		if w != "" {
			out = append(out, w)
		}
	}
	if len(out) > MaxWords {
		out = out[:MaxWords]
	}
	title := capChars(strings.Join(out, " "), MaxChars)
	title = strings.TrimRight(title, trailingPunct+edgeQuotes+" ")
	if !hasLetter(title) {
		return ""
	}
	return title
}

// stripKindPrefix drops a leading "<kind> |" so a reply that was not parsed
// as kind and title never leaks the kind into the title.
func stripKindPrefix(line string) string {
	if head, rest, ok := strings.Cut(line, "|"); ok && normalizeKind(head) != "" {
		return strings.TrimSpace(rest)
	}
	return line
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

// capChars cuts s to at most n runes, at a word boundary when there is one.
func capChars(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)[:n]
	cut := string(r)
	if i := strings.LastIndexByte(cut, ' '); i > 0 && []rune(s)[n] != ' ' {
		cut = cut[:i]
	}
	return strings.TrimSpace(cut)
}

func hasLetter(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}
