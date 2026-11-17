package serve

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/buildid"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/remoteapproval"
)

const secretPrompt = "TOP-SECRET-PROMPT-TEXT do the migration"

// approvalEnv is a write-enabled server with its control socket on a real
// unix socket in a temp dir.
type approvalEnv struct {
	*writeEnv
	sock   string
	pusher *fakePusher
}

func newApprovalEnv(t *testing.T, allowWrite bool, mutate func(*Options)) *approvalEnv {
	t.Helper()
	fp := &fakePusher{}
	e := newWriteEnvWith(t, allowWrite, func(o *Options) {
		o.Pusher = fp
		o.Build = buildid.Identity{Dev: 1, Ino: 2, Size: 3, MtimeUnix: 4}
		if mutate != nil {
			mutate(o)
		}
	})
	// Initialize logging before the background goroutines start: its lazy
	// init races with logging.Debug's unguarded flag read otherwise.
	logging.Log("approvals test")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	e.Start(ctx)
	dir := shortTempDir(t)
	ln, err := listenControl(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go serveControl(ln, e.Server)
	return &approvalEnv{writeEnv: e, sock: filepath.Join(dir, ControlSocket), pusher: fp}
}

func (e *approvalEnv) request(t *testing.T) remoteapproval.Request {
	t.Helper()
	id, err := remoteapproval.NewID()
	if err != nil {
		t.Fatal(err)
	}
	return remoteapproval.Request{
		ID: id, Kind: remoteapproval.KindDispatch, AskingAgent: "orch", Session: "agentctl-proj", WindowID: "@523",
		TargetAgent: "go-engineer", Prompt: secretPrompt, Command: "agentctl dispatch --agent go-engineer --prompt-key k",
		Deadline: e.clock.Now().Add(5 * time.Minute),
	}
}

// open plays the hook: dial the control socket and send the request.
func (e *approvalEnv) open(t *testing.T, req remoteapproval.Request) *remoteapproval.Approval {
	t.Helper()
	conn, err := net.Dial("unix", e.sock)
	if err != nil {
		t.Fatal(err)
	}
	a, err := remoteapproval.Start(conn, req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	e.waitPending(t, 1)
	return a
}

func (e *approvalEnv) list(t *testing.T) []approvalView {
	t.Helper()
	w := do(e.Handler(), http.MethodGet, "/v1/approvals", withCookie(e.tok))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /v1/approvals: %d %s", w.Code, w.Body)
	}
	var body struct {
		Approvals []approvalView `json:"approvals"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Approvals
}

func (e *approvalEnv) waitPending(t *testing.T, n int) []approvalView {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := e.list(t)
		if len(got) == n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("pending approvals = %d, want %d", len(got), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (e *approvalEnv) answer(id, decision, hash string, opts ...reqOpt) *httptest.ResponseRecorder {
	return e.post("/v1/approvals/"+id, fmt.Sprintf(`{"decision":%q,"payload_sha256":%q}`, decision, hash), opts...)
}

func waitDecision(t *testing.T, a *remoteapproval.Approval) (remoteapproval.Decision, error) {
	t.Helper()
	type res struct {
		d   remoteapproval.Decision
		err error
	}
	ch := make(chan res, 1)
	go func() { d, err := a.Wait(); ch <- res{d, err} }()
	select {
	case r := <-ch:
		return r.d, r.err
	case <-time.After(5 * time.Second):
		t.Fatal("no decision on the hook's connection")
	}
	return remoteapproval.Decision{}, nil
}

func TestApprovalLifecycle(t *testing.T) {
	for _, decision := range []string{"allow", "deny"} {
		t.Run(decision, func(t *testing.T) {
			e := newApprovalEnv(t, true, nil)
			req := e.request(t)
			a := e.open(t, req)

			got := e.list(t)[0]
			want := remoteapproval.PayloadHash(req)
			if got.ID != req.ID || got.Prompt != secretPrompt || got.Command != req.Command || got.TargetAgent != "go-engineer" ||
				got.AskingAgent != "orch" || got.WindowID != "@523" || got.Project != "proj" || got.Kind != "dispatch" {
				t.Fatalf("listed = %+v", got)
			}
			if got.PayloadSHA256 != want || got.SecondsRemaining != 300 {
				t.Fatalf("hash %q remaining %d", got.PayloadSHA256, got.SecondsRemaining)
			}

			w := e.answer(req.ID, decision, want)
			if w.Code != http.StatusOK {
				t.Fatalf("answer: %d %s", w.Code, w.Body)
			}
			d, err := waitDecision(t, a)
			if err != nil || d.Decision != decision || d.Device != "phone" || d.PayloadSHA256 != want {
				t.Fatalf("hook got %+v, %v", d, err)
			}
			e.waitPending(t, 0)

			// A second answer is refused.
			if w := e.answer(req.ID, "allow", want); w.Code != http.StatusConflict || errCode(t, w) != codeAlreadyAnswered {
				t.Fatalf("second answer: %d %s", w.Code, w.Body)
			}

			lines := e.auditLines(t)
			var actions []string
			for _, l := range lines {
				actions = append(actions, l.Action+":"+l.Result)
			}
			wantActions := []string{"approval_requested:pending", "approval:attempt", "approval:ok", "approval:" + codeAlreadyAnswered}
			if strings.Join(actions, ",") != strings.Join(wantActions, ",") {
				t.Fatalf("audit = %v, want %v", actions, wantActions)
			}
			ok := lines[2]
			if ok.DeviceName != "phone" || ok.Decision != decision || ok.PayloadSHA256 != want || ok.ApprovalID != req.ID ||
				ok.Session != "agentctl-proj" || ok.Window != "@523" || ok.AskingAgent != "orch" {
				t.Fatalf("answered audit line = %+v", ok)
			}
			raw, _ := os.ReadFile(filepath.Join(e.dir, AuditFile))
			if strings.Contains(string(raw), secretPrompt) || strings.Contains(string(raw), "--prompt-key") {
				t.Fatal("audit log contains prompt or command text")
			}
		})
	}
}

func TestApprovalHashMismatch(t *testing.T) {
	e := newApprovalEnv(t, true, nil)
	req := e.request(t)
	a := e.open(t, req)
	other := req
	other.Prompt = "something else"
	w := e.answer(req.ID, "allow", remoteapproval.PayloadHash(other))
	if w.Code != http.StatusConflict || errCode(t, w) != codeApprovalChanged {
		t.Fatalf("mismatch: %d %s", w.Code, w.Body)
	}
	e.waitPending(t, 1) // still pending
	if w := e.answer(req.ID, "deny", remoteapproval.PayloadHash(req)); w.Code != http.StatusOK {
		t.Fatalf("correct answer: %d %s", w.Code, w.Body)
	}
	if d, err := waitDecision(t, a); err != nil || d.Decision != "deny" {
		t.Fatalf("hook got %+v %v", d, err)
	}
}

func TestApprovalUnknownAndBadBody(t *testing.T) {
	e := newApprovalEnv(t, true, nil)
	if w := e.answer(strings.Repeat("b", 32), "allow", "x"); w.Code != http.StatusNotFound {
		t.Fatalf("unknown: %d", w.Code)
	}
	if w := e.answer("not-an-id", "allow", "x"); w.Code != http.StatusNotFound {
		t.Fatalf("bad id: %d", w.Code)
	}
	req := e.request(t)
	e.open(t, req)
	if w := e.answer(req.ID, "yes", remoteapproval.PayloadHash(req)); w.Code != http.StatusBadRequest {
		t.Fatalf("bad decision: %d", w.Code)
	}
	if w := e.post("/v1/approvals/"+req.ID, `{"decision":"allow","payload_sha256":"x","extra":1}`); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", w.Code)
	}
	e.waitPending(t, 1)
}

func TestApprovalConnectionDropRemoves(t *testing.T) {
	e := newApprovalEnv(t, true, nil)
	req := e.request(t)
	a := e.open(t, req)
	a.Close()
	e.waitPending(t, 0)
	if w := e.answer(req.ID, "allow", remoteapproval.PayloadHash(req)); w.Code != http.StatusConflict {
		t.Fatalf("answer after drop: %d %s", w.Code, w.Body)
	}
	waitAudit(t, e, "approval_resolved", outcomeGone)
}

func TestApprovalLaptopCancel(t *testing.T) {
	e := newApprovalEnv(t, true, nil)
	req := e.request(t)
	a := e.open(t, req)
	a.Cancel(remoteapproval.ReasonLaptop)
	e.waitPending(t, 0)
	waitAudit(t, e, "approval_resolved", remoteapproval.ReasonLaptop)
}

func waitAudit(t *testing.T, e *approvalEnv, action, result string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, l := range e.auditLines(t) {
			if l.Action == action && l.Result == result {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no audit line %s:%s in %+v", action, result, e.auditLines(t))
}

func TestApprovalRequiresWriteAndCSRF(t *testing.T) {
	t.Run("allow_write false", func(t *testing.T) {
		e := newApprovalEnv(t, false, nil)
		req := e.request(t)
		e.open(t, req)
		w := e.answer(req.ID, "allow", remoteapproval.PayloadHash(req))
		if w.Code != http.StatusForbidden || errCode(t, w) != codeWriteDisabled {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		e.waitPending(t, 1)
	})
	t.Run("csrf", func(t *testing.T) {
		e := newApprovalEnv(t, true, nil)
		req := e.request(t)
		e.open(t, req)
		w := e.answer(req.ID, "allow", remoteapproval.PayloadHash(req), withoutHeader(CSRFHeader))
		if w.Code != http.StatusForbidden || errCode(t, w) != codeCSRF {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		e.waitPending(t, 1)
	})
}

func TestApprovalRefusesBadRequests(t *testing.T) {
	e := newApprovalEnv(t, true, nil)
	for name, mutate := range map[string]func(*remoteapproval.Request){
		"past deadline": func(r *remoteapproval.Request) { r.Deadline = e.clock.Now().Add(-time.Second) },
		"bad kind":      func(r *remoteapproval.Request) { r.Kind = "rm" },
	} {
		t.Run(name, func(t *testing.T) {
			req := e.request(t)
			mutate(&req)
			conn, err := net.Dial("unix", e.sock)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(req)
			b = append([]byte(`{"op":"approval_request",`), b[1:]...)
			_, _ = conn.Write(append(b, '\n'))
			var d remoteapproval.Decision
			if err := json.NewDecoder(conn).Decode(&d); err != nil || d.Error == "" || d.Decision != "" {
				t.Fatalf("got %+v %v", d, err)
			}
			_ = conn.Close()
		})
	}
	if n := len(e.list(t)); n != 0 {
		t.Fatalf("%d pending", n)
	}
}

// No control-socket op decides anything.
func TestControlOpsCannotApprove(t *testing.T) {
	e := newApprovalEnv(t, true, nil)
	req := e.request(t)
	a := e.open(t, req)
	for _, line := range []string{
		fmt.Sprintf(`{"op":"approval_decision","id":%q,"decision":"allow"}`, req.ID),
		fmt.Sprintf(`{"op":"approval_cancel","id":%q,"reason":"answered_on_laptop"}`, req.ID),
		fmt.Sprintf(`{"op":"approve","id":%q}`, req.ID),
	} {
		conn, err := net.Dial("unix", e.sock)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = conn.Write([]byte(line + "\n"))
		var resp controlResponse
		if err := json.NewDecoder(conn).Decode(&resp); err != nil || resp.Error == "" {
			t.Fatalf("%s: got %+v %v", line, resp, err)
		}
		_ = conn.Close()
	}
	e.waitPending(t, 1)
	// The hook's own connection sees nothing yet.
	ch := make(chan error, 1)
	go func() { _, err := a.Wait(); ch <- err }()
	select {
	case err := <-ch:
		t.Fatalf("hook got a decision: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestControlPing(t *testing.T) {
	e := newApprovalEnv(t, true, nil)
	conn, err := net.Dial("unix", e.sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	info, err := remoteapproval.Ping(conn)
	if err != nil {
		t.Fatal(err)
	}
	if info.PID != os.Getpid() || info.Build.Ino != 2 {
		t.Fatalf("ping = %+v", info)
	}
}

func TestApprovalSSEEvents(t *testing.T) {
	e := newApprovalEnv(t, true, nil)
	srv := httptest.NewServer(e.Handler())
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sreq, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/v1/events", nil)
	sreq.AddCookie(&http.Cookie{Name: DeviceCookie, Value: e.tok})
	resp, err := http.DefaultClient.Do(sreq)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	r := &sseReader{sc: bufio.NewScanner(resp.Body)}
	r.sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	nextNamed := func(name string) sseEvent {
		t.Helper()
		for i := 0; i < 200; i++ {
			ev, ok := r.next(t)
			if !ok {
				t.Fatalf("stream ended waiting for %q", name)
			}
			if ev.name == name {
				return ev
			}
		}
		t.Fatalf("no %q event", name)
		return sseEvent{}
	}
	nextNamed("fleet")

	req := e.request(t)
	a := e.open(t, req)
	ev := nextNamed("approval")
	var v approvalView
	if err := json.Unmarshal([]byte(ev.data), &v); err != nil || v.ID != req.ID || v.PayloadSHA256 != remoteapproval.PayloadHash(req) {
		t.Fatalf("approval event = %s (%v)", ev.data, err)
	}
	if w := e.answer(req.ID, "allow", v.PayloadSHA256); w.Code != http.StatusOK {
		t.Fatalf("answer %d", w.Code)
	}
	if _, err := waitDecision(t, a); err != nil {
		t.Fatal(err)
	}
	ev = nextNamed("approval_resolved")
	var res approvalResolved
	if err := json.Unmarshal([]byte(ev.data), &res); err != nil || res.ID != req.ID || res.Outcome != outcomeAnswered || res.Decision != "allow" {
		t.Fatalf("resolved event = %s", ev.data)
	}

	// A new stream gets pending approvals on connect.
	req2 := e.request(t)
	e.open(t, req2)
	sreq2, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/v1/events", nil)
	sreq2.AddCookie(&http.Cookie{Name: DeviceCookie, Value: e.tok})
	resp2, err := http.DefaultClient.Do(sreq2)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp2.Body.Close() }()
	r = &sseReader{sc: bufio.NewScanner(resp2.Body)}
	r.sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	if ev := nextNamed("approval"); !strings.Contains(ev.data, req2.ID) {
		t.Fatalf("connect snapshot = %s", ev.data)
	}
}

func (f *fakePusher) waitCalls(t *testing.T, n int) []pushCall {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		f.mu.Lock()
		calls := append([]pushCall(nil), f.calls...)
		f.mu.Unlock()
		if len(calls) >= n || time.Now().After(deadline) {
			return calls
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestApprovalPush(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*Options)
		kind     string
		wantPush bool
		wantBody string
	}{
		{"dispatch", nil, remoteapproval.KindDispatch, true, "orch wants to dispatch go-engineer"},
		{"bash", nil, remoteapproval.KindBash, true, "orch wants to run a command"},
		{"quiet hours ignored", func(o *Options) { o.Config.Push.QuietHours = "00:00-23:59" }, remoteapproval.KindDispatch, true, "orch wants to dispatch go-engineer"},
		{"muted project", func(o *Options) { o.Config.Push.MutedProjects = []string{"proj"} }, remoteapproval.KindDispatch, false, ""},
		{"approvals off", func(o *Options) { off := false; o.Config.Push.Approvals = &off }, remoteapproval.KindDispatch, false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newApprovalEnv(t, true, tc.mutate)
			if err := e.subs.Set(e.dev.ID, testSub("phone"), e.clock.Now()); err != nil {
				t.Fatal(err)
			}
			req := e.request(t)
			req.Kind = tc.kind
			e.open(t, req)
			calls := e.pusher.waitCalls(t, 1)
			if !tc.wantPush {
				time.Sleep(50 * time.Millisecond)
				if calls = e.pusher.waitCalls(t, 0); len(calls) != 0 {
					t.Fatalf("pushed: %+v", calls)
				}
				return
			}
			if len(calls) != 1 {
				t.Fatalf("%d pushes", len(calls))
			}
			c := calls[0]
			if c.payload.Title != "Approval needed" || c.payload.Body != tc.wantBody || c.payload.URL != "/#/approvals/"+req.ID {
				t.Fatalf("payload = %+v", c.payload)
			}
			if !c.opts.Urgent || c.opts.TTL != 300 {
				t.Fatalf("opts = %+v", c.opts)
			}
			e.pusher.mu.Lock()
			defer e.pusher.mu.Unlock()
			raw, _ := json.Marshal(c.payload)
			if strings.Contains(string(raw), "SECRET") || strings.Contains(string(raw), "--prompt-key") {
				t.Fatalf("push carries prompt or command text: %s", raw)
			}
		})
	}
}

func TestApprovalPushPayloadBytesHaveNoPrompt(t *testing.T) {
	m := Message{Title: "Approval needed", Body: "orch wants to dispatch go-engineer", URL: approvalURL("abc"), Kind: "approval", TTL: 9}
	if strings.Contains(string(m.payload()), "ttl") || strings.Contains(string(m.payload()), "approval\"") {
		t.Fatalf("payload leaks internals: %s", m.payload())
	}
	if r := redactForNtfy(m); r.Title != "agentctl: approval needed" || r.URL != "" {
		t.Fatalf("ntfy redaction = %+v", r)
	}
}
