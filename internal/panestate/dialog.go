package panestate

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// Option is one selectable answer in a permission dialog: the key to press
// ("1", "2", ...) and the label shown next to it.
type Option struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// Dialog is a parsed Claude Code permission dialog.
type Dialog struct {
	// Text is the normalized dialog: ANSI escapes, box-drawing borders and the
	// selection cursor removed, lines trimmed, blank lines dropped.
	Text string `json:"text"`
	// Hash is HashDialogText(Text). A client sends it back with an answer and
	// the server re-parses the live pane to prove the same dialog is still up.
	Hash    string   `json:"hash"`
	Options []Option `json:"options"`
}

var (
	ansiEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)
	optionLine = regexp.MustCompile(`^(\d+)\.\s+(\S.*)$`)
)

// boxChars are the border glyphs Claude Code draws around a dialog.
const boxChars = "│╭╮╰╯─┃┏┓┗┛━"

// StripANSI removes terminal escape sequences from captured pane text.
func StripANSI(s string) string { return ansiEscape.ReplaceAllString(s, "") }

// HashDialogText is the one hash function for dialog identity. PR4's answer
// endpoint must obtain hashes only through ParseDialog (or this function) so
// the value a phone was shown and the value the server recomputes agree.
func HashDialogText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// cleanLine strips box borders, the selection cursor and surrounding space.
func cleanLine(line string) string {
	line = strings.Trim(line, boxChars+" \t\r")
	line = strings.TrimPrefix(line, "❯")
	return strings.TrimSpace(line)
}

// maxDialogContext bounds how many non-blank lines above the options belong
// to the dialog when no separator rule bounds it (a dialog Claude Code draws
// always has one: the box top or a ─ rule).
const maxDialogContext = 14

// dialogStart returns the first line of the dialog body whose options start
// at line first. The body is everything above the options back to the
// nearest separator rule, however long: a long command must be part of the
// text (and so of the hash), or two commands that differ only in their early
// lines would be the same dialog. Only when no rule is found is the body cut
// at maxDialogContext non-blank lines, so pane history is never swept in.
func dialogStart(lines, cleaned []string, first int) int {
	for i := first - 1; i >= 0; i-- {
		if isRule(lines[i]) {
			return i + 1
		}
	}
	start, budget := first, maxDialogContext
	for i := first - 1; i >= 0 && budget > 0; i-- {
		start = i
		if cleaned[i] != "" {
			budget--
		}
	}
	return start
}

// ParseDialog extracts the permission dialog from the captured pane (ANSI is
// accepted). It finds the last run of numbered options starting at "1", takes
// the lines above it, back to the nearest separator rule, as the dialog body
// (see dialogStart), and returns ok=false when the pane has no such run.
func ParseDialog(pane string) (Dialog, bool) {
	lines := strings.Split(StripANSI(pane), "\n")
	cleaned := make([]string, len(lines))
	for i, l := range lines {
		cleaned[i] = cleanLine(l)
	}

	// Find the last option run: walk back to the last option line, then up to
	// the start of its contiguous (blank-tolerant) run.
	last := -1
	for i := len(cleaned) - 1; i >= 0; i-- {
		if optionLine.MatchString(cleaned[i]) {
			last = i
			break
		}
	}
	if last < 0 {
		return Dialog{}, false
	}
	first := last
	for i := last - 1; i >= 0; i-- {
		switch {
		case optionLine.MatchString(cleaned[i]):
			first = i
		case cleaned[i] == "":
			// tolerate blank lines inside the run
		default:
			i = -1
		}
	}
	var opts []Option
	for _, l := range cleaned[first : last+1] {
		if m := optionLine.FindStringSubmatch(l); m != nil {
			opts = append(opts, Option{Key: m[1], Label: strings.TrimSpace(m[2])})
		}
	}
	if len(opts) == 0 || opts[0].Key != "1" {
		return Dialog{}, false
	}

	start := dialogStart(lines, cleaned, first)
	var kept []string
	for _, l := range cleaned[start : last+1] {
		if l != "" {
			kept = append(kept, l)
		}
	}
	text := strings.Join(kept, "\n")
	return Dialog{Text: text, Hash: HashDialogText(text), Options: opts}, true
}

// isRule reports whether a raw line is a horizontal separator (a run of ─,
// optionally with a label like "── go-engineer ──"), which bounds the dialog
// above. Box top borders (╭───╮) count too.
func isRule(line string) bool {
	t := strings.TrimSpace(StripANSI(line))
	if t == "" {
		return false
	}
	return strings.Count(t, "─") >= 10 && !strings.ContainsAny(t, "│")
}
