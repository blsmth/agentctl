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
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/config"
)

// dirName is the subdirectory of the state dir that holds the markers.
const dirName = "phone-viewed"

var (
	windowRe = regexp.MustCompile(`^@[0-9]+$`)
	viewerRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

// PanePrefix starts the viewer name of a snapshot (capture-pane) view: the
// phone's window detail screen polls the pane instead of holding a socket, so
// its marker has no close event and expires PaneTTL after its last Touch.
const PanePrefix = "pane-"

// PaneTTL is how long a snapshot marker counts after its last Touch. The
// phone polls every 2s; this survives a few missed polls and a phone that
// vanished without a close.
const PaneTTL = 10 * time.Second

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

// PaneViewer is the viewer name for a snapshot view by the given device. Any
// character outside the marker alphabet is replaced.
func PaneViewer(device string) string {
	b := []byte(device)
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			b[i] = '_'
		}
	}
	return PanePrefix + string(b)
}

// Touch records (or refreshes) a snapshot view of windowID by viewer, which
// must start with PanePrefix. The marker stops counting PaneTTL later.
func Touch(dir, windowID, viewer string) error {
	if !strings.HasPrefix(viewer, PanePrefix) {
		return fmt.Errorf("viewer %q is not a snapshot viewer", viewer)
	}
	if err := Add(dir, windowID, viewer); err != nil {
		return err
	}
	now := time.Now()
	return os.Chtimes(filepath.Join(dir, windowID, viewer), now, now)
}

// expired reports whether a snapshot marker is past its TTL. Live terminal
// markers never expire here: serve removes them on close or prunes at startup.
func expired(name string, info os.FileInfo, now time.Time) bool {
	return strings.HasPrefix(name, PanePrefix) && now.Sub(info.ModTime()) > PaneTTL
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
	if err != nil {
		return false
	}
	now := time.Now()
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), PanePrefix) {
			return true
		}
		if info, err := e.Info(); err == nil && !expired(e.Name(), info, now) {
			return true
		}
	}
	return false
}

// PruneExpired removes snapshot markers past their TTL (and emptied window
// directories), for views that vanished without a close. It returns the number
// removed.
func PruneExpired(dir string) int {
	windows, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	now := time.Now()
	n := 0
	for _, w := range windows {
		if !w.IsDir() {
			continue
		}
		wd := filepath.Join(dir, w.Name())
		markers, _ := os.ReadDir(wd)
		for _, m := range markers {
			if info, err := m.Info(); err == nil && expired(m.Name(), info, now) && os.Remove(filepath.Join(wd, m.Name())) == nil {
				n++
			}
		}
		_ = os.Remove(wd)
	}
	return n
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
