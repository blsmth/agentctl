// Package phoneview records which tmux windows the phone web app (agentctl
// serve) currently has a live terminal open on, so the statusline can pick a
// phone-friendly format. serve writes one marker file per connection under
// <dir>/<windowID>/<viewer session>; the statusline stats the window's
// directory on its hot path. Window IDs are unique per tmux server and serve is
// per machine, so the directory lives in the machine-wide state dir.
package phoneview

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/blsmth/agentctl/internal/config"
)

// dirName is the subdirectory of the state dir that holds the markers.
const dirName = "phone-viewed"

var (
	windowRe = regexp.MustCompile(`^@[0-9]+$`)
	viewerRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

// DefaultDir returns <StateDir>/phone-viewed.
func DefaultDir() (string, error) {
	state, err := config.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(state, dirName), nil
}

func windowDir(dir, windowID string) (string, error) {
	if dir == "" || !windowRe.MatchString(windowID) {
		return "", fmt.Errorf("invalid phone-view marker target %q", windowID)
	}
	return filepath.Join(dir, windowID), nil
}

// Add records that viewer shows windowID to a phone.
func Add(dir, windowID, viewer string) error {
	wd, err := windowDir(dir, windowID)
	if err != nil {
		return err
	}
	if !viewerRe.MatchString(viewer) {
		return fmt.Errorf("invalid viewer name %q", viewer)
	}
	if err := os.MkdirAll(wd, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", wd, err)
	}
	if err := os.WriteFile(filepath.Join(wd, viewer), nil, 0o600); err != nil {
		return fmt.Errorf("writing marker: %w", err)
	}
	return nil
}

// Remove deletes viewer's marker and the window directory once it is empty.
// A missing marker is not an error.
func Remove(dir, windowID, viewer string) error {
	wd, err := windowDir(dir, windowID)
	if err != nil {
		return err
	}
	if !viewerRe.MatchString(viewer) {
		return fmt.Errorf("invalid viewer name %q", viewer)
	}
	if err := os.Remove(filepath.Join(wd, viewer)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing marker: %w", err)
	}
	// Best effort: fails (harmlessly) while other connections remain.
	_ = os.Remove(wd)
	return nil
}

// Active reports whether any phone connection is recorded for windowID. It is
// a single directory read, cheap enough for the statusline hot path.
func Active(dir, windowID string) bool {
	wd, err := windowDir(dir, windowID)
	if err != nil {
		return false
	}
	entries, err := os.ReadDir(wd)
	return err == nil && len(entries) > 0
}

// Prune removes every marker for which keep(viewer) is false, and empty window
// directories. keep nil removes everything (startup: a crash leaves stale
// markers). It returns the number of markers removed.
func Prune(dir string, keep func(viewer string) bool) int {
	windows, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, w := range windows {
		wd := filepath.Join(dir, w.Name())
		if !w.IsDir() {
			_ = os.Remove(wd)
			continue
		}
		markers, _ := os.ReadDir(wd)
		for _, m := range markers {
			if keep != nil && keep(m.Name()) {
				continue
			}
			if os.Remove(filepath.Join(wd, m.Name())) == nil {
				n++
			}
		}
		_ = os.Remove(wd)
	}
	return n
}
