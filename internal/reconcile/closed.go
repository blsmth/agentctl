package reconcile

import (
	"path/filepath"

	"github.com/blsmth/agentctl/internal/closedlog"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/tmux"
)

// appendClosed writes one closed-ledger line; a seam for tests.
var appendClosed = closedlog.Append

// recordClosed appends c's closed-ledger line. It never fails Finish: an
// error is logged and the close carries on.
func recordClosed(c Claimed, d Deps, instance, base string) {
	if err := appendClosed(closedRecord(c, d, instance, base)); err != nil {
		logging.Log("reconcile: recording closed window %s: %v", c.WindowID, err)
	}
}

// closedRecord builds the ledger line for the claimed window c.
func closedRecord(c Claimed, d Deps, instance, base string) closedlog.Record {
	ws := c.Record
	session := d.Session
	if session == "" {
		session = tmux.SessionName
	}
	var project string
	if d.RuntimeDir != "" {
		project = filepath.Dir(d.RuntimeDir)
	}
	work := ws.WorkDir
	if work == "" {
		work = ws.WorktreeDir
	}
	if work == "" {
		work = project
	}
	pr, prURL := ws.PRNumber, ws.PRURL
	if pr == 0 && prURL == "" {
		pr, prURL = ws.LogPR.PRNumber, ws.LogPR.PRURL
	}
	reason := closedlog.ReasonClosed
	if ws.ClosedBy != "" {
		reason = closedlog.ReasonCascade
	}
	return closedlog.Record{
		WindowID:         c.WindowID,
		Session:          session,
		ProjectDir:       project,
		RuntimeDir:       d.RuntimeDir,
		WorkDir:          work,
		Agent:            base,
		Instance:         instance,
		Harness:          ws.Harness,
		Account:          ws.Account,
		Backend:          ws.Backend,
		Model:            ws.Model,
		Mode:             ws.Mode,
		SessionID:        ws.SessionID,
		SessionName:      ws.SessionName,
		LogPath:          ws.LogPath,
		ShortTitle:       ws.ShortTitle.Title,
		TitleKind:        ws.ShortTitle.Kind,
		Ticket:           ws.Ticket,
		TicketTitle:      ws.TicketTitle,
		FirstPrompt:      firstNonEmpty(ws.FirstPrompt, ws.Task),
		State:            ws.State,
		Done:             ws.Done,
		TurnCount:        ws.TurnCount,
		Branch:           ws.Branch,
		Worktree:         ws.WorktreeDir,
		PRNumber:         pr,
		PRURL:            prURL,
		SpawnedBy:        ws.SpawnedBy,
		SpawnedBySession: ws.SpawnedBySession,
		ScheduledBy:      ws.ScheduledBy,
		Reason:           reason,
		ClosedBy:         ws.ClosedBy,
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
