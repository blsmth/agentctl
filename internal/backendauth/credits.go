package backendauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/notify"
	"github.com/blsmth/agentctl/internal/safefs"
)

// CreditsFreshFor is how long a no-credits marker is trusted without a new
// request. After it the marker is stale and the next caller probes once; a
// probe that sees no credits renews it.
const CreditsFreshFor = time.Hour

// CreditsURL is where a user adds Nous Portal credits.
const CreditsURL = "https://portal.nousresearch.com"

// IsNoCredits classifies a backend HTTP response as "the account is out of
// credits": any 402, or a 403/404 whose JSON error.code is the paid-model
// code. A non-JSON 403 (a WAF block) is not a credit problem.
func IsNoCredits(status int, body []byte) bool {
	if status == http.StatusPaymentRequired {
		return true
	}
	if status != http.StatusForbidden && status != http.StatusNotFound {
		return false
	}
	var e struct {
		Error struct {
			Code any `json:"code"`
		} `json:"error"`
	}
	return json.Unmarshal(body, &e) == nil && e.Error.Code == any(noCreditsCode)
}

// CreditState is the persisted per-backend credit marker,
// <stateDir>/backends/<name>.json. A zero NoCreditsSince means clear.
type CreditState struct {
	NoCreditsSince time.Time `json:"no_credits_since"`
	LastChecked    time.Time `json:"last_checked"`
}

// Fresh reports whether the marker is set and was confirmed within
// CreditsFreshFor of now.
func (s CreditState) Fresh(now time.Time) bool {
	return !s.NoCreditsSince.IsZero() && now.Sub(s.LastChecked) < CreditsFreshFor
}

// creditStateDir is the state-dir resolver; tests replace it.
var creditStateDir = config.StateDir

// creditNotify announces the clear-to-set transition; tests replace it.
var creditNotify = func(name string) {
	notify.NotifyWithOptions(notify.Options{
		Title:    "Nous Portal out of credits",
		Subtitle: "Backend " + name + " · agentctl",
		Message:  "Short titles paused, nous dispatches will be refused",
		Env: map[string]string{
			"AGENTCTL_AGENT_NAME":        "agentctl",
			"AGENTCTL_NOTIFICATION_TYPE": "no_credits",
			"AGENTCTL_BACKEND":           name,
		},
	})
}

func creditPath(name string) (string, error) {
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		return "", fmt.Errorf("backendauth: invalid backend name %q", name)
	}
	d, err := creditStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "backends", name+".json"), nil
}

func readCreditState(path string) CreditState {
	f, err := os.Open(path)
	if err != nil {
		return CreditState{}
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, 64<<10))
	var s CreditState
	if err != nil || json.Unmarshal(data, &s) != nil {
		return CreditState{} // corrupt: treat as clear
	}
	return s
}

// LoadCreditState reads the marker for backend name with no network and no
// locking. Missing, unreadable or corrupt state is the zero (clear) state.
func LoadCreditState(name string) CreditState {
	p, err := creditPath(name)
	if err != nil {
		return CreditState{}
	}
	return readCreditState(p)
}

// NoCreditsFresh returns the marker for name and whether it is set and fresh.
func NoCreditsFresh(name string, now time.Time) (CreditState, bool) {
	s := LoadCreditState(name)
	return s, s.Fresh(now)
}

// lockCredits takes an exclusive flock on a sidecar lock file (the state file
// itself is replaced by rename, so it cannot carry the lock).
func lockCredits(path string) (unlock func(), err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(strings.TrimSuffix(path, ".json")+".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// MarkNoCredits records that backend name has no credits. When the marker
// goes from clear to set (a stale marker re-confirmed by a probe is not a new
// event) it sends one desktop notification; the claim is made under a
// flock so concurrent hooks notify once. Errors are logged, never returned:
// credit tracking must not break the caller.
func MarkNoCredits(name string) (transitioned bool) {
	return markNoCredits(name, time.Now())
}

func markNoCredits(name string, now time.Time) bool {
	p, err := creditPath(name)
	if err != nil {
		logging.Debug("backendauth: %v", err)
		return false
	}
	unlock, err := lockCredits(p)
	if err != nil {
		logging.Debug("backendauth: lock credit state: %v", err)
		return false
	}
	cur := readCreditState(p)
	next := CreditState{NoCreditsSince: cur.NoCreditsSince, LastChecked: now}
	transitioned := cur.NoCreditsSince.IsZero()
	if transitioned {
		next.NoCreditsSince = now
	}
	data, err := json.Marshal(next)
	if err == nil {
		err = safefs.ReplaceFile(p, data, 0o600)
	}
	unlock()
	if err != nil {
		logging.Debug("backendauth: write credit state: %v", err)
		return false
	}
	if transitioned {
		creditNotify(name)
	}
	return transitioned
}

// ClearNoCredits removes the marker for name after a successful request. It
// is a single stat when there is nothing to clear.
func ClearNoCredits(name string) {
	p, err := creditPath(name)
	if err != nil {
		return
	}
	if _, err := os.Lstat(p); err != nil {
		return
	}
	unlock, err := lockCredits(p)
	if err != nil {
		return
	}
	defer unlock()
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		logging.Debug("backendauth: clear credit state: %v", err)
	}
}

// RelativeSince renders how long ago t was, e.g. "2h ago".
func RelativeSince(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// NoCreditsMessage is the user-facing refusal for a backend with a fresh
// marker.
func NoCreditsMessage(name string, s CreditState, now time.Time) string {
	return fmt.Sprintf("Nous Portal account %s is out of credits (since %s). Add credits at %s, or use a :free model; `agentctl backend status --verify` rechecks.",
		name, RelativeSince(s.NoCreditsSince, now), CreditsURL)
}
