package demo

import (
	"fmt"
	"io"
	"time"

	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/shellquote"
	"github.com/blsmth/agentctl/internal/windowstate"
	"github.com/blsmth/agentctl/internal/winname"
)

// SeedProject creates the project's windows in its (already running)
// session and writes their state. It must run in a process of its own whose
// environment Layout.Isolate has replaced: it binds paths and windowstate to
// the project's runtime dir. Each window's "key @id" goes to out.
func SeedProject(l Layout, s *Scene, project, self string, out io.Writer) error {
	paths.Init(l.Repo(project))
	windowstate.Init()
	if got, want := paths.RuntimeDir(), l.RuntimeDir(project); got != want {
		return fmt.Errorf("runtime dir resolved to %s, want %s", got, want)
	}
	t := Tmux{Layout: l}
	session := SessionFor(project)
	now := time.Now()
	ids := map[string]string{}
	sessionIDs := map[string]string{}
	var selectID string
	for _, w := range s.WindowsIn(project) {
		id, err := newWindow(l, t, session, w, self)
		if err != nil {
			return fmt.Errorf("window %s: %w", w.Key, err)
		}
		ids[w.Key] = id
		sessionIDs[w.Key] = WindowSessionID(w)
		parent := Parent{WindowID: ids[w.SpawnedBy], SessionID: sessionIDs[w.SpawnedBy]}
		if err := writeState(l, w, id, parent, now); err != nil {
			return fmt.Errorf("window %s: %w", w.Key, err)
		}
		if w.Select {
			selectID = id
		}
		fmt.Fprintf(out, "%s %s\n", w.Key, id)
	}
	if selectID != "" {
		if _, err := t.Run("select-window", "-t", selectID); err != nil {
			return err
		}
	}
	return nil
}

// newWindow opens the window with the fake pane in it, the way
// tmux.LaunchAgent opens a real one (same name, cwd and AGENTCTL env), and
// returns its ID. The session's after-new-window hook gives it a chrome bar.
func newWindow(l Layout, t Tmux, session string, w Window, self string) (string, error) {
	cmd := shellquote.Quote(self) + " pane --spec " + shellquote.Quote(l.PaneSpec(w.Project, w.Key))
	return t.Run("new-window", "-d", "-P", "-F", "#{window_id}",
		"-t", "="+session+":", "-n", w.Instance, "-c", WorkDir(l, w),
		"-e", "AGENTCTL=1",
		"-e", "AGENTCTL_RUNTIME_DIR="+l.RuntimeDir(w.Project),
		"-e", "AGENTCTL_AGENT_NAME="+w.Agent,
		"-e", "AGENTCTL_SESSION_ID="+WindowSessionID(w),
		cmd)
}

// Parent is a dispatched window's dispatcher.
type Parent struct{ WindowID, SessionID string }

// writeState writes the window's record through windowstate in two
// transactions: the first sets everything the hooks and launcher would have,
// the second backdates StateSince (Transact stamps it whenever State changes).
func writeState(l Layout, w Window, id string, parent Parent, now time.Time) error {
	// The pane spec holds the transcript WriteWorld resolved.
	spec, err := ReadPaneSpec(l.PaneSpec(w.Project, w.Key))
	if err != nil {
		return err
	}
	t := &spec.Transcript
	since := now.Add(-w.SinceDuration())
	err = windowstate.Update(id, func(ws *windowstate.WindowState) {
		applyWindow(ws, l, w, t, parent, since)
		winname.SyncName(id, ws)
	})
	if err != nil {
		return fmt.Errorf("writing state: %w", err)
	}
	return windowstate.Update(id, func(ws *windowstate.WindowState) {
		ws.StateSince = since
		if w.State == "blocked" {
			ws.BlockedSince = since
		}
	})
}

// applyWindow sets the record's fields from the scene window.
func applyWindow(ws *windowstate.WindowState, l Layout, w Window, t *Transcript, parent Parent, since time.Time) {
	ws.Instance = w.Instance
	ws.AgentName = w.Agent
	ws.Mode = modeOf(w)
	ws.SessionID = WindowSessionID(w)
	ws.LogPath = LogPath(l, w)
	ws.WorkDir = WorkDir(l, w)
	ws.SessionName = fmt.Sprintf("%s-%s-%s", w.Agent, w.Project, w.Key)
	ws.SessionNameVerified = true
	ws.Model = w.Model
	ws.CostUSD = w.CostUSD
	if w.CtxPct > 0 {
		ws.CtxPct = w.CtxPct
		ws.CtxPctSource = "statusline"
		ws.CtxPushedAt = time.Now()
		ws.ContextWindowSize = 200000
		ws.ContextTokens = 200000 * w.CtxPct / 100
	}
	ws.TurnCount = w.Turns
	ws.LastText = t.LastText()
	ws.LastToolDetail = w.LastTool
	if ws.LastToolDetail == "" {
		ws.LastToolDetail = t.LastTool()
	}
	if ws.LastToolDetail != "" {
		ws.LastToolTime = since.Unix()
	}
	if w.Title != "" {
		kind := w.Kind
		if w.Mode == "review-pr" && kind == "" {
			kind = "review"
		}
		ws.ShortTitle = windowstate.ShortTitle{Title: w.Title, Kind: kind, At: since, Final: true}
	}
	if w.Mode != "bare" {
		ws.Branch = w.Branch
	}
	if w.HasWorktree() {
		ws.WorktreeDir = WorkDir(l, w)
	}
	if w.Ticket > 0 {
		ws.Ticket = fmt.Sprintf("#%d", w.Ticket)
		ws.TicketTitle = w.TicketTitle
	}
	ws.Task = w.Task
	if w.ReviewPR > 0 && ws.Task == "" {
		ws.Task = fmt.Sprintf("Review PR #%d", w.ReviewPR)
	}
	if w.PR > 0 {
		ws.PRNumber = w.PR
		ws.PRURL = PRURL(w.Project, w.PR)
	}
	ws.SpawnedBy, ws.SpawnedBySession = parent.WindowID, parent.SessionID

	switch w.State {
	case "done":
		ws.State = "idle"
		if w.Mode == "bare" {
			ws.TurnDone = true
		} else {
			ws.Done = true
			ws.CompleteFiredAt = since
		}
	case "blocked":
		ws.State = "blocked"
		ws.PendingTool = w.PendingTool
		ws.PendingToolDetail = w.PendingDetail
		ws.PendingToolAt = since
	default:
		ws.State = w.State
	}
	ws.Seen = w.IsSeen()
}

// modeOf is the windowstate mode string of a scene mode.
func modeOf(w Window) string {
	switch w.Mode {
	case "dispatched":
		return "dispatch"
	case "adhoc":
		return "adhoc"
	case "review-pr":
		return "review-pr"
	}
	return "bare"
}
