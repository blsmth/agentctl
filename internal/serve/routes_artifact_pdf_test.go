package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/artifactpdf"
	"github.com/blsmth/agentctl/internal/artifacts"
)

// fakePDF stands in for artifactpdf.Render: no browser is ever launched.
type fakePDF struct {
	mu    sync.Mutex
	calls []pdfKey
	err   error
	// gate, when set, blocks every render until it is closed (or ctx ends).
	gate chan struct{}
	// started receives each render's key as it begins, when set.
	started chan pdfKey
	// canceled receives the key of a render whose ctx ended while gated.
	canceled chan pdfKey
}

func (f *fakePDF) render(ctx context.Context, _ *artifacts.Store, id string, rev int, w io.Writer) error {
	k := pdfKey{id: id, rev: rev}
	f.mu.Lock()
	f.calls = append(f.calls, k)
	gate, started, canceled, err := f.gate, f.started, f.canceled, f.err
	f.mu.Unlock()
	if started != nil {
		started <- k
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			if canceled != nil {
				canceled <- k
			}
			return ctx.Err()
		}
	}
	if err != nil {
		return err
	}
	_, werr := fmt.Fprintf(w, "%%PDF-1.4 %s r%d %%%%EOF", id, rev)
	return werr
}

func (f *fakePDF) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// newPDFEnv is an artifact env whose PDF renders go to the returned fake.
func newPDFEnv(t *testing.T) (*artifactEnv, *fakePDF) {
	t.Helper()
	fake := &fakePDF{}
	ae := &artifactEnv{root: filepath.Join(t.TempDir(), "state", "artifacts"), now: time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)}
	ae.store = &artifacts.Store{
		Dir: func() (string, error) { return ae.root, nil },
		Now: func() time.Time { return ae.now },
	}
	ae.writeEnv = newWriteEnvWith(t, false, func(o *Options) {
		o.Artifacts = ae.store
		o.RenderPDF = fake.render
	})
	return ae, fake
}

func pdfPath(id string, query string) string {
	p := "/v1/artifacts/" + id + "/pdf"
	if query != "" {
		p += "?" + query
	}
	return p
}

func decodeAPIError(t *testing.T, w *httptest.ResponseRecorder) apiError {
	t.Helper()
	var e apiError
	if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil {
		t.Fatalf("body %q is not a JSON error: %v", w.Body.String(), err)
	}
	return e
}

func TestArtifactPDFAuthAndValidation(t *testing.T) {
	ae, fake := newPDFEnv(t)
	m := ae.add(t, map[string]string{"r.html": "<p>x</p>"}, artifacts.AddOptions{Title: "Report"})

	t.Run("unpaired is refused", func(t *testing.T) {
		w := do(ae.Handler(), http.MethodGet, pdfPath(m.ID, ""))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", w.Code)
		}
		w = do(ae.Handler(), http.MethodGet, pdfPath(m.ID, ""), withCookie("not-a-token"))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("bad cookie status = %d, want 401", w.Code)
		}
	})

	notFound := ae.send(http.MethodGet, "/v1/artifacts/"+strings.Repeat("a", artifacts.IDLen), "")
	if notFound.Code != http.StatusNotFound {
		t.Fatalf("detail route status = %d", notFound.Code)
	}
	tests := []struct {
		name, path string
		want       int
	}{
		{"malformed id", pdfPath("NOT_AN_ID", ""), http.StatusNotFound},
		{"unknown id", pdfPath(strings.Repeat("a", artifacts.IDLen), ""), http.StatusNotFound},
		{"rev zero", pdfPath(m.ID, "rev=0"), http.StatusBadRequest},
		{"rev negative", pdfPath(m.ID, "rev=-1"), http.StatusBadRequest},
		{"rev text", pdfPath(m.ID, "rev=abc"), http.StatusBadRequest},
		{"rev padded", pdfPath(m.ID, "rev=01"), http.StatusBadRequest},
		{"unknown rev", pdfPath(m.ID, "rev=9"), http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := ae.send(http.MethodGet, tt.path, "")
			if w.Code != tt.want {
				t.Fatalf("status = %d, want %d: %s", w.Code, tt.want, w.Body)
			}
			e := decodeAPIError(t, w)
			if tt.want == http.StatusNotFound && (e.Code != codeNotFound || e.Error != decodeAPIError(t, notFound).Error) {
				t.Errorf("404 body = %+v, want the detail route's", e)
			}
		})
	}
	if n := fake.count(); n != 0 {
		t.Errorf("renders = %d, want 0", n)
	}
}

