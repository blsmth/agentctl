package cleanup

import (
	"strings"
	"testing"
)

// TestClassify is table-driven over Facts alone -- no git, gh, or tmux
// dependency, per the design goal in classify.go's doc comment.
func TestClassify(t *testing.T) {
	tests := []struct {
		name           string
		facts          Facts
		wantDisp       Disposition
		wantPreserve   bool
		reasonContains string
	}{
		{
			name: "dirty tree is unreclaimable",
			facts: Facts{
				Dirty:       true,
				AheadOfBase: 3,
				PR:          PRMerged,
			},
			wantDisp:       DispositionSkip,
			reasonContains: "uncommitted changes",
		},
		{
			name: "unpushed commits are unreclaimable",
			facts: Facts{
				UnpushedCount: 2,
				HasUpstream:   true,
				AheadOfBase:   2,
				PR:            PRMerged,
			},
			wantDisp:       DispositionSkip,
			reasonContains: "unpushed commit",
		},
		{
			name: "no upstream with commits ahead of base is unreclaimable",
			facts: Facts{
				HasUpstream: false,
				AheadOfBase: 1,
				PR:          PRMerged,
			},
			wantDisp:       DispositionSkip,
			reasonContains: "no upstream",
		},
		{
			name: "no upstream with zero commits ahead is empty, not the no-upstream skip",
			facts: Facts{
				HasUpstream: false,
				AheadOfBase: 0,
			},
			wantDisp:       DispositionEmpty,
			reasonContains: "abandoned dispatch",
		},
		{
			name: "working window is unreclaimable regardless of git state",
			facts: Facts{
				AheadOfBase: 0, // would otherwise be "empty"
				Window:      WindowFacts{Live: true, Status: "working"},
			},
			wantDisp:       DispositionSkip,
			reasonContains: "working",
		},
		{
			name: "blocked window is unreclaimable regardless of git state",
			facts: Facts{
				AheadOfBase: 5,
				PR:          PRMerged, // would otherwise be "landed"
				Window:      WindowFacts{Live: true, Status: "blocked"},
			},
			wantDisp:       DispositionSkip,
			reasonContains: "blocked",
		},
		{
			name: "bare mode window is never a dispatch target",
			facts: Facts{
				AheadOfBase: 0,
				Window:      WindowFacts{Live: true, Status: "idle", Mode: "bare"},
			},
			wantDisp:       DispositionSkip,
			reasonContains: "bare",
		},
		{
			name: "shell window is never touched",
			facts: Facts{
				AheadOfBase: 0,
				Window:      WindowFacts{Live: true, Status: "idle", IsShell: true},
			},
			wantDisp:       DispositionSkip,
			reasonContains: "shell",
		},
		{
			name: "self window is never touched",
			facts: Facts{
				AheadOfBase: 3,
				PR:          PRMerged,
				Window:      WindowFacts{Live: true, Status: "idle", IsSelf: true},
			},
			wantDisp:       DispositionSkip,
			reasonContains: "this command is running in",
		},
		{
			name: "landed: PR merged",
			facts: Facts{
				HasUpstream: true,
				AheadOfBase: 4,
				PR:          PRMerged,
			},
			wantDisp:       DispositionLanded,
			reasonContains: "merged",
		},
		{
			name: "landed: PR closed",
			facts: Facts{
				HasUpstream: true,
				AheadOfBase: 4,
				PR:          PRClosed,
			},
			wantDisp:       DispositionLanded,
			reasonContains: "closed",
		},
		{
			name: "landed: fully merged into base with no PR record at all",
			facts: Facts{
				HasUpstream:    true,
				AheadOfBase:    4,
				MergedIntoBase: true,
				PR:             PRNone,
			},
			wantDisp:       DispositionLanded,
			reasonContains: "merged into",
		},
		{
			name: "empty: zero commits relative to base, abandoned dispatch",
			facts: Facts{
				HasUpstream: true,
				AheadOfBase: 0,
				PR:          PRNone,
			},
			wantDisp:       DispositionEmpty,
			reasonContains: "abandoned dispatch",
		},
		{
			name: "open-pr without the flag is skipped",
			facts: Facts{
				HasUpstream:    true,
				AheadOfBase:    2,
				PR:             PROpen,
				IncludeOpenPRs: false,
			},
			wantDisp:       DispositionSkip,
			wantPreserve:   false,
			reasonContains: "--include-open-prs",
		},
		{
			name: "open-pr with the flag reclaims the worktree but preserves the branch",
			facts: Facts{
				HasUpstream:    true,
				AheadOfBase:    2,
				PR:             PROpen,
				IncludeOpenPRs: true,
			},
			wantDisp:       DispositionOpenPR,
			wantPreserve:   true,
			reasonContains: "open",
		},
		{
			name: "gh-unknown never reclaims, even with real commits",
			facts: Facts{
				HasUpstream: true,
				AheadOfBase: 3,
				PR:          PRUnknown,
			},
			wantDisp:       DispositionSkip,
			reasonContains: "gh PR lookup failed",
		},
		{
			name: "no PR found and not merged into base refuses to guess",
			facts: Facts{
				HasUpstream: true,
				AheadOfBase: 3,
				PR:          PRNone,
			},
			wantDisp:       DispositionSkip,
			reasonContains: "no PR found",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.facts)
			if got.Disposition != tc.wantDisp {
				t.Errorf("Classify().Disposition = %q, want %q (reason: %q)", got.Disposition, tc.wantDisp, got.Reason)
			}
			if got.PreserveBranch != tc.wantPreserve {
				t.Errorf("Classify().PreserveBranch = %v, want %v", got.PreserveBranch, tc.wantPreserve)
			}
			if tc.reasonContains != "" && !strings.Contains(strings.ToLower(got.Reason), strings.ToLower(tc.reasonContains)) {
				t.Errorf("Classify().Reason = %q, want it to contain %q", got.Reason, tc.reasonContains)
			}
		})
	}
}

// TestClassify_DirtyAndUnpushedStayUnreclaimableRegardlessOfYes documents and
// asserts the invariant the spec calls out explicitly: there is no
// "--force"/"--yes"-equivalent input to Classify at all. Dirty and unpushed
// worktrees are unreclaimable purely as a function of Facts -- the CLI's
// --yes flag only ever skips the confirmation *prompt* for candidates
// Classify already decided were reclaimable; it is never consulted here and
// cannot turn a Skip into a reclaim.
func TestClassify_DirtyAndUnpushedStayUnreclaimableRegardlessOfYes(t *testing.T) {
	dirty := Facts{Dirty: true, AheadOfBase: 10, PR: PRMerged}
	unpushed := Facts{UnpushedCount: 3, HasUpstream: true, AheadOfBase: 10, PR: PRMerged}

	for _, f := range []Facts{dirty, unpushed} {
		if got := Classify(f).Disposition; got.Reclaimable() {
			t.Fatalf("Classify(%+v) = %q, want an unreclaimable disposition", f, got)
		}
	}
}
