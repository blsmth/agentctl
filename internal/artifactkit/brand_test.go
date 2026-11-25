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
