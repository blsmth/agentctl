package hooks

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/notify"
	"github.com/blsmth/agentctl/internal/remoteapproval"
	"github.com/blsmth/agentctl/internal/testenv"
	"github.com/blsmth/agentctl/internal/winname"
)

// TestMain keeps tests that reach beginApproval (via the popup gates) from
// firing real desktop notifications or resolving the developer's real window.
func TestMain(m *testing.M) {
	cleanup := testenv.Setup()
	deps.Notify = func(notify.Options) {}
	approvalWindowID = func() string { return "" }
	approvalSessionName = func() string { return "agentctl-test" }
	approvalRuntimeDir = func() string { return "" }
	// Hook tests must never spawn a real janitor process.
	spawnJanitorFn = func(string) error { return nil }
	// Nor a real monitor via the watchdog.
	watchdogSpawnFn = func(string) error { return nil }
	// Nor a metrics serve or session-meta sync: in a test binary
	// os.Executable() is the test binary, which would re-run the suite.
	spawnMetaSyncFn = func(string, string) error { return nil }
	ensureMetricsServeFn = func() bool { return false }
	// Nor read a real pane or sleep in the launch watcher.
	lwPaneOf = func(string) string { return "" }
	lwCapture = func(string) (string, error) { return "", errors.New("no tmux in tests") }
	lwSession = func(string) string { return "" }
	lwSleep = func(time.Duration) {}
	// Handles come from a fixed empty index, never tmux or the ledger.
	sessionIndexFn = func() *fleet.Index { return fleet.NewIndex(nil, nil) }
	// Never rename (or query) real tmux windows from a unit test.
	winname.SetSeamsForTest(
		func(string, string) error { return nil },
		func(string) string { return "" },
		func() string { return "" },
	)
	// Never open a remote approval on the developer's running agentctl
	// serve (it would push to their phone).
	openRemote = func(remoteapproval.Request) (*remoteapproval.Approval, error) {
		return nil, remoteapproval.ErrNotRunning
	}
	code := m.Run()
	cleanup()
	os.Exit(code)
}
