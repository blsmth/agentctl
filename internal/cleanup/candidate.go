package cleanup

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/mailbox"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/windowstate"
	"github.com/blsmth/agentctl/internal/worktree"
)

// Options configures a cleanup run.
type Options struct {
	// RepoDir is the root of the git repository whose .worktrees/ directory
	// is being evaluated.
	RepoDir string
	// RepoSlug is "owner/repo", used for gh PR lookups. Empty disables PR
	// lookups entirely -- every candidate's PR state is PRUnknown, which
	// Classify treats as unreclaimable except for "empty" (which never needs
	// a PR answer at all).
	RepoSlug string
	// SelfWindowID is the tmux window ID the cleanup command itself is
	// running in. Never a candidate for reclaim.
	SelfWindowID string
	// IncludeOpenPRs enables reclaiming (worktree only, branch preserved)
	// worktrees whose branch has an open PR.
	IncludeOpenPRs bool
}

// Candidate is one worktree evaluated for cleanup, plus -- once Reclaim has
// run -- the outcome of acting on it.
type Candidate struct {
	WorktreePath string
	Branch       string
	// RepoDir is the main repository directory, needed to delete the branch
	// after the worktree itself has been removed.
	RepoDir        string
	WindowID       string // "" if no window could be associated
	Disposition    Disposition
	Reason         string
	PreserveBranch bool

	// Executed is true once Reclaim has run for this candidate (regardless
	// of whether every step succeeded).
	Executed  bool
	StepsDone []string
	Errors    []string
}

// ActionText renders a one-line human-readable description of what will
// happen (before Reclaim runs) or what did happen (after), suitable for a
// report table's Action column.
func (c Candidate) ActionText() string {
	if c.Disposition == DispositionSkip {
		return "skip: " + c.Reason
	}
	if !c.Executed {
		return "plan: " + strings.Join(plannedSteps(c), "; ")
	}
	parts := append([]string{}, c.StepsDone...)
	for _, e := range c.Errors {
		parts = append(parts, "FAILED "+e)
	}
	if len(parts) == 0 {
		return "(no action taken)"
	}
	return strings.Join(parts, "; ")
}

func plannedSteps(c Candidate) []string {
	steps := []string{"kill window (if live)", "remove worktree"}
	if c.PreserveBranch {
		steps = append(steps, "preserve branch")
	} else {
		steps = append(steps, "delete branch")
	}
	steps = append(steps, "remove window state", "remove mailbox")
	return steps
}

// Reclaimable returns pointers into candidates for every entry whose
// Disposition is reclaimable, so Reclaim can mutate them in place and a
// later render of the same candidates slice reflects the outcome.
func Reclaimable(candidates []Candidate) []*Candidate {
	var out []*Candidate
	for i := range candidates {
		if candidates[i].Disposition.Reclaimable() {
			out = append(out, &candidates[i])
		}
	}
	return out
}

// Gather enumerates every worktree under <RepoDir>/.worktrees, classifies
// each one, and returns the full report. It performs no destructive action --
// see Reclaim for that.
func Gather(opts Options) ([]Candidate, error) {
	worktrees, err := worktree.List(opts.RepoDir)
	if err != nil {
		return nil, fmt.Errorf("listing worktrees: %w", err)
	}

	worktreesRoot := filepath.Join(opts.RepoDir, ".worktrees")
	windowStates := windowstate.All()
	liveWindows := make(map[string]string) // windowID -> window name
	windowPaths := make(map[string]string) // windowID -> pane_current_path
	for _, w := range tmux.ListWindows() {
		liveWindows[w.ID] = w.Name
		windowPaths[w.ID] = w.Path
	}

	baseRef := worktree.ResolveBaseRef(opts.RepoDir)

	var candidates []Candidate
	for _, wt := range worktrees {
		if !underWorktreesRoot(worktreesRoot, wt.Path) {
			continue
		}

		windowID := matchWindow(wt, windowStates, windowPaths)
		wf := buildWindowFacts(windowID, windowStates, liveWindows, opts.SelfWindowID)

		facts := Facts{
			Window:         wf,
			IncludeOpenPRs: opts.IncludeOpenPRs,
		}
		gatherGitFacts(&facts, wt.Path, wt.Branch, opts.RepoSlug, baseRef)

		verdict := Classify(facts)

		candidates = append(candidates, Candidate{
			WorktreePath:   wt.Path,
			Branch:         wt.Branch,
			RepoDir:        opts.RepoDir,
			WindowID:       windowID,
			Disposition:    verdict.Disposition,
			Reason:         verdict.Reason,
			PreserveBranch: verdict.PreserveBranch,
		})
	}

	candidates = append(candidates, orphanDirectoryCandidates(worktreesRoot, worktrees)...)

	return candidates, nil
}

