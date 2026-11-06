package cleanup

import (
	"strings"
	"testing"
)

func tracked(mut func(*SweepFacts)) SweepFacts {
	f := SweepFacts{Tracked: true, State: "idle", Done: true}
	if mut != nil {
		mut(&f)
	}
	return f
}

func TestClassifyWindow(t *testing.T) {
	landed := Facts{HasUpstream: true, AheadOfBase: 2, PR: PRMerged}
	tests := []struct {
		name     string
		f        SweepFacts
		kind     SweepKind
		contains string
		worktree bool
	}{
		{"self never closed", tracked(func(f *SweepFacts) { f.IsSelf = true }), SweepSkip, "this window", false},
		{"shell/improve never closed", tracked(func(f *SweepFacts) { f.NonAgent = true }), SweepSkip, "", false},
		{"untracked never closed", SweepFacts{Done: true}, SweepSkip, "untracked", false},
		{"working", tracked(func(f *SweepFacts) { f.State = "working"; f.Done = false }), SweepWorking, "", false},
		{"pane busy beats idle record", tracked(func(f *SweepFacts) { f.Pane = "busy" }), SweepWorking, "", false},
		{"blocked record", tracked(func(f *SweepFacts) { f.State = "blocked"; f.PendingTool = "Bash"; f.PendingToolDetail = "rm x" }), SweepAction, "Bash rm x", false},
		{"record idle but pane dialog", tracked(func(f *SweepFacts) { f.Pane = "awaiting" }), SweepAction, "approve/answer", false},
		{"pending mail on done window", tracked(func(f *SweepFacts) { f.PendingMail = 2 }), SweepAction, "2 unread", false},
		{"idle not done", tracked(func(f *SweepFacts) { f.Done = false; f.Activity = "Editing x.go" }), SweepAction, "waiting for input: Editing x.go", false},
		{"bare without include", tracked(func(f *SweepFacts) { f.Mode = "bare" }), SweepAction, "idle interactive", false},
		{"bare with include closes", tracked(func(f *SweepFacts) { f.Mode = "bare"; f.IncludeBare = true }), SweepClose, "done, no branch", false},
		{"bare include but not done", tracked(func(f *SweepFacts) { f.Mode = "bare"; f.IncludeBare = true; f.Done = false }), SweepAction, "waiting", false},
		{"done no branch no worktree", tracked(nil), SweepClose, "done, no branch", false},
		{"done landed worktree", tracked(func(f *SweepFacts) { f.HasWorktree = true; f.WT = landed }), SweepClose, "merged", true},
		{"done empty worktree", tracked(func(f *SweepFacts) { f.HasWorktree = true; f.WT = Facts{HasUpstream: true} }), SweepClose, "no commits", true},
		{"done dirty worktree", tracked(func(f *SweepFacts) { f.HasWorktree = true; w := landed; w.Dirty = true; f.WT = w }), SweepAction, "dirty", false},
		{"done unpushed worktree", tracked(func(f *SweepFacts) { f.HasWorktree = true; w := landed; w.UnpushedCount = 1; f.WT = w }), SweepAction, "unpushed", false},
		{"done no upstream worktree", tracked(func(f *SweepFacts) { f.HasWorktree = true; f.WT = Facts{AheadOfBase: 1, PR: PRNone} }), SweepAction, "no upstream", false},
		{"done unknown PR worktree", tracked(func(f *SweepFacts) { f.HasWorktree = true; f.WT = Facts{HasUpstream: true, AheadOfBase: 1} }), SweepAction, "gh PR lookup", false},
		{"done open PR worktree", tracked(func(f *SweepFacts) {
			f.HasWorktree = true
			f.WT = Facts{HasUpstream: true, AheadOfBase: 1, PR: PROpen}
			f.WTPR = PRInfo{State: PROpen, Number: 7, URL: "http://x/7"}
		}), SweepAction, "review PR #7 http://x/7", false},
		{"branch only open PR", tracked(func(f *SweepFacts) { f.Branch = "b"; f.BranchPR = PRInfo{State: PROpen, Number: 3, URL: "u"} }), SweepAction, "review PR #3", false},
		{"branch only merged", tracked(func(f *SweepFacts) { f.Branch = "b"; f.BranchPR = PRInfo{State: PRMerged} }), SweepClose, "PR merged", false},
		{"branch only closed", tracked(func(f *SweepFacts) { f.Branch = "b"; f.BranchPR = PRInfo{State: PRClosed} }), SweepClose, "PR closed", false},
		{"branch only none", tracked(func(f *SweepFacts) { f.Branch = "b"; f.BranchPR = PRInfo{State: PRNone} }), SweepClose, "no PR", false},
		{"branch only unknown", tracked(func(f *SweepFacts) { f.Branch = "b" }), SweepAction, "lookup failed", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := ClassifyWindow(tt.f)
			if v.Kind != tt.kind {
				t.Fatalf("kind = %s (%+v), want %s", v.Kind, v, tt.kind)
			}
			if got := v.Reason + " | " + v.Action; !strings.Contains(got, tt.contains) {
				t.Errorf("verdict %q missing %q", got, tt.contains)
			}
			if v.Worktree != tt.worktree {
				t.Errorf("Worktree = %v, want %v", v.Worktree, tt.worktree)
			}
		})
	}
}
