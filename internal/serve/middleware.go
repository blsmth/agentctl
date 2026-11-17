package serve

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// DeviceCookie is the cookie that carries a paired device's token.
const DeviceCookie = "agentctl_device"

// CSRFHeader must be "1" on every non-GET request. Combined with the
// SameSite=Strict cookie it keeps a page on another origin from driving
// write endpoints.
const CSRFHeader = "X-Agentctl-CSRF"

// cidrFilter is the source-address allowlist.
type cidrFilter []netip.Prefix

// parseCIDRs parses allow_cidrs entries. A bare address is accepted as a
// single-host prefix.
func parseCIDRs(cidrs []string) (cidrFilter, error) {
	out := make(cidrFilter, 0, len(cidrs))
	for _, c := range cidrs {
		c = strings.TrimSpace(c)
		if p, err := netip.ParsePrefix(c); err == nil {
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(c)
		if err != nil {
			return nil, fmt.Errorf("serve.allow_cidrs: invalid entry %q", c)
		}
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

// allows reports whether the connection's remote address (http.Request
// RemoteAddr, "ip:port") is inside the allowlist. Proxy headers such as
// X-Forwarded-For are deliberately never consulted: anyone can send them.
func (f cidrFilter) allows(remoteAddr string) bool {
	host := remoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = h
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	addr = addr.WithZone("").Unmap()
	for _, p := range f {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// middleware wraps the CIDR filter around next. It runs first, before any
// other check, /healthz included.
func (f cidrFilter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !f.allows(r.RemoteAddr) {
			writeError(w, http.StatusForbidden, codeForbiddenSource, "source address not allowed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// securityHeaders sets conservative defaults. Handlers may override them
// (e.g. Cache-Control for static assets).
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// requireCSRF rejects every state-changing method without CSRFHeader: 1.
func requireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if r.Header.Get(CSRFHeader) != "1" {
				writeError(w, http.StatusForbidden, codeCSRF, "missing "+CSRFHeader+" header")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// deviceAuthenticator resolves a device token to a paired device.
// *DeviceStore implements it; tests may substitute a fake.
type deviceAuthenticator interface {
	Authenticate(token string, now time.Time) (Device, bool)
}

type deviceKey struct{}

// DeviceFrom returns the authenticated device for a request that passed a
// Device or Write route.
func DeviceFrom(ctx context.Context) (Device, bool) {
	d, ok := ctx.Value(deviceKey{}).(Device)
	return d, ok
}

// requireDevice rejects requests without a valid device cookie: JSON 401 for
// API clients, the "not paired" page for browsers.
func requireDevice(auth deviceAuthenticator, now func() time.Time, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(DeviceCookie)
		if err == nil && c.Value != "" {
			if d, ok := auth.Authenticate(c.Value, now()); ok {
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), deviceKey{}, d)))
				return
			}
		}
		if wantsHTML(r) {
			writeHTML(w, http.StatusUnauthorized, pageNotPaired)
			return
		}
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "device not paired: run `agentctl serve pair`")
	})
}

// requireWrite returns 403 unless serve.allow_write is on.
func requireWrite(allowed bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowed {
			writeError(w, http.StatusForbidden, codeWriteDisabled, "write actions are disabled (serve.allow_write is false)")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// wantsHTML reports whether an unauthenticated request should get the HTML
// "not paired" page rather than JSON: a GET outside /v1/ that accepts HTML.
func wantsHTML(r *http.Request) bool {
	if r.Method != http.MethodGet || strings.HasPrefix(r.URL.Path, "/v1/") {
		return false
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}
