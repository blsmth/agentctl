package hooks

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/buildid"
	"github.com/blsmth/agentctl/internal/remoteapproval"
)

// fakeServe plays agentctl serve on the far end of a net.Pipe that openRemote
// hands the hook.
type fakeServe struct {
	t      *testing.T
	reqs   chan remoteapproval.Request
	cancel chan remoteapproval.Cancel
	// dropped is closed when the hook closes its end.
	dropped chan struct{}

	mu   sync.Mutex
	conn net.Conn
	req  remoteapproval.Request
}

func withFakeServe(t *testing.T) *fakeServe {
	t.Helper()
	fs := &fakeServe{t: t, reqs: make(chan remoteapproval.Request, 1), cancel: make(chan remoteapproval.Cancel, 1), dropped: make(chan struct{})}
	orig := openRemote
	t.Cleanup(func() { openRemote = orig })
	openRemote = func(req remoteapproval.Request) (*remoteapproval.Approval, error) {
		client, server := net.Pipe()
		t.Cleanup(func() { _ = server.Close() })
		fs.mu.Lock()
		fs.conn = server
		fs.mu.Unlock()
		go fs.serve(server)
		return remoteapproval.Start(client, req)
	}
	return fs
}

func (fs *fakeServe) serve(c net.Conn) {
	br := bufio.NewReader(c)
	line, err := br.ReadBytes('\n')
	if err != nil {
		return
	}
	var req remoteapproval.Request
	_ = json.Unmarshal(line, &req)
	fs.mu.Lock()
	fs.req = req
	fs.mu.Unlock()
	fs.reqs <- req
	line, err = br.ReadBytes('\n')
	if err == nil {
		var cn remoteapproval.Cancel
		_ = json.Unmarshal(line, &cn)
		fs.cancel <- cn
		_, _ = br.ReadBytes('\n')
	}
	close(fs.dropped)
}

// waitRequest returns the request the hook sent.
func (fs *fakeServe) waitRequest() remoteapproval.Request {
	fs.t.Helper()
	select {
	case r := <-fs.reqs:
		return r
	case <-time.After(5 * time.Second):
		fs.t.Fatal("hook never opened a remote approval")
	}
	return remoteapproval.Request{}
}

// send writes v to the hook as the decision line.
func (fs *fakeServe) send(v any) {
	fs.mu.Lock()
	c := fs.conn
	fs.mu.Unlock()
	_ = json.NewEncoder(c).Encode(v)
}

func (fs *fakeServe) answer(decision string) {
	fs.mu.Lock()
	req := fs.req
	fs.mu.Unlock()
	fs.send(remoteapproval.Decision{Decision: decision, Device: "iphone", PayloadSHA256: remoteapproval.PayloadHash(req)})
}

// blockingRunner is a popup that stays open until its context ends.
type blockingRunner struct {
	calls     atomic.Int32
	cancelled atomic.Int32
	clients   chan string
}

func installBlockingRunner(t *testing.T) *blockingRunner {
	t.Helper()
	br := &blockingRunner{clients: make(chan string, 8)}
	dispatchPopupRunner = func(ctx context.Context, client string, _, _ int, _ string) error {
		br.calls.Add(1)
		br.clients <- client
		<-ctx.Done()
		br.cancelled.Add(1)
		return ctx.Err()
	}
	return br
}

// recordCloser records popupCloser calls.
func recordCloser(t *testing.T) *[]string {
	t.Helper()
	var mu sync.Mutex
	closed := &[]string{}
	popupCloser = func(c string) error {
		mu.Lock()
		defer mu.Unlock()
		*closed = append(*closed, c)
		return nil
	}
	return closed
}

func remoteNotice() popupNotice {
	return popupNotice{Agent: "orch", Tool: "dispatch", Action: "dispatch go-engineer", Remote: &remoteDetails{
		Kind: remoteapproval.KindDispatch, AskingAgent: "orch", TargetAgent: "go-engineer",
		Prompt: "the full prompt", Command: "agentctl dispatch --agent go-engineer --prompt-key k",
	}}
}

type gateOut struct {
	approved, decided bool
	reason            string
}

func runRemoteGate(t *testing.T, target popupTarget) <-chan gateOut {
	t.Helper()
	out := make(chan gateOut, 1)
	go func() {
		a, d, r := runPopupWithDeadline(target, "dispatch", "test", 10, 10, filepath.Join(t.TempDir(), "args"), remoteNotice())
		out <- gateOut{a, d, r}
	}()
	return out
}

func waitGate(t *testing.T, ch <-chan gateOut) gateOut {
	t.Helper()
	select {
	case g := <-ch:
		return g
	case <-time.After(10 * time.Second):
		t.Fatal("gate never returned")
	}
	return gateOut{}
}

