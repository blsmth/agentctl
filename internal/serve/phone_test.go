package serve

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/artifacts"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/remoteapproval"
	"github.com/blsmth/agentctl/internal/servectl"
)

// setPhone flips the switch the way `agentctl serve stop|start` does.
func setPhone(t *testing.T, dir string, on bool) {
	t.Helper()
	if err := servectl.SetPhone(dir, on); err != nil {
		t.Fatal(err)
	}
}

func TestPhoneOffGatesAppRoutes(t *testing.T) {
	e := newWriteEnv(t, true)
	cookie := withCookie(e.tok)
	setPhone(t, e.dir, false)

	for _, path := range []string{"/v1/fleet", "/v1/events", "/v1/approvals", "/v1/artifacts", "/v1/push/vapid", "/app.js", "/nope"} {
		w := do(e.Handler(), "GET", path, cookie)
		if w.Code != http.StatusServiceUnavailable || decodeError(t, w.Body.String()).Code != codePhoneOff {
			t.Errorf("GET %s with phone off = %d %s, want 503 phone_off", path, w.Code, w.Body)
		}
	}
	for _, path := range []string{"/v1/presence", winPath + "/reply", "/v1/dispatch"} {
		w := e.post(path, `{}`)
		if w.Code != http.StatusServiceUnavailable || decodeError(t, w.Body.String()).Code != codePhoneOff {
			t.Errorf("POST %s with phone off = %d %s, want 503 phone_off", path, w.Code, w.Body)
		}
	}
	e.inj.mu.Lock()
	injected := len(e.inj.got)
	e.inj.mu.Unlock()
	if injected != 0 {
		t.Errorf("a write reached tmux with phone off (%d injections)", injected)
	}
	// The app shell is the self-contained "phone access is off" page.
	w := do(e.Handler(), "GET", "/", cookie, withHeader("Accept", "text/html"))
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "Phone access is off") ||
		w.Header().Get("Content-Security-Policy") != cspHTML {
		t.Errorf("GET / with phone off = %d %q", w.Code, w.Body)
	}
	// An unpaired browser still learns only that it is not paired.
	if w := do(e.Handler(), "GET", "/v1/fleet"); w.Code != http.StatusUnauthorized {
		t.Errorf("unpaired with phone off = %d", w.Code)
	}
	// Pairing is refused, before the code is consumed.
	if w := do(e.Handler(), "GET", "/pair?code=x"); w.Code != http.StatusServiceUnavailable {
		t.Errorf("/pair with phone off = %d", w.Code)
	}
	if _, err := e.OpenPairing("phone"); err == nil || !strings.Contains(err.Error(), "phone access is off") {
		t.Errorf("OpenPairing with phone off = %v", err)
	}
	var hz healthz
	if err := json.Unmarshal(do(e.Handler(), "GET", "/healthz").Body.Bytes(), &hz); err != nil || !hz.OK || !hz.PhoneOff {
		t.Errorf("healthz with phone off = %+v %v", hz, err)
	}

	// Back on: no restart needed.
	setPhone(t, e.dir, true)
	if w := do(e.Handler(), "GET", "/v1/push/vapid", cookie); w.Code != http.StatusOK {
		t.Errorf("vapid with phone on = %d %s", w.Code, w.Body)
	}
	if body := do(e.Handler(), "GET", "/healthz").Body.String(); strings.Contains(body, "phone_off") {
		t.Errorf("healthz with phone on = %s", body)
	}
}

// Artifact links are the point of keeping serve up with the phone off.
func TestPhoneOffKeepsArtifacts(t *testing.T) {
	ae := newArtifactEnv(t, false)
	site := ae.add(t, map[string]string{"index.html": `<link href="a.css">`, "a.css": "x"}, artifacts.AddOptions{})
	legacy := ae.addLegacy(t, map[string]string{"index.html": "<p>L</p>"}, artifacts.AddOptions{})
	setPhone(t, ae.dir, false)

	if w := do(ae.Handler(), "GET", "/a/"+site.ID+"/latest/"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "a.css") {
		t.Errorf("capability entry with phone off = %d %s", w.Code, w.Body)
	}
	if w := do(ae.Handler(), "GET", "/a/"+site.ID+"/latest/a.css"); w.Code != http.StatusOK || w.Body.String() != "x" {
		t.Errorf("capability asset with phone off = %d %s", w.Code, w.Body)
	}
	// A legacy id still takes the device cookie to its token URL.
	w := ae.get("/a/" + legacy.ID + "/latest/")
	loc := w.Header().Get("Location")
	if w.Code != http.StatusFound || !strings.HasPrefix(loc, "/a/t/") {
		t.Fatalf("legacy with cookie and phone off = %d %q", w.Code, loc)
	}
	if w := do(ae.Handler(), "GET", loc); w.Code != http.StatusOK || w.Body.String() != "<p>L</p>" {
		t.Errorf("token URL with phone off = %d %q", w.Code, w.Body)
	}
}

