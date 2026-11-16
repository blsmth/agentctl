package hooks

import (
	"os"
	"testing"

	"github.com/blsmth/agentctl/internal/notify"
)

// TestMain keeps tests that reach beginApproval (via the popup gates) from
// firing real desktop notifications or resolving the developer's real window.
func TestMain(m *testing.M) {
	deps.Notify = func(notify.Options) {}
	approvalWindowID = func() string { return "" }
	approvalSessionName = func() string { return "agentctl-test" }
	approvalRuntimeDir = func() string { return "" }
	os.Exit(m.Run())
}
