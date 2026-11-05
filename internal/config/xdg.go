package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/blsmth/agentctl/internal/logging"
)

// xdgDirIn returns <$envVar>/agentctl when envVar holds an absolute path, else
// <home>/<defaultRel>/agentctl. Per the XDG spec, a relative value is invalid
// and ignored.
func xdgDirIn(envVar, home, defaultRel string) string {
	if v := os.Getenv(envVar); filepath.IsAbs(v) {
		return filepath.Join(v, "agentctl")
	}
	return filepath.Join(home, defaultRel, "agentctl")
}

// xdgDir is xdgDirIn for the current user. If the home directory cannot be
// resolved, it falls back to the XDG variable (when absolute) and otherwise
// returns an error.
func xdgDir(envVar, defaultRel string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		if v := os.Getenv(envVar); filepath.IsAbs(v) {
			return filepath.Join(v, "agentctl"), nil
		}
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return xdgDirIn(envVar, home, defaultRel), nil
}

// DataDirIn returns the agentctl data directory for the given home:
// $XDG_DATA_HOME/agentctl, else <home>/.local/share/agentctl.
func DataDirIn(home string) string {
	return xdgDirIn("XDG_DATA_HOME", home, filepath.Join(".local", "share"))
}

// DataDir returns the agentctl data directory (see DataDirIn). It errors only
// when the home directory is needed but cannot be resolved.
func DataDir() (string, error) {
	return xdgDir("XDG_DATA_HOME", filepath.Join(".local", "share"))
}

// StateDirIn returns the agentctl state directory for the given home:
// $XDG_STATE_HOME/agentctl, else <home>/.local/state/agentctl.
func StateDirIn(home string) string {
	return xdgDirIn("XDG_STATE_HOME", home, filepath.Join(".local", "state"))
}

// StateDir returns the agentctl state directory (see StateDirIn). It errors
// only when the home directory is needed but cannot be resolved.
func StateDir() (string, error) {
	return xdgDir("XDG_STATE_HOME", filepath.Join(".local", "state"))
}

// MigrateLegacy moves oldPath to newPath once, when newPath does not exist and
// oldPath does. It returns the path the caller should use: newPath normally,
// or oldPath when the move failed for a reason other than a lost race (for
// example a cross-device rename), so existing data is never orphaned.
// It is safe for concurrent processes: if the rename loses a race (newPath now
// exists, or oldPath vanished) that counts as success.
func MigrateLegacy(oldPath, newPath string) string {
	if oldPath == "" || oldPath == newPath {
		return newPath
	}
	if _, err := os.Lstat(newPath); err == nil {
		return newPath
	}
	if _, err := os.Lstat(oldPath); err != nil {
		return newPath
	}
	if err := os.MkdirAll(filepath.Dir(newPath), 0o700); err != nil {
		logging.Log("migrate: creating %s: %v; keeping %s", filepath.Dir(newPath), err, oldPath)
		return oldPath
	}
	err := os.Rename(oldPath, newPath)
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return newPath
	}
	if _, serr := os.Lstat(newPath); serr == nil {
		return newPath // lost a race to another process
	}
	logging.Log("migrate: moving %s to %s failed: %v; keeping %s", oldPath, newPath, err, oldPath)
	return oldPath
}
