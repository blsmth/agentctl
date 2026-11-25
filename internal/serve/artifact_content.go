package serve

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/artifactpage"
	"github.com/blsmth/agentctl/internal/artifacts"
	"github.com/blsmth/agentctl/internal/logging"
)

// Artifact content is served under /a/ on the app's own origin, in a
// sandbox: artifactCSP has no allow-same-origin, so the document runs in an
// opaque origin and its script cannot read any /v1 response or send the CSRF
// header. Two ways in:
//
//   - An artifact with a capability id (artifacts.CapabilityID, 130 random
//     bits) is served straight from /a/<id>/<rev>/<path> with no cookie: the
//     id is the credential, so anyone on an allowed source address who has
//     the link can view it, in any browser, unpaired. Relative URLs keep the
//     id in the base path, so the document's own assets load too.
//   - A legacy 10-character id is guessable, so it still needs the device
//     cookie, which only authenticates the entry request: it redirects to the
//     capability URL /a/t/<token>/<id>/<n>/<path> (artifact_token.go), and
//     everything the document then loads by relative URL carries the token.
//     Without a cookie it is the same 404 as an unknown id.
//
// The source list is deliberately open (CDN scripts, styles, fonts): the
// protection is the opaque origin.
const artifactCSP = artifactpage.CSP

// cacheArtifactRev is the Cache-Control of content at a capability URL: a
// numbered revision never changes. The cookie redirects are no-store.
const cacheArtifactRev = "private, max-age=31536000, immutable"

// cacheArtifactLatest is the Cache-Control of /a/<id>/latest/ content, which
// changes with every update: revalidate each time (by ETag).
const cacheArtifactLatest = "private, no-cache"

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
		sw := &sandboxWriter{ResponseWriter: w, cors: isArtifactCapabilityPath(r.URL.Path)}
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

// artifactAccess decides who may read /a/<id>/...: a capability id is open
// to anyone (ok, no device); a legacy id needs a paired device's cookie.
// Anything else, a legacy id without the cookie included, gets the same 404
// page as an id that does not exist, so a miss tells nothing.
func (s *Server) artifactAccess(w http.ResponseWriter, r *http.Request, id string) (Device, bool) {
	if artifacts.CapabilityID(id) {
		return Device{}, true
	}
	if artifacts.ValidID(id) {
		if d, ok := deviceFromCookie(s.router.auth, s.now, r); ok {
			return d, true
		}
	}
	writeArtifactError(w, http.StatusNotFound, errorText(http.StatusNotFound))
	return Device{}, false
}

// handleArtifactRoot redirects /a/<id> to /a/<id>/latest/ when the caller
// may read it (artifactAccess).
func (s *Server) handleArtifactRoot(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.artifactAccess(w, r, id); !ok {
		return
	}
	http.Redirect(w, r, "/a/"+id+"/latest/", http.StatusFound)
}

// handleArtifactNotFound is /a and /a/: the same 404 page as an unknown id,
// cookie or not.
func (s *Server) handleArtifactNotFound(w http.ResponseWriter, _ *http.Request) {
	writeArtifactError(w, http.StatusNotFound, errorText(http.StatusNotFound))
}

// handleArtifactRevRoot redirects /a/<id>/<rev> to /a/<id>/<rev>/.
func (s *Server) handleArtifactRevRoot(w http.ResponseWriter, r *http.Request) {
	id, rev := r.PathValue("id"), r.PathValue("rev")
	if _, ok := s.artifactAccess(w, r, id); !ok {
		return
	}
	if _, ok := parseArtifactRev(rev); !ok {
		writeArtifactError(w, http.StatusBadRequest, "Invalid revision.")
		return
	}
	http.Redirect(w, r, "/a/"+id+"/"+rev+"/", http.StatusFound)
}

