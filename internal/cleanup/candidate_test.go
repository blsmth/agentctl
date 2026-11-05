package cleanup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/windowstate"
	"github.com/blsmth/agentctl/internal/worktree"
)

func TestUnderWorktreesRoot(t *testing.T) {
	root := "/repo/.worktrees"
	tests := []struct {
		path string
		want bool
	}{
		{"/repo/.worktrees/agent-go-engineer-issue-1", true},
		{"/repo/.worktrees", false}, // the root itself, not a subdirectory
		{"/repo", false},            // the main checkout worktree.List always includes
		{"/somewhere/else", false},
	}
	for _, tc := range tests {
		if got := underWorktreesRoot(root, tc.path); got != tc.want {
			t.Errorf("underWorktreesRoot(%q, %q) = %v, want %v", root, tc.path, got, tc.want)
		}
	}
}

func TestMatchWindow_PrimaryWorktreeDirMatch(t *testing.T) {
	wt := worktree.WorktreeInfo{Path: "/repo/.worktrees/agent-go-engineer-issue-42", Branch: "agent/go-engineer/issue-42"}
	states := map[string]windowstate.WindowState{
		"@1": {WorktreeDir: "/repo/.worktrees/agent-go-engineer-issue-42"},
		"@2": {WorktreeDir: "/repo/.worktrees/other"},
	}

	got := matchWindow(wt, states, nil)
	if got != "@1" {
		t.Errorf("matchWindow = %q, want @1", got)
	}
}

func TestMatchWindow_FallbackToBranchSlug(t *testing.T) {
	wt := worktree.WorktreeInfo{Path: "/repo/.worktrees/agent-go-engineer-issue-42", Branch: "agent/go-engineer/issue-42"}
	// No WorktreeDir recorded (predates that field) but Branch is present.
	states := map[string]windowstate.WindowState{
		"@1": {Branch: "agent/go-engineer/issue-42"},
	}

	got := matchWindow(wt, states, nil)
	if got != "@1" {
		t.Errorf("matchWindow fallback = %q, want @1", got)
	}
}

func TestMatchWindow_NoMatch(t *testing.T) {
	wt := worktree.WorktreeInfo{Path: "/repo/.worktrees/agent-go-engineer-issue-42", Branch: "agent/go-engineer/issue-42"}
	states := map[string]windowstate.WindowState{
		"@1": {Branch: "agent/go-engineer/issue-99"},
	}

	got := matchWindow(wt, states, nil)
	if got != "" {
		t.Errorf("matchWindow = %q, want empty (no match)", got)
	}
}

func TestMatchWindow_FallbackToPanePath(t *testing.T) {
	wt := worktree.WorktreeInfo{Path: "/repo/.worktrees/agent-go-engineer-issue-42"}
	// Predates both WorktreeDir and Branch fields entirely.
	states := map[string]windowstate.WindowState{
		"@1": {},
	}
	windowPaths := map[string]string{
		"@1": "/repo/.worktrees/agent-go-engineer-issue-42",
	}

	got := matchWindow(wt, states, windowPaths)
	if got != "@1" {
		t.Errorf("matchWindow pane-path fallback = %q, want @1", got)
	}
}

func TestMatchWindow_FallbackToPanePath_Descendant(t *testing.T) {
	wt := worktree.WorktreeInfo{Path: "/repo/.worktrees/agent-go-engineer-issue-42"}
	states := map[string]windowstate.WindowState{
		"@1": {},
	}
	windowPaths := map[string]string{
		"@1": "/repo/.worktrees/agent-go-engineer-issue-42/subdir",
	}

	got := matchWindow(wt, states, windowPaths)
	if got != "@1" {
		t.Errorf("matchWindow pane-path descendant fallback = %q, want @1", got)
	}
}

