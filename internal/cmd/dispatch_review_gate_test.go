package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/dispatch"
	"github.com/blsmth/agentctl/internal/dispatchapproval"
)

// gateStubs pins the reviewer approval check's inputs. Approvals go through
// the real dispatchapproval store, which testenv puts under a temp home.
type gateStubs struct {
	agentSession bool
	mode         string
	// local lists the dirs from which the reviewer resolves to a
	// project-local file; "*" means every dir.
	local    []string
	resolved [][]string
}

const gateWindow = "@7"

var gateArgs = []string{"--review-pr", "42"}

func stubReviewerGate(t *testing.T, g *gateStubs) {
	t.Helper()
	prevCaller, prevWindow, prevArgs, prevRes, prevCfg := callerIsAgentSession, approvalWindowID, dispatchArgs, reviewerResolution, loadGateConfig
	t.Cleanup(func() {
		callerIsAgentSession, approvalWindowID, dispatchArgs, reviewerResolution, loadGateConfig = prevCaller, prevWindow, prevArgs, prevRes, prevCfg
	})
	callerIsAgentSession = func() bool { return g.agentSession }
	approvalWindowID = func() string { return gateWindow }
	dispatchArgs = func() []string { return gateArgs }
	loadGateConfig = func() *config.Config { return &config.Config{DispatchApproval: g.mode} }
	reviewerResolution = func(_ *config.Config, name string, dirs ...string) agent.GlobalCheck {
		g.resolved = append(g.resolved, dirs)
		for _, d := range dirs {
			for _, l := range g.local {
				if l == "*" || l == d {
					return agent.GlobalCheck{Path: filepath.Join(d, ".claude", "agents", name+".md"), Reason: "project-local"}
				}
			}
		}
		return agent.GlobalCheck{Global: true, Path: "/home/.claude/agents/" + name + ".md"}
	}
}

func writeApproval(t *testing.T, created time.Time) {
	t.Helper()
	dir, err := dispatchapproval.Dir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"window": gateWindow, "created": created})
	if err := os.WriteFile(filepath.Join(dir, dispatchapproval.Key(gateWindow, gateArgs)), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func approvalLeft(t *testing.T) bool {
	t.Helper()
	dir, err := dispatchapproval.Dir()
	if err != nil {
		t.Fatal(err)
	}
	_, err = os.Stat(filepath.Join(dir, dispatchapproval.Key(gateWindow, gateArgs)))
	return err == nil
}

func clearApprovals(t *testing.T) {
	t.Helper()
	dir, err := dispatchapproval.Dir()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
}

func TestDispatchReviewPR_ReviewerApproval(t *testing.T) {
	tests := []struct {
		name         string
		gate         gateStubs
		agent        string
		approval     time.Duration // age of a recorded approval; <0 = none
		wantErr      string
		wantConsumed bool
	}{
		{name: "global reviewer from an agent session launches",
			gate: gateStubs{agentSession: true}, approval: -1},
		{name: "global reviewer leaves an approval unused",
			gate: gateStubs{agentSession: true}, approval: 0},
		{name: "local reviewer without approval is refused",
			gate: gateStubs{agentSession: true, local: []string{"*"}}, approval: -1,
			wantErr: "no human approval was recorded"},
		{name: "local reviewer with approval launches and consumes it",
			gate: gateStubs{agentSession: true, local: []string{"*"}}, approval: 0, wantConsumed: true},
		{name: "explicit --agent code-reviewer is checked too",
			gate: gateStubs{agentSession: true, local: []string{"*"}}, agent: "code-reviewer", approval: -1,
			wantErr: "no human approval was recorded"},
		{name: "local reviewer with an expired approval is refused",
			gate: gateStubs{agentSession: true, local: []string{"*"}}, approval: dispatchapproval.TTL + time.Minute,
			wantErr: "expired", wantConsumed: true},
		{name: "plain shell human is never refused",
			gate: gateStubs{agentSession: false, local: []string{"*"}}, approval: -1},
		{name: "dispatch_approval off is not checked",
			gate: gateStubs{agentSession: true, local: []string{"*"}, mode: config.DispatchApprovalOff}, approval: -1},
		{name: "other agents are not checked",
			gate: gateStubs{agentSession: true, local: []string{"*"}}, agent: "other-reviewer", approval: -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearApprovals(t)
			s := &reviewStubs{pr: dispatch.PullRequest{State: "OPEN"}, result: dispatch.Result{WindowID: "@40"}}
			stubReviewDispatch(t, s)
			g := tt.gate
			stubReviewerGate(t, &g)
			if tt.approval >= 0 {
				writeApproval(t, time.Now().Add(-tt.approval))
			}
			_, _, err := dispatchReviewPR(DispatchOptions{ReviewPR: 42, Agent: tt.agent, Dir: t.TempDir()})
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				if !strings.Contains(err.Error(), "rerun this exact command as its own Bash call") {
					t.Errorf("refusal does not say how to get the popup: %v", err)
				}
				if s.launched != nil {
					t.Error("launched despite the refusal")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if s.launched == nil {
					t.Fatal("not launched")
				}
				// Launch re-checks once the worktree exists; one approval
				// covers both checks.
				if err := s.launched.VerifyAgent(s.launched.WorkDir, s.launched.WorkDir); err != nil {
					t.Errorf("VerifyAgent after the pre-launch check: %v", err)
				}
			}
			if tt.approval >= 0 && approvalLeft(t) == tt.wantConsumed {
				t.Errorf("approval consumed = %v, want %v", !approvalLeft(t), tt.wantConsumed)
			}
		})
	}
}