// handleArtifactEntry is GET /a/{id}/{rev}/{path...}, a Public route. A
// capability id is served right here, rev "latest" or a number, no cookie
// (subresources authenticate by the id in their path). A legacy id needs the
// device cookie: rev is resolved and the request redirected to the token URL
// of the same file for this device, keeping the query; the file itself is
// checked when the token URL is read.
func (s *Server) handleArtifactEntry(w http.ResponseWriter, r *http.Request) {
	id, rel, revSeg := r.PathValue("id"), r.PathValue("path"), r.PathValue("rev")
	d, ok := s.artifactAccess(w, r, id)
	if !ok {
		return
	}
	rev, ok := parseArtifactRev(revSeg)
	if !ok {
		writeArtifactError(w, http.StatusBadRequest, "Invalid revision.")
		return
	}
	if artifacts.CapabilityID(id) {
		cache := cacheArtifactRev
		if rev == 0 {
			cache = cacheArtifactLatest
		}
		s.serveArtifactFile(w, r, id, rev, rel, cache, func(rel string) string {
			return artifactDirectURL(id, revSeg, rel)
		})
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
	s.serveArtifactFile(w, r, id, rev, rel, cacheArtifactRev, func(rel string) string {
		return artifactTokenURL(tok, id, rev, rel)
	})
}

// serveArtifactFile writes file rel of revision rev (0 = latest) of id, once
// the caller has decided the request may read it. urlOf is the URL of a file
// of the same revision under the route that was used, for the redirect to an
// entry in a subdirectory. A markdown or text entry is rendered to an HTML
// page unless ?raw=1; everything else is served byte for byte with a
// Content-Type from its extension (never sniffed).
func (s *Server) serveArtifactFile(w http.ResponseWriter, r *http.Request, id string, rev int, rel, cache string, urlOf func(rel string) string) {
	if rel == "" {
		// An entry in a subdirectory is redirected to, so its relative URLs
		// resolve against its own directory.
		if m, err := s.artifacts.Get(id); err == nil && strings.Contains(m.Entry, "/") {
			target := urlOf(m.Entry)
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusFound)
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

	modTime := c.Info.ModTime()
	if rev == 0 {
		// latest changes under the same URL, and a new revision's file may
		// carry an older mtime: validate by revision, not by time.
		w.Header().Set("ETag", fmt.Sprintf(`"r%d-%d-%d"`, c.Rev, c.Info.Size(), modTime.UnixNano()))
		modTime = time.Time{}
	}
	if c.Rel == c.Meta.Entry && r.URL.Query().Get("raw") != "1" {
		switch c.Meta.Kind {
		case artifacts.KindMarkdown, artifacts.KindText:
			s.renderArtifact(w, c, cache)
			return
		}
	}
	h := w.Header()
	h.Set("Content-Type", artifactpage.ContentType(c.Rel))
	h.Set("Cache-Control", cache)
	http.ServeContent(w, r, "", modTime, c.File)
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

// renderArtifact writes a markdown or text entry as an agentctl HTML page.
func (s *Server) renderArtifact(w http.ResponseWriter, c *artifacts.Content, cache string) {
	src, err := io.ReadAll(io.LimitReader(c.File, artifacts.MaxRevBytes+1))
	if err != nil {
		logging.Warn("serve: reading artifact %s: %s", artifacts.ShortID(c.Meta.ID), redactArtifactIDs(err.Error()))
		writeArtifactError(w, http.StatusInternalServerError, "Reading the artifact failed.")
		return
	}
	out, err := artifactpage.Render(c.Meta.Kind, c.Rel, c.Meta.Title, src)
	if err != nil {
		logging.Warn("serve: rendering artifact %s: %v", artifacts.ShortID(c.Meta.ID), err)
		writeArtifactError(w, http.StatusInternalServerError, "Rendering the artifact failed.")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", cache)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}

// writeArtifactError writes a small HTML error page (it shows inside the
// viewer's iframe). The sandbox headers come from sandboxWriter.
func writeArtifactError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(artifactpage.ErrorPage(msg))
}

// artifactDirectURL is the no-cookie URL of a file of a capability-id
// artifact: /a/<id>/<rev>/<path>, rev "latest" or a number.
func artifactDirectURL(id, rev, rel string) string {
	return "/a/" + id + "/" + rev + "/" + escapeRel(rel)
}

// isArtifactCapabilityPath reports a path whose credential is in the path, a
// token URL or a capability id's direct URL (CORS-readable, see
// setArtifactHeaders). A legacy id's path only redirects, so it is not one.
func isArtifactCapabilityPath(p string) bool {
	if isArtifactTokenPath(p) {
		return true
	}
	seg, _, _ := strings.Cut(strings.TrimPrefix(p, "/a/"), "/")
	return strings.HasPrefix(p, "/a/") && artifacts.CapabilityID(seg)
}

// capabilityIDRe finds capability ids in text bound for the log.
var capabilityIDRe = regexp.MustCompile(`\b[a-z2-7]{26}\b`)

// redactArtifactIDs shortens every capability id in s to its ShortID: an id
// is a viewing credential and must not reach the server log.
func redactArtifactIDs(s string) string {
	return capabilityIDRe.ReplaceAllStringFunc(s, artifacts.ShortID)
}
