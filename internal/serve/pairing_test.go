package serve

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/config"
)

func TestPairingCode(t *testing.T) {
	tests := []struct {
		name    string
		advance time.Duration
		code    func(string) string
		want    bool
	}{
		{name: "valid", code: func(c string) string { return c }, want: true},
		{name: "wrong", code: func(c string) string {
			last := "A"
			if strings.HasSuffix(c, "A") {
				last = "B"
			}
			return c[:len(c)-1] + last
		}},
		{name: "empty", code: func(string) string { return "" }},
		{name: "lowercase", code: strings.ToLower},
		{name: "just before expiry", advance: PairingWindow - time.Second, code: func(c string) string { return c }, want: true},
		{name: "expired", advance: PairingWindow, code: func(c string) string { return c }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var p pairing
			clock := newClock()
			code, expires, err := p.open("phone", clock.Now())
			if err != nil {
				t.Fatal(err)
			}
			if len(code) != 26 { // 128 bits, base32 without padding
				t.Fatalf("code %q has length %d", code, len(code))
			}
			if !expires.Equal(clock.Now().Add(PairingWindow)) {
				t.Errorf("expires = %v", expires)
			}
			clock.Advance(tt.advance)
			name, ok := p.consume(tt.code(code), clock.Now())
			if ok != tt.want {
				t.Fatalf("consume ok = %v, want %v", ok, tt.want)
			}
			if ok && name != "phone" {
				t.Errorf("name = %q", name)
			}
		})
	}
}

func TestPairingCodeOneTime(t *testing.T) {
	var p pairing
	clock := newClock()
	code, _, _ := p.open("", clock.Now())
	if _, ok := p.consume(code, clock.Now()); !ok {
		t.Fatal("first use rejected")
	}
	if _, ok := p.consume(code, clock.Now()); ok {
		t.Fatal("code accepted twice")
	}
}

func TestPairingReopenInvalidatesOldCode(t *testing.T) {
	var p pairing
	clock := newClock()
	old, _, _ := p.open("", clock.Now())
	fresh, _, _ := p.open("", clock.Now())
	if _, ok := p.consume(old, clock.Now()); ok {
		t.Fatal("superseded code accepted")
	}
	if _, ok := p.consume(fresh, clock.Now()); !ok {
		t.Fatal("fresh code rejected")
	}
}

func TestPairHandler(t *testing.T) {
	ts := newTestServer(t, config.ServeConfig{})
	info, err := ts.OpenPairing("iphone")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(info.PairURL)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "https" || u.Host != "192.168.1.10:8443" || u.Path != "/pair" {
		t.Errorf("pair URL = %s", info.PairURL)
	}
	if info.BootstrapURL != "" {
		t.Errorf("bootstrap URL without a bootstrap listener: %s", info.BootstrapURL)
	}
	if info.Fingerprint != ts.opts.CA.Fingerprint() {
		t.Errorf("fingerprint = %s", info.Fingerprint)
	}

	w := do(ts.Handler(), http.MethodGet, u.RequestURI())
	if w.Code != http.StatusOK {
		t.Fatalf("pair status = %d (%s)", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), `http-equiv="refresh" content="0; url=/"`) {
		t.Errorf("pair response does not redirect to /: %s", w.Body)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %v", cookies)
	}
	c := cookies[0]
	if c.Name != DeviceCookie || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.MaxAge != 31536000 {
		t.Errorf("cookie attributes wrong: %+v", c)
	}
	list, _ := ts.devices.List()
	if len(list) != 1 || list[0].Name != "iphone" {
		t.Fatalf("devices = %+v", list)
	}
	if w := do(ts.Handler(), http.MethodGet, "/", withCookie(c.Value)); w.Code != http.StatusOK {
		t.Errorf("new cookie rejected: %d", w.Code)
	}

	// Reuse of the same code fails and creates nothing.
	w = do(ts.Handler(), http.MethodGet, u.RequestURI())
	if w.Code != http.StatusForbidden || len(w.Result().Cookies()) != 0 {
		t.Errorf("reused code: status %d cookies %v", w.Code, w.Result().Cookies())
	}
	if list, _ := ts.devices.List(); len(list) != 1 {
		t.Errorf("reused code created a device: %+v", list)
	}
}

