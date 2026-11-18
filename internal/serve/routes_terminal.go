package serve

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/phoneview"
	"github.com/blsmth/agentctl/internal/tmux"
)

const (
	// maxTerminals bounds concurrent live terminals (each is a tmux client
	// and a PTY).
	maxTerminals = 4
	// defaultTermSizeEvery is how often the window size is re-read.
	defaultTermSizeEvery = 2 * time.Second
	// termReadLimit bounds one WebSocket message from the phone.
	termReadLimit = 64 << 10
	// termBufSize is the PTY read buffer (one binary frame at most).
	termBufSize = 32 << 10
	// termSizeFailures is how many consecutive failed size reads end the
	// connection (the window is gone).
	termSizeFailures = 2

	codeForbiddenOrigin  = "forbidden_origin"
	codeTooManyTerminals = "too_many_terminals"
	codeShuttingDown     = "shutting_down"
)

// terminals tracks live terminal connections so Close can end them and wait
// for their viewer sessions to be killed.
type terminals struct {
	tmux      TerminalTmux
	ptys      PTYStarter
	sizeEvery time.Duration
	slots     chan struct{}
	// markers is the phoneview directory; empty disables the markers.
	markers string

	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
}

func newTerminals(t TerminalTmux, p PTYStarter, sizeEvery time.Duration) *terminals {
	if t == nil {
		t = tmuxTerminal{}
	}
	if p == nil {
		p = creackPTYs{}
	}
	if sizeEvery <= 0 {
		sizeEvery = defaultTermSizeEvery
	}
	return &terminals{tmux: t, ptys: p, sizeEvery: sizeEvery, slots: make(chan struct{}, maxTerminals)}
}

// acquire reserves a terminal slot. It fails when all slots are taken or the
// server is closing; release must follow a successful acquire.
func (ts *terminals) acquire() (code string, ok bool) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.closed {
		return codeShuttingDown, false
	}
	select {
	case ts.slots <- struct{}{}:
	default:
		return codeTooManyTerminals, false
	}
	ts.wg.Add(1)
	return "", true
}

func (ts *terminals) release() {
	<-ts.slots
	ts.wg.Done()
}

// close refuses new terminals and waits for open ones to clean up (their
// handlers watch Server.done).
func (ts *terminals) close() {
	ts.mu.Lock()
	ts.closed = true
	ts.mu.Unlock()
	ts.wg.Wait()
}

// SweepTerminals kills phone viewer sessions left over from a previous run
// (a crash, or a kill -9). Run calls it once at startup, before serving.
func (s *Server) SweepTerminals() {
	// No terminal is open yet, so every marker is stale.
	if s.term.markers != "" {
		phoneview.Prune(s.term.markers, nil)
	}
	names, err := s.term.tmux.Viewers()
	if err != nil {
		logging.Warn("serve: listing stale terminal viewers: %v", err)
		return
	}
	for _, n := range names {
		if !tmux.IsMobileViewerSession(n) {
			continue
		}
		if err := s.term.tmux.KillViewer(n); err != nil {
			logging.Warn("serve: killing stale viewer %s: %v", n, err)
			continue
		}
		logging.Log("serve: killed stale terminal viewer %s", n)
	}
}

// registerTerminal registers the live terminal WebSocket.
func (s *Server) registerTerminal(rt *Router) {
	rt.Feature("terminal")
	rt.Device("GET /v1/sessions/{session}/windows/{window}/terminal", s.handleTerminal)
}

// sameOrigin reports whether the request's Origin is this server. Browsers
// always send Origin on a WebSocket handshake and CORS does not cover
// WebSockets, so a missing or foreign Origin is refused: without this check
// any page the phone visits could open a terminal with the device cookie.
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return false
	}
	u, err := url.Parse(o)
	if err != nil || u.Host == "" || u.Path != "" || u.User != nil {
		return false
	}
	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
	}
	return u.Scheme == scheme && strings.EqualFold(u.Host, r.Host)
}

// termSize is the {type:"size"} control message. ReadOnly tells the phone
// whether its input is accepted.
type termSize struct {
	Type     string `json:"type"`
	Cols     int    `json:"cols"`
	Rows     int    `json:"rows"`
	ReadOnly bool   `json:"read_only"`
}

