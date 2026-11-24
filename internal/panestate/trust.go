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

// Trust dialog answers. A trust Dialog's options carry these keys instead of
// numbers: the phone names the choice and the server moves the cursor to its
// row (the current layout is unnumbered, so a number key means nothing).
const (
	// KindTrust is Dialog.Kind for the folder-trust dialog.
	KindTrust = "trust"
	// TrustKeyTrust accepts the folder; TrustKeyExit declines, which makes
	// Claude Code exit.
	TrustKeyTrust = "trust"
	TrustKeyExit  = "exit"
)

// trustExitOption is the decline row, alone on its line, selected or not,
// numbered or not.
var trustExitOption = regexp.MustCompile(`^(❯\s*)?(\d+\.\s+)?No, exit$`)

// ParseTrustDialog parses the folder-trust dialog into a Dialog with Kind
// KindTrust, the folder, and its two rows as Options in screen order (keys
// TrustKeyTrust and TrustKeyExit). Cursor is the index in Options of the row
// the ❯ cursor is on, -1 when neither shows it. Text runs from the
// "Accessing workspace:" heading to the last option row and leaves the
// cursor out, so moving the cursor keeps the Hash.
//
// It returns ok=false unless the dialog is the last thing on screen: after
// its option rows only blank lines and the "Enter to confirm" footer may
// follow. When Claude Code exits on "No, exit" the dialog stays in the pane
// above the exit message and the shell; that is not an answerable dialog.
func ParseTrustDialog(pane string) (Dialog, bool) {
	if !IsTrustDialog(pane) {
		return Dialog{}, false
	}
	lines := strings.Split(StripANSI(pane), "\n")
	last := -1
	for i := len(lines) - 1; i >= 0; i-- {
		l := dialogLine(lines[i])
		if trustOption.MatchString(l) || trustExitOption.MatchString(l) {
			last = i
			break
		}
		if l != "" && !dialogFooterLine(l) && !isRule(lines[i]) && strings.Trim(l, boxChars) != "" {
			return Dialog{}, false
		}
	}
	if last < 0 {
		return Dialog{}, false
	}
	// The option run: contiguous option rows ending at last.
	first := last
	for first > 0 {
		l := dialogLine(lines[first-1])
		if !trustOption.MatchString(l) && !trustExitOption.MatchString(l) {
			break
		}
		first--
	}
	d := Dialog{Kind: KindTrust, Folder: TrustFolder(pane), Cursor: -1}
	for _, raw := range lines[first : last+1] {
		l := dialogLine(raw)
		o := Option{Key: TrustKeyTrust, Label: "Yes, I trust this folder"}
		if trustExitOption.MatchString(l) {
			o = Option{Key: TrustKeyExit, Label: "No, exit"}
		}
		if strings.HasPrefix(l, "❯") {
			d.Cursor = len(d.Options)
		}
		d.Options = append(d.Options, o)
	}
	if !hasKeys(d.Options, TrustKeyTrust, TrustKeyExit) || len(d.Options) != 2 {
		return Dialog{}, false
	}
	start := first
	for i := first - 1; i >= 0; i-- {
		if dialogLine(lines[i]) == trustHeading {
			start = i
			break
		}
		if isRule(lines[i]) {
			start = i + 1
			break
		}
	}
	var kept []string
	for _, raw := range lines[start : last+1] {
		if l := cleanLine(raw); l != "" {
			kept = append(kept, l)
		}
	}
	d.Text = strings.Join(kept, "\n")
	d.Hash = HashDialogText(d.Text)
	return d, true
}

// TrustRow is the index in d.Options of the option with key, -1 when d has
// none.
func TrustRow(d Dialog, key string) int {
	for i, o := range d.Options {
		if o.Key == key {
			return i
		}
	}
	return -1
}

func hasKeys(opts []Option, keys ...string) bool {
	for _, k := range keys {
		found := false
		for _, o := range opts {
			found = found || o.Key == k
		}
		if !found {
			return false
		}
	}
	return true
}