func TestArtifactPDFHeaders(t *testing.T) {
	ae, fake := newPDFEnv(t)
	m := ae.add(t, map[string]string{"r.html": "<p>1</p>"}, artifacts.AddOptions{Title: "Weekly Report"})
	src := filepath.Join(t.TempDir(), "r.html")
	if err := os.WriteFile(src, []byte("<p>2</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ae.store.Update(m.ID, artifacts.UpdateOptions{Source: artifacts.Source{Path: src}}); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct{ query, file string }{
		{"", "weekly-report-r2.pdf"},
		{"rev=latest", "weekly-report-r2.pdf"},
		{"rev=1", "weekly-report-r1.pdf"},
	} {
		t.Run("rev "+tt.query, func(t *testing.T) {
			w := ae.send(http.MethodGet, pdfPath(m.ID, tt.query), "")
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", w.Code, w.Body)
			}
			h := w.Header()
			want := map[string]string{
				"Content-Type":           "application/pdf",
				"Content-Disposition":    `attachment; filename="` + tt.file + `"`,
				"Cache-Control":          "private",
				"X-Content-Type-Options": "nosniff",
			}
			for k, v := range want {
				if got := h.Get(k); got != v {
					t.Errorf("%s = %q, want %q", k, got, v)
				}
			}
			if !strings.HasPrefix(w.Body.String(), "%PDF-") {
				t.Errorf("body = %q", w.Body)
			}
		})
	}
	// latest and rev=latest were keyed by the number: one render per revision.
	if n := fake.count(); n != 2 {
		t.Errorf("renders = %d (%v), want 2", n, fake.calls)
	}
	for _, k := range fake.calls {
		if k.rev == 0 {
			t.Errorf("rendered with rev 0: latest must be resolved first")
		}
	}
}

func TestPDFDisposition(t *testing.T) {
	id := "abcdefghijklmnopqrstuvwxyz"
	tests := []struct {
		name string
		m    artifacts.Meta
		rev  int
		want string
	}{
		{"slug", artifacts.Meta{ID: id, Slug: "weekly-report", Title: "Weekly report"}, 3,
			`attachment; filename="weekly-report-r3.pdf"`},
		{"hostile slug", artifacts.Meta{ID: id, Slug: `../a"b\c` + "\r\n" + `d;e`}, 1,
			`attachment; filename="a-b-c-d-e-r1.pdf"`},
		{"no slug, ascii-less title", artifacts.Meta{ID: id, Title: "日本語 レポート"}, 1,
			`attachment; filename="abcdef-r1.pdf"; filename*=UTF-8''%E6%97%A5%E6%9C%AC%E8%AA%9E-%E3%83%AC%E3%83%9D%E3%83%BC%E3%83%88-r1.pdf`},
		{"no slug, no title", artifacts.Meta{ID: id}, 2, `attachment; filename="abcdef-r2.pdf"`},
		{"no slug, title of separators", artifacts.Meta{ID: id, Title: `"/\`}, 1, `attachment; filename="abcdef-r1.pdf"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pdfDisposition(tt.m, tt.rev); got != tt.want {
				t.Errorf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
	if got := asciiFilename(strings.Repeat("x", 200)); len(got) > 60 {
		t.Errorf("asciiFilename not bounded: %d", len(got))
	}
}

func TestArtifactPDFCacheAndDelete(t *testing.T) {
	ae, fake := newPDFEnv(t)
	m := ae.add(t, map[string]string{"r.html": "<p>x</p>"}, artifacts.AddOptions{Title: "R"})
	for i := range 3 {
		if w := ae.send(http.MethodGet, pdfPath(m.ID, ""), ""); w.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d", i, w.Code)
		}
	}
	if n := fake.count(); n != 1 {
		t.Fatalf("renders = %d, want 1 (cache hits)", n)
	}
	// Removed by the CLI, behind serve's back: still cached, but a 404.
	if _, err := ae.store.Remove(m.ID); err != nil {
		t.Fatal(err)
	}
	if w := ae.send(http.MethodGet, pdfPath(m.ID, ""), ""); w.Code != http.StatusNotFound {
		t.Fatalf("deleted: status = %d, want 404", w.Code)
	}
	if w := ae.send(http.MethodGet, pdfPath(m.ID, "rev=1"), ""); w.Code != http.StatusNotFound {
		t.Fatalf("deleted rev=1: status = %d, want 404", w.Code)
	}
	if n := fake.count(); n != 1 {
		t.Errorf("renders = %d, want 1", n)
	}
}

func TestArtifactPDFErrors(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		status   int
		code     string
		contains string
	}{
		{"no browser", fmt.Errorf("%w: install Chrome", artifactpdf.ErrNoBrowser), http.StatusNotImplemented, codeNoBrowser, artifactpdf.ConfigKey},
		{"bad browser config", fmt.Errorf("x: %w", artifactpdf.ErrBadBrowserConfig), http.StatusNotImplemented, codeNoBrowser, artifactpdf.ConfigKey},
		{"timeout", fmt.Errorf("browser: %w", context.DeadlineExceeded), http.StatusBadGateway, codeRenderFailed, "timed out"},
		{"failure", errors.New("browser failed: exit status 1: /tmp/secret stderr"), http.StatusBadGateway, codeRenderFailed, "rendering the PDF failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ae, fake := newPDFEnv(t)
			fake.err = tt.err
			m := ae.add(t, map[string]string{"r.html": "<p>x</p>"}, artifacts.AddOptions{Title: "R"})
			w := ae.send(http.MethodGet, pdfPath(m.ID, ""), "")
			if w.Code != tt.status {
				t.Fatalf("status = %d, want %d: %s", w.Code, tt.status, w.Body)
			}
			if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Errorf("Content-Type = %q", ct)
			}
			e := decodeAPIError(t, w)
			if e.Code != tt.code || !strings.Contains(e.Error, tt.contains) || strings.Contains(e.Error, "secret") {
				t.Errorf("error = %+v", e)
			}
			// Failures are not cached: the next request renders again.
			ae.send(http.MethodGet, pdfPath(m.ID, ""), "")
			if n := fake.count(); n != 2 {
				t.Errorf("renders = %d, want 2", n)
			}
		})
	}
}

