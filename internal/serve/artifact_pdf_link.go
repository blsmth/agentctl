package serve

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sync"
	"time"

	"github.com/blsmth/agentctl/internal/artifacts"
)

// PDF links: a short-lived URL that serves one revision's PDF with no
// cookie, for the viewer's "Open PDF". An iOS home-screen app does not share
// Safari's cookie jar, so the browser that shows the PDF (the in-app Safari
// sheet, with its own Save to Files and Print) cannot use the Device route.
//
// POST /v1/artifacts/{id}/pdf-link?rev=N (Write, audited) mints a token for
// that id, the resolved revision number and the requesting device. The
// token is 32 random bytes, kept in memory only (as its SHA-256), valid for
// pdfLinkTTL, and grants nothing but GET /pdf/{token}/{name}: the PDF,
// rendered through the same exporter (one render at a time, shared, cached)
// as the Device route. The GET checks the token before it touches the store
// or the renderer, so without one it can neither find artifacts nor start a
// render. A link survives no restart.

const (
	// pdfLinkTTL is how long a PDF link works.
	pdfLinkTTL = 5 * time.Minute
	// pdfLinkMax bounds the live links; the one closest to expiry goes
	// first when a new one would exceed it.
	pdfLinkMax = 64
	// pdfLinkTokenBytes is the token's random length (256 bits).
	pdfLinkTokenBytes = 32
)

// pdfLinkTokenRe is a token as minted: 32 bytes, unpadded base64url.
var pdfLinkTokenRe = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// pdfLink is what one token grants.
type pdfLink struct {
	id     string
	rev    int
	device string
	exp    time.Time
}

// pdfLinks holds the live links keyed by the SHA-256 of their token.
type pdfLinks struct {
	mu sync.Mutex
	m  map[[sha256.Size]byte]pdfLink
}

func newPDFLinks() *pdfLinks {
	return &pdfLinks{m: map[[sha256.Size]byte]pdfLink{}}
}

// mint returns a new token for l (l.exp is set from now).
func (p *pdfLinks) mint(l pdfLink, now time.Time) (string, time.Time, error) {
	raw := make([]byte, pdfLinkTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, fmt.Errorf("generating pdf link token: %w", err)
	}
	tok := base64.RawURLEncoding.EncodeToString(raw)
	l.exp = now.Add(pdfLinkTTL)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneLocked(now)
	for len(p.m) >= pdfLinkMax {
		p.dropSoonestLocked()
	}
	p.m[sha256.Sum256([]byte(tok))] = l
	return tok, l.exp, nil
}

// lookup returns the link tok grants now. A malformed token never reaches
// the map.
func (p *pdfLinks) lookup(tok string, now time.Time) (pdfLink, bool) {
	if !pdfLinkTokenRe.MatchString(tok) {
		return pdfLink{}, false
	}
	k := sha256.Sum256([]byte(tok))
	p.mu.Lock()
	defer p.mu.Unlock()
	l, ok := p.m[k]
	if !ok {
		return pdfLink{}, false
	}
	if !now.Before(l.exp) {
		delete(p.m, k)
		return pdfLink{}, false
	}
	return l, true
}

// forget drops every link to id (on delete).
func (p *pdfLinks) forget(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, l := range p.m {
		if l.id == id {
			delete(p.m, k)
		}
	}
}

func (p *pdfLinks) pruneLocked(now time.Time) {
	for k, l := range p.m {
		if !now.Before(l.exp) {
			delete(p.m, k)
		}
	}
}

func (p *pdfLinks) dropSoonestLocked() {
	var (
		soonest [sha256.Size]byte
		at      time.Time
		found   bool
	)
	for k, l := range p.m {
		if !found || l.exp.Before(at) {
			soonest, at, found = k, l.exp, true
		}
	}
	if found {
		delete(p.m, soonest)
	}
}

// artifactPDFLinkIn is the body of POST /v1/artifacts/{id}/pdf-link: {}.
type artifactPDFLinkIn struct{}

// artifactPDFLinkOut is its answer: the link (a relative URL) and when it
// stops working.
type artifactPDFLinkOut struct {
	URL     string `json:"url"`
	Expires string `json:"expires"`
}

// pdfLinkURL is the URL of a PDF link. name is only for the browser (the
// file name its PDF viewer shows and saves under); the token alone decides.
func pdfLinkURL(tok, name string) string {
	return "/pdf/" + tok + "/" + url.PathEscape(name)
}

