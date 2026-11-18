package serve

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
)

var (
	importRe      = regexp.MustCompile(`(?s)\bimport\s*\{([^}]*)\}\s*from\s*["'](\./[^"']+)["']`)
	exportDeclRe  = regexp.MustCompile(`(?m)^export\s+(?:async\s+function\*?|function\*?|const|let|var|class)\s+([A-Za-z_$][\w$]*)`)
	exportBlockRe = regexp.MustCompile(`(?s)\bexport\s*\{([^}]*)\}`)
	lineCommentRe = regexp.MustCompile(`(?m)//.*$`)
)

// jsImports returns file -> imported names for every relative named import.
func jsImports(src string) map[string][]string {
	out := map[string][]string{}
	for _, m := range importRe.FindAllStringSubmatch(lineCommentRe.ReplaceAllString(src, ""), -1) {
		for _, spec := range strings.Split(m[1], ",") {
			name := strings.TrimSpace(spec)
			if name == "" {
				continue
			}
			// "a as b" imports a.
			name = strings.Fields(name)[0]
			out[m[2]] = append(out[m[2]], name)
		}
	}
	return out
}

// jsExports returns the set of names a module exports.
func jsExports(src string) map[string]bool {
	out := map[string]bool{}
	for _, m := range exportDeclRe.FindAllStringSubmatch(src, -1) {
		out[m[1]] = true
	}
	for _, m := range exportBlockRe.FindAllStringSubmatch(src, -1) {
		for _, spec := range strings.Split(m[1], ",") {
			f := strings.Fields(spec)
			if len(f) == 0 {
				continue
			}
			// "a as b" exports b.
			out[f[len(f)-1]] = true
		}
	}
	return out
}

// missingImports reports "file imports X from ./y.js" for names y.js does not export.
func missingImports(files map[string]string) []string {
	var problems []string
	for name, src := range files {
		for target, names := range jsImports(src) {
			tsrc, ok := files[path.Clean(target)]
			if !ok {
				problems = append(problems, name+" imports from missing file "+target)
				continue
			}
			exp := jsExports(tsrc)
			for _, n := range names {
				if !exp[n] {
					problems = append(problems, name+" imports "+n+" from "+target+", which does not export it")
				}
			}
		}
	}
	sort.Strings(problems)
	return problems
}

func TestWebJSImportsResolve(t *testing.T) {
	files := map[string]string{}
	entries, err := fs.ReadDir(webFS, "web")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".js") {
			continue
		}
		b, err := webFS.ReadFile("web/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		files[e.Name()] = string(b)
	}
	if len(files) == 0 {
		t.Fatal("no embedded web/*.js files found")
	}
	for _, p := range missingImports(files) {
		t.Error(p)
	}
}

func TestMissingImportsDetectsBrokenModule(t *testing.T) {
	files := map[string]string{
		"app.js":    `import { mountWindow, other as o } from "./window.js";`,
		"window.js": "export const other = 1;\nfunction exactView() {}\n",
		"x.js":      `import { a } from "./y.js"; // import { zz } from "./y.js"`,
		"y.js":      "export { b as a };\nexport async function z() {}\n",
	}
	got := missingImports(files)
	want := []string{"app.js imports mountWindow from ./window.js, which does not export it"}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("got %v, want %v", got, want)
	}
}

var (
	// jsRefRe matches a module specifier: `from "./x.js"` (named, default
	// and re-exports) and `import "./x.js"` / `import("./x.js")`.
	jsRefRe = regexp.MustCompile(`(?:\bfrom|\bimport)\s*\(?\s*["']((?:\.{1,2})?/[^"']+)["']`)
	// htmlRefRe matches a same-origin src/href ("#/..." routes excluded).
	htmlRefRe = regexp.MustCompile(`\b(?:src|href)\s*=\s*["'](/[^"'#?]*)["']`)
	// swShellRe captures the body of sw.js's precache list.
	swShellRe = regexp.MustCompile(`(?s)\bconst\s+SHELL\s*=\s*\[(.*?)\]`)
	quotedRe  = regexp.MustCompile(`["']([^"']+)["']`)
)

// webRef is one URL the web app loads, and where it is referenced.
type webRef struct {
	URL  string
	From string
}

// webRefs lists every same-origin URL the app shell loads: static imports in
// any .js file, src/href in index.html, the sw.js precache list and the
// manifest's icons. files maps a path under web/ to its contents.
func webRefs(files map[string]string) ([]webRef, error) {
	var refs []webRef
	for name, src := range files {
		switch {
		case strings.HasSuffix(name, ".js"):
			dir := "/" + path.Dir(name)
			for _, m := range jsRefRe.FindAllStringSubmatch(lineCommentRe.ReplaceAllString(src, ""), -1) {
				u := m[1]
				if !strings.HasPrefix(u, "/") {
					u = path.Join(dir, u)
				}
				refs = append(refs, webRef{u, name + " import"})
			}
		case name == "index.html":
			for _, m := range htmlRefRe.FindAllStringSubmatch(src, -1) {
				refs = append(refs, webRef{m[1], name})
			}
		case name == "manifest.webmanifest":
			var mf struct {
				Icons []struct{ Src string } `json:"icons"`
			}
			if err := json.Unmarshal([]byte(src), &mf); err != nil {
				return nil, fmt.Errorf("parsing %s: %w", name, err)
			}
			for _, ic := range mf.Icons {
				refs = append(refs, webRef{ic.Src, name})
			}
		}
	}
	shell, err := swShell(files["sw.js"])
	if err != nil {
		return nil, err
	}
	for _, u := range shell {
		refs = append(refs, webRef{u, "sw.js SHELL"})
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].URL != refs[j].URL {
			return refs[i].URL < refs[j].URL
		}
		return refs[i].From < refs[j].From
	})
	return refs, nil
}

