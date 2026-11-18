// Package servectl starts, stops and probes agentctl serve from outside the
// server: `agentctl serve start|stop|toggle`, the prefix-key toggle and the
// hub's serve indicator. It is a leaf package (internal/serve imports the
// TUI, which needs this), so it mirrors the few serve constants it needs;
// internal/serve defines its own in terms of these.
//
// Every launchctl call goes through launchd.Agent's Runner, and every signal
// through Controller.Terminate, so tests never touch the real job.
package servectl

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/launchd"
	"github.com/blsmth/agentctl/internal/pidfile"
)

// Names shared with internal/serve.
const (
	// Label is the LaunchAgent label.
	Label = "com.blsmth.agentctl.serve"
	// PIDFile and ControlSocket live in the serve dir.
	PIDFile       = "serve.pid"
	ControlSocket = "control.sock"
	// SystemdUnit is the unit name `serve install` suggests on Linux.
	SystemdUnit = "agentctl-serve"
)

// probeDialTimeout bounds the control-socket connect in Probe. The socket is
// local, so a live server accepts at once.
const probeDialTimeout = 200 * time.Millisecond

// controlTimeout bounds the status round trip in QueryURL.
const controlTimeout = 2 * time.Second

// Dir returns the serve state dir, <StateDir>/serve.
func Dir() (string, error) {
	state, err := config.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(state, "serve"), nil
}

// State is what Probe saw.
type State int

const (
	// StateOff: no live agentctl serve process.
	StateOff State = iota
	// StateRunning: the process is alive and its control socket accepts.
	StateRunning
	// StateUnresponsive: serve.pid names a live agentctl serve, but its
	// control socket does not accept (remote approvals are not offered).
	StateUnresponsive
)

// String names the state for messages and tests.
func (s State) String() string {
	switch s {
	case StateRunning:
		return "running"
	case StateUnresponsive:
		return "unresponsive"
	}
	return "off"
}

// RunningPID reports the live serve pid named by serve.pid. pidfile.Running
// checks that the process's command line is `agentctl ... serve`, so a stale
// pidfile whose pid was reused by something else reads as not running.
func RunningPID(dir string) (int, bool) {
	return pidfile.Running(dir, PIDFile, "serve")
}

// Probe classifies serve's state cheaply: one local socket connect (no
// request is sent; the server answers the empty request with an error and
// closes) and, only when that fails, the pidfile liveness check, which forks
// ps. While serve is up that is a single connect with no fork. The socket
// lives in the 0700 serve dir, so an accepting one is taken as serve; this
// is a display hint, not the remote-approval Guard. A missing serve dir
// reads as off.
func Probe(dir string) State {
	return probe(dir, RunningPID, dialControl)
}

func probe(dir string, running func(string) (int, bool), dial func(string) error) State {
	if dial(dir) == nil {
		return StateRunning
	}
	if _, ok := running(dir); ok {
		return StateUnresponsive
	}
	return StateOff
}

func dialControl(dir string) error {
	c, err := net.DialTimeout("unix", filepath.Join(dir, ControlSocket), probeDialTimeout)
	if err != nil {
		return err
	}
	return c.Close()
}

// QueryURL asks the running server for its URL over the control socket.
func QueryURL(dir string) (string, error) {
	conn, err := net.DialTimeout("unix", filepath.Join(dir, ControlSocket), controlTimeout)
	if err != nil {
		return "", errors.New("agentctl serve is not answering on its control socket")
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(controlTimeout))
	if err := json.NewEncoder(conn).Encode(struct {
		Op string `json:"op"`
	}{"status"}); err != nil {
		return "", fmt.Errorf("control socket: %w", err)
	}
	var resp struct {
		Error  string `json:"error"`
		Status *struct {
			URL string `json:"url"`
		} `json:"status"`
	}
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return "", fmt.Errorf("control socket: %w", err)
	}
	if resp.Error != "" {
		return "", errors.New(resp.Error)
	}
	if resp.Status == nil {
		return "", errors.New("control socket: empty status response")
	}
	return resp.Status.URL, nil
}

// ErrNotInstalled is returned by Start on macOS when the LaunchAgent plist
// is missing.
var ErrNotInstalled = errors.New("agentctl serve is not installed: run `agentctl serve install` (or run `agentctl serve` in a terminal)")

// Controller starts and stops serve. Build one with New; tests fill the
// seams.
type Controller struct {
	Dir string
	// GOOS selects launchd (darwin) or the printed systemd commands.
	GOOS string
	// Agent runs launchctl; its Run is the only way out to a process.
	Agent launchd.Agent
	// Running reports the live serve pid (RunningPID).
	Running func(dir string) (int, bool)
	// URL asks the running server for its URL; an error means it does not
	// answer (QueryURL).
	URL func(dir string) (string, error)
	// Terminate sends SIGTERM to an unmanaged serve process.
	Terminate func(pid int) error
	// Sleep and Wait pace the wait for serve to come up or go away.
	Sleep func(time.Duration)
	Wait  time.Duration
}

// pollEvery is how often Start/Stop re-check while waiting.
const pollEvery = 200 * time.Millisecond

