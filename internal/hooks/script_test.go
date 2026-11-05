package hooks

import (
	"strings"
	"testing"
)

// TestShellPrefixScriptUsesEval is a regression test for the shell-prefix wrapper rewrite.
// It verifies that the embedded shell-prefix.sh calls the command with
// eval "$@" — not a bare "$@" — so that the single-string argument Claude Code
// passes is correctly interpreted by the shell rather than exec'd as a binary.
func TestShellPrefixScriptUsesEval(t *testing.T) {
	content := shellPrefixScript

	// Must contain eval "$@"
	if !strings.Contains(content, `eval "$@"`) {
		t.Errorf("shell-prefix.sh does not contain 'eval \"$@\"'; content:\n%s", content)
	}

	// Regression guard: must NOT contain a bare "$@" line (the old broken form).
	// We check line-by-line to avoid false positives on comments or the eval line.
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == `"$@"` {
			t.Errorf("shell-prefix.sh contains a bare \"$@\" line (should be 'eval \"$@\"'): %q", line)
		}
	}
}
