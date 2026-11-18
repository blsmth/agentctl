package tui

import (
	"errors"
	"os"
	"testing"

	"github.com/blsmth/agentctl/internal/servectl"
	"github.com/blsmth/agentctl/internal/testenv"
)

// TestMain keeps the hub's view state (folds, filter) off the real state
// dir: NewFleetModel would otherwise read it, and a `z` in any test would
// write it. Tests that exercise persistence point hubViewPath at a temp dir.
// It also stubs the hub's agentctl serve probe and toggle.
func TestMain(m *testing.M) {
	cleanup := testenv.Setup()
	hubViewPath = func() (string, error) { return "", errors.New("hub view state disabled in tests") }
	_ = os.Unsetenv(HubViewEnv)
	// Never probe or toggle the real agentctl serve; tests that need either
	// replace these.
	fleetServeProbe = func() servectl.State { return servectl.StateOff }
	fleetServeToggle = func() (string, error) { return "", errors.New("serve toggle disabled in tests") }
	code := m.Run()
	cleanup()
	os.Exit(code)
}
