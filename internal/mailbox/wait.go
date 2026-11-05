package mailbox

// wait.go implements the blocking mailbox wait used by `agentctl inbox
// --wait` and `agentctl send --wait-reply` (see internal/cmd/inbox.go,
// internal/cmd/send.go). It is the non-polling replacement for orchestrator
// agents that previously had no way to learn mail arrived while waiting for
// a reply and resorted to `sleep 40` guesses.

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/logging"

	"github.com/fsnotify/fsnotify"
)

// DefaultWaitTimeout is the default timeout for WaitAndClaim when the
// caller (`agentctl inbox --wait` / `agentctl send --wait-reply`) does not
// specify one explicitly.
const DefaultWaitTimeout = 5 * time.Minute

// WaitAndClaim blocks until at least one of windowID's pending messages
// matches from, then claims (marks read) only the matching messages and
// returns them, in Pending's chronological order. from is matched
// case-insensitively against Message.From; an empty from matches any
// sender.
//
// Messages that do NOT match from are left pending, untouched -- this
// mirrors DrainForIDs' claim-before-return pattern (see deliver.go) but
// scoped to only the matching subset, rather than draining the entire
// mailbox: a message from a different sender must still be there for
// whoever eventually asks for it.
//
// It always checks Pending(windowID) for an already-matching message BEFORE
// arming the fsnotify watcher. This ordering is load-bearing: a message
// already sitting on disk when the watch starts was created before fsnotify
// began observing the directory, so it generates no CREATE event -- a
// naive watch-then-check would hang until timeout even though matching
// mail was there the entire time.
//
// windowID's pending directory is created if it does not yet exist -- a
// window that has never received mail has no inbox directory at all, and
// that must not be treated as an error.
//
// Returns (nil, true, nil) on timeout -- a timeout is the normal, expected
// outcome of waiting for mail that never arrives, not an error condition.
func WaitAndClaim(windowID, from string, timeout time.Duration) (matched []Message, timedOut bool, err error) {
	claim := func() ([]Message, error) {
		pending, err := Pending(windowID)
		if err != nil {
			return nil, err
		}
		var out []Message
		for _, m := range pending {
			if from == "" || strings.EqualFold(m.From, from) {
				out = append(out, m)
			}
		}
		if len(out) == 0 {
			return nil, nil
		}
		for _, m := range out {
			if err := MarkRead(windowID, m.ID); err != nil {
				logging.Warn("mailbox: WaitAndClaim(%s): MarkRead(%s) after claiming: %v", windowID, m.ID, err)
			}
		}
		return out, nil
	}

	// Check for already-pending mail BEFORE arming the watcher -- see the
	// doc comment above for why this ordering is the critical race fix.
	if got, cerr := claim(); cerr != nil {
		return nil, false, cerr
	} else if len(got) > 0 {
		return got, false, nil
	}

	// pendingDir creates the directory if it does not exist -- a window
	// with no mail yet has no inbox directory, and fsnotify.Watcher.Add
	// errors on a directory that does not exist.
	dir, err := pendingDir(windowID)
	if err != nil {
		return nil, false, err
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, false, fmt.Errorf("mailbox: creating watcher: %w", err)
	}
	defer func() { _ = watcher.Close() }()

	if err := watcher.Add(dir); err != nil {
		return nil, false, fmt.Errorf("mailbox: watching inbox dir %s: %w", dir, err)
	}

	// Re-check once the watcher is armed: this closes the narrow race
	// window between the first check above and the OS actually registering
	// the watch -- anything enqueued in that gap generates no event either,
	// for the same reason described above.
	if got, cerr := claim(); cerr != nil {
		return nil, false, cerr
	} else if len(got) > 0 {
		return got, false, nil
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return nil, false, fmt.Errorf("mailbox: watcher closed unexpectedly")
			}
			// Only a newly-written pending message ("<id>.json" directly in
			// dir) can possibly change the answer. This filter also
			// naturally excludes read/'s own creation (its name has no
			// ".json" suffix) and anything happening inside read/ itself
			// (fsnotify.Watcher is non-recursive, so read/'s contents never
			// surface as events on dir at all) -- both are the read/
			// subdirectory events this wait must ignore.
			if filepath.Ext(event.Name) != ".json" || filepath.Dir(event.Name) != dir {
				continue
			}
			got, cerr := claim()
			if cerr != nil {
				return nil, false, cerr
			}
			if len(got) > 0 {
				return got, false, nil
			}
		case werr, ok := <-watcher.Errors:
			if !ok {
				return nil, false, fmt.Errorf("mailbox: watcher closed unexpectedly")
			}
			logging.Warn("mailbox: WaitAndClaim(%s): watcher error: %v", windowID, werr)
		case <-timer.C:
			return nil, true, nil
		}
	}
}
