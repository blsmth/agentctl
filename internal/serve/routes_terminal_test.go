package serve

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/phoneview"
	"github.com/blsmth/agentctl/internal/tmux"
)

// fakeTermTmux records viewer lifecycle calls.
type fakeTermTmux struct {
	mu        sync.Mutex
	cols      int
	rows      int
	sizeErr   error
	createErr error
	created   []string // "home window" per CreateViewer
	killed    []string
	live      []string
	n         int
}

func (f *fakeTermTmux) CreateViewer(home, windowID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return "", f.createErr
	}
	f.n++
	name := tmux.MobileViewerSessionPrefix + strings.Repeat("a", f.n)
	f.created = append(f.created, home+" "+windowID)
	f.live = append(f.live, name)
	return name, nil
}

func (f *fakeTermTmux) WindowSize(string) (int, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cols, f.rows, f.sizeErr
}

func (f *fakeTermTmux) setSize(c, r int) {
	f.mu.Lock()
	f.cols, f.rows = c, r
	f.mu.Unlock()
}

func (f *fakeTermTmux) KillViewer(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killed = append(f.killed, name)
	f.live = slices.DeleteFunc(f.live, func(s string) bool { return s == name })
	return nil
}

func (f *fakeTermTmux) Viewers() ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.live), nil
}

func (f *fakeTermTmux) killedNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.killed)
}

// fakePTY is a PTY whose output the test writes and whose input it reads.
type fakePTY struct {
	args     []string
	outR     *io.PipeReader
	outW     *io.PipeWriter
	mu       sync.Mutex
	input    []byte
	resizes  [][2]int
	closed   bool
	inputHit chan struct{}
}

func (p *fakePTY) Read(b []byte) (int, error) { return p.outR.Read(b) }

func (p *fakePTY) Write(b []byte) (int, error) {
	p.mu.Lock()
	p.input = append(p.input, b...)
	p.mu.Unlock()
	select {
	case p.inputHit <- struct{}{}:
	default:
	}
	return len(b), nil
}

func (p *fakePTY) Resize(c, r int) error {
	p.mu.Lock()
	p.resizes = append(p.resizes, [2]int{c, r})
	p.mu.Unlock()
	return nil
}

func (p *fakePTY) Close() error {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	_ = p.outW.Close()
	return nil
}

func (p *fakePTY) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

func (p *fakePTY) gotInput() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.input)
}

type fakePTYs struct {
	mu      sync.Mutex
	started []*fakePTY
	ch      chan *fakePTY
}

func (f *fakePTYs) Start(name string, args []string, cols, rows int) (PTY, error) {
	if name != "tmux" {
		return nil, errors.New("unexpected command " + name)
	}
	r, w := io.Pipe()
	p := &fakePTY{args: args, outR: r, outW: w, inputHit: make(chan struct{}, 16)}
	f.mu.Lock()
	f.started = append(f.started, p)
	f.mu.Unlock()
	if f.ch != nil {
		f.ch <- p
	}
	return p, nil
}

type termEnv struct {
	*writeEnv
	tmux *fakeTermTmux
	ptys *fakePTYs
	http *httptest.Server
	// markers is the phone-view marker dir.
	markers string
}

func newTermEnv(t *testing.T, allowWrite bool) *termEnv {
	t.Helper()
	e := &termEnv{
		markers: t.TempDir(),
		tmux:    &fakeTermTmux{cols: 120, rows: 40},
		ptys:    &fakePTYs{ch: make(chan *fakePTY, 8)},
	}
	e.writeEnv = newWriteEnvWith(t, allowWrite, func(o *Options) {
		o.Terminal, o.PTY = e.tmux, e.ptys
		o.TerminalSizeEvery = 20 * time.Millisecond
		o.PhoneViewDir = e.markers
	})
	e.http = httptest.NewServer(e.Handler())
	t.Cleanup(e.http.Close)
	return e
}

const termPath = "/v1/sessions/agentctl-proj/windows/@523/terminal"

