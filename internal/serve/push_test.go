package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/config"
)

type pushCall struct {
	sub     Subscription
	payload Message
	opts    PushOptions
}

type fakePusher struct {
	mu     sync.Mutex
	calls  []pushCall
	status map[string]int // by endpoint; default 201
	err    error
}

func (f *fakePusher) Push(_ context.Context, sub Subscription, payload []byte, opts PushOptions) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var m Message
	_ = json.Unmarshal(payload, &m)
	f.calls = append(f.calls, pushCall{sub, m, opts})
	if f.err != nil {
		return 0, f.err
	}
	if st, ok := f.status[sub.Endpoint]; ok {
		return st, nil
	}
	return http.StatusCreated, nil
}

func (f *fakePusher) endpoints() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		out = append(out, c.sub.Endpoint)
	}
	return out
}

func testSub(name string) Subscription {
	return Subscription{Endpoint: "https://web.push.apple.com/" + name, Keys: SubKeys{P256dh: "p", Auth: "a"}}
}

func postJSON(h http.Handler, method, target, body string, opts ...reqOpt) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.RemoteAddr = "192.168.1.20:51515"
	r.Header.Set("X-Agentctl-CSRF", "1")
	for _, o := range opts {
		o(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestVAPIDGeneratedOnceWith0600(t *testing.T) {
	dir := t.TempDir()
	a, err := LoadOrCreateVAPID(dir)
	if err != nil || a.Public == "" || a.Private == "" {
		t.Fatalf("LoadOrCreateVAPID = %+v, %v", a, err)
	}
	fi, err := os.Stat(filepath.Join(dir, VAPIDFile))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("stat = %v, %v", fi, err)
	}
	b, err := LoadOrCreateVAPID(dir)
	if err != nil || a != b {
		t.Errorf("second load differs: %+v vs %+v (%v)", a, b, err)
	}
}

func TestSubscriptionStorePersistence(t *testing.T) {
	dir := t.TempDir()
	st, err := OpenSubscriptionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	if err := st.Set("d1", testSub("one"), now); err != nil {
		t.Fatal(err)
	}
	if err := st.Set("d2", testSub("two"), now); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, SubscriptionsFile))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("stat = %v, %v", fi, err)
	}

	// A second handle (the CLI process) sees the writes and its own deletes
	// are visible to the first.
	st2, err := OpenSubscriptionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := st2.Get("d1"); !ok || got.Endpoint != "https://web.push.apple.com/one" {
		t.Fatalf("Get(d1) = %+v, %v", got, ok)
	}
	if err := st2.Delete("d1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Get("d1"); ok {
		t.Error("deletion by another handle not visible")
	}
	if _, ok := st.Get("d2"); !ok {
		t.Error("d2 lost")
	}
	if err := st.DeleteEndpoint("https://web.push.apple.com/two"); err != nil {
		t.Fatal(err)
	}
	if all, _ := st.All(); len(all) != 0 {
		t.Errorf("All = %v", all)
	}
}

func TestSubscriptionValidate(t *testing.T) {
	cases := map[string]Subscription{
		"http endpoint": {Endpoint: "http://x/y", Keys: SubKeys{"p", "a"}},
		"no endpoint":   {Keys: SubKeys{"p", "a"}},
		"no keys":       {Endpoint: "https://web.push.apple.com/y"},
		// Not a push service: a paired device must not be able to aim
		// broadcasts at the LAN, loopback or cloud metadata.
		"loopback":           {Endpoint: "https://127.0.0.1:6443/", Keys: SubKeys{"p", "a"}},
		"metadata":           {Endpoint: "https://169.254.169.254/latest", Keys: SubKeys{"p", "a"}},
		"private ip":         {Endpoint: "https://10.0.0.5:22/", Keys: SubKeys{"p", "a"}},
		"other host":         {Endpoint: "https://example.com/push", Keys: SubKeys{"p", "a"}},
		"lookalike suffix":   {Endpoint: "https://evilpush.apple.com/x", Keys: SubKeys{"p", "a"}},
		"bare wildcard base": {Endpoint: "https://push.apple.com/x", Keys: SubKeys{"p", "a"}},
		"allowed host, port": {Endpoint: "https://web.push.apple.com:8443/x", Keys: SubKeys{"p", "a"}},
		"userinfo":           {Endpoint: "https://u:p@web.push.apple.com/x", Keys: SubKeys{"p", "a"}},
	}
	for name, sub := range cases {
		if sub.Validate() == nil {
			t.Errorf("%s: want error", name)
		}
	}
	for _, ep := range []string{
		"https://web.push.apple.com/QGx",
		"https://fcm.googleapis.com/fcm/send/abc",
		"https://updates.push.services.mozilla.com/wpush/v2/x",
		"https://wns2-par02p.notify.windows.com/w/?token=x",
		"https://WEB.PUSH.APPLE.COM:443/x",
	} {
		if err := (Subscription{Endpoint: ep, Keys: SubKeys{"p", "a"}}).Validate(); err != nil {
			t.Errorf("%s: %v", ep, err)
		}
	}
}

