package dispatch

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/tmux/launchtest"
)

const prJSON = `{"number":42,"title":"Add widgets","url":"https://github.com/o/r/pull/42","body":"Adds widgets.","baseRefName":"main","headRefName":"agent/go-engineer/issue-9","headRefOid":"abc123def","state":"%s","author":{"login":"bob"},"files":[{"path":"a.go"},{"path":"b/b.go"}]}`

func TestFetchPR(t *testing.T) {
	tests := []struct {
		name    string
		state   string
		err     error
		wantErr string
	}{
		{name: "open", state: "OPEN"},
		{name: "closed", state: "CLOSED", wantErr: "PR #42 is closed"},
		{name: "merged", state: "MERGED", wantErr: "PR #42 is merged"},
		{name: "gh error", err: errors.New("boom"), wantErr: "fetching PR #42"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotArgs []string
			stubRunner(t, func(_, name string, args ...string) ([]byte, error) {
				gotArgs = append([]string{name}, args...)
				if tt.err != nil {
					return nil, tt.err
				}
				return []byte(strings.Replace(prJSON, "%s", tt.state, 1)), nil
			})
			pr, err := FetchPR("o/r", 42)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(gotArgs, "--repo") || !slices.Contains(gotArgs, "o/r") || gotArgs[1] != "pr" {
				t.Errorf("gh args = %v", gotArgs)
			}
			want := PullRequest{Number: 42, Title: "Add widgets", URL: "https://github.com/o/r/pull/42", Body: "Adds widgets.",
				BaseRef: "main", HeadRef: "agent/go-engineer/issue-9", HeadSHA: "abc123def", State: "OPEN", Login: "bob",
				Files: []string{"a.go", "b/b.go"}}
			if pr.Number != want.Number || pr.Title != want.Title || pr.HeadSHA != want.HeadSHA || pr.HeadRef != want.HeadRef ||
				pr.BaseRef != want.BaseRef || pr.Login != want.Login || !slices.Equal(pr.Files, want.Files) || pr.Body != want.Body {
				t.Errorf("pr = %+v, want %+v", pr, want)
			}
		})
	}
}

func samplePullRequest() PullRequest {
	return PullRequest{Number: 42, Title: "Add widgets", URL: "https://github.com/o/r/pull/42", Body: "Adds widgets.",
		BaseRef: "main", HeadRef: "agent/go-engineer/issue-9", HeadSHA: "abc123def", Files: []string{"a.go", "b/b.go"}}
}

func TestReviewPRInstructions(t *testing.T) {
	found := PRAuthor{WindowID: "@12", Pane: "%34", Name: "go-engineer-r-1"}
	tests := []struct {
		name        string
		author      PRAuthor
		extra       string
		noMessaging bool
		want        []string
		notWant     []string
	}{
		{
			name:   "author found",
			author: found,
			want: []string{"### The author", "@12.%34", "Its session name was go-engineer-r-1", "Send it your must-fix list with SendMessage",
				"Do not message a different session"},
			notWant: []string{"## Additional review instructions", "different Claude account"},
		},
		{
			name:    "author not found",
			want:    []string{"No live agentctl session was found working on this PR's branch (agent/go-engineer/issue-9)", "orchestrator only"},
			notWant: []string{"@12", "Send it your must-fix list"},
		},
		{
			name:    "author on another account",
			author:  PRAuthor{WindowID: "@12", Pane: "%34", crossAccount: true},
			want:    []string{"different Claude account", "Do not try to reach it with SendMessage"},
			notWant: []string{"@12.%34"},
		},
		{
			name:        "no messaging harness",
			author:      found,
			noMessaging: true,
			want:        []string{"cannot message other sessions", "first line of your final message"},
			notWant:     []string{"@12.%34", "SendMessage)"},
		},
		{
			name:  "extra instructions",
			extra: "Focus on the tmux calls.",
			want:  []string{"## Additional review instructions\n\nFocus on the tmux calls."},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pr := samplePullRequest()
			pr.Author, pr.Extra = tt.author, tt.extra
			got := reviewPRInstructions(Request{Mode: ModeReviewPR, ReviewPR: &pr, noMessaging: tt.noMessaging})
			common := []string{
				"## Pull request #42: Add widgets", "https://github.com/o/r/pull/42", "Base branch: main",
				"Head branch: agent/go-engineer/issue-9", "Head sha: abc123def", "- a.go\n- b/b.go", "Adds widgets.",
				"REVIEW PR #42 — VERDICT: MERGE | MERGE-WITH-FOLLOWUPS | BLOCKED | NEEDS-BRENDAN",
				"Do not commit, push, open a PR, or write to GitHub", "at most 3 rounds",
			}
			for _, w := range append(common, tt.want...) {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in:\n%s", w, got)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(got, w) {
					t.Errorf("unexpected %q in:\n%s", w, got)
				}
			}
		})
	}
}

