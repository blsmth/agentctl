package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"

	"github.com/blsmth/agentctl/internal/cleanup"
	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/mailbox"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/reconcile"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/sweeprun"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/tui"
	"github.com/blsmth/agentctl/internal/windowstate"
	"github.com/blsmth/agentctl/internal/worktree"
)

// SweepOptions holds the flags for `agentctl sweep`.
type SweepOptions struct {
	Session     string
	DryRun      bool
	IncludeBare bool
	JSON        bool
}

// sweepRow and sweepReport are the JSON report shared with the hub and serve,
// which run sweep as a subprocess (see internal/sweeprun).
type (
	sweepRow    = sweeprun.Row
	sweepReport = sweeprun.Report
)

// errSweepNoTmux is returned when sweep cannot tell which session to scope to.
var errSweepNoTmux = errors.New("not inside tmux ($TMUX_PANE unset): run from an agentctl window or pass --session <name>")

// resolveSweepSession picks the one session sweep may touch: the explicit
// --session override, else the session owning the pane in $TMUX_PANE. It never
// falls back to the global tmux.SessionName or lists other sessions.
func resolveSweepSession(flag, pane string, paneSession func(string) string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	if pane == "" {
		return "", errSweepNoTmux
	}
	name := paneSession(pane)
	if name == "" {
		return "", fmt.Errorf("could not resolve the tmux session of pane %s", pane)
	}
	return name, nil
}

// checkSweepSession fails when session does not exist, so a session that died
// (or a mistyped --session) is an error rather than an empty "closed 0".
func checkSweepSession(session string, exists func(string) bool) error {
	if !exists(session) {
		return fmt.Errorf("no tmux session %q", session)
	}
	return nil
}

// sweepDeps are the side-effecting operations sweep performs, injected so the
// walk can be tested without tmux, git, or gh.
type sweepDeps struct {
	Windows      func() []tmux.WindowInfo
	States       func() map[string]windowstate.WindowState
	ReadState    func(id string) (windowstate.WindowState, bool)
	Capture      func(id string) string
	Mail         func(id string) int
	Agent        func(id string) (agent, activity string)
	DirExists    func(path string) bool
	WorktreeInfo func(ws windowstate.WindowState) (cleanup.Facts, cleanup.PRInfo, string)
	BranchPR     func(branch string) cleanup.PRInfo
	CloseWindow  func(id string) error
	Reclaim      func(c *cleanup.Candidate)
	RemoveMail   func(id string) error
	// LiveChildren counts live windows (any state) dispatched by the
	// window with this ID and session ID (any tmux session).
	LiveChildren func(id, sessionID string) int
}

func paneVerdict(text string) string {
	switch fleet.ClassifyPane(text) {
	case fleet.PaneAwaitingInput:
		return "awaiting"
	case fleet.PaneBusy:
		return "busy"
	case fleet.PaneIdle:
		return "idle"
	}
	return ""
}

// applyDynamic refreshes the fields that can change while sweep runs.
func (d sweepDeps) applyDynamic(f *cleanup.SweepFacts, ws windowstate.WindowState) {
	f.State, f.Done, f.Mode = ws.State, ws.Done, ws.Mode
	f.PendingTool, f.PendingToolDetail = ws.PendingTool, ws.PendingToolDetail
	f.TurnCount = ws.TurnCount
}

func (d sweepDeps) refresh(f *cleanup.SweepFacts, id string) bool {
	ws, ok := d.ReadState(id)
	if !ok {
		return false
	}
	d.applyDynamic(f, ws)
	f.Pane = paneVerdict(d.Capture(id))
	f.PendingMail = d.Mail(id)
	if f.IncludeBare && ws.Mode == "bare" && d.LiveChildren != nil {
		f.LiveChildren = d.LiveChildren(id, ws.SessionID)
	}
	return true
}

