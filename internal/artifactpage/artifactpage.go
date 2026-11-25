// Package artifactpage renders the agentctl HTML pages around artifact
// content that is not HTML itself: markdown, text and CSV entries, a bare
// image, and the small error page. internal/serve serves them to the viewer;
// internal/artifactpdf serves the same pages to a headless browser, so the
// two never drift. It also owns the fixed extension to Content-Type table.
package artifactpage

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	gmhtml "github.com/yuin/goldmark/renderer/html"

	"github.com/blsmth/agentctl/internal/artifacts"
)

// CSP is the Content-Security-Policy of every artifact response: no
// allow-same-origin, so the document runs in an opaque origin and cannot reach
// the app's API, and plain http: targets (other services on this machine) are
// blocked. The source list is otherwise open (CDN scripts, styles, fonts).
const CSP = "sandbox allow-scripts allow-forms allow-popups allow-modals allow-downloads; " +
	"default-src 'self' https: data: blob: 'unsafe-inline' 'unsafe-eval'; connect-src 'self' https:"

// MaxCSVRows bounds the rendered CSV table.
const MaxCSVRows = 5000

// types maps extensions to Content-Types. A fixed table (not the host's mime
// database) so the answer is the same everywhere; unknown extensions are
// application/octet-stream, which a browser never renders as HTML (nosniff).
var types = map[string]string{
	".html": "text/html; charset=utf-8", ".htm": "text/html; charset=utf-8",
	".css": "text/css; charset=utf-8",
	".js":  "text/javascript; charset=utf-8", ".mjs": "text/javascript; charset=utf-8",
	".json": "application/json; charset=utf-8", ".map": "application/json; charset=utf-8",
	".txt": "text/plain; charset=utf-8", ".log": "text/plain; charset=utf-8",
	".md": "text/plain; charset=utf-8", ".markdown": "text/plain; charset=utf-8",
	".csv": "text/plain; charset=utf-8", ".tsv": "text/plain; charset=utf-8",
	".xml": "application/xml; charset=utf-8",
	".svg": "image/svg+xml", ".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".gif": "image/gif", ".webp": "image/webp", ".avif": "image/avif", ".ico": "image/x-icon",
	".bmp":  "image/bmp",
	".woff": "font/woff", ".woff2": "font/woff2", ".ttf": "font/ttf", ".otf": "font/otf",
	".wasm": "application/wasm", ".pdf": "application/pdf",
	".mp4": "video/mp4", ".webm": "video/webm", ".mp3": "audio/mpeg", ".wav": "audio/wav", ".ogg": "audio/ogg",
}

// ContentType is the Content-Type for a file name.
func ContentType(name string) string {
	if t, ok := types[strings.ToLower(path.Ext(name))]; ok {
		return t
	}
	return "application/octet-stream"
}

// Rendered reports whether the entry file rel of an artifact of this kind is
// shown as an agentctl page instead of byte for byte (markdown and text).
func Rendered(kind string) bool {
	return kind == artifacts.KindMarkdown || kind == artifacts.KindText
}

// page is the data of the artifact template.
type page struct {
	Title   string
	Class   string
	Body    template.HTML
	Mermaid bool
}

