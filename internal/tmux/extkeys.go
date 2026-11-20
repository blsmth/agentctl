package tmux

import (
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// extendedKeysArgs returns the tmux set-option calls that let a pane tell
// Shift+Enter, Ctrl+Enter and Enter apart, which pi needs (see pi's
// docs/tmux.md): extended-keys on, plus the CSI-u format on tmux 3.5+.
// Both are server options: tmux has no per-session form. "on" only takes
// effect for applications that ask for extended keys, so other programs are
// unaffected. version is `tmux -V` output; an unparseable one gets only the
// first call.
func extendedKeysArgs(version string) [][]string {
	calls := [][]string{{"set-option", "-s", "extended-keys", "on"}}
	if major, minor, ok := parseTmuxVersion(version); ok && (major > 3 || (major == 3 && minor >= 5)) {
		calls = append(calls, []string{"set-option", "-s", "extended-keys-format", "csi-u"})
	}
	return calls
}

var tmuxVersionRe = regexp.MustCompile(`(\d+)\.(\d+)`)

// parseTmuxVersion reads "tmux 3.6a" (or "tmux next-3.7") as 3, 6.
func parseTmuxVersion(v string) (major, minor int, ok bool) {
	m := tmuxVersionRe.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return 0, 0, false
	}
	major, err1 := strconv.Atoi(m[1])
	minor, err2 := strconv.Atoi(m[2])
	return major, minor, err1 == nil && err2 == nil
}

// applyExtendedKeys enables extended keys on the tmux server. Failures are
// ignored: an older tmux without the option still runs every agent.
func applyExtendedKeys() {
	out, _ := exec.Command("tmux", "-V").Output()
	for _, args := range extendedKeysArgs(string(out)) {
		_ = exec.Command("tmux", args...).Run()
	}
}

// hyperlinksArgs returns the call that lets OSC 8 hyperlinks through tmux
// to the outer terminal (the hub card's PR link), or nil when current, the
// `show-options -s terminal-features` output, already has the feature.
// terminal-features is a server-wide array, so appending unconditionally
// would add a duplicate entry on every apply. tmux reads it when a client
// attaches: clients already attached pick it up on their next attach.
// Any existing hyperlinks entry counts, even one scoped to a terminal
// pattern ("xterm*:hyperlinks"): a user who configured it is left alone.
func hyperlinksArgs(current string) []string {
	if strings.Contains(current, "hyperlinks") {
		return nil
	}
	return []string{"set-option", "-as", "terminal-features", ",*:hyperlinks"}
}

// applyHyperlinks enables the hyperlinks terminal feature on the tmux
// server. Failures are ignored, like applyExtendedKeys.
func applyHyperlinks() {
	out, err := exec.Command("tmux", "show-options", "-s", "terminal-features").Output()
	if err != nil {
		return
	}
	if args := hyperlinksArgs(string(out)); args != nil {
		_ = exec.Command("tmux", args...).Run()
	}
}
