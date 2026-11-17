package remoteapproval

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/blsmth/agentctl/internal/buildid"
	"github.com/blsmth/agentctl/internal/pidfile"
)

// Files under the serve dir (mirrors internal/serve, which this package must
// not import).
const (
	ControlSocket = "control.sock"
	PIDFile       = "serve.pid"
)

// DialTimeout is how long the hook waits for serve's control socket to accept
// before deciding serve is not running.
const DialTimeout = 200 * time.Millisecond

// pingTimeout bounds the ping round trip.
const pingTimeout = 2 * time.Second

// ErrNotRunning means nothing accepted on the control socket: there is no
// server, and the hook behaves exactly as without away mode.
var ErrNotRunning = errors.New("agentctl serve is not running")

// ErrUntrusted means something answered on the control socket but it could
// not be verified as the real agentctl serve. The hook must not use it.
var ErrUntrusted = errors.New("control socket failed verification")

// Guard opens remote approvals only on a control socket that belongs to the
// running agentctl serve. Its checks, in order:
//
//  1. The socket accepts within DialTimeout (else ErrNotRunning).
//  2. serve.pid is a regular 0600 file owned by this user, holding a live
//     process whose command line is `agentctl ... serve`.
//  3. Where the OS reports it (macOS LOCAL_PEERPID, Linux SO_PEERCRED), the
//     process on the other end of the socket is that pid.
//  4. A ping on the socket reports that pid and the same executable build
//     (dev, inode, size, mtime) as the hook's own binary.
//
// A same-uid process can still defeat this by impersonating every answer
// (see docs/design/mobile-web-app.md, "Remote approvals"); the guard raises
// the bar from "bind a socket" to "replace the server process convincingly",
// and a fake socket can only ever cause a deny or a timeout unless it also
// forges the payload hash, because the hook decides only on its own
// connection.
type Guard struct {
	// Dir is the serve state dir (<StateDir>/serve).
	Dir string
	// Dial connects to the unix socket at path. nil means net.DialTimeout.
	Dial func(path string, timeout time.Duration) (net.Conn, error)
	// Self returns the build identity the server must report. nil skips
	// nothing: it is required.
	Self func() (buildid.Identity, error)
	// Running reports the live serve pid named by the pidfile. nil means
	// pidfile.Running(dir, PIDFile, "serve").
	Running func(dir string) (int, bool)
	// PeerPID reports the pid on the other end of conn, when the OS can say.
	// nil means the platform implementation.
	PeerPID func(conn net.Conn) (pid int, ok bool)
	// Uid is the owner the pidfile must have; nil means os.Getuid.
	Uid func() int
}

func (g Guard) dial(timeout time.Duration) (net.Conn, error) {
	path := filepath.Join(g.Dir, ControlSocket)
	if g.Dial != nil {
		return g.Dial(path, timeout)
	}
	return net.DialTimeout("unix", path, timeout)
}

// Open verifies the server and starts the approval on a fresh connection.
// It returns ErrNotRunning when serve is not answering, an error wrapping
// ErrUntrusted when verification failed, or another error.
func (g Guard) Open(req Request) (*Approval, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	probe, err := g.dial(DialTimeout)
	if err != nil {
		return nil, ErrNotRunning
	}
	pid, err := g.verify(probe)
	_ = probe.Close()
	if err != nil {
		return nil, err
	}
	conn, err := g.dial(DialTimeout)
	if err != nil {
		return nil, ErrNotRunning
	}
	if err := g.checkPeer(conn, pid); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return Start(conn, req)
}

// verify runs checks 2-4 on probe and returns the verified pid.
func (g Guard) verify(probe net.Conn) (int, error) {
	pid, err := g.readPIDFile()
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrUntrusted, err)
	}
	running := g.Running
	if running == nil {
		running = func(dir string) (int, bool) { return pidfile.Running(dir, PIDFile, "serve") }
	}
	live, ok := running(g.Dir)
	if !ok || live != pid {
		return 0, fmt.Errorf("%w: pid %d from %s is not a live agentctl serve", ErrUntrusted, pid, PIDFile)
	}
	if err := g.checkPeer(probe, pid); err != nil {
		return 0, err
	}
	info, err := Ping(probe)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrUntrusted, err)
	}
	if info.PID != pid {
		return 0, fmt.Errorf("%w: socket reports pid %d, %s says %d", ErrUntrusted, info.PID, PIDFile, pid)
	}
	if g.Self == nil {
		return 0, fmt.Errorf("%w: no build identity to compare", ErrUntrusted)
	}
	self, err := g.Self()
	if err != nil {
		return 0, fmt.Errorf("%w: own build identity: %v", ErrUntrusted, err)
	}
	if !buildid.SameBuild(self, info.Build) {
		return 0, fmt.Errorf("%w: server build differs from this agentctl binary", ErrUntrusted)
	}
	return pid, nil
}

func (g Guard) checkPeer(conn net.Conn, pid int) error {
	peer := g.PeerPID
	if peer == nil {
		peer = PeerPID
	}
	if got, ok := peer(conn); ok && got != pid {
		return fmt.Errorf("%w: socket peer is pid %d, %s says %d", ErrUntrusted, got, PIDFile, pid)
	}
	return nil
}

// readPIDFile reads serve.pid, which must be a regular file, mode 0600 (no
// group or other bits), owned by the expected uid.
func (g Guard) readPIDFile() (int, error) {
	path := filepath.Join(g.Dir, PIDFile)
	fi, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	if !fi.Mode().IsRegular() {
		return 0, fmt.Errorf("%s is not a regular file", path)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return 0, fmt.Errorf("%s is mode %o, want 0600", path, fi.Mode().Perm())
	}
	uid := os.Getuid
	if g.Uid != nil {
		uid = g.Uid
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != uid() {
		return 0, fmt.Errorf("%s is owned by uid %d", path, st.Uid)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("%s holds no pid", path)
	}
	return pid, nil
}

// Ping asks the server on conn who it is.
func Ping(conn net.Conn) (PingInfo, error) {
	_ = conn.SetDeadline(time.Now().Add(pingTimeout))
	defer func() { _ = conn.SetDeadline(time.Time{}) }()
	if err := json.NewEncoder(conn).Encode(struct {
		Op string `json:"op"`
	}{OpPing}); err != nil {
		return PingInfo{}, fmt.Errorf("sending ping: %w", err)
	}
	var resp struct {
		Error string    `json:"error"`
		Ping  *PingInfo `json:"ping"`
	}
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return PingInfo{}, fmt.Errorf("reading ping: %w", err)
	}
	if resp.Error != "" {
		return PingInfo{}, fmt.Errorf("ping: %s", resp.Error)
	}
	if resp.Ping == nil {
		return PingInfo{}, errors.New("ping: empty response")
	}
	return *resp.Ping, nil
}
