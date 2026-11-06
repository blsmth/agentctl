package cleanup

import "fmt"

// SweepKind is ClassifyWindow's verdict category for one tmux window.
type SweepKind string

const (
	// SweepClose means the window is finished with nothing left to do.
	SweepClose SweepKind = "close"
	// SweepAction means the window needs the user.
	SweepAction SweepKind = "action"
	// SweepWorking means the window is mid-turn; leave it alone.
	SweepWorking SweepKind = "working"
	// SweepSkip means the window is never swept (self, shell, improve~*,
	// untracked).
	SweepSkip SweepKind = "skip"
)

// SweepFacts is every observed fact ClassifyWindow needs. Like Facts, it
// carries no tmux/windowstate types; the caller gathers it.
type SweepFacts struct {
	// Window identity and exclusions.
	IsSelf      bool // resolved from TMUX_PANE, never from env session IDs
	NonAgent    bool // shell window or improve~*
	Tracked     bool // a windowstate record exists
	IncludeBare bool

	// Record state.
	State             string // "working", "blocked", "idle", "" (record)
	Done              bool
	Mode              string
	PendingTool       string
	PendingToolDetail string

	// Pane is what the pane text says: "awaiting", "busy", "idle", or ""
	// (unknown / unreadable). A definite pane verdict beats the record.
	Pane string

	PendingMail int
	Activity    string // last activity line, for "waiting for input"

	// Worktree linkage. HasWorktree is true only when the recorded
	// worktree directory still exists; WT then holds its git/gh facts and
	// WTPR the open PR's number/URL (when WT.PR == PROpen).
	HasWorktree bool
	WT          Facts
	WTPR        PRInfo

	// Branch is the recorded branch; BranchPR is its PR lookup, used only
	// when there is no worktree.
	Branch   string
	BranchPR PRInfo
}

// SweepVerdict is ClassifyWindow's result.
type SweepVerdict struct {
	Kind SweepKind
	// Reason is why a window closes ("PR merged") or the status shown for
	// actions/working windows ("blocked", "idle").
	Reason string
	// Action is what the user must do. Set only for SweepAction.
	Action string
	// Worktree is true when closing must go through Reclaim (the window owns
	// a worktree); PreserveBranch mirrors Verdict.PreserveBranch.
	Worktree       bool
	Disposition    Disposition // Classify's disposition when Worktree
	PreserveBranch bool
}

// effectiveState resolves the window's real status: a definite pane verdict
// beats the record.
func effectiveState(f SweepFacts) string {
	switch f.Pane {
	case "awaiting":
		return "blocked"
	case "busy":
		return "working"
	case "idle":
		return "idle"
	}
	switch f.State {
	case "working", "blocked":
		return f.State
	}
	return "idle"
}

// ClassifyWindow decides one window's fate from facts alone. It never shells
// out; see Classify, which it reuses for windows that own a worktree.
func ClassifyWindow(f SweepFacts) SweepVerdict {
	if f.IsSelf {
		return SweepVerdict{Kind: SweepSkip, Reason: "this window"}
	}
	if f.NonAgent {
		return SweepVerdict{Kind: SweepSkip, Reason: "not an agent window"}
	}
	if !f.Tracked {
		return SweepVerdict{Kind: SweepSkip, Reason: "untracked"}
	}

	switch effectiveState(f) {
	case "working":
		return SweepVerdict{Kind: SweepWorking, Reason: "working"}
	case "blocked":
		action := "approve/answer prompt"
		if f.PendingTool != "" {
			action += ": " + f.PendingTool
			if f.PendingToolDetail != "" {
				action += " " + f.PendingToolDetail
			}
		}
		return SweepVerdict{Kind: SweepAction, Reason: "blocked", Action: action}
	}

	if f.PendingMail > 0 {
		return SweepVerdict{Kind: SweepAction, Reason: "idle", Action: fmt.Sprintf("%d unread message(s)", f.PendingMail)}
	}

	if f.Mode == "bare" && !f.IncludeBare {
		return SweepVerdict{Kind: SweepAction, Reason: "idle", Action: "idle interactive session"}
	}

	if !f.Done {
		action := "waiting for input"
		if f.Activity != "" {
			action += ": " + f.Activity
		}
		return SweepVerdict{Kind: SweepAction, Reason: "idle", Action: action}
	}

	return classifyDone(f)
}

func classifyDone(f SweepFacts) SweepVerdict {
	if f.HasWorktree {
		wt := f.WT
		wt.Window = WindowFacts{Live: true, Status: "idle"} // never self/shell/bare here
		wt.IncludeOpenPRs = true
		v := Classify(wt)
		switch v.Disposition {
		case DispositionEmpty, DispositionLanded:
			return SweepVerdict{Kind: SweepClose, Reason: closeReason(v), Worktree: true, Disposition: v.Disposition}
		case DispositionOpenPR:
			return SweepVerdict{Kind: SweepAction, Reason: "done", Action: reviewPR(f.WTPR)}
		}
		return SweepVerdict{Kind: SweepAction, Reason: "done", Action: v.Reason}
	}

	if f.Branch != "" {
		switch f.BranchPR.State {
		case PROpen:
			return SweepVerdict{Kind: SweepAction, Reason: "done", Action: reviewPR(f.BranchPR)}
		case PRMerged:
			return SweepVerdict{Kind: SweepClose, Reason: "PR merged"}
		case PRClosed:
			return SweepVerdict{Kind: SweepClose, Reason: "PR closed"}
		case PRNone:
			return SweepVerdict{Kind: SweepClose, Reason: "done, no PR"}
		}
		return SweepVerdict{Kind: SweepAction, Reason: "done", Action: "PR lookup failed for " + f.Branch}
	}

	return SweepVerdict{Kind: SweepClose, Reason: "done, no branch"}
}

func closeReason(v Verdict) string {
	if v.Disposition == DispositionEmpty {
		return "no commits"
	}
	return v.Reason
}

func reviewPR(p PRInfo) string {
	if p.Number == 0 {
		return "review PR"
	}
	return fmt.Sprintf("review PR #%d %s", p.Number, p.URL)
}
