// Package servectl starts, stops and probes agentctl serve from outside the
// server: `agentctl serve start|stop|toggle`, the prefix-key toggle and the
// hub's serve indicator. It is a leaf package (internal/serve imports the
// TUI, which needs this), so it mirrors the few serve constants it needs;
// internal/serve defines its own in terms of these.
//
// Start, stop and toggle switch phone access, not the process: stop writes
// the phone-off file (PhoneOffFile) in the serve dir, which the running
// server reads per request, so artifact links keep working while the phone
// app and push notifications are off. StopAll is the full stop (a launchctl
// bootout). Start removes the file and starts the process when it is down.
//
// Every launchctl call goes through launchd.Agent's Runner, and every signal
// through Controller.Terminate, so tests never touch the real job.
package servectl

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
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
	// PhoneOffFile, in the serve dir, switches phone access off while it
	// exists: no push notifications, no remote approvals, and the phone app
	// routes answer 503 phone_off. Artifact links keep working. Only
	// SetPhone writes it (agents' Bash and Write are denied the serve dir).
	PhoneOffFile = "phone-off"
)

// PhoneOff reports whether phone access is switched off in dir. Any error
// other than "no such file" reads as off, so a switch that cannot be read
// errs toward silence.
func PhoneOff(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, PhoneOffFile))
	return !errors.Is(err, fs.ErrNotExist)
}

// SetPhone switches phone access on (removes PhoneOffFile) or off (writes
// it, creating the 0700 serve dir when needed).
func SetPhone(dir string, on bool) error {
	path := filepath.Join(dir, PhoneOffFile)
	if on {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("turning phone access on: %w", err)
		}
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("turning phone access off: %w", err)
	}
	stamp := time.Now().UTC().Format(time.RFC3339) + "\n"
	if err := os.WriteFile(path, []byte(stamp), 0o600); err != nil {
		return fmt.Errorf("turning phone access off: %w", err)
	}
	return nil
}

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
	// StateOff: no live agentctl serve process (artifact links are down
	// too).
	StateOff State = iota
	// StateRunning: the process is alive, its control socket accepts and
	// phone access is on.
	StateRunning
	// StateUnresponsive: serve.pid names a live agentctl serve, but its
	// control socket does not accept (remote approvals are not offered).
	StateUnresponsive
	// StatePhoneOff: the process is alive and answering, serving artifact
	// links, but phone access is switched off (PhoneOffFile).
	StatePhoneOff
)

// String names the state for messages and tests.
func (s State) String() string {
	switch s {
	case StateRunning:
		return "running"
	case StateUnresponsive:
		return "unresponsive"
	case StatePhoneOff:
		return "phone-off"
	}
	return "off"
}

// Serving reports whether the process answers (artifact links work),
// whatever the phone switch says.
func (s State) Serving() bool { return s == StateRunning || s == StatePhoneOff }

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
// reads as off. An answering server with PhoneOffFile present is
// StatePhoneOff (one more stat).
func Probe(dir string) State {
	return probe(dir, RunningPID, dialControl)
}

func probe(dir string, running func(string) (int, bool), dial func(string) error) State {
	if dial(dir) == nil {
		if PhoneOff(dir) {
			return StatePhoneOff
		}
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

// Start turns phone access on and makes sure serve runs. With the process
// already answering that is all it does. Otherwise, on macOS, it bootstraps
// the installed LaunchAgent, or kickstarts it when launchd already has it
// loaded (restarting a process whose control socket is dead). It does not
// spawn an unmanaged server when the LaunchAgent is not installed: that
// process would have no supervisor, no log capture and would not come back
// at login, so Start returns ErrNotInstalled instead. On other systems it
// returns the systemctl command to run.
func (c Controller) Start() (string, error) {
	wasOff := PhoneOff(c.Dir)
	if err := SetPhone(c.Dir, true); err != nil {
		return "", err
	}
	if url, err := c.URL(c.Dir); err == nil {
		if wasOff {
			return "phone access on (serve at " + url + ")", nil
		}
		return "serve is already running at " + url, nil
	}
	return c.startProcess()
}

// startProcess starts the serve process (see Start).
func (c Controller) startProcess() (string, error) {
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
		return "", fmt.Errorf("agentctl serve (pid %d) runs outside launchd and is not answering; stop it first: agentctl serve stop --all", pid)
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

// Stop turns phone access off: no push notifications, no approvals offered
// to the phone, and the phone app answers "phone access is off". The serve
// process keeps running, so artifact links keep working. It needs no
// launchctl and works the same on every OS.
func (c Controller) Stop() (string, error) {
	if err := SetPhone(c.Dir, false); err != nil {
		return "", err
	}
	if _, alive := c.Running(c.Dir); !alive {
		return "phone access off (serve is not running; agentctl serve start starts it with phone access on)", nil
	}
	return "phone access off: no phone notifications or app; artifact links still work (agentctl serve start turns it back on)", nil
}

// StopAll stops the serve process until the next login (or `agentctl serve
// start`), artifact links included. On macOS it boots the LaunchAgent out,
// which leaves the plist installed, and SIGTERMs a serve process launchd
// does not manage (one run in a terminal). It does not `launchctl disable`
// the job: a stop that silently outlived a reboot would leave serve
// installed but never running, and remote approvals quietly off; `agentctl
// serve uninstall` is the permanent stop. It leaves the phone switch as it
// is. On other systems it returns the systemctl command to run.
func (c Controller) StopAll() (string, error) {
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

// Toggle turns phone access off when serve answers with phone access on,
// and otherwise starts: phone access on, and the process started when it
// is down or restarted when it is stuck (alive, control socket dead), so
// one press on a stuck server fixes it rather than only writing the switch.
func (c Controller) Toggle() (string, error) {
	if _, err := c.URL(c.Dir); err == nil && !PhoneOff(c.Dir) {
		return c.Stop()
	}
	return c.Start()
}
