package serve

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/blsmth/agentctl/internal/artifactpdf"
	"github.com/blsmth/agentctl/internal/artifacts"
	"github.com/blsmth/agentctl/internal/logging"
)

const (
	codeNoBrowser    = "no_browser"
	codeRenderFailed = "render_failed"
)

// pdfWriteDeadline is the write deadline of the PDF route, past the server's
// 30s WriteTimeout: a request may wait pdfQueueWait for the render slot and
// then up to artifactpdf.Timeout for the browser, plus a margin to send the
// file. Without it a render slower than ~30s would finish (and be cached)
// while the phone saw a network error.
const pdfWriteDeadline = pdfQueueWait + artifactpdf.Timeout + 15*time.Second

// pdfDeadline extends the write deadline for the PDF route.
func pdfDeadline(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(pdfWriteDeadline)); err != nil {
			logging.Debug("serve: extending pdf write deadline: %v", err)
		}
		h(w, r)
	}
}

// handleArtifactPDF is GET /v1/artifacts/{id}/pdf?rev=N (rev absent or
// "latest" = the latest revision): the revision printed to a PDF by
// artifactpdf.Render, as an attachment. The artifact and revision are
// checked on every request, before the cache, so a deleted artifact is a
// 404 even while its PDF is still cached.
func (s *Server) handleArtifactPDF(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !artifacts.ValidID(id) {
		status, code, msg := artifactErr(artifacts.ErrNotFound)
		writeError(w, status, code, msg)
		return
	}
	rev := 0
	if v := r.URL.Query().Get("rev"); v != "" {
		n, ok := parseArtifactRev(v)
		if !ok {
			writeError(w, http.StatusBadRequest, codeBadRequest, "rev must be a revision number or latest")
			return
		}
		rev = n
	}
	m, err := s.artifacts.Get(id)
	if err != nil {
		status, code, msg := artifactErr(err)
		writeError(w, status, code, msg)
		return
	}
	if rev == 0 {
		rev = m.Latest()
	}
	if rev == 0 || !m.HasRev(rev) {
		status, code, msg := artifactErr(artifacts.ErrNotFound)
		writeError(w, status, code, msg)
		return
	}

	pdf, err := s.pdfs.get(r.Context(), pdfKey{id: id, rev: rev})
	if err != nil {
		s.writePDFError(w, r, id, rev, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/pdf")
	h.Set("Content-Disposition", pdfDisposition(m, rev))
	h.Set("Content-Length", strconv.Itoa(len(pdf)))
	h.Set("Cache-Control", "private")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pdf)
}

// writePDFError maps a render error to a JSON error. The log names the
// artifact by ShortID only: its id is a viewing credential.
func (s *Server) writePDFError(w http.ResponseWriter, r *http.Request, id string, rev int, err error) {
	short := artifacts.ShortID(id)
	switch {
	case r.Context().Err() != nil:
		// The client left; nobody reads the answer.
		return
	case errors.Is(err, errPDFBusy):
		w.Header().Set("Retry-After", pdfRetryAfter)
		writeError(w, http.StatusServiceUnavailable, codeBusy, "another PDF is rendering; try again shortly")
	case errors.Is(err, artifactpdf.ErrNoBrowser):
		writeError(w, http.StatusNotImplemented, codeNoBrowser,
			"no Chrome, Chromium or Edge on the server: install Google Chrome or set "+artifactpdf.ConfigKey+" in the global config")
	case errors.Is(err, artifactpdf.ErrBadBrowserConfig):
		writeError(w, http.StatusNotImplemented, codeNoBrowser,
			artifactpdf.ConfigKey+" in the global config is not an executable file")
	case errors.Is(err, artifacts.ErrNotFound):
		status, code, msg := artifactErr(err)
		writeError(w, status, code, msg)
	case errors.Is(err, context.DeadlineExceeded):
		logging.Warn("serve: pdf of artifact %s rev %d: %s", short, rev, redactArtifactIDs(err.Error()))
		writeError(w, http.StatusBadGateway, codeRenderFailed, "rendering the PDF timed out")
	default:
		logging.Warn("serve: pdf of artifact %s rev %d: %s", short, rev, redactArtifactIDs(err.Error()))
		writeError(w, http.StatusBadGateway, codeRenderFailed, "rendering the PDF failed")
	}
}

// pdfDisposition is the attachment header for revision rev of m:
// "<slug>-r<N>.pdf". The plain filename is ASCII only; when the name has
// other characters (an artifact whose title has no ASCII letters has no
// slug, so its title names the file) filename* carries the UTF-8 name
// (RFC 6266, RFC 8187) and filename falls back to the id prefix.
func pdfDisposition(m artifacts.Meta, rev int) string {
	suffix := fmt.Sprintf("-r%d.pdf", rev)
	fallback := m.ID
	if len(fallback) > 6 {
		fallback = fallback[:6]
	}
	ascii := asciiFilename(m.Slug)
	if ascii == "" {
		ascii = asciiFilename(fallback)
	}
	v := `attachment; filename="` + ascii + suffix + `"`
	if m.Slug == "" {
		if name := unicodeFilename(m.Title); name != "" && name != ascii {
			v += "; filename*=UTF-8''" + url.PathEscape(name+suffix)
		}
	}
	return v
}

// asciiFilename keeps letters, digits, '.', '_' and '-' of s (others become
// one '-'), trimmed of dots and dashes and cut to 60 bytes: safe inside a
// quoted filename on every platform.
func asciiFilename(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range s {
		ok := r < utf8.RuneSelf && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-')
		switch {
		case ok:
			b.WriteRune(r)
			dash = r == '-'
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
		if b.Len() >= 60 {
			break
		}
	}
	return strings.Trim(b.String(), ".-")
}

// unicodeFilename is s with anything unsafe in a file name (controls, path
// separators, quotes, reserved Windows characters) replaced by '-', spaces
// collapsed, cut to 60 runes.
func unicodeFilename(s string) string {
	var b strings.Builder
	n, dash := 0, false
	for _, r := range s {
		ok := unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) || r == '.' || r == '_' || r == '-'
		switch {
		case ok:
			b.WriteRune(r)
			dash = r == '-'
			n++
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
			n++
		}
		if n >= 60 {
			break
		}
	}
	return strings.Trim(b.String(), ".-")
}
