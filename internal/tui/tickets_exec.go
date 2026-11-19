package tui

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// tkExec runs an external program for the tickets browser (gh, tmux) and
// returns its stdout. A failure's error carries the first line of stderr, so
// callers can show it inline. Every gh/tmux/browser call the browser makes
// goes through this seam; TestMain replaces it so no test ever spawns one.
var tkExec = func(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if line := firstNonBlankLine(stderr.String()); line != "" {
			return out, fmt.Errorf("%s", line)
		}
		return out, err
	}
	return out, nil
}

// errExecDisabled is what the test stub of tkExec returns.
var errExecDisabled = errors.New("exec disabled in tests")

// firstLine returns the first non-blank line of s, trimmed.
func firstNonBlankLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}
