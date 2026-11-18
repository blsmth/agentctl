package tui

import (
	"errors"
	"os"
	"testing"
)

// TestMain keeps the hub's view state (folds, filter) off the real state
// dir: NewFleetModel would otherwise read it, and a `z` in any test would
// write it. Tests that exercise persistence point hubViewPath at a temp dir.
func TestMain(m *testing.M) {
	hubViewPath = func() (string, error) { return "", errors.New("hub view state disabled in tests") }
	_ = os.Unsetenv(HubViewEnv)
	os.Exit(m.Run())
}
