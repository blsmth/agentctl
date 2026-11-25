package artifactkit

import (
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"strings"

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

var rootBlockRE = regexp.MustCompile(`(?s):root\{(.*?)\}`)

// lightTokens returns the declarations of kit.css's first :root block (the
// light palette) as ordered name/value pairs, without color-scheme.
func lightTokens() (names []string, vals map[string]string) {
	vals = map[string]string{}
	m := rootBlockRE.FindStringSubmatch(kit.CSS)
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

// brandCSS builds the stylesheet that follows kit.css on a branded page: the
// light tokens restated after kit.css's dark-mode override (so no dark rule
// applies), the brand colours and font on top, and the header/footer styles.
func brandCSS(b *artifactbrand.Brand) string {
	names, vals := lightTokens()
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
	css.WriteString("color-scheme:light}\n")
	css.WriteString("html{color-scheme:light}\n")
	css.WriteString(brandChromeCSS)
	return css.String()
}

func brandHeader(b *artifactbrand.Brand) string {
	var s strings.Builder
	s.WriteString(`<div class="brand-bar">`)
	if b.Logo != nil {
		s.WriteString(`<img class="brand-logo" src="` + b.Logo.DataURI() + `" alt="">`)
	}
	s.WriteString(`<span class="brand-firm">` + html.EscapeString(b.Firm) + "</span></div>\n")
	return s.String()
}

func brandFooter(b *artifactbrand.Brand) string {
	text := b.Footer
	if text == "" {
		text = b.Firm
	}
	return `<footer class="brand-foot">` + html.EscapeString(text) + "</footer>\n"
}
