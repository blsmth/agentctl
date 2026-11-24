package hooks

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/blsmth/agentctl/internal/buildid"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/remoteapproval"
	"github.com/blsmth/agentctl/internal/selfexec"
)

// remoteDetails is what the phone shows for an approval. Session and window
// are resolved when the approval opens.
type remoteDetails struct {
	Kind        string // remoteapproval.KindDispatch or KindBash
	AskingAgent string
	TargetAgent string
	Issue       int
	// Prompt is the fully resolved dispatch prompt (including --prompt-key
	// content).
	Prompt  string
	Command string
	// Details is the resolved where/how of a dispatch (zero for other kinds).
	Details remoteapproval.Details
}

// openRemote opens a remote approval with the running agentctl serve. It
// returns remoteapproval.ErrNotRunning when serve is not answering. Seam for
// tests (TestMain makes it always "not running").
var openRemote = openRemoteApproval

// popupCloseWait bounds how long the hook waits for the popup goroutine after
// the phone answered or the deadline passed.
var popupCloseWait = 2 * time.Second

// remoteGuard builds the guard that verifies serve's control socket. Seam
// for tests (a fake dialer and process checks).
var remoteGuard = defaultRemoteGuard

func defaultRemoteGuard() (remoteapproval.Guard, error) {
	state, err := config.StateDir()
	if err != nil {
		return remoteapproval.Guard{}, err
	}
	return remoteapproval.Guard{
		Dir:  filepath.Join(state, "serve"),
		Self: func() (buildid.Identity, error) { return selfexec.Capture() },
	}, nil
}

func openRemoteApproval(req remoteapproval.Request) (*remoteapproval.Approval, error) {
	g, err := remoteGuard()
	if err != nil {
		return nil, remoteapproval.ErrNotRunning
	}
	return g.Open(req)
}

// startRemote opens the phone side of an approval, or returns nil when there
// is none (no details, serve not running, or serve failed verification): the
// popup alone then decides, exactly as without away mode.
func startRemote(d *remoteDetails, logPrefix string, deadline time.Time) *remoteapproval.Approval {
	if d == nil {
		return nil
	}
	id, err := remoteapproval.NewID()
	if err != nil {
		logging.Log("%s: remote approval: %v; laptop popup only", logPrefix, err)
		return nil
	}
	req := remoteapproval.Request{
		ID:          id,
		Kind:        d.Kind,
		AskingAgent: d.AskingAgent,
		Session:     approvalSessionName(),
		WindowID:    approvalWindowID(),
		TargetAgent: d.TargetAgent,
		Issue:       d.Issue,
		Prompt:      d.Prompt,
		Command:     d.Command,
		Details:     d.Details,
		Deadline:    deadline,
	}
	a, err := openRemote(req)
	switch {
	case errors.Is(err, remoteapproval.ErrPhoneOff):
		logging.Debug("%s: phone access is off; laptop popup only", logPrefix)
		return nil
	case errors.Is(err, remoteapproval.ErrNotRunning):
		logging.Debug("%s: agentctl serve not running; laptop popup only", logPrefix)
		return nil
	case err != nil:
		logging.Log("%s: remote approval unavailable: %v; laptop popup only", logPrefix, err)
		return nil
	}
	logging.Log("%s: remote approval %s opened on agentctl serve", logPrefix, id)
	return a
}

type remoteResult struct {
	d   remoteapproval.Decision
	err error
}