// A consumed approval cannot be replayed by a second dispatch.
func TestDispatchReviewPR_ApprovalIsSingleUse(t *testing.T) {
	clearApprovals(t)
	s := &reviewStubs{pr: dispatch.PullRequest{State: "OPEN"}, result: dispatch.Result{WindowID: "@40"}}
	stubReviewDispatch(t, s)
	stubReviewerGate(t, &gateStubs{agentSession: true, local: []string{"*"}})
	writeApproval(t, time.Now())
	if _, _, err := dispatchReviewPR(DispatchOptions{ReviewPR: 42, Dir: t.TempDir()}); err != nil {
		t.Fatalf("first dispatch: %v", err)
	}
	s.launched = nil
	_, _, err := dispatchReviewPR(DispatchOptions{ReviewPR: 42, Dir: t.TempDir()})
	if err == nil || s.launched != nil {
		t.Fatalf("replayed dispatch: err = %v, launched = %v; want a refusal", err, s.launched != nil)
	}
}

// A reviewer that turns local only in the worktree (a definition committed on
// the default branch) is caught by Launch's VerifyAgent call.
func TestDispatchReviewPR_LocalOnlyInWorktree(t *testing.T) {
	clearApprovals(t)
	s := &reviewStubs{pr: dispatch.PullRequest{State: "OPEN"}, result: dispatch.Result{WindowID: "@40"}}
	stubReviewDispatch(t, s)
	g := &gateStubs{agentSession: true, local: []string{"/repo/.worktrees/review"}}
	stubReviewerGate(t, g)
	if _, _, err := dispatchReviewPR(DispatchOptions{ReviewPR: 42, Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	err := s.launched.VerifyAgent("/repo/.worktrees/review", "/repo")
	if err == nil || !strings.Contains(err.Error(), "no human approval") {
		t.Fatalf("VerifyAgent = %v, want a refusal", err)
	}
	if last := g.resolved[len(g.resolved)-1]; !slices.Contains(last, "/repo/.worktrees/review") || !slices.Contains(last, "/repo") {
		t.Errorf("resolved from %v, want the worktree and repo dirs", last)
	}
	writeApproval(t, time.Now())
	if err := s.launched.VerifyAgent("/repo/.worktrees/review", "/repo"); err != nil {
		t.Fatalf("VerifyAgent with approval: %v", err)
	}
}

func TestDispatchArgsFrom(t *testing.T) {
	tests := []struct {
		argv []string
		want []string
	}{
		{[]string{"agentctl", "dispatch", "--review-pr", "5"}, []string{"--review-pr", "5"}},
		{[]string{"/usr/local/bin/agentctl", "dispatch"}, []string{}},
		{[]string{"agentctl", "status"}, nil},
	}
	for _, tt := range tests {
		got := dispatchArgsFrom(tt.argv)
		if !slices.Equal(got, tt.want) || (got == nil) != (tt.want == nil) {
			t.Errorf("dispatchArgsFrom(%v) = %#v, want %#v", tt.argv, got, tt.want)
		}
	}
}
