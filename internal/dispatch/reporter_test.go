package dispatch

import (
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/tmux/launchtest"
	"github.com/blsmth/agentctl/internal/worktree"
)

// TestReporterPrompt checks a role: reporter agent's launch: worker
// preamble, no commit/push/PR finishing steps, no "stay responsive until it
// merges" paragraph, and a report step that names the orchestrator when
// there is one.
func TestReporterPrompt(t *testing.T) {
	orch := Orchestrator{WindowID: "@5", Pane: "%6"}
	tests := []struct {
		name       string
		req        Request
		want       []string
		wantAbsent []string
	}{
		{
			name: "adhoc with orchestrator",
			req:  Request{Mode: ModeAdhoc, CustomPrompt: "Review the design.", Orchestrator: orch},
			want: []string{
				"### Your orchestrator",
				"make no commits and open no pull request unless the task asks for one",
				"send your result to your orchestrator (window @5) with SendMessage",
				`Execute the task under "## Task" from start to finish.`,
			},
		},
		{
			name: "adhoc without orchestrator",
			req:  Request{Mode: ModeAdhoc, CustomPrompt: "Research X."},
			want: []string{"make no commits and open no pull request unless the task asks for one, then put your result in your final message."},
			wantAbsent: []string{
				"### Your orchestrator",
				"SendMessage to your orchestrator",
			},
		},
		{
			name:       "dispatch",
			req:        Request{Mode: ModeDispatch, Ticket: 9, TicketTitle: "t", TicketBody: "b", Orchestrator: orch},
			want:       []string{"Work on issue #9", "and report the result.", "send your result to your orchestrator (window @5)"},
			wantAbsent: []string{"open a PR when done"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.req.AgentName = "researcher"
			req, spec := applyAgentRole(tt.req, modes[tt.req.Mode], agent.RoleReporter, false)
			got := buildBoth(req, spec, "/repo/.worktrees/x", "agent/researcher/adhoc-1", true, nil, "")
			for _, w := range append(tt.want, mergeLine, "### Git conventions", sendMessageDeferredNote) {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in:\n%s", w, got)
				}
			}
			for _, w := range append(tt.wantAbsent, finishingMarker, "git push", "gh pr create", "After you open the PR", orchMarker) {
				if strings.Contains(got, w) {
					t.Errorf("unexpected %q in:\n%s", w, got)
				}
			}
		})
	}
}

// TestReporterPreambleMatchesWorker pins that a reporter's preamble differs
// from a worker's only by the dropped PR paragraph.
func TestReporterPreambleMatchesWorker(t *testing.T) {
	o := Orchestrator{WindowID: "@1"}
	if got := preambleWithOrchestrator(RoleReporter, Orchestrator{}); got != WorkerPreamble {
		t.Error("reporter without orchestrator should get WorkerPreamble")
	}
	worker := preambleWithOrchestrator(RoleWorker, o)
	reporter := preambleWithOrchestrator(RoleReporter, o)
	if !strings.HasPrefix(worker, reporter) {
		t.Fatalf("reporter preamble is not a prefix of the worker one:\n%s", reporter)
	}
	if !strings.Contains(strings.TrimPrefix(worker, reporter), "After you open the PR") {
		t.Error("the worker-only tail should be the PR paragraph")
	}
	if got := preambleForHarness(RoleReporter, false); got != preambleForHarness(RoleWorker, false) {
		t.Error("no-messaging reporter preamble should match the worker one")
	}
}

// TestLaunchFactsCheckout covers the launch-facts line for each kind of
// directory a launch can run in.
func TestLaunchFactsCheckout(t *testing.T) {
	tests := []struct {
		name       string
		facts      launchFacts
		want       string
		wantAbsent []string
	}{
		{"own worktree", launchFacts{Branch: "agent/x/adhoc-1", InWorktree: true}, "- Branch: agent/x/adhoc-1 (a git worktree)", []string{"No worktree"}},
		{"main checkout", launchFacts{}, "- No worktree: this is the repo's main checkout, which other sessions may also use", nil},
		{"existing linked worktree", launchFacts{Checkout: checkoutInfo{Kind: checkoutLinked, Branch: "feature"}}, "- Branch: feature (an existing git worktree, not created for this launch; other sessions may also use it)", []string{"No worktree", "main checkout"}},
		{"detached linked worktree", launchFacts{Checkout: checkoutInfo{Kind: checkoutLinked}}, "- Detached HEAD in an existing git worktree", []string{"No worktree", "- Branch:"}},
		{"not a repo", launchFacts{Checkout: checkoutInfo{Kind: checkoutNotRepo}}, "- No worktree: the working directory is not in a git repository", []string{"main checkout"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := launchFactsBlock(Request{}, tt.facts)
			if !strings.Contains(got, tt.want) {
				t.Errorf("missing %q in:\n%s", tt.want, got)
			}
			for _, w := range tt.wantAbsent {
				if strings.Contains(got, w) {
					t.Errorf("unexpected %q in:\n%s", w, got)
				}
			}
		})
	}
}

func TestInspectCheckout(t *testing.T) {
	tests := []struct {
		name string
		in   worktree.Checkout
		want checkoutInfo
	}{
		{"main", worktree.Checkout{Repo: true, Branch: "main"}, checkoutInfo{Kind: checkoutMain}},
		{"linked", worktree.Checkout{Repo: true, Linked: true, Branch: "f"}, checkoutInfo{Kind: checkoutLinked, Branch: "f"}},
		{"not a repo", worktree.Checkout{}, checkoutInfo{Kind: checkoutNotRepo}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prev := inspectCheckoutFn
			inspectCheckoutFn = func(string) worktree.Checkout { return tt.in }
			t.Cleanup(func() { inspectCheckoutFn = prev })
			if got := inspectCheckout("/d"); got != tt.want {
				t.Errorf("inspectCheckout = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestLaunchNoWorktreeInLinkedWorktree is the regression test for a
// --no-worktree launch from a directory that is itself a linked worktree:
// the launch facts used to call it "the shared checkout".
func TestLaunchNoWorktreeInLinkedWorktree(t *testing.T) {
	no := false
	env := setupGolden(t, goldenConfig)
	inspectCheckoutFn = func(string) worktree.Checkout {
		return worktree.Checkout{Repo: true, Linked: true, Branch: "agent/lead/adhoc-1"}
	}
	rec := launchtest.Capture(t)
	if _, err := Launch(Request{AgentName: "golden", Mode: ModeAdhoc, CustomPrompt: "hi", WorkDir: env.Project, UseWorktree: &no, Background: true}); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	cmd := rec.Only().Command
	for _, w := range []string{"Branch: agent/lead/adhoc-1 (an existing git worktree, not created for this launch", "an existing git worktree that was not created for this launch"} {
		if !strings.Contains(cmd, w) {
			t.Errorf("launch missing %q", w)
		}
	}
	if strings.Contains(cmd, "main checkout") {
		t.Error("launch still calls a linked worktree the main checkout")
	}
}