func TestMatchWindow_PanePathSiblingDoesNotMatch(t *testing.T) {
	wt := worktree.WorktreeInfo{Path: "/repo/.worktrees/foo"}
	states := map[string]windowstate.WindowState{
		"@1": {},
	}
	windowPaths := map[string]string{
		// Shares "foo" as a string prefix but is a distinct sibling worktree.
		"@1": "/repo/.worktrees/foo-bar",
	}

	got := matchWindow(wt, states, windowPaths)
	if got != "" {
		t.Errorf("matchWindow = %q, want empty (sibling worktree must not match)", got)
	}
}

func TestMatchWindow_WorktreeDirPrecedesPanePath(t *testing.T) {
	wtA := worktree.WorktreeInfo{Path: "/repo/.worktrees/a"}
	states := map[string]windowstate.WindowState{
		// Recorded intent says this window belongs to worktree A...
		"@1": {WorktreeDir: "/repo/.worktrees/a"},
	}
	windowPaths := map[string]string{
		// ...even though the agent has since cd-ed into worktree B.
		"@1": "/repo/.worktrees/b",
	}

	got := matchWindow(wtA, states, windowPaths)
	if got != "@1" {
		t.Errorf("matchWindow = %q, want @1 (recorded WorktreeDir must win over live pane path)", got)
	}
}

func TestMatchWindow_PanePathAtRepoRootDoesNotMatch(t *testing.T) {
	wt := worktree.WorktreeInfo{Path: "/repo/.worktrees/foo"}
	states := map[string]windowstate.WindowState{
		"@1": {},
	}
	windowPaths := map[string]string{
		// The main repo checkout is an ancestor of every worktree, never a
		// descendant, so it must never match.
		"@1": "/repo",
	}

	got := matchWindow(wt, states, windowPaths)
	if got != "" {
		t.Errorf("matchWindow = %q, want empty (repo root must not match a worktree)", got)
	}
}

func TestBuildWindowFacts_NotLiveReturnsZeroValue(t *testing.T) {
	states := map[string]windowstate.WindowState{
		"@1": {State: "working", Mode: "dispatch"},
	}
	live := map[string]string{} // @1 is not in the live set -- window has closed

	got := buildWindowFacts("@1", states, live, "")
	if got.Live {
		t.Errorf("expected Live=false for a window not in the live set, got %+v", got)
	}
}

func TestBuildWindowFacts_LiveShellAndSelf(t *testing.T) {
	states := map[string]windowstate.WindowState{
		"@1": {State: "idle", Mode: "dispatch"},
		"@2": {State: "working"},
	}
	live := map[string]string{
		"@1": "shell",
		"@2": "go-engineer-1",
	}

	shellFacts := buildWindowFacts("@1", states, live, "@99")
	if !shellFacts.Live || !shellFacts.IsShell || shellFacts.IsSelf {
		t.Errorf("shell window facts = %+v, want Live=true IsShell=true IsSelf=false", shellFacts)
	}

	selfFacts := buildWindowFacts("@2", states, live, "@2")
	if !selfFacts.Live || !selfFacts.IsSelf || selfFacts.IsShell {
		t.Errorf("self window facts = %+v, want Live=true IsSelf=true IsShell=false", selfFacts)
	}
	if selfFacts.Status != "working" {
		t.Errorf("self window facts Status = %q, want working", selfFacts.Status)
	}
}

