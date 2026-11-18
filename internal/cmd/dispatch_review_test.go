package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/dispatch"
	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/mailbox"
	"github.com/blsmth/agentctl/internal/state"
)

func TestValidateDispatchOptions(t *testing.T) {
	tests := []struct {
		name    string
		opts    DispatchOptions
		wantErr string
	}{
		{"issue", DispatchOptions{Issue: 1}, ""},
		{"review-pr alone", DispatchOptions{ReviewPR: 5}, ""},
		{"review-pr with prompt", DispatchOptions{ReviewPR: 5, Prompt: "focus on x"}, ""},
		{"review-pr with prompt-key", DispatchOptions{ReviewPR: 5, PromptKey: "k"}, ""},
		{"review-pr with author", DispatchOptions{ReviewPR: 5, Author: "@12"}, ""},
		{"review-pr and issue", DispatchOptions{ReviewPR: 5, Issue: 1}, "--issue and --review-pr are mutually exclusive"},
		{"issue and prompt", DispatchOptions{Issue: 1, Prompt: "x", Agent: "a"}, "mutually exclusive"},
		{"prompt and prompt-key", DispatchOptions{Prompt: "x", PromptKey: "k", Agent: "a"}, "mutually exclusive"},
		{"nothing", DispatchOptions{}, "one of --issue, --review-pr, --prompt, or --prompt-key is required"},
		{"prompt without agent", DispatchOptions{Prompt: "x"}, "--agent is required"},
		{"author without review-pr", DispatchOptions{Prompt: "x", Agent: "a", Author: "@1"}, "--author requires --review-pr"},
		{"bad author", DispatchOptions{ReviewPR: 5, Author: "go-engineer"}, "invalid --author"},
		{"negative pr", DispatchOptions{ReviewPR: -1}, "positive number"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDispatchOptions(tt.opts)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func authorRow(window, pane, branch, session, account string) fleet.Row {
	return fleet.Row{AgentState: state.AgentState{WindowID: window, MainPane: pane, Branch: branch, SessionID: session, Account: account}}
}

func TestFindPRAuthor(t *testing.T) {
	groups := []fleet.Group{
		{Session: "agentctl-a", RuntimeDir: "/a/.agentctl", Rows: []fleet.Row{
			authorRow("@1", "%10", "agent/x/issue-1", "s1", ""),
			authorRow("@2", "%20", "feature", "", ""), // linked copy without its record
		}},
		{Session: "agentctl-b", RuntimeDir: "/b/.agentctl", Rows: []fleet.Row{
			authorRow("@2", "%20", "feature", "s2", "work"),
			authorRow("@3", "%30", "", "s3", ""),
		}},
	}
	tests := []struct {
		name       string
		head       string
		override   string
		wantWindow string
		wantDir    string
		wantErr    string
	}{
		{"by branch", "agent/x/issue-1", "", "@1", "/a/.agentctl", ""},
		{"prefers the row with a record", "feature", "", "@2", "/b/.agentctl", ""},
		{"no match", "other", "", "", "", ""},
		{"empty head ref matches nothing", "", "", "", "", ""},
		{"override", "agent/x/issue-1", "@3", "@3", "/b/.agentctl", ""},
		{"override not found", "agent/x/issue-1", "@99", "", "", "no agentctl agent window"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := findPRAuthor(groups, tt.head, tt.override)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.WindowID != tt.wantWindow || got.RuntimeDir != tt.wantDir {
				t.Errorf("author = %+v, want window %q dir %q", got, tt.wantWindow, tt.wantDir)
			}
			if tt.wantWindow == "@2" && (got.Pane != "%20" || got.Account != "work" || got.SessionID != "s2") {
				t.Errorf("author fields = %+v", got)
			}
		})
	}
}

// stubReviewDispatch replaces every seam dispatchReviewPR reaches gh, tmux,
// the fleet or another session's mailbox through.
type reviewStubs struct {
	launched *dispatch.Request
	notices  []string // runtimeDir + "|" + To + "|" + ToSession + "|" + Body
	result   dispatch.Result
	pr       dispatch.PullRequest
	prErr    error
	groups   []fleet.Group
}

func stubReviewDispatch(t *testing.T, s *reviewStubs) {
	t.Helper()
	prevFetch, prevLaunch, prevFleet, prevEnqueue, prevPane := fetchPR, launchReview, loadFleetGroups, enqueueNoticeIn, reviewerMainPane
	prevSlug, prevValidate := resolveRepoSlug, validateAgentName
	t.Cleanup(func() {
		fetchPR, launchReview, loadFleetGroups, enqueueNoticeIn, reviewerMainPane = prevFetch, prevLaunch, prevFleet, prevEnqueue, prevPane
		resolveRepoSlug, validateAgentName = prevSlug, prevValidate
	})
	resolveRepoSlug = func(string) (string, error) { return "o/r", nil }
	validateAgentName = func(string, string) error { return nil }
	fetchPR = func(repo string, n int) (dispatch.PullRequest, error) {
		if repo != "o/r" {
			t.Errorf("repo = %q", repo)
		}
		if s.prErr != nil {
			return dispatch.PullRequest{}, s.prErr
		}
		pr := s.pr
		pr.Number = n
		return pr, nil
	}
	launchReview = func(req dispatch.Request) (dispatch.Result, error) {
		r := req
		s.launched = &r
		res := s.result
		res.AgentName = req.AgentName
		return res, nil
	}
	loadFleetGroups = func() ([]fleet.Group, error) { return s.groups, nil }
	enqueueNoticeIn = func(dir string, m mailbox.Message) (string, error) {
		if m.Kind != mailbox.KindFYI || m.From != s.result.WindowID {
			t.Errorf("notice = %+v", m)
		}
		s.notices = append(s.notices, dir+"|"+m.To+"|"+m.ToSession+"|"+m.Body)
		return "id", nil
	}
	reviewerMainPane = func(string) string { return "%41" }
}

func TestDispatchReviewPR(t *testing.T) {
	authorGroups := []fleet.Group{{Session: "agentctl-p", RuntimeDir: "/p/.agentctl", Rows: []fleet.Row{
		authorRow("@12", "%34", "agent/go-engineer/issue-9", "sess-12", ""),
		authorRow("@13", "%35", "", "sess-13", ""),
	}}}
	pr := dispatch.PullRequest{Title: "Add widgets", HeadRef: "agent/go-engineer/issue-9", State: "OPEN"}
	wantNotice := "/p/.agentctl|@12|sess-12|agentctl notice: code-reviewer in @40.%41 is reviewing your PR #42 at the request of your orchestrator; it may message you directly."
	tests := []struct {
		name          string
		opts          DispatchOptions
		groups        []fleet.Group
		messages      bool // Launch reports the author reachable
		prErr         error
		wantAgent     string
		wantAuthor    string
		wantNotice    string
		wantErr       string
		wantExtra     string
		wantPromptKey string
	}{
		{name: "author found", opts: DispatchOptions{ReviewPR: 42}, groups: authorGroups, messages: true,
			wantAgent: "code-reviewer", wantAuthor: "@12", wantNotice: wantNotice},
		{name: "author unreachable (other account)", opts: DispatchOptions{ReviewPR: 42}, groups: authorGroups, messages: false,
			wantAgent: "code-reviewer", wantAuthor: "@12"},
		{name: "author not found", opts: DispatchOptions{ReviewPR: 42}, groups: nil,
			wantAgent: "code-reviewer"},
		{name: "author override", opts: DispatchOptions{ReviewPR: 42, Author: "@13", Agent: "other-reviewer"}, groups: authorGroups, messages: true,
			wantAgent: "other-reviewer", wantAuthor: "@13",
			wantNotice: "/p/.agentctl|@13|sess-13|agentctl notice: other-reviewer in @40.%41 is reviewing your PR #42 at the request of your orchestrator; it may message you directly."},
		{name: "author override not found", opts: DispatchOptions{ReviewPR: 42, Author: "@77"}, groups: authorGroups,
			wantErr: "--author @77"},
		{name: "extra instructions", opts: DispatchOptions{ReviewPR: 42, Prompt: "Focus on tmux.", PromptKey: "k"}, groups: nil,
			wantAgent: "code-reviewer", wantExtra: "Focus on tmux.", wantPromptKey: "k"},
		{name: "closed PR", opts: DispatchOptions{ReviewPR: 42}, prErr: errors.New("PR #42 is closed; only open PRs can be reviewed"),
			wantErr: "PR #42 is closed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &reviewStubs{pr: pr, prErr: tt.prErr, groups: tt.groups,
				result: dispatch.Result{WindowID: "@40", MessagesAuthor: tt.messages}}
			stubReviewDispatch(t, s)
			opts := tt.opts
			opts.Dir = t.TempDir()
			wid, agentName, err := dispatchReviewPR(opts)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				if s.launched != nil {
					t.Error("launched despite the error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if wid != "@40" || agentName != tt.wantAgent {
				t.Errorf("got (%s, %s), want (@40, %s)", wid, agentName, tt.wantAgent)
			}
			req := s.launched
			if req == nil {
				t.Fatal("not launched")
			}
			if req.Mode != dispatch.ModeReviewPR || req.ReviewPR == nil || req.ReviewPR.Number != 42 || req.RepoSlug != "o/r" || req.UseWorktree == nil || !*req.UseWorktree {
				t.Errorf("request = %+v", req)
			}
			if got := req.ReviewPR.Author.WindowID; got != tt.wantAuthor {
				t.Errorf("author = %q, want %q", got, tt.wantAuthor)
			}
			if req.ReviewPR.Extra != tt.wantExtra || req.PromptKey != tt.wantPromptKey {
				t.Errorf("extra = %q key = %q", req.ReviewPR.Extra, req.PromptKey)
			}
			switch {
			case tt.wantNotice == "" && len(s.notices) != 0:
				t.Errorf("notices = %v, want none", s.notices)
			case tt.wantNotice != "" && (len(s.notices) != 1 || s.notices[0] != tt.wantNotice):
				t.Errorf("notices = %v, want %q", s.notices, tt.wantNotice)
			}
		})
	}
}