// runSweep walks windows, classifies each, and (unless dryRun) closes the
// ones that are finished. selfID is excluded unconditionally.
func runSweep(d sweepDeps, session, selfID string, opts SweepOptions) sweepReport {
	rep := sweepReport{Session: session, DryRun: opts.DryRun,
		Closed: []sweepRow{}, Attention: []sweepRow{}, InProgress: []sweepRow{}, Skipped: []sweepRow{}}
	states := d.States()

	for _, w := range d.Windows() {
		ws, tracked := states[w.ID]
		agentName, activity := d.Agent(w.ID)
		if agentName == "" {
			agentName = w.Name
		}
		row := sweepRow{WindowID: w.ID, Name: w.Name, Agent: agentName}

		f := cleanup.SweepFacts{
			IsSelf:      w.ID == selfID,
			NonAgent:    fleet.IsNonAgentWindow(w.Name),
			Tracked:     tracked,
			IncludeBare: opts.IncludeBare,
			Activity:    activity,
			Branch:      ws.Branch,
		}
		if tracked && !f.IsSelf && !f.NonAgent {
			d.refresh(&f, w.ID)
		}
		var repoDir string
		var candidate cleanup.Candidate
		if tracked && !f.IsSelf && !f.NonAgent && (ws.Done || (opts.IncludeBare && ws.Mode == "bare")) && f.PendingMail == 0 {
			// Only gather git/gh facts for windows that could actually close.
			if ws.WorktreeDir != "" && d.DirExists(ws.WorktreeDir) {
				f.HasWorktree = true
				f.WT, f.WTPR, repoDir = d.WorktreeInfo(ws)
			} else if ws.Branch != "" {
				f.BranchPR = d.BranchPR(ws.Branch)
			}
		}

		v := cleanup.ClassifyWindow(f)
		row.Status = v.Reason
		switch v.Kind {
		case cleanup.SweepSkip:
			rep.Skipped = append(rep.Skipped, row)
		case cleanup.SweepWorking:
			rep.InProgress = append(rep.InProgress, row)
		case cleanup.SweepAction:
			row.Detail = v.Action
			rep.Attention = append(rep.Attention, row)
		case cleanup.SweepClose:
			row.Detail = v.Reason
			if opts.DryRun {
				rep.Closed = append(rep.Closed, row)
				continue
			}
			// Re-check right before killing: state may have flipped.
			if !d.refresh(&f, w.ID) {
				row.Status, row.Detail = "unknown", "window state disappeared before close"
				rep.Attention = append(rep.Attention, row)
				continue
			}
			if v2 := cleanup.ClassifyWindow(f); v2.Kind != cleanup.SweepClose {
				row.Status = v2.Reason
				if v2.Kind == cleanup.SweepWorking {
					rep.InProgress = append(rep.InProgress, row)
				} else {
					row.Detail = v2.Action
					rep.Attention = append(rep.Attention, row)
				}
				continue
			}
			if v.Worktree {
				candidate = cleanup.Candidate{
					WorktreePath: ws.WorktreeDir, Branch: ws.Branch, RepoDir: repoDir,
					WindowID: w.ID, Disposition: v.Disposition, PreserveBranch: v.PreserveBranch,
				}
				d.Reclaim(&candidate)
				row.Errors = candidate.Errors
			} else {
				if err := d.CloseWindow(w.ID); err != nil {
					row.Errors = append(row.Errors, err.Error())
				}
				// The record is left for the reconcile spawned by the kill,
				// which claims it and fires the Gone handlers once.
				if err := d.RemoveMail(w.ID); err != nil {
					row.Errors = append(row.Errors, "remove mailbox: "+err.Error())
				}
			}
			rep.Closed = append(rep.Closed, row)
		}
	}
	return rep
}

// countLiveChildren counts live windows in any session that reconcile.IsChild
// matches (the close_children cascade would kill each, whatever its state). It
// needs a non-empty sessionID (spawned_by alone is untrustworthy: @N is
// recycled), matching reconcile's close_children rule. A failed listing counts
// as one child so the window is kept.
func countLiveChildren(list func() ([]reconcile.LiveWindow, error), id, sessionID string) int {
	if sessionID == "" {
		return 0
	}
	live, err := list()
	if err != nil {
		return 1
	}
	n := 0
	for _, w := range live {
		if reconcile.IsChild(w, id, sessionID) {
			n++
		}
	}
	return n
}

