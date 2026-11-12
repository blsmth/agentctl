// Package winlink wraps tmux window IDs ("@65") in OSC 8 terminal hyperlinks
// so a human can Cmd-click one in agentctl output and jump to that window.
//
// The link target is agentctl://window/@<digits>. Resolving that URL is the
// job of an OS-level URL handler outside this repo (it runs tmux
// switch-client/select-window); this package only emits the escape sequence.
//
// Links are emitted only for humans: stdout must be a terminal, NO_COLOR must
// be unset, and TERM must not be "dumb". Callers are responsible for not
// linking in --json mode — JSON output never goes through this package.
package winlink

import (
	"os"
	"regexp"

	"golang.org/x/term"
)

// Scheme is the URL prefix every window link starts with.
const Scheme = "agentctl://window/"

// OSC 8 framing, terminated with ST (ESC \) rather than BEL.
const (
	oscOpen  = "\x1b]8;;"
	oscST    = "\x1b\\"
	oscReset = oscOpen + oscST
)

var windowIDRe = regexp.MustCompile(`^@[0-9]+$`)

// stdoutIsTerminal is a variable so tests can force either answer.
var stdoutIsTerminal = func() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
}

// Enabled reports whether window links should be emitted on stdout.
func Enabled() bool {
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	return stdoutIsTerminal()
}

// URL returns the link target for windowID, or "" when windowID is not a
// tmux window ID of the form "@<digits>".
func URL(windowID string) string {
	if !windowIDRe.MatchString(windowID) {
		return ""
	}
	return Scheme + windowID
}

// Wrap unconditionally wraps visible in an OSC 8 hyperlink to windowID.
// visible may itself contain SGR styling. If windowID is not a valid window
// ID, visible is returned unchanged.
func Wrap(windowID, visible string) string {
	u := URL(windowID)
	if u == "" {
		return visible
	}
	return oscOpen + u + oscST + visible + oscReset
}

// Text wraps visible in a link to windowID when Enabled, and otherwise
// returns visible unchanged. Use it when the visible text is already styled.
func Text(windowID, visible string) string {
	if !Enabled() {
		return visible
	}
	return Wrap(windowID, visible)
}

// ID is Text(windowID, windowID): the window ID, linked to itself.
func ID(windowID string) string {
	return Text(windowID, windowID)
}
