package tmux

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/safefs"
)

// The hub origin is the directory `agentctl hub` was last run from. The hub
// session's cwd is pinned to hubDir() and StartHubSession returns early when
// the hub already exists, so hub-view cannot learn it from its own process:
// `agentctl hub` records it on every invocation and the hub's "new session"
// prompt reads it as its default.

// hubOriginFile is the origin file's name in the agentctl state dir.
const hubOriginFile = "hub-origin"

// hubOriginPath is the origin file's path, resolved on every call so tests
// (and a changed $XDG_STATE_HOME) see the current state dir.
func hubOriginPath() (string, error) {
	base, err := config.StateDir()
	if err != nil {
		return "", fmt.Errorf("resolving state dir: %w", err)
	}
	return filepath.Join(base, hubOriginFile), nil
}

// hubOriginSkipped reports whether cwd must not become the hub origin: a
// relative path, the hub's own pinned directory (or anything under it), or a
// dispatch worktree (any path with a .worktrees element).
func hubOriginSkipped(cwd string) bool {
	if cwd == "" || !filepath.IsAbs(cwd) {
		return true
	}
	cwd = filepath.Clean(cwd)
	if slices.Contains(strings.Split(cwd, string(filepath.Separator)), ".worktrees") {
		return true
	}
	base, err := config.StateDir()
	if err != nil {
		return false
	}
	hub := filepath.Join(base, "hub")
	for _, c := range []string{cwd, evalOr(cwd)} {
		for _, h := range []string{hub, evalOr(hub)} {
			if c == h || strings.HasPrefix(c, h+string(filepath.Separator)) {
				return true
			}
		}
	}
	return false
}

// evalOr resolves symlinks in p, returning p unchanged when that fails.
func evalOr(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// RecordHubOrigin stores cwd as the hub origin (0600, written atomically).
// A cwd that hubOriginSkipped rejects leaves the previous origin in place.
func RecordHubOrigin(cwd string) error {
	if hubOriginSkipped(cwd) {
		return nil
	}
	path, err := hubOriginPath()
	if err != nil {
		return err
	}
	if err := safefs.ReplaceFile(path, []byte(filepath.Clean(cwd)+"\n"), 0o600); err != nil {
		return fmt.Errorf("recording hub origin: %w", err)
	}
	return nil
}

// HubOrigin returns the recorded hub origin. ok is false when the file is
// missing, unreadable, or does not name an absolute path to an existing
// directory; callers then fall back to $HOME.
func HubOrigin() (dir string, ok bool) {
	path, err := hubOriginPath()
	if err != nil {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	dir = strings.TrimSpace(string(data))
	if !filepath.IsAbs(dir) {
		return "", false
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return "", false
	}
	return filepath.Clean(dir), true
}

// CheckNewSessionDir rejects a directory the hub must not start a project
// in: a relative path, anything that is not an existing directory, the root
// directory, and $HOME itself (init would create ~/.claude/agents, which is
// the global agents directory).
func CheckNewSessionDir(dir string) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("not an absolute path: %s", dir)
	}
	fi, err := os.Stat(dir)
	switch {
	case os.IsNotExist(err):
		return fmt.Errorf("no such directory: %s", dir)
	case err != nil:
		return fmt.Errorf("checking %s: %w", dir, err)
	case !fi.IsDir():
		return fmt.Errorf("not a directory: %s", dir)
	}
	if SamePath(dir, string(filepath.Separator)) {
		return fmt.Errorf("refusing to start a project in /")
	}
	if home, err := os.UserHomeDir(); err == nil && SamePath(dir, home) {
		return fmt.Errorf("refusing to start a project in your home directory (%s)", dir)
	}
	return nil
}

// SamePath compares two paths after cleaning and resolving symlinks (each
// side falls back to its cleaned form when it cannot be resolved).
func SamePath(a, b string) bool {
	return evalOr(filepath.Clean(a)) == evalOr(filepath.Clean(b))
}
