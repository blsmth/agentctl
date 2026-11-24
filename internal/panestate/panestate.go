// Package panestate classifies captured Claude Code pane text. It is a leaf
// package (imports nothing of ours) so both internal/fleet and internal/tmux
// can share ONE dialog predicate -- two detectors that must agree about "is a
// permission dialog showing" would drift, and the drift is a silent
// auto-approval.
package panestate

import (
	"regexp"
	"strings"
)

// Verdict is what a pane's visible contents say the agent is doing, as
// opposed to what windowstate claims. When the two disagree the pane is right.
type Verdict int

const (
	// Unknown means the pane could not be read or matched no rule.
	Unknown Verdict = iota
	// AwaitingInput means a permission/confirmation dialog is on screen and
	// the agent cannot proceed until a human answers it.
	AwaitingInput
	// Busy means the agent is mid-turn.
	Busy
	// Idle means the agent finished its turn and is sitting at a prompt.
	Idle
)

// String returns a lowercase, stable name suitable for JSON and table output.
func (v Verdict) String() string {
	switch v {
	case AwaitingInput:
		return "awaiting-input"
	case Busy:
		return "busy"
	case Idle:
		return "idle"
	}
	return "unknown"
}

// dialogQuestions are the question lines Claude Code renders only when a
// permission or confirmation dialog is up and waiting on a human. Each counts
// only as the start of a line (after borders and indentation): the same words
// quoted mid-line in an answer ("the \"Do you want to proceed?\" dialog") are
// prose, not a dialog.
var dialogQuestions = []string{
	"Do you want to proceed?",
	"Do you want to create",
	"Do you want to make this edit",
}

// dialogFooterSegment is the dialog footer hint. Footers are hint segments
// joined by " · " ("Esc to cancel · Tab to amend", "Enter to select · ↑/↓ to
// navigate · Esc to cancel"), so the hint counts only as a whole segment of
// such a line.
const dialogFooterSegment = "Esc to cancel"

// yesOption and noOption are the first and decline rows of a numbered-choice
// dialog, matched against a whole cleaned line.
var (
	yesOption = regexp.MustCompile(`^❯\s*1\.\s+Yes\b`)
	noOption  = regexp.MustCompile(`^(❯\s*)?\d+\.\s+No\b`)
)

// busyMarkers are strings the footer renders only while a turn is in flight.
var busyMarkers = []string{
	"esc to interrupt",
	"tokens · esc to interrupt",
}

// busyTailLines bounds how far above the input box the spinner and
// background-hint checks look, so a spinner-like line echoed deep in
// scrollback does not count.
const busyTailLines = 15

// spinnerLine matches the current Claude Code status line: one non-ASCII
// spinner glyph, a word ending in an ellipsis, then " (" and an elapsed
// duration. Glyphs and verbs rotate, so neither is enumerated. The finished
// "✻ Worked for 4m 25s" line has no ellipsis and does not match.
var spinnerLine = regexp.MustCompile(`^[^\x00-\x7f] \S+… \(\d+[hms]`)

// backgroundHint is the whole line shown under a running Bash tool. Anchoring
// both ends keeps prose that quotes the hint from reading as busy.
var backgroundHint = regexp.MustCompile(`^\(.*to run in background\)$`)

// Classify derives a verdict from captured pane text. It must be fed the
// full visible pane: a bounded tail can cut the dialog text off and yield a
// false negative.
//
// Order matters: the dialog check comes before the busy check, because a
// window blocked on a permission prompt can still render a footer left over
// from the turn that triggered it.
//
// The "⏸" glyph is NOT a blocked signal: it appears in the persistent
// "⏸ manual mode on" footer of idle and working panes alike.
func Classify(text string) Verdict {
	if strings.TrimSpace(text) == "" {
		return Unknown
	}
	if DialogShowing(text) {
		return AwaitingInput
	}
	for _, m := range busyMarkers {
		if strings.Contains(text, m) {
			return Busy
		}
	}
	if spinnerShowing(text) {
		return Busy
	}
	if PromptCount(text) > 0 {
		return Idle
	}
	return Unknown
}

// DialogShowing reports whether a permission/confirmation dialog is visible.
//
// Matching is structural, line by line: a marker counts only where Claude
// Code draws it (a question starting its line, the footer hint as a whole
// " · " segment, numbered options starting their lines), never as a
// substring of prose. It still scans the whole pane, not a bottom-N tail, so
// a tall dialog whose question is far above the footer is not missed.
func DialogShowing(text string) bool {
	lines := strings.Split(StripANSI(text), "\n")
	yes, no := false, false
	for _, raw := range lines {
		line := dialogLine(raw)
		if line == "" {
			continue
		}
		if dialogQuestionLine(line) || dialogFooterLine(line) {
			return true
		}
		// A numbered-choice dialog that phrases its question some other way
		// still renders a selected first option and a decline option.
		yes = yes || yesOption.MatchString(line)
		no = no || noOption.MatchString(line)
	}
	if yes && no {
		return true
	}
	// The folder-trust dialog phrases its options without "1. Yes" (see
	// IsTrustDialog), so it gets its own structural match.
	return reviewPageShowing(text) || IsTrustDialog(text)
}