func (e *termEnv) dial(t *testing.T, origin string, cookie bool) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	h := http.Header{}
	if origin != "" {
		h.Set("Origin", origin)
	}
	if cookie {
		h.Set("Cookie", DeviceCookie+"="+e.tok)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(e.http.URL, "http")+termPath, &websocket.DialOptions{HTTPHeader: h})
	if c != nil {
		t.Cleanup(func() { _ = c.CloseNow() })
	}
	return c, resp, err
}

func (e *termEnv) open(t *testing.T) (*websocket.Conn, *fakePTY) {
	t.Helper()
	c, _, err := e.dial(t, e.http.URL, true)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	select {
	case p := <-e.ptys.ch:
		return c, p
	case <-time.After(5 * time.Second):
		t.Fatal("no PTY started")
	}
	return nil, nil
}

func readMsg(t *testing.T, c *websocket.Conn) (websocket.MessageType, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	typ, b, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return typ, b
}

// readSize reads text frames until a size message (skipping pings).
func readSize(t *testing.T, c *websocket.Conn) termSize {
	t.Helper()
	for {
		typ, b := readMsg(t, c)
		if typ != websocket.MessageText {
			continue
		}
		var m termSize
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("bad control message %q: %v", b, err)
		}
		if m.Type == "size" {
			return m
		}
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestTerminalFeatureAdvertised(t *testing.T) {
	e := newTermEnv(t, false)
	if !slices.Contains(e.router.Features(), "terminal") {
		t.Fatalf("features = %v", e.router.Features())
	}
}

func TestTerminalRejectsForeignOrigin(t *testing.T) {
	e := newTermEnv(t, true)
	for _, origin := range []string{"", "https://evil.example", "http://evil.example", "https://" + strings.TrimPrefix(e.http.URL, "http://"), e.http.URL + "/x"} {
		_, resp, err := e.dial(t, origin, true)
		if err == nil {
			t.Fatalf("origin %q: upgrade accepted", origin)
		}
		if resp == nil || resp.StatusCode != http.StatusForbidden {
			t.Errorf("origin %q: response %v, want 403", origin, resp)
		}
	}
	if len(e.tmux.created) != 0 || len(e.ptys.started) != 0 {
		t.Errorf("a refused origin created a viewer or PTY")
	}
}

func TestTerminalUnpaired(t *testing.T) {
	e := newTermEnv(t, true)
	_, resp, err := e.dial(t, e.http.URL, false)
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unpaired: err %v resp %v, want 401", err, resp)
	}
}

func TestTerminalUnknownWindow(t *testing.T) {
	e := newTermEnv(t, true)
	e.fleet.set()
	_, resp, err := e.dial(t, e.http.URL, true)
	if err == nil || resp == nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown window: err %v resp %v, want 404", err, resp)
	}
}

func TestTerminalSizeFirstAndOutput(t *testing.T) {
	e := newTermEnv(t, true)
	c, p := e.open(t)
	typ, b := readMsg(t, c)
	if typ != websocket.MessageText {
		t.Fatalf("first frame is %v, want the size message", typ)
	}
	var m termSize
	if err := json.Unmarshal(b, &m); err != nil || m.Type != "size" || m.Cols != 120 || m.Rows != 40 || m.ReadOnly {
		t.Fatalf("first message = %s", b)
	}
	if got := e.tmux.created; len(got) != 1 || got[0] != "agentctl-proj @523" {
		t.Errorf("viewer created for %v", got)
	}
	go func() { _, _ = p.outW.Write([]byte("\x1b[1mhello")) }()
	typ, b = readMsg(t, c)
	if typ != websocket.MessageBinary || string(b) != "\x1b[1mhello" {
		t.Fatalf("output frame = %v %q", typ, b)
	}

	// A window resize on the laptop is pushed to the phone and the PTY.
	e.tmux.setSize(200, 50)
	m = readSize(t, c)
	if m.Cols != 200 || m.Rows != 50 {
		t.Fatalf("resent size = %+v", m)
	}
	p.mu.Lock()
	resizes := slices.Clone(p.resizes)
	p.mu.Unlock()
	if !slices.Contains(resizes, [2]int{200, 50}) {
		t.Errorf("pty resizes = %v", resizes)
	}
}

