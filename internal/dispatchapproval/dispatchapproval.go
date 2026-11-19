// Package dispatchapproval records that a human approved one `agentctl
// dispatch` invocation in the approval popup, so the dispatch CLI can tell an
// approved launch from an unapproved one.
//
// The PreToolUse hook (internal/hooks) records a marker when the human
// approves a dispatch popup; the CLI consumes it when a launch needs proof of
// approval (today: a --review-pr whose code-reviewer is not the global
// definition, see internal/cmd/dispatch_review.go). A marker is keyed by the
// issuing tmux window and the dispatch arguments, is single-use and expires
// after TTL.
//
// Markers live in <StateDir>/dispatch-approvals/ (0700 dir, 0600 files),
// which the hook tamper rules deny agents' Bash and Write/Edit from writing.
// They are per user, not per project, so a dispatch that changed directory
// before running still finds the marker its popup recorded.
package dispatchapproval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/config"
)

// DirName is the marker directory's name under the state dir.
const DirName = "dispatch-approvals"

// TTL is how long an approval stays valid: the gap between the popup closing
// and the CLI starting is the shell's start-up time, so this is generous.
const TTL = 2 * time.Minute

// futureSkew is how far in the future a marker's timestamp may be before it
// is treated as forged or corrupt.
const futureSkew = 5 * time.Second

// ErrNotApproved means no valid marker matched: none was recorded, it was
// already used, or it expired.
var ErrNotApproved = errors.New("no approval recorded for this dispatch")

// now is the clock; tests pin it.
var now = time.Now

// dirFn resolves the marker directory; tests point it at a temp dir.
var dirFn = func() (string, error) {
	state, err := config.StateDir()
	if err != nil {
		return "", fmt.Errorf("resolving state dir: %w", err)
	}
	return filepath.Join(state, DirName), nil
}

// Dir returns the marker directory, resolved on each call.
func Dir() (string, error) { return dirFn() }

type marker struct {
	Window  string    `json:"window"`
	Created time.Time `json:"created"`
}

// Key is the marker name for a dispatch issued from windowID with args (the
// words after "dispatch", as the shell passes them).
func Key(windowID string, args []string) string {
	h := sha256.New()
	h.Write([]byte(windowID))
	for _, a := range args {
		h.Write([]byte{0})
		h.Write([]byte(a))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Record stores an approval for one dispatch from windowID with args, and
// prunes expired markers.
func Record(windowID string, args []string) error {
	if windowID == "" {
		return errors.New("recording dispatch approval: no window")
	}
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	prune(dir)
	data, err := json.Marshal(marker{Window: windowID, Created: now()})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("writing dispatch approval: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return fmt.Errorf("writing dispatch approval: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("writing dispatch approval: %w", err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, Key(windowID, args))); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("writing dispatch approval: %w", err)
	}
	return nil
}

// Consume claims the approval for a dispatch from windowID with args. It
// returns nil exactly once per recorded approval, and ErrNotApproved (or a
// wrapped error) when there is none, it was used, or it expired.
func Consume(windowID string, args []string) error {
	if windowID == "" {
		return ErrNotApproved
	}
	dir, err := Dir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, Key(windowID, args))
	fi, err := os.Lstat(path)
	if err != nil {
		return ErrNotApproved
	}
	if !fi.Mode().IsRegular() {
		os.Remove(path)
		return ErrNotApproved
	}
	data, readErr := os.ReadFile(path)
	// Removing is the claim: of two concurrent consumers only one succeeds.
	if err := os.Remove(path); err != nil {
		return ErrNotApproved
	}
	if readErr != nil {
		return ErrNotApproved
	}
	var m marker
	if err := json.Unmarshal(data, &m); err != nil || m.Window != windowID {
		return ErrNotApproved
	}
	t := now()
	if m.Created.After(t.Add(futureSkew)) {
		return ErrNotApproved
	}
	if t.Sub(m.Created) > TTL {
		return fmt.Errorf("%w (the approval expired after %s)", ErrNotApproved, TTL)
	}
	return nil
}

// prune removes markers older than TTL (and stray temp files), best effort.
func prune(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := now().Add(-TTL)
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) || (strings.HasPrefix(e.Name(), ".tmp-") && info.ModTime().Before(now().Add(-time.Minute))) {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
