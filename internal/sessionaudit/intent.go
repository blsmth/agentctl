package sessionaudit

import (
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/logstream"
	"github.com/blsmth/agentctl/internal/race"
)

// humanPrompt is one prompt a person typed in the main conversation.
type humanPrompt struct {
	at   time.Time
	text string
}

// humanPrompts lists the person's prompts in transcript order.
func humanPrompts(entries []logstream.Entry) []humanPrompt {
	var out []humanPrompt
	for _, e := range entries {
		if !race.IsPrompt(e) || e.Timestamp.IsZero() {
			continue
		}
		if meta, _ := e.Raw["isMeta"].(bool); meta {
			continue
		}
		if t := promptText(e.Raw); t != "" {
			out = append(out, humanPrompt{at: e.Timestamp, text: t})
		}
	}
	return out
}

func promptText(raw map[string]any) string {
	msg, _ := raw["message"].(map[string]any)
	switch c := msg["content"].(type) {
	case string:
		return strings.TrimSpace(c)
	case []any:
		var parts []string
		for _, v := range c {
			if b, ok := v.(map[string]any); ok && b["type"] == "text" {
				if s, _ := b["text"].(string); strings.TrimSpace(s) != "" {
					parts = append(parts, strings.TrimSpace(s))
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// promptBefore is the most recent prompt at or before t.
func promptBefore(ps []humanPrompt, t time.Time) string {
	text := ""
	for _, p := range ps {
		if p.at.After(t) {
			break
		}
		text = p.text
	}
	return text
}

var (
	deleteVerb = regexp.MustCompile(`(?i)\b(delet\w*|remov\w*|clean\w*|wip\w*)\b`)
	wordSplit  = regexp.MustCompile(`[^a-z0-9]+`)
	sentenceAt = regexp.MustCompile(`[.!?\n]`)
)

// genericWords are too common to show that a prompt named a path.
var genericWords = map[string]bool{
	"the": true, "dir": true, "directory": true, "folder": true, "file": true, "files": true,
	"repo": true, "repos": true, "tmp": true, "home": true, "users": true, "user": true,
	"old": true, "new": true, "all": true, "and": true, "src": true, "bin": true, "lib": true,
}

// requestedRemoval reports whether prompt explicitly asks for the removal of
// every operand: it has a delete/remove/clean/wipe verb, and each operand's
// basename shares a word with the prompt. The excerpt (at most 80 runes) is
// the part of the prompt that asks. Conservative: any operand it cannot
// judge, or that the prompt does not name, means no.
func requestedRemoval(prompt string, operands []string) (excerpt string, ok bool) {
	loc := deleteVerb.FindStringIndex(prompt)
	if loc == nil || len(operands) == 0 {
		return "", false
	}
	words := map[string]bool{}
	for _, w := range wordSplit.Split(strings.ToLower(prompt), -1) {
		words[w] = true
	}
	for _, op := range operands {
		if !namedIn(op, words) {
			return "", false
		}
	}
	start := 0
	for _, m := range sentenceAt.FindAllStringIndex(prompt[:loc[0]], -1) {
		start = m[1]
	}
	return excerpt80(prompt[start:]), true
}

// namedIn reports whether a path operand's basename shares a word with words.
func namedIn(op string, words map[string]bool) bool {
	if strings.Contains(op, dynamic) {
		return false
	}
	base := filepath.Base(strings.TrimRight(op, "/*"))
	if base == "." || base == ".." || base == "/" || base == "~" || strings.HasPrefix(base, "$") {
		return false
	}
	for _, w := range wordSplit.Split(strings.ToLower(base), -1) {
		if len(w) >= 3 && !genericWords[w] && words[w] {
			return true
		}
	}
	return false
}

// excerpt80 collapses whitespace and cuts s to 80 runes.
func excerpt80(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > 80 {
		return string(r[:79]) + "…"
	}
	return s
}
