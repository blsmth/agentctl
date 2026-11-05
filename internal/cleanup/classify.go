// Package cleanup implements the classification and reclaim logic for
// `agentctl cleanup` -- reclaiming dispatch worktrees and windows left
// behind by finished agents.
//
// Classify is deliberately a pure function over Facts: it has no git, gh, or
// tmux dependency, so the safety-critical decision of "is this reclaimable"
// can be tested exhaustively without a live repo or session. Everything that
// actually shells out (git-*.go, gh.go, candidate.go's Gather) exists only to
// gather Facts and to act on a Verdict once decided.
package cleanup

import "fmt"

// PRState describes what gh reported for a branch's associated pull request.
type PRState int

const (
	// PRUnknown means gh failed, was unavailable, or produced output that
	// could not be parsed. It is never treated as a green light to reclaim --
	// a missing or broken gh must never cause a deletion.
	PRUnknown PRState = iota
	// PRNone means gh succeeded and found no PR at all for the branch.
	PRNone
	PROpen
	PRMerged
	PRClosed
)

// String returns a lowercase label for the PR state, for reports and logs.
func (s PRState) String() string {
	switch s {
	case PRNone:
		return "none"
	case PROpen:
		return "open"
	case PRMerged:
		return "merged"
	case PRClosed:
		return "closed"
	default:
		return "unknown"
	}
}

// WindowFacts describes the tmux window (if any) associated with a worktree,
// reduced to exactly what Classify needs. It intentionally carries no
// tmux/windowstate types so classify_test.go needs neither package.
type WindowFacts struct {
	// Live is true when a tmux window is currently associated with this
	// worktree. When false, none of the other fields apply -- a window that
	// isn't running can't be "working", "the shell", or "self".
	Live bool
	// Status mirrors state.AgentStatus.String(): "working", "blocked", or
	// "idle". Only meaningful when Live.
	Status string
	// Mode mirrors windowstate.WindowState.Mode ("dispatch", "adhoc", "bare",
	// or ""). Only meaningful when Live.
	Mode string
	// IsSelf is true when this window is the one the cleanup command itself
	// is running in.
	IsSelf bool
	// IsShell is true when this window is the special "shell" window, which
	// is never a dispatched agent.
	IsShell bool
}

// Facts is every observed fact Classify needs to decide a single worktree's
// fate. Callers (candidate.go's Gather) are responsible for gathering these
// from git, gh, and tmux; Classify itself never shells out to anything.
type Facts struct {
	// Dirty is true when the worktree has any uncommitted changes, tracked
	// or untracked, staged or not.
	Dirty bool
	// UnpushedCount is the number of commits on HEAD not present on its
	// upstream. Zero (with HasUpstream true) means fully pushed.
	UnpushedCount int
	// HasUpstream is true when the branch has a configured remote-tracking
	// branch at all.
	HasUpstream bool
	// AheadOfBase is the number of commits HEAD has beyond the resolved base
	// ref the branch was cut from.
	AheadOfBase int
	// MergedIntoBase is true when HEAD is an ancestor of the remote default
	// branch, i.e. every commit on this branch already landed there --
	// independent of whether a PR record exists at all (e.g. a manually
	// merged or fast-forwarded branch).
	MergedIntoBase bool
	// PR is the branch's most recent pull request state, as reported by gh.
	PR PRState
	// Window is the associated tmux window's facts, if any.
	Window WindowFacts
	// IncludeOpenPRs mirrors the --include-open-prs CLI flag: whether a
	// worktree whose branch has an open PR may be reclaimed (worktree only;
	// the branch is always preserved in that case).
	IncludeOpenPRs bool
}

// Disposition is Classify's verdict for a single worktree.
type Disposition string

const (
	DispositionSkip   Disposition = "skip"
	DispositionLanded Disposition = "landed"
	DispositionEmpty  Disposition = "empty"
	DispositionOpenPR Disposition = "open-pr"
)

