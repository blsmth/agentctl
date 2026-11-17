package hooks

import (
	"os"
	"testing"

	"github.com/blsmth/agentctl/internal/notify"
	"github.com/blsmth/agentctl/internal/winname"
)

// TestMain keeps tests that reach beginApproval (via the popup gates) from
// firing real desktop notifications or resolving the developer's real window.
func TestMain(m *testing.M) {
	deps.Notify = func(notify.Options) {}
	approvalWindowID = func() string { return "" }
	approvalSessionName = func() string { return "agentctl-test" }
	approvalRuntimeDir = func() string { return "" }
	// Hook tests must never spawn a real janitor process.
	spawnJanitorFn = func(string) error { return nil }
	// Nor a real monitor via the watchdog.
	watchdogSpawnFn = func(string) error { return nil }
	// Never rename (or query) real tmux windows from a unit test.
	winname.SetSeamsForTest(
		func(string, string) error { return nil },
		func(string) string { return "" },
		func() string { return "" },
	)
	os.Exit(m.Run())
}
