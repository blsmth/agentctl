package serve

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
)

// Content types. Set explicitly rather than guessed from the host's mime
// database, which differs between platforms (notably for .webmanifest).
const (
	ctJS       = "text/javascript; charset=utf-8"
	ctCSS      = "text/css; charset=utf-8"
	ctManifest = "application/manifest+json"
	ctPNG      = "image/png"
	ctText     = "text/plain; charset=utf-8"
)

// Cache-Control values for static files. Shell assets revalidate on every
// load (cheap, thanks to ETag) so `just install` upgrades show up at once;
// pinned vendor files and icons may be reused for a day.
const (
	cacheRevalidate = "private, no-cache"
	cacheDay        = "private, max-age=86400"
)

// cspHTML is the Content-Security-Policy on every HTML page. Scripts must come
// from this origin: no inline scripts, no event-handler attributes, no eval.
// Styles allow inline because xterm.js injects <style> elements and the
// pairing pages inline their stylesheet.
const cspHTML = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; connect-src 'self'; manifest-src 'self'; worker-src 'self'; " +
	"base-uri 'none'; form-action 'self'; frame-ancestors 'none'; object-src 'none'"

// staticAsset is one embedded file the server exposes.
type staticAsset struct {
	URL    string // request path
	File   string // path under web/
	Type   string
	Cache  string
	Public bool // reachable without a device cookie
}

// staticAssets is the allowlist of served files. Anything else under web/
// (README, licenses, the pairing pages) is never served by path.
//
// Public is limited to what a browser fetches without credentials: the
// manifest and its icons (manifest and icon requests omit cookies). The app
// shell and service worker need a paired device; an unpaired phone gets the
// self-contained "not paired" page instead.
var staticAssets = []staticAsset{
	{"/manifest.webmanifest", "manifest.webmanifest", ctManifest, cacheRevalidate, true},
	{"/icons/icon-192.png", "icons/icon-192.png", ctPNG, cacheDay, true},
	{"/icons/icon-512.png", "icons/icon-512.png", ctPNG, cacheDay, true},
	{"/sw.js", "sw.js", ctJS, "no-cache", false},
	{"/app.css", "app.css", ctCSS, cacheRevalidate, false},
	{"/app.js", "app.js", ctJS, cacheRevalidate, false},
	{"/api.js", "api.js", ctJS, cacheRevalidate, false},
	{"/live.js", "live.js", ctJS, cacheRevalidate, false},
	{"/logic.js", "logic.js", ctJS, cacheRevalidate, false},
	{"/ui.js", "ui.js", ctJS, cacheRevalidate, false},
	{"/fleet.js", "fleet.js", ctJS, cacheRevalidate, false},
	{"/window.js", "window.js", ctJS, cacheRevalidate, false},
	{"/terminal.js", "terminal.js", ctJS, cacheRevalidate, false},
	{"/dispatch.js", "dispatch.js", ctJS, cacheRevalidate, false},
	{"/settings.js", "settings.js", ctJS, cacheRevalidate, false},
	{"/vendor/xterm.js", "vendor/xterm.js", ctJS, cacheDay, false},
	{"/vendor/xterm.css", "vendor/xterm.css", ctCSS, cacheDay, false},
	{"/vendor/addon-fit.js", "vendor/addon-fit.js", ctJS, cacheDay, false},
}

// registerStatic registers every entry of staticAssets on rt.
func (s *Server) registerStatic(rt *Router) {
	for _, a := range staticAssets {
		h := staticHandler(a)
		if a.Public {
			rt.Public("GET "+a.URL, h)
		} else {
			rt.Device("GET "+a.URL, h)
		}
	}
}

// staticHandler serves one embedded file with its content type, cache policy
// and a content-hash ETag (embed.FS files carry no modification time).
func staticHandler(a staticAsset) http.HandlerFunc {
	body, err := webFS.ReadFile("web/" + a.File)
	if err != nil {
		panic(err) // embedded at build time; a missing file is a build bug
	}
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	return func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Type", a.Type)
		h.Set("Cache-Control", a.Cache)
		h.Set("ETag", etag)
		if a.URL == "/sw.js" {
			h.Set("Service-Worker-Allowed", "/")
		}
		if etagMatches(r.Header.Get("If-None-Match"), etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		h.Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	}
}

func etagMatches(header, etag string) bool {
	for _, v := range strings.Split(header, ",") {
		if strings.TrimPrefix(strings.TrimSpace(v), "W/") == etag {
			return true
		}
	}
	return false
}