func TestReviewPRSystemPrompt(t *testing.T) {
	pr := samplePullRequest()
	req := Request{AgentName: "code-reviewer", Mode: ModeReviewPR, ReviewPR: &pr, Orchestrator: Orchestrator{WindowID: "@5", Pane: "%6"}}
	tests := []struct {
		name        string
		shared      bool
		want        []string
		notWant     []string
		useWorktree bool
	}{
		{"worktree", false, []string{"cut from the default branch, not from the PR", "git fetch origin pull/42/head && git checkout --detach FETCH_HEAD", "is abc123def", "git checkout agent/code-reviewer/review-pr-42-"}, []string{"shared checkout"}, true},
		{"shared checkout", true, []string{"You are in the shared checkout", "gh pr diff 42"}, []string{"git checkout --detach"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := req
			r.reviewSharedCheckout = tt.shared
			got := buildSystemPrompt(r, modes[ModeReviewPR], "/repo/.worktrees/agent-code-reviewer-review-pr-42", buildBranch(r), tt.useWorktree, nil, "")
			for _, w := range append(tt.want, "### Your orchestrator", "REVIEW PR #42") {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q", w)
				}
			}
			for _, w := range append(tt.notWant, finishingMarker, mergeLine, execMarker, orchMarker) {
				if strings.Contains(got, w) {
					t.Errorf("unexpected %q", w)
				}
			}
		})
	}
}

func TestReviewModeSpec(t *testing.T) {
	spec := modes[ModeReviewPR]
	if !spec.Autonomous || spec.IncludeFinishingSteps || spec.Role != RoleWorker || !spec.DefaultWorktree || spec.DefaultModel != "" {
		t.Errorf("ModeReviewPR spec = %+v", spec)
	}
	if !IsAutonomous(string(ModeReviewPR)) {
		t.Error("review-pr is not autonomous")
	}
	r := Request{AgentName: "code-reviewer", Mode: ModeReviewPR, ReviewPR: &PullRequest{Number: 7}}
	b1, b2 := buildBranch(r), buildBranch(r)
	if !strings.HasPrefix(b1, "agent/code-reviewer/review-pr-7-") || b1 == b2 {
		t.Errorf("branches = %s, %s; want distinct agent/code-reviewer/review-pr-7-<id>", b1, b2)
	}
	if got := TaskFor(Request{ReviewPR: &PullRequest{Number: 7, Title: "Fix"}, CustomPrompt: "extra"}); got != "Review PR #7: Fix" {
		t.Errorf("task = %q", got)
	}
}

func TestReviewAuthorNotice(t *testing.T) {
	got := ReviewAuthorNotice("code-reviewer", "@40", "%41", 42)
	want := "agentctl notice: code-reviewer in @40.%41 is reviewing your PR #42 at the request of your orchestrator; it may message you directly."
	if got != want {
		t.Errorf("notice = %q, want %q", got, want)
	}
}