func TestArtifactPDFBusy(t *testing.T) {
	ae, fake := newPDFEnv(t)
	a := ae.add(t, map[string]string{"a.html": "<p>a</p>"}, artifacts.AddOptions{Title: "A"})
	b := ae.add(t, map[string]string{"b.html": "<p>b</p>"}, artifacts.AddOptions{Title: "B"})
	fake.gate = make(chan struct{})
	fake.started = make(chan pdfKey, 4)
	ae.pdfs.wait = 50 * time.Millisecond

	done := make(chan *httptest.ResponseRecorder)
	go func() { done <- ae.send(http.MethodGet, pdfPath(a.ID, ""), "") }()
	<-fake.started // a holds the only slot

	w := ae.send(http.MethodGet, pdfPath(b.ID, ""), "")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", w.Code, w.Body)
	}
	if ra := w.Header().Get("Retry-After"); ra != pdfRetryAfter {
		t.Errorf("Retry-After = %q", ra)
	}
	if e := decodeAPIError(t, w); e.Code != codeBusy {
		t.Errorf("code = %q", e.Code)
	}
	close(fake.gate)
	if w := <-done; w.Code != http.StatusOK {
		t.Fatalf("first request: status = %d", w.Code)
	}
	if n := fake.count(); n != 1 {
		t.Errorf("renders = %d, want 1 (b never started)", n)
	}
}

// newTestExporter is a pdfExporter over fake with a hook that reports each
// request joining a render.
func newTestExporter(fake *fakePDF) (*pdfExporter, chan int) {
	e := newPDFExporter(nil, fake.render)
	joined := make(chan int, 16)
	e.joined = func(n int) { joined <- n }
	return e, joined
}

func TestPDFExporterSingleflight(t *testing.T) {
	fake := &fakePDF{gate: make(chan struct{}), started: make(chan pdfKey, 4)}
	e, joined := newTestExporter(fake)
	k := pdfKey{id: "abc", rev: 1}

	type result struct {
		pdf []byte
		err error
	}
	out := make(chan result, 2)
	for range 2 {
		go func() {
			pdf, err := e.get(context.Background(), k)
			out <- result{pdf, err}
		}()
	}
	<-joined
	<-joined
	close(fake.gate)
	r1, r2 := <-out, <-out
	if r1.err != nil || r2.err != nil || string(r1.pdf) != string(r2.pdf) || len(r1.pdf) == 0 {
		t.Fatalf("results: %q %v / %q %v", r1.pdf, r1.err, r2.pdf, r2.err)
	}
	if n := fake.count(); n != 1 {
		t.Errorf("renders = %d, want 1", n)
	}
}

