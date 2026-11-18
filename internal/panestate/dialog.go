package panestate

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"slices"
	"strings"
)

// Option is one selectable answer in a dialog: the key to press ("1", "2",
// ...) and the label shown next to it.
type Option struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// Dialog kinds. A permission dialog has no kind.
const (
	// KindQuestion is a single-select AskUserQuestion tab: a digit picks the
	// option and moves on (to the next tab, or submits a lone question).
	KindQuestion = "question"
	// KindMulti is a multi-select AskUserQuestion tab: a digit toggles the
	// option's checkbox and Tab moves on.
	KindMulti = "multi"
)

// Dialog is a parsed Claude Code dialog: a permission prompt or an
// AskUserQuestion tab.
type Dialog struct {
	// Text is the normalized dialog: ANSI escapes, box-drawing borders and the
	// selection cursor removed, lines trimmed, blank lines dropped.
	Text string `json:"text"`
	// Hash is HashDialogText(Text). A client sends it back with an answer and
	// the server re-parses the live pane to prove the same dialog is still up.
	Hash string `json:"hash"`
	// Kind is "" for a permission dialog, else KindQuestion or KindMulti.
	Kind string `json:"kind,omitempty"`
	// Options are the answers a key press gives. For an AskUserQuestion tab
	// the free-text entries ("Type something", "Chat about this") are left
	// out: they need typing in the terminal. They stay in Text.
	Options []Option `json:"options"`
	// Checked are the keys of a multi-select tab's ticked options ("[✔]").
	Checked []string `json:"checked,omitempty"`
}

var (
	ansiEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)
	optionLine = regexp.MustCompile(`^(\d+)\.\s+(\S.*)$`)
	// checkbox is a multi-select option's "[ ] " / "[✔] " label prefix.
	checkbox = regexp.MustCompile(`^\[(.)\]\s*(.*)$`)
	// checkboxMark is checkbox anywhere in a normalized option line.
	checkboxMark = regexp.MustCompile(`(?m)^(\d+\.\s+)\[.\]`)
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

// ParseDialog extracts the dialog from the captured pane (ANSI is accepted).
// It finds the last run of numbered options starting at "1" (see optionRun),
// takes the lines above it, back to the nearest separator rule, as the dialog
// body (see dialogStart), and returns ok=false when the pane has no such run.
func ParseDialog(pane string) (Dialog, bool) {
	lines := strings.Split(StripANSI(pane), "\n")
	cleaned := make([]string, len(lines))
	for i, l := range lines {
		cleaned[i] = cleanLine(l)
	}
	first, last, ok := optionRun(lines, cleaned)
	if !ok {
		return Dialog{}, false
	}
	var opts []Option
	for i := first; i <= last; i++ {
		if m := optionLine.FindStringSubmatch(cleaned[i]); m != nil {
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
	d := Dialog{Text: text, Hash: HashDialogText(text), Options: opts}
	if isQuestion(opts, cleaned[last+1:]) {
		d.Kind, d.Options, d.Checked = questionOptions(opts)
	}
	return d, true
}

// optionRun finds the last run of numbered options: the last option line,
// then up through option lines, blank lines, and, inside the run, indented
// continuation lines (an AskUserQuestion option's description, a wrapped
// label) and separator rules (AskUserQuestion draws one above "Chat about
// this"). It stops above option "1" or at any other line. first and last are
// the run's first and last option lines.
func optionRun(lines, cleaned []string) (first, last int, ok bool) {
	last = -1
	for i := len(cleaned) - 1; i >= 0; i-- {
		if optionLine.MatchString(cleaned[i]) {
			last = i
			break
		}
	}
	if last < 0 {
		return 0, 0, false
	}
	first, col := last, optionColumn(lines[last])
	for i := last - 1; i >= 0; i-- {
		if strings.HasPrefix(cleaned[first], "1.") {
			break
		}
		switch {
		case optionLine.MatchString(cleaned[i]):
			first = i
		case cleaned[i] == "" || isRule(lines[i]):
			// blank lines and rules inside the run
		case optionColumn(lines[i]) > col:
			// a continuation of the option above
		default:
			i = -1
		}
	}
	return first, last, true
}

// optionColumn is the column (in runes) of a line's first character other
// than a box border, the selection cursor or space: an option line's number,
// a description line's first letter.
func optionColumn(line string) int {
	line = strings.TrimLeft(line, "│┃")
	for i, r := range []rune(line) {
		if r != ' ' && r != '\t' && r != '❯' {
			return i
		}
	}
	return len(line)
}

// freeTextLabels are the AskUserQuestion entries that open a text input.
var freeTextLabels = []string{"Type something", "Type something.", "Chat about this"}

// isQuestion reports whether an option run is an AskUserQuestion tab: it
// offers "Chat about this" or its footer reads "Enter to select".
func isQuestion(opts []Option, below []string) bool {
	for _, o := range opts {
		if o.Label == "Chat about this" {
			return true
		}
	}
	for _, l := range below {
		if strings.HasPrefix(l, "Enter to select") {
			return true
		}
	}
	return false
}

// questionOptions drops the free-text entries of an AskUserQuestion tab and
// splits checkboxes off the labels; any checkbox makes it multi-select.
func questionOptions(opts []Option) (kind string, out []Option, checked []string) {
	kind = KindQuestion
	for _, o := range opts {
		ticked := false
		if m := checkbox.FindStringSubmatch(o.Label); m != nil {
			kind = KindMulti
			o.Label, ticked = strings.TrimSpace(m[2]), m[1] != " "
		}
		if slices.Contains(freeTextLabels, o.Label) {
			continue
		}
		out = append(out, o)
		if ticked {
			checked = append(checked, o.Key)
		}
	}
	return kind, out, checked
}

// IsChecked reports whether key is a ticked option of a multi-select tab.
func (d Dialog) IsChecked(key string) bool { return slices.Contains(d.Checked, key) }

// QuestionKey identifies the question a dialog asks regardless of which of
// its checkboxes are ticked: toggling a multi-select option changes the
// dialog's Hash (the boxes are part of Text) but not its QuestionKey. Tab
// header marks (☐/☒) are normalized too, since ticking a box marks the tab
// answered.
func QuestionKey(d Dialog) string {
	t := checkboxMark.ReplaceAllString(d.Text, "$1[ ]")
	return HashDialogText(strings.ReplaceAll(t, "☒", "☐"))
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
