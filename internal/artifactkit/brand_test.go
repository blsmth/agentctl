package artifactkit

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/artifactbrand"
	"github.com/blsmth/agentctl/plugin/skills/artifacts/kit"
)

const brandedSpec = `{"kind":"comparison","title":"Q4 review","client":"Acme <Corp>","brand":"lincoln-loop","blocks":[{"type":"text","text":"x"}]}`

func testBrand() *artifactbrand.Brand {
	return &artifactbrand.Brand{
		Name:   "lincoln-loop",
		Firm:   `Lincoln <b>Loop</b> & Co`,
		Logo:   &artifactbrand.File{Mime: "image/png", Data: []byte("\x89PNGdata")},
		Colors: artifactbrand.Colors{Accent: "#1d4ed8", Chart: [6]string{"#111111", "", "", "", "", "#666666"}},
		Font:   artifactbrand.Font{Family: "Inter", Regular: &artifactbrand.File{Mime: "font/woff2", Data: []byte("wOF2reg")}},
		Footer: `© 2026 <i>Lincoln</i> Loop · Confidential`,
	}
}

func TestRenderBrandedGolden(t *testing.T) {
	page, err := RenderBranded([]byte(brandedSpec), testBrand())
	if err != nil {
		t.Fatal(err)
	}
	html := string(page.HTML)
	brandCSS := html[strings.Index(html, `<style id="brand">`):]
	brandCSS = brandCSS[:strings.Index(brandCSS, "</style>")]

	for _, want := range []string{
		"--accent:#1d4ed8;", "--info:#1d4ed8;", "--c1:#111111;", "--c6:#666666;",
		"--c2:#0f766e;",                 // untouched chart colours keep the kit's value
		"--bg:#fff;", "--card:#f5f7fa;", // the whole light palette is restated
		"color-scheme:light}",
		`@font-face{font-family:"Inter";font-weight:400;`, "data:font/woff2;base64,",
		`--font:"Inter",system-ui`,
	} {
		if !strings.Contains(brandCSS, want) {
			t.Errorf("brand css lacks %q:\n%s", want, brandCSS)
		}
	}
	if strings.Contains(brandCSS, "--bg:#0f1216") || strings.Contains(brandCSS, "prefers-color-scheme") {
		t.Error("brand css carries dark tokens")
	}
	// the brand sheet comes after kit.css so it wins, and the light palette is pinned
	if strings.Index(html, `<style id="brand">`) < strings.Index(html, kit.CSS) {
		t.Error("brand style precedes kit.css")
	}
	for _, want := range []string{`<html lang="en" data-light>`, `<meta name="color-scheme" content="light">`} {
		if !strings.Contains(html, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(html, `content="light dark"`) {
		t.Error("page still allows dark")
	}

	// header above <main>, footer below, all text escaped
	logo := strings.Index(html, `<img class="brand-logo" src="data:image/png;base64,`)
	firm := strings.Index(html, `<span class="brand-firm">Lincoln &lt;b&gt;Loop&lt;/b&gt; &amp; Co</span>`)
	main := strings.Index(html, `<main id="kit-main">`)
	foot := strings.Index(html, `<footer class="brand-foot">© 2026 &lt;i&gt;Lincoln&lt;/i&gt; Loop · Confidential</footer>`)
	if logo < 0 || firm < logo || main < firm || foot < main {
		t.Errorf("chrome order wrong: logo=%d firm=%d main=%d foot=%d", logo, firm, main, foot)
	}
	if strings.Contains(html, "<b>Loop</b>") || strings.Contains(html, "<i>Lincoln</i>") {
		t.Error("brand text is not escaped")
	}
	// the client is rendered by kit.js from the (escaped) spec JSON
	if !strings.Contains(html, `"client":"Acme \u003cCorp\u003e"`) {
		t.Error("client missing from the embedded spec")
	}
	if !strings.Contains(kit.JS, `"Prepared for "`) {
		t.Error("kit.js does not render Prepared for")
	}
}

func TestRenderBrandedFooterFallsBackToFirm(t *testing.T) {
	b := testBrand()
	b.Footer = ""
	page, err := RenderBranded([]byte(brandedSpec), b)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(page.HTML), `<footer class="brand-foot">Lincoln &lt;b&gt;`) {
		t.Error("footer does not fall back to the firm name")
	}
}

func TestRenderBrandedStacks(t *testing.T) {
	for stack, want := range map[string]string{"serif": "--font:Georgia,", "mono": "--font:var(--mono);"} {
		b := &artifactbrand.Brand{Firm: "F", Font: artifactbrand.Font{Stack: stack}}
		page, err := RenderBranded([]byte(brandedSpec), b)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(page.HTML), want) {
			t.Errorf("%s: want %q", stack, want)
		}
	}
}

