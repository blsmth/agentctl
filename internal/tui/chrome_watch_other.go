//go:build !darwin

package tui

// newPlatformWatcher is fsnotify (inotify on Linux) everywhere but macOS.
func newPlatformWatcher() (dirWatcher, error) { return newFsnotifyWatcher() }