func TestPhoneOffMutesPush(t *testing.T) {
	posted := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { posted <- r.Header.Get("Title") }))
	defer srv.Close()
	fp := &fakePusher{}
	ts := newTestServerWith(t, config.ServeConfig{NtfyTopic: "topic", NtfyDetails: true}, func(o *Options) { o.Pusher = fp; o.NtfyBase = srv.URL })
	d, _ := ts.pairDevice(t, "phone")
	ts.subs.Set(d.ID, testSub("phone"), ts.clock.Now()) //nolint:errcheck
	setPhone(t, ts.dir, false)

	ts.sender.Broadcast(context.Background(), Message{Title: "muted", Kind: "done"})
	if got := fp.endpoints(); len(got) != 0 {
		t.Errorf("Broadcast with phone off pushed to %v", got)
	}
	if lp := ts.sender.LastPush(); lp == nil || lp.Result != "phone access off" {
		t.Errorf("last push = %+v", lp)
	}
	if err := ts.sender.SendTo(context.Background(), d.ID, Message{Title: "muted"}); !errors.Is(err, errPhoneOff) {
		t.Errorf("SendTo with phone off = %v", err)
	}

	ts.pushQ.deliver(context.Background(), Message{Title: "muted", Kind: "done"})
	setPhone(t, ts.dir, true)
	ts.pushQ.deliver(context.Background(), Message{Title: "heard", Kind: "done"})
	select {
	case got := <-posted:
		if got != "heard" {
			t.Errorf("ntfy posted %q with phone off", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ntfy not posted with phone on")
	}
	if got := fp.endpoints(); len(got) != 1 {
		t.Errorf("web push calls = %v, want only the one with phone on", got)
	}
}

func TestPhoneOffRefusesApprovals(t *testing.T) {
	e := newApprovalEnv(t, true, nil)
	setPhone(t, e.dir, false)
	req := e.request(t)
	conn, err := net.Dial("unix", e.sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	a, err := remoteapproval.Start(conn, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := waitDecision(t, a); err == nil || !strings.Contains(err.Error(), "phone access is off") {
		t.Fatalf("Wait with phone off = %v, want a refusal", err)
	}
	if got := e.pusher.endpoints(); len(got) != 0 {
		t.Errorf("approval pushed with phone off: %v", got)
	}
}

// Switching the phone off ends an open approval with an error, so the hook
// stops waiting on the phone and the laptop popup alone answers.
func TestPhoneOffEndsOpenApproval(t *testing.T) {
	e := newApprovalEnv(t, true, nil)
	req := e.request(t)
	a := e.open(t, req)
	setPhone(t, e.dir, false)
	e.phone.Off() // what the poll does within a second
	if _, err := waitDecision(t, a); err == nil || !strings.Contains(err.Error(), "turned off") {
		t.Fatalf("Wait after phone off = %v, want an error", err)
	}
	waitAudit(t, e, "approval_resolved", outcomePhoneOff)
}

func TestPhoneOffEndsSSE(t *testing.T) {
	e := newReadEnv(t)
	srv := httptest.NewServer(e.Handler())
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/v1/events", nil)
	req.AddCookie(&http.Cookie{Name: DeviceCookie, Value: e.tok})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	r := &sseReader{sc: bufio.NewScanner(resp.Body)}
	if ev, ok := r.next(t); !ok || ev.name != "fleet" {
		t.Fatalf("first event = %+v", ev)
	}
	setPhone(t, e.dir, false)
	e.phone.Off()
	done := make(chan struct{})
	go func() {
		for {
			if _, ok := r.next(t); !ok {
				close(done)
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stream still open after phone off")
	}
}

func TestPhoneSwitchChannel(t *testing.T) {
	dir := t.TempDir()
	p := newPhoneSwitch(dir)
	c := p.turnedOff()
	if p.Off() {
		t.Fatal("off with no file")
	}
	setPhone(t, dir, false)
	if !p.Off() {
		t.Fatal("on with the file")
	}
	select {
	case <-c:
	default:
		t.Fatal("channel not closed on turning off")
	}
	setPhone(t, dir, true)
	p.Off()
	select {
	case <-p.turnedOff():
		t.Fatal("new channel closed while on")
	default:
	}
	var nilSwitch *phoneSwitch
	if nilSwitch.Off() || nilSwitch.turnedOff() != nil {
		t.Error("nil switch is not always on")
	}
}