func TestPDFExporterLimit(t *testing.T) {
	fake := &fakePDF{gate: make(chan struct{}), started: make(chan pdfKey, 4)}
	e, _ := newTestExporter(fake)
	ka, kb := pdfKey{id: "a", rev: 1}, pdfKey{id: "b", rev: 1}

	errs := make(chan error, 2)
	go func() { _, err := e.get(context.Background(), ka); errs <- err }()
	if got := <-fake.started; got != ka {
		t.Fatalf("started %v", got)
	}
	go func() { _, err := e.get(context.Background(), kb); errs <- err }()
	select {
	case got := <-fake.started:
		t.Fatalf("%v started while %v held the slot", got, ka)
	case <-time.After(50 * time.Millisecond):
	}
	close(fake.gate)
	if got := <-fake.started; got != kb {
		t.Fatalf("started %v, want %v", got, kb)
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

func TestPDFExporterCancel(t *testing.T) {
	t.Run("last waiter leaving cancels the render", func(t *testing.T) {
		fake := &fakePDF{gate: make(chan struct{}), started: make(chan pdfKey, 4), canceled: make(chan pdfKey, 4)}
		e, _ := newTestExporter(fake)
		k := pdfKey{id: "a", rev: 1}
		ctx, cancel := context.WithCancel(context.Background())
		errs := make(chan error, 1)
		go func() { _, err := e.get(ctx, k); errs <- err }()
		<-fake.started
		cancel()
		if err := <-errs; !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
		if got := <-fake.canceled; got != k {
			t.Fatalf("canceled %v", got)
		}
		// The next request starts a fresh render rather than joining the
		// canceled one.
		close(fake.gate)
		if _, err := e.get(context.Background(), k); err != nil {
			t.Fatal(err)
		}
		if n := fake.count(); n != 2 {
			t.Errorf("renders = %d, want 2", n)
		}
	})
	t.Run("a waiter leaving does not cancel the others", func(t *testing.T) {
		fake := &fakePDF{gate: make(chan struct{}), started: make(chan pdfKey, 4), canceled: make(chan pdfKey, 4)}
		e, joined := newTestExporter(fake)
		k := pdfKey{id: "a", rev: 1}
		ctx, cancel := context.WithCancel(context.Background())
		errs := make(chan error, 2)
		go func() { _, err := e.get(ctx, k); errs <- err }()
		go func() { _, err := e.get(context.Background(), k); errs <- err }()
		<-joined
		<-joined
		cancel()
		if err := <-errs; !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
		close(fake.gate)
		if err := <-errs; err != nil {
			t.Fatalf("remaining waiter: %v", err)
		}
		if len(fake.canceled) != 0 {
			t.Error("render was canceled")
		}
	})
}

func TestPDFCacheLRU(t *testing.T) {
	c := newPDFCache(10)
	a, b, d := pdfKey{"a", 1}, pdfKey{"b", 1}, pdfKey{"d", 1}
	c.put(a, []byte("aaaa"))
	c.put(b, []byte("bbbb"))
	if _, ok := c.get(a); !ok { // a is now most recent
		t.Fatal("a missing")
	}
	c.put(d, []byte("dddd")) // 12 > 10: evicts b, the least recently used
	if _, ok := c.get(b); ok {
		t.Error("b not evicted")
	}
	if _, ok := c.get(a); !ok {
		t.Error("a evicted")
	}
	if c.size != 8 {
		t.Errorf("size = %d", c.size)
	}
	c.put(pdfKey{"big", 1}, make([]byte, 11))
	if _, ok := c.get(pdfKey{"big", 1}); ok || c.size != 8 {
		t.Error("an entry over the cap was cached")
	}
	c.put(pdfKey{"a", 2}, []byte("a2"))
	c.forget("a")
	if _, ok := c.get(a); ok {
		t.Error("forget kept a r1")
	}
	if _, ok := c.get(pdfKey{"a", 2}); ok {
		t.Error("forget kept a r2")
	}
	if c.size != 4 {
		t.Errorf("size after forget = %d", c.size)
	}
}
