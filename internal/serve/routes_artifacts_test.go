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

// addLegacy is add, then the artifact is moved to a legacy 10-character id,
// as published before ids became capabilities: viewing it needs the cookie.
func (ae *artifactEnv) addLegacy(t *testing.T, files map[string]string, o artifacts.AddOptions) artifacts.Meta {
	t.Helper()
	m := ae.add(t, files, o)
	return ae.toLegacy(t, m)
}

// toLegacy renames artifact m to the legacy id m.ID[:10].
func (ae *artifactEnv) toLegacy(t *testing.T, m artifacts.Meta) artifacts.Meta {
	t.Helper()
	id := m.ID[:artifacts.LegacyIDLen]
	if err := os.Rename(filepath.Join(ae.root, m.ID), filepath.Join(ae.root, id)); err != nil {
		t.Fatal(err)
	}
	metaPath := filepath.Join(ae.root, id, "meta.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	raw["id"] = id
	if data, err = json.Marshal(raw); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metaPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ae.store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// capabilityPath reports an /a/ path that carries its credential: a token
// URL or a capability id's direct URL (CORS-readable).
func capabilityPath(p string) bool {
	if strings.HasPrefix(p, "/a/t/") {
		return true
	}
	seg, _, _ := strings.Cut(strings.TrimPrefix(p, "/a/"), "/")
	return strings.HasPrefix(p, "/a/") && artifacts.CapabilityID(seg)
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
	return ae.follow(t, ae.get(path), path)
}

// fetchPublic is fetch with no cookie at all, as an unpaired browser.
func (ae *artifactEnv) fetchPublic(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	return ae.follow(t, do(ae.Handler(), "GET", path), path)
}

func (ae *artifactEnv) follow(t *testing.T, w *httptest.ResponseRecorder, path string) *httptest.ResponseRecorder {
	t.Helper()
	assertSandboxedAt(t, w, capabilityPath(path))
	for i := 0; i < 3 && w.Code == http.StatusFound; i++ {
		loc := w.Header().Get("Location")
		if !capabilityPath(loc) {
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

// assertSandboxedAt also checks CORS: only capability responses (a token
// URL, a capability id's direct URL) are readable cross-origin.
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
	legacy := ae.addLegacy(t, map[string]string{"l.md": "# L"}, artifacts.AddOptions{})
	// The API enumerates ids, so it stays paired for every kind of id.
	for _, p := range []string{"/v1/artifacts", "/v1/artifacts/" + m.ID, "/v1/artifacts/" + legacy.ID} {
		for _, accept := range []string{"", "text/html"} {
			if w := do(ae.Handler(), "GET", p, withHeader("Accept", accept)); w.Code != http.StatusUnauthorized {
				t.Errorf("%s (accept %q) without cookie = %d", p, accept, w.Code)
			}
		}
		if w := do(ae.Handler(), "GET", p, withCookie("bogus")); w.Code != http.StatusUnauthorized {
			t.Errorf("%s with a bad cookie = %d", p, w.Code)
		}
	}
	// A legacy id's content without a (valid) cookie is the same 404 as an
	// id that does not exist, so a miss does not reveal the artifact.
	unknown := do(ae.Handler(), "GET", "/a/aaaaaaaaaa/latest/", withHeader("Accept", "text/html"))
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown id = %d", unknown.Code)
	}
	assertSandboxed(t, unknown)
	for _, p := range []string{"/a/" + legacy.ID + "/latest/", "/a/" + legacy.ID + "/1/l.md", "/a/" + legacy.ID + "/1"} {
		for _, opts := range [][]reqOpt{
			{withHeader("Accept", "text/html")},
			{withHeader("Accept", "")},
			{withHeader("Accept", "text/html"), withCookie("bogus")},
		} {
			w := do(ae.Handler(), "GET", p, opts...)
			if w.Code != http.StatusNotFound || w.Body.String() != unknown.Body.String() {
				t.Errorf("%s without a valid cookie = %d %q, want the unknown-id 404", p, w.Code, w.Body)
			}
			assertSandboxed(t, w)
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
	old = ae.toLegacy(t, old)
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
	// A capability id's raw URLs are the direct, numbered ones: no token.
	if b := all[1]; b.RawURL != "/a/"+other.ID+"/1/" || len(b.Revs) != 1 || b.Revs[0].RawURL != b.RawURL {
		t.Errorf("capability view raw urls = %q %+v", b.RawURL, b.Revs)
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

// TestArtifactContent runs every case for a legacy id (cookie, then the
// token redirect) and for a capability id (no cookie at all).
func TestArtifactContent(t *testing.T) {
	for _, mode := range []string{"legacy", "capability"} {
		t.Run(mode, func(t *testing.T) { testArtifactContent(t, mode == "legacy") })
	}
}

func testArtifactContent(t *testing.T, legacy bool) {
	ae := newArtifactEnv(t, false)
	add := ae.add
	if legacy {
		add = ae.addLegacy
	}
	site := add(t, map[string]string{
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
	md := add(t, map[string]string{"report.md": "# Report\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n<div id=\"raw\">raw html</div>\n\n```mermaid\ngraph TD; A-->B\n```\n"}, artifacts.AddOptions{})
	txt := add(t, map[string]string{"out.log": "line <b>1</b>\n&amp;"}, artifacts.AddOptions{})
	csvA := add(t, map[string]string{"t.csv": "name,qty\n<x>,1\n\"q,uoted\",2\n"}, artifacts.AddOptions{})
	img := add(t, map[string]string{"pic.svg": `<svg xmlns="http://www.w3.org/2000/svg"/>`}, artifacts.AddOptions{})

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
		{"unknown capability id", "/a/aaaaaaaaaaaaaaaaaaaaaaaaaa/latest/", 404, "text/html; charset=utf-8", "", nil, nil},
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
			fetch, cache := ae.fetch, tc.cache
			if !legacy {
				// No redirect: "latest" is served as itself and revalidated.
				fetch = ae.fetchPublic
				if cache == cacheArtifactRev && strings.Contains(tc.path, "/latest/") {
					cache = cacheArtifactLatest
				}
			}
			w := fetch(t, tc.path)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.status, w.Body)
			}
			if tc.ctype != "" && w.Header().Get("Content-Type") != tc.ctype {
				t.Errorf("Content-Type = %q, want %q", w.Header().Get("Content-Type"), tc.ctype)
			}
			if cache != "" && w.Header().Get("Cache-Control") != cache {
				t.Errorf("Cache-Control = %q, want %q", w.Header().Get("Cache-Control"), cache)
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
	m := ae.addLegacy(t, map[string]string{"docs/page.html": "<p>x</p>", "docs/a.css": "x"}, artifacts.AddOptions{Entry: "docs/page.html"})
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
	for _, m := range []artifacts.Meta{
		ae.add(t, map[string]string{"a.txt": "x"}, artifacts.AddOptions{}),
		ae.addLegacy(t, map[string]string{"b.txt": "x"}, artifacts.AddOptions{}),
	} {
		p := "/a/" + m.ID + "/latest/"
		w := ae.send("POST", p, "", withoutHeader(CSRFHeader))
		if w.Code != http.StatusForbidden {
			t.Errorf("POST %s without CSRF = %d", p, w.Code)
		}
		assertSandboxedAt(t, w, capabilityPath(p))
		w = ae.send("POST", p, "")
		if w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s = %d", p, w.Code)
		}
		assertSandboxedAt(t, w, capabilityPath(p))
	}
}

// A capability id is viewable by anyone who has the link, with no cookie:
// a directory artifact's entry and its relative assets, the rev root
// redirect, an entry in a subdirectory, and the errors, all sandboxed.
func TestArtifactCapabilityNoCookie(t *testing.T) {
	ae := newArtifactEnv(t, false)
	site := ae.add(t, map[string]string{
		"index.html":     `<link rel="stylesheet" href="css/app.css"><script src="js/app.js"></script><img src="img/dot.png">`,
		"css/app.css":    "body{color:red}",
		"js/app.js":      "console.log(1)",
		"img/dot.png":    "\x89PNG\r\n\x1a\nxx",
		"docs/page.html": "<p>page</p>",
	}, artifacts.AddOptions{})
	if !artifacts.CapabilityID(site.ID) {
		t.Fatalf("new artifact id %q is not a capability id", site.ID)
	}
	base := "/a/" + site.ID + "/latest/"
	entry := do(ae.Handler(), "GET", base)
	if entry.Code != http.StatusOK || !strings.Contains(entry.Body.String(), "css/app.css") {
		t.Fatalf("entry without cookie = %d %s", entry.Code, entry.Body)
	}
	assertSandboxedAt(t, entry, true)
	if entry.Header().Get("ETag") == "" || entry.Header().Get("Cache-Control") != cacheArtifactLatest {
		t.Errorf("latest caching = %q %q", entry.Header().Get("ETag"), entry.Header().Get("Cache-Control"))
	}
	// The browser resolves the relative URLs against base and sends no cookie.
	for rel, want := range map[string]string{"css/app.css": "body{color:red}", "js/app.js": "console.log(1)", "img/dot.png": "\x89PNG\r\n\x1a\nxx", "docs/page.html": "<p>page</p>"} {
		w := do(ae.Handler(), "GET", base+rel)
		if w.Code != http.StatusOK || w.Body.String() != want {
			t.Errorf("%s without cookie = %d %q", rel, w.Code, w.Body)
		}
		assertSandboxedAt(t, w, true)
	}
	if w := do(ae.Handler(), "GET", "/a/"+site.ID+"/1/css/app.css"); w.Code != http.StatusOK || w.Header().Get("Cache-Control") != cacheArtifactRev {
		t.Errorf("numbered rev = %d %q", w.Code, w.Header().Get("Cache-Control"))
	}
	// Revalidating latest with its ETag is a 304 until the next revision.
	etag := entry.Header().Get("ETag")
	if w := do(ae.Handler(), "GET", base, withHeader("If-None-Match", etag)); w.Code != http.StatusNotModified {
		t.Errorf("revalidate = %d", w.Code)
	}
	if _, err := ae.store.Update(site.ID, artifacts.UpdateOptions{Source: artifacts.Source{Path: writeTemp(t, "index.html", "<p>v2</p>")}}); err != nil {
		t.Fatal(err)
	}
	if w := do(ae.Handler(), "GET", base, withHeader("If-None-Match", etag)); w.Code != http.StatusOK || w.Body.String() != "<p>v2</p>" {
		t.Errorf("latest after update = %d %q", w.Code, w.Body)
	}
	// /a/<id>/<rev> redirects to its "/" form, with no cookie.
	w := do(ae.Handler(), "GET", "/a/"+site.ID+"/latest")
	if w.Code != http.StatusFound || w.Header().Get("Location") != base {
		t.Errorf("rev root = %d %q", w.Code, w.Header().Get("Location"))
	}
	assertSandboxedAt(t, w, true)
	w = do(ae.Handler(), "GET", "/a/"+site.ID)
	if w.Code != http.StatusFound || w.Header().Get("Location") != base {
		t.Errorf("id root = %d %q", w.Code, w.Header().Get("Location"))
	}
	assertSandboxedAt(t, w, true)
	unknown := do(ae.Handler(), "GET", "/a/aaaaaaaaaa/latest/").Body.String()
	legacy := ae.addLegacy(t, map[string]string{"l.txt": "x"}, artifacts.AddOptions{})
	for _, p := range []string{"/a/" + legacy.ID, "/a", "/a/"} {
		w := do(ae.Handler(), "GET", p)
		if w.Code != http.StatusNotFound || w.Body.String() != unknown {
			t.Errorf("%s without cookie = %d, want the unknown-id 404", p, w.Code)
		}
		assertSandboxed(t, w)
	}

	sub := ae.add(t, map[string]string{"docs/page.html": "<p>x</p>", "docs/a.css": "x"}, artifacts.AddOptions{Entry: "docs/page.html"})
	w = do(ae.Handler(), "GET", "/a/"+sub.ID+"/latest/?raw=1")
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/a/"+sub.ID+"/latest/docs/page.html?raw=1" {
		t.Errorf("subdir entry redirect = %d %q", w.Code, w.Header().Get("Location"))
	}
	if w := do(ae.Handler(), "GET", "/a/"+sub.ID+"/latest/docs/a.css"); w.Code != http.StatusOK || w.Body.String() != "x" {
		t.Errorf("subdir asset = %d %q", w.Code, w.Body)
	}

	// Errors without a cookie: sandboxed, and an unknown capability id is the
	// same page as an unknown legacy id.
	for path, status := range map[string]int{
		base + "nope.css":                       http.StatusNotFound,
		"/a/" + site.ID + "/0/":                 http.StatusBadRequest,
		base + "../meta.json":                   http.StatusBadRequest,
		base + "%2e%2e/%2e%2e/meta.json":        http.StatusBadRequest,
		"/a/aaaaaaaaaaaaaaaaaaaaaaaaaa/latest/": http.StatusNotFound,
	} {
		w := do(ae.Handler(), "GET", path, withHeader("Accept", "text/html"))
		if w.Code != status {
			t.Errorf("%s = %d, want %d", path, w.Code, status)
		}
		assertSandboxedAt(t, w, capabilityPath(path))
		if strings.Contains(w.Body.String(), `"entry"`) {
			t.Errorf("%s leaked meta.json", path)
		}
	}
	unknownCap := do(ae.Handler(), "GET", "/a/aaaaaaaaaaaaaaaaaaaaaaaaaa/latest/")
	unknownLegacy := do(ae.Handler(), "GET", "/a/aaaaaaaaaa/latest/")
	if unknownCap.Body.String() != unknownLegacy.Body.String() {
		t.Errorf("unknown capability id page differs from unknown legacy id page")
	}
}

// A legacy id with the cookie still resolves rev and redirects to the token
// URL; the content is not served at the cookie URL itself.
func TestArtifactLegacyWithCookieRedirects(t *testing.T) {
	ae := newArtifactEnv(t, false)
	m := ae.addLegacy(t, map[string]string{"index.html": "<p>L</p>"}, artifacts.AddOptions{})
	w := ae.get("/a/" + m.ID + "/latest/")
	loc := w.Header().Get("Location")
	if w.Code != http.StatusFound || !strings.HasPrefix(loc, "/a/t/") || !strings.HasSuffix(loc, "/"+m.ID+"/1/") || strings.Contains(w.Body.String(), "<p>L</p>") {
		t.Fatalf("legacy with cookie = %d %q", w.Code, loc)
	}
	assertSandboxedAt(t, w, false)
	if w := do(ae.Handler(), "GET", loc); w.Code != http.StatusOK || w.Body.String() != "<p>L</p>" {
		t.Errorf("token URL = %d %q", w.Code, w.Body)
	}
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