// TestRenderUnbrandedUnchanged: with no brand the page has none of the brand
// chrome, and Render and RenderBranded(nil) agree byte for byte.
func TestRenderUnbrandedUnchanged(t *testing.T) {
	a, err := Render([]byte(brandedSpec))
	if err != nil {
		t.Fatal(err)
	}
	b, err := RenderBranded([]byte(brandedSpec), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.HTML, b.HTML) {
		t.Error("Render != RenderBranded(nil)")
	}
	html := string(a.HTML)
	for _, banned := range []string{`lang="en" data-light`, `id="brand"`, "brand-bar", "brand-foot", `content="light">`} {
		if strings.Contains(html, banned) {
			t.Errorf("unbranded page contains %q", banned)
		}
	}
	want := "<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n" +
		"<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n" +
		"<meta name=\"color-scheme\" content=\"light dark\">\n<title>Q4 review</title>\n<style>\n" + kit.CSS +
		"</style>\n</head>\n<body>\n<main id=\"kit-main\"></main>\n<script type=\"application/json\" id=\"spec\">\n"
	if !strings.HasPrefix(html, want) {
		t.Error("unbranded shell changed")
	}
}

// TestBrandedPageNeverSaysAgentctl: outside code comments in the inlined CSS
// and JS, nothing visible names agentctl.
func TestBrandedPageNeverSaysAgentctl(t *testing.T) {
	page, err := RenderBranded([]byte(brandedSpec), testBrand())
	if err != nil {
		t.Fatal(err)
	}
	html := string(page.HTML)
	html = strings.Replace(html, kit.CSS, "", 1)
	html = strings.Replace(html, kit.JS, "", 1)
	if strings.Contains(strings.ToLower(html), "agentctl") {
		t.Error("branded page shell mentions agentctl")
	}
	noCSS := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(kit.CSS, "")
	noJS := regexp.MustCompile(`(?m)^\s*//.*$|\s//\s.*$`).ReplaceAllString(kit.JS, "")
	noJS = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(noJS, "")
	for name, src := range map[string]string{"kit.css": noCSS, "kit.js": noJS} {
		if strings.Contains(strings.ToLower(src), "agentctl") {
			t.Errorf("%s names agentctl outside a comment", name)
		}
	}
}

func TestSpecBrand(t *testing.T) {
	if got := SpecBrand([]byte(brandedSpec)); got != "lincoln-loop" {
		t.Errorf("SpecBrand = %q", got)
	}
	for _, in := range []string{`{}`, `[1]`, `nope`, `{"brand":3}`} {
		if got := SpecBrand([]byte(in)); got != "" {
			t.Errorf("SpecBrand(%s) = %q", in, got)
		}
	}
}

// The print repaint rule must not restate tokens on a branded page: it would
// out-specify the brand's :root{} and drop brand colours from client PDFs.
func TestKitPrintTokensSkipBrandedPages(t *testing.T) {
	if !strings.Contains(kit.CSS, ":root[data-print]:not([data-light]):not([data-dark])") {
		t.Error("kit.css data-print rule is not scoped with :not([data-light])")
	}
	if strings.Contains(strings.ReplaceAll(kit.CSS, ":root[data-print]:not([data-light]):not([data-dark])", ""), "[data-print]") {
		t.Error("kit.css has an unscoped [data-print] rule")
	}
}

func TestBrandedCoverAndPagedCSS(t *testing.T) {
	spec := `{"kind":"comparison","title":"Q4 <review>","summary":"All good","client":"Acme <Corp>","date":"2026-11-24","blocks":[{"type":"text","text":"x"}]}`
	page, err := RenderBranded([]byte(spec), testBrand())
	if err != nil {
		t.Fatal(err)
	}
	html := string(page.HTML)
	cover := html[strings.Index(html, `<section class="brand-cover"`):]
	cover = cover[:strings.Index(cover, "</section>")]
	for _, want := range []string{
		`<img class="cover-logo" src="data:image/png;base64,`, // the logo stays an <img> data URI
		`<span class="cover-firm">Lincoln &lt;b&gt;Loop&lt;/b&gt; &amp; Co</span>`,
		`<h1 class="cover-title">Q4 &lt;review&gt;</h1>`,
		`<p class="cover-summary">All good</p>`,
		`<p class="cover-for">Prepared for Acme &lt;Corp&gt;</p>`,
		`<p class="cover-date">Nov 24, 2026</p>`,
	} {
		if !strings.Contains(cover, want) {
			t.Errorf("cover lacks %q:\n%s", want, cover)
		}
	}
	if strings.Contains(html, "<svg") && strings.Contains(cover, "<svg") {
		t.Error("cover inlines an svg")
	}
	if strings.Index(html, `class="brand-cover"`) > strings.Index(html, `class="brand-bar"`) {
		t.Error("cover must precede the header")
	}
	css := html[strings.Index(html, `<style id="brand">`):]
	css = css[:strings.Index(css, "</style>")]
	for _, want := range []string{
		".brand-cover{display:none}", ".brand-foot{display:none}", "break-after:page",
		`@bottom-left{content:"© 2026 \3c i\3e Lincoln\3c /i\3e  Loop · Confidential";font-family:"Inter",system-ui`,
		`@bottom-right{content:"Page " counter(page) " of " counter(pages);`,
		"@page :first{margin:.5in;@bottom-left{content:none;border:0}@bottom-right{content:none;border:0}}",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("paged css lacks %q:\n%s", want, css)
		}
	}
}

