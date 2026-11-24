package serve

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
)

// capturePushLog swaps pushLogf for a recorder for the test's duration.
func capturePushLog(t *testing.T) func() string {
	t.Helper()
	var mu sync.Mutex
	var lines []string
	old := pushLogf
	pushLogf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmtLine(format, args...))
	}
	t.Cleanup(func() { pushLogf = old })
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(lines, "\n")
	}
}

func fmtLine(format string, args ...any) string {
	return strings.TrimSpace(fmt.Sprintf(format, args...))
}

func TestBroadcastLogsZeroSubscriptions(t *testing.T) {
	logs := capturePushLog(t)
	ts := newTestServerWith(t, config.ServeConfig{}, func(o *Options) { o.Pusher = &fakePusher{} })
	ts.sender.Broadcast(context.Background(), Message{Kind: "blocked", Title: "SECRET TITLE", Body: "SECRET BODY"})
	got := logs()
	if strings.Count(got, "push: no subscribed devices, nothing sent (kind blocked)") != 1 {
		t.Errorf("log = %q", got)
	}
	if strings.Contains(got, "SECRET") {
		t.Errorf("log leaks title/body: %q", got)
	}
	lp := ts.sender.LastPush()
	if lp == nil || lp.Kind != "blocked" || lp.Result != "no subscribed devices" {
		t.Errorf("LastPush = %+v", lp)
	}
}

func TestBroadcastLogsPerDeviceResults(t *testing.T) {
	logs := capturePushLog(t)
	fp := &fakePusher{status: map[string]int{"https://web.push.apple.com/gone": http.StatusGone}}
	ts := newTestServerWith(t, config.ServeConfig{}, func(o *Options) { o.Pusher = fp })
	a, _ := ts.pairDevice(t, "ok")
	b, _ := ts.pairDevice(t, "gone")
	_ = ts.subs.Set(a.ID, testSub("ok"), ts.clock.Now())
	_ = ts.subs.Set(b.ID, testSub("gone"), ts.clock.Now())
	ts.sender.Broadcast(context.Background(), Message{Kind: "done", Tag: "s:@1"})
	got := logs()
	for _, want := range []string{"kind=done tag=s:@1 subscriptions=2", "result=sent", "result=expired-deleted", "host=web.push.apple.com"} {
		if !strings.Contains(got, want) {
			t.Errorf("log missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "/ok") || strings.Contains(got, "/gone") {
		t.Errorf("log leaks endpoint path:\n%s", got)
	}
	if lp := ts.sender.LastPush(); lp == nil || lp.Result != "sent to 1/2" {
		t.Errorf("LastPush = %+v", lp)
	}
}

func TestPushSubscriptionEndpointAndAudit(t *testing.T) {
	logs := capturePushLog(t)
	ts := newTestServerWith(t, config.ServeConfig{}, func(o *Options) { o.Pusher = &fakePusher{} })
	_, tok := ts.pairDevice(t, "phone")
	cookie := withCookie(tok)

	if w := do(ts.Handler(), "GET", "/v1/push/subscription"); w.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated = %d", w.Code)
	}
	if w := do(ts.Handler(), "GET", "/v1/push/subscription", cookie); w.Code != 200 || !strings.Contains(w.Body.String(), `"subscribed":false`) {
		t.Errorf("before = %d %s", w.Code, w.Body)
	}
	body := `{"endpoint":"https://web.push.apple.com/secretpath","keys":{"p256dh":"P","auth":"A"}}`
	if w := postJSON(ts.Handler(), "POST", "/v1/push/subscribe", body, cookie); w.Code != 200 {
		t.Fatalf("subscribe = %d", w.Code)
	}
	if w := do(ts.Handler(), "GET", "/v1/push/subscription", cookie); !strings.Contains(w.Body.String(), `"subscribed":true`) {
		t.Errorf("after = %s", w.Body)
	}
	postJSON(ts.Handler(), "POST", "/v1/push/test", "{}", cookie)
	postJSON(ts.Handler(), "DELETE", "/v1/push/subscribe", "", cookie)

	raw, err := os.ReadFile(filepath.Join(ts.dir, AuditFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"action":"push_subscribe"`, `"action":"push_test"`, `"action":"push_unsubscribe"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("audit missing %s:\n%s", want, raw)
		}
	}
	if strings.Contains(string(raw), "secretpath") {
		t.Error("audit leaks endpoint")
	}
	_ = logs
}

func TestStatusReportsPush(t *testing.T) {
	ts := newTestServerWith(t, config.ServeConfig{}, func(o *Options) { o.Pusher = &fakePusher{} })
	d, _ := ts.pairDevice(t, "phone")
	ts.pairDevice(t, "tablet")
	_ = ts.subs.Set(d.ID, testSub("phone"), ts.clock.Now())
	ts.sender.Broadcast(context.Background(), Message{Kind: "blocked"})
	st := ts.Status()
	if len(st.PushDevices) != 1 || st.PushDevices[0] != "phone" {
		t.Errorf("PushDevices = %v", st.PushDevices)
	}
	if st.LastPush == nil || st.LastPush.Kind != "blocked" || st.LastPush.Result != "sent to 1/1" {
		t.Errorf("LastPush = %+v", st.LastPush)
	}
}

// An artifact id is a viewing credential: push diagnostics log only a prefix.
func TestBroadcastLogsNoArtifactID(t *testing.T) {
	logs := capturePushLog(t)
	ts := newTestServerWith(t, config.ServeConfig{}, func(o *Options) { o.Pusher = &fakePusher{} })
	a, _ := ts.pairDevice(t, "ok")
	_ = ts.subs.Set(a.ID, testSub("ok"), ts.clock.Now())
	id := "abcdefghijklmnopqrstuvwxyz"
	ts.sender.Broadcast(context.Background(), Message{Kind: "artifact", Tag: "artifact:" + id, URL: artifactViewerURL(id)})
	got := logs()
	if strings.Contains(got, id) || !strings.Contains(got, "tag=artifact:abcdef…") {
		t.Errorf("log = %q, want only the id prefix", got)
	}
}

func TestRedactArtifactIDs(t *testing.T) {
	for in, want := range map[string]string{
		"artifacts abcdefghijklmnopqrstuvwxyz rev 2: gone":   "artifacts abcdef… rev 2: gone",
		"/a/abcdefghijklmnopqrstuvwxyz/latest/x":             "/a/abcdef…/latest/x",
		"legacy abcdefghij stays":                            "legacy abcdefghij stays",
		"longer abcdefghijklmnopqrstuvwxyzabcd is not an id": "longer abcdefghijklmnopqrstuvwxyzabcd is not an id",
	} {
		if got := redactArtifactIDs(in); got != want {
			t.Errorf("redactArtifactIDs(%q) = %q, want %q", in, got, want)
		}
	}
}
