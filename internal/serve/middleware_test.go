package serve

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
)

func decodeError(t *testing.T, body string) apiError {
	t.Helper()
	var e apiError
	if err := json.Unmarshal([]byte(body), &e); err != nil {
		t.Fatalf("error body %q is not JSON: %v", body, err)
	}
	return e
}

func TestCIDRFilter(t *testing.T) {
	tests := []struct {
		name   string
		cidrs  []string
		remote string
		xff    string
		want   int
	}{
		{name: "rfc1918 allowed", remote: "192.168.1.20:5000", want: http.StatusOK},
		{name: "10/8 allowed", remote: "10.1.2.3:5000", want: http.StatusOK},
		{name: "cgnat allowed", remote: "100.100.1.1:5000", want: http.StatusOK},
		{name: "loopback allowed", remote: "127.0.0.1:5000", want: http.StatusOK},
		{name: "ipv6 loopback allowed", remote: "[::1]:5000", want: http.StatusOK},
		{name: "public ipv4 rejected", remote: "8.8.8.8:5000", want: http.StatusForbidden},
		{name: "public ipv6 rejected", remote: "[2001:db8::1]:5000", want: http.StatusForbidden},
		{name: "ipv6 link-local rejected", remote: "[fe80::1%en0]:5000", want: http.StatusForbidden},
		{name: "ipv4-mapped ipv6 private allowed", remote: "[::ffff:192.168.1.20]:5000", want: http.StatusOK},
		{name: "ipv4-mapped ipv6 public rejected", remote: "[::ffff:8.8.8.8]:5000", want: http.StatusForbidden},
		{name: "x-forwarded-for ignored", remote: "8.8.8.8:5000", xff: "192.168.1.20", want: http.StatusForbidden},
		{name: "garbage remote rejected", remote: "nonsense", want: http.StatusForbidden},
		{name: "custom ipv6 cidr allowed", cidrs: []string{"2001:db8::/32"}, remote: "[2001:db8::5]:1", want: http.StatusOK},
		{name: "custom cidr excludes default", cidrs: []string{"192.168.5.0/24"}, remote: "192.168.1.20:5000", want: http.StatusForbidden},
		{name: "bare address entry", cidrs: []string{"192.168.1.20"}, remote: "192.168.1.20:5000", want: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := newTestServer(t, config.ServeConfig{AllowCIDRs: tt.cidrs})
			opts := []reqOpt{from(tt.remote)}
			if tt.xff != "" {
				opts = append(opts, withHeader("X-Forwarded-For", tt.xff), withHeader("X-Real-IP", tt.xff))
			}
			w := do(ts.Handler(), http.MethodGet, "/healthz", opts...)
			if w.Code != tt.want {
				t.Fatalf("status = %d, want %d (%s)", w.Code, tt.want, w.Body)
			}
			if tt.want == http.StatusForbidden {
				if e := decodeError(t, w.Body.String()); e.Code != codeForbiddenSource {
					t.Errorf("code = %q", e.Code)
				}
			}
		})
	}
}

func TestCIDRFilterRunsBeforeAuth(t *testing.T) {
	ts := newTestServer(t, config.ServeConfig{})
	_, tok := ts.pairDevice(t, "phone")
	w := do(ts.Handler(), http.MethodGet, "/", from("8.8.8.8:1"), withCookie(tok))
	if w.Code != http.StatusForbidden {
		t.Fatalf("paired device from a public address got %d, want 403", w.Code)
	}
}

func TestParseCIDRsRejectsGarbage(t *testing.T) {
	if _, err := parseCIDRs([]string{"192.168.0.0/16", "not-a-cidr"}); err == nil {
		t.Fatal("expected error")
	}
	if _, err := New(Options{Config: config.ServeConfig{AllowCIDRs: []string{"bogus"}}, Dir: t.TempDir()}); err == nil {
		t.Fatal("New accepted an invalid allow_cidrs entry")
	}
}

func TestDeviceAuth(t *testing.T) {
	ts := newTestServer(t, config.ServeConfig{})
	_, good := ts.pairDevice(t, "phone")
	revoked, revokedTok := ts.pairDevice(t, "old-phone")
	if _, err := ts.devices.Remove(revoked.ID); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		path     string
		opts     []reqOpt
		want     int
		wantHTML bool
	}{
		{name: "valid cookie", path: "/", opts: []reqOpt{withCookie(good)}, want: http.StatusOK, wantHTML: true},
		{name: "missing cookie api", path: "/v1/anything", want: http.StatusUnauthorized},
		{name: "missing cookie browser", path: "/", opts: []reqOpt{withHeader("Accept", "text/html")}, want: http.StatusUnauthorized, wantHTML: true},
		{name: "invalid cookie", path: "/v1/anything", opts: []reqOpt{withCookie("not-a-token")}, want: http.StatusUnauthorized},
		{name: "empty cookie", path: "/v1/anything", opts: []reqOpt{withCookie("")}, want: http.StatusUnauthorized},
		{name: "revoked cookie", path: "/v1/anything", opts: []reqOpt{withCookie(revokedTok)}, want: http.StatusUnauthorized},
		{name: "token hash as cookie", path: "/v1/anything", opts: []reqOpt{withCookie(revoked.TokenSHA256)}, want: http.StatusUnauthorized},
		{name: "api path never gets html", path: "/v1/x", opts: []reqOpt{withHeader("Accept", "text/html")}, want: http.StatusUnauthorized},
		{name: "unknown path paired is 404", path: "/nope", opts: []reqOpt{withCookie(good)}, want: http.StatusNotFound},
		{name: "unknown path unpaired is 401", path: "/nope", want: http.StatusUnauthorized},
		{name: "healthz needs no cookie", path: "/healthz", want: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := do(ts.Handler(), http.MethodGet, tt.path, tt.opts...)
			if w.Code != tt.want {
				t.Fatalf("status = %d, want %d (%s)", w.Code, tt.want, w.Body)
			}
			isHTML := strings.HasPrefix(w.Header().Get("Content-Type"), "text/html")
			if isHTML != tt.wantHTML {
				t.Errorf("content-type = %q, wantHTML %v", w.Header().Get("Content-Type"), tt.wantHTML)
			}
			if tt.want == http.StatusUnauthorized {
				if tt.wantHTML {
					if !strings.Contains(w.Body.String(), "agentctl serve pair") {
						t.Errorf("not-paired page missing instructions: %s", w.Body)
					}
				} else if e := decodeError(t, w.Body.String()); e.Code != codeUnauthorized {
					t.Errorf("code = %q", e.Code)
				}
			}
		})
	}
}

