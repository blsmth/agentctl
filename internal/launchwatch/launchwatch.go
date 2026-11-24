// Package launchwatch starts the launch watcher for a freshly launched agent
// window: a detached, hidden `agentctl launch-watch <windowID>` that reads
// the pane for a short while after launch and reports a folder-trust dialog
// (see hooks.LaunchWatch). Claude Code shows that dialog before it loads any
// hooks, so nothing else notices a window stuck on it.
//
// This package only decides whether to watch and spawns the process, behind a
// seam every launching test stubs (SetSpawnForTest, launchtest.Capture). The
// watching itself lives in internal/hooks, beside the other blocked-state
// writers and notifications.
package launchwatch

import (
	"errors"
	"sync"
	"testing"

	"github.com/blsmth/agentctl/internal/detach"
	"github.com/blsmth/agentctl/internal/harness"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/paths"
)

// Subcommand is the hidden CLI subcommand the watcher runs as.
const Subcommand = "launch-watch"

// errTestSpawn is what the default spawn returns inside a test binary that
// forgot to stub it: a test must never fork a watcher.
var errTestSpawn = errors.New("launchwatch: spawn not stubbed in a test binary")

var (
	mu sync.Mutex
	// spawnFn starts the detached watcher. The runtime dir is pinned so the
	// watcher reads the windowstate the launcher just wrote, whatever its cwd.
	spawnFn = func(windowID string) error {
		if testing.Testing() {
			return errTestSpawn
		}
		return detach.StartDaemon([]string{Subcommand, windowID}, paths.RuntimeDir())
	}
)

// SetSpawnForTest replaces the spawn and returns a restore func. Tests that
// run a launch path must stub it (launchtest.Capture does).
func SetSpawnForTest(fn func(windowID string) error) (restore func()) {
	mu.Lock()
	defer mu.Unlock()
	old := spawnFn
	spawnFn = fn
	return func() {
		mu.Lock()
		defer mu.Unlock()
		spawnFn = old
	}
}

// Start spawns the launch watcher for windowID when the harness can stop on
// a folder-trust dialog (Caps.TrustDialog). It does nothing without a window
// ID (no tmux, or the launch did not create one). Failures are logged, never
// returned: the watcher is a best-effort signal and must not fail a launch.
func Start(windowID string, caps harness.Caps) {
	if windowID == "" || !caps.TrustDialog {
		return
	}
	mu.Lock()
	fn := spawnFn
	mu.Unlock()
	if err := fn(windowID); err != nil {
		logging.Warn("launchwatch: starting watcher for %s: %v", windowID, err)
	}
}
