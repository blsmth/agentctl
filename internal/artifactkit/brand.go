package artifactkit

import (
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/artifactbrand"
	"github.com/blsmth/agentctl/plugin/skills/artifacts/kit"
)

// SpecBrand returns the top-level "brand" of a spec, or "" when it is unset or
// the spec is not a JSON object. It does not validate.
func SpecBrand(data []byte) string {
	var top struct {
		Brand string `json:"brand"`
	}
	_ = json.Unmarshal(data, &top)
	return top.Brand
}

// Brand chrome is built here, in Go with escaped text, rather than by kit.js:
// it needs no script, so it also shows in print and when the spec fails to
// render, and the logo and firm name never depend on the spec JSON.
const brandChromeCSS = `.brand-bar{max-width:var(--max);margin:0 auto;padding:16px 16px 0;display:flex;align-items:center;gap:12px}
.brand-logo{display:block;width:auto;height:auto;max-width:200px;max-height:44px}
.brand-firm{font-weight:650;font-size:1.1rem}
h1,.block>h2{color:var(--accent)}
.brand-foot{max-width:var(--max);margin:8px auto 0;padding:16px;border-top:1px solid var(--line);color:var(--muted);font-size:.8rem}
@media print{.brand-bar,.brand-foot{max-width:none;padding-left:0;padding-right:0}.brand-foot{break-inside:avoid}}
`

// darkPrintCSS keeps a dark brand dark in print and PDF export: kit.css's
// print rules paint html and body white, and Chrome drops backgrounds unless
// asked to keep them.
const darkPrintCSS = `html,body{-webkit-print-color-adjust:exact;print-color-adjust:exact}
@media print{html,body{background:var(--bg)}}
`

var (
	rootBlockRE = regexp.MustCompile(`(?s):root\{(.*?)\}`)
	darkBlockRE = regexp.MustCompile(`(?s)@media\(prefers-color-scheme:dark\)\{:root\{(.*?)\}\}`)
)

// isDark reports whether the brand pins the dark palette.
func isDark(b *artifactbrand.Brand) bool { return b.Scheme == artifactbrand.SchemeDark }

// tokens parses the declarations of the first match of re in kit.css as
// ordered name/value pairs, without color-scheme.
func tokens(re *regexp.Regexp) (names []string, vals map[string]string) {
	vals = map[string]string{}
	m := re.FindStringSubmatch(kit.CSS)
	if m == nil {
		return nil, vals
	}
	for _, decl := range strings.Split(m[1], ";") {
		name, val, ok := strings.Cut(strings.TrimSpace(decl), ":")
		if !ok || name == "color-scheme" {
			continue
		}
		names = append(names, name)
		vals[name] = val
	}
	return names, vals
}

// lightTokens returns kit.css's first :root block (the light palette).
func lightTokens() ([]string, map[string]string) { return tokens(rootBlockRE) }

// brandTokens returns the palette a brand pins. The dark block holds only the
// colours, so the rest (radii, fonts) come from the light block.
func brandTokens(b *artifactbrand.Brand) ([]string, map[string]string) {
	names, vals := lightTokens()
	if !isDark(b) {
		return names, vals
	}
	_, dark := tokens(darkBlockRE)
	for k, v := range dark {
		vals[k] = v
	}
	return names, vals
}

// brandCSS builds the stylesheet that follows kit.css on a branded page: the
// light tokens restated after kit.css's dark-mode override (so no dark rule
// applies; a dark brand restates the dark tokens instead), the brand colours and font on top, and the header/footer styles.
func brandCSS(b *artifactbrand.Brand) string {
	names, vals := brandTokens(b)
	override := func(name, val string) {
		if val != "" {
			vals[name] = val
		}
	}
	override("--accent", b.Colors.Accent)
	override("--info", b.Colors.Accent)
	for i, c := range b.Colors.Chart {
		override(fmt.Sprintf("--c%d", i+1), c)
	}

	var css strings.Builder
	f := b.Font
	if f.Family != "" {
		for _, face := range []struct {
			file   *artifactbrand.File
			weight int
		}{{f.Regular, 400}, {f.Bold, 700}} {
			if face.file != nil {
				fmt.Fprintf(&css, "@font-face{font-family:%q;font-weight:%d;font-style:normal;font-display:swap;src:url(%s) format(\"woff2\")}\n",
					f.Family, face.weight, face.file.DataURI())
			}
		}
		vals["--font"] = fmt.Sprintf("%q,%s", f.Family, vals["--font"])
	}
	switch f.Stack {
	case "serif":
		vals["--font"] = `Georgia,"Times New Roman",Times,serif`
	case "mono":
		vals["--font"] = "var(--mono)"
	}

	css.WriteString(":root{")
	for _, n := range names {
		css.WriteString(n + ":" + vals[n] + ";")
	}
	scheme := "light"
	if isDark(b) {
		scheme = "dark"
	}
	css.WriteString("color-scheme:" + scheme + "}\n")
	css.WriteString("html{color-scheme:" + scheme + "}\n")
	if isDark(b) {
		css.WriteString(darkPrintCSS)
		css.WriteString("@page{background:" + vals["--bg"] + "}\n")
	}
	css.WriteString(brandChromeCSS)
	font := strings.ReplaceAll(vals["--font"], "var(--mono)", vals["--mono"])
	css.WriteString(pagedCSS(b, font, vals["--muted"], vals["--line"]))
	return css.String()
}

