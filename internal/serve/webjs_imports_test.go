package serve

import (
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"testing"
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
