package panestate

import (
	"regexp"
	"strings"
)

// Claude Code asks whether to trust a folder the first time it starts in it,
// before it loads any settings or hooks, so no hook can report the window as
// blocked. The dialog (Claude Code 2.1.3xx):
//
//	Accessing workspace:
//
//	/path/to/the/folder
//
//	Quick safety check: Is this a project you created or one you trust? ...
//	...
//	❯ No, exit
//	  Yes, I trust this folder
//
//	Enter to confirm · Esc to cancel
//
// Older builds number the options ("❯ 1. Yes, I trust this folder") and may
// start the question at the line start.
const (
	trustHeading  = "Accessing workspace:"
	trustQuestion = "Is this a project you created or one you trust?"
	// trustQuestionLead is the prefix the current build puts before the
	// question on the same line.
	trustQuestionLead = "Quick safety check: "
)

// trustOption is the accept row, alone on its line, selected or not,
// numbered or not.
var trustOption = regexp.MustCompile(`^(❯\s*)?(\d+\.\s+)?Yes, I trust this folder$`)

// IsTrustDialog reports whether Claude Code's folder-trust dialog is on
// screen. The match is structural, like DialogShowing: the accept option row
// alone on its line, plus the "Accessing workspace:" heading or the question
// starting its line (after the "Quick safety check:" lead). Prose quoting the
// dialog mid-line does not match.
func IsTrustDialog(text string) bool {
	option, context := false, false
	for _, raw := range strings.Split(StripANSI(text), "\n") {
		line := dialogLine(raw)
		if line == "" {
			continue
		}
		switch {
		case trustOption.MatchString(line):
			option = true
		case line == trustHeading, trustQuestionLine(line):
			context = true
		}
	}
	return option && context
}

func trustQuestionLine(line string) bool {
	return strings.HasPrefix(strings.TrimPrefix(line, trustQuestionLead), trustQuestion)
}

// TrustFolder returns the folder path the trust dialog names: the non-blank
// lines after the "Accessing workspace:" heading up to the next blank line,
// joined without separators (a long path wraps mid-word at the pane width).
// It returns "" when no trust dialog is showing or the heading is missing.
func TrustFolder(text string) string {
	if !IsTrustDialog(text) {
		return ""
	}
	lines := strings.Split(StripANSI(text), "\n")
	for i, raw := range lines {
		if dialogLine(raw) != trustHeading {
			continue
		}
		var b strings.Builder
		for _, next := range lines[i+1:] {
			l := dialogLine(next)
			if l == "" {
				if b.Len() > 0 {
					break
				}
				continue
			}
			b.WriteString(l)
		}
		return b.String()
	}
	return ""
}