// termPing is the {type:"ping"} keepalive, sent both ways.
type termPing struct {
	Type string `json:"type"`
}

func (s *Server) handleTerminal(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, codeForbiddenOrigin, "cross-origin terminal request refused")
		return
	}
	g, i, ok := s.lookup(w, r)
	if !ok {
		return
	}
	target := WindowTarget{Session: g.Session, RuntimeDir: g.RuntimeDir, WindowID: g.Windows[i].WindowID}
	if code, ok := s.term.acquire(); !ok {
		if code == codeTooManyTerminals {
			w.Header().Set("Retry-After", "5")
			writeError(w, http.StatusTooManyRequests, code, "too many live terminals are open")
		} else {
			writeError(w, http.StatusServiceUnavailable, code, "server is shutting down")
		}
		return
	}
	defer s.term.release()

	// The server's Read/WriteTimeout would cut the connection after 30s; a
	// hijacked connection keeps the deadlines, so lift them first.
	rc := http.NewResponseController(w)
	if err := rc.SetReadDeadline(time.Time{}); err != nil {
		logging.Debug("serve: lifting read deadline: %v", err)
	}
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		logging.Debug("serve: lifting write deadline: %v", err)
	}
	// Origin was verified above, exactly (scheme and host).
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		logging.Debug("serve: terminal upgrade: %v", err)
		return
	}
	conn.SetReadLimit(termReadLimit)
	d, _ := DeviceFrom(r.Context())
	s.runTerminal(r, conn, d, target)
}

// termStats counts bytes each way, for the log and the audit line.
type termStats struct {
	in, out atomic.Int64
}

// runTerminal sets up the viewer and PTY behind an accepted WebSocket and
// pipes until either side ends or the server closes. Every exit path kills
// the PTY process and the viewer session.
func (s *Server) runTerminal(r *http.Request, conn *websocket.Conn, d Device, t WindowTarget) {
	readOnly := !s.opts.Config.AllowWrite
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		select {
		case <-s.done:
			cancel()
		case <-ctx.Done():
		}
	}()

	cols, rows, err := s.term.tmux.WindowSize(t.WindowID)
	if err != nil {
		logging.Warn("serve: terminal: window size of %s: %v", t.WindowID, err)
		_ = conn.Close(websocket.StatusInternalError, "window unavailable")
		return
	}
	viewer, err := s.term.tmux.CreateViewer(t.Session, t.WindowID)
	if err != nil {
		logging.Warn("serve: terminal: creating viewer for %s:%s: %v", t.Session, t.WindowID, err)
		_ = conn.Close(websocket.StatusInternalError, "could not create viewer")
		return
	}
	defer func() {
		if err := s.term.tmux.KillViewer(viewer); err != nil {
			logging.Warn("serve: terminal: killing viewer %s: %v", viewer, err)
		}
	}()
	p, err := s.term.ptys.Start("tmux", tmux.MobileViewerAttachArgs(viewer, readOnly), cols, rows)
	if err != nil {
		logging.Warn("serve: terminal: %v", err)
		_ = conn.Close(websocket.StatusInternalError, "could not attach")
		return
	}
	defer func() { _ = p.Close() }()
	defer s.markPhoneView(t.WindowID, viewer)()

	var st termStats
	s.terminalOpened(r, d, t, viewer, readOnly)
	defer func() { s.terminalClosed(r, d, t, viewer, readOnly, &st) }()

	if err := writeControl(ctx, conn, termSize{Type: "size", Cols: cols, Rows: rows, ReadOnly: readOnly}); err != nil {
		return
	}
	go pipeOutput(ctx, cancel, p, conn, &st)
	go pipeInput(ctx, cancel, conn, p, readOnly, &st)
	s.watchTerminal(ctx, conn, p, t.WindowID, cols, rows, readOnly)
	_ = conn.Close(websocket.StatusNormalClosure, "")
}

// markPhoneView records that viewer shows windowID to the phone and returns
// the func that removes the record. A failure to write is logged and never
// blocks the terminal: the statusline just keeps its normal format.
func (s *Server) markPhoneView(windowID, viewer string) func() {
	dir := s.term.markers
	if dir == "" {
		return func() {}
	}
	if err := phoneview.Add(dir, windowID, viewer); err != nil {
		logging.Warn("serve: terminal: phone-view marker: %v", err)
		return func() {}
	}
	return func() {
		if err := phoneview.Remove(dir, windowID, viewer); err != nil {
			logging.Warn("serve: terminal: removing phone-view marker: %v", err)
		}
	}
}