var laptop = popupTarget{Client: "/dev/ttys001", Viewing: true, WindowID: "@1"}

func TestRemoteApproveClosesOpenPopup(t *testing.T) {
	stubPopupSeams(t, 5*time.Second)
	fs := withFakeServe(t)
	br := installBlockingRunner(t)
	closed := recordCloser(t)

	out := runRemoteGate(t, laptop)
	req := fs.waitRequest()
	if req.Prompt != "the full prompt" || req.TargetAgent != "go-engineer" || req.Kind != "dispatch" || req.AskingAgent != "orch" {
		t.Fatalf("request = %+v", req)
	}
	if req.Deadline.IsZero() || time.Until(req.Deadline) > 5*time.Second {
		t.Fatalf("deadline %v not bound to the popup deadline", req.Deadline)
	}
	if c := <-br.clients; c != laptop.Client {
		t.Fatalf("popup on %q", c)
	}
	fs.answer(remoteapproval.Allow)
	g := waitGate(t, out)
	if !g.approved || !g.decided || g.reason != "" {
		t.Fatalf("got %+v, want phone approval", g)
	}
	if lastApproval.Source != approvalSourcePhone || lastApproval.TimedOut {
		t.Fatalf("lastApproval = %+v, want phone source", lastApproval)
	}
	if br.cancelled.Load() != 1 {
		t.Fatal("popup runner was not cancelled")
	}
	if len(*closed) != 1 || (*closed)[0] != laptop.Client {
		t.Fatalf("popup closes = %v, want one on %s", *closed, laptop.Client)
	}
}

func TestRemoteDeny(t *testing.T) {
	stubPopupSeams(t, 5*time.Second)
	fs := withFakeServe(t)
	installBlockingRunner(t)
	closed := recordCloser(t)
	out := runRemoteGate(t, laptop)
	fs.waitRequest()
	fs.answer(remoteapproval.Deny)
	g := waitGate(t, out)
	if g.approved || !g.decided || g.reason != "" {
		t.Fatalf("got %+v, want phone deny", g)
	}
	if len(*closed) != 1 {
		t.Fatalf("popup closes = %v", *closed)
	}
}

