package serve

import (
	"context"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/servectl"
)

// Phone access switch. `agentctl serve stop|toggle` (internal/servectl)
// writes <serve dir>/phone-off instead of stopping the process; while it
// exists the server:
//
//   - sends no push notifications (Sender and the ntfy relay are muted),
//   - refuses remote approvals (the hook's Guard checks the file first, and
//     ServeApproval refuses and ends open ones as a backstop),
//   - answers every Device and Write route with 503 phone_off (the app shell
//     gets the "phone access is off" page), ends open SSE streams and live
//     terminals, and refuses pairing,
//
// and keeps serving artifacts: /a/<capability id>/..., the token routes and
// the legacy-id entry (which checks the device cookie itself) are Public
// routes and never consult the switch.
//
// Requests stat the file (no caching, so a toggle is seen at once); a poll
// in Start notices the switch for the long-lived streams.

// phonePollEvery is how often the server re-reads the switch to end streams.
const phonePollEvery = time.Second

// phoneOffMessage is the 503 body's message.
const phoneOffMessage = "phone access is off on the laptop: run `agentctl serve start` there to turn it back on"

// phoneSwitch reads servectl's phone-off file.
type phoneSwitch struct {
	path string

	mu  sync.Mutex
	off bool
	// offC is closed while phone access is off and replaced when it comes
	// back on, so a stream selects on it to end when the phone turns off.
	offC chan struct{}
}

func newPhoneSwitch(dir string) *phoneSwitch {
	p := &phoneSwitch{path: filepath.Join(dir, servectl.PhoneOffFile), offC: make(chan struct{})}
	p.Off()
	return p
}

// Off reports whether phone access is off, reading the file now, and
// records a change.
func (p *phoneSwitch) Off() bool {
	if p == nil {
		return false
	}
	off := servectl.PhoneOff(filepath.Dir(p.path))
	p.mu.Lock()
	defer p.mu.Unlock()
	if off != p.off {
		p.off = off
		if off {
			close(p.offC)
			logging.Log("serve: phone access off (push muted, phone app disabled; artifacts still served)")
		} else {
			p.offC = make(chan struct{})
			logging.Log("serve: phone access on")
		}
	}
	return off
}

// turnedOff returns a channel that is closed once phone access is off
// (already closed while it is). A nil switch never turns off.
func (p *phoneSwitch) turnedOff() <-chan struct{} {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.offC
}

// run re-reads the switch every interval until ctx ends.
func (p *phoneSwitch) run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.Off()
		}
	}
}

// requirePhone answers 503 phone_off while off reports true: the "phone
// access is off" page for a browser navigation, JSON otherwise. A nil off
// never refuses.
func requirePhone(off func() bool, next http.Handler) http.Handler {
	if off == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if off() {
			writePhoneOff(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// writePhoneOff writes the phone-off answer.
func writePhoneOff(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Retry-After", "60")
	if wantsHTML(r) {
		writeHTML(w, http.StatusServiceUnavailable, pagePhoneOff)
		return
	}
	writeError(w, http.StatusServiceUnavailable, codePhoneOff, phoneOffMessage)
}