// swShell parses the SHELL precache list out of sw.js.
func swShell(src string) ([]string, error) {
	m := swShellRe.FindStringSubmatch(src)
	if m == nil {
		return nil, fmt.Errorf("sw.js: no `const SHELL = [...]` precache list")
	}
	var out []string
	for _, q := range quotedRe.FindAllStringSubmatch(m[1], -1) {
		out = append(out, q[1])
	}
	return out, nil
}

// unlistedRefs reports references to URLs missing from the asset table
// ("/" is the index route, not a static asset).
func unlistedRefs(refs []webRef, table []staticAsset) []string {
	listed := map[string]bool{"/": true}
	for _, a := range table {
		listed[a.URL] = true
	}
	var out []string
	for _, r := range refs {
		if !listed[r.URL] {
			out = append(out, fmt.Sprintf("%s (referenced by %s) is not in staticAssets (assets.go), so it 404s", r.URL, r.From))
		}
	}
	return out
}

func embeddedWebFiles(t *testing.T) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := fs.WalkDir(webFS, "web", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		switch path.Ext(p) {
		case ".js", ".html", ".webmanifest":
			b, err := webFS.ReadFile(p)
			if err != nil {
				return err
			}
			files[strings.TrimPrefix(p, "web/")] = string(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// TestWebReferencesServed guards the class of bug where a new module is
// imported (or precached) but never added to the asset table: the import
// 404s and the whole app fails to load.
func TestWebReferencesServed(t *testing.T) {
	refs, err := webRefs(embeddedWebFiles(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) == 0 {
		t.Fatal("no web references found")
	}
	for _, p := range unlistedRefs(refs, staticAssets) {
		t.Error(p)
	}

	ts := newTestServer(t, config.ServeConfig{})
	_, tok := ts.pairDevice(t, "phone")
	seen := map[string]bool{}
	for _, r := range refs {
		if seen[r.URL] {
			continue
		}
		seen[r.URL] = true
		if w := do(ts.Handler(), "GET", r.URL, withCookie(tok)); w.Code != http.StatusOK {
			t.Errorf("GET %s (referenced by %s) = %d, want 200", r.URL, r.From, w.Code)
		}
	}
}

// TestServiceWorkerPrecachesShell keeps sw.js's SHELL in step with the asset
// table, so a new module is also available offline.
func TestServiceWorkerPrecachesShell(t *testing.T) {
	shell, err := swShell(embeddedWebFiles(t)["sw.js"])
	if err != nil {
		t.Fatal(err)
	}
	in := map[string]bool{}
	for _, u := range shell {
		in[u] = true
	}
	notPrecached := map[string]bool{"/sw.js": true, "/manifest.webmanifest": true}
	for _, a := range staticAssets {
		if !notPrecached[a.URL] && !in[a.URL] {
			t.Errorf("%s is served but missing from the sw.js SHELL precache list", a.URL)
		}
	}
}

func TestWebRefsCatchesUnlistedModule(t *testing.T) {
	files := map[string]string{
		"fleet.js":   "import {\n  a,\n} from \"./logic.js\";\nimport { attachSwipe } from \"./swipe.js\";\n// import { x } from \"./gone.js\";\nimport \"./side.js\";\nexport { b } from \"./re.js\";\n",
		"index.html": `<link rel="stylesheet" href="/app.css"><a href="#/settings">s</a><script type="module" src="/app.js"></script>`,
		"sw.js":      `const SHELL = [ "/", "/app.js", "/swipe.js" ];`,
	}
	refs, err := webRefs(files)
	if err != nil {
		t.Fatal(err)
	}
	var urls []string
	for _, r := range refs {
		urls = append(urls, r.URL)
	}
	want := "/ /app.css /app.js /app.js /logic.js /re.js /side.js /swipe.js /swipe.js"
	if got := strings.Join(urls, " "); got != want {
		t.Fatalf("refs = %q, want %q", got, want)
	}
	table := []staticAsset{{URL: "/app.css"}, {URL: "/app.js"}, {URL: "/logic.js"}, {URL: "/re.js"}, {URL: "/side.js"}}
	got := unlistedRefs(refs, table)
	if len(got) != 2 || !strings.Contains(got[0], "/swipe.js (referenced by fleet.js import)") || !strings.Contains(got[1], "sw.js SHELL") {
		t.Fatalf("unlisted = %q", got)
	}
}
