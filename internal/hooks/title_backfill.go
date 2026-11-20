package hooks

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/shorttitle"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/windowstate"
)

const (
	// BackfillPerRun caps the title requests one housekeeping run sends, so a
	// run cannot drain the hourly budget.
	BackfillPerRun = 3
	// backfillRequestTimeout bounds one backfill request; shorter than
	// shorttitle.RequestTimeout so a run stays well under a minute.
	backfillRequestTimeout = 8 * time.Second
)

// Backfill seams; tests replace them.
var (
	titleRecords = windowstate.All
	// titleRecap is the newest away_summary in a window's log (a local read,
	// cached by fleet).
	titleRecap = func(logPath string) string { return fleet.ReadLogInfo(logPath).Recap }
)

// BackfillTitles titles live windows that never got a title, or whose
// refinement never happened, because no hook fired since titles shipped (an
// idle window). live is the set of this session's tmux window IDs; records of
// windows not in it (other sessions, vanished windows) are never touched, and
// windowstate only holds this runtime dir's windows. It sends at most
// BackfillPerRun requests, through the same claim, budget (minus
// shorttitle.BackfillReserve) and backoff as hook-title, and returns how many
// it sent.
//
// A window that has finished a turn is titled from its task or first prompt
// plus its last response and session recap, so the title is final: it
// describes the session so far. A window before its first Stop gets a normal
// first title, which its first Stop refines as usual.
//
// It does nothing when titles are off, no key is available or the backend is
// out of credits. It never spawns anything and is called only from the
// already-detached `agentctl reconcile --housekeeping`.
func BackfillTitles(ctx context.Context, live map[string]bool) (sent int) {
	defer func() {
		if r := recover(); r != nil {
			logging.Log("title-backfill: recovered: %v", r)
		}
	}()
	s := titleSettings()
	if !s.Enabled || len(live) == 0 {
		return 0
	}
	now := titleNow()
	if !titleReady(s, now) {
		return 0
	}
	for _, c := range backfillCandidates(titleRecords(), live, now) {
		if sent >= BackfillPerRun {
			break
		}
		sessionID := c.ws.SessionID
		still := func(w windowstate.WindowState) bool { return w.SessionID == sessionID }
		rctx, cancel := context.WithTimeout(ctx, backfillRequestTimeout)
		out := titleWindow(rctx, s, c.id, c.in, now, titleTakeBackfillBudget, still)
		cancel()
		switch out {
		case titleDeferred:
			// Budget, key or credits: the next window would hit the same wall.
			return sent
		case titleRequested:
			sent++
		}
	}
	if sent > 0 {
		logging.Log("title-backfill: sent %d title request(s)", sent)
	}
	return sent
}

type backfillCandidate struct {
	id string
	ws windowstate.WindowState
	in shorttitle.Input
}

// backfillCandidates are the live records due a title, untitled windows
// first, then by window ID.
func backfillCandidates(recs map[string]windowstate.WindowState, live map[string]bool, now time.Time) []backfillCandidate {
	var out []backfillCandidate
	for id, ws := range recs {
		// Under backoff (or claimed by a hook right now): skip before reading
		// the log.
		if !live[id] || !backfillEligible(ws) || now.Before(ws.ShortTitle.RetryAfter) {
			continue
		}
		in := backfillInput(ws)
		if !shorttitle.Due(ws.ShortTitle, in, now) {
			continue
		}
		out = append(out, backfillCandidate{id: id, ws: ws, in: in})
	}
	sort.Slice(out, func(i, j int) bool {
		ti, tj := out[i].ws.ShortTitle.Title == "", out[j].ws.ShortTitle.Title == ""
		if ti != tj {
			return ti
		}
		return out[i].id < out[j].id
	})
	return out
}

// backfillEligible reports whether ws is an agent window the backfill may
// title: a started Claude session, not frozen or human-titled, not a shell or improvement
// window, and not a cron run (short-lived; its own hooks title it).
func backfillEligible(ws windowstate.WindowState) bool {
	if ws.ShortTitle.Final || ws.ShortTitle.Manual || ws.SessionID == "" || ws.ScheduledBy != "" {
		return false
	}
	instance := tmux.StripAgentPrefix(ws.Instance)
	if instance == "" {
		instance = tmux.StripAgentPrefix(ws.RenderedName)
	}
	return instance != "shell" && !strings.HasPrefix(instance, "improve~")
}

// backfillInput is hook-title's input for ws, plus, once the window has
// finished a turn, its last response and session recap, which make the
// title final.
func backfillInput(ws windowstate.WindowState) shorttitle.Input {
	in := titleInput(ws, "")
	if ws.TurnCount > 0 || ws.LastText != "" {
		in.LastText = ws.LastText
		in.Recap = titleRecap(ws.LogPath)
	}
	return in
}
