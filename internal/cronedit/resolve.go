package cronedit

import (
	"os"
	"time"

	"github.com/blsmth/agentctl/internal/claudehome"
	"github.com/blsmth/agentctl/internal/closedlog"
	"github.com/blsmth/agentctl/internal/cron"
	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/sessions"
)

// RunKind says where a scheduled run's session is now.
type RunKind string

const (
	// RunLive: the run's window is still open.
	RunLive RunKind = "live"
	// RunClosed: the window closed; the transcript can be read.
	RunClosed RunKind = "closed"
	// RunMissing: nothing to open (see RunTarget.Reason).
	RunMissing RunKind = "missing"
)

// legacyLiveTolerance is how far a window's scheduled_at may sit from the
// run entry's timestamp when matching an entry that has no session ID.
const legacyLiveTolerance = 2 * time.Minute

// closedLookback bounds the closed-ledger read (the ledger prunes at 90 days).
const closedLookback = 100 * 24 * time.Hour

// RunTarget is what a run entry resolves to.
type RunTarget struct {
	Kind      RunKind   `json:"kind"`
	Session   string    `json:"session,omitempty"`
	WindowID  string    `json:"window_id,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
	LogPath   string    `json:"log_path,omitempty"`
	WorkDir   string    `json:"work_dir,omitempty"`
	Agent     string    `json:"agent,omitempty"`
	Account   string    `json:"account,omitempty"`
	Harness   string    `json:"harness,omitempty"`
	Title     string    `json:"title,omitempty"`
	ClosedAt  time.Time `json:"closed_at,omitzero"`
	// Resumable is true when a closed run can be resumed; Reason then says
	// why not (or, for a missing run, why nothing opens).
	Resumable bool   `json:"resumable"`
	Reason    string `json:"reason,omitempty"`

	// Record is the closed-ledger record behind a closed target, for the
	// shared resume path. Not serialized.
	Record *closedlog.Record `json:"-"`
}

// LiveWindow is a live agent window as the resolver sees it.
type LiveWindow struct {
	Session     string
	WindowID    string
	SessionID   string
	ScheduledBy string
	ScheduledAt time.Time
	Title       string
	Agent       string
	Account     string
	Harness     string
	WorkDir     string
	LogPath     string
}

// Resolver turns run entries into targets. Every external read is a seam.
type Resolver struct {
	// Live lists the live agent windows (any tmux session).
	Live func() []LiveWindow
	// Closed lists closed-ledger records since the given time.
	Closed func(since time.Time) []closedlog.Record
	// FindLog returns the transcript for a session ID, or "".
	FindLog func(sessionID string) string
	// Stat is os.Stat.
	Stat func(string) (os.FileInfo, error)
	Now  func() time.Time
}

// NewResolver returns a Resolver over the real fleet, closed ledger and every
// account's projects dir. It makes no tmux calls beyond fleet.Load's listing.
func NewResolver() *Resolver {
	return &Resolver{
		Live:   liveFromFleet,
		Closed: func(since time.Time) []closedlog.Record { r, _ := closedlog.Load(since); return r },
		FindLog: func(id string) string {
			for _, dir := range claudehome.ProjectsDirs() {
				if p := sessions.FindLogBySessionIDIn(dir, id); p != "" {
					return p
				}
			}
			return ""
		},
		Stat: os.Stat,
		Now:  time.Now,
	}
}

func liveFromFleet() []LiveWindow {
	groups, err := fleet.Load()
	if err != nil {
		return nil
	}
	var out []LiveWindow
	for _, g := range groups {
		for _, r := range g.Rows {
			out = append(out, LiveWindow{
				Session: r.Session, WindowID: r.WindowID, SessionID: r.SessionID,
				ScheduledBy: r.ScheduledBy, ScheduledAt: r.ScheduledAt,
				Title: r.ShortTitle, Agent: r.AgentName, Account: r.Account, Harness: r.Harness,
				WorkDir: r.WorkDir, LogPath: r.LogPath,
			})
		}
	}
	return out
}

// Resolvable reports whether the entry is a run that has a session: only
// fired (scheduled or manual) entries are.
func Resolvable(e cron.Entry) bool { return e.Result == cron.ResultFired }

// ResolveRun finds the session behind a run entry.
func (r *Resolver) ResolveRun(e cron.Entry) RunTarget {
	if !Resolvable(e) {
		return RunTarget{Kind: RunMissing, Reason: "this run did not start a session"}
	}
	if t, ok := r.resolveLive(e); ok {
		return t
	}
	if t, ok := r.resolveClosed(e); ok {
		return t
	}
	reason := "the session is no longer recorded (closed more than 90 days ago, or on another machine)"
	if e.SessionID == "" {
		reason = "run predates session tracking"
	}
	return RunTarget{Kind: RunMissing, SessionID: e.SessionID, Reason: reason}
}

func (r *Resolver) resolveLive(e cron.Entry) (RunTarget, bool) {
	for _, w := range r.Live() {
		if !liveMatches(e, w) {
			continue
		}
		return RunTarget{
			Kind: RunLive, Session: w.Session, WindowID: w.WindowID, SessionID: w.SessionID,
			LogPath: w.LogPath, WorkDir: w.WorkDir, Agent: w.Agent, Account: w.Account,
			Harness: w.Harness, Title: w.Title,
		}, true
	}
	return RunTarget{}, false
}

func liveMatches(e cron.Entry, w LiveWindow) bool {
	if e.SessionID != "" {
		// A run still launching has no session yet in its window record;
		// the same tmux window and job still identify it.
		if w.SessionID == e.SessionID {
			return true
		}
		return w.SessionID == "" && w.Session == e.Session && w.WindowID == e.WindowID &&
			w.ScheduledBy == e.Job && within(w.ScheduledAt, e.Time, legacyLiveTolerance)
	}
	return w.Session == e.Session && w.WindowID != "" && w.WindowID == e.WindowID &&
		w.ScheduledBy == e.Job && within(w.ScheduledAt, e.Time, legacyLiveTolerance)
}

func within(a, b time.Time, d time.Duration) bool {
	if a.IsZero() || b.IsZero() {
		return false
	}
	diff := a.Sub(b)
	if diff < 0 {
		diff = -diff
	}
	return diff <= d
}

func (r *Resolver) resolveClosed(e cron.Entry) (RunTarget, bool) {
	recs := r.Closed(r.Now().Add(-closedLookback))
	var best *closedlog.Record
	for i := range recs {
		rec := &recs[i]
		if e.SessionID != "" {
			if rec.SessionID == e.SessionID {
				best = rec
				break
			}
			continue
		}
		if rec.Session != e.Session || rec.WindowID != e.WindowID || rec.ScheduledBy != e.Job || rec.ClosedAt.Before(e.Time) {
			continue
		}
		if best == nil || rec.ClosedAt.Before(best.ClosedAt) {
			best = rec
		}
	}
	if best == nil {
		return RunTarget{}, false
	}
	rec := *best
	log := rec.LogPath
	if log != "" {
		if _, err := r.Stat(log); err != nil {
			log = ""
		}
	}
	if log == "" && rec.SessionID != "" {
		log = r.FindLog(rec.SessionID)
	}
	t := RunTarget{
		Kind: RunClosed, Session: rec.Session, WindowID: rec.WindowID, SessionID: rec.SessionID,
		LogPath: log, WorkDir: rec.WorkDir, Agent: rec.Agent, Account: rec.Account,
		Harness: rec.Harness, Title: rec.Title(), ClosedAt: rec.ClosedAt, Record: &rec,
	}
	if log == "" {
		t.Kind = RunMissing
		t.Reason = "transcript no longer on disk"
		return t, true
	}
	rec.LogPath = log
	if err := rec.ResumeCheck(r.Stat); err != nil {
		t.Reason = err.Error()
	} else {
		t.Resumable = true
	}
	return t, true
}