// TestLaunchReviewPR launches a reviewer through Launch and checks the
// author section and Result.MessagesAuthor, including an author under
// another Claude account.
func TestLaunchReviewPR(t *testing.T) {
	no := false
	accounts := `{
  "accounts": {"work": {"config_dir": "~/work-claude"}},
  "agents": {"code-reviewer": {"account": "work"}}
}`
	tests := []struct {
		name         string
		cfg          string
		author       PRAuthor
		wantMessages bool
		want         string
	}{
		{"author found", `{}`, PRAuthor{WindowID: "@12", Pane: "%34"}, true, "@12.%34"},
		{"author not found", `{}`, PRAuthor{}, false, "No live agentctl session"},
		{"author same account", accounts, PRAuthor{WindowID: "@12", Pane: "%34", Account: "work"}, true, "@12.%34"},
		{"author other account", accounts, PRAuthor{WindowID: "@12", Pane: "%34"}, false, "different Claude account"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := launchtest.Setup(t, tt.cfg, map[string]string{"code-reviewer": "---\nname: code-reviewer\n---\nReview.\n"})
			if err := os.MkdirAll(filepath.Join(env.Home, "work-claude"), 0o700); err != nil {
				t.Fatal(err)
			}
			prevOrch, prevParent, prevExec := resolveOrchestratorFn, parentAccountFn, execPath
			resolveOrchestratorFn = func() Orchestrator { return Orchestrator{} }
			parentAccountFn = func() string { return "" }
			execPath = func() (string, error) { return "/opt/bin/agentctl", nil }
			t.Cleanup(func() { resolveOrchestratorFn, parentAccountFn, execPath = prevOrch, prevParent, prevExec })

			rec := launchtest.Capture(t)
			pr := samplePullRequest()
			pr.Author = tt.author
			res, err := Launch(Request{AgentName: "code-reviewer", Mode: ModeReviewPR, ReviewPR: &pr, WorkDir: env.Project, UseWorktree: &no, Background: true})
			if err != nil {
				t.Fatalf("Launch: %v", err)
			}
			if res.MessagesAuthor != tt.wantMessages {
				t.Errorf("MessagesAuthor = %v, want %v", res.MessagesAuthor, tt.wantMessages)
			}
			if pr.Author.crossAccount {
				t.Error("Launch mutated the caller's PullRequest")
			}
			l := rec.Only()
			sys := launchedSystemPrompt(t, l)
			if !strings.Contains(sys, tt.want) {
				t.Errorf("system prompt missing %q", tt.want)
			}
			if strings.Contains(sys, finishingMarker) {
				t.Error("reviewer got finishing steps")
			}
			if !strings.Contains(l.Command, "--permission-mode 'auto'") || !strings.Contains(l.Command, "Review PR #42: Add widgets") {
				t.Errorf("command = %s", l.Command)
			}
		})
	}
}

func TestLaunchVerifyAgent(t *testing.T) {
	no := false
	tests := []struct {
		name    string
		verify  error
		wantErr bool
	}{
		{"verified launches", nil, false},
		{"refused does not launch", errors.New("not approved"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := launchtest.Setup(t, `{}`, map[string]string{"code-reviewer": "---\nname: code-reviewer\n---\nReview.\n"})
			prevOrch, prevParent, prevExec := resolveOrchestratorFn, parentAccountFn, execPath
			resolveOrchestratorFn = func() Orchestrator { return Orchestrator{} }
			parentAccountFn = func() string { return "" }
			execPath = func() (string, error) { return "/opt/bin/agentctl", nil }
			t.Cleanup(func() { resolveOrchestratorFn, parentAccountFn, execPath = prevOrch, prevParent, prevExec })

			rec := launchtest.Capture(t)
			pr := samplePullRequest()
			var gotDirs []string
			_, err := Launch(Request{AgentName: "code-reviewer", Mode: ModeReviewPR, ReviewPR: &pr, WorkDir: env.Project,
				UseWorktree: &no, Background: true, VerifyAgent: func(dirs ...string) error {
					gotDirs = dirs
					return tt.verify
				}})
			if (err != nil) != tt.wantErr {
				t.Fatalf("Launch err = %v, wantErr %v", err, tt.wantErr)
			}
			if len(gotDirs) != 2 || gotDirs[0] != env.Project || gotDirs[1] != env.Project {
				t.Errorf("VerifyAgent dirs = %v, want the launch and repo dirs", gotDirs)
			}
			if n := len(rec.Launches()); n != map[bool]int{true: 0, false: 1}[tt.wantErr] {
				t.Errorf("launches = %d", n)
			}
		})
	}
}

func TestPromptFor(t *testing.T) {
	if got := PromptFor(Request{CustomPrompt: "  full\nprompt  "}); got != "full\nprompt" {
		t.Errorf("got %q", got)
	}
	if got := PromptFor(Request{Ticket: 3, CustomPrompt: "x"}); got != "" {
		t.Errorf("ticket dispatch got %q", got)
	}
}
