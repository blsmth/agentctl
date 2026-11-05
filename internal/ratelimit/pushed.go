package ratelimit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/safefs"
)

// pushedFreshness is how recent ~/.local/state/agentctl/ratelimits.json must be
// for RateLimitProber.Tick to trust it instead of running the API-consuming
// probe.
const pushedFreshness = 15 * time.Minute

// PushedWindow mirrors one rate_limits.{five_hour,seven_day,spend_limit}
// entry from a statusLine payload, already coerced and validated by
// internal/statusline. Unlike the payload's own RateLimitWindow, this is the
// persisted representation, so it is value-typed -- the *presence* of a
// *PushedWindow pointer on PushedRateLimits (not a value inside it) is what
// encodes "this window exists".
type PushedWindow struct {
	UsedPercentage float64 `json:"used_percentage"`
	ResetsAt       int64   `json:"resets_at"`
}

// PushedRateLimits is the on-disk shape of
// ~/.local/state/agentctl/ratelimits.json. It is account-level, not per-window:
// every agentctl process on the machine (one per tmux window) writes and
// reads this same file, because Claude's rate limits apply to the whole
// account, not to an individual session. Concurrent writers are safe because
// SavePushed writes atomically (tmp+rename).
//
// A nil window field means that window was absent from the triggering
// statusLine payload (an expired window, or an account not on
// Pro/Max/gateway) -- it is never zero-filled. Do not add a zero-value
// PushedWindow to represent "no data"; that is exactly the bug pointer
// fields exist to prevent (see internal/statusline's Payload doc comment).
type PushedRateLimits struct {
	UpdatedAt  int64         `json:"updated_at"`
	Source     string        `json:"source"`
	FiveHour   *PushedWindow `json:"five_hour,omitempty"`
	SevenDay   *PushedWindow `json:"seven_day,omitempty"`
	SpendLimit *PushedWindow `json:"spend_limit,omitempty"`
}

// Fresh reports whether pr is usable as a probe replacement at time now:
// non-nil, carrying a five_hour window (the window RateLimitProber's callers
// actually consume), and written within pushedFreshness of now. A nil
// receiver is handled (reports false) so callers can pass through a possibly
// nil *PushedRateLimits from LoadPushed without an extra check.
func (pr *PushedRateLimits) Fresh(now time.Time) bool {
	if pr == nil || pr.FiveHour == nil {
		return false
	}
	age := now.Sub(time.Unix(pr.UpdatedAt, 0))
	return age <= pushedFreshness
}

// pushedRateLimitsPath returns ~/.local/state/agentctl/ratelimits.json.
func pushedRateLimitsPath() (string, error) {
	dir, err := config.StateDir()
	if err != nil {
		return "", fmt.Errorf("resolving state dir: %w", err)
	}
	newPath := filepath.Join(dir, "ratelimits.json")
	if cfg, cerr := config.GlobalDir(); cerr == nil {
		return config.MigrateLegacy(filepath.Join(cfg, "ratelimits.json"), newPath), nil
	}
	return newPath, nil
}

// LoadPushed reads the shared statusLine-sourced rate limit file. It returns
// a nil *PushedRateLimits (not an error) when the file does not exist --
// callers treat that identically to "no pushed data yet".
func LoadPushed() (*PushedRateLimits, error) {
	path, err := pushedRateLimitsPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var pr PushedRateLimits
	if err := json.Unmarshal(data, &pr); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return &pr, nil
}

// SavePushed atomically (tmp+rename) writes the shared statusLine-sourced
// rate limit file. Atomicity matters here specifically because Claude Code
// cancels an in-flight statusLine script the instant a new update triggers;
// a bare os.WriteFile could be truncated mid-write and leave every reader on
// the machine looking at a corrupt or empty file.
func SavePushed(pr *PushedRateLimits) error {
	path, err := pushedRateLimitsPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(pr, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling rate limits: %w", err)
	}
	data = append(data, '\n')

	// An unpredictable temp name (not path+".tmp", which a planted symlink
	// could redirect) and a refusal to replace a symlinked target. The
	// directory chain is the user's own config dir and is not checked.
	if err := safefs.ReplaceFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
