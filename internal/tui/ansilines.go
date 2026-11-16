package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

const sgrReset = "\x1b[0m"

// normalizeANSILines makes every line of s self-contained with respect to SGR
// styling: a line that begins while a style is active re-emits that style, and
// any line that carries or inherits styling ends with a reset. tmux
// capture-pane -e only emits attribute changes, so without this, styles bleed
// across lines (into the box border, the next prefix) and a line sliced off
// from the line that opened its style loses its color. Plain lines pass
// through unchanged.
func normalizeANSILines(s string) string {
	if !strings.Contains(s, "\x1b") {
		return s
	}
	lines := strings.Split(s, "\n")
	var active []string // SGR sequences applied since the last reset
	for i, line := range lines {
		prefix := strings.Join(active, "")
		styled := len(active) > 0
		p := ansi.NewParser()
		state := byte(0)
		for rest := line; rest != ""; {
			seq, _, n, ns := ansi.DecodeSequence(rest, state, p)
			state = ns
			rest = rest[n:]
			if params, ok := sgrParams(seq); ok {
				styled = true
				active = applySGR(active, seq, params)
			}
		}
		if styled {
			line = prefix + line + sgrReset
		}
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}

// sgrParams returns the parameter string of an SGR sequence (CSI ... m).
func sgrParams(seq string) (string, bool) {
	if !strings.HasPrefix(seq, "\x1b[") || !strings.HasSuffix(seq, "m") {
		return "", false
	}
	params := seq[2 : len(seq)-1]
	for _, r := range params {
		if (r < '0' || r > '9') && r != ';' && r != ':' {
			return "", false // private/intermediate form, not plain SGR
		}
	}
	return params, true
}

// applySGR folds one SGR sequence into the active set. A leading 0 (or an
// empty parameter list) resets; any parameters after it start a new set.
func applySGR(active []string, seq, params string) []string {
	if params == "" || params == "0" {
		return nil
	}
	first, rest, found := strings.Cut(params, ";")
	if first == "" || first == "0" || first == "00" {
		active = nil
		if !found || rest == "" {
			return nil
		}
		return append(active, "\x1b["+rest+"m")
	}
	return append(active, seq)
}