// Reclaimable reports whether d is ever actioned by Reclaim. DispositionSkip
// is the only disposition that is not -- and that must remain true
// regardless of any "act without asking" flag the CLI offers: Classify never
// takes such a flag as input, so nothing about how the caller invokes
// cleanup can turn a Skip into a reclaim.
func (d Disposition) Reclaimable() bool {
	return d == DispositionLanded || d == DispositionEmpty || d == DispositionOpenPR
}

// Verdict is Classify's full result: what to do, why, and whether the branch
// must be preserved even on a full reclaim.
type Verdict struct {
	Disposition Disposition
	Reason      string
	// PreserveBranch is true only for DispositionOpenPR: the worktree is
	// removed but the branch is kept, because the open PR still needs it.
	PreserveBranch bool
}

// Classify decides a single worktree's disposition from Facts alone.
//
// Order matters and is deliberate:
//  1. Hard safety rails (self window, shell window, bare mode, working/
//     blocked) -- unconditional, never reclaimable no matter what git/gh
//     reports.
//  2. Never-touch skips (dirty tree, unpushed commits, no-upstream-with-
//     commits) -- destroying uncommitted or unpushed agent work is never a
//     tradeoff worth offering, so there is no override for these.
//  3. Reclaimable classifications: empty (zero commits over base), landed
//     (PR merged/closed, or fully merged into base independent of any PR
//     record), open-pr (gated behind IncludeOpenPRs, branch always
//     preserved).
//  4. Anything left over (no PR found, or gh's answer was unknown) refuses
//     to guess and skips.
func Classify(f Facts) Verdict {
	if f.Window.Live && f.Window.IsSelf {
		return Verdict{DispositionSkip, "associated window is the one this command is running in", false}
	}
	if f.Window.Live && f.Window.IsShell {
		return Verdict{DispositionSkip, "associated window is the shell window, never a dispatched agent", false}
	}
	if f.Window.Live && f.Window.Mode == "bare" {
		return Verdict{DispositionSkip, "associated window is a bare (interactive human) session, not a dispatched agent", false}
	}
	if f.Window.Live && (f.Window.Status == "working" || f.Window.Status == "blocked") {
		return Verdict{DispositionSkip, fmt.Sprintf("associated window is %s", f.Window.Status), false}
	}

	if f.Dirty {
		return Verdict{DispositionSkip, "worktree has uncommitted changes (dirty tree)", false}
	}
	if f.UnpushedCount > 0 {
		return Verdict{DispositionSkip, fmt.Sprintf("branch has %d unpushed commit(s)", f.UnpushedCount), false}
	}
	if !f.HasUpstream && f.AheadOfBase > 0 {
		return Verdict{DispositionSkip, fmt.Sprintf("branch has no upstream and is %d commit(s) ahead of base", f.AheadOfBase), false}
	}

	if f.AheadOfBase == 0 {
		return Verdict{DispositionEmpty, "zero commits relative to base ref (abandoned dispatch)", false}
	}

	if f.MergedIntoBase || f.PR == PRMerged || f.PR == PRClosed {
		reason := "branch is fully merged into the remote default branch"
		switch f.PR {
		case PRMerged:
			reason = "PR is merged"
		case PRClosed:
			reason = "PR is closed"
		}
		return Verdict{DispositionLanded, reason, false}
	}

	if f.PR == PROpen {
		if !f.IncludeOpenPRs {
			return Verdict{DispositionSkip, "PR is open (pass --include-open-prs to reclaim the worktree; the branch is preserved even then)", false}
		}
		return Verdict{DispositionOpenPR, "PR is open -- worktree reclaimed but branch preserved for continued review", true}
	}

	if f.PR == PRUnknown {
		return Verdict{DispositionSkip, "gh PR lookup failed or was unavailable -- refusing to classify without a definitive answer", false}
	}

	return Verdict{DispositionSkip, "no PR found and branch is not merged into base -- nothing to safely reclaim", false}
}