func TestPairHandlerExpired(t *testing.T) {
	ts := newTestServer(t, config.ServeConfig{})
	info, err := ts.OpenPairing("")
	if err != nil {
		t.Fatal(err)
	}
	ts.clock.Advance(PairingWindow + time.Second)
	u, _ := url.Parse(info.PairURL)
	w := do(ts.Handler(), http.MethodGet, u.RequestURI())
	if w.Code != http.StatusForbidden {
		t.Fatalf("expired code: status %d", w.Code)
	}
	if list, _ := ts.devices.List(); len(list) != 0 {
		t.Errorf("expired code created a device")
	}
}

func TestPairHandlerNoWindow(t *testing.T) {
	ts := newTestServer(t, config.ServeConfig{})
	if w := do(ts.Handler(), http.MethodGet, "/pair?code="); w.Code != http.StatusForbidden {
		t.Fatalf("status %d", w.Code)
	}
	if w := do(ts.Handler(), http.MethodGet, "/pair?code=AAAA", from("8.8.8.8:1")); w.Code != http.StatusForbidden {
		t.Fatalf("public source: status %d", w.Code)
	}
}

func TestBootstrapListenerLifecycle(t *testing.T) {
	ts := newTestServer(t, config.ServeConfig{})
	ts.opts.BootstrapAddr = "127.0.0.1:0"
	info, err := ts.OpenPairing("phone")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(info.BootstrapURL, "http://192.168.1.10:") {
		t.Fatalf("bootstrap URL = %q", info.BootstrapURL)
	}
	port := ts.bootPort
	base := "http://127.0.0.1:" + strings.TrimPrefix(info.BootstrapURL, "http://192.168.1.10:")

	get := func(path string) (*http.Response, string) {
		t.Helper()
		resp, err := http.Get(base + strings.TrimPrefix(path, "/"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		return resp, string(body)
	}
	resp, body := get("/")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, ts.opts.CA.Fingerprint()) || !strings.Contains(body, "/ca.mobileconfig") {
		t.Errorf("landing page: %d %s", resp.StatusCode, body)
	}
	if strings.Contains(body, info.Code) {
		t.Error("plain-HTTP landing page leaks the pairing code")
	}
	resp, body = get("/ca.mobileconfig")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != MobileconfigContentType || !strings.Contains(body, "com.apple.security.root") {
		t.Errorf("mobileconfig: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	resp, _ = get("/pair?code=" + info.Code)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("bootstrap listener serves /pair: %d", resp.StatusCode)
	}

	// Reopening keeps the same listener.
	if _, err := ts.OpenPairing("phone"); err != nil {
		t.Fatal(err)
	}
	if ts.bootPort != port {
		t.Errorf("reopen moved the listener from %d to %d", port, ts.bootPort)
	}

	ts.Close()
	if _, err := http.Get(base); err == nil {
		t.Error("bootstrap listener still answering after the window closed")
	}
	if ts.pairing.active(ts.clock.Now()) {
		t.Error("pairing still active after close")
	}
}

func TestBootstrapHandlerCIDR(t *testing.T) {
	ts := newTestServer(t, config.ServeConfig{})
	if w := do(ts.BootstrapHandler(), http.MethodGet, "/ca.mobileconfig", from("8.8.8.8:1")); w.Code != http.StatusForbidden {
		t.Errorf("public source got %d", w.Code)
	}
	if w := do(ts.BootstrapHandler(), http.MethodGet, "/ca.mobileconfig"); w.Code != http.StatusOK {
		t.Errorf("LAN source got %d", w.Code)
	}
}

func TestPairingWindowExpiresOnTimer(t *testing.T) {
	ts := newTestServer(t, config.ServeConfig{})
	ts.opts.BootstrapAddr = "127.0.0.1:0"
	if _, err := ts.OpenPairing(""); err != nil {
		t.Fatal(err)
	}
	ts.bootMu.Lock()
	gen := ts.bootGen
	ts.bootMu.Unlock()
	ts.expirePairing(gen - 1) // a stale timer is ignored
	if !ts.pairing.active(ts.clock.Now()) {
		t.Fatal("stale timer closed the window")
	}
	ts.expirePairing(gen)
	if ts.pairing.active(ts.clock.Now()) {
		t.Fatal("window still open after its timer fired")
	}
	ts.bootMu.Lock()
	defer ts.bootMu.Unlock()
	if ts.boot != nil {
		t.Fatal("bootstrap listener still running")
	}
}