var pageTmpl = template.Must(template.New("artifact").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>{{.Title}}</title>
<style>` + CSS + `</style>
</head><body class="{{.Class}}"><main>
{{.Body}}
</main>{{if .Mermaid}}
<script type="module">
const blocks = document.querySelectorAll("pre > code.language-mermaid");
if (blocks.length) {
  const { default: mermaid } = await import("https://cdn.jsdelivr.net/npm/mermaid@11/dist/mermaid.esm.min.mjs");
  for (const code of blocks) {
    const div = document.createElement("div");
    div.className = "mermaid";
    div.textContent = code.textContent;
    code.parentElement.replaceWith(div);
  }
  const dark = matchMedia("(prefers-color-scheme: dark)").matches && !matchMedia("print").matches;
  mermaid.initialize({ startOnLoad: false, theme: dark ? "dark" : "default" });
  await mermaid.run();
}
</script>{{end}}
</body></html>
`))

// CSS styles the agentctl pages, light and dark. Printing always uses the
// light palette on Letter paper, with wide content wrapped instead of
// clipped and rows, quotes and figures kept whole across page breaks.
const CSS = `
:root{--fg:#1d1d1f;--muted:#6e6e73;--bg:#fff;--code:#f3f3f5;--line:#d9d9de;--link:#0a64d6}
@media (prefers-color-scheme: dark){:root{--fg:#e8e8ed;--muted:#a1a1a6;--bg:#111113;--code:#1f1f23;--line:#3a3a40;--link:#5ea2ff}}
*{box-sizing:border-box}
html{-webkit-text-size-adjust:100%}
body{margin:0;background:var(--bg);color:var(--fg);font:16px/1.55 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Helvetica,Arial,sans-serif}
main{max-width:920px;margin:0 auto;padding:16px 18px 48px}
a{color:var(--link)}
h1,h2,h3,h4{line-height:1.25;margin:1.4em 0 .5em}
h1{font-size:1.7em}h2{font-size:1.35em;border-bottom:1px solid var(--line);padding-bottom:.2em}
img,svg,video{max-width:100%;height:auto}
code,pre{font:13px/1.45 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
:not(pre)>code{background:var(--code);padding:.1em .35em;border-radius:4px}
pre{background:var(--code);padding:12px;border-radius:8px;overflow:auto}
body.text pre{background:none;padding:0;white-space:pre-wrap;word-break:break-word}
blockquote{margin:0;padding:0 1em;color:var(--muted);border-left:3px solid var(--line)}
table{border-collapse:collapse;display:block;overflow:auto;max-width:100%}
th,td{border:1px solid var(--line);padding:6px 10px;text-align:left;vertical-align:top}
th{background:var(--code);position:sticky;top:0}
hr{border:0;border-top:1px solid var(--line)}
.note{color:var(--muted);font-size:.9em}
body.image main{text-align:center}
body.image img{max-height:9.5in;object-fit:contain}
@page{size:letter;margin:.5in}
@media print{
:root{--fg:#1d1d1f;--muted:#6e6e73;--bg:#fff;--code:#f3f3f5;--line:#d9d9de;--link:#0a64d6;color-scheme:light}
body{font-size:12pt;-webkit-print-color-adjust:exact;print-color-adjust:exact}
main{max-width:none;padding:0}
h1,h2,h3,h4{break-after:avoid}
pre{overflow:visible;white-space:pre-wrap;overflow-wrap:anywhere}
table{display:table;width:100%;overflow:visible;table-layout:auto}
thead{display:table-header-group}
tr,img,blockquote,svg,.mermaid{break-inside:avoid}
th{position:static}
th,td{overflow-wrap:anywhere}
}
`

// markdown renders GitHub-flavored markdown. Raw HTML passes through: the
// page is sandboxed like any HTML artifact.
var markdown = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	goldmark.WithRendererOptions(gmhtml.WithUnsafe()),
)

// Render builds the agentctl page of a markdown or text entry: kind is the
// artifact's kind, rel the entry's path (a .csv text entry is a table), title
// the page title, src the file's bytes.
func Render(kind, rel, title string, src []byte) ([]byte, error) {
	p := page{Title: title}
	switch {
	case kind == artifacts.KindMarkdown:
		var buf bytes.Buffer
		if err := markdown.Convert(src, &buf); err != nil {
			return nil, fmt.Errorf("rendering markdown: %w", err)
		}
		p.Class = "markdown"
		p.Body = template.HTML(buf.String()) //nolint:gosec // sandboxed by the caller, like an HTML artifact
		p.Mermaid = bytes.Contains(src, []byte("```mermaid")) || bytes.Contains(src, []byte("~~~mermaid"))
	case strings.EqualFold(path.Ext(rel), ".csv"):
		p.Class = "csv"
		p.Body = renderCSV(src)
	default:
		p.Class = "text"
		p.Body = preBlock(src)
	}
	var out bytes.Buffer
	if err := pageTmpl.Execute(&out, p); err != nil {
		return nil, fmt.Errorf("rendering page: %w", err)
	}
	return out.Bytes(), nil
}

var imageTmpl = template.Must(template.New("image").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>{{.Title}}</title>
<style>` + CSS + `</style>
</head><body class="image"><main><img src="{{.Src}}" alt="{{.Title}}"></main></body></html>
`))

// ImagePage wraps the image file name (a path relative to the page's own URL,
// served raw with ?raw=1) in a minimal agentctl page.
func ImagePage(title, name string) ([]byte, error) {
	src := url.PathEscape(path.Base(name)) + "?raw=1"
	var out bytes.Buffer
	if err := imageTmpl.Execute(&out, struct{ Title, Src string }{title, src}); err != nil {
		return nil, fmt.Errorf("rendering image page: %w", err)
	}
	return out.Bytes(), nil
}

var errorTmpl = template.Must(template.New("err").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>{{.}}</title>
<style>` + CSS + `</style>
</head><body><main><p class="note">{{.}}</p></main></body></html>
`))

// ErrorPage is a small HTML error page showing msg.
func ErrorPage(msg string) []byte {
	var out bytes.Buffer
	_ = errorTmpl.Execute(&out, msg)
	return out.Bytes()
}

// preBlock is src HTML-escaped in a <pre>.
func preBlock(src []byte) template.HTML {
	return template.HTML("<pre>" + template.HTMLEscapeString(string(src)) + "</pre>") //nolint:gosec // escaped
}

// renderCSV renders CSV as a table (the first row as the header), at most
// MaxCSVRows rows. Input that does not parse is shown as text.
func renderCSV(src []byte) template.HTML {
	rd := csv.NewReader(bytes.NewReader(src))
	rd.FieldsPerRecord = -1
	rd.LazyQuotes = true
	var b strings.Builder
	b.WriteString("<table>")
	rows, more := 0, false
	for {
		rec, err := rd.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return preBlock(src)
		}
		if rows == MaxCSVRows {
			more = true
			break
		}
		cell := "td"
		if rows == 0 {
			cell = "th"
			b.WriteString("<thead>")
		}
		b.WriteString("<tr>")
		for _, f := range rec {
			b.WriteString("<" + cell + ">" + template.HTMLEscapeString(f) + "</" + cell + ">")
		}
		b.WriteString("</tr>")
		if rows == 0 {
			b.WriteString("</thead><tbody>")
		}
		rows++
	}
	if rows == 0 {
		return preBlock(src)
	}
	b.WriteString("</tbody></table>")
	if more {
		b.WriteString(`<p class="note">Showing the first ` + strconv.Itoa(MaxCSVRows) + ` rows; open the raw file for the rest.</p>`)
	}
	return template.HTML(b.String()) //nolint:gosec // every cell is escaped
}