// dialogLine trims box borders and surrounding whitespace from a pane line,
// keeping the ❯ cursor (the option checks need it).
func dialogLine(line string) string {
	return strings.Trim(line, "│┃ \t\r")
}

func dialogQuestionLine(line string) bool {
	for _, q := range dialogQuestions {
		if strings.HasPrefix(line, q) {
			return true
		}
	}
	return false
}

func dialogFooterLine(line string) bool {
	for _, seg := range strings.Split(line, "·") {
		if strings.TrimSpace(seg) == dialogFooterSegment {
			return true
		}
	}
	return false
}

// PromptCount returns the number of "❯" prompt lines in the pane, tolerating
// a leading input-box border ("│ ❯").
//
// An idle pane has exactly one: the empty input box. A count above one means
// something has been typed or injected into the box and not submitted.
func PromptCount(text string) int {
	n := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimLeft(line, "│ \t"), "❯") {
			n++
		}
	}
	return n
}

// spinnerShowing reports whether the last busyTailLines non-empty lines above
// the input box contain the spinner status line or the background hint.
func spinnerShowing(text string) bool {
	lines := strings.Split(text, "\n")
	end := len(lines)
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(strings.TrimLeft(lines[i], "│ \t"), "❯") {
			end = i
			break
		}
	}
	seen := 0
	for i := end - 1; i >= 0 && seen < busyTailLines; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		seen++
		if spinnerLine.MatchString(line) || backgroundHint.MatchString(line) {
			return true
		}
	}
	return false
}

// reviewQuestion and reviewSubmit are the final page of a multi-question
// AskUserQuestion flow: the question line, then (blank lines allowed) its
// first choice.
const reviewQuestion = "Ready to submit your answers?"

var reviewCancel = regexp.MustCompile(`^(❯\s*)?2\.\s+Cancel$`)

var reviewSubmit = regexp.MustCompile(`^(❯\s*)?1\.\s+Submit answers$`)

// reviewPageShowing reports whether AskUserQuestion's review page is up. It
// has no "Esc to cancel" footer, so no marker matches it. The match is
// structural (the question alone on its line, then "1. Submit answers")
// because the phrases alone turn up in scrollback, in prose about this very
// page, and a pane misread as a dialog offers its numbered prose as answers.
func reviewPageShowing(text string) bool {
	lines := strings.Split(StripANSI(text), "\n")
	return reviewPageWithQuestion(lines) || reviewPageBottom(lines)
}

const (
	reviewHeading = "Review your answers"
	// reviewHeadingReach is how many non-blank lines above "1. Submit answers"
	// the heading may sit: two lines per answered question (up to four
	// questions, more when an answer wraps), plus slack.
	reviewHeadingReach = 16
	// reviewTailLines is how many non-blank lines may follow "2. Cancel"
	// (a footer hint) on the live page.
	reviewTailLines = 2
)

// reviewPageBottom matches the layout without the "Ready to submit" line: the
// heading alone on its line, then "1. Submit answers" and "2. Cancel" as the
// last choices of the pane. Anchoring at the bottom keeps scrollback and prose
// that quote these phrases from matching.
func reviewPageBottom(lines []string) bool {
	var tail []int // indexes of non-blank lines, in order
	for i, l := range lines {
		if strings.TrimSpace(l) != "" {
			tail = append(tail, i)
		}
	}
	for k := len(tail) - 2; k >= 0 && k >= len(tail)-2-reviewTailLines; k-- {
		if !reviewSubmit.MatchString(strings.TrimSpace(lines[tail[k]])) ||
			!reviewCancel.MatchString(strings.TrimSpace(lines[tail[k+1]])) {
			continue
		}
		for j := k - 1; j >= 0 && j >= k-reviewHeadingReach; j-- {
			if strings.TrimSpace(lines[tail[j]]) == reviewHeading {
				return true
			}
		}
		return false
	}
	return false
}

// reviewPageWithQuestion matches the older layout with the question line.
func reviewPageWithQuestion(lines []string) bool {
	for i, l := range lines {
		if strings.TrimSpace(l) != reviewQuestion {
			continue
		}
		for _, next := range lines[i+1:] {
			if t := strings.TrimSpace(next); t != "" {
				if reviewSubmit.MatchString(t) {
					return true
				}
				break
			}
		}
	}
	return false
}
