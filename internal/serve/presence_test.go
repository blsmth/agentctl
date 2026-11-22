package serve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/config"
)

func TestPresenceTracker(t *testing.T) {
	clock := newClock()
	p := NewPresence(clock.Now)
	if p.Foreground("d") {
		t.Error("unknown device counted as foreground")
	}
	p.Report("d", true)
	if !p.Foreground("d") {
		t.Error("visible report not foreground")
	}
	clock.Advance(presenceTTL - time.Second)
	if !p.Foreground("d") {
		t.Error("report inside TTL not foreground")
	}
	p.Report("d", true) // heartbeat refreshes
	clock.Advance(presenceTTL - time.Second)
	if !p.Foreground("d") {
		t.Error("heartbeat did not extend TTL")
	}
	clock.Advance(2 * time.Second)
	if p.Foreground("d") {
		t.Error("stale report past TTL counted as foreground")
	}
	p.Report("d", true)
	p.Report("d", false)
	if p.Foreground("d") {
		t.Error("hidden report still foreground")
	}
	p.Report("d", true)
	p.Forget("d")
	if p.Foreground("d") {
		t.Error("forgotten device still foreground")
	}
}

func TestBroadcastSkipsForegroundDevices(t *testing.T) {
	ts := newTestServer(t, config.ServeConfig{})
	inApp, _ := ts.pairDevice(t, "phone")
	away, _ := ts.pairDevice(t, "tablet")
	ts.subs.Set(inApp.ID, testSub("phone"), ts.clock.Now()) //nolint:errcheck
	ts.subs.Set(away.ID, testSub("tablet"), ts.clock.Now()) //nolint:errcheck
	p := NewPresence(ts.clock.Now)
	fp := &fakePusher{}
	s := NewSender(ts.devices, ts.subs, fp)
	s.SetPresence(p)

	p.Report(inApp.ID, true)
	s.Broadcast(context.Background(), Message{Kind: "blocked"})
	if got := fp.endpoints(); len(got) != 1 || got[0] != "https://web.push.apple.com/tablet" {
		t.Errorf("pushed to %v, want only tablet", got)
	}
	if lp := s.LastPush(); lp == nil || lp.Result != "sent to 1/1, 1 in app" {
		t.Errorf("last push = %+v", lp)
	}

	// SendTo is never suppressed.
	fp.calls = nil
	if err := s.SendTo(context.Background(), inApp.ID, Message{Kind: "test"}); err != nil || len(fp.calls) != 1 {
		t.Errorf("SendTo while in app: err=%v calls=%d", err, len(fp.calls))
	}

	// Past the TTL the device is background again.
	fp.calls = nil
	ts.clock.Advance(presenceTTL + time.Second)
	s.Broadcast(context.Background(), Message{Kind: "done"})
	if len(fp.calls) != 2 {
		t.Errorf("calls after TTL = %d, want 2", len(fp.calls))
	}

	// Everyone in app: nothing sent.
	fp.calls = nil
	p.Report(inApp.ID, true)
	p.Report(away.ID, true)
	s.Broadcast(context.Background(), Message{Kind: "approval"})
	if len(fp.calls) != 0 {
		t.Errorf("calls = %d, want 0", len(fp.calls))
	}
	if lp := s.LastPush(); lp == nil || lp.Result != "sent to 0/0, 2 in app" {
		t.Errorf("last push = %+v", lp)
	}
}

func TestBroadcastNoPresenceAndNoDevices(t *testing.T) {
	ts := newTestServer(t, config.ServeConfig{})
	s := NewSender(ts.devices, ts.subs, &fakePusher{})
	s.SetPresence(NewPresence(ts.clock.Now))
	s.Broadcast(context.Background(), Message{Kind: "done"})
	if lp := s.LastPush(); lp == nil || lp.Result != "no subscribed devices" {
		t.Errorf("last push = %+v", lp)
	}
}

func TestBroadcastRevokedDeviceForgotten(t *testing.T) {
	ts := newTestServer(t, config.ServeConfig{})
	d, _ := ts.pairDevice(t, "phone")
	ts.subs.Set(d.ID, testSub("phone"), ts.clock.Now()) //nolint:errcheck
	p := NewPresence(ts.clock.Now)
	p.Report(d.ID, true)
	if err := ts.devices.mutate(func([]Device) ([]Device, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	fp := &fakePusher{}
	s := NewSender(ts.devices, ts.subs, fp)
	s.SetPresence(p)
	s.Broadcast(context.Background(), Message{Kind: "done"})
	if len(fp.calls) != 0 || p.Foreground(d.ID) {
		t.Errorf("revoked device: calls=%d foreground=%v", len(fp.calls), p.Foreground(d.ID))
	}
}

func TestSuppressWhileOpenConfig(t *testing.T) {
	off := false
	for _, tc := range []struct {
		name string
		cfg  config.ServePushConfig
		want bool
	}{
		{"default", config.ServePushConfig{}, true},
		{"false", config.ServePushConfig{SuppressWhileOpen: &off}, false},
	} {
		if got := tc.cfg.SuppressWhileOpenEnabled(); got != tc.want {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
	// Wiring: disabled config leaves Broadcast unsuppressed.
	ts := newTestServer(t, config.ServeConfig{Push: config.ServePushConfig{SuppressWhileOpen: &off}})
	if ts.sender.presence != nil {
		t.Error("presence wired despite suppress_while_open=false")
	}
	ts = newTestServer(t, config.ServeConfig{})
	if ts.sender.presence == nil {
		t.Error("presence not wired by default")
	}
}

func TestPresenceRoute(t *testing.T) {
	ts := newTestServer(t, config.ServeConfig{})
	dev, tok := ts.pairDevice(t, "phone")
	h := ts.Handler()
	cookie := withCookie(tok)

	if w := postJSON(h, "POST", "/v1/presence", `{"visible":true}`); w.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated = %d", w.Code)
	}
	if w := postJSON(h, "POST", "/v1/presence", `{"visible":true}`, withCookie("bogus")); w.Code != http.StatusUnauthorized {
		t.Errorf("unknown device = %d", w.Code)
	}
	if ts.presence.Foreground(dev.ID) {
		t.Fatal("foreground before any report")
	}
	r := httptest.NewRequest("POST", "/v1/presence", strings.NewReader(`{"visible":true}`)) // no CSRF header
	r.RemoteAddr = "192.168.1.20:51515"
	cookie(r)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || ts.presence.Foreground(dev.ID) {
		t.Errorf("missing CSRF = %d", w.Code)
	}
	if w := postJSON(h, "POST", "/v1/presence", `{}`, cookie); w.Code != http.StatusBadRequest {
		t.Errorf("missing field = %d", w.Code)
	}
	if w := postJSON(h, "POST", "/v1/presence", `{"visible":true}`, cookie); w.Code != 200 || !ts.presence.Foreground(dev.ID) {
		t.Errorf("visible = %d", w.Code)
	}
	if w := postJSON(h, "POST", "/v1/presence", `{"visible":false}`, cookie); w.Code != 200 || ts.presence.Foreground(dev.ID) {
		t.Errorf("hidden = %d", w.Code)
	}
}

func TestPresenceRouteWorksReadOnly(t *testing.T) {
	ts := newTestServer(t, config.ServeConfig{AllowWrite: false})
	dev, tok := ts.pairDevice(t, "phone")
	if w := postJSON(ts.Handler(), "POST", "/v1/presence", `{"visible":true}`, withCookie(tok)); w.Code != 200 || !ts.presence.Foreground(dev.ID) {
		t.Errorf("read-only presence = %d", w.Code)
	}
}