func TestTerminalReadOnlyDropsInput(t *testing.T) {
	e := newTermEnv(t, false)
	c, p := e.open(t)
	if m := readSize(t, c); !m.ReadOnly {
		t.Errorf("size message does not say read-only: %+v", m)
	}
	attach := p.args[:slices.Index(p.args, ";")]
	if !slices.Contains(attach, "-r") || !slices.Contains(attach, "ignore-size") {
		t.Errorf("read-only attach args = %q, want -r and ignore-size", p.args)
	}
	ctx := context.Background()
	if err := c.Write(ctx, websocket.MessageBinary, []byte("rm -rf /\r")); err != nil {
		t.Fatal(err)
	}
	// A ping round-trip proves the server processed the input frame first.
	if err := c.Write(ctx, websocket.MessageText, []byte(`{"type":"ping"}`)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if got := p.gotInput(); len(got) != 0 {
		t.Fatalf("read-only terminal forwarded input %q", got)
	}
	_ = c.Close(websocket.StatusNormalClosure, "")
	waitFor(t, "viewer killed", func() bool { return len(e.tmux.killedNames()) == 1 })
	if lines := e.auditLines(t); len(lines) != 0 {
		t.Errorf("read-only terminal audited: %+v", lines)
	}
}

func TestTerminalWriteModeForwardsInputAndAudits(t *testing.T) {
	e := newTermEnv(t, true)
	c, p := e.open(t)
	readSize(t, c)
	if slices.Contains(p.args, "-r") {
		t.Errorf("write-mode attach is read-only: %q", p.args)
	}
	if err := c.Write(context.Background(), websocket.MessageBinary, []byte("ls\r")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "input", func() bool { return string(p.gotInput()) == "ls\r" })
	// Text frames are control messages, never keystrokes.
	_ = c.Write(context.Background(), websocket.MessageText, []byte("echo pwned"))
	time.Sleep(30 * time.Millisecond)
	if got := string(p.gotInput()); got != "ls\r" {
		t.Fatalf("input = %q", got)
	}

	_ = c.Close(websocket.StatusNormalClosure, "")
	waitFor(t, "disconnect audit", func() bool { return len(e.auditLines(t)) == 2 })
	lines := e.auditLines(t)
	if lines[0].Action != "terminal" || lines[0].Result != "connected" || lines[0].Window != "@523" {
		t.Errorf("connect line = %+v", lines[0])
	}
	if lines[1].Result != "disconnected" || lines[1].BytesIn == nil || *lines[1].BytesIn != 3 {
		t.Errorf("disconnect line = %+v", lines[1])
	}
}

func TestTerminalCleanupOnClientClose(t *testing.T) {
	e := newTermEnv(t, true)
	c, p := e.open(t)
	readSize(t, c)
	_ = c.Close(websocket.StatusNormalClosure, "bye")
	waitFor(t, "cleanup", func() bool { return p.isClosed() && len(e.tmux.killedNames()) == 1 })
	if got := e.tmux.killedNames()[0]; !tmux.IsMobileViewerSession(got) {
		t.Errorf("killed %q", got)
	}
}

func TestTerminalCleanupOnPTYExit(t *testing.T) {
	e := newTermEnv(t, true)
	c, p := e.open(t)
	readSize(t, c)
	_ = p.outW.Close() // the tmux client exited
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		if _, _, err := c.Read(ctx); err != nil {
			break
		}
	}
	waitFor(t, "viewer killed", func() bool { return len(e.tmux.killedNames()) == 1 })
}