// cssString quotes s as a CSS string literal. Backslash, quote and every
// control character are escaped, so no brand text can end the string or the
// rule around it.
func cssString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f || r == '<' || r == '>':
			fmt.Fprintf(&b, "\\%x ", r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// pagedCSS is the print layout of a branded page: a cover page (the first
// page, no footer) and, on every other page, the brand footer on the left and
// "Page N of M" on the right in margin boxes. The font is spelled out because
// margin boxes do not see the :root tokens. The footer text is the brand
// footer, or the firm name when none is set.
func pagedCSS(b *artifactbrand.Brand, font, muted, line string) string {
	box := fmt.Sprintf("font-family:%s;font-size:9pt;color:%s;vertical-align:top;padding-top:10pt", font, muted)
	return fmt.Sprintf(`.brand-cover{display:none}
@media print{
.brand-cover{display:flex;flex-direction:column;justify-content:center;align-items:flex-start;gap:14pt;height:8.6in;break-after:page;break-inside:avoid}
.cover-logo{display:block;width:auto;height:auto;max-width:3.2in;max-height:1.4in}
.cover-firm{font-weight:650;font-size:1.3rem;color:var(--accent)}
.cover-title{font-size:2.4rem;line-height:1.15;font-weight:700;margin:.6in 0 0;color:var(--fg)}
.cover-summary{font-size:1.2rem;color:var(--muted);margin:0;max-width:5.5in}
.cover-for{font-size:1.1rem;margin:.4in 0 0;font-weight:600}
.cover-date{color:var(--muted);margin:0}
.brand-foot{display:none}
}
@page{margin:.5in .5in .75in;@bottom-left{content:%s;%s;text-align:left;border-top:.5pt solid %s}@bottom-right{content:"Page " counter(page) " of " counter(pages);%s;text-align:right;border-top:.5pt solid %s}}
@page :first{margin:.5in;@bottom-left{content:none;border:0}@bottom-right{content:none;border:0}}
`, cssString(footerText(b)), box, line, box, line)
}

func footerText(b *artifactbrand.Brand) string {
	if b.Footer != "" {
		return b.Footer
	}
	return b.Firm
}

// formatDate renders an ISO date or RFC 3339 datetime as "Jan 2, 2006" (UTC),
// the way kit.js shows it; anything else is returned as given.
func formatDate(s string) string {
	for _, layout := range []string{"2006-01-02", time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Format("Jan 2, 2006")
		}
	}
	return s
}

// brandHeader is the logo and firm name above the page. The logo stays an
// <img> data URI (never inlined SVG); with logo_has_name the name text is left
// out because the logo already shows it, and alt carries it for readers.
func brandHeader(b *artifactbrand.Brand) string {
	var s strings.Builder
	s.WriteString(`<div class="brand-bar">`)
	s.WriteString(logoAndName(b, "brand-logo", "brand-firm"))
	s.WriteString("</div>\n")
	return s.String()
}

func logoAndName(b *artifactbrand.Brand, logoClass, firmClass string) string {
	var s strings.Builder
	if b.Logo != nil {
		alt := ""
		if b.LogoHasName {
			alt = html.EscapeString(b.Firm)
		}
		s.WriteString(`<img class="` + logoClass + `" src="` + b.Logo.DataURI() + `" alt="` + alt + `">`)
	}
	if !(b.Logo != nil && b.LogoHasName) {
		s.WriteString(`<span class="` + firmClass + `">` + html.EscapeString(b.Firm) + "</span>")
	}
	return s.String()
}

// brandCover is the print-only first page: logo, title, summary, "Prepared
// for <client>", date and firm name. It is hidden on screen, where the header
// and kit.js's title block show the same facts. The page footer is switched
// off for the first page by pagedCSS.
func brandCover(b *artifactbrand.Brand, spec map[string]any) string {
	str := func(k string) string { s, _ := spec[k].(string); return s }
	var s strings.Builder
	s.WriteString(`<section class="brand-cover" aria-hidden="true">`)
	s.WriteString(logoAndName(b, "cover-logo", "cover-firm"))
	s.WriteString(`<h1 class="cover-title">` + html.EscapeString(str("title")) + "</h1>")
	if v := str("summary"); v != "" {
		s.WriteString(`<p class="cover-summary">` + html.EscapeString(v) + "</p>")
	}
	if v := str("client"); v != "" {
		s.WriteString(`<p class="cover-for">Prepared for ` + html.EscapeString(v) + "</p>")
	}
	if v := str("date"); v != "" {
		s.WriteString(`<p class="cover-date">` + html.EscapeString(formatDate(v)) + "</p>")
	}
	s.WriteString("</section>\n")
	return s.String()
}

func brandFooter(b *artifactbrand.Brand) string {
	return `<footer class="brand-foot">` + html.EscapeString(footerText(b)) + "</footer>\n"
}