// RunSweep implements `agentctl sweep`.
func RunSweep(opts SweepOptions) error {
	pane := os.Getenv("TMUX_PANE")
	session, err := resolveSweepSession(opts.Session, pane, tmux.SessionNameForPane)
	if err != nil {
		return err
	}
	if err := checkSweepSession(session, tmux.HasSessionNamed); err != nil {
		return err
	}
	// Self is resolved from the pane first; AGENTCTL_SESSION_ID can be inherited.
	selfID := tmux.WindowIDForPane(pane)

	// KillWindow and cleanup.Reclaim target the package-global session; pin
	// it to the one sweep is scoped to.
	tmux.SessionName = session

	cwd, _ := os.Getwd()
	cwdRoot, _ := worktree.Toplevel(cwd)
	built, _ := fleet.LoadAgents(session, paths.RuntimeDir())
	byWindow := make(map[string]*state.AgentState, len(built))
	for i := range built {
		byWindow[built[i].WindowID] = &built[i]
	}

	d := sweepDeps{
		Windows: func() []tmux.WindowInfo { return tmux.ListWindowsIn(session) },
		States:  windowstate.All,
		ReadState: func(id string) (windowstate.WindowState, bool) {
			ws, err := windowstate.Read(id)
			return ws, err == nil
		},
		Capture: func(id string) string {
			text, _ := tmux.CapturePane(session, id, 60)
			return text
		},
		Mail: func(id string) int { n, _ := mailbox.PendingCount(id); return n },
		Agent: func(id string) (string, string) {
			a := byWindow[id]
			if a == nil {
				return "", ""
			}
			name := a.AgentName
			if name == "" {
				name = a.Name
			}
			return name, fleet.ActivityLine(fleet.Row{AgentState: *a})
		},
		DirExists: func(p string) bool { st, err := os.Stat(p); return err == nil && st.IsDir() },
		WorktreeInfo: func(ws windowstate.WindowState) (cleanup.Facts, cleanup.PRInfo, string) {
			repoDir := cwdRoot
			if filepath.Base(filepath.Dir(ws.WorktreeDir)) == ".worktrees" {
				repoDir = filepath.Dir(filepath.Dir(ws.WorktreeDir))
			}
			slug := tui.TLRepoSlug(repoDir)
			facts := cleanup.WorktreeFacts(repoDir, slug, ws.WorktreeDir, ws.Branch)
			var pr cleanup.PRInfo
			if facts.PR == cleanup.PROpen {
				pr = cleanup.LookupPR(slug, ws.Branch)
			}
			return facts, pr, repoDir
		},
		BranchPR: func(branch string) cleanup.PRInfo {
			if cwdRoot == "" {
				return cleanup.PRInfo{State: cleanup.PRUnknown}
			}
			return cleanup.LookupPR(tui.TLRepoSlug(cwdRoot), branch)
		},
		CloseWindow: tmux.KillWindow,
		Reclaim:     cleanup.Reclaim,
		RemoveMail:  mailbox.RemoveInbox,
		LiveChildren: func(id, sessionID string) int {
			return countLiveChildren(reconcile.ListLiveWindows, id, sessionID)
		},
	}

	rep := runSweep(d, session, selfID, opts)
	printSweepReport(rep, opts.JSON)
	return nil
}

func printSweepReport(rep sweepReport, jsonOut bool) {
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(rep) //nolint:errcheck
		return
	}
	muted := cliMuted()
	verb := "Closed"
	if rep.DryRun {
		verb = "Would close"
	}
	if len(rep.Closed) > 0 {
		fmt.Println(cliHeaderStyle().Render(verb))
		sort.SliceStable(rep.Closed, func(i, j int) bool { return rep.Closed[i].WindowID < rep.Closed[j].WindowID })
		for _, r := range rep.Closed {
			line := fmt.Sprintf("  %s  %s  %s", linkWindow(r.WindowID, r.WindowID), r.Name, muted.Render("("+r.Detail+")"))
			if len(r.Errors) > 0 {
				line += "  FAILED: " + strings.Join(r.Errors, "; ")
			}
			fmt.Println(line)
		}
		fmt.Println()
	}
	if len(rep.Attention) > 0 {
		fmt.Println(cliHeaderStyle().Render("Needs attention"))
		t := table.New().
			Border(lipgloss.RoundedBorder()).
			BorderStyle(muted).
			Headers("WINDOW", "NAME", "AGENT", "STATUS", "ACTION").
			StyleFunc(func(row, _ int) lipgloss.Style {
				if row == table.HeaderRow {
					return cliHeaderStyle()
				}
				return lipgloss.NewStyle()
			})
		for _, r := range rep.Attention {
			t.Row(linkWindow(r.WindowID, r.WindowID), r.Name, r.Agent, r.Status, r.Detail)
		}
		fmt.Println(t.Render())
		fmt.Println()
	}
	if len(rep.Skipped) > 0 {
		names := make([]string, 0, len(rep.Skipped))
		for _, r := range rep.Skipped {
			names = append(names, r.Name+" ("+r.Status+")")
		}
		fmt.Println(muted.Render("Skipped: " + strings.Join(names, ", ")))
	}
	fmt.Printf("%s %d, need attention %d, in progress %d\n",
		strings.ToLower(verb), len(rep.Closed), len(rep.Attention), len(rep.InProgress))
}