func TestTerminalCleanupOnServerClose(t *testing.T) {
	e := newTermEnv(t, true)
	c, p := e.open(t)
	readSize(t, c)
	// Keep reading so the close handshake can complete.
	go func() {
		for {
			if _, _, err := c.Read(context.Background()); err != nil {
				return
			}
		}
	}()
	e.Close()
	if !p.isClosed() || len(e.tmux.killedNames()) != 1 {
		t.Fatalf("after Close: pty closed %v, killed %v", p.isClosed(), e.tmux.killedNames())
	}
	// No new terminals once closing.
	_, resp, err := e.dial(t, e.http.URL, true)
	if err == nil || resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("after Close: err %v resp %v, want 503", err, resp)
	}
}

func TestTerminalWindowGoneClosesSocket(t *testing.T) {
	e := newTermEnv(t, true)
	c, _ := e.open(t)
	readSize(t, c)
	e.tmux.mu.Lock()
	e.tmux.sizeErr = errors.New("can't find window")
	e.tmux.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		if _, _, err := c.Read(ctx); err != nil {
			if websocket.CloseStatus(err) != websocket.StatusGoingAway {
				t.Errorf("close = %v, want going away", err)
			}
			break
		}
	}
	waitFor(t, "viewer killed", func() bool { return len(e.tmux.killedNames()) == 1 })
}

func TestTerminalLimit(t *testing.T) {
	e := newTermEnv(t, true)
	for range maxTerminals {
		c, _ := e.open(t)
		readSize(t, c)
	}
	_, resp, err := e.dial(t, e.http.URL, true)
	if err == nil || resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("terminal %d: err %v resp %v, want 429", maxTerminals+1, err, resp)
	}
	if len(e.tmux.created) != maxTerminals {
		t.Errorf("created %d viewers, want %d", len(e.tmux.created), maxTerminals)
	}
}

func TestTerminalCreateViewerFails(t *testing.T) {
	e := newTermEnv(t, true)
	e.tmux.createErr = errors.New("boom")
	c, _, err := e.dial(t, e.http.URL, true)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err = c.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusInternalError {
		t.Fatalf("read err = %v, want internal error close", err)
	}
	if len(e.ptys.started) != 0 {
		t.Error("PTY started without a viewer")
	}
	// The slot was released.
	waitFor(t, "slot released", func() bool { return len(e.term.slots) == 0 })
}

func TestSweepTerminals(t *testing.T) {
	e := newTermEnv(t, true)
	e.tmux.live = []string{"agentctl-mobileview-dead1", "agentctl-mobileview-dead2", "agentctl-proj", "agentctl-hubview"}
	e.SweepTerminals()
	if got := e.tmux.killedNames(); !slices.Equal(got, []string{"agentctl-mobileview-dead1", "agentctl-mobileview-dead2"}) {
		t.Fatalf("swept %v", got)
	}
}

func TestSameOrigin(t *testing.T) {
	r := httptest.NewRequest("GET", "https://192.168.1.10:8443/v1/x", nil)
	tests := []struct {
		origin string
		want   bool
	}{
		{"https://192.168.1.10:8443", true},
		{"HTTPS://192.168.1.10:8443", true},
		{"https://192.168.1.10:8444", false},
		{"http://192.168.1.10:8443", false},
		{"https://192.168.1.10", false},
		{"https://evil.example", false},
		{"https://user@192.168.1.10:8443", false},
		{"null", false},
		{"", false},
	}
	for _, tt := range tests {
		r.Header.Set("Origin", tt.origin)
		if got := sameOrigin(r); got != tt.want {
			t.Errorf("sameOrigin(%q) = %v, want %v", tt.origin, got, tt.want)
		}
	}
}

