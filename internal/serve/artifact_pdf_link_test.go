package serve

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/artifactpdf"
	"github.com/blsmth/agentctl/internal/artifacts"
)

func pdfLinkPath(id, query string) string {
	p := "/v1/artifacts/" + id + "/pdf-link"
	if query != "" {
		p += "?" + query
	}
	return p
}

var pdfLinkURLRe = regexp.MustCompile(`^/pdf/[A-Za-z0-9_-]{43}/[^/]+$`)

// mintPDFLink creates a link and returns its URL.
func (ae *artifactEnv) mintPDFLink(t *testing.T, id, query string) string {
	t.Helper()
	w := ae.send(http.MethodPost, pdfLinkPath(id, query), `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("pdf-link status = %d: %s", w.Code, w.Body)
	}
	var out artifactPDFLinkOut
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !pdfLinkURLRe.MatchString(out.URL) {
		t.Fatalf("url = %q", out.URL)
	}
	return out.URL
}

// getPublic GETs a path with no cookie, as Safari outside the app would.
func (ae *artifactEnv) getPublic(path string, opts ...reqOpt) *httptest.ResponseRecorder {
	return do(ae.Handler(), http.MethodGet, path, append([]reqOpt{from("192.168.1.20:51515")}, opts...)...)
}

// assertPDFLinkMiss checks the /a/ 404 page.
func assertPDFLinkMiss(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", w.Code, w.Body)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q", ct)
	}
	if !strings.Contains(w.Body.String(), errorText(http.StatusNotFound)) {
		t.Errorf("body is not the /a/ 404 page: %s", w.Body)
	}
	if csp := w.Header().Get("Content-Security-Policy"); csp != artifactCSP {
		t.Errorf("CSP = %q", csp)
	}
}

func TestPDFLinkCreate(t *testing.T) {
	ae, fake := newPDFEnvWrite(t, true)
	m := ae.add(t, map[string]string{"r.html": "<p>x</p>"}, artifacts.AddOptions{Title: "Weekly Report"})

	t.Run("needs a paired device", func(t *testing.T) {
		w := ae.send(http.MethodPost, pdfLinkPath(m.ID, "rev=1"), `{}`, func(r *http.Request) {
			r.Header.Del("Cookie")
		})
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", w.Code)
		}
	})
	t.Run("needs the CSRF header", func(t *testing.T) {
		w := ae.send(http.MethodPost, pdfLinkPath(m.ID, "rev=1"), `{}`, withoutHeader(CSRFHeader))
		if w.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", w.Code)
		}
	})
	t.Run("not a GET", func(t *testing.T) {
		if w := ae.send(http.MethodGet, pdfLinkPath(m.ID, "rev=1"), ""); w.Code == http.StatusOK {
			t.Fatalf("GET minted a link: %s", w.Body)
		}
	})

	tests := []struct {
		name, id, query string
		want            int
	}{
		{"malformed id", "NOT_AN_ID", "", http.StatusNotFound},
		{"unknown id", strings.Repeat("a", artifacts.IDLen), "", http.StatusNotFound},
		{"rev zero", m.ID, "rev=0", http.StatusBadRequest},
		{"rev text", m.ID, "rev=abc", http.StatusBadRequest},
		{"unknown rev", m.ID, "rev=9", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if w := ae.send(http.MethodPost, pdfLinkPath(tt.id, tt.query), `{}`); w.Code != tt.want {
				t.Fatalf("status = %d, want %d: %s", w.Code, tt.want, w.Body)
			}
		})
	}

	u := ae.mintPDFLink(t, m.ID, "rev=1")
	if !strings.HasSuffix(u, "/weekly-report-r1.pdf") {
		t.Errorf("url = %q, want the file name last", u)
	}
	if n := fake.count(); n != 0 {
		t.Errorf("minting rendered %d times", n)
	}
	tok := strings.Split(u, "/")[2]
	var found bool
	for _, l := range ae.auditLines(t) {
		line, _ := json.Marshal(l)
		if strings.Contains(string(line), m.ID) || strings.Contains(string(line), tok) {
			t.Errorf("audit line has the full id or token: %s", line)
		}
		if l.Action == "artifact-pdf-link" && l.Result == "ok" {
			found = true
			if l.Artifact != artifacts.ShortID(m.ID) {
				t.Errorf("audit artifact = %q", l.Artifact)
			}
		}
	}
	if !found {
		t.Errorf("no ok audit line: %+v", ae.auditLines(t))
	}
}

func TestPDFLinkNeedsAllowWrite(t *testing.T) {
	ae, _ := newPDFEnvWrite(t, false)
	m := ae.add(t, map[string]string{"r.html": "<p>x</p>"}, artifacts.AddOptions{Title: "R"})
	if w := ae.send(http.MethodPost, pdfLinkPath(m.ID, ""), `{}`); w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestPDFLinkServesInline(t *testing.T) {
	ae, fake := newPDFEnvWrite(t, true)
	m := ae.add(t, map[string]string{"r.html": "<p>1</p>"}, artifacts.AddOptions{Title: "Weekly Report"})
	latest := ae.mintPDFLink(t, m.ID, "")
	// A new revision after minting: the link stays bound to the number it
	// resolved.
	src := filepath.Join(t.TempDir(), "r.html")
	if err := os.WriteFile(src, []byte("<p>2</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ae.store.Update(m.ID, artifacts.UpdateOptions{Source: artifacts.Source{Path: src}}); err != nil {
		t.Fatal(err)
	}

	w := ae.getPublic(latest)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}
	want := map[string]string{
		"Content-Type":           "application/pdf",
		"Content-Disposition":    `inline; filename="weekly-report-r1.pdf"`,
		"Cache-Control":          "private, no-store",
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "no-referrer",
		"Content-Length":         fmt.Sprint(w.Body.Len()),
	}
	for k, v := range want {
		if got := w.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if w.Header().Get("Content-Security-Policy") != "" {
		t.Errorf("a CSP sandbox would stop the browser's PDF viewer")
	}
	if !strings.HasPrefix(w.Body.String(), "%PDF-") || !strings.Contains(w.Body.String(), " r1 ") {
		t.Errorf("body = %q, want rev 1", w.Body)
	}

	// Reusable while it lives (a viewer reloads), through the cache.
	if w := ae.getPublic(latest); w.Code != http.StatusOK {
		t.Fatalf("second GET status = %d", w.Code)
	}
	// A range request, as a PDF viewer may send.
	if w := ae.getPublic(latest, withHeader("Range", "bytes=0-4")); w.Code != http.StatusPartialContent || w.Body.String() != "%PDF-" {
		t.Errorf("range: %d %q", w.Code, w.Body)
	}
	r2 := ae.mintPDFLink(t, m.ID, "rev=2")
	if w := ae.getPublic(r2); !strings.Contains(w.Body.String(), " r2 ") {
		t.Errorf("rev 2 link body = %q", w.Body)
	}
	if n := fake.count(); n != 2 {
		t.Errorf("renders = %d, want 2 (one per revision)", n)
	}
	// The name in the path is only for the browser.
	if w := ae.getPublic(strings.TrimSuffix(latest, "weekly-report-r1.pdf") + "other.pdf"); w.Code != http.StatusOK {
		t.Errorf("renamed path: status = %d", w.Code)
	}
}

func TestPDFLinkMisses(t *testing.T) {
	ae, fake := newPDFEnvWrite(t, true)
	m := ae.add(t, map[string]string{"r.html": "<p>x</p>"}, artifacts.AddOptions{Title: "R"})
	good := ae.mintPDFLink(t, m.ID, "rev=1")
	tok := strings.Split(good, "/")[2]

	// Count store reads: a bad token must not reach the store.
	var reads atomic.Int32
	dir := ae.store.Dir
	ae.store.Dir = func() (string, error) { reads.Add(1); return dir() }

	other := []byte(tok)
	other[0] ^= 1
	for _, path := range []string{
		"/pdf/" + string(other) + "/r-r1.pdf",
		"/pdf/" + strings.Repeat("A", 43) + "/r-r1.pdf",
		"/pdf/short/r-r1.pdf",
		"/pdf/" + tok,
		"/pdf/",
		"/pdf/" + tok + "/a/b",
		"/pdf/" + m.ID + "/r-r1.pdf",
	} {
		t.Run(path, func(t *testing.T) {
			assertPDFLinkMiss(t, ae.getPublic(path))
		})
	}
	if n := reads.Load(); n != 0 {
		t.Errorf("bad tokens read the store %d times", n)
	}
	if n := fake.count(); n != 0 {
		t.Errorf("bad tokens rendered %d times", n)
	}
	ae.store.Dir = dir

	t.Run("expired", func(t *testing.T) {
		u := ae.mintPDFLink(t, m.ID, "rev=1")
		ae.clock.Advance(pdfLinkTTL - time.Second)
		if w := ae.getPublic(u); w.Code != http.StatusOK {
			t.Fatalf("before expiry: status = %d", w.Code)
		}
		ae.clock.Advance(time.Second)
		assertPDFLinkMiss(t, ae.getPublic(u))
	})

	t.Run("deleted artifact", func(t *testing.T) {
		d := ae.add(t, map[string]string{"d.html": "<p>d</p>"}, artifacts.AddOptions{Title: "D"})
		u := ae.mintPDFLink(t, d.ID, "")
		if w := ae.getPublic(u); w.Code != http.StatusOK {
			t.Fatalf("status = %d", w.Code)
		}
		before := fake.count()
		// Removed by the CLI: the PDF is still cached, the link is a 404.
		if _, err := ae.store.Remove(d.ID); err != nil {
			t.Fatal(err)
		}
		assertPDFLinkMiss(t, ae.getPublic(u))
		if fake.count() != before {
			t.Errorf("rendered a deleted artifact")
		}
	})

	t.Run("revoked device", func(t *testing.T) {
		u := ae.mintPDFLink(t, m.ID, "rev=1")
		if _, err := ae.devices.Remove(ae.dev.ID); err != nil {
			t.Fatal(err)
		}
		assertPDFLinkMiss(t, ae.getPublic(u))
	})
}

func TestPDFLinkRenderErrors(t *testing.T) {
	t.Run("no browser", func(t *testing.T) {
		ae, fake := newPDFEnvWrite(t, true)
		fake.err = fmt.Errorf("%w: install Chrome", artifactpdf.ErrNoBrowser)
		m := ae.add(t, map[string]string{"r.html": "<p>x</p>"}, artifacts.AddOptions{Title: "R"})
		w := ae.getPublic(ae.mintPDFLink(t, m.ID, ""))
		if w.Code != http.StatusNotImplemented || !strings.Contains(w.Body.String(), artifactpdf.ConfigKey) {
			t.Fatalf("status = %d: %s", w.Code, w.Body)
		}
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("Content-Type = %q", ct)
		}
	})
	t.Run("failure hides details", func(t *testing.T) {
		ae, fake := newPDFEnvWrite(t, true)
		fake.err = errors.New("exit status 1: /tmp/secret")
		m := ae.add(t, map[string]string{"r.html": "<p>x</p>"}, artifacts.AddOptions{Title: "R"})
		w := ae.getPublic(ae.mintPDFLink(t, m.ID, ""))
		if w.Code != http.StatusBadGateway || strings.Contains(w.Body.String(), "secret") {
			t.Fatalf("status = %d: %s", w.Code, w.Body)
		}
	})
	t.Run("busy", func(t *testing.T) {
		ae, fake := newPDFEnvWrite(t, true)
		a := ae.add(t, map[string]string{"a.html": "<p>a</p>"}, artifacts.AddOptions{Title: "A"})
		b := ae.add(t, map[string]string{"b.html": "<p>b</p>"}, artifacts.AddOptions{Title: "B"})
		ua, ub := ae.mintPDFLink(t, a.ID, ""), ae.mintPDFLink(t, b.ID, "")
		fake.gate = make(chan struct{})
		fake.started = make(chan pdfKey, 4)
		ae.pdfs.wait = 50 * time.Millisecond
		done := make(chan int)
		go func() { done <- ae.getPublic(ua).Code }()
		<-fake.started
		w := ae.getPublic(ub)
		if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != pdfRetryAfter {
			t.Fatalf("status = %d, Retry-After %q", w.Code, w.Header().Get("Retry-After"))
		}
		close(fake.gate)
		if code := <-done; code != http.StatusOK {
			t.Fatalf("first: %d", code)
		}
	})
}

func TestPDFLinksStore(t *testing.T) {
	now := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
	p := newPDFLinks()
	first, _, err := p.mint(pdfLink{id: "first", rev: 1}, now)
	if err != nil {
		t.Fatal(err)
	}
	for i := range pdfLinkMax {
		if _, _, err := p.mint(pdfLink{id: fmt.Sprint("x", i), rev: 1}, now.Add(time.Duration(i+1)*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}
	if len(p.m) != pdfLinkMax {
		t.Errorf("links = %d, want the cap %d", len(p.m), pdfLinkMax)
	}
	if _, ok := p.lookup(first, now); ok {
		t.Errorf("the link closest to expiry survived the cap")
	}
	tok, _, _ := p.mint(pdfLink{id: "gone", rev: 2}, now)
	if l, ok := p.lookup(tok, now); !ok || l.id != "gone" || l.rev != 2 {
		t.Fatalf("lookup = %+v %v", l, ok)
	}
	p.forget("gone")
	if _, ok := p.lookup(tok, now); ok {
		t.Errorf("forget kept the link")
	}
	if _, ok := p.lookup(tok+"x", now); ok {
		t.Errorf("a malformed token matched")
	}
}
