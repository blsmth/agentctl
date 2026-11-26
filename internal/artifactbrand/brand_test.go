package artifactbrand

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const goodSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><defs><linearGradient id="g"/></defs><rect width="10" height="10" fill="url(#g)"/></svg>`

var pngBytes = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 16)...)
var woff2Bytes = append([]byte("wOF2"), make([]byte, 16)...)

// writeBrand creates root/<name>/ with brand.json and the given files.
func writeBrand(t *testing.T, root, name, brandJSON string, files map[string][]byte) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "brand.json"), []byte(brandJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	for f, data := range files {
		if err := os.WriteFile(filepath.Join(dir, f), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func errPaths(err error) []string {
	var out []string
	if es, ok := err.(Errors); ok {
		for _, e := range es {
			out = append(out, e.Path)
		}
	}
	return out
}

func TestLoadFull(t *testing.T) {
	root := t.TempDir()
	writeBrand(t, root, "lincoln-loop", `{
		"name": "Lincoln Loop",
		"logo": "logo.svg",
		"colors": {"accent": "#1d4ed8", "c1": "#1D4ED8", "c6": "#334155"},
		"font": {"family": "Inter", "regular": "Inter-Regular.woff2", "bold": "Inter-Bold.woff2"},
		"footer": "© 2026 Lincoln Loop · Confidential"
	}`, map[string][]byte{"logo.svg": []byte(goodSVG), "Inter-Regular.woff2": woff2Bytes, "Inter-Bold.woff2": woff2Bytes})

	b, err := LoadFrom(root, "lincoln-loop")
	if err != nil {
		t.Fatal(err)
	}
	if b.Name != "lincoln-loop" || b.Firm != "Lincoln Loop" || b.Footer != "© 2026 Lincoln Loop · Confidential" {
		t.Errorf("brand = %+v", b)
	}
	if b.Logo == nil || !strings.HasPrefix(b.Logo.DataURI(), "data:image/svg+xml;base64,") {
		t.Errorf("logo = %+v", b.Logo)
	}
	if b.Colors.Accent != "#1d4ed8" || b.Colors.Chart[0] != "#1d4ed8" || b.Colors.Chart[5] != "#334155" || b.Colors.Chart[1] != "" {
		t.Errorf("colors = %+v", b.Colors)
	}
	if b.Font.Family != "Inter" || b.Font.Regular == nil || b.Font.Bold == nil {
		t.Errorf("font = %+v", b.Font)
	}
}

func TestLogoHasName(t *testing.T) {
	root := t.TempDir()
	files := map[string][]byte{"l.png": pngBytes}
	writeBrand(t, root, "on", `{"name":"X","logo":"l.png","logo_has_name":true}`, files)
	writeBrand(t, root, "off", `{"name":"X","logo":"l.png"}`, files)
	if b, err := LoadFrom(root, "on"); err != nil || !b.LogoHasName {
		t.Errorf("on: %+v %v", b, err)
	}
	if b, err := LoadFrom(root, "off"); err != nil || b.LogoHasName {
		t.Errorf("off: %+v %v", b, err)
	}
}

func TestLoadMinimalAndStack(t *testing.T) {
	root := t.TempDir()
	writeBrand(t, root, "min", `{"name":"Min"}`, nil)
	writeBrand(t, root, "serif", `{"name":"S","font":{"stack":"serif"}}`, nil)
	if b, err := LoadFrom(root, "min"); err != nil || b.Firm != "Min" {
		t.Errorf("min: %+v %v", b, err)
	}
	if b, err := LoadFrom(root, "serif"); err != nil || b.Font.Stack != "serif" {
		t.Errorf("serif: %+v %v", b, err)
	}
}

func TestValidation(t *testing.T) {
	big := make([]byte, MaxLogoBytes+1)
	copy(big, goodSVG)
	cases := []struct {
		name  string
		json  string
		files map[string][]byte
		path  string // an error must be reported here
		msg   string // and contain this
	}{
		{"missing name", `{}`, nil, "name", "required"},
		{"empty name", `{"name":" "}`, nil, "name", "must not be empty"},
		{"unknown field", `{"name":"X","theme":"dark"}`, nil, "theme", "unknown field"},
		{"unknown colour field", `{"name":"X","colors":{"accent":"#000000","bg":"#000000"}}`, nil, "colors.bg", "unknown field"},
		{"bad hex", `{"name":"X","colors":{"accent":"blue"}}`, nil, "colors.accent", "#rrggbb"},
		{"short hex", `{"name":"X","colors":{"c3":"#fff"}}`, nil, "colors.c3", "#rrggbb"},
		{"low contrast", `{"name":"X","colors":{"accent":"#7aa7ff"}}`, nil, "colors.accent", "contrast ratio 2."},
		{"bad scheme", `{"name":"X","scheme":"auto"}`, nil, "scheme", `must be "light" or "dark"`},
		{"scheme type", `{"name":"X","scheme":true}`, nil, "scheme", "expected a string"},
		{"dark accent too dark", `{"name":"X","scheme":"dark","colors":{"accent":"#1d4ed8"}}`, nil, "colors.accent", "#0f1216 (the dark background) is below 4.5:1 (links and headings use the accent); pick a lighter colour"},
		{"light accent too light", `{"name":"X","scheme":"light","colors":{"accent":"#7aa7ff"}}`, nil, "colors.accent", "pick a darker colour"},
		{"footer newline", `{"name":"X","footer":"a\nb"}`, nil, "footer", "one line"},
		{"footer type", `{"name":"X","footer":3}`, nil, "footer", "expected a string"},
		{"oversized logo", `{"name":"X","logo":"big.svg"}`, map[string][]byte{"big.svg": big}, "logo", "limit is 256 KB"},
		{"logo type", `{"name":"X","logo":"logo.gif"}`, map[string][]byte{"logo.gif": []byte("GIF89a")}, "logo", "only .svg and .png"},
		{"logo missing", `{"name":"X","logo":"nope.png"}`, nil, "logo", "file not found"},
		{"logo dotdot", `{"name":"X","logo":"../x.png"}`, nil, "logo", "relative path inside"},
		{"logo absolute", `{"name":"X","logo":"/etc/passwd.png"}`, nil, "logo", "relative path inside"},
		{"png not png", `{"name":"X","logo":"l.png"}`, map[string][]byte{"l.png": []byte("nope")}, "logo", "not a PNG"},
		{"svg script", `{"name":"X","logo":"l.svg"}`, map[string][]byte{"l.svg": []byte(`<svg><script>alert(1)</script></svg>`)}, "logo", "<script>"},
		{"svg onload", `{"name":"X","logo":"l.svg"}`, map[string][]byte{"l.svg": []byte(`<svg onload="x()"></svg>`)}, "logo", "event attribute"},
		{"svg external href", `{"name":"X","logo":"l.svg"}`, map[string][]byte{"l.svg": []byte(`<svg><image href="https://evil.example/x.png"/></svg>`)}, "logo", "external href"},
		{"svg xlink href", `{"name":"X","logo":"l.svg"}`, map[string][]byte{"l.svg": []byte(`<svg><use xlink:href="http://evil.example/x.svg#a"/></svg>`)}, "logo", "external href"},
		{"svg external url", `{"name":"X","logo":"l.svg"}`, map[string][]byte{"l.svg": []byte(`<svg><rect style="fill:url(http://evil.example/p)"/></svg>`)}, "logo", "external url()"},
		{"svg namespaced script", `{"name":"X","logo":"l.svg"}`, map[string][]byte{"l.svg": []byte(`<svg xmlns:s="http://www.w3.org/2000/svg"><s:script>alert(1)</s:script></svg>`)}, "logo", "<script>"},
		{"svg entity", `{"name":"X","logo":"l.svg"}`, map[string][]byte{"l.svg": []byte(`<?xml version="1.0"?><!DOCTYPE svg [<!ENTITY x "&#60;script&#62;">]><svg xmlns="http://www.w3.org/2000/svg">&x;</svg>`)}, "logo", "DTD"},
		{"svg not svg", `{"name":"X","logo":"l.svg"}`, map[string][]byte{"l.svg": []byte(`hello`)}, "logo", "not an SVG"},
		{"font not woff2 ext", `{"name":"X","font":{"family":"Inter","regular":"a.ttf"}}`, map[string][]byte{"a.ttf": woff2Bytes}, "font.regular", "only .woff2"},
		{"font not woff2 data", `{"name":"X","font":{"family":"Inter","regular":"a.woff2"}}`, map[string][]byte{"a.woff2": []byte("OTTO....")}, "font.regular", "not a woff2"},
		{"font too big", `{"name":"X","font":{"family":"Inter","regular":"a.woff2"}}`, map[string][]byte{"a.woff2": append([]byte("wOF2"), make([]byte, MaxFontBytes)...)}, "font.regular", "limit is 300 KB"},
		{"font family chars", `{"name":"X","font":{"family":"a;}b","regular":"a.woff2"}}`, map[string][]byte{"a.woff2": woff2Bytes}, "font.family", "letters, digits"},
		{"font stack unknown", `{"name":"X","font":{"stack":"cursive"}}`, nil, "font.stack", "serif"},
		{"font both", `{"name":"X","font":{"stack":"serif","family":"Inter"}}`, nil, "font", "not both"},
		{"font neither", `{"name":"X","font":{}}`, nil, "font", "needs"},
		{"font regular missing", `{"name":"X","font":{"family":"Inter"}}`, nil, "font.regular", "required"},
		{"logo_has_name type", `{"name":"X","logo":"l.png","logo_has_name":"yes"}`, map[string][]byte{"l.png": pngBytes}, "logo_has_name", "true or false"},
		{"logo_has_name without logo", `{"name":"X","logo_has_name":true}`, nil, "logo_has_name", "needs a logo"},
		{"not json", `{`, nil, "brand.json", "not valid JSON"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			writeBrand(t, root, "x", c.json, c.files)
			_, err := LoadFrom(root, "x")
			if err == nil {
				t.Fatal("accepted")
			}
			es, ok := err.(Errors)
			if !ok {
				t.Fatalf("error = %v", err)
			}
			found := slices.ContainsFunc(es, func(e Error) bool { return e.Path == c.path && strings.Contains(e.Msg, c.msg) })
			if !found {
				t.Errorf("want %s: ...%s..., got %v", c.path, c.msg, es)
			}
		})
	}
}

func TestValidationReportsEveryProblem(t *testing.T) {
	root := t.TempDir()
	writeBrand(t, root, "x", `{"colors":{"accent":"nope","c1":"#12"},"footer":1,"extra":true}`, nil)
	_, err := LoadFrom(root, "x")
	got := errPaths(err)
	for _, want := range []string{"name", "colors.accent", "colors.c1", "footer", "extra"} {
		if !slices.Contains(got, want) {
			t.Errorf("missing %s in %v", want, got)
		}
	}
}

func TestSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.svg")
	if err := os.WriteFile(outside, []byte(goodSVG), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := writeBrand(t, root, "x", `{"name":"X","logo":"link.svg"}`, nil)
	if err := os.Symlink(outside, filepath.Join(dir, "link.svg")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err := LoadFrom(root, "x")
	if es, _ := err.(Errors); len(es) != 1 || !strings.Contains(es[0].Msg, "points outside") {
		t.Errorf("err = %v", err)
	}
}

func TestSymlinkInsideAllowed(t *testing.T) {
	root := t.TempDir()
	dir := writeBrand(t, root, "x", `{"name":"X","logo":"link.png"}`, map[string][]byte{"real.png": pngBytes})
	if err := os.Symlink("real.png", filepath.Join(dir, "link.png")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if b, err := LoadFrom(root, "x"); err != nil || b.Logo == nil || b.Logo.Mime != "image/png" {
		t.Errorf("brand = %+v err = %v", b, err)
	}
}

func TestUnknownBrandListsAvailable(t *testing.T) {
	root := filepath.Join(t.TempDir(), "brands")
	_, err := LoadFrom(root, "acme")
	if err == nil || !strings.Contains(err.Error(), "available: none yet") || !strings.Contains(err.Error(), filepath.Join(root, "acme")) {
		t.Errorf("err = %v", err)
	}
	writeBrand(t, root, "beta", `{"name":"B"}`, nil)
	writeBrand(t, root, "alpha", `{"name":"A"}`, nil)
	_, err = LoadFrom(root, "acme")
	if err == nil || !strings.Contains(err.Error(), "available: alpha, beta") {
		t.Errorf("err = %v", err)
	}
	if _, err := LoadFrom(root, "../x"); err == nil || !strings.Contains(err.Error(), "invalid brand name") {
		t.Errorf("err = %v", err)
	}
}

func TestListIn(t *testing.T) {
	root := t.TempDir()
	if names, err := ListIn(filepath.Join(root, "missing")); err != nil || names != nil {
		t.Errorf("missing dir: %v %v", names, err)
	}
	writeBrand(t, root, "b", `{"name":"B"}`, nil)
	writeBrand(t, root, "a", `{"name":"A"}`, nil)
	if err := os.WriteFile(filepath.Join(root, "stray.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	names, err := ListIn(root)
	if err != nil || !slices.Equal(names, []string{"a", "b"}) {
		t.Errorf("names = %v err = %v", names, err)
	}
}

// TestDirFollowsConfigDir: the brands dir is resolved at call time from the
// config dir.
func TestDirFollowsConfigDir(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	dir, err := Dir()
	if err != nil || dir != filepath.Join(xdg, "agentctl", "brands") {
		t.Errorf("Dir = %q, %v", dir, err)
	}
	writeBrand(t, dir, "viaload", `{"name":"V"}`, nil)
	if b, err := Load("viaload"); err != nil || b.Firm != "V" {
		t.Errorf("Load = %+v %v", b, err)
	}
	if names, err := List(); err != nil || !slices.Equal(names, []string{"viaload"}) {
		t.Errorf("List = %v %v", names, err)
	}
}

func TestContrast(t *testing.T) {
	if r := contrast("#000000", "#ffffff"); r < 20.9 || r > 21.1 {
		t.Errorf("black on white = %v", r)
	}
	if r := contrast("#ffffff", "#ffffff"); r != 1 {
		t.Errorf("same = %v", r)
	}
	if r := contrast("#2563eb", "#ffffff"); r < 5 || r > 5.3 {
		t.Errorf("kit accent on white = %v", r)
	}
}

func TestSchemeDefaultsAndAccentPerScheme(t *testing.T) {
	root := t.TempDir()
	writeBrand(t, root, "def", `{"name":"D","colors":{"accent":"#1d4ed8"}}`, nil)
	writeBrand(t, root, "dark", `{"name":"D","scheme":"dark","colors":{"accent":"#7aa7ff"}}`, nil)
	if b, err := LoadFrom(root, "def"); err != nil || b.Scheme != SchemeLight {
		t.Errorf("default: %+v %v", b, err)
	}
	if b, err := LoadFrom(root, "dark"); err != nil || b.Scheme != SchemeDark {
		t.Errorf("dark: %+v %v", b, err)
	}
}

func TestDarkBackgroundsComeFromKitCSS(t *testing.T) {
	if got := darkBackgrounds(); len(got) != 2 || got[0] != "#0f1216" || got[1] != "#181d24" {
		t.Errorf("darkBackgrounds = %v", got)
	}
}