func TestTerminalEnv(t *testing.T) {
	got := terminalEnv([]string{"HOME=/h", "TMUX=/tmp/x,1,0", "TMUX_PANE=%1", "TERM=screen", "PATH=/bin"})
	want := []string{"HOME=/h", "PATH=/bin", "TERM=xterm-256color"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestTerminalAssetsServed(t *testing.T) {
	ts := newTestServer(t, config.ServeConfig{})
	_, tok := ts.pairDevice(t, "phone")
	w := do(ts.Handler(), "GET", "/terminal.js", withCookie(tok))
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != ctJS {
		t.Fatalf("terminal.js: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	body := w.Body.String()
	for _, want := range []string{"WebSocket", "binaryType", "read-only", "textContent"} {
		if !strings.Contains(body, want) {
			t.Errorf("terminal.js missing %q", want)
		}
	}
	for _, bad := range []string{"innerHTML", "eval(", "fit()"} {
		if strings.Contains(body, bad) {
			t.Errorf("terminal.js contains %q", bad)
		}
	}
}

func TestTerminalPhoneViewMarker(t *testing.T) {
	t.Run("created on open and removed on client close", func(t *testing.T) {
		e := newTermEnv(t, true)
		c, _ := e.open(t)
		readSize(t, c)
		waitFor(t, "marker", func() bool { return phoneview.Active(e.markers, "@523") })
		_ = c.Close(websocket.StatusNormalClosure, "")
		waitFor(t, "marker removed", func() bool { return !phoneview.Active(e.markers, "@523") })
	})
	t.Run("removed when the PTY exits", func(t *testing.T) {
		e := newTermEnv(t, true)
		c, p := e.open(t)
		readSize(t, c)
		waitFor(t, "marker", func() bool { return phoneview.Active(e.markers, "@523") })
		_ = p.outW.Close()
		waitFor(t, "marker removed", func() bool { return !phoneview.Active(e.markers, "@523") })
	})
	t.Run("removed when the window disappears", func(t *testing.T) {
		e := newTermEnv(t, true)
		c, _ := e.open(t)
		readSize(t, c)
		waitFor(t, "marker", func() bool { return phoneview.Active(e.markers, "@523") })
		e.tmux.mu.Lock()
		e.tmux.sizeErr = errors.New("gone")
		e.tmux.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for {
			if _, _, err := c.Read(ctx); err != nil {
				break
			}
		}
		waitFor(t, "marker removed", func() bool { return !phoneview.Active(e.markers, "@523") })
	})
	t.Run("removed on server close", func(t *testing.T) {
		e := newTermEnv(t, true)
		c, _ := e.open(t)
		readSize(t, c)
		waitFor(t, "marker", func() bool { return phoneview.Active(e.markers, "@523") })
		e.Close()
		if phoneview.Active(e.markers, "@523") {
			t.Error("marker survived server close")
		}
	})
	t.Run("never created when the viewer cannot be made", func(t *testing.T) {
		e := newTermEnv(t, true)
		e.tmux.createErr = errors.New("boom")
		c, _, err := e.dial(t, e.http.URL, true)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _, _ = c.Read(ctx)
		waitFor(t, "slot released", func() bool { return len(e.term.slots) == 0 })
		if phoneview.Active(e.markers, "@523") {
			t.Error("marker written without a viewer")
		}
	})
	t.Run("overlapping connections keep it until both close", func(t *testing.T) {
		e := newTermEnv(t, true)
		c1, _ := e.open(t)
		readSize(t, c1)
		c2, _ := e.open(t)
		readSize(t, c2)
		waitFor(t, "two markers", func() bool {
			entries, _ := os.ReadDir(filepath.Join(e.markers, "@523"))
			return len(entries) == 2
		})
		_ = c1.Close(websocket.StatusNormalClosure, "")
		waitFor(t, "one marker", func() bool {
			entries, _ := os.ReadDir(filepath.Join(e.markers, "@523"))
			return len(entries) == 1
		})
		if !phoneview.Active(e.markers, "@523") {
			t.Fatal("marker dropped while one connection is still open")
		}
		_ = c2.Close(websocket.StatusNormalClosure, "")
		waitFor(t, "marker removed", func() bool { return !phoneview.Active(e.markers, "@523") })
	})
}

func TestSweepTerminalsClearsStaleMarkers(t *testing.T) {
	e := newTermEnv(t, true)
	if err := phoneview.Add(e.markers, "@523", tmux.MobileViewerSessionPrefix+"stale"); err != nil {
		t.Fatal(err)
	}
	e.SweepTerminals()
	if phoneview.Active(e.markers, "@523") {
		t.Fatal("stale marker survived startup sweep")
	}
}
