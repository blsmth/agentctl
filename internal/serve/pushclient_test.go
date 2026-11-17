package serve

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
)

func TestIsPublicAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"17.57.144.10":           true, // Apple push
		"142.250.80.10":          true,
		"2607:f8b0:4004::200a":   true,
		"127.0.0.1":              false,
		"::1":                    false,
		"10.0.0.5":               false,
		"172.16.3.4":             false,
		"192.168.1.10":           false,
		"169.254.169.254":        false,
		"fe80::1":                false,
		"fc00::1":                false,
		"0.0.0.0":                false,
		"0.1.2.3":                false,
		"::":                     false,
		"100.100.100.100":        false, // CGNAT / Tailscale
		"224.0.0.1":              false,
		"::ffff:127.0.0.1":       false, // v4-mapped loopback
		"::ffff:169.254.169.254": false,
	} {
		if got := isPublicAddr(netip.MustParseAddr(addr)); got != want {
			t.Errorf("isPublicAddr(%s) = %v, want %v", addr, got, want)
		}
	}
}

func TestRefuseNonPublicDial(t *testing.T) {
	for _, tc := range []struct {
		addr string
		ok   bool
	}{
		{"17.57.144.10:443", true},
		{"[2607:f8b0:4004::200a]:443", true},
		{"127.0.0.1:6443", false},
		{"169.254.169.254:443", false},
		{"10.0.0.5:22", false},
		{"[::1]:443", false},
		{"not-an-ip:443", false},
		{"garbage", false},
	} {
		err := refuseNonPublicDial("tcp", tc.addr, nil)
		if (err == nil) != tc.ok {
			t.Errorf("refuseNonPublicDial(%s) = %v, want ok=%v", tc.addr, err, tc.ok)
		}
		if err != nil && !errors.Is(err, errDialRefused) {
			t.Errorf("refuseNonPublicDial(%s) error %v is not errDialRefused", tc.addr, err)
		}
	}
}

// The production client never connects to a loopback server, even when
// asked to directly (DNS rebinding would look the same to it).
func TestPushClientRefusesLoopback(t *testing.T) {
	hit := false
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	defer srv.Close()
	_, err := newPushClient().Post(srv.URL, "text/plain", strings.NewReader("x"))
	if err == nil || !errors.Is(err, errDialRefused) {
		t.Fatalf("err = %v, want errDialRefused", err)
	}
	if hit {
		t.Error("request reached the loopback server")
	}
}

func TestPushClientRefusesRedirects(t *testing.T) {
	client := newPushClient()
	if client.CheckRedirect == nil {
		t.Fatal("no redirect policy")
	}
	req := httptest.NewRequest("POST", "https://web.push.apple.com/x", nil)
	if err := client.CheckRedirect(req, []*http.Request{req}); !errors.Is(err, errRedirectRefused) {
		t.Errorf("CheckRedirect = %v", err)
	}
	if tr, ok := client.Transport.(*http.Transport); !ok || tr.Proxy != nil {
		t.Error("push transport must not use an environment proxy")
	}
}

func TestVAPIDSubscriber(t *testing.T) {
	for in, want := range map[string]string{
		"":                       "https://github.com/blsmth/agentctl",
		"  ":                     "https://github.com/blsmth/agentctl",
		"mailto:ops@example.com": "ops@example.com",
		"MAILTO:ops@example.com": "ops@example.com",
		"ops@example.com":        "ops@example.com",
		"https://example.com":    "https://example.com",
	} {
		if got := vapidSubscriber(in); got != want {
			t.Errorf("vapidSubscriber(%q) = %q, want %q", in, got, want)
		}
	}
}

// testP256Sub returns a subscription with real keys, so webpush-go can
// encrypt to it.
func testP256Sub(t *testing.T, endpoint string) Subscription {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		t.Fatal(err)
	}
	return Subscription{Endpoint: endpoint, Keys: SubKeys{
		P256dh: base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()),
		Auth:   base64.RawURLEncoding.EncodeToString(auth),
	}}
}

