package serve

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/remoteapproval"
	"github.com/blsmth/agentctl/internal/servectl"
)

// ControlSocket is the unix socket the CLI uses to talk to the running
// server. It lives in the 0700 serve dir and is itself 0600.
const ControlSocket = servectl.ControlSocket

const controlTimeout = 5 * time.Second

// ErrNotRunning is returned by the control client when no server answers.
var ErrNotRunning = errors.New("agentctl serve is not running (start it with `agentctl serve`, or `agentctl serve install` to run it under launchd)")

// controller is what the control socket exposes. *Server implements it.
type controller interface {
	OpenPairing(name string) (PairInfo, error)
	Status() StatusInfo
	RequestRestart()
	// Ping reports the server's pid and build for the approval hook's
	// guard.
	Ping() remoteapproval.PingInfo
	// ServeApproval runs one remote approval on its connection (it owns
	// conn and keeps reading br for the hook's cancel). Nothing it reads
	// can approve: decisions come only from POST /v1/approvals/{id}.
	ServeApproval(conn net.Conn, br *bufio.Reader, req remoteapproval.Request)
}

type controlRequest struct {
	Op   string `json:"op"`
	Name string `json:"name,omitempty"`
}

type controlResponse struct {
	Error  string                   `json:"error,omitempty"`
	Pair   *PairInfo                `json:"pair,omitempty"`
	Status *StatusInfo              `json:"status,omitempty"`
	Ping   *remoteapproval.PingInfo `json:"ping,omitempty"`
}

// listenControl binds <dir>/control.sock. A socket nobody answers on is
// stale (a crashed server) and is replaced; a live one is an error.
func listenControl(dir string) (net.Listener, error) {
	path := filepath.Join(dir, ControlSocket)
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode().Type() != fs.ModeSocket {
			return nil, fmt.Errorf("%s exists and is not a socket", path)
		}
		if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
			_ = c.Close()
			return nil, fmt.Errorf("another agentctl serve is answering on %s", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("removing stale %s: %w", path, err)
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listening on %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("chmod %s: %w", path, err)
	}
	return ln, nil
}

// serveControl answers one JSON request per connection until ln closes.
func serveControl(ln net.Listener, c controller) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				logging.Error("serve: control socket: %v", err)
			}
			return
		}
		go handleControl(conn, c)
	}
}

// handleControl answers one newline-terminated JSON request. An
// approval_request keeps the connection open (see controller.ServeApproval).
func handleControl(conn net.Conn, c controller) {
	_ = conn.SetDeadline(time.Now().Add(controlTimeout))
	br := bufio.NewReaderSize(conn, 4096)
	line, err := remoteapproval.ReadLine(br)
	var req controlRequest
	if err == nil {
		err = json.Unmarshal(line, &req)
	}
	if err != nil {
		_ = json.NewEncoder(conn).Encode(controlResponse{Error: "bad request: " + err.Error()})
		_ = conn.Close()
		return
	}
	if req.Op == remoteapproval.OpRequest {
		var ar remoteapproval.Request
		if err := json.Unmarshal(line, &ar); err != nil {
			_ = json.NewEncoder(conn).Encode(remoteapproval.Decision{Error: "bad request: " + err.Error()})
			_ = conn.Close()
			return
		}
		c.ServeApproval(conn, br, ar)
		return
	}
	defer func() { _ = conn.Close() }()
	var resp controlResponse
	switch req.Op {
	case "pair":
		info, err := c.OpenPairing(req.Name)
		if err != nil {
			resp.Error = err.Error()
		} else {
			resp.Pair = &info
		}
	case "restart":
		c.RequestRestart()
	case "status":
		st := c.Status()
		resp.Status = &st
	case remoteapproval.OpPing:
		info := c.Ping()
		resp.Ping = &info
	case remoteapproval.OpCancel:
		// A cancel is only meaningful on the approval's own connection.
		resp.Error = "approval_cancel must be sent on the approval's connection"
	default:
		resp.Error = fmt.Sprintf("unknown op %q", req.Op)
	}
	_ = json.NewEncoder(conn).Encode(resp)
}

// controlCall sends req to the server listening in dir.
func controlCall(dir string, req controlRequest) (controlResponse, error) {
	conn, err := net.DialTimeout("unix", filepath.Join(dir, ControlSocket), controlTimeout)
	if err != nil {
		return controlResponse{}, ErrNotRunning
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(controlTimeout))
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return controlResponse{}, fmt.Errorf("control socket: %w", err)
	}
	var resp controlResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return controlResponse{}, fmt.Errorf("control socket: %w", err)
	}
	if resp.Error != "" {
		return resp, errors.New(resp.Error)
	}
	return resp, nil
}

// RequestPairing asks the running server to open a pairing window.
func RequestPairing(dir, name string) (PairInfo, error) {
	resp, err := controlCall(dir, controlRequest{Op: "pair", Name: name})
	if err != nil {
		return PairInfo{}, err
	}
	if resp.Pair == nil {
		return PairInfo{}, errors.New("control socket: empty pairing response")
	}
	return *resp.Pair, nil
}

// RequestServerRestart asks the running server to re-exec itself.
func RequestServerRestart(dir string) error {
	_, err := controlCall(dir, controlRequest{Op: "restart"})
	return err
}

// QueryStatus asks the running server for its status.
func QueryStatus(dir string) (StatusInfo, error) {
	resp, err := controlCall(dir, controlRequest{Op: "status"})
	if err != nil {
		return StatusInfo{}, err
	}
	if resp.Status == nil {
		return StatusInfo{}, errors.New("control socket: empty status response")
	}
	return *resp.Status, nil
}
