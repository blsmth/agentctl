package serve

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/safefs"
)

const (
	// maxWriteBody caps every write request body.
	maxWriteBody = 64 << 10
	// writesPerMinute is the per-device write rate (token bucket, burst of
	// the same size).
	writesPerMinute = 30
	// AuditFile is the write audit log under the serve dir.
	AuditFile = "audit.jsonl"
	// maxAuditField bounds request-derived strings in an audit line.
	maxAuditField = 128
)

// writeLimiter is the per-device write rate limit.
type writeLimiter struct {
	mu sync.Mutex
	m  map[string]*rate.Limiter
}

// reset forgets every device's bucket (they start full again).
func (l *writeLimiter) reset() {
	l.mu.Lock()
	l.m = nil
	l.mu.Unlock()
}

// reserve takes a token for device at now. It returns 0 when the write may
// proceed, else how long until a token is available (nothing is consumed).
func (l *writeLimiter) reserve(device string, now time.Time) time.Duration {
	l.mu.Lock()
	lim, ok := l.m[device]
	if !ok {
		if l.m == nil {
			l.m = make(map[string]*rate.Limiter)
		}
		lim = rate.NewLimiter(rate.Every(time.Minute/writesPerMinute), writesPerMinute)
		l.m[device] = lim
	}
	l.mu.Unlock()
	r := lim.ReserveN(now, 1)
	if !r.OK() {
		return time.Minute
	}
	if d := r.DelayFrom(now); d > 0 {
		r.CancelAt(now)
		return d
	}
	return 0
}

// auditEntry is one line of audit.jsonl. It never carries request text:
// prompts and replies are represented only by BodySHA256.
type auditEntry struct {
	Time       string `json:"time"`
	DeviceID   string `json:"device_id"`
	DeviceName string `json:"device_name"`
	RemoteIP   string `json:"remote_ip"`
	Action     string `json:"action"`
	Session    string `json:"session"`
	Window     string `json:"window"`
	BodySHA256 string `json:"body_sha256"`
	Result     string `json:"result"`
	// Agent and Project name what a dispatch launches.
	Agent   string `json:"agent,omitempty"`
	Project string `json:"project,omitempty"`
	// Schedule names the cron job a /v1/schedules write acts on.
	Schedule string `json:"schedule,omitempty"`
	// Artifact is the id a /v1/artifacts write acts on.
	Artifact string `json:"artifact,omitempty"`
	// Backend, Model and Effort are the requested launch choices (not secret).
	Backend string `json:"backend,omitempty"`
	Model   string `json:"model,omitempty"`
	Effort  string `json:"effort,omitempty"`
	// BytesIn and BytesOut count a live terminal's traffic on its
	// "disconnected" line (keystrokes in, screen output out).
	BytesIn  *int64 `json:"bytes_in,omitempty"`
	BytesOut *int64 `json:"bytes_out,omitempty"`
	// Remote approval lines: the approval, who asked, the decision and the
	// SHA-256 of the payload the phone was shown (never the payload).
	ApprovalID    string `json:"approval_id,omitempty"`
	ApprovalKind  string `json:"approval_kind,omitempty"`
	AskingAgent   string `json:"asking_agent,omitempty"`
	Decision      string `json:"decision,omitempty"`
	PayloadSHA256 string `json:"payload_sha256,omitempty"`
}

// auditLog appends entries to <serve dir>/audit.jsonl (0600), one locked
// append per entry.
type auditLog struct {
	mu   sync.Mutex
	dir  string
	path string
}

func newAuditLog(dir string) *auditLog {
	return &auditLog{dir: dir, path: filepath.Join(dir, AuditFile)}
}

func (a *auditLog) append(e auditEntry) error {
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	a.mu.Lock()
	defer a.mu.Unlock()
	f, err := safefs.OpenNoFollow(a.dir, a.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("opening audit log: %w", err)
	}
	_ = f.Chmod(0o600)
	if _, err := f.Write(line); err != nil {
		_ = f.Close()
		return fmt.Errorf("writing audit log: %w", err)
	}
	return f.Close()
}

// clip bounds a request-derived audit field.
func clip(s string) string {
	if len(s) > maxAuditField {
		return s[:maxAuditField]
	}
	return s
}

func remoteIP(r *http.Request) string {
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return h
	}
	return r.RemoteAddr
}

// writeActions maps the last path segment of a window write route to its
// audit action.
var writeActions = map[string]bool{"answer": true, "reply": true, "queue": true, "command": true, "upload": true, "interrupt": true, "close": true}

// writeActionFor names the write route a path targets, for auditing requests
// that are rejected before routing (CSRF) or by the allow_write gate.
func writeActionFor(path string) (action, session, window string, ok bool) {
	if path == "/v1/dispatch" {
		return "dispatch", "", "", true
	}
	if path == "/v1/shell" {
		return "shell", "", "", true
	}
	if path == "/v1/dispatch/upload" {
		return "dispatch-upload", "", "", true
	}
	if path == "/v1/projects/open" {
		return "open-project", "", "", true
	}
	if path == "/v1/group-order" {
		return "group-order", "", "", true
	}
	if path == "/v1/sweep" {
		return "sweep", "", "", true
	}
	if path == "/v1/sweep/preview" {
		return "sweep-preview", "", "", true
	}
	if strings.HasPrefix(path, "/v1/closed/") && strings.HasSuffix(path, "/resume") {
		return "closed-resume", "", "", true
	}
	if strings.HasPrefix(path, "/v1/approvals/") {
		return "approval", "", "", true
	}
	if action, ok := scheduleActionFor(path); ok {
		return action, "", "", true
	}
	if action, ok := artifactActionFor(path); ok {
		return action, "", "", true
	}
	// /v1/sessions/{s}/windows/{w}/{action}
	p := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(p) == 6 && p[0] == "v1" && p[1] == "sessions" && p[3] == "windows" && writeActions[p[5]] {
		return p[5], p[2], p[4], true
	}
	// /v1/sessions/{s}/shells/{w}/close
	if len(p) == 6 && p[0] == "v1" && p[1] == "sessions" && p[3] == "shells" && p[5] == "close" {
		return "close-shell", p[2], p[4], true
	}
	return "", "", "", false
}

