package fleet

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/blsmth/agentctl/internal/closedlog"
)

// MinHandle is the shortest handle, as with a short git SHA.
const MinHandle = 4

// Handle is the shortest prefix of sessionID (at least MinHandle characters)
// that no other ID in known shares. known need not contain sessionID. An
// empty sessionID (a window still launching, or a legacy record) has no
// handle.
func Handle(sessionID string, known []string) string {
	if sessionID == "" {
		return ""
	}
	n := min(MinHandle, len(sessionID))
	for _, k := range known {
		if k == sessionID {
			continue
		}
		n = max(n, min(commonPrefix(sessionID, k)+1, len(sessionID)))
	}
	return sessionID[:n]
}

func commonPrefix(a, b string) int {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	return i
}

// Session is one session known on this machine: running in a window, or
// closed and in the closed ledger.
type Session struct {
	SessionID string
	Handle    string
	Label     string
	// Row is the session's row: its live window's, or a closed record's
	// stand-in (see RecordRow).
	Row Row
	// Live reports a window still exists; Group is its session group.
	Live  bool
	Group Group
	// Closed is the ledger record of a closed session.
	Closed *closedlog.Record
}

// Index is every session ID known on this machine (live windows of all
// runtime dirs, plus the closed ledger) with its handle and label. Built from
// windowstate and the ledger alone: no tmux or monitor is needed.
type Index struct {
	sessions []Session // sorted by SessionID
	ids      []string
}

// NewIndex builds the index. A session that is both live and closed (the
// ledger keeps the old record of a resumed window) is listed once, live.
func NewIndex(groups []Group, closed []closedlog.Record) *Index {
	byID := map[string]Session{}
	for _, g := range groups {
		for _, r := range g.Rows {
			if r.SessionID == "" || r.Shell {
				continue
			}
			byID[r.SessionID] = Session{SessionID: r.SessionID, Label: Label(r, Scope{}), Row: r, Live: true, Group: g}
		}
	}
	for i := range closed {
		rec := closed[i]
		if rec.SessionID == "" {
			continue
		}
		if _, ok := byID[rec.SessionID]; ok { // live wins; closed is newest first
			continue
		}
		row := RecordRow(rec)
		byID[rec.SessionID] = Session{SessionID: rec.SessionID, Label: Label(row, Scope{}), Row: row, Closed: &rec}
	}
	ix := &Index{}
	for id := range byID {
		ix.ids = append(ix.ids, id)
	}
	sort.Strings(ix.ids)
	for _, id := range ix.ids {
		s := byID[id]
		s.Handle = Handle(id, ix.ids)
		ix.sessions = append(ix.sessions, s)
	}
	return ix
}

// HandleFor is sessionID's handle in this index; "" for no session ID.
func (ix *Index) HandleFor(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	return Handle(sessionID, ix.ids)
}

// ErrUnknownSession means no known session matches a reference.
var ErrUnknownSession = errors.New("unknown session")

// AmbiguousError is a reference that matches several sessions.
type AmbiguousError struct {
	Ref        string
	Candidates []Session
}

func (e *AmbiguousError) Error() string {
	parts := make([]string, 0, len(e.Candidates))
	for _, c := range e.Candidates {
		p := c.Handle
		if c.Label != "" {
			p += " " + fmt.Sprintf("%q", c.Label)
		}
		parts = append(parts, p)
	}
	return fmt.Sprintf("%q is ambiguous, it matches %s; use more characters", e.Ref, strings.Join(parts, ", "))
}

// Resolve finds the session a handle (an ID prefix of at least MinHandle
// characters) or a full session ID names. A full ID wins over longer IDs it
// prefixes. Unknown references wrap ErrUnknownSession; several matches are an
// *AmbiguousError listing the candidates with their labels.
func (ix *Index) Resolve(ref string) (Session, error) {
	ref = strings.ToLower(strings.TrimSpace(ref))
	if len(ref) < MinHandle {
		return Session{}, fmt.Errorf("%w %q (handles have at least %d characters)", ErrUnknownSession, ref, MinHandle)
	}
	var hits []Session
	for _, s := range ix.sessions {
		if s.SessionID == ref {
			return s, nil
		}
		if strings.HasPrefix(s.SessionID, ref) {
			hits = append(hits, s)
		}
	}
	switch len(hits) {
	case 0:
		return Session{}, fmt.Errorf("%w %q", ErrUnknownSession, ref)
	case 1:
		return hits[0], nil
	}
	return Session{}, &AmbiguousError{Ref: ref, Candidates: hits}
}