func TestSenderBroadcast(t *testing.T) {
	ts := newTestServer(t, config.ServeConfig{})
	d1, _ := ts.pairDevice(t, "phone")
	d2, _ := ts.pairDevice(t, "tablet")
	fp := &fakePusher{status: map[string]int{"https://web.push.apple.com/gone": http.StatusGone}}
	ts.subs.Set(d1.ID, testSub("phone"), ts.clock.Now()) //nolint:errcheck
	ts.subs.Set(d2.ID, testSub("gone"), ts.clock.Now())  //nolint:errcheck
	s := NewSender(ts.devices, ts.subs, fp)

	s.Broadcast(context.Background(), Message{Title: "t", Urgent: true})
	if len(fp.calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(fp.calls))
	}
	for _, c := range fp.calls {
		if !c.opts.Urgent {
			t.Error("urgent message not sent with high urgency")
		}
	}
	if _, ok := ts.subs.Get(d2.ID); ok {
		t.Error("410 subscription was not deleted")
	}
	if _, ok := ts.subs.Get(d1.ID); !ok {
		t.Error("healthy subscription was deleted")
	}

	fp.status = map[string]int{"https://web.push.apple.com/phone": http.StatusNotFound}
	s.Broadcast(context.Background(), Message{Title: "t"})
	if _, ok := ts.subs.Get(d1.ID); ok {
		t.Error("404 subscription was not deleted")
	}
}

func TestSenderSkipsRevokedDevice(t *testing.T) {
	ts := newTestServer(t, config.ServeConfig{})
	d, _ := ts.pairDevice(t, "phone")
	keep, _ := ts.pairDevice(t, "tablet")
	ts.subs.Set(d.ID, testSub("phone"), ts.clock.Now())     //nolint:errcheck
	ts.subs.Set(keep.ID, testSub("tablet"), ts.clock.Now()) //nolint:errcheck
	// Simulate a stale subscriptions.json: revoke through a raw mutation that
	// bypasses DeviceStore.Remove's own cleanup.
	if err := ts.devices.mutate(func(l []Device) ([]Device, error) {
		var out []Device
		for _, x := range l {
			if x.ID != d.ID {
				out = append(out, x)
			}
		}
		return out, nil
	}); err != nil {
		t.Fatal(err)
	}
	fp := &fakePusher{}
	s := NewSender(ts.devices, ts.subs, fp)
	s.Broadcast(context.Background(), Message{Title: "t"})
	if got := fp.endpoints(); len(got) != 1 || got[0] != "https://web.push.apple.com/tablet" {
		t.Errorf("pushed to %v, want only tablet", got)
	}
	if _, ok := ts.subs.Get(d.ID); ok {
		t.Error("stale subscription not pruned")
	}
	if err := s.SendTo(context.Background(), d.ID, Message{}); err == nil {
		t.Error("SendTo revoked device succeeded")
	}
}

func TestRevokeDropsSubscriptions(t *testing.T) {
	ts := newTestServer(t, config.ServeConfig{})
	a, _ := ts.pairDevice(t, "a")
	b, _ := ts.pairDevice(t, "b")
	ts.subs.Set(a.ID, testSub("a"), ts.clock.Now()) //nolint:errcheck
	ts.subs.Set(b.ID, testSub("b"), ts.clock.Now()) //nolint:errcheck

	// `serve devices rm` runs in another process with its own stores.
	cli, err := OpenDeviceStore(ts.dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Remove("a"); err != nil {
		t.Fatal(err)
	}
	if _, ok := ts.subs.Get(a.ID); ok {
		t.Error("rm kept the subscription")
	}
	if _, ok := ts.subs.Get(b.ID); !ok {
		t.Error("rm dropped another device's subscription")
	}
	if err := cli.Clear(); err != nil { // ca rotate
		t.Fatal(err)
	}
	if all, _ := ts.subs.All(); len(all) != 0 {
		t.Errorf("Clear kept %v", all)
	}
}

func TestNtfyRequestShape(t *testing.T) {
	var gotReq *http.Request
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReq = r
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
	}))
	defer srv.Close()
	n := &Ntfy{Base: srv.URL, Topic: "my topic", AppURL: "https://192.168.1.10:8443", Details: true}
	msg := Message{Title: "agentctl: go-engineer blocked", Body: "proj · @5 · Bash", URL: windowURL("agentctl-proj", "@5"), Kind: "blocked", Urgent: true}
	if err := n.Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if gotReq.Method != http.MethodPost || gotReq.URL.EscapedPath() != "/my%20topic" {
		t.Errorf("request = %s %s", gotReq.Method, gotReq.URL.EscapedPath())
	}
	if gotBody != msg.Body {
		t.Errorf("body = %q", gotBody)
	}
	if got := gotReq.Header.Get("Title"); got != msg.Title {
		t.Errorf("Title = %q", got)
	}
	if got, want := gotReq.Header.Get("Click"), "https://192.168.1.10:8443/#/w/agentctl-proj/%405"; got != want {
		t.Errorf("Click = %q, want %q", got, want)
	}
	if got := gotReq.Header.Get("Tags"); !strings.Contains(got, "warning") {
		t.Errorf("Tags = %q", got)
	}
	if got := gotReq.Header.Get("Priority"); got != "high" {
		t.Errorf("Priority = %q", got)
	}
}

func TestNtfyErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }))
	defer srv.Close()
	n := &Ntfy{Base: srv.URL, Topic: "t", AppURL: "https://h"}
	if err := n.Send(context.Background(), Message{}); err == nil {
		t.Error("want error for 500")
	}
}

func TestWindowURLMatchesRouter(t *testing.T) {
	if got, want := windowURL("agentctl-my proj", "@523"), "/#/w/agentctl-my%20proj/%40523"; got != want {
		t.Errorf("windowURL = %q, want %q", got, want)
	}
}

func TestPushEndpoints(t *testing.T) {
	fp := &fakePusher{}
	ts := newTestServerWith(t, config.ServeConfig{}, func(o *Options) { o.Pusher = fp })
	dev, tok := ts.pairDevice(t, "phone")
	other, _ := ts.pairDevice(t, "tablet")
	ts.subs.Set(other.ID, testSub("tablet"), ts.clock.Now()) //nolint:errcheck
	cookie := withCookie(tok)

	// vapid
	w := do(ts.Handler(), "GET", "/v1/push/vapid", cookie)
	var vb struct {
		PublicKey string `json:"public_key"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &vb) != nil || vb.PublicKey != ts.vapid.Public || vb.PublicKey == "" {
		t.Fatalf("vapid = %d %s", w.Code, w.Body)
	}
	// unauthenticated
	if w := do(ts.Handler(), "GET", "/v1/push/vapid"); w.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated vapid = %d", w.Code)
	}
	// features
	if !bytes.Contains(do(ts.Handler(), "GET", "/healthz").Body.Bytes(), []byte(`"push"`)) {
		t.Error("push feature not advertised")
	}

	// test before subscribing -> 409
	if w := postJSON(ts.Handler(), "POST", "/v1/push/test", "{}", cookie); w.Code != http.StatusConflict {
		t.Errorf("test without subscription = %d", w.Code)
	}
	// bad subscription
	if w := postJSON(ts.Handler(), "POST", "/v1/push/subscribe", `{"endpoint":"http://x"}`, cookie); w.Code != http.StatusBadRequest {
		t.Errorf("bad subscribe = %d", w.Code)
	}
	// subscribe in the shape PushSubscription.toJSON() produces
	body := `{"endpoint":"https://web.push.apple.com/phone","expirationTime":null,"keys":{"p256dh":"P","auth":"A"}}`
	if w := postJSON(ts.Handler(), "POST", "/v1/push/subscribe", body, cookie); w.Code != 200 {
		t.Fatalf("subscribe = %d %s", w.Code, w.Body)
	}
	if sub, ok := ts.subs.Get(dev.ID); !ok || sub.Keys.Auth != "A" {
		t.Fatalf("stored = %+v, %v", sub, ok)
	}
	// test goes to the calling device only
	if w := postJSON(ts.Handler(), "POST", "/v1/push/test", "{}", cookie); w.Code != 200 {
		t.Fatalf("test = %d %s", w.Code, w.Body)
	}
	if got := fp.endpoints(); len(got) != 1 || got[0] != "https://web.push.apple.com/phone" {
		t.Errorf("test push went to %v", got)
	}
	// unsubscribe
	if w := postJSON(ts.Handler(), "DELETE", "/v1/push/subscribe", "", cookie); w.Code != 200 {
		t.Fatalf("unsubscribe = %d", w.Code)
	}
	if _, ok := ts.subs.Get(dev.ID); ok {
		t.Error("subscription not removed")
	}
	if _, ok := ts.subs.Get(other.ID); !ok {
		t.Error("another device's subscription removed")
	}
}

func TestPushDispatcherDeliversToWebPushAndNtfy(t *testing.T) {
	posted := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { posted <- r.Header.Get("Title") }))
	defer srv.Close()
	fp := &fakePusher{}
	ts := newTestServerWith(t, config.ServeConfig{NtfyTopic: "topic", NtfyDetails: true}, func(o *Options) { o.Pusher = fp; o.NtfyBase = srv.URL })
	d, _ := ts.pairDevice(t, "phone")
	ts.subs.Set(d.ID, testSub("phone"), ts.clock.Now()) //nolint:errcheck

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ts.pushQ.run(ctx)
	ts.pushQ.enqueue(Message{Title: "hello", Kind: "done"})
	select {
	case got := <-posted:
		if got != "hello" {
			t.Errorf("ntfy Title = %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ntfy not posted")
	}
	if got := fp.endpoints(); len(got) != 1 {
		t.Errorf("web push calls = %v", got)
	}
}