// jwtClaims decodes the claims of the VAPID JWT in an Authorization header
// ("vapid t=<jwt>, k=<key>").
func jwtClaims(t *testing.T, authz string) map[string]any {
	t.Helper()
	rest, ok := strings.CutPrefix(authz, "vapid t=")
	if !ok {
		t.Fatalf("Authorization = %q", authz)
	}
	jwt, _, _ := strings.Cut(rest, ",")
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt = %q", jwt)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

// Regression: webpush-go prepends "mailto:" to a subject that does not start
// with "https:", so the configured "mailto:..." became "mailto:mailto:...".
func TestWebPusherVAPIDSubject(t *testing.T) {
	keys, err := LoadOrCreateVAPID(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ configured, want string }{
		{"", DefaultVAPIDSubscriber},
		{"mailto:ops@example.com", "mailto:ops@example.com"},
		{"ops@example.com", "mailto:ops@example.com"},
		{"https://example.com/contact", "https://example.com/contact"},
	} {
		t.Run(tc.configured, func(t *testing.T) {
			var authz, urgency string
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				authz, urgency = r.Header.Get("Authorization"), r.Header.Get("Urgency")
				_, _ = io.Copy(io.Discard, r.Body)
				w.WriteHeader(http.StatusCreated)
			}))
			defer srv.Close()
			p := newWebPusher(keys, tc.configured)
			p.client = srv.Client() // the production client refuses loopback
			status, err := p.Push(context.Background(), testP256Sub(t, srv.URL+"/push/1"), []byte(`{"title":"t"}`), PushOptions{Urgent: true})
			if err != nil || status != http.StatusCreated {
				t.Fatalf("Push = %d, %v", status, err)
			}
			claims := jwtClaims(t, authz)
			if claims["sub"] != tc.want {
				t.Errorf("sub = %v, want %q", claims["sub"], tc.want)
			}
			if aud, _ := claims["aud"].(string); aud != srv.URL {
				t.Errorf("aud = %q, want %q", aud, srv.URL)
			}
			if urgency != "high" {
				t.Errorf("Urgency = %q", urgency)
			}
		})
	}
}

// /v1/push/test never echoes the delivery error: it would turn the endpoint
// into a port-scan oracle (dial errors, status codes).
func TestPushTestErrorIsGeneric(t *testing.T) {
	fp := &fakePusher{err: errors.New("dial tcp 10.0.0.5:22: connect: connection refused")}
	ts := newTestServerWith(t, config.ServeConfig{}, func(o *Options) { o.Pusher = fp })
	dev, tok := ts.pairDevice(t, "phone")
	ts.subs.Set(dev.ID, testSub("phone"), ts.clock.Now()) //nolint:errcheck
	w := postJSON(ts.Handler(), "POST", "/v1/push/test", "{}", withCookie(tok))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d %s", w.Code, w.Body)
	}
	for _, leak := range []string{"10.0.0.5", "dial", "refused"} {
		if strings.Contains(w.Body.String(), leak) {
			t.Errorf("response leaks %q: %s", leak, w.Body)
		}
	}
}

func TestNtfyRedactedByDefault(t *testing.T) {
	var gotReq *http.Request
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReq = r
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
	}))
	defer srv.Close()
	n := &Ntfy{Base: srv.URL, Topic: "t", AppURL: "https://192.168.1.10:8443"}
	msg := Message{Title: "agentctl: go-engineer blocked", Body: "secretproj · @5 · Bash", URL: windowURL("agentctl-secretproj", "@5"), Kind: "blocked", Urgent: true}
	if err := n.Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	all := gotBody + gotReq.Header.Get("Title") + gotReq.Header.Get("Click")
	for _, leak := range []string{"secretproj", "go-engineer", "192.168.1.10", "@5"} {
		if strings.Contains(all, leak) {
			t.Errorf("ntfy post leaks %q (title %q, body %q, click %q)", leak, gotReq.Header.Get("Title"), gotBody, gotReq.Header.Get("Click"))
		}
	}
	if gotReq.Header.Get("Click") != "" {
		t.Errorf("Click = %q, want none", gotReq.Header.Get("Click"))
	}
	if got := gotReq.Header.Get("Title"); !strings.Contains(got, "blocked") {
		t.Errorf("Title = %q, want the kind of event", got)
	}
	if got := gotReq.Header.Get("Priority"); got != "high" {
		t.Errorf("Priority = %q", got)
	}
}