// orphanDirectoryCandidates reports every subdirectory of worktreesRoot that
// `git worktree list` does not know about at all -- e.g. a directory copied
// or extracted there by hand, or left behind by tooling that predates this
// package. These are surfaced for visibility only: DispositionSkip is never
// reclaimable (see Disposition.Reclaimable), so nothing here is ever deleted
// automatically.
//
// If worktreesRoot does not exist yet, this contributes nothing and is not an
// error -- a project that has never dispatched anything has no .worktrees/
// directory at all.
func orphanDirectoryCandidates(worktreesRoot string, worktrees []worktree.WorktreeInfo) []Candidate {
	entries, err := os.ReadDir(worktreesRoot)
	if err != nil {
		return nil
	}

	known := make(map[string]bool, len(worktrees))
	for _, wt := range worktrees {
		known[resolvedClean(wt.Path)] = true
	}

	var out []Candidate
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dirPath := filepath.Join(worktreesRoot, e.Name())
		if known[resolvedClean(dirPath)] {
			continue
		}
		out = append(out, Candidate{
			WorktreePath: dirPath,
			Disposition:  DispositionSkip,
			Reason:       "unregistered directory under .worktrees (not a git worktree)",
		})
	}
	return out
}

// resolvedClean resolves symlinks in path (so comparisons agree with how
// `git worktree list` reports paths, e.g. on macOS where /tmp is a symlink
// into /private/tmp) and falls back to a plain filepath.Clean if the path
// can't be resolved (e.g. it no longer exists).
func resolvedClean(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

// underWorktreesRoot reports whether path is a (strict) subdirectory of
// root, i.e. an actual dispatch worktree rather than the main checkout that
// `worktree.List` always includes as its first entry.
func underWorktreesRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != "." && !strings.HasPrefix(rel, "..")
}

// matchWindow finds the windowID whose state is associated with wt. Signals
// are tried in order, most authoritative first, and the first match wins:
//
//  1. The WorktreeDir field written by dispatch at launch time.
//  2. windowstate files that predate that field are matched on a
//     best-effort basis by comparing their recorded branch's slug to the
//     worktree directory's name.
//  3. windowstate files that predate both fields have no recorded
//     intent at all, so fall back to the window's live pane path: a match if
//     the pane's current directory is the worktree itself or a descendant of
//     it. Recorded intent (1 and 2) always wins over this live signal, so an
//     agent that has since `cd`-ed elsewhere cannot defeat a correctly
//     recorded WorktreeDir/Branch for some other worktree.
func matchWindow(wt worktree.WorktreeInfo, states map[string]windowstate.WindowState, windowPaths map[string]string) string {
	cleanPath := filepath.Clean(wt.Path)
	for id, ws := range states {
		if ws.WorktreeDir != "" && filepath.Clean(ws.WorktreeDir) == cleanPath {
			return id
		}
	}
	base := filepath.Base(cleanPath)
	for id, ws := range states {
		if ws.Branch != "" && worktree.Slug(ws.Branch) == base {
			return id
		}
	}
	for id, panePath := range windowPaths {
		if panePath == "" {
			continue
		}
		if isPathOrDescendant(cleanPath, panePath) {
			return id
		}
	}
	return ""
}