func TestBrandedCoverMinimal(t *testing.T) {
	b := &artifactbrand.Brand{Firm: "Min"}
	page, err := RenderBranded([]byte(`{"kind":"comparison","title":"T","blocks":[{"type":"text","text":"x"}]}`), b)
	if err != nil {
		t.Fatal(err)
	}
	html := string(page.HTML)
	for _, no := range []string{"cover-summary", `class="cover-for"`, `class="cover-date"`, "cover-logo"} {
		if strings.Contains(html[strings.Index(html, `<section`):strings.Index(html, `</section>`)], no) {
			t.Errorf("cover has %s", no)
		}
	}
	if !strings.Contains(html, `<span class="cover-firm">Min</span>`) || !strings.Contains(html, `@bottom-left{content:"Min";`) {
		t.Error("firm name missing from cover or footer fallback")
	}
}

func TestLogoHasNameHidesFirmText(t *testing.T) {
	b := testBrand()
	b.LogoHasName = true
	page, err := RenderBranded([]byte(brandedSpec), b)
	if err != nil {
		t.Fatal(err)
	}
	html := string(page.HTML)
	if strings.Contains(html, `class="brand-firm"`) || strings.Contains(html, `class="cover-firm"`) {
		t.Error("firm name text shown beside a wordmark logo")
	}
	if !strings.Contains(html, `class="brand-logo" src="data:image/png;base64,`) || !strings.Contains(html, `alt="Lincoln &lt;b&gt;Loop&lt;/b&gt; &amp; Co"`) {
		t.Error("logo missing or without the firm name as alt")
	}
	if !strings.Contains(html, `<footer class="brand-foot">`) {
		t.Error("footer lost")
	}
	// without a logo the name is still shown
	b.Logo = nil
	page, _ = RenderBranded([]byte(brandedSpec), b)
	if !strings.Contains(string(page.HTML), `class="brand-firm"`) {
		t.Error("no logo and no firm name")
	}
}

func TestCSSString(t *testing.T) {
	got := cssString("a\"b\\c\nd</style>")
	want := `"a\"b\\c\a d\3c /style\3e "`
	if got != want {
		t.Errorf("cssString = %s, want %s", got, want)
	}
}

func darkBrand() *artifactbrand.Brand {
	b := testBrand()
	b.Scheme = artifactbrand.SchemeDark
	b.Colors.Accent = "#a78bfa"
	return b
}

func brandStyle(t *testing.T, html string) string {
	t.Helper()
	css := html[strings.Index(html, `<style id="brand">`):]
	return css[:strings.Index(css, "</style>")]
}

func TestRenderDarkBrand(t *testing.T) {
	page, err := RenderBranded([]byte(brandedSpec), darkBrand())
	if err != nil {
		t.Fatal(err)
	}
	html := string(page.HTML)
	css := brandStyle(t, html)
	for _, want := range []string{
		"--bg:#0f1216;", "--card:#181d24;", "--fg:#e8ebef;", "--accent:#a78bfa;", "--c1:#111111;",
		"--r:12px;", "color-scheme:dark}", "html{color-scheme:dark}",
		"@media print{html,body{background:var(--bg)}}", "print-color-adjust:exact", "@page{background:#0f1216}",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("dark brand css lacks %q:\n%s", want, css)
		}
	}
	for _, banned := range []string{"--bg:#fff;", "--card:#f5f7fa;", "color-scheme:light", "prefers-color-scheme"} {
		if strings.Contains(css, banned) {
			t.Errorf("dark brand css contains %q", banned)
		}
	}
	for _, want := range []string{`<html lang="en" data-dark>`, `<meta name="color-scheme" content="dark">`} {
		if !strings.Contains(html, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(html, "data-light>") {
		t.Error("dark page carries data-light")
	}
}

// TestLightBrandUnchanged pins the light brand sheet: an explicit "light"
// scheme and the zero value render exactly as before schemes existed.
func TestLightBrandUnchanged(t *testing.T) {
	zero, err := RenderBranded([]byte(brandedSpec), testBrand())
	if err != nil {
		t.Fatal(err)
	}
	b := testBrand()
	b.Scheme = artifactbrand.SchemeLight
	light, err := RenderBranded([]byte(brandedSpec), b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(zero.HTML, light.HTML) {
		t.Error("explicit light differs from the default")
	}
	css := brandStyle(t, string(light.HTML))
	wantHead := ":root{--bg:#fff;--fg:#1a1d21;--muted:#5f6b7a;--card:#f5f7fa;--line:#dde2e8;--accent:#1d4ed8;"
	if !strings.Contains(css, wantHead) || !strings.Contains(css, "color-scheme:light}\nhtml{color-scheme:light}\n"+brandChromeCSS) {
		t.Errorf("light brand css changed:\n%s", css)
	}
	if strings.Contains(css, "print-color-adjust") || strings.Contains(string(light.HTML), "data-dark>") {
		t.Error("light brand carries dark-only rules")
	}
}
