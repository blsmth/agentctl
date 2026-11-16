// Package approval tracks pending human-approval popups with marker files in
// the runtime dir, so other processes (the monitor) can avoid stealing focus
// while a human is deciding.
package approval

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// StaleAfter is how old a marker may be before it is ignored. It exceeds the
// popup deadline (9m) slightly so a crashed hook cannot suppress auto-open
// forever.
const StaleAfter = 10 * time.Minute

func dir(runtimeDir string) string { return filepath.Join(runtimeDir, "approval-pending") }

// Mark creates the marker for windowID and returns a function that removes
// it. With an empty runtimeDir or windowID it is a no-op.
func Mark(runtimeDir, windowID string) (clear func()) {
	if runtimeDir == "" || windowID == "" || strings.ContainsAny(windowID, `/\`) {
		return func() {}
	}
	d := dir(runtimeDir)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return func() {}
	}
	p := filepath.Join(d, windowID)
	if err := os.WriteFile(p, []byte(time.Now().Format(time.RFC3339)), 0o600); err != nil {
		return func() {}
	}
	return func() { _ = os.Remove(p) }
}

// Pending reports whether any marker younger than StaleAfter exists.
func Pending(runtimeDir string, now time.Time) bool {
	if runtimeDir == "" {
		return false
	}
	entries, err := os.ReadDir(dir(runtimeDir))
	if err != nil {
		return false
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) < StaleAfter {
			return true
		}
	}
	return false
}
