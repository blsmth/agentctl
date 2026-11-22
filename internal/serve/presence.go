package serve

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// presenceTTL is how long a "visible" report keeps a device counted as in the
// app. The web app heartbeats every ~20s while visible; iOS freezes a
// backgrounded page without a final report, so the TTL is the backstop.
const presenceTTL = 45 * time.Second

// Presence tracks which devices have the web app open in the foreground. It is
// in memory only: a serve restart treats every device as in the background.
type Presence struct {
	now func() time.Time

	mu   sync.Mutex
	seen map[string]time.Time // device ID -> last visible report
}

// NewPresence returns a tracker. A nil now means time.Now.
func NewPresence(now func() time.Time) *Presence {
	if now == nil {
		now = time.Now
	}
	return &Presence{now: now, seen: make(map[string]time.Time)}
}

// Report records a device's foreground state.
func (p *Presence) Report(deviceID string, visible bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if visible {
		p.seen[deviceID] = p.now()
	} else {
		delete(p.seen, deviceID)
	}
}

// Foreground reports whether the device's last report was visible and is
// younger than presenceTTL.
func (p *Presence) Foreground(deviceID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	t, ok := p.seen[deviceID]
	if !ok {
		return false
	}
	if p.now().Sub(t) >= presenceTTL {
		delete(p.seen, deviceID)
		return false
	}
	return true
}

// Forget drops a device (revoked).
func (p *Presence) Forget(deviceID string) {
	p.mu.Lock()
	delete(p.seen, deviceID)
	p.mu.Unlock()
}

// registerPresence registers POST /v1/presence. It is a Device route, not a
// Write route: presence changes no agent state, so it must keep working when
// serve.allow_write is off. CSRF still applies (non-GET).
func (s *Server) registerPresence(rt *Router) {
	rt.Device("POST /v1/presence", s.handlePresence)
}

func (s *Server) handlePresence(w http.ResponseWriter, r *http.Request) {
	dev, ok := DeviceFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "no device")
		return
	}
	var body struct {
		Visible *bool `json:"visible"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<10)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Visible == nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, `body must be {"visible": bool}`)
		return
	}
	s.presence.Report(dev.ID, *body.Visible)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
