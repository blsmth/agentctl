package hooks

import (
	"os"
	"strings"
)

// popupResultPath is where a preview popup records the operator's verdict,
// next to its args file.
func popupResultPath(argsFile string) string { return argsFile + ".result" }

// WritePopupResult records decision ("allow" or "deny") at path with mode
// 0600. An empty path is a no-op, so popups started without --result-file
// keep working.
func WritePopupResult(path, decision string) error {
	if path == "" {
		return nil
	}
	return os.WriteFile(path, []byte(decision), 0o600)
}

// readPopupResult returns "allow", "deny", or "" when the file is missing or
// holds anything else. Exit codes are never consulted: tmux display-popup
// exits 0 without running the command when another overlay is open.
func readPopupResult(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	switch v := strings.TrimSpace(string(b)); v {
	case "allow", "deny":
		return v
	}
	return ""
}
