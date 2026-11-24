// Package attention decides, from an agent's final assistant text, whether
// the turn ended asking the human something. It is a pure heuristic (no LLM
// call) behind the "question" reason of the ! needs-attention window state;
// see windowstate.Attention.
package attention

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// MaxDetailRunes caps the detail Question returns.
const MaxDetailRunes = 120

// verdictRe matches a verdict that hands the decision to the human.
// BLOCKED is matched in capitals only, so "Blocked: none" in a status list
// does not count.
var verdictRe = regexp.MustCompile(`(?i:\bNEEDS[- _]BRENDAN\b)|\bVERDICT:\s*BLOCKED\b|^\W*BLOCKED\b`)

// askPrefixes start a sentence that asks the human for a decision even
// without a question mark ("Should I open the PR.", "Which one").
var askPrefixes = []string{
	"should i ", "shall i ", "do you want", "would you like", "want me to ",
	"which ", "can you confirm", "could you confirm", "please confirm",
	"can you check", "could you check", "do you prefer", "are you ok with",
	"are you okay with", "is it ok", "is it okay", "ok to ", "okay to ",
}

// askPhrases ask the human anywhere in a sentence.
var askPhrases = []string{
	"do you want me to", "would you like me to",
	"want me to go ahead", "your call", "waiting on you", "waiting for you",
	"waiting for your", "need your input", "need your decision",
	"need you to", "needs your", "up to you",
}

// Question reports whether text, the final assistant message of a turn, asks
// the human something, and returns the sentence that does (one line, at most
// MaxDetailRunes). It fires on:
//   - a NEEDS-BRENDAN verdict, a "VERDICT: BLOCKED" or a line starting with
//     BLOCKED, anywhere in the text;
//   - a last paragraph whose last sentence ends with "?";
//   - a sentence in the last paragraph that is a direct ask ("should I",
//     "do you want", "which ...", "let me know").
//
// Fenced code blocks are ignored, so a "?" in code or a shell snippet does
// not count.
func Question(text string) (string, bool) {
	text = stripFences(text)
	for _, line := range strings.Split(text, "\n") {
		if verdictRe.MatchString(strings.TrimSpace(line)) {
			return detail(line), true
		}
	}
	para := lastParagraph(text)
	if para == "" {
		return "", false
	}
	sentences := splitSentences(para)
	if len(sentences) == 0 {
		return "", false
	}
	last := sentences[len(sentences)-1]
	if strings.HasSuffix(trimDecoration(last), "?") {
		return detail(last), true
	}
	for _, s := range sentences {
		if isAsk(s) {
			return detail(s), true
		}
	}
	return "", false
}

// isAsk reports whether sentence s directly asks the human for something.
// "Let me know which ..." asks; the courtesy "Let me know if you want any
// changes" that ends many finished turns does not.
func isAsk(s string) bool {
	l := strings.ToLower(strings.TrimLeft(trimDecoration(s), "-*>#0123456789. ("))
	if i := strings.Index(l, "let me know"); i >= 0 && !strings.HasPrefix(l[i+len("let me know"):], " if") {
		return true
	}
	for _, p := range askPrefixes {
		if strings.HasPrefix(l, p) {
			return true
		}
	}
	for _, p := range askPhrases {
		if strings.Contains(l, p) {
			return true
		}
	}
	return false
}

// stripFences removes fenced code blocks (``` or ~~~), keeping the prose.
func stripFences(text string) string {
	var b strings.Builder
	in := false
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			in = !in
			continue
		}
		if !in {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// lastParagraph is the last non-blank block of text separated by blank lines.
func lastParagraph(text string) string {
	blocks := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n\n")
	for i := len(blocks) - 1; i >= 0; i-- {
		if b := strings.TrimSpace(blocks[i]); b != "" && !isRule(b) {
			return b
		}
	}
	return ""
}

// isRule reports a Markdown horizontal rule.
func isRule(s string) bool {
	return strings.Trim(s, "-*_ ") == ""
}

// splitSentences splits a paragraph into sentences at ". ", "? ", "! " and
// line breaks. It is rough on purpose: abbreviations split early, which only
// matters for which sentence is reported.
func splitSentences(p string) []string {
	var out []string
	for _, line := range strings.Split(p, "\n") {
		start := 0
		for i := 0; i < len(line); i++ {
			c := line[i]
			if (c == '.' || c == '?' || c == '!') && (i+1 == len(line) || line[i+1] == ' ') {
				if s := strings.TrimSpace(line[start : i+1]); s != "" {
					out = append(out, s)
				}
				start = i + 1
			}
		}
		if s := strings.TrimSpace(line[start:]); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// trimDecoration drops trailing Markdown emphasis, quotes and brackets so
// "**Merge it?**" ends with "?".
func trimDecoration(s string) string {
	return strings.TrimRight(strings.TrimSpace(s), "*_`'\")] ")
}

// detail is s as one trimmed line of at most MaxDetailRunes.
func detail(s string) string {
	s = strings.ReplaceAll(s, "**", "")
	s = strings.Join(strings.Fields(strings.Trim(strings.TrimSpace(s), "*_#>- ")), " ")
	if utf8.RuneCountInString(s) <= MaxDetailRunes {
		return s
	}
	r := []rune(s)
	return string(r[:MaxDetailRunes-1]) + "…"
}