func TestPopupAnswersFirstCancelsRemote(t *testing.T) {
	for _, verdict := range []string{"allow", "deny"} {
		t.Run(verdict, func(t *testing.T) {
			stubPopupSeams(t, 5*time.Second)
			fs := withFakeServe(t)
			release := make(chan struct{})
			dispatchPopupRunner = func(_ context.Context, _ string, _, _ int, argsFile string) error {
				<-release
				return WritePopupResult(popupResultPath(argsFile), verdict)
			}
			out := runRemoteGate(t, laptop)
			fs.waitRequest()
			close(release)
			g := waitGate(t, out)
			if g.approved != (verdict == "allow") || !g.decided || g.reason != "" {
				t.Fatalf("got %+v", g)
			}
			select {
			case c := <-fs.cancel:
				if c.Reason != remoteapproval.ReasonLaptop || c.Op != remoteapproval.OpCancel {
					t.Fatalf("cancel = %+v", c)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("serve was not told the laptop answered")
			}
		})
	}
}

func TestRemoteDeadlineDeniesAndClosesBoth(t *testing.T) {
	stubPopupSeams(t, 80*time.Millisecond)
	fs := withFakeServe(t)
	installBlockingRunner(t)
	closed := recordCloser(t)
	out := runRemoteGate(t, laptop)
	fs.waitRequest()
	g := waitGate(t, out)
	if g.approved || g.decided || !strings.Contains(g.reason, "timed out") {
		t.Fatalf("got %+v, want timeout deny", g)
	}
	if len(*closed) != 1 {
		t.Fatalf("popup closes = %v, want 1", *closed)
	}
	select {
	case c := <-fs.cancel:
		if c.Reason != remoteapproval.ReasonTimeout {
			t.Fatalf("cancel reason %q", c.Reason)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("remote approval not cancelled at the deadline")
	}
}

// A socket that accepts the request but never answers must not stall past
// the deadline or allow anything: phone-only, so nothing else can answer.
func TestRemoteSilentServeDeadlineDenies(t *testing.T) {
	stubPopupSeams(t, 80*time.Millisecond)
	fs := withFakeServe(t)
	br := installBlockingRunner(t)
	out := runRemoteGate(t, popupTarget{})
	fs.waitRequest()
	g := waitGate(t, out)
	if g.approved || g.decided || !strings.Contains(g.reason, "timed out") {
		t.Fatalf("got %+v, want timeout deny", g)
	}
	if br.calls.Load() != 0 {
		t.Fatal("popup shown with no laptop client")
	}
	<-fs.dropped
}

func TestServeNotRunningUnchanged(t *testing.T) {
	stubPopupSeams(t, 5*time.Second)
	// TestMain's openRemote already reports "not running".
	calls := 0
	dispatchPopupRunner = func(_ context.Context, client string, _, _ int, argsFile string) error {
		calls++
		if client != "" {
			t.Errorf("client %q", client)
		}
		return WritePopupResult(popupResultPath(argsFile), "allow")
	}
	// Even with no laptop client the popup runs as before.
	g := waitGate(t, runRemoteGate(t, popupTarget{}))
	if !g.approved || !g.decided || calls != 1 {
		t.Fatalf("got %+v calls=%d", g, calls)
	}
}

func TestNoLaptopClientPhoneOnly(t *testing.T) {
	stubPopupSeams(t, 5*time.Second)
	fs := withFakeServe(t)
	br := installBlockingRunner(t)
	closed := recordCloser(t)
	out := runRemoteGate(t, popupTarget{WindowID: "@1"})
	fs.waitRequest()
	fs.answer(remoteapproval.Allow)
	g := waitGate(t, out)
	if !g.approved || !g.decided {
		t.Fatalf("got %+v", g)
	}
	if br.calls.Load() != 0 || len(*closed) != 0 {
		t.Fatalf("popup shown/closed with no laptop client: calls=%d closes=%v", br.calls.Load(), *closed)
	}
}

func TestRemoteErrorNeverAllows(t *testing.T) {
	wrongHash := strings.Repeat("0", 64)
	tests := []struct {
		name  string
		reply func(fs *fakeServe)
	}{
		{"wrong hash allow", func(fs *fakeServe) {
			fs.send(remoteapproval.Decision{Decision: remoteapproval.Allow, PayloadSHA256: wrongHash})
		}},
		{"missing hash allow", func(fs *fakeServe) { fs.send(remoteapproval.Decision{Decision: remoteapproval.Allow}) }},
		{"error line", func(fs *fakeServe) { fs.send(remoteapproval.Decision{Error: "boom"}) }},
		{"garbage", func(fs *fakeServe) { fs.send("allow") }},
		{"connection drop", func(fs *fakeServe) {
			fs.mu.Lock()
			_ = fs.conn.Close()
			fs.mu.Unlock()
		}},
	}
	for _, tc := range tests {
		for _, mode := range []string{"laptop", "phone-only"} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				stubPopupSeams(t, 150*time.Millisecond)
				fs := withFakeServe(t)
				br := installBlockingRunner(t)
				recordCloser(t)
				target := laptop
				if mode == "phone-only" {
					target = popupTarget{}
				}
				out := runRemoteGate(t, target)
				fs.waitRequest()
				tc.reply(fs)
				g := waitGate(t, out)
				if g.approved || g.decided {
					t.Fatalf("remote error produced a verdict: %+v", g)
				}
				if !strings.Contains(g.reason, "timed out") {
					t.Fatalf("reason %q, want the popup deadline deny", g.reason)
				}
				if br.calls.Load() == 0 {
					t.Fatal("popup did not take over after the remote error")
				}
			})
		}
	}
}

// A remote error while the laptop popup is open leaves the popup in charge.
func TestRemoteErrorPopupStillDecides(t *testing.T) {
	stubPopupSeams(t, 5*time.Second)
	fs := withFakeServe(t)
	release := make(chan struct{})
	dispatchPopupRunner = func(_ context.Context, _ string, _, _ int, argsFile string) error {
		<-release
		return WritePopupResult(popupResultPath(argsFile), "allow")
	}
	out := runRemoteGate(t, laptop)
	fs.waitRequest()
	fs.send(remoteapproval.Decision{Decision: remoteapproval.Allow, PayloadSHA256: "bad"})
	time.Sleep(20 * time.Millisecond)
	close(release)
	g := waitGate(t, out)
	if !g.approved || !g.decided {
		t.Fatalf("got %+v, want the laptop verdict", g)
	}
}

