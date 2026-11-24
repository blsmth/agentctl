package cmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/blsmth/agentctl/internal/fleet"
)

// statusTarget is what a window reference resolved to: a live window's row,
// or a closed session (closed != nil, row zero).
type statusTarget struct {
	row    statusRow
	closed *fleet.Session
}

// handleLike reports whether ref could be a session handle or ID: at least
// fleet.MinHandle hex digits (and the dashes of a full UUID).
func handleLike(ref string) bool {
	if len(ref) < fleet.MinHandle {
		return false
	}
	for _, c := range strings.ToLower(ref) {
		if !strings.ContainsRune("0123456789abcdef-", c) {
			return false
		}
	}
	return true
}

// resolveTarget resolves the reference every window-taking command accepts:
//
//   - "@N" is that tmux window, as always;
//   - a handle or full session ID (hex, 4+ characters) is the session with
//     that ID prefix, live or closed. Ambiguity is an error listing the
//     candidates with their labels;
//   - otherwise a bare window number or an agent name, as before.
//
// A handle is tried before a bare number because a 4-digit handle ("1234") is
// indistinguishable from a window number; write "@1234" for the window.
func resolveTarget(rows []statusRow, ix *fleet.Index, ref string) (statusTarget, error) {
	ref = strings.TrimSpace(ref)
	if ref != "" && !strings.HasPrefix(ref, "@") && handleLike(ref) {
		s, err := ix.Resolve(ref)
		var amb *fleet.AmbiguousError
		switch {
		case err == nil:
			return targetFromSession(rows, s)
		case errors.As(err, &amb):
			return statusTarget{}, err
		}
	}
	sr, err := findWindow(rows, ref)
	if err != nil {
		if handleLike(ref) {
			return statusTarget{}, fmt.Errorf("unknown session or window %q (try `agentctl status`): %w", ref, err)
		}
		return statusTarget{}, err
	}
	return statusTarget{row: sr}, nil
}

func targetFromSession(rows []statusRow, s fleet.Session) (statusTarget, error) {
	if !s.Live {
		return statusTarget{closed: &s}, nil
	}
	for _, sr := range rows {
		if sr.row.SessionID == s.SessionID {
			return statusTarget{row: sr}, nil
		}
	}
	// Live but filtered out of rows (cannot happen for unfiltered callers).
	return statusTarget{}, fmt.Errorf("session %s is not among the listed windows", s.Handle)
}

// loadTarget loads every live session and resolves ref against them and the
// closed ledger.
func loadTarget(ref string) (statusTarget, error) {
	rows, groups, err := collectStatusRows(StatusOptions{})
	if err != nil {
		return statusTarget{}, err
	}
	return resolveTarget(rows, loadSessionIndex(groups), ref)
}