// watchTerminal resends the window size when it changes and sends keepalive
// pings, until ctx ends or the window disappears.
func (s *Server) watchTerminal(ctx context.Context, conn *websocket.Conn, p PTY, windowID string, cols, rows int, readOnly bool) {
	size := time.NewTicker(s.term.sizeEvery)
	defer size.Stop()
	ping := time.NewTicker(s.pingEvery)
	defer ping.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			if writeControl(ctx, conn, termPing{Type: "ping"}) != nil {
				return
			}
		case <-size.C:
			c, r, err := s.term.tmux.WindowSize(windowID)
			if err != nil {
				if failures++; failures >= termSizeFailures {
					_ = conn.Close(websocket.StatusGoingAway, "window closed")
					return
				}
				continue
			}
			failures = 0
			if c == cols && r == rows {
				continue
			}
			cols, rows = c, r
			if err := p.Resize(cols, rows); err != nil {
				logging.Debug("serve: terminal: resizing pty: %v", err)
			}
			if writeControl(ctx, conn, termSize{Type: "size", Cols: cols, Rows: rows, ReadOnly: readOnly}) != nil {
				return
			}
		}
	}
}

func writeControl(ctx context.Context, conn *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, b)
}

// pipeOutput copies PTY output to binary frames until the PTY ends (the
// client exited or was killed), then cancels.
func pipeOutput(ctx context.Context, cancel context.CancelFunc, p PTY, conn *websocket.Conn, st *termStats) {
	defer cancel()
	buf := make([]byte, termBufSize)
	for {
		n, err := p.Read(buf)
		if n > 0 {
			if werr := conn.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
				return
			}
			st.out.Add(int64(n))
		}
		if err != nil {
			return
		}
	}
}

// pipeInput reads frames from the phone until the socket closes, then
// cancels. Binary frames are keystrokes: written to the PTY in write mode,
// dropped in read-only mode. Text frames are control messages; only ping is
// defined, and it needs no reply (traffic is the keepalive).
func pipeInput(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn, p PTY, readOnly bool, st *termStats) {
	defer cancel()
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			var ce websocket.CloseError
			if !errors.As(err, &ce) && ctx.Err() == nil {
				logging.Debug("serve: terminal read: %v", err)
			}
			return
		}
		if typ != websocket.MessageBinary || readOnly || len(data) == 0 {
			continue
		}
		if _, err := p.Write(data); err != nil {
			return
		}
		st.in.Add(int64(len(data)))
	}
}

// terminalOpened logs a new terminal and, in write mode, audits it: the
// phone can type into an agent from here on.
func (s *Server) terminalOpened(r *http.Request, d Device, t WindowTarget, viewer string, readOnly bool) {
	logging.Log("serve: terminal opened: device %s, %s:%s via %s (read-only %v)", d.Name, t.Session, t.WindowID, viewer, readOnly)
	if readOnly {
		return
	}
	a := s.terminalAudit(r, d, t)
	a.Result = "connected"
	s.recordAudit(a)
}

// terminalClosed logs and, in write mode, audits the end of a terminal with
// byte counts (never content).
func (s *Server) terminalClosed(r *http.Request, d Device, t WindowTarget, viewer string, readOnly bool, st *termStats) {
	in, out := st.in.Load(), st.out.Load()
	logging.Log("serve: terminal closed: device %s, %s:%s via %s (%d bytes in, %d out)", d.Name, t.Session, t.WindowID, viewer, in, out)
	if readOnly {
		return
	}
	a := s.terminalAudit(r, d, t)
	a.Result = "disconnected"
	a.BytesIn, a.BytesOut = &in, &out
	s.recordAudit(a)
}

func (s *Server) terminalAudit(r *http.Request, d Device, t WindowTarget) auditEntry {
	return auditEntry{
		Time:       s.now().UTC().Format(time.RFC3339Nano),
		DeviceID:   d.ID,
		DeviceName: d.Name,
		RemoteIP:   remoteIP(r),
		Action:     "terminal",
		Session:    clip(t.Session),
		Window:     clip(t.WindowID),
	}
}
