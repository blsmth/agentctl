// Package sessionname persists the claude `-n` session name per Claude
// session ID, so resuming a session (including a forked resume) can reuse its
// original name. windowstate cannot serve this: a window's record is removed
// when the window closes, which is exactly when a session gets resumed.
package sessionname

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/blsmth/agentctl/internal/paths"
)

func file(sessionID string) string {
	dir := paths.RuntimeDir()
	if dir == "" || sessionID == "" || strings.ContainsAny(sessionID, "/\\") || strings.Contains(sessionID, "..") {
		return ""
	}
	return filepath.Join(dir, "session-names", sessionID)
}

// Record stores name for sessionID. Best-effort: a no-op without a runtime
// dir, session ID or name.
func Record(sessionID, name string) error {
	p := file(sessionID)
	if p == "" || name == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(name), 0o600)
}

// Lookup returns the recorded name for sessionID, or "" when unknown (legacy
// sessions launched before names were recorded).
func Lookup(sessionID string) string {
	p := file(sessionID)
	if p == "" {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
