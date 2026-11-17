// Package remoteapproval is the wire protocol between an approval hook and
// agentctl serve for answering approval popups from the phone ("away mode"),
// plus the hook-side client and the guard that checks the control socket
// belongs to the real server before anything is sent to it.
//
// One control-socket connection carries one approval. The hook writes a
// Request, then reads a single Decision line from that same connection; the
// decision is never read from anywhere else. The hook may write a Cancel (or
// just close the connection) when the laptop popup answered first. No message
// the hook can receive on any other channel approves anything.
//
// The package is deliberately small and free of the serve package's
// dependencies so the hook binary path stays light.
package remoteapproval

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"sync"
	"time"

	"github.com/blsmth/agentctl/internal/buildid"
)

// Control-socket ops.
const (
	OpRequest = "approval_request"
	OpCancel  = "approval_cancel"
	OpPing    = "ping"
)

// Kinds of approval.
const (
	KindDispatch = "dispatch"
	KindBash     = "bash"
)

// Decisions.
const (
	Allow = "allow"
	Deny  = "deny"
)

// Cancel reasons sent by the hook.
const (
	ReasonLaptop   = "answered_on_laptop"
	ReasonTimeout  = "timeout"
	ReasonCanceled = "cancelled"
)

// MaxMessageBytes bounds one control-socket line (request or decision).
const MaxMessageBytes = 1 << 20

var idRE = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ValidID reports whether id is a 128-bit lowercase hex approval ID.
func ValidID(id string) bool { return idRE.MatchString(id) }

// NewID returns a fresh 128-bit random approval ID (hex).
func NewID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generating approval id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// Request opens a remote approval. Prompt is the fully resolved prompt
// (including --prompt-key content); Command is the raw Bash command.
type Request struct {
	Op          string    `json:"op"`
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	AskingAgent string    `json:"asking_agent"`
	Session     string    `json:"session"`
	WindowID    string    `json:"window_id"`
	TargetAgent string    `json:"target_agent"`
	Issue       int       `json:"issue"`
	Prompt      string    `json:"prompt"`
	Command     string    `json:"command"`
	Deadline    time.Time `json:"deadline"`
}

// Cancel withdraws a remote approval (the laptop answered, or the hook gave
// up). Closing the connection has the same effect.
type Cancel struct {
	Op     string `json:"op"`
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// Decision is the single line serve writes back on the approval's
// connection. Error is set instead when serve refused the request.
type Decision struct {
	Decision      string `json:"decision,omitempty"`
	Device        string `json:"device,omitempty"`
	PayloadSHA256 string `json:"payload_sha256,omitempty"`
	Error         string `json:"error,omitempty"`
}

// PingInfo is serve's answer to a ping: who is listening.
type PingInfo struct {
	PID   int              `json:"pid"`
	Build buildid.Identity `json:"build"`
}

// canonicalPayload is what the phone shows and what the payload hash covers.
// Field order is fixed by the struct, and nothing is omitted, so the JSON is
// canonical.
type canonicalPayload struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	AskingAgent string `json:"asking_agent"`
	Session     string `json:"session"`
	WindowID    string `json:"window_id"`
	TargetAgent string `json:"target_agent"`
	Issue       int    `json:"issue"`
	Prompt      string `json:"prompt"`
	Command     string `json:"command"`
}

// PayloadHash is the SHA-256 (hex) of the canonical JSON of the fields the
// phone shows. An answer must carry the hash of exactly what was shown.
func PayloadHash(r Request) string {
	b, _ := json.Marshal(canonicalPayload{
		ID: r.ID, Kind: r.Kind, AskingAgent: r.AskingAgent, Session: r.Session, WindowID: r.WindowID,
		TargetAgent: r.TargetAgent, Issue: r.Issue, Prompt: r.Prompt, Command: r.Command,
	})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Validate checks a request's shape (serve applies it to everything it
// receives; the hook applies it before sending).
func (r Request) Validate() error {
	switch {
	case !ValidID(r.ID):
		return errors.New("invalid approval id")
	case r.Kind != KindDispatch && r.Kind != KindBash:
		return fmt.Errorf("invalid approval kind %q", r.Kind)
	case r.Command == "":
		return errors.New("empty command")
	case r.Deadline.IsZero():
		return errors.New("missing deadline")
	}
	return nil
}

// Approval is one open remote approval on the hook side.
type Approval struct {
	conn net.Conn
	r    *bufio.Reader
	id   string
	hash string

	once sync.Once
}

// Start sends req on conn and returns the open approval. conn is owned by the
// approval from here on, also on error.
func Start(conn net.Conn, req Request) (*Approval, error) {
	req.Op = OpRequest
	if err := req.Validate(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	b, err := json.Marshal(req)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("encoding approval request: %w", err)
	}
	if len(b) >= MaxMessageBytes {
		_ = conn.Close()
		return nil, errors.New("approval request too large")
	}
	_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write(append(b, '\n')); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("sending approval request: %w", err)
	}
	_ = conn.SetWriteDeadline(time.Time{})
	return &Approval{conn: conn, r: bufio.NewReaderSize(conn, 4096), id: req.ID, hash: PayloadHash(req)}, nil
}

// ID returns the approval's ID.
func (a *Approval) ID() string { return a.id }

// Wait blocks for the single decision line. It returns an error for anything
// but a well-formed allow or deny bound to this approval's payload hash: a
// closed connection, a refusal, garbage, or a hash mismatch. Callers must
// treat every error as "no remote answer", never as allow.
func (a *Approval) Wait() (Decision, error) {
	line, err := ReadLine(a.r)
	if err != nil {
		return Decision{}, fmt.Errorf("reading remote decision: %w", err)
	}
	var d Decision
	if err := json.Unmarshal(line, &d); err != nil {
		return Decision{}, fmt.Errorf("decoding remote decision: %w", err)
	}
	if d.Error != "" {
		return Decision{}, fmt.Errorf("serve refused the approval: %s", d.Error)
	}
	if d.Decision != Allow && d.Decision != Deny {
		return Decision{}, fmt.Errorf("invalid remote decision %q", d.Decision)
	}
	if d.PayloadSHA256 != a.hash {
		return Decision{}, errors.New("remote decision is for a different payload")
	}
	return d, nil
}

// Cancel tells serve the approval is over (best effort) and closes the
// connection. Safe to call more than once and concurrently with Wait.
func (a *Approval) Cancel(reason string) {
	a.once.Do(func() {
		b, _ := json.Marshal(Cancel{Op: OpCancel, ID: a.id, Reason: reason})
		_ = a.conn.SetWriteDeadline(time.Now().Add(500 * time.Millisecond))
		_, _ = a.conn.Write(append(b, '\n'))
		_ = a.conn.Close()
	})
}

// Close closes the connection without a cancel line.
func (a *Approval) Close() {
	a.once.Do(func() { _ = a.conn.Close() })
}

// ReadLine reads one newline-terminated line of at most MaxMessageBytes.
func ReadLine(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, isPrefix, err := r.ReadLine()
		if err != nil {
			return nil, err
		}
		buf = append(buf, chunk...)
		if len(buf) > MaxMessageBytes {
			return nil, errors.New("message too large")
		}
		if !isPrefix {
			return buf, nil
		}
	}
}
