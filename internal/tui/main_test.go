package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/artifacts"
	"github.com/blsmth/agentctl/internal/cron"
	"github.com/blsmth/agentctl/internal/cronedit"
	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/prompttemplate"
	"github.com/blsmth/agentctl/internal/servectl"
	"github.com/blsmth/agentctl/internal/sweeprun"
	"github.com/blsmth/agentctl/internal/testenv"
	"github.com/blsmth/agentctl/internal/tmux"
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
	// No real timers: tests send the dropdown's slide ticks themselves.
	nsDropTick = func(int) tea.Cmd { return nil }
	// Never spawn `agentctl sweep`; sweep tests install their own stub.
	fleetSweep = func(context.Context, sweeprun.Target, bool) (sweeprun.Report, error) {
		return sweeprun.Report{}, errors.New("sweep disabled in tests")
	}
	// Never touch the real hub window's panes or spawn the activity HUD;
	// activity tests install their own stubs.
	activityOpenPane = func(int) (string, error) { return "", errors.New("activity pane disabled in tests") }
	activityKillPanes = func() error { return nil }
	activityResize = func(string, int) error { return nil }
	activityPanes = func() ([]tmux.HubPane, error) { return nil, errors.New("activity poll disabled in tests") }
	activityListPane = func() string { return "" }
	activityHideViewer = func() error { return nil }
	// Never create, resize or kill the real status pane (which would spawn
	// `agentctl hub-status`); status tests install their own stubs.
	hubStatusSync = func(bool, int) (bool, error) { return false, nil }
	hubStatusPin = func(int) error { return nil }
	// Never read the real artifact store or spawn agentctl/a browser; artifact
	// tests install their own stubs.
	fleetArtifactStamp = func() (time.Time, bool) { return time.Time{}, false }
	fleetArtifactList = func() ([]artifacts.Meta, error) { return nil, nil }
	fleetArtifactOpen = func(string) error { return errors.New("artifact open disabled in tests") }
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
	// Never read or write the real schedules, run `agentctl cron run`, close
	// the real viewer pane or exec an editor; schedule tests install their
	// own stubs.
	fleetSchedStore = func(func(string, string) error) cronedit.Store {
		disabled := func() (string, error) { return "", errors.New("schedules disabled in tests") }
		return cronedit.Store{JobsPath: disabled, StateDir: disabled, SchedulerInstalled: func() bool { return true }}
	}
	fleetSchedRun = func(context.Context, string) (cron.Entry, error) {
		return cron.Entry{}, errors.New("cron run disabled in tests")
	}
	fleetSchedPermMode = func(string) string { return "auto" }
	fleetSchedTemplates = func(string) []prompttemplate.Template { return nil }
	fleetSchedEnvLoad = testSchedEnv
	// Never exec tmux from the hub: the viewer, zoom, focus, switch and
	// row-action paths all reach real (sandboxed, but leaked) servers.
	// Tests that exercise them install their own stubs.
	errTmux := errors.New("tmux disabled in tests")
	showLiveViewer = func(string, string, int) error { return errTmux }
	resizeLiveViewer = func(int) error { return errTmux }
	hideLiveViewer = func() error { return nil }
	viewerActiveWindow = func() (string, string) { return "", "" }
	switchClientToLast = func(string) error { return errTmux }
	switchClientToSession = func(string, string) error { return errTmux }
	listSessionActivity = func() []tmux.SessionActivity { return nil }
	switchToSessionWindow = func(string, string) error { return errTmux }
	focusLivePane = func() error { return errTmux }
	focusViewerPane = func() error { return errTmux }
	hubWindowWidth = func() (int, error) { return 0, errTmux }
	hubViewerState = func() tmux.ViewerState { return tmux.ViewerState{} }
	repointViewer = func(string, string) error { return errTmux }
	hubZoomSync = func(bool) {}
	newShellWindow = func(string, string) (string, error) { return "", errTmux }
	killSessionWindow = func(string, string) error { return errTmux }
	code := m.Run()
	// Guard: a unit test must never start a tmux server, even a sandboxed
	// one (cleanup would kill it, but the exec path is the bug).
	if testenv.LeakedTmux() {
		fmt.Fprintln(os.Stderr, "FAIL: a tui test left a tmux server running in the sandbox; stub the tmux seam it reached")
		code = 1
	}
	cleanup()
	os.Exit(code)
}
