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
	// Multi is true for a multi-select AskUserQuestion: its options are
	// "[ ]"/"[x]" checkboxes, so pressing a number toggles one and submits
	// nothing. Only Esc is a safe remote answer.
	Multi bool `json:"multi,omitempty"`
	// Submit locates the row that confirms a multi-select question ("Next" or
	// "Submit", an unnumbered row under the options). Nil when the pane shows
	// no such row or no cursor, in which case the question cannot be submitted
	// remotely.
	Submit *SubmitRow `json:"submit,omitempty"`
	// Cursor is the ❯ cursor's row among a multi-select question's
	// navigable rows (the numbered options, then the confirm row), -1 when
	// none shows. The server checks it after each arrow press of a submit.
	// Zero for any other dialog.
	Cursor int `json:"-"`
}

// SubmitRow is the confirm row of a multi-select question.
type SubmitRow struct {
	Label string `json:"label"`
	// Moves is how many arrow presses take the cursor from where it is to the
	// row: positive = Down, negative = Up. Enter then confirms. Pressing an
	// option's number toggles it without moving the cursor.
	Moves int `json:"moves"`
}

// IsFreeText reports whether a multi-select option label is the free-text
// "Type something" row, which needs typing and is not answerable by a toggle.
func IsFreeText(label string) bool {
	return strings.HasPrefix(strings.TrimSpace(checkboxLabel.ReplaceAllString(label, "")), "Type something")
}

// Checked reports whether a multi-select option label shows a ticked box.
func Checked(label string) bool {
	return checkboxLabel.MatchString(label) && !strings.HasPrefix(label, "[ ]")
}

var (
	ansiEscape    = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)
	optionLine    = regexp.MustCompile(`^(\d+)\.\s+(\S.*)$`)
	checkboxLabel = regexp.MustCompile(`^\[[ xX✔✓]\]\s`)
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
	optIndent := indentOf(lines[last])
run:
	for i := last - 1; i >= 0; i-- {
		switch {
		case optionLine.MatchString(cleaned[i]):
			first = i
			if m := optionLine.FindStringSubmatch(cleaned[i]); m[1] == "1" {
				break run
			}
		case cleaned[i] == "":
			// blank lines and separator rules (AskUserQuestion draws one
			// above its trailing "Chat about this" row) are tolerated
		case indentOf(lines[i]) > optIndent:
			// an option's description or the "Submit" row: indented deeper
			// than the option labels, so part of the run
		default:
			break run
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

	multi := false
	for _, o := range opts {
		if checkboxLabel.MatchString(o.Label) {
			multi = true
		}
	}

	var submit *SubmitRow
	cursor := 0
	if multi {
		submit, cursor = findSubmitRow(lines, cleaned, first, last)
	}

	start := dialogStart(lines, cleaned, first)
	var kept []string
	for _, l := range cleaned[start : last+1] {
		if l != "" {
			kept = append(kept, l)
		}
	}
	text := strings.Join(kept, "\n")
	return Dialog{Text: text, Hash: HashDialogText(text), Options: opts, Multi: multi, Submit: submit, Cursor: cursor}, true
}

// findSubmitRow locates the confirm row of a multi-select question within
// lines[first..last] and the arrow presses from the ❯ cursor to it. The row
// is the unnumbered line after the last checkbox option reading exactly
// "Next" or "Submit" (a described last option's description is not it). Navigable rows are the
// numbered options plus that row, in screen order. Returns nil without a
// cursor or confirm row. cursor is the ❯ row (-1 when none), whether or not
// a confirm row was found.
func findSubmitRow(lines, cleaned []string, first, last int) (submit *SubmitRow, cursor int) {
	lastBox := -1
	for i := first; i <= last; i++ {
		if m := optionLine.FindStringSubmatch(cleaned[i]); m != nil && checkboxLabel.MatchString(m[2]) {
			lastBox = i
		}
	}
	if lastBox < 0 {
		return nil, -1
	}
	row, submitRow := -1, -1
	cursor = -1
	cursorFree := false
	label := ""
	for i := first; i <= last; i++ {
		isOpt := optionLine.MatchString(cleaned[i])
		isSubmit := !isOpt && submitRow < 0 && i > lastBox && (cleaned[i] == "Next" || cleaned[i] == "Submit")
		if !isOpt && !isSubmit {
			continue
		}
		row++
		if isSubmit {
			submitRow, label = row, cleaned[i]
		}
		if strings.HasPrefix(strings.TrimLeft(lines[i], boxChars+" \t"), "❯") {
			cursor = row
			if m := optionLine.FindStringSubmatch(cleaned[i]); m != nil {
				cursorFree = IsFreeText(m[2])
			}
		}
	}
	// With the cursor on the free-text row, digits would be typed into its
	// field instead of toggling boxes.
	if submitRow < 0 || cursor < 0 || cursorFree {
		return nil, cursor
	}
	return &SubmitRow{Label: label, Moves: submitRow - cursor}, cursor
}

// checkboxMark is a checkbox at the start of an option line of Text.
var checkboxMark = regexp.MustCompile(`(?m)^(\d+\.\s+)\[[ xX✔✓]\]`)

// QuestionKey identifies the question a dialog asks regardless of which of
// its boxes are ticked: toggling a multi-select option changes the dialog's
// Hash (the boxes are part of Text) but not its QuestionKey. Tab header
// marks (☐/☒) are normalized too, since ticking a box marks the tab
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

// indentOf is the display column of a raw line's content, after any box
// border, with the selection cursor counted as a space so "❯ 1." and "  2."
// share an indent.
func indentOf(line string) int {
	t := StripANSI(line)
	if b := strings.TrimLeft(t, " \t"); strings.HasPrefix(b, "│") || strings.HasPrefix(b, "┃") {
		t = strings.TrimPrefix(strings.TrimPrefix(b, "│"), "┃")
	}
	n := 0
	for _, r := range t {
		if r != ' ' && r != '\t' && r != '❯' {
			break
		}
		n++
	}
	return n
}
