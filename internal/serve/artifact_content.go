package serve

import (
	"bytes"
	"encoding/csv"
	"errors"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	gmhtml "github.com/yuin/goldmark/renderer/html"

	"github.com/blsmth/agentctl/internal/artifacts"
	"github.com/blsmth/agentctl/internal/logging"
)

// Artifact content is served under /a/ on the app's own origin, in a
// sandbox: artifactCSP has no allow-same-origin, so the document runs in an
// opaque origin and its script cannot read any /v1 response or send the CSRF
// header. The device cookie only authenticates the entry request at
// /a/<id>/<rev>/<path>, which redirects to the capability URL
// /a/t/<token>/<id>/<n>/<path> (artifact_token.go); everything the document
// then loads by relative URL carries the token. The source list is
// deliberately open (CDN scripts, styles, fonts): the protection is the
// opaque origin.
const artifactCSP = "sandbox allow-scripts allow-forms allow-popups allow-modals allow-downloads; " +
	"default-src 'self' https: data: blob: 'unsafe-inline' 'unsafe-eval'; connect-src 'self' https:"

// cacheArtifactRev is the Cache-Control of content at a capability URL: a
// numbered revision never changes. The cookie redirects are no-store.
const cacheArtifactRev = "private, max-age=31536000, immutable"

// maxCSVRows bounds the rendered CSV table.
const maxCSVRows = 5000

// isArtifactPath reports whether a request path is under /a/.
func isArtifactPath(p string) bool { return p == "/a" || strings.HasPrefix(p, "/a/") }

// sandboxArtifacts is the outermost middleware for /a/: every response there,
// whatever writes it (the CIDR filter, the CSRF check, the device check, the
// mux's 404/405, the handler), leaves with the sandbox CSP and the artifact
// security headers, applied as the status line is written so nothing earlier
// in the chain (writeHTML's cspHTML, securityHeaders' X-Frame-Options DENY)
// can replace them. A path that tries to leave the revision dir is refused
// here with 400, before the mux would clean and redirect it.
func sandboxArtifacts(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isArtifactPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		sw := &sandboxWriter{ResponseWriter: w, cors: isArtifactTokenPath(r.URL.Path)}
		if badArtifactPath(r.URL) {
			writeArtifactError(sw, http.StatusBadRequest, "Invalid artifact path.")
			return
		}
		next.ServeHTTP(sw, r)
	})
}

// sandboxWriter forces the artifact headers onto the response.
type sandboxWriter struct {
	http.ResponseWriter
	cors  bool
	wrote bool
}

