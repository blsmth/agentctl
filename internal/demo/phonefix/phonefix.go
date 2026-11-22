// Package phonefix converts a demo scene into the servetest fixture the phone
// web app is served over, so one scene file drives the terminal screenshots
// and the phone ones. Pane text comes from demo.Render, the same renderer the
// fake terminal panes use, driven by each window's transcript.
//
// It imports both internal/demo and internal/serve/servetest; neither imports
// it, and nothing in the agentctl binary does.
package phonefix

import (
	"fmt"
	"strings"

	"github.com/blsmth/agentctl/internal/demo"
	"github.com/blsmth/agentctl/internal/serve/servetest"
)

// PaneWidth is the column count panes are drawn at: the size the fixture
// reports for every window (servetest panes.Size).
const PaneWidth = 80

// Ref locates a scene window in the fixture.
type Ref struct {
	Key      string `json:"key"`
	Project  string `json:"project"`
	Session  string `json:"session"`
	WindowID string `json:"window_id"`
	Agent    string `json:"agent"`
	// Approval marks a window whose scene sets approval: its command is
	// held by agentctl's approval gate, so it has a remote approval open.
	// A plain blocked window shows Claude Code's own dialog instead, with no
	// remote approval (the two never appear together).
	Approval bool `json:"approval,omitempty"`
	// Command is the approval's command; empty otherwise.
	Command string `json:"command,omitempty"`
}

// Session is the fixture session name of a project.
func Session(project string) string { return "agentctl-" + project }

// Convert turns the scene's projects into fixture groups and its windows into
// fixture windows. fixtures is the transcript fixture directory.
func Convert(s *demo.Scene, fixtures string) ([]servetest.Group, []Ref, error) {
	var groups []servetest.Group
	var refs []Ref
	ids := map[string]string{} // scene key -> window ID
	for _, p := range s.Projects {
		g := servetest.Group{Session: Session(p.Name), Project: p.Name, Dir: "/home/demo/code/" + p.Name}
		for i, w := range s.WindowsIn(p.Name) {
			id := fmt.Sprintf("@%d", len(refs)+11)
			ids[w.Key] = id
			fw, err := window(w, id, i+1, ids, fixtures)
			if err != nil {
				return nil, nil, fmt.Errorf("window %s: %w", w.Key, err)
			}
			g.Windows = append(g.Windows, fw)
			ref := Ref{Key: w.Key, Project: p.Name, Session: g.Session, WindowID: id, Agent: w.Agent}
			if w.Approval != "" {
				ref.Approval, ref.Command = true, w.Approval
			}
			refs = append(refs, ref)
		}
		groups = append(groups, g)
	}
	return groups, refs, nil
}

// pendingCommand is what a blocked window asks permission for.
func pendingCommand(w demo.Window) string {
	if w.PendingDetail != "" {
		return w.PendingDetail
	}
	if w.Permission != nil && w.Permission.Command != "" {
		return w.Permission.Command
	}
	return w.PendingTool
}

func window(w demo.Window, id string, index int, ids map[string]string, fixtures string) (servetest.Window, error) {
	t, err := w.Transcript.Resolve(fixtures)
	if err != nil {
		return servetest.Window{}, err
	}
	fw := servetest.Window{
		ID: id, Index: index, Agent: w.Instance, State: w.State,
		Mode: phoneMode(w.Mode), Branch: branch(w),
		ContextPct: w.CtxPct, Model: w.Model, CostUSD: w.CostUSD,
		Title: w.TicketTitle, Issue: w.Ticket, Task: task(w),
		ShortTitle: w.Title, ShortKind: kind(w),
		SinceSec: int(w.SinceDuration().Seconds()),
	}
	if w.Mode != "bare" {
		fw.Prompt = firstUser(t)
		fw.Parent = ids[w.SpawnedBy]
	}
	switch w.State {
	case "done":
		fw.State = "idle"
		if w.Mode == "bare" {
			fw.TurnDone = !w.IsSeen()
		} else {
			fw.Done = true
		}
	case "blocked":
		fw.PendingTool, fw.PendingDetail = w.PendingTool, pendingCommand(w)
	}
	if w.Approval != "" {
		// Mid-hook: blocked on agentctl's gate, no Claude dialog yet.
		fw.State, fw.Done, fw.PendingTool, fw.PendingDetail = "blocked", false, "Bash", w.Approval
	}
	fw.Activity = w.LastTool
	if fw.Activity == "" {
		fw.Activity = t.LastTool()
	}
	if fw.Activity == "" {
		fw.Activity = t.LastText()
	}
	if w.PR > 0 {
		fw.PRNumber, fw.PRURL = w.PR, demo.PRURL(w.Project, w.PR)
	}
	fw.Recap, fw.RecapSource = recap(w, t)
	fw.PaneText = pane(w, t)
	return fw, nil
}

// pane draws the window's screen with the terminal demo's renderer.
func pane(w demo.Window, t *demo.Transcript) string {
	dir := "~/code/" + w.Project
	if w.HasWorktree() {
		dir += "/.worktrees/" + strings.ReplaceAll(w.Branch, "/", "-")
	}
	var extra []string
	if w.Approval != "" {
		// Drawn as the pane does between Enter and the popup's answer.
		w.State, w.Spinner, w.Permission = "idle", "", nil
		extra = demo.ApprovalWaitingLines(w.Approval)
	}
	spec := demo.PaneSpec{Window: w, Transcript: *t, WorkDir: dir, Branch: w.Branch}
	return strings.ReplaceAll(demo.Render(spec, PaneWidth, "", extra), "\r\n", "\n") + "\n"
}

// phoneMode is the windowstate mode string of a scene mode.
func phoneMode(m string) string {
	if m == "dispatched" {
		return "dispatch"
	}
	return m
}

// branch is the window's own branch; bare windows record none.
func branch(w demo.Window) string {
	if w.Mode == "bare" {
		return ""
	}
	return w.Branch
}

func task(w demo.Window) string {
	if w.Task == "" && w.ReviewPR > 0 {
		return fmt.Sprintf("Review PR #%d", w.ReviewPR)
	}
	return w.Task
}

func kind(w demo.Window) string {
	if w.Kind == "" && w.Mode == "review-pr" && w.Title != "" {
		return "review"
	}
	return w.Kind
}

func firstUser(t *demo.Transcript) string {
	for _, e := range t.Entries {
		if e.User != "" {
			return e.User
		}
	}
	return ""
}

// recap mirrors fleet's recap precedence: the transcript's away summary, the
// ticket or task line, the transcript title, then the last activity.
func recap(w demo.Window, t *demo.Transcript) (text, source string) {
	switch {
	case t.Recap != "":
		return t.Recap, "summary"
	case w.Ticket > 0 && w.TicketTitle != "":
		return fmt.Sprintf("#%d %s", w.Ticket, w.TicketTitle), "task"
	case task(w) != "":
		return task(w), "task"
	case t.Title != "":
		return t.Title, "title"
	}
	return "", ""
}
