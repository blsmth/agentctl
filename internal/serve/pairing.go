package serve

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base32"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/blsmth/agentctl/internal/logging"
)

// PairingWindow is how long `agentctl serve pair` keeps pairing open.
const PairingWindow = 5 * time.Minute

// pairing holds at most one outstanding one-time pairing code.
type pairing struct {
	mu      sync.Mutex
	code    string
	name    string
	expires time.Time
}

// open replaces any outstanding code with a fresh 128-bit one valid until
// now+PairingWindow.
func (p *pairing) open(name string, now time.Time) (code string, expires time.Time, err error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", time.Time{}, fmt.Errorf("generating pairing code: %w", err)
	}
	code = base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.code, p.name, p.expires = code, name, now.Add(PairingWindow)
	return code, p.expires, nil
}

// consume validates code in constant time and invalidates it, so each code
// pairs exactly one device. It returns the device name requested with
// `serve pair --name`.
func (p *pairing) consume(code string, now time.Time) (name string, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.code == "" || code == "" {
		return "", false
	}
	if !now.Before(p.expires) {
		p.code, p.name = "", ""
		return "", false
	}
	if subtle.ConstantTimeCompare([]byte(code), []byte(p.code)) != 1 {
		return "", false
	}
	name = p.name
	p.code, p.name = "", ""
	return name, true
}

// close discards any outstanding code.
func (p *pairing) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.code, p.name = "", ""
}

// active reports whether a code is outstanding and unexpired.
func (p *pairing) active(now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.code != "" && now.Before(p.expires)
}

// handlePair is GET /pair?code=…: on a valid code it creates a device, sets
// its cookie and sends the browser to "/".
//
// The hand-off to "/" is a same-origin meta refresh rather than a 3xx: the
// phone usually arrives here from the camera's QR scanner, and a
// SameSite=Strict cookie set during a navigation that began off-site is not
// sent on a redirect within that same navigation, which would land the
// freshly paired phone on the "not paired" page.
func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	now := s.now()
	name, ok := s.pairing.consume(r.URL.Query().Get("code"), now)
	if !ok {
		logging.Warn("serve: pairing rejected from %s (invalid, used or expired code)", r.RemoteAddr)
		writeHTML(w, http.StatusForbidden, pagePairFailed)
		return
	}
	dev, token, err := s.devices.Add(name, now)
	if err != nil {
		logging.Error("serve: pairing: %v", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "could not store the device")
		return
	}
	logging.Log("serve: paired device %s (%s) from %s", dev.ID, dev.Name, r.RemoteAddr)
	http.SetCookie(w, deviceCookie(token))
	writeHTML(w, http.StatusOK, pagePaired)
}

// deviceCookie builds the long-lived device cookie.
func deviceCookie(token string) *http.Cookie {
	return &http.Cookie{
		Name:     DeviceCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int((365 * 24 * time.Hour).Seconds()),
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	}
}
