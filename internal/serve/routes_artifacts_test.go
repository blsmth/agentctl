package serve

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/artifacts"
)

// artifactEnv is a write env over a temp artifact store.
type artifactEnv struct {
	*writeEnv
	store *artifacts.Store
	root  string
	now   time.Time
}

func newArtifactEnv(t *testing.T, allowWrite bool) *artifactEnv {
	t.Helper()
	ae := &artifactEnv{root: filepath.Join(t.TempDir(), "state", "artifacts"), now: time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)}
	ae.store = &artifacts.Store{
		Dir: func() (string, error) { return ae.root, nil },
		Now: func() time.Time { return ae.now },
	}
	ae.writeEnv = newWriteEnvWith(t, allowWrite, func(o *Options) { o.Artifacts = ae.store })
	return ae
}

// add publishes files (rel -> content) as one artifact; a single file is
// published as that file.
func (ae *artifactEnv) add(t *testing.T, files map[string]string, o artifacts.AddOptions) artifacts.Meta {
	t.Helper()
	src := filepath.Join(t.TempDir(), "src")
	for rel, content := range files {
		p := filepath.Join(src, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	o.Source = artifacts.Source{Path: src}
	if len(files) == 1 {
		for rel := range files {
			o.Source.Path = filepath.Join(src, rel)
		}
	}
	m, err := ae.store.Add(o)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (ae *artifactEnv) send(method, path, body string, opts ...reqOpt) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "192.168.1.20:51515"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(CSRFHeader, "1")
	r.AddCookie(&http.Cookie{Name: DeviceCookie, Value: ae.tok})
	for _, o := range opts {
		o(r)
	}
	w := httptest.NewRecorder()
	ae.Handler().ServeHTTP(w, r)
	return w
}

// fetch GETs an /a/ path with the device cookie and, when it redirects to a
// capability URL, follows it (and an entry redirect) without any cookie,
// checking the sandbox headers on every hop. It returns the last response.
func (ae *artifactEnv) fetch(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := ae.get(path)
	assertSandboxed(t, w)
	for i := 0; i < 3 && w.Code == http.StatusFound; i++ {
		loc := w.Header().Get("Location")
		if !strings.HasPrefix(loc, "/a/t/") {
			return w
		}
		w = do(ae.Handler(), "GET", loc)
		assertSandboxedAt(t, w, true)
	}
	return w
}

// assertSandboxed checks the headers every /a/ response must carry.
func assertSandboxed(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	assertSandboxedAt(t, w, false)
}

// assertSandboxedAt also checks CORS: only capability (/a/t/) responses are
// readable cross-origin.
func assertSandboxedAt(t *testing.T, w *httptest.ResponseRecorder, tokenPath bool) {
	t.Helper()
	h := w.Header()
	if got := h.Get("Access-Control-Allow-Origin"); (got == "*") != tokenPath || h.Get("Access-Control-Allow-Credentials") != "" {
		t.Errorf("ACAO = %q on token path %v", got, tokenPath)
	}
	csp := h.Get("Content-Security-Policy")
	if csp != artifactCSP {
		t.Errorf("CSP = %q, want the artifact sandbox", csp)
	}
	if !strings.HasPrefix(csp, "sandbox allow-scripts allow-forms allow-popups allow-modals allow-downloads;") {
		t.Errorf("CSP does not start with the sandbox directive: %q", csp)
	}
	if strings.Contains(csp, "allow-same-origin") || strings.Contains(strings.Join(h.Values("Content-Security-Policy"), " "), "allow-same-origin") {
		t.Errorf("CSP allows same origin: %q", csp)
	}
	if n := len(h.Values("Content-Security-Policy")); n != 1 {
		t.Errorf("%d CSP headers", n)
	}
	for k, want := range map[string]string{
		"X-Content-Type-Options":       "nosniff",
		"Referrer-Policy":              "no-referrer",
		"Cross-Origin-Resource-Policy": "cross-origin",
		"X-Frame-Options":              "SAMEORIGIN",
	} {
		if got := h.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func TestArtifactRoutesNeedDevice(t *testing.T) {
	ae := newArtifactEnv(t, true)
	m := ae.add(t, map[string]string{"r.md": "# R"}, artifacts.AddOptions{})
	for _, p := range []string{"/v1/artifacts", "/v1/artifacts/" + m.ID, "/a/" + m.ID + "/latest/", "/a/" + m.ID + "/1/r.md"} {
		for _, accept := range []string{"", "text/html"} {
			w := do(ae.Handler(), "GET", p, withHeader("Accept", accept))
			if w.Code != http.StatusUnauthorized {
				t.Errorf("%s (accept %q) without cookie = %d", p, accept, w.Code)
			}
			if strings.HasPrefix(p, "/a/") {
				assertSandboxed(t, w)
				if strings.Contains(w.Body.String(), "# R") {
					t.Errorf("%s leaked content without auth", p)
				}
			}
		}
		if w := do(ae.Handler(), "GET", p, withCookie("bogus")); w.Code != http.StatusUnauthorized {
			t.Errorf("%s with a bad cookie = %d", p, w.Code)
		}
	}
	for _, c := range []struct{ method, path string }{
		{"DELETE", "/v1/artifacts/" + m.ID},
		{"POST", "/v1/artifacts/" + m.ID + "/pin"},
	} {
		r := httptest.NewRequest(c.method, c.path, strings.NewReader(`{"pinned":true}`))
		r.RemoteAddr = "192.168.1.20:51515"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set(CSRFHeader, "1")
		w := httptest.NewRecorder()
		ae.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without cookie = %d", c.method, c.path, w.Code)
		}
	}
	if got, _ := ae.store.Get(m.ID); got.Pinned {
		t.Error("unauthenticated pin took effect")
	}
}

func TestArtifactListAndDetail(t *testing.T) {
	ae := newArtifactEnv(t, false)
	old := ae.add(t, map[string]string{"a.md": "# Old"}, artifacts.AddOptions{Origin: artifacts.Origin{Project: "proj", Agent: "go-engineer", ProjectDir: "/code/proj"}})
	ae.now = ae.now.Add(time.Hour)
	other := ae.add(t, map[string]string{"b.html": "<title>Other</title>"}, artifacts.AddOptions{Origin: artifacts.Origin{Project: "other"}})
	ae.now = ae.now.Add(time.Hour)
	if _, err := ae.store.Update(old.ID, artifacts.UpdateOptions{Source: artifacts.Source{Path: writeTemp(t, "a.md", "# New")}}); err != nil {
		t.Fatal(err)
	}

	type listBody struct {
		Artifacts []artifactView `json:"artifacts"`
	}
	list := func(q string) []artifactView {
		t.Helper()
		w := ae.get("/v1/artifacts" + q)
		if w.Code != http.StatusOK {
			t.Fatalf("list %s = %d %s", q, w.Code, w.Body)
		}
		var b listBody
		if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
			t.Fatal(err)
		}
		return b.Artifacts
	}
	all := list("")
	if len(all) != 2 || all[0].ID != old.ID || all[1].ID != other.ID {
		t.Fatalf("order = %+v, want updated newest first", all)
	}
	a := all[0]
	if a.URL != "/#/artifacts/"+old.ID || !strings.HasPrefix(a.RawURL, "/a/t/") || !strings.HasSuffix(a.RawURL, "/"+old.ID+"/2/") || a.Latest != 2 || a.RevCount != 2 ||
		a.Title != "Old" || a.Agent != "go-engineer" || a.Project != "proj" || a.Kind != "markdown" {
		t.Errorf("view = %+v", a)
	}
	if len(a.Revs) != 2 || !strings.HasSuffix(a.Revs[0].RawURL, "/"+old.ID+"/1/") || a.Revs[1].RawURL != a.RawURL || a.Revs[1].N != 2 {
		t.Errorf("revs = %+v", a.Revs)
	}
	if got := list("?project=other"); len(got) != 1 || got[0].ID != other.ID {
		t.Errorf("project filter = %+v", got)
	}
	if got := list("?project=/code/proj"); len(got) != 1 || got[0].ID != old.ID {
		t.Errorf("project dir filter = %+v", got)
	}
	// The test server clock is 2026-11-16; artifacts are dated later.
	if got := list("?since=2026-11-23T10:30:00Z"); len(got) != 1 || got[0].ID != old.ID {
		t.Errorf("since filter = %+v", got)
	}
	if w := ae.get("/v1/artifacts?since=yesterday"); w.Code != http.StatusBadRequest {
		t.Errorf("bad since = %d", w.Code)
	}

	w := ae.get("/v1/artifacts/" + other.ID)
	var d artifactView
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &d) != nil || d.ID != other.ID || d.Kind != "html" || len(d.Revs) != 1 {
		t.Errorf("detail = %d %s", w.Code, w.Body)
	}
	for _, id := range []string{"aaaaaaaaaa", "not-an-id"} {
		if w := ae.get("/v1/artifacts/" + id); w.Code != http.StatusNotFound {
			t.Errorf("detail %q = %d", id, w.Code)
		}
	}

	// An empty store lists [] (not null), including one that does not exist.
	empty := newArtifactEnv(t, false)
	if w := empty.get("/v1/artifacts"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"artifacts":[]`) {
		t.Errorf("empty list = %d %s", w.Code, w.Body)
	}
}

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestArtifactContent(t *testing.T) {
	ae := newArtifactEnv(t, false)
	site := ae.add(t, map[string]string{
		"index.html":     "<title>Site</title><script>parent.x=1</script>",
		"css/app.css":    "body{}",
		"js/app.js":      "console.log(1)",
		"data.json":      `{"a":1}`,
		"notes.md":       "# not rendered",
		"blob.weird":     "<html><script>alert(1)</script>",
		"img/logo.svg":   `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`,
		"img/dot.png":    "\x89PNG\r\n\x1a\nxx",
		"sub/index.html": "sub",
	}, artifacts.AddOptions{})
	md := ae.add(t, map[string]string{"report.md": "# Report\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n<div id=\"raw\">raw html</div>\n\n```mermaid\ngraph TD; A-->B\n```\n"}, artifacts.AddOptions{})
	txt := ae.add(t, map[string]string{"out.log": "line <b>1</b>\n&amp;"}, artifacts.AddOptions{})
	csvA := ae.add(t, map[string]string{"t.csv": "name,qty\n<x>,1\n\"q,uoted\",2\n"}, artifacts.AddOptions{})
	img := ae.add(t, map[string]string{"pic.svg": `<svg xmlns="http://www.w3.org/2000/svg"/>`}, artifacts.AddOptions{})

	// A symlink planted inside a revision dir (the store never creates one).
	revDir := filepath.Join(ae.root, site.ID, "rev", "1")
	secret := writeTemp(t, "secret.txt", "TOPSECRET")
	if err := os.Symlink(secret, filepath.Join(revDir, "leak.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(secret), filepath.Join(revDir, "leakdir")); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name, path string
		status     int
		ctype      string
		cache      string
		contains   []string
		absent     []string
	}{
		{"html entry", "/a/" + site.ID + "/latest/", 200, "text/html; charset=utf-8", cacheArtifactRev, []string{"<title>Site</title>"}, nil},
		{"html numbered", "/a/" + site.ID + "/1/index.html", 200, "text/html; charset=utf-8", cacheArtifactRev, nil, nil},
		{"css", "/a/" + site.ID + "/1/css/app.css", 200, "text/css; charset=utf-8", cacheArtifactRev, nil, nil},
		{"js", "/a/" + site.ID + "/latest/js/app.js", 200, "text/javascript; charset=utf-8", cacheArtifactRev, nil, nil},
		{"json", "/a/" + site.ID + "/latest/data.json", 200, "application/json; charset=utf-8", "", nil, nil},
		{"non-entry md is raw text", "/a/" + site.ID + "/latest/notes.md", 200, "text/plain; charset=utf-8", "", []string{"# not rendered"}, []string{"<h1"}},
		{"unknown ext never html", "/a/" + site.ID + "/latest/blob.weird", 200, "application/octet-stream", "", nil, nil},
		{"svg in html artifact", "/a/" + site.ID + "/latest/img/logo.svg", 200, "image/svg+xml", "", nil, nil},
		{"png", "/a/" + site.ID + "/latest/img/dot.png", 200, "image/png", "", nil, nil},
		{"dir index", "/a/" + site.ID + "/latest/sub/", 200, "text/html; charset=utf-8", "", []string{"sub"}, nil},
		{"markdown render", "/a/" + md.ID + "/latest/", 200, "text/html; charset=utf-8", cacheArtifactRev,
			[]string{"<title>Report</title>", "<h1 id=\"report\">Report</h1>", "<table>", `<div id="raw">raw html</div>`, "prefers-color-scheme: dark", "language-mermaid", "mermaid.esm"}, nil},
		{"markdown render numbered", "/a/" + md.ID + "/1/report.md", 200, "text/html; charset=utf-8", cacheArtifactRev, []string{"<h1"}, nil},
		{"markdown raw", "/a/" + md.ID + "/1/report.md?raw=1", 200, "text/plain; charset=utf-8", cacheArtifactRev, []string{"# Report"}, []string{"<h1"}},
		{"text render", "/a/" + txt.ID + "/latest/", 200, "text/html; charset=utf-8", "", []string{"<pre>line &lt;b&gt;1&lt;/b&gt;\n&amp;amp;</pre>"}, []string{"<b>1</b>"}},
		{"csv table", "/a/" + csvA.ID + "/latest/", 200, "text/html; charset=utf-8", "", []string{"<th>name</th>", "<td>&lt;x&gt;</td>", "<td>q,uoted</td>"}, []string{"<x>"}},
		{"image entry", "/a/" + img.ID + "/latest/", 200, "image/svg+xml", cacheArtifactRev, nil, nil},
		{"missing file", "/a/" + site.ID + "/latest/nope.html", 404, "text/html; charset=utf-8", "no-store", nil, nil},
		{"latest resolves to the newest rev", "/a/" + site.ID + "/latest/css/app.css", 200, "text/css; charset=utf-8", cacheArtifactRev, []string{"body{}"}, nil},
		{"missing rev", "/a/" + site.ID + "/9/", 404, "text/html; charset=utf-8", "no-store", nil, nil},
		{"unknown id", "/a/aaaaaaaaaa/latest/", 404, "text/html; charset=utf-8", "", nil, nil},
		{"bad id", "/a/NOT_AN_ID/latest/", 404, "text/html; charset=utf-8", "", nil, nil},
		{"bad rev", "/a/" + site.ID + "/0/", 400, "text/html; charset=utf-8", "", nil, nil},
		{"bad rev word", "/a/" + site.ID + "/head/", 400, "text/html; charset=utf-8", "", nil, nil},
		{"bad rev padded", "/a/" + site.ID + "/01/", 400, "text/html; charset=utf-8", "", nil, nil},
		{"dotdot", "/a/" + site.ID + "/latest/../../meta.json", 400, "text/html; charset=utf-8", "", nil, []string{`"entry"`}},
		{"dotdot to other artifact", "/a/" + site.ID + "/latest/css/../../../" + md.ID + "/meta.json", 400, "", "", nil, nil},
		{"encoded dotdot", "/a/" + site.ID + "/latest/%2e%2e/%2e%2e/meta.json", 400, "", "", nil, nil},
		{"encoded dotdot upper", "/a/" + site.ID + "/latest/%2E%2E/meta.json", 400, "", "", nil, nil},
		{"double encoded dotdot", "/a/" + site.ID + "/latest/%252e%252e/meta.json", 400, "", "", nil, nil},
		{"encoded slash", "/a/" + site.ID + "/latest/..%2f..%2fmeta.json", 400, "", "", nil, nil},
		{"encoded slash alone", "/a/" + site.ID + "/latest/css%2fapp.css", 400, "", "", nil, nil},
		{"encoded backslash", "/a/" + site.ID + "/latest/..%5c..%5cmeta.json", 400, "", "", nil, nil},
		{"absolute", "/a/" + site.ID + "/latest//etc/passwd", 400, "", "", nil, nil},
		{"dot segment", "/a/" + site.ID + "/latest/./index.html", 400, "", "", nil, nil},
		{"nul", "/a/" + site.ID + "/latest/index.html%00", 400, "", "", nil, nil},
		{"symlink file", "/a/" + site.ID + "/latest/leak.txt", 400, "text/html; charset=utf-8", "", nil, []string{"TOPSECRET"}},
		{"symlink dir", "/a/" + site.ID + "/latest/leakdir/secret.txt", 400, "", "", nil, []string{"TOPSECRET"}},
		{"bare /a", "/a", 404, "", "", nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := ae.fetch(t, tc.path)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.status, w.Body)
			}
			if tc.ctype != "" && w.Header().Get("Content-Type") != tc.ctype {
				t.Errorf("Content-Type = %q, want %q", w.Header().Get("Content-Type"), tc.ctype)
			}
			if tc.cache != "" && w.Header().Get("Cache-Control") != tc.cache {
				t.Errorf("Cache-Control = %q, want %q", w.Header().Get("Cache-Control"), tc.cache)
			}
			body := w.Body.String()
			for _, s := range tc.contains {
				if !strings.Contains(body, s) {
					t.Errorf("body lacks %q:\n%s", s, body)
				}
			}
			for _, s := range tc.absent {
				if strings.Contains(body, s) {
					t.Errorf("body has %q", s)
				}
			}
		})
	}
}

func TestArtifactContentRedirects(t *testing.T) {
	ae := newArtifactEnv(t, false)
	m := ae.add(t, map[string]string{"docs/page.html": "<p>x</p>", "docs/a.css": "x"}, artifacts.AddOptions{Entry: "docs/page.html"})
	w := ae.get("/a/" + m.ID + "/latest/?raw=1")
	loc := w.Header().Get("Location")
	if w.Code != http.StatusFound || !strings.HasPrefix(loc, "/a/t/") || !strings.HasSuffix(loc, "/"+m.ID+"/1/?raw=1") ||
		w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("cookie redirect = %d %q %q", w.Code, loc, w.Header().Get("Cache-Control"))
	}
	assertSandboxed(t, w)
	tokenRoot := strings.TrimSuffix(loc, "?raw=1")
	w = do(ae.Handler(), "GET", tokenRoot)
	if w.Code != http.StatusFound || w.Header().Get("Location") != tokenRoot+"docs/page.html" {
		t.Errorf("entry redirect = %d %q", w.Code, w.Header().Get("Location"))
	}
	assertSandboxedAt(t, w, true)
	w = do(ae.Handler(), "GET", strings.TrimSuffix(tokenRoot, "/"))
	if w.Code != http.StatusFound || w.Header().Get("Location") != tokenRoot {
		t.Errorf("token rev root redirect = %d %q", w.Code, w.Header().Get("Location"))
	}
	w = ae.get("/a/" + m.ID + "/1")
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/a/"+m.ID+"/1/" {
		t.Errorf("rev root redirect = %d %q", w.Code, w.Header().Get("Location"))
	}
	assertSandboxed(t, w)
	// Relative assets of the entry resolve under the token and need no cookie.
	if w := do(ae.Handler(), "GET", tokenRoot+"docs/a.css"); w.Code != http.StatusOK || w.Body.String() != "x" {
		t.Errorf("relative asset = %d %q", w.Code, w.Body)
	}
}

// Non-GET requests under /a/ are refused (CSRF, or no route: the catch-all
// 404) and still carry the sandbox headers.
func TestArtifactContentOtherMethods(t *testing.T) {
	ae := newArtifactEnv(t, false)
	m := ae.add(t, map[string]string{"a.txt": "x"}, artifacts.AddOptions{})
	w := ae.send("POST", "/a/"+m.ID+"/latest/", "", withoutHeader(CSRFHeader))
	if w.Code != http.StatusForbidden {
		t.Errorf("POST without CSRF = %d", w.Code)
	}
	assertSandboxed(t, w)
	w = ae.send("POST", "/a/"+m.ID+"/latest/", "")
	if w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d", w.Code)
	}
	assertSandboxed(t, w)
}

// The app's own pages keep the app CSP; only /a/ is sandboxed.
func TestArtifactSandboxOnlyUnderA(t *testing.T) {
	ae := newArtifactEnv(t, false)
	w := ae.get("/")
	if w.Header().Get("Content-Security-Policy") != cspHTML || w.Header().Get("X-Frame-Options") != "DENY" {
		t.Errorf("index headers = %v", w.Header())
	}
	w = ae.get("/v1/artifacts")
	if strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("/v1 got the artifact CSP")
	}
	// A path that only looks like /a/ is not.
	w = ae.get("/ab/x")
	if strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("/ab got the artifact CSP")
	}
}

func TestArtifactWritesNeedAllowWriteAndCSRF(t *testing.T) {
	ro := newArtifactEnv(t, false)
	m := ro.add(t, map[string]string{"a.txt": "x"}, artifacts.AddOptions{})
	for _, c := range []struct{ method, path, body string }{
		{"DELETE", "/v1/artifacts/" + m.ID, `{}`},
		{"POST", "/v1/artifacts/" + m.ID + "/pin", `{"pinned":true}`},
	} {
		w := ro.send(c.method, c.path, c.body)
		if w.Code != http.StatusForbidden || decodeError(t, w.Body.String()).Code != codeWriteDisabled {
			t.Errorf("%s %s with allow_write off = %d %s", c.method, c.path, w.Code, w.Body)
		}
	}
	if got, err := ro.store.Get(m.ID); err != nil || got.Pinned {
		t.Errorf("write took effect with allow_write off: %+v %v", got, err)
	}
	lines := ro.auditLines(t)
	if len(lines) != 2 || lines[0].Action != "artifact-delete" || lines[0].Result != codeWriteDisabled || lines[1].Action != "artifact-pin" {
		t.Errorf("audit = %+v", lines)
	}

	rw := newArtifactEnv(t, true)
	m = rw.add(t, map[string]string{"a.txt": "x"}, artifacts.AddOptions{})
	for _, c := range []struct{ method, path, body string }{
		{"DELETE", "/v1/artifacts/" + m.ID, `{}`},
		{"POST", "/v1/artifacts/" + m.ID + "/pin", `{"pinned":true}`},
	} {
		w := rw.send(c.method, c.path, c.body, withoutHeader(CSRFHeader))
		if w.Code != http.StatusForbidden || decodeError(t, w.Body.String()).Code != codeCSRF {
			t.Errorf("%s %s without CSRF = %d %s", c.method, c.path, w.Code, w.Body)
		}
	}
	if got, err := rw.store.Get(m.ID); err != nil || got.Pinned {
		t.Errorf("write took effect without CSRF: %+v %v", got, err)
	}
}

func TestArtifactPinAndDelete(t *testing.T) {
	ae := newArtifactEnv(t, true)
	m := ae.add(t, map[string]string{"a.txt": "x"}, artifacts.AddOptions{Origin: artifacts.Origin{Project: "proj", Agent: "go-engineer"}})

	for _, body := range []string{`{}`, `{"pinned":"yes"}`, `{"pinned":true,"x":1}`} {
		if w := ae.send("POST", "/v1/artifacts/"+m.ID+"/pin", body); w.Code != http.StatusBadRequest {
			t.Errorf("pin %s = %d", body, w.Code)
		}
	}
	w := ae.send("POST", "/v1/artifacts/"+m.ID+"/pin", `{"pinned":true}`)
	var v artifactView
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &v) != nil || !v.Pinned {
		t.Fatalf("pin = %d %s", w.Code, w.Body)
	}
	if got, _ := ae.store.Get(m.ID); !got.Pinned {
		t.Error("pin not stored")
	}
	if w := ae.send("POST", "/v1/artifacts/"+m.ID+"/pin", `{"pinned":false}`); w.Code != http.StatusOK {
		t.Errorf("unpin = %d", w.Code)
	}
	if got, _ := ae.store.Get(m.ID); got.Pinned {
		t.Error("unpin not stored")
	}
	if w := ae.send("POST", "/v1/artifacts/aaaaaaaaaa/pin", `{"pinned":true}`); w.Code != http.StatusNotFound {
		t.Errorf("pin unknown = %d", w.Code)
	}
	if w := ae.send("DELETE", "/v1/artifacts/"+m.ID, `{}`); w.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", w.Code, w.Body)
	}
	if _, err := ae.store.Get(m.ID); err == nil {
		t.Error("artifact still there after delete")
	}
	if w := ae.send("DELETE", "/v1/artifacts/"+m.ID, `{}`); w.Code != http.StatusNotFound {
		t.Errorf("delete again = %d", w.Code)
	}
	if w := ae.fetch(t, "/a/"+m.ID+"/latest/"); w.Code != http.StatusNotFound {
		t.Errorf("content after delete = %d", w.Code)
	}

	var actions []string
	for _, a := range ae.auditLines(t) {
		if a.Artifact != m.ID {
			continue // the unknown-id pin
		}
		actions = append(actions, a.Action+":"+a.Result)
		if a.Result == auditAttemptResult && (a.Project != "proj" || a.Agent != "go-engineer") {
			t.Errorf("attempt line lacks origin: %+v", a)
		}
	}
	want := "artifact-pin:bad_request artifact-pin:bad_request artifact-pin:bad_request " +
		"artifact-pin:attempt artifact-pin:ok artifact-pin:attempt artifact-pin:ok " +
		"artifact-delete:attempt artifact-delete:ok artifact-delete:not_found"
	if got := strings.Join(actions, " "); got != want {
		t.Errorf("audit actions =\n%s\nwant\n%s", got, want)
	}
}

func TestBadArtifactPath(t *testing.T) {
	tests := map[string]bool{
		"/a/abc/latest/":              false,
		"/a/abc/latest/x/y.css":       false,
		"/a/abc/1/100%25.png":         false,
		"/a/abc/latest/..":            true,
		"/a/abc/latest/%2e%2e":        true,
		"/a/abc/latest/%252e%252e":    true,
		"/a/abc/latest/a%2Fb":         true,
		"/a/abc/latest/a%5Cb":         true,
		"/a/abc/latest/a%00":          true,
		"/a/abc/latest//x":            true,
		"/a//latest/x":                true,
		"/a/abc/latest/.":             true,
		"/a/abc/latest/%2e/index.htm": true,
	}
	for target, want := range tests {
		r := httptest.NewRequest("GET", target, nil)
		if got := badArtifactPath(r.URL); got != want {
			t.Errorf("badArtifactPath(%q) = %v, want %v", target, got, want)
		}
	}
}
