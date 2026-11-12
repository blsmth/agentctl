// Package panestate classifies captured Claude Code pane text. It is a leaf
// package (imports nothing of ours) so both internal/herd and internal/tmux
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

// dialogMarkers are strings Claude Code renders only when a permission or
// confirmation dialog is up and waiting on a human.
var dialogMarkers = []string{
	"Do you want to proceed?",
	"Do you want to create",
	"Do you want to make this edit",
	"Esc to cancel",
}

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

// backgroundHint appears under a running Bash tool.
const backgroundHint = "to run in background)"

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
func DialogShowing(text string) bool {
	for _, m := range dialogMarkers {
		if strings.Contains(text, m) {
			return true
		}
	}
	// A numbered-choice dialog that phrases its question some other way still
	// renders a selected first option and a decline option.
	return strings.Contains(text, "❯ 1. Yes") && strings.Contains(text, ". No")
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
		if spinnerLine.MatchString(line) || strings.Contains(line, backgroundHint) {
			return true
		}
	}
	return false
}