// handleArtifactPDFLink is POST /v1/artifacts/{id}/pdf-link?rev=N (rev
// absent or "latest" = the latest revision, resolved to its number now).
func (s *Server) handleArtifactPDFLink(r *http.Request, _ artifactPDFLinkIn, a *auditEntry) outcome {
	id := r.PathValue("id")
	a.Artifact = artifacts.ShortID(id)
	if !artifacts.ValidID(id) {
		return failure(artifactErr(artifacts.ErrNotFound))
	}
	rev := 0
	if v := r.URL.Query().Get("rev"); v != "" {
		n, ok := parseArtifactRev(v)
		if !ok {
			return failure(http.StatusBadRequest, codeBadRequest, "rev must be a revision number or latest")
		}
		rev = n
	}
	m, err := s.artifacts.Get(id)
	if err != nil {
		return failure(artifactErr(err))
	}
	a.Project, a.Agent = clip(m.Project), clip(m.Agent)
	if rev == 0 {
		rev = m.Latest()
	}
	if rev == 0 || !m.HasRev(rev) {
		return failure(artifactErr(artifacts.ErrNotFound))
	}
	d, _ := DeviceFrom(r.Context())
	now := s.now()
	tok, exp, err := s.pdfLinks.mint(pdfLink{id: id, rev: rev, device: d.ID}, now)
	if err != nil {
		return failure(http.StatusInternalServerError, codeInternal, "creating the PDF link failed")
	}
	return success(artifactPDFLinkOut{
		URL:     pdfLinkURL(tok, pdfASCIIName(m, rev)),
		Expires: exp.UTC().Format(time.RFC3339),
	})
}

// handlePDFLink is GET /pdf/{token}/{name}, a Public route: the PDF the
// token grants, shown inline. Order matters: the token first (no store read
// and no render without a live one), then its device (revoked = dead
// link), then the artifact (deleted = 404 even while the PDF is cached),
// then the render. Every miss is the /a/ 404 page.
func (s *Server) handlePDFLink(w http.ResponseWriter, r *http.Request) {
	l, ok := s.pdfLinks.lookup(r.PathValue("token"), s.now())
	if !ok || !s.devicePaired(l.device) {
		writePDFLinkPage(w, http.StatusNotFound, errorText(http.StatusNotFound))
		return
	}
	if s.phone.Off() {
		writePDFLinkPage(w, http.StatusServiceUnavailable, "Phone access is off on the laptop.")
		return
	}
	m, err := s.artifacts.Get(l.id)
	if err != nil || !m.HasRev(l.rev) {
		status := http.StatusNotFound
		if err != nil && !errors.Is(err, artifacts.ErrNotFound) {
			status, _, _ = artifactErr(err)
		}
		writePDFLinkPage(w, status, errorText(status))
		return
	}
	pdf, err := s.pdfs.get(r.Context(), pdfKey{id: l.id, rev: l.rev})
	if err != nil {
		status, _, msg, ok := s.pdfFailure(r, l.id, l.rev, err)
		if !ok {
			return
		}
		if status == http.StatusServiceUnavailable {
			w.Header().Set("Retry-After", pdfRetryAfter)
		}
		writePDFLinkPage(w, status, pdfPageText(status, msg))
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/pdf")
	h.Set("Content-Disposition", pdfDispositionAs("inline", m, l.rev))
	h.Set("Cache-Control", "private, no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	// ServeContent sets Content-Length and answers HEAD and Range requests
	// (a PDF viewer may load the file in pieces) from the bytes in hand.
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(pdf))
}

// handlePDFLinkNotFound is any other /pdf/ path: the same 404 page.
func (s *Server) handlePDFLinkNotFound(w http.ResponseWriter, _ *http.Request) {
	writePDFLinkPage(w, http.StatusNotFound, errorText(http.StatusNotFound))
}

// writePDFLinkPage is the /a/ error page under the artifact sandbox CSP
// (the page is outside /a/, so sandboxArtifacts does not add it).
func writePDFLinkPage(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Security-Policy", artifactCSP)
	writeArtifactError(w, status, msg)
}

// pdfPageText turns a render error message into a sentence for the page.
func pdfPageText(status int, msg string) string {
	switch status {
	case http.StatusServiceUnavailable:
		return "Another PDF is rendering. Try again shortly."
	case http.StatusNotFound:
		return errorText(status)
	}
	if msg == "" {
		return "Rendering the PDF failed."
	}
	return string(bytes.ToUpper([]byte(msg[:1]))) + msg[1:] + "."
}