// auditFor starts an audit entry for a write request by device d.
func (s *Server) auditFor(r *http.Request, d Device, action string) auditEntry {
	_, session, window, _ := writeActionFor(r.URL.Path)
	e := auditEntry{
		Time:       s.now().UTC().Format(time.RFC3339Nano),
		DeviceID:   d.ID,
		DeviceName: d.Name,
		RemoteIP:   remoteIP(r),
		Action:     action,
		Session:    clip(session),
		Window:     clip(window),
	}
	if action, ok := artifactActionFor(r.URL.Path); ok && action != "artifact-order" {
		e.Artifact = clip(strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")[2])
	}
	return e
}

func (s *Server) recordAudit(e auditEntry) {
	if err := s.audit.append(e); err != nil {
		logging.Error("serve: audit: %v", err)
	}
}

// auditAttemptResult marks the line written before a write acts.
const auditAttemptResult = "attempt"

// auditAttempt records that a write is about to act, before it does: a
// side effect that hangs or crashes serve still leaves a trace. The final
// line (with the result) follows when the handler returns.
func (s *Server) auditAttempt(e auditEntry) {
	e.Result = auditAttemptResult
	s.recordAudit(e)
}

// auditRejected records a write attempt refused before its handler ran
// (allow_write off, CSRF header missing). Requests that carry no valid device
// cookie are not recorded: they never reached a write route.
func (s *Server) auditRejected(r *http.Request, code string) {
	action, _, _, ok := writeActionFor(r.URL.Path)
	if !ok {
		return
	}
	d, found := DeviceFrom(r.Context())
	if !found {
		c, err := r.Cookie(DeviceCookie)
		if err != nil || c.Value == "" {
			return
		}
		if d, found = s.devices.Authenticate(c.Value, s.now()); !found {
			return
		}
	}
	e := s.auditFor(r, d, action)
	e.Result = code
	s.recordAudit(e)
}

// outcome is a write handler's result: a JSON body on success, or an error.
type outcome struct {
	status int
	code   string
	msg    string
	body   any
	header http.Header
}

func success(body any) outcome { return outcome{status: http.StatusOK, body: body} }

func failure(status int, code, msg string) outcome {
	return outcome{status: status, code: code, msg: msg}
}

// okBody is the success body of the window actions.
var okBody = struct {
	OK bool `json:"ok"`
}{true}

// decodeStrict decodes exactly one JSON object with no unknown fields.
func decodeStrict(body []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing data after JSON object")
	}
	return nil
}

// writeRoute wraps a write handler with the guards every write shares, in
// order: per-device rate limit, JSON content type, body size limit, strict
// decoding into T. Every outcome, success or rejection, is audited with the
// body's SHA-256 (never the body). h may fill a.Session and a.Window with
// the resolved target.
func writeRoute[T any](s *Server, action string, h func(r *http.Request, in T, a *auditEntry) outcome) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d, _ := DeviceFrom(r.Context())
		a := s.auditFor(r, d, action)
		out := s.guardWrite(w, r, d, &a, func(body []byte) outcome {
			var in T
			if err := decodeStrict(body, &in); err != nil {
				return failure(http.StatusBadRequest, codeBadRequest, "invalid JSON body: "+err.Error())
			}
			return h(r, in, &a)
		})
		for k, v := range out.header {
			w.Header()[k] = v
		}
		if out.status >= 400 {
			a.Result = out.code
			if out.body != nil {
				writeJSON(w, out.status, out.body)
			} else {
				writeError(w, out.status, out.code, out.msg)
			}
		} else {
			a.Result = "ok"
			writeJSON(w, out.status, out.body)
		}
		s.recordAudit(a)
	}
}

func (s *Server) guardWrite(w http.ResponseWriter, r *http.Request, d Device, a *auditEntry, next func([]byte) outcome) outcome {
	if wait := s.limiter.reserve(d.ID, s.now()); wait > 0 {
		out := failure(http.StatusTooManyRequests, codeRateLimited, "too many write actions; try again shortly")
		out.header = http.Header{"Retry-After": {strconv.Itoa(int(math.Ceil(wait.Seconds())))}}
		return out
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		return failure(http.StatusUnsupportedMediaType, codeUnsupportedMedia, "Content-Type must be application/json")
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWriteBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return failure(http.StatusRequestEntityTooLarge, codeTooLarge, fmt.Sprintf("request body exceeds %d bytes", maxWriteBody))
		}
		return failure(http.StatusBadRequest, codeBadRequest, "reading request body")
	}
	sum := sha256.Sum256(body)
	a.BodySHA256 = hex.EncodeToString(sum[:])
	return next(body)
}
