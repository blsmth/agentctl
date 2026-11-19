package tui

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/servectl"
	"github.com/blsmth/agentctl/internal/sweeprun"
	"github.com/blsmth/agentctl/internal/testenv"
)

// TestMain keeps the hub's view state (folds, filter) off the real state
// dir: NewFleetModel would otherwise read it, and a `z` in any test would
// write it. Tests that exercise persistence point hubViewPath at a temp dir.
// It also stubs the hub's agentctl serve probe and toggle.
func TestMain(m *testing.M) {
	cleanup := testenv.Setup()
	hubViewPath = func() (string, error) { return "", errors.New("hub view state disabled in tests") }
	groupOrderPath = func() (string, error) { return "", errors.New("group order disabled in tests") }
	_ = os.Unsetenv(HubViewEnv)
	// Never probe or toggle the real agentctl serve; tests that need either
	// replace these.
	fleetServeProbe = func() servectl.State { return servectl.StateOff }
	fleetServeToggle = func() (string, error) { return "", errors.New("serve toggle disabled in tests") }
	// Never spawn agentctl (launch, hub-new-session) or read the real hub
	// origin; tests that need them replace these.
	fleetLaunch = func(g fleet.Group) tea.Cmd {
		return func() tea.Msg {
			return fleetLaunchedMsg{session: g.Session, err: errors.New("launch disabled in tests")}
		}
	}
	fleetNewSessionRun = func(string, []string) (string, error) {
		return "", errors.New("hub-new-session disabled in tests")
	}
	fleetHubOrigin = func() (string, bool) { return "", false }
	// Never spawn `agentctl sweep`; sweep tests install their own stub.
	fleetSweep = func(context.Context, sweeprun.Target, bool) (sweeprun.Report, error) {
		return sweeprun.Report{}, errors.New("sweep disabled in tests")
	}
	// Never spawn gh or tmux from the tickets browser; tests that need a
	// result install their own stub.
	tkExec = func(string, ...string) ([]byte, error) { return nil, errExecDisabled }
	// Never list ready issues, dispatch, or read/write the autopilot setting.
	tkAutoPollRunner = autoPollRunner{
		list:     func(string) ([]byte, error) { return nil, errExecDisabled },
		dispatch: func(int, string) error { return errExecDisabled },
	}
	tkLoadAutoPoll = func() (bool, time.Duration) { return false, 5 * time.Minute }
	tkWriteAutoPoll = func(bool) error { return errExecDisabled }
	tkEditorCmd = func(string) *exec.Cmd { return nil }
	code := m.Run()
	cleanup()
	os.Exit(code)
}