// defaultWait is how long Start/Stop wait for serve to come up or go away.
const defaultWait = 10 * time.Second

// errTestLaunchctl stops a real Controller built inside a test binary from
// reaching the user's real LaunchAgent.
var errTestLaunchctl = errors.New("servectl: refusing to run launchctl from a test binary")

// New returns a Controller for the current user and the real serve dir.
// Inside a test binary its launchctl runner and Terminate refuse to run.
func New() (Controller, error) {
	dir, err := Dir()
	if err != nil {
		return Controller{}, err
	}
	agent, err := launchd.NewAgent()
	if err != nil {
		return Controller{}, err
	}
	terminate := func(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }
	if testing.Testing() {
		agent.Run = func(string, ...string) ([]byte, error) { return nil, errTestLaunchctl }
		terminate = func(int) error { return errTestLaunchctl }
	}
	return Controller{
		Dir:       dir,
		GOOS:      runtime.GOOS,
		Agent:     agent,
		Running:   RunningPID,
		URL:       QueryURL,
		Terminate: terminate,
		Sleep:     time.Sleep,
		Wait:      defaultWait,
	}, nil
}

// waitFor polls cond until it holds or Wait passes.
func (c Controller) waitFor(cond func() bool) bool {
	for waited := time.Duration(0); ; waited += pollEvery {
		if cond() {
			return true
		}
		if waited >= c.Wait {
			return false
		}
		c.Sleep(pollEvery)
	}
}

// Start starts serve. On macOS it bootstraps the installed LaunchAgent, or
// kickstarts it when launchd already has it loaded (restarting a process
// whose control socket is dead). It does not spawn an unmanaged server when
// the LaunchAgent is not installed: that process would have no supervisor,
// no log capture and would not come back at login, so Start returns
// ErrNotInstalled instead. On other systems it returns the systemctl
// command to run.
func (c Controller) Start() (string, error) {
	if url, err := c.URL(c.Dir); err == nil {
		return "serve is already running at " + url, nil
	}
	if c.GOOS != "darwin" {
		return "Run: systemctl --user start " + SystemdUnit, nil
	}
	if !c.Agent.Installed(Label) {
		return "", ErrNotInstalled
	}
	pid, alive := c.Running(c.Dir)
	var err error
	switch {
	case c.Agent.Loaded(Label):
		err = c.Agent.Kickstart(Label, alive)
	case alive:
		// A server launchd does not manage holds the pidfile; a bootstrapped
		// one would exit "already running" and be respawned forever.
		return "", fmt.Errorf("agentctl serve (pid %d) runs outside launchd and is not answering; stop it first: agentctl serve stop", pid)
	default:
		err = c.Agent.Bootstrap(Label)
	}
	if err != nil {
		return "", fmt.Errorf("starting agentctl serve: %w", err)
	}
	var url string
	if !c.waitFor(func() bool {
		u, err := c.URL(c.Dir)
		url = u
		return err == nil
	}) {
		return "", errors.New("serve was started but is not answering yet; check `agentctl serve logs`")
	}
	return "serve started at " + url, nil
}

// Stop stops serve until the next login (or `agentctl serve start`). On
// macOS it boots the LaunchAgent out, which leaves the plist installed, and
// SIGTERMs a serve process launchd does not manage (one run in a terminal).
// It does not `launchctl disable` the job: a stop that silently outlived a
// reboot would leave serve installed but never running, and remote
// approvals quietly off; `agentctl serve uninstall` is the permanent stop.
// On other systems it returns the systemctl command to run.
func (c Controller) Stop() (string, error) {
	if c.GOOS != "darwin" {
		return "Run: systemctl --user stop " + SystemdUnit, nil
	}
	_, alive := c.Running(c.Dir)
	booted := false
	// Loaded alone: a job loaded with its plist deleted must still be booted
	// out, or KeepAlive respawns whatever SIGTERM stops.
	if c.Agent.Loaded(Label) {
		if err := c.Agent.Bootout(Label); err != nil {
			return "", fmt.Errorf("stopping agentctl serve: %w", err)
		}
		booted = true
	}
	if !alive && !booted {
		return "serve is not running", nil
	}
	gone := func() bool { _, ok := c.Running(c.Dir); return !ok }
	if booted && c.waitFor(gone) {
		return stoppedMessage(booted), nil
	}
	if pid, ok := c.Running(c.Dir); ok {
		if err := c.Terminate(pid); err != nil {
			return "", fmt.Errorf("stopping agentctl serve (pid %d): %w", pid, err)
		}
		if !c.waitFor(gone) {
			return "", fmt.Errorf("agentctl serve (pid %d) did not exit", pid)
		}
	}
	return stoppedMessage(booted), nil
}

func stoppedMessage(launchAgent bool) string {
	if launchAgent {
		return "serve stopped (it starts again at login, or with agentctl serve start)"
	}
	return "serve stopped"
}

// Toggle stops serve when a serve process is alive (answering or not) and
// starts it otherwise.
func (c Controller) Toggle() (string, error) {
	if _, alive := c.Running(c.Dir); alive {
		return c.Stop()
	}
	return c.Start()
}