func TestRevocationByAnotherStoreApplies(t *testing.T) {
	ts := newTestServer(t, config.ServeConfig{})
	d, tok := ts.pairDevice(t, "phone")
	if w := do(ts.Handler(), http.MethodGet, "/", withCookie(tok)); w.Code != http.StatusOK {
		t.Fatalf("before revoke: %d", w.Code)
	}
	// `agentctl serve devices rm` opens its own store on the same file.
	cli, err := OpenDeviceStore(ts.dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Remove(d.Name); err != nil {
		t.Fatal(err)
	}
	if w := do(ts.Handler(), http.MethodGet, "/", withCookie(tok)); w.Code != http.StatusUnauthorized {
		t.Fatalf("after revoke: %d, want 401", w.Code)
	}
}

func TestCSRFAndWriteGate(t *testing.T) {
	tests := []struct {
		name       string
		allowWrite bool
		method     string
		csrf       string
		auth       bool
		want       int
		wantCode   string
	}{
		{name: "post without csrf header", allowWrite: true, method: http.MethodPost, auth: true, want: http.StatusForbidden, wantCode: codeCSRF},
		{name: "post with wrong csrf value", allowWrite: true, method: http.MethodPost, csrf: "yes", auth: true, want: http.StatusForbidden, wantCode: codeCSRF},
		{name: "delete without csrf header", allowWrite: true, method: http.MethodDelete, auth: true, want: http.StatusForbidden, wantCode: codeCSRF},
		{name: "post with csrf allowed", allowWrite: true, method: http.MethodPost, csrf: "1", auth: true, want: http.StatusNoContent},
		{name: "csrf checked before auth", allowWrite: true, method: http.MethodPost, want: http.StatusForbidden, wantCode: codeCSRF},
		{name: "unpaired with csrf", allowWrite: true, method: http.MethodPost, csrf: "1", want: http.StatusUnauthorized, wantCode: codeUnauthorized},
		{name: "allow_write false", allowWrite: false, method: http.MethodPost, csrf: "1", auth: true, want: http.StatusForbidden, wantCode: codeWriteDisabled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := newTestServer(t, config.ServeConfig{AllowWrite: tt.allowWrite})
			// A later PR's write route, registered the way it will be.
			rt := newRouter(ts.devices, tt.allowWrite, ts.clock.Now)
			var gotDevice Device
			rt.Write("POST /v1/test", func(w http.ResponseWriter, r *http.Request) {
				gotDevice, _ = DeviceFrom(r.Context())
				w.WriteHeader(http.StatusNoContent)
			})
			rt.Write("DELETE /v1/test", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
			h := ts.chain(rt)

			var opts []reqOpt
			d := Device{}
			if tt.auth {
				var tok string
				d, tok = ts.pairDevice(t, "phone")
				opts = append(opts, withCookie(tok))
			}
			if tt.csrf != "" {
				opts = append(opts, withHeader(CSRFHeader, tt.csrf))
			}
			w := do(h, tt.method, "/v1/test", opts...)
			if w.Code != tt.want {
				t.Fatalf("status = %d, want %d (%s)", w.Code, tt.want, w.Body)
			}
			if tt.wantCode != "" {
				if e := decodeError(t, w.Body.String()); e.Code != tt.wantCode {
					t.Errorf("code = %q, want %q", e.Code, tt.wantCode)
				}
			}
			if tt.want == http.StatusNoContent && tt.method == http.MethodPost && gotDevice.ID != d.ID {
				t.Errorf("handler saw device %q, want %q", gotDevice.ID, d.ID)
			}
		})
	}
}

func TestHealthz(t *testing.T) {
	ts := newTestServer(t, config.ServeConfig{})
	w := do(ts.Handler(), http.MethodGet, "/healthz")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["ok"] != true || got["version"] != "test" || got["started"] != "2026-11-16T12:00:00Z" {
		t.Errorf("healthz = %v", got)
	}
	f, ok := got["features"].([]any)
	if !ok || len(f) != 2 || f[0] != "fleet" || f[1] != "events" {
		t.Errorf("features = %#v, want [fleet events]", got["features"])
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("missing security headers: %v", w.Header())
	}
}

func TestRouterFeatures(t *testing.T) {
	rt := newRouter(nil, false, nil)
	rt.Feature("fleet")
	rt.Feature("fleet")
	rt.Feature("push")
	got := rt.Features()
	if strings.Join(got, ",") != "fleet,push" {
		t.Errorf("features = %v", got)
	}
	got[0] = "mutated"
	if rt.Features()[0] != "fleet" {
		t.Error("Features exposed internal slice")
	}
}