// isPathOrDescendant reports whether candidate is exactly root or a strict
// descendant of it. It never matches an ancestor or a sibling that merely
// shares root as a string prefix (e.g. ".worktrees/foo-bar" must not match
// root ".worktrees/foo"), which is why this uses filepath.Rel and rejects any
// result that escapes with "..", rather than a string prefix comparison.
func isPathOrDescendant(root, candidate string) bool {
	rel, err := filepath.Rel(root, filepath.Clean(candidate))
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func buildWindowFacts(windowID string, states map[string]windowstate.WindowState, live map[string]string, selfWindowID string) WindowFacts {
	if windowID == "" {
		return WindowFacts{}
	}
	name, isLive := live[windowID]
	if !isLive {
		return WindowFacts{}
	}
	ws := states[windowID]
	return WindowFacts{
		Live:    true,
		Status:  resolveStatus(ws.State),
		Mode:    ws.Mode,
		IsSelf:  windowID == selfWindowID,
		IsShell: name == "shell",
	}
}

// resolveStatus mirrors the monitor's own hook-state resolution
// (monitor.go's resolveState): the raw windowstate.State string maps to
// working/blocked/idle, defaulting to idle for anything else (no
// authoritative signal yet).
func resolveStatus(hookState string) string {
	switch hookState {
	case "working":
		return "working"
	case "blocked":
		return "blocked"
	default:
		return "idle"
	}
}

// Reclaim performs the destructive steps for a single already-classified
// candidate. It is a caller error to invoke this on a Skip disposition --
// Reclaim refuses and does nothing in that case, as a second line of
// defense on top of the CLI only ever calling this for entries returned by
// Reclaimable.
//
// Steps run in order and continue past individual failures, recording each
// as a StepsDone entry (on success) or an Errors entry (on failure):
//  1. Kill the associated tmux window, if one is live.
//  2. worktree.Remove(path).
//  3. Delete the local branch -- landed/empty only, never open-pr, and only
//     via the safe delete (never forced).
//  4. windowstate.Remove(windowID), if there was an associated window.
//  5. Remove the per-window mailbox directory, if there was an associated
//     window.
func Reclaim(c *Candidate) {
	if !c.Disposition.Reclaimable() {
		logging.Warn("cleanup: Reclaim called on non-reclaimable candidate %s (disposition=%s) -- refusing", c.WorktreePath, c.Disposition)
		return
	}

	c.Executed = true

	if c.WindowID != "" {
		if err := tmux.KillWindow(c.WindowID); err != nil {
			c.Errors = append(c.Errors, fmt.Sprintf("kill window %s: %v", c.WindowID, err))
		} else {
			c.StepsDone = append(c.StepsDone, "killed window "+c.WindowID)
		}
	}

	if err := worktree.Remove(c.WorktreePath); err != nil {
		c.Errors = append(c.Errors, fmt.Sprintf("remove worktree: %v", err))
	} else {
		c.StepsDone = append(c.StepsDone, "removed worktree")
	}

	if c.PreserveBranch {
		c.StepsDone = append(c.StepsDone, "preserved branch")
	} else if c.Branch != "" {
		if err := DeleteLocalBranch(c.RepoDir, c.Branch); err != nil {
			c.Errors = append(c.Errors, fmt.Sprintf("delete branch: %v", err))
		} else {
			c.StepsDone = append(c.StepsDone, "deleted branch")
		}
	}

	if c.WindowID != "" {
		windowstate.Remove(c.WindowID)
		c.StepsDone = append(c.StepsDone, "removed window state")

		if err := mailbox.RemoveInbox(c.WindowID); err != nil {
			c.Errors = append(c.Errors, fmt.Sprintf("remove mailbox: %v", err))
		} else {
			c.StepsDone = append(c.StepsDone, "removed mailbox")
		}
	}
}

// gatherGitFacts fills the git/gh-derived fields of f for the worktree at
// path. An inconclusive probe always resolves to the conservative answer
// (dirty, unpushed, ahead of base, PR unknown).
func gatherGitFacts(f *Facts, path, branch, repoSlug, baseRef string) {
	if dirty, err := IsDirty(path); err != nil {
		// An inconclusive git probe is treated the same as dirty --
		// refusing to reclaim on an unknown answer is always the safe
		// choice.
		logging.Warn("cleanup: IsDirty(%s): %v -- treating as dirty", path, err)
		f.Dirty = true
	} else {
		f.Dirty = dirty
	}

	f.HasUpstream = HasUpstream(path)
	if f.HasUpstream {
		if n, err := UnpushedCount(path); err == nil {
			f.UnpushedCount = n
		} else {
			logging.Warn("cleanup: UnpushedCount(%s): %v -- treating as unpushed", path, err)
			f.UnpushedCount = 1
		}
	}

	if n, err := AheadOfBase(path, baseRef); err == nil {
		f.AheadOfBase = n
	} else {
		logging.Warn("cleanup: AheadOfBase(%s, %s): %v -- treating as ahead of base", path, baseRef, err)
		f.AheadOfBase = 1 // never treat an unknown ahead-count as "empty"
	}
	f.MergedIntoBase = MergedIntoBase(path, baseRef)

	if repoSlug != "" && branch != "" {
		f.PR = LookupPRState(repoSlug, branch)
	} else {
		f.PR = PRUnknown
	}
}

// WorktreeFacts gathers the git/gh Facts for a single worktree (everything
// except the Window and IncludeOpenPRs fields, which the caller owns), for
// callers that start from a window rather than from Gather's worktree walk.
func WorktreeFacts(repoDir, repoSlug, path, branch string) Facts {
	var f Facts
	gatherGitFacts(&f, path, branch, repoSlug, worktree.ResolveBaseRef(repoDir))
	return f
}
