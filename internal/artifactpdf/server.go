package artifactpdf

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/artifactpage"
	"github.com/blsmth/agentctl/internal/artifacts"
)

// source serves one revision of one artifact over loopback HTTP, the way
// `agentctl serve` does, so the browser loads relative assets and CDN scripts
// as a viewer would. Every request must carry the random path prefix and the
// loopback Host, so another local process cannot read the artifact by probing
// the port and a rebinding page cannot reach it.
type source struct {
	store *artifacts.Store
	id    string
	rev   int
	// prefix is "/<token>/".
	prefix string
	host   string
}

// handler serves the revision's files under prefix.
func (s *source) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.Host != s.host || !strings.HasPrefix(r.URL.Path, s.prefix) {
			writeError(w, http.StatusNotFound)
			return
		}
		s.serve(w, r, strings.TrimPrefix(r.URL.Path, s.prefix))
	})
}

// setHeaders applies the headers serve puts on every /a/ response: the
// sandbox CSP, and CORP/CORS so the sandboxed (opaque-origin) page can load
// its own stylesheets, images and modules.
func setHeaders(h http.Header) {
	h.Set("Content-Security-Policy", artifactpage.CSP)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cross-Origin-Resource-Policy", "cross-origin")
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Cache-Control", "no-store")
}

func (s *source) serve(w http.ResponseWriter, r *http.Request, rel string) {
	c, err := s.store.Open(s.id, s.rev, rel)
	if err != nil {
		status := http.StatusNotFound
		if errors.Is(err, artifacts.ErrBadPath) {
			status = http.StatusBadRequest
		}
		writeError(w, status)
		return
	}
	defer c.Close() //nolint:errcheck

	h := w.Header()
	setHeaders(h)
	if c.Rel == c.Meta.Entry && r.URL.Query().Get("raw") != "1" {
		page, ok, err := entryPage(c)
		if err != nil {
			writeError(w, http.StatusInternalServerError)
			return
		}
		if ok {
			h.Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(page)
			return
		}
	}
	h.Set("Content-Type", artifactpage.ContentType(c.Rel))
	http.ServeContent(w, r, "", c.Info.ModTime(), c.File)
}

// entryPage renders the entry of a markdown, text or image artifact as the
// agentctl page serve shows; ok is false for kinds served as-is.
func entryPage(c *artifacts.Content) (page []byte, ok bool, err error) {
	switch {
	case artifactpage.Rendered(c.Meta.Kind):
		src, err := io.ReadAll(io.LimitReader(c.File, artifacts.MaxRevBytes+1))
		if err != nil {
			return nil, false, fmt.Errorf("reading entry: %w", err)
		}
		page, err = artifactpage.Render(c.Meta.Kind, c.Rel, c.Meta.Title, src)
		return page, err == nil, err
	case c.Meta.Kind == artifacts.KindImage:
		page, err = artifactpage.ImagePage(c.Meta.Title, c.Rel)
		return page, err == nil, err
	}
	return nil, false, nil
}

func writeError(w http.ResponseWriter, status int) {
	setHeaders(w.Header())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(artifactpage.ErrorPage(http.StatusText(status)))
}

// serveSource starts the loopback server for one revision and returns the URL
// of its entry and a stop function. The port is OS-assigned, so concurrent
// exports never collide.
func serveSource(store *artifacts.Store, m artifacts.Meta, rev int) (entryURL string, stop func(), err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, fmt.Errorf("starting local server: %w", err)
	}
	tok := make([]byte, 16)
	if _, err := rand.Read(tok); err != nil {
		_ = ln.Close()
		return "", nil, fmt.Errorf("generating token: %w", err)
	}
	host := ln.Addr().String()
	s := &source{store: store, id: m.ID, rev: rev, prefix: "/" + hex.EncodeToString(tok) + "/", host: host}
	srv := &http.Server{Handler: s.handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	// print=1 tells the kit's charts to draw without animation (the browser
	// prints before an animation would finish).
	u := url.URL{Scheme: "http", Host: host, Path: s.prefix + m.Entry, RawQuery: "print=1"}
	return u.String(), func() { _ = srv.Close() }, nil
}