// raceRemote waits for the first answer from the laptop popup or the phone.
//
//   - Laptop answers: the remote approval is cancelled (answered_on_laptop).
//   - Phone answers: the popup is closed (display-popup -C on its client) and
//     the phone's verdict returned.
//   - Deadline or signal: both are closed and the command denied.
//   - Any remote error: the remote is dropped and the popup alone counts.
//
// With no laptop client (target.Client empty) the popup is not shown and only
// the phone can answer; if the remote path then fails, the popup is started
// after all, so behavior falls back to the popup alone.
func raceRemote(ctx context.Context, target popupTarget, logPrefix string, remote *remoteapproval.Approval,
	run func(context.Context) popupResult, deny func(closePopup bool) (bool, bool, string)) (bool, bool, string) {
	popupCtx, cancelPopup := context.WithCancel(ctx)
	defer cancelPopup()

	var popupCh chan popupResult
	startPopup := func() {
		popupCh = make(chan popupResult, 1)
		go func() { popupCh <- run(popupCtx) }()
	}
	// stopPopup ends a running popup and returns how it ended (zero value
	// when it did not finish in time).
	stopPopup := func() popupResult {
		if popupCh == nil {
			return popupResult{end: popupExpired}
		}
		cancelPopup()
		select {
		case pr := <-popupCh:
			popupCh = nil
			return pr
		case <-time.After(popupCloseWait):
			popupCh = nil
			return popupResult{end: popupExpired, popupOpen: true}
		}
	}

	phoneOnly := target.Client == ""
	if phoneOnly {
		logging.Log("%s: no laptop tmux client; waiting for the phone only", logPrefix)
	} else {
		startPopup()
	}
	remoteCh := make(chan remoteResult, 1)
	go func() {
		d, err := remote.Wait()
		remoteCh <- remoteResult{d, err}
	}()
	popupPassedThrough := false

	for {
		select {
		case pr := <-popupCh:
			popupCh = nil
			switch pr.end {
			case popupVerdict:
				// A phone answer that arrived in the same instant counts too.
				if d, ok := pendingPhone(remoteCh); ok {
					remote.Close()
					return conflictVerdict(logPrefix, pr, d)
				}
				remote.Cancel(remoteapproval.ReasonLaptop)
				logging.Log("%s: approval source=laptop approved=%v", logPrefix, pr.approved)
				lastApproval.Source = approvalSourceLaptop
				return pr.approved, pr.decided, ""
			case popupPassThrough:
				if remoteCh != nil {
					logging.Log("%s: popup unavailable; waiting for the phone", logPrefix)
					popupPassedThrough = true
					continue
				}
				return false, false, ""
			default:
				remote.Cancel(remoteapproval.ReasonTimeout)
				return deny(pr.popupOpen)
			}

		case rr := <-remoteCh:
			remoteCh = nil
			if rr.err != nil {
				remote.Close()
				logging.Log("%s: remote approval failed: %v; only the laptop popup counts", logPrefix, rr.err)
				switch {
				case popupCh != nil:
					continue
				case popupPassedThrough:
					return false, false, ""
				default: // phone only: fall back to the popup
					startPopup()
					continue
				}
			}
			remote.Close()
			pr := stopPopup()
			if !phoneOnly {
				if cerr := popupCloser(target.Client); cerr != nil {
					logging.Log("%s: failed to close popup: %v", logPrefix, cerr)
				}
			}
			if pr.end == popupVerdict {
				// The laptop answered in the same instant.
				return conflictVerdict(logPrefix, pr, rr.d)
			}
			approved := rr.d.Decision == remoteapproval.Allow
			logging.Log("%s: approval source=phone device=%s approved=%v", logPrefix, rr.d.Device, approved)
			lastApproval.Source = approvalSourcePhone
			return approved, true, ""

		case <-ctx.Done():
			pr := stopPopup()
			d, phoneAnswered := pendingPhone(remoteCh)
			remote.Cancel(remoteapproval.ReasonTimeout)
			switch {
			case pr.end == popupVerdict && phoneAnswered:
				return conflictVerdict(logPrefix, pr, d)
			case pr.end == popupVerdict:
				logging.Log("%s: approval source=laptop approved=%v (at the deadline)", logPrefix, pr.approved)
				lastApproval.Source = approvalSourceLaptop
				return pr.approved, pr.decided, ""
			}
			return deny(pr.popupOpen)
		}
	}
}

// pendingPhone returns a phone decision that has already arrived on
// remoteCh, without waiting. A remote error counts as no decision.
func pendingPhone(remoteCh chan remoteResult) (remoteapproval.Decision, bool) {
	if remoteCh == nil {
		return remoteapproval.Decision{}, false
	}
	select {
	case rr := <-remoteCh:
		return rr.d, rr.err == nil
	default:
		return remoteapproval.Decision{}, false
	}
}

// conflictVerdict combines a laptop verdict and a phone decision that arrived
// together: approved only when both said allow, denied otherwise.
func conflictVerdict(logPrefix string, pr popupResult, d remoteapproval.Decision) (bool, bool, string) {
	approved := pr.approved && d.Decision == remoteapproval.Allow
	logging.Log("%s: approval source=laptop+phone laptop_approved=%v phone=%s device=%s approved=%v",
		logPrefix, pr.approved, d.Decision, d.Device, approved)
	lastApproval.Source = approvalSourceLaptop // both answered allow
	return approved, true, ""
}