func TestResolveStatus(t *testing.T) {
	tests := map[string]string{
		"working": "working",
		"blocked": "blocked",
		"idle":    "idle",
		"":        "idle",
		"bogus":   "idle",
	}
	for in, want := range tests {
		if got := resolveStatus(in); got != want {
			t.Errorf("resolveStatus(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestActionText_Skip(t *testing.T) {
	c := Candidate{Disposition: DispositionSkip, Reason: "worktree has uncommitted changes (dirty tree)"}
	got := c.ActionText()
	want := "skip: worktree has uncommitted changes (dirty tree)"
	if got != want {
		t.Errorf("ActionText() = %q, want %q", got, want)
	}
}

func TestActionText_PlanBeforeExecution(t *testing.T) {
	c := Candidate{Disposition: DispositionLanded}
	got := c.ActionText()
	if got == "" || got == "skip: " {
		t.Errorf("ActionText() for an unexecuted reclaimable candidate should describe the plan, got %q", got)
	}
}

func TestActionText_ExecutedWithErrors(t *testing.T) {
	c := Candidate{
		Disposition: DispositionEmpty,
		Executed:    true,
		StepsDone:   []string{"removed worktree"},
		Errors:      []string{"delete branch: exit status 1"},
	}
	got := c.ActionText()
	if got != "removed worktree; FAILED delete branch: exit status 1" {
		t.Errorf("ActionText() = %q", got)
	}
}

func TestReclaimable_FiltersSkips(t *testing.T) {
	candidates := []Candidate{
		{WorktreePath: "a", Disposition: DispositionSkip},
		{WorktreePath: "b", Disposition: DispositionLanded},
		{WorktreePath: "c", Disposition: DispositionEmpty},
		{WorktreePath: "d", Disposition: DispositionOpenPR},
	}

	got := Reclaimable(candidates)
	if len(got) != 3 {
		t.Fatalf("Reclaimable() returned %d candidates, want 3", len(got))
	}
	for _, c := range got {
		if c.Disposition == DispositionSkip {
			t.Errorf("Reclaimable() included a skip disposition: %+v", c)
		}
	}

	// Mutating through the returned pointers must be visible in the
	// original slice, since the CLI reprints `candidates` after Reclaim.
	got[0].Executed = true
	if !candidates[1].Executed {
		t.Error("Reclaimable() pointers should alias the original slice's backing array")
	}
}

func TestReclaim_RefusesSkipDisposition(t *testing.T) {
	c := &Candidate{WorktreePath: "/tmp/does-not-matter", Disposition: DispositionSkip}
	Reclaim(c)
	if c.Executed {
		t.Error("Reclaim must refuse to act on a Skip disposition")
	}
	if len(c.StepsDone) != 0 || len(c.Errors) != 0 {
		t.Errorf("Reclaim on a Skip disposition should be a pure no-op, got StepsDone=%v Errors=%v", c.StepsDone, c.Errors)
	}
}

// TestPanePathFallback_PreventsUnsafeReclaimOfWorkingWindow is the safety
// payload of the pane-path fallback: a legacy windowstate file with no
// Branch/WorktreeDir recorded used to leave matchWindow returning "", which
// meant buildWindowFacts produced a zero WindowFacts and every safety rail in
// Classify silently passed -- even for a window that is actively working.
// With the pane-path fallback wired in, the window must resolve, and Classify
// must skip it for being "working", never reclaim it.
func TestPanePathFallback_PreventsUnsafeReclaimOfWorkingWindow(t *testing.T) {
	wt := worktree.WorktreeInfo{Path: "/repo/.worktrees/agent-go-engineer-issue-42", Branch: "agent/go-engineer/issue-42"}
	// Legacy file: no Branch or WorktreeDir recorded, only the live state.
	states := map[string]windowstate.WindowState{
		"@1": {State: "working"},
	}
	windowPaths := map[string]string{
		"@1": "/repo/.worktrees/agent-go-engineer-issue-42",
	}
	live := map[string]string{"@1": "go-engineer"}

	windowID := matchWindow(wt, states, windowPaths)
	if windowID != "@1" {
		t.Fatalf("matchWindow = %q, want @1", windowID)
	}

	wf := buildWindowFacts(windowID, states, live, "")
	// Facts that would otherwise make this worktree reclaimable (fully merged,
	// no dirty tree, no unpushed commits) -- the only thing standing between
	// "reclaimable" and "skip" here is the working-window safety rail.
	facts := Facts{Window: wf, AheadOfBase: 3, MergedIntoBase: true}
	verdict := Classify(facts)

	if verdict.Disposition != DispositionSkip {
		t.Fatalf("Classify() disposition = %q, want %q -- a working window's worktree must never be reclaimed",
			verdict.Disposition, DispositionSkip)
	}
	if !strings.Contains(verdict.Reason, "working") {
		t.Errorf("Classify() reason = %q, want it to mention the working status", verdict.Reason)
	}
}

// ---------------------------------------------------------------------------
// Gather: orphan directories under .worktrees/
// ---------------------------------------------------------------------------

// setupRepoWithWorktree creates a plain git repo with one commit and one
// real linked worktree registered under .worktrees/, and returns the repo
// root and the worktree's path.
func setupRepoWithWorktree(t *testing.T) (repoDir, wtPath string) {
	t.Helper()

	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	gitRun(t, dir, "init")
	gitRun(t, dir, "config", "user.email", "test@example.com")
	gitRun(t, dir, "config", "user.name", "Test")

	readme := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readme, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "README.md")
	gitRun(t, dir, "commit", "-m", "initial commit")

	wt := filepath.Join(dir, ".worktrees", "registered")
	gitRun(t, dir, "worktree", "add", "-b", "registered-branch", wt)

	return dir, wt
}

func TestGather_ReportsUnregisteredDirectoryAsSkip(t *testing.T) {
	repoDir, wtPath := setupRepoWithWorktree(t)

	// A plain directory under .worktrees/ that git never knew about (e.g.
	// hand-copied or left behind by tooling that predates worktree.Create).
	orphan := filepath.Join(repoDir, ".worktrees", "orphan")
	if err := os.MkdirAll(orphan, 0o755); err != nil {
		t.Fatal(err)
	}

	candidates, err := Gather(Options{RepoDir: repoDir})
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	var orphanCount int
	var orphanCandidate Candidate
	for _, c := range candidates {
		if resolvedClean(c.WorktreePath) == resolvedClean(orphan) {
			orphanCount++
			orphanCandidate = c
		}
	}
	if orphanCount != 1 {
		t.Fatalf("orphan directory appeared %d times in candidates, want exactly 1", orphanCount)
	}
	if orphanCandidate.Disposition != DispositionSkip {
		t.Errorf("orphan candidate Disposition = %q, want %q", orphanCandidate.Disposition, DispositionSkip)
	}
	if orphanCandidate.Branch != "" {
		t.Errorf("orphan candidate Branch = %q, want empty", orphanCandidate.Branch)
	}
	if orphanCandidate.Reason != "unregistered directory under .worktrees (not a git worktree)" {
		t.Errorf("orphan candidate Reason = %q", orphanCandidate.Reason)
	}

	// The registered worktree must still be reported as its own (non-skip
	// on this axis -- it just isn't reclaimable here because it has no
	// upstream and a commit ahead of base, dirty status aside) candidate,
	// not folded into or confused with the orphan.
	var sawRegistered bool
	for _, c := range candidates {
		if resolvedClean(c.WorktreePath) == resolvedClean(wtPath) {
			sawRegistered = true
		}
	}
	if !sawRegistered {
		t.Error("registered worktree missing from Gather results")
	}

	// The orphan must never show up as reclaimable.
	for _, c := range Reclaimable(candidates) {
		if resolvedClean(c.WorktreePath) == resolvedClean(orphan) {
			t.Error("orphan directory must never be Reclaimable")
		}
	}
}

func TestGather_NoWorktreesDirIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	gitRun(t, dir, "init")
	gitRun(t, dir, "config", "user.email", "test@example.com")
	gitRun(t, dir, "config", "user.name", "Test")
	readme := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readme, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "README.md")
	gitRun(t, dir, "commit", "-m", "initial commit")

	candidates, err := Gather(Options{RepoDir: dir})
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(candidates) != 0 {
		t.Errorf("Gather() with no .worktrees/ dir and no linked worktrees = %d candidates, want 0", len(candidates))
	}
}

func TestOrphanDirectoryCandidates_MissingRootIsNotAnError(t *testing.T) {
	got := orphanDirectoryCandidates(filepath.Join(t.TempDir(), "does-not-exist"), nil)
	if got != nil {
		t.Errorf("orphanDirectoryCandidates() with missing root = %v, want nil", got)
	}
}