func (w *sandboxWriter) WriteHeader(code int) {
	if !w.wrote {
		w.wrote = true
		setArtifactHeaders(w.Header(), w.cors)
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *sandboxWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the connection.
func (w *sandboxWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// setArtifactHeaders sets the security headers of every /a/ response.
//
// Cross-Origin-Resource-Policy is cross-origin: the artifact document's
// opaque origin is cross-origin to everything, so same-origin would make the
// browser drop its own stylesheets and images. On capability paths (cors)
// Access-Control-Allow-Origin is * without credentials, so ES modules and
// fetch() of the artifact's own files work from the opaque origin; the token
// is the credential and grants one revision of one artifact.
//
// X-Frame-Options is SAMEORIGIN (not the app's DENY) so the PWA can frame
// the artifact; /v1 pages keep frame-ancestors 'none'. Referrer-Policy
// no-referrer keeps the token out of Referer headers to CDNs.
func setArtifactHeaders(h http.Header, cors bool) {
	h.Set("Content-Security-Policy", artifactCSP)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cross-Origin-Resource-Policy", "cross-origin")
	h.Set("X-Frame-Options", "SAMEORIGIN")
	if cors {
		h.Set("Access-Control-Allow-Origin", "*")
	} else {
		h.Del("Access-Control-Allow-Origin")
	}
}

// badArtifactPath reports a /a/ URL that tries to escape: a "." or ".."
// segment (also double-encoded, %252e%252e), an empty segment ("//", an
// absolute path), an encoded slash or backslash, a backslash or NUL. Only a
// trailing "/" (a directory URL) may leave an empty last segment.
func badArtifactPath(u *url.URL) bool {
	raw := strings.ToLower(u.EscapedPath())
	for _, enc := range []string{"%2f", "%5c", "%00"} {
		if strings.Contains(raw, enc) {
			return true
		}
	}
	p := u.Path
	if strings.ContainsAny(p, "\\\x00") {
		return true
	}
	segs := strings.Split(strings.TrimPrefix(p, "/"), "/")
	for i, seg := range segs {
		if seg == "" {
			if i == len(segs)-1 && i > 0 {
				continue
			}
			return true
		}
		dec, err := url.PathUnescape(seg)
		if err != nil {
			dec = seg
		}
		if seg == "." || seg == ".." || dec == "." || dec == ".." || strings.ContainsAny(dec, "/\\\x00") {
			return true
		}
	}
	return false
}

// parseArtifactRev parses the {rev} segment: "latest" (0) or a positive
// revision number.
func parseArtifactRev(s string) (int, bool) {
	if s == "latest" {
		return 0, true
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 || strconv.Itoa(n) != s {
		return 0, false
	}
	return n, true
}

// handleArtifactRevRoot redirects /a/<id>/<rev> to /a/<id>/<rev>/.
func (s *Server) handleArtifactRevRoot(w http.ResponseWriter, r *http.Request) {
	id, rev := r.PathValue("id"), r.PathValue("rev")
	if !artifacts.ValidID(id) {
		writeArtifactError(w, http.StatusNotFound, "No such artifact.")
		return
	}
	if _, ok := parseArtifactRev(rev); !ok {
		writeArtifactError(w, http.StatusBadRequest, "Invalid revision.")
		return
	}
	http.Redirect(w, r, "/a/"+id+"/"+rev+"/", http.StatusFound)
}

// handleArtifactEntry is GET /a/{id}/{rev}/{path...} with the device cookie:
// it resolves rev ("latest" or a number) and redirects to the capability URL
// of the same file for this device, keeping the query. The file itself is
// checked when the capability URL is read.
func (s *Server) handleArtifactEntry(w http.ResponseWriter, r *http.Request) {
	id, rel := r.PathValue("id"), r.PathValue("path")
	rev, ok := parseArtifactRev(r.PathValue("rev"))
	if !ok {
		writeArtifactError(w, http.StatusBadRequest, "Invalid revision.")
		return
	}
	m, err := s.artifacts.Get(id)
	if err != nil {
		status, _, _ := artifactErr(err)
		writeArtifactError(w, status, errorText(status))
		return
	}
	if rev == 0 {
		rev = m.Latest()
	}
	if !m.HasRev(rev) {
		writeArtifactError(w, http.StatusNotFound, errorText(http.StatusNotFound))
		return
	}
	d, _ := DeviceFrom(r.Context())
	target := artifactTokenURL(s.mintArtifactToken(id, rev, d.ID), id, rev, rel)
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusFound)
}

// handleArtifactTokenRoot redirects /a/t/<token>/<id>/<rev> to its "/" form.
func (s *Server) handleArtifactTokenRoot(w http.ResponseWriter, r *http.Request) {
	tok, id, revSeg := r.PathValue("token"), r.PathValue("id"), r.PathValue("rev")
	rev, ok := parseArtifactRev(revSeg)
	if !ok || rev == 0 || !s.checkArtifactToken(tok, id, rev) {
		writeArtifactError(w, http.StatusForbidden, errorText(http.StatusForbidden))
		return
	}
	http.Redirect(w, r, artifactTokenURL(tok, id, rev, ""), http.StatusFound)
}

// handleArtifactContent is GET /a/t/{token}/{id}/{rev}/{path...}: a file of
// the revision the token grants, or (empty path) its entry. No cookie is
// needed or read. A markdown or text entry is rendered to an HTML page unless
// ?raw=1; everything else is served byte for byte with a Content-Type from its
// extension (never sniffed).
func (s *Server) handleArtifactContent(w http.ResponseWriter, r *http.Request) {
	tok, id, rel := r.PathValue("token"), r.PathValue("id"), r.PathValue("path")
	rev, ok := parseArtifactRev(r.PathValue("rev"))
	if !ok || rev == 0 || !s.checkArtifactToken(tok, id, rev) {
		writeArtifactError(w, http.StatusForbidden, errorText(http.StatusForbidden))
		return
	}
	if rel == "" {
		// An entry in a subdirectory is redirected to, so its relative URLs
		// resolve against its own directory.
		if m, err := s.artifacts.Get(id); err == nil && strings.Contains(m.Entry, "/") {
			http.Redirect(w, r, artifactTokenURL(tok, id, rev, m.Entry), http.StatusFound)
			return
		}
	}
	c, err := s.artifacts.Open(id, rev, rel)
	if err != nil {
		status, _, _ := artifactErr(err)
		writeArtifactError(w, status, errorText(status))
		return
	}
	defer c.Close() //nolint:errcheck

	if c.Rel == c.Meta.Entry && r.URL.Query().Get("raw") != "1" {
		switch c.Meta.Kind {
		case artifacts.KindMarkdown, artifacts.KindText:
			s.renderArtifact(w, c, cacheArtifactRev)
			return
		}
	}
	h := w.Header()
	h.Set("Content-Type", artifactContentType(c.Rel))
	h.Set("Cache-Control", cacheArtifactRev)
	http.ServeContent(w, r, "", c.Info.ModTime(), c.File)
}

// errorText is the message of an /a/ error page.
func errorText(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "Invalid artifact path."
	case http.StatusForbidden:
		return "This artifact link is invalid or has expired. Open the artifact again from agentctl."
	case http.StatusInternalServerError:
		return "Reading the artifact failed."
	}
	return "No such artifact or file."
}

// escapeRel path-escapes each segment of a slash path.
func escapeRel(rel string) string {
	segs := strings.Split(rel, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// artifactTypes maps extensions to Content-Types. A fixed table (not the
// host's mime database) so the answer is the same everywhere; unknown
// extensions are application/octet-stream, which a browser never renders as
// HTML (nosniff).
var artifactTypes = map[string]string{
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

// artifactContentType is the Content-Type for a file name.
func artifactContentType(name string) string {
	if t, ok := artifactTypes[strings.ToLower(path.Ext(name))]; ok {
		return t
	}
	return "application/octet-stream"
}

// artifactPage is the agentctl page that wraps a rendered artifact.
type artifactPage struct {
	Title   string
	Class   string
	Body    template.HTML
	Mermaid bool
}

var artifactPageTmpl = template.Must(template.New("artifact").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>{{.Title}}</title>
<style>` + artifactCSS + `</style>
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
  const dark = matchMedia("(prefers-color-scheme: dark)").matches;
  mermaid.initialize({ startOnLoad: false, theme: dark ? "dark" : "default" });
  await mermaid.run();
}
</script>{{end}}
</body></html>
`))

// artifactCSS styles rendered pages, light and dark.
const artifactCSS = `
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
`

// markdown renders GitHub-flavored markdown. Raw HTML passes through: the
// page is sandboxed like any HTML artifact.
var markdown = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	goldmark.WithRendererOptions(gmhtml.WithUnsafe()),
)

// renderArtifact writes a markdown or text entry as an agentctl HTML page.
func (s *Server) renderArtifact(w http.ResponseWriter, c *artifacts.Content, cache string) {
	src, err := io.ReadAll(io.LimitReader(c.File, artifacts.MaxRevBytes+1))
	if err != nil {
		logging.Warn("serve: reading artifact %s: %v", c.Meta.ID, err)
		writeArtifactError(w, http.StatusInternalServerError, "Reading the artifact failed.")
		return
	}
	page := artifactPage{Title: c.Meta.Title}
	switch {
	case c.Meta.Kind == artifacts.KindMarkdown:
		var buf bytes.Buffer
		if err := markdown.Convert(src, &buf); err != nil {
			logging.Warn("serve: rendering artifact %s: %v", c.Meta.ID, err)
			writeArtifactError(w, http.StatusInternalServerError, "Rendering the artifact failed.")
			return
		}
		page.Class = "markdown"
		page.Body = template.HTML(buf.String()) //nolint:gosec // sandboxed by artifactCSP, like an HTML artifact
		page.Mermaid = bytes.Contains(src, []byte("```mermaid")) || bytes.Contains(src, []byte("~~~mermaid"))
	case strings.EqualFold(path.Ext(c.Rel), ".csv"):
		page.Class = "csv"
		page.Body = renderCSV(src)
	default:
		page.Class = "text"
		page.Body = preBlock(src)
	}
	var out bytes.Buffer
	if err := artifactPageTmpl.Execute(&out, page); err != nil {
		logging.Warn("serve: rendering artifact page %s: %v", c.Meta.ID, err)
		writeArtifactError(w, http.StatusInternalServerError, "Rendering the artifact failed.")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", cache)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out.Bytes())
}

// preBlock is src HTML-escaped in a <pre>.
func preBlock(src []byte) template.HTML {
	return template.HTML("<pre>" + template.HTMLEscapeString(string(src)) + "</pre>") //nolint:gosec // escaped
}

// renderCSV renders CSV as a table (the first row as the header), at most
// maxCSVRows rows. Input that does not parse is shown as text.
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
		if rows == maxCSVRows {
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
		b.WriteString(`<p class="note">Showing the first ` + strconv.Itoa(maxCSVRows) + ` rows; open the raw file for the rest.</p>`)
	}
	return template.HTML(b.String()) //nolint:gosec // every cell is escaped
}

var artifactErrorTmpl = template.Must(template.New("err").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>{{.}}</title>
<style>` + artifactCSS + `</style>
</head><body><main><p class="note">{{.}}</p></main></body></html>
`))

// writeArtifactError writes a small HTML error page (it shows inside the
// viewer's iframe). The sandbox headers come from sandboxWriter.
func writeArtifactError(w http.ResponseWriter, status int, msg string) {
	var out bytes.Buffer
	_ = artifactErrorTmpl.Execute(&out, msg)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(out.Bytes())
}
