package serve

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
)

func TestStaticAssets(t *testing.T) {
	ts := newTestServer(t, config.ServeConfig{})
	_, tok := ts.pairDevice(t, "phone")
	h := ts.Handler()

	for _, a := range staticAssets {
		t.Run(a.URL, func(t *testing.T) {
			// Authenticated: always served with the declared type.
			w := do(h, "GET", a.URL, withCookie(tok))
			if w.Code != http.StatusOK {
				t.Fatalf("paired: status %d", w.Code)
			}
			if got := w.Header().Get("Content-Type"); got != a.Type {
				t.Errorf("Content-Type = %q, want %q", got, a.Type)
			}
			if w.Header().Get("Cache-Control") == "no-store" || w.Header().Get("Cache-Control") == "" {
				t.Errorf("Cache-Control = %q, want a static-file policy", w.Header().Get("Cache-Control"))
			}
			if w.Body.Len() == 0 {
				t.Error("empty body")
			}
			// Unauthenticated: only Public assets.
			w = do(h, "GET", a.URL)
			if a.Public && w.Code != http.StatusOK {
				t.Errorf("public asset unpaired: status %d", w.Code)
			}
			if !a.Public && w.Code != http.StatusUnauthorized {
				t.Errorf("device asset unpaired: status %d, want 401", w.Code)
			}
		})
	}
}

func TestStaticETag(t *testing.T) {
	ts := newTestServer(t, config.ServeConfig{})
	_, tok := ts.pairDevice(t, "phone")
	w := do(ts.Handler(), "GET", "/app.js", withCookie(tok))
	etag := w.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}
	w = do(ts.Handler(), "GET", "/app.js", withCookie(tok), withHeader("If-None-Match", etag))
	if w.Code != http.StatusNotModified {
		t.Errorf("status = %d, want 304", w.Code)
	}
}

func TestServiceWorkerScope(t *testing.T) {
	ts := newTestServer(t, config.ServeConfig{})
	_, tok := ts.pairDevice(t, "phone")
	w := do(ts.Handler(), "GET", "/sw.js", withCookie(tok))
	if got := w.Header().Get("Service-Worker-Allowed"); got != "/" {
		t.Errorf("Service-Worker-Allowed = %q", got)
	}
	body := w.Body.String()
	for _, want := range []string{"showNotification", "notificationclick", `"/v1/"`} {
		if !strings.Contains(body, want) {
			t.Errorf("sw.js missing %q", want)
		}
	}
}

func TestManifestParses(t *testing.T) {
	ts := newTestServer(t, config.ServeConfig{})
	w := do(ts.Handler(), "GET", "/manifest.webmanifest")
	var m struct {
		Display string `json:"display"`
		Start   string `json:"start_url"`
		Icons   []struct {
			Src   string `json:"src"`
			Sizes string `json:"sizes"`
		} `json:"icons"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m.Display != "standalone" || m.Start != "/" {
		t.Errorf("manifest = %+v", m)
	}
	sizes := map[string]bool{}
	for _, ic := range m.Icons {
		sizes[ic.Sizes] = true
		if r := do(ts.Handler(), "GET", ic.Src); r.Code != http.StatusOK || !strings.HasPrefix(r.Body.String(), "\x89PNG") {
			t.Errorf("icon %s: status %d, not a PNG", ic.Src, r.Code)
		}
	}
	if !sizes["192x192"] || !sizes["512x512"] {
		t.Errorf("icons = %v, want 192 and 512", sizes)
	}
}

func TestHTMLHasStrictCSP(t *testing.T) {
	ts := newTestServer(t, config.ServeConfig{})
	_, tok := ts.pairDevice(t, "phone")
	h := ts.Handler()
	pages := map[string]string{
		"index":     do(h, "GET", "/", withCookie(tok)).Header().Get("Content-Security-Policy"),
		"notpaired": do(h, "GET", "/", withHeader("Accept", "text/html")).Header().Get("Content-Security-Policy"),
	}
	for name, csp := range pages {
		if !strings.Contains(csp, "script-src 'self';") || !strings.Contains(csp, "connect-src 'self'") {
			t.Errorf("%s: CSP = %q", name, csp)
		}
		if strings.Contains(csp, "script-src 'self' 'unsafe") || strings.Contains(csp, "unsafe-eval") {
			t.Errorf("%s: CSP allows unsafe scripts: %q", name, csp)
		}
	}
}

// The shell must stay free of inline scripts and event-handler attributes.
func TestIndexHasNoInlineScript(t *testing.T) {
	body := string(page("index.html"))
	for _, bad := range []string{" onclick=", " onload=", " onerror=", "javascript:"} {
		if strings.Contains(body, bad) {
			t.Errorf("index.html contains %q", bad)
		}
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, "<script") && !strings.Contains(line, " src=") {
			t.Errorf("inline script: %s", line)
		}
	}
}

func TestUnlistedFilesNotServed(t *testing.T) {
	ts := newTestServer(t, config.ServeConfig{})
	_, tok := ts.pairDevice(t, "phone")
	for _, p := range []string{"/vendor/README.md", "/vendor/LICENSE-xterm", "/notpaired.html", "/web/app.js"} {
		if w := do(ts.Handler(), "GET", p, withCookie(tok)); w.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", p, w.Code)
		}
	}
}