// The real opener must refuse a socket whose ping pid does not match the
// pidfile, and never send it the approval.
func TestFakeSocketGuardRejectsPIDMismatch(t *testing.T) {
	stubPopupSeams(t, 5*time.Second)
	origOpen, origGuard := openRemote, remoteGuard
	t.Cleanup(func() { openRemote, remoteGuard = origOpen, origGuard })
	openRemote = openRemoteApproval

	dir := t.TempDir()
	if err := WritePopupResult(filepath.Join(dir, remoteapproval.PIDFile), "4242\n"); err != nil {
		t.Fatal(err)
	}
	self := buildid.Identity{Dev: 1, Ino: 2, Size: 3, MtimeUnix: 4}
	var sawRequest atomic.Bool
	remoteGuard = func() (remoteapproval.Guard, error) {
		return remoteapproval.Guard{
			Dir: dir,
			Dial: func(string, time.Duration) (net.Conn, error) {
				client, server := net.Pipe()
				go func() {
					defer func() { _ = server.Close() }()
					line, err := bufio.NewReader(server).ReadBytes('\n')
					if err != nil {
						return
					}
					if strings.Contains(string(line), remoteapproval.OpRequest) {
						sawRequest.Store(true)
						return
					}
					// An impostor: right build, wrong pid.
					_ = json.NewEncoder(server).Encode(map[string]any{"ping": remoteapproval.PingInfo{PID: 999, Build: self}})
				}()
				return client, nil
			},
			Self:    func() (buildid.Identity, error) { return self, nil },
			Running: func(string) (int, bool) { return 4242, true },
			PeerPID: func(net.Conn) (int, bool) { return 0, false },
		}, nil
	}
	calls := 0
	dispatchPopupRunner = func(_ context.Context, _ string, _, _ int, argsFile string) error {
		calls++
		return WritePopupResult(popupResultPath(argsFile), "deny")
	}
	g := waitGate(t, runRemoteGate(t, laptop))
	if g.approved || !g.decided || calls != 1 {
		t.Fatalf("got %+v calls=%d; want the laptop popup alone", g, calls)
	}
	if sawRequest.Load() {
		t.Fatal("approval request sent to an unverified socket")
	}
}

// Verdicts from the laptop and the phone that arrive together resolve
// conservatively: approved only if both said allow.
func TestConflictingVerdictsDeny(t *testing.T) {
	tests := []struct {
		name          string
		phone, laptop string
		phoneFirst    bool
		wantApproved  bool
	}{
		{"phone allow, laptop deny (phone first)", remoteapproval.Allow, "deny", true, false},
		{"phone deny, laptop allow (phone first)", remoteapproval.Deny, "allow", true, false},
		{"phone allow, laptop deny (both pending)", remoteapproval.Allow, "deny", false, false},
		{"phone deny, laptop allow (both pending)", remoteapproval.Deny, "allow", false, false},
		{"both allow (phone first)", remoteapproval.Allow, "allow", true, true},
		{"both allow (both pending)", remoteapproval.Allow, "allow", false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stubPopupSeams(t, 5*time.Second)
			fs := withFakeServe(t)
			recordCloser(t)
			phoneSent := make(chan struct{})
			dispatchPopupRunner = func(ctx context.Context, _ string, _, _ int, argsFile string) error {
				if tc.phoneFirst {
					// The laptop verdict lands just as the phone's answer
					// stops the popup.
					<-ctx.Done()
				} else {
					// Both arrive before the gate looks at either.
					<-phoneSent
					time.Sleep(50 * time.Millisecond)
				}
				return WritePopupResult(popupResultPath(argsFile), tc.laptop)
			}
			out := runRemoteGate(t, laptop)
			fs.waitRequest()
			fs.answer(tc.phone)
			close(phoneSent)
			g := waitGate(t, out)
			if g.approved != tc.wantApproved || !g.decided || g.reason != "" {
				t.Fatalf("got %+v, want approved=%v", g, tc.wantApproved)
			}
		})
	}
}

// At the deadline, a laptop verdict and a phone decision that are both
// pending also combine conservatively (whichever branch sees them first).
func TestConflictAtDeadlineDenies(t *testing.T) {
	for _, tc := range []struct{ phone, laptop string }{
		{remoteapproval.Deny, "allow"},
		{remoteapproval.Allow, "deny"},
	} {
		t.Run(tc.phone+"/"+tc.laptop, func(t *testing.T) {
			stubPopupSeams(t, 5*time.Second)
			fs := withFakeServe(t)
			recordCloser(t)
			dispatchPopupRunner = func(ctx context.Context, _ string, _, _ int, argsFile string) error {
				<-ctx.Done()
				// The phone answers and the laptop verdict lands while the
				// deadline is being handled.
				go fs.answer(tc.phone)
				time.Sleep(50 * time.Millisecond)
				return WritePopupResult(popupResultPath(argsFile), tc.laptop)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel() // the deadline has passed
			req := remoteapproval.Request{ID: strings.Repeat("c", 32), Kind: "dispatch", Command: "x", Deadline: time.Now().Add(time.Minute)}
			a, err := openRemote(req)
			if err != nil {
				t.Fatal(err)
			}
			fs.waitRequest()
			args := filepath.Join(t.TempDir(), "args")
			run := func(c context.Context) popupResult { return runPopupLoop(c, laptop, "test", 1, 1, args) }
			deny := func(bool) (bool, bool, string) { return false, false, "timed out" }
			if approved, _, _ := raceRemote(ctx, laptop, "test", a, run, deny); approved {
				t.Fatal("conflicting verdicts at the deadline approved")
			}
		})
	}
}
