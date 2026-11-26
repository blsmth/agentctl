package dispatch

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	ctxstore "github.com/blsmth/agentctl/internal/context"
	"github.com/blsmth/agentctl/internal/shellquote"
)

// TestSystemFileStableAcrossLaunches is the cache contract: two launches of
// the same agent and mode, with different tasks, worktrees, windows,
// orchestrators and start times, write byte-identical system prompt files.
func TestSystemFileStableAcrossLaunches(t *testing.T) {
	standing := ctxstore.Entry{Key: "go-style", Agent: "go-engineer", Content: "Prefer table tests."}
	type launch struct {
		req     Request
		facts   launchFacts
		entries []ctxstore.Entry
	}
	pr := func(n int, sha string, author PRAuthor) *PullRequest {
		return &PullRequest{Number: n, Title: "PR " + sha, HeadRef: "feature-" + sha, HeadSHA: sha, Body: "body " + sha,
			Files: []string{sha + ".go"}, Extra: "focus " + sha, Author: author}
	}
	tests := []struct {
		name string
		a, b launch
	}{
		{"dispatch", launch{
			req: Request{AgentName: "go-engineer", Mode: ModeDispatch, Ticket: 42, TicketTitle: "Fix the widget", TicketBody: "Widget broken.",
				RepoSlug: "o/r", Orchestrator: Orchestrator{WindowID: "@35", Pane: "%35", Name: "lead"}, ExtraInstructions: "be quick"},
			facts:   launchFacts{WorkDir: "/r/.worktrees/a", RepoDir: "/r", Branch: "agent/go-engineer/issue-42", InWorktree: true, Window: "go-engineer:3", Started: time.Unix(1, 0)},
			entries: []ctxstore.Entry{standing, {Key: "ticket-42-spec", Agent: "go-engineer", Content: "spec 42"}},
		}, launch{
			req: Request{AgentName: "go-engineer", Mode: ModeDispatch, Ticket: 7, TicketTitle: "Other", TicketBody: "Something else entirely.",
				RepoSlug: "o/r", Orchestrator: Orchestrator{WindowID: "@99", Pane: "%120"}},
			facts:   launchFacts{WorkDir: "/r/.worktrees/b", RepoDir: "/r", Branch: "agent/go-engineer/issue-7", InWorktree: true, Window: "go-engineer:4", Started: time.Unix(99999, 0)},
			entries: []ctxstore.Entry{standing, {Key: "ticket-7-spec", Agent: "go-engineer", Content: "spec 7"}},
		}},
		{"adhoc", launch{
			req:     Request{AgentName: "go-engineer", Mode: ModeAdhoc, CustomPrompt: "Do A.", PromptKey: "a-prompt", RepoSlug: "o/r", Orchestrator: Orchestrator{WindowID: "@1", Pane: "%1"}},
			facts:   launchFacts{WorkDir: "/r/.worktrees/x", RepoDir: "/r", Branch: "agent/go-engineer/adhoc-1", InWorktree: true, Window: "go-engineer", Started: time.Unix(5, 0)},
			entries: []ctxstore.Entry{standing, {Key: "a-prompt", Agent: "lead", Content: "Do A."}},
		}, launch{
			req:     Request{AgentName: "go-engineer", Mode: ModeAdhoc, CustomPrompt: strings.Repeat("Do B. ", 400), RepoSlug: "o/r", Orchestrator: Orchestrator{WindowID: "@2", Pane: "%9", Name: "orch-name-x"}},
			facts:   launchFacts{WorkDir: "/r/.worktrees/y", RepoDir: "/r", Branch: "agent/go-engineer/adhoc-2", InWorktree: true, Window: "go-engineer:2", Started: time.Unix(6, 0)},
			entries: []ctxstore.Entry{standing},
		}},
		{"review-pr", launch{
			req: Request{AgentName: "code-reviewer", Mode: ModeReviewPR, RepoSlug: "o/r", Orchestrator: Orchestrator{WindowID: "@35", Pane: "%35"},
				ReviewPR: pr(344, "aaa", PRAuthor{WindowID: "@40", Pane: "%41"})},
			facts: launchFacts{WorkDir: "/r/.worktrees/r1", RepoDir: "/r", Branch: "agent/code-reviewer/review-pr-344-1", InWorktree: true, Window: "code-reviewer", Started: time.Unix(7, 0)},
		}, launch{
			req: Request{AgentName: "code-reviewer", Mode: ModeReviewPR, RepoSlug: "o/r", Orchestrator: Orchestrator{WindowID: "@36", Pane: "%50", Name: "orch-name-n"},
				ReviewPR: pr(12, "bbb", PRAuthor{})},
			facts: launchFacts{WorkDir: "/r/.worktrees/r2", RepoDir: "/r", Branch: "agent/code-reviewer/review-pr-12-2", InWorktree: true, Window: "code-reviewer:2", Started: time.Unix(8, 0)},
		}},
		{"triage", launch{
			req:   Request{AgentName: "triager", Mode: ModeTriage, Ticket: 1, TicketTitle: "a", TicketBody: "TRIAGE-BODY-A", RepoSlug: "o/r"},
			facts: launchFacts{WorkDir: "/r/.worktrees/t1", RepoDir: "/r", Branch: "b1", InWorktree: true, Started: time.Unix(9, 0)},
		}, launch{
			req:   Request{AgentName: "triager", Mode: ModeTriage, Ticket: 2, TicketTitle: "b", TicketBody: "TRIAGE-BODY-B", RepoSlug: "o/r"},
			facts: launchFacts{WorkDir: "/r/.worktrees/t2", RepoDir: "/r", Branch: "b2", InWorktree: true, Started: time.Unix(10, 0)},
		}},
		{"custom", launch{
			req:   Request{AgentName: "x", Mode: ModeCustom, CustomPrompt: "Instructions one.", RepoSlug: "o/r"},
			facts: launchFacts{WorkDir: "/r/.worktrees/c1", RepoDir: "/r", Branch: "c1", InWorktree: true, Started: time.Unix(11, 0)},
		}, launch{
			req:   Request{AgentName: "x", Mode: ModeCustom, CustomPrompt: "Instructions two.", RepoSlug: "o/r"},
			facts: launchFacts{WorkDir: "/r/.worktrees/c2", RepoDir: "/r", Branch: "c2", InWorktree: true, Started: time.Unix(12, 0)},
		}},
		{"bare with prompt", launch{
			req:   Request{AgentName: "lead", Mode: ModeBare, CustomPrompt: "hello", RepoSlug: "o/r", ExtraInstructions: "EXTRA-SHORT"},
			facts: launchFacts{WorkDir: "/r/.worktrees/l1", RepoDir: "/r", Branch: "l1", InWorktree: true, Started: time.Unix(13, 0)},
		}, launch{
			req:   Request{AgentName: "lead", Mode: ModeBare, CustomPrompt: "something else", RepoSlug: "o/r"},
			facts: launchFacts{WorkDir: "/r/.worktrees/l2", RepoDir: "/r", Branch: "l2", InWorktree: true, Started: time.Unix(14, 0)},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := modes[tt.a.req.Mode]
			a := buildLaunchPrompt(tt.a.req, spec, tt.a.facts, tt.a.entries, "shared rules")
			b := buildLaunchPrompt(tt.b.req, spec, tt.b.facts, tt.b.entries, "shared rules")
			if a.System != b.System {
				t.Errorf("system files differ between launches:\n--- a\n%s\n--- b\n%s", a.System, b.System)
			}
			if a.Message == b.Message {
				t.Errorf("first messages are equal; the per-launch content is missing:\n%s", a.Message)
			}
			for _, l := range []struct {
				launch
				lp launchPrompt
			}{{tt.a, a}, {tt.b, b}} {
				for _, perLaunch := range []string{l.facts.WorkDir, l.facts.Branch, l.req.Orchestrator.WindowID, l.req.Orchestrator.Pane, l.req.Orchestrator.Name, l.req.TicketBody, l.req.CustomPrompt, l.req.ExtraInstructions} {
					if perLaunch != "" && strings.Contains(l.lp.System, perLaunch) {
						t.Errorf("system file contains per-launch value %q", perLaunch)
					}
				}
				if strings.Contains(l.lp.System, "started") || strings.Contains(l.lp.System, "Started") {
					t.Error("system file mentions a start time")
				}
			}
		})
	}
}

// TestFirstMessageLayout pins the order of a worker's first message: task,
// launch facts, then the directive and finishing steps, last.
func TestFirstMessageLayout(t *testing.T) {
	req := Request{
		AgentName: "go-engineer", Mode: ModeDispatch, Ticket: 42, TicketTitle: "Fix the widget",
		TicketBody: "The widget is broken.", RepoSlug: "blsmth/agentctl", ExtraInstructions: "Mind the docs.",
		Orchestrator: Orchestrator{WindowID: "@35", Pane: "%35", Name: "lead"},
	}
	facts := launchFacts{WorkDir: "/r/.worktrees/w", RepoDir: "/r", Branch: "agent/go-engineer/issue-42", InWorktree: true,
		Window: "go-engineer:3", Started: time.Date(2026, 11, 24, 21, 5, 43, 0, time.UTC)}
	entries := []ctxstore.Entry{
		{Key: "ticket-42-spec", Agent: "go-engineer", Content: "THE SPEC"},
		{Key: "standing", Agent: "go-engineer", Content: "STANDING"},
	}
	lp := buildLaunchPrompt(req, modes[ModeDispatch], facts, entries, "")
	msg := lp.Message

	order := []string{
		"## Issue #42: Fix the widget\n\nThe widget is broken.",
		"## Context: ticket-42-spec",
		AdditionalInstructionsHeading + "\n\nMind the docs.",
		"## Launch facts",
		"- Started: 2026-11-24T21:05:43Z",
		"- Window: go-engineer:3",
		"- Working directory: /r/.worktrees/w",
		"- Branch: agent/go-engineer/issue-42 (a git worktree)",
		"- Orchestrator: tmux window @35, pane %35 (`@35.%35`); its session name was lead at launch",
		"Implement issue #42 (Fix the widget) and open a PR when done.",
		"When you are finished",
		"git push -u origin agent/go-engineer/issue-42",
		"SendMessage to your orchestrator (window @35) with the PR URL.",
	}
	last := -1
	for _, want := range order {
		i := strings.Index(msg, want)
		if i < 0 {
			t.Fatalf("first message missing %q:\n%s", want, msg)
		}
		if i <= last {
			t.Errorf("%q is out of order in:\n%s", want, msg)
		}
		last = i
	}
	if !strings.HasSuffix(msg, "do NOT merge to main yourself.") {
		t.Errorf("finishing steps are not the last thing in the message:\n%s", msg)
	}
	if strings.Contains(msg, "STANDING") {
		t.Error("a context entry other than the ticket spec was injected")
	}
	if strings.Contains(lp.System, "STANDING") || strings.Contains(lp.System, "THE SPEC") {
		t.Error("system file should hold neither the standing entry nor the ticket spec")
	}
}

// TestSystemFileLayout checks the system file's content and order: the
// preamble first, the session block (without a timestamp) last.
func TestSystemFileLayout(t *testing.T) {
	req := Request{AgentName: "go-engineer", Mode: ModeDispatch, Ticket: 42, TicketTitle: "T", TicketBody: "B", RepoSlug: "blsmth/agentctl",
		Orchestrator: Orchestrator{WindowID: "@35", Pane: "%35"}}
	got := buildSystemPrompt(req, modes[ModeDispatch], "/w", "agent/go-engineer/issue-42", true,
		[]ctxstore.Entry{{Key: "k", Content: "v"}}, "SHARED")
	order := []string{
		"## agentctl Runtime Environment",
		"### Your orchestrator",
		"## Shared Conventions",
		"Execute this ticket autonomously",
		"You are working in a git worktree on its own branch",
		"## Session\n\n- Agent: go-engineer\n- Repo: blsmth/agentctl\n- Mode: dispatch",
	}
	last := -1
	for _, want := range order {
		i := strings.Index(got, want)
		if i < 0 {
			t.Fatalf("system file missing %q:\n%s", want, got)
		}
		if i <= last {
			t.Errorf("%q is out of order", want)
		}
		last = i
	}
	for _, absent := range []string{"started:", "ticket:", "## Issue #42", "When you are finished", "git push", "---\nagent:", "## Shared Context", "## Context: k"} {
		if strings.Contains(got, absent) {
			t.Errorf("system file contains %q", absent)
		}
	}
}

// TestOversizedMessageFallsBackToSystemFile covers the ARG_MAX guard: a task
// block that would push the message over maxInlineMessage moves to the end
// of the system file, and the message keeps its launch facts and steps.
func TestOversizedMessageFallsBackToSystemFile(t *testing.T) {
	huge := strings.Repeat("spec line\n", maxInlineMessage/10+1)
	req := Request{AgentName: "go-engineer", Mode: ModeDispatch, Ticket: 9, TicketTitle: "Big", TicketBody: huge, RepoSlug: "o/r"}
	facts := launchFacts{WorkDir: "/w", RepoDir: "/w", Branch: "b", InWorktree: true}
	lp := buildLaunchPrompt(req, modes[ModeDispatch], facts, nil, "")
	if !lp.TaskInSystem {
		t.Fatal("TaskInSystem = false for an oversized task")
	}
	if len(lp.Message) > maxInlineMessage || strings.Contains(lp.Message, "spec line") {
		t.Errorf("message still carries the task (%d bytes)", len(lp.Message))
	}
	for _, want := range []string{"Task material", "## Launch facts", "Implement issue #9", "git push -u origin b"} {
		if !strings.Contains(lp.Message, want) {
			t.Errorf("message missing %q", want)
		}
	}
	if !strings.Contains(lp.System, taskMaterialHeading+"\n\n## Issue #9: Big") {
		t.Error("system file does not end with the task block")
	}

	small := buildLaunchPrompt(Request{AgentName: "go-engineer", Mode: ModeDispatch, Ticket: 9, TicketBody: "small", RepoSlug: "o/r"}, modes[ModeDispatch], facts, nil, "")
	if small.TaskInSystem || !strings.HasPrefix(lp.System, small.System) {
		t.Error("the fallback should only append to the stable system file")
	}
}

// TestFirstMessageSurvivesShellExec runs a first message at the size cap,
// full of shell metacharacters, through the same quoting the launch script
// uses and a real exec (argv, so ARG_MAX applies), and checks it arrives
// byte for byte.
func TestFirstMessageSurvivesShellExec(t *testing.T) {
	printf := "/usr/bin/printf"
	if _, err := os.Stat(printf); err != nil {
		t.Skip("no /usr/bin/printf")
	}
	nasty := "It's `rm -rf /` and $(whoami) and \"quotes\" and \\ and $HOME\n\ttab\n"
	body := strings.Repeat(nasty, maxInlineMessage/len(nasty))
	req := Request{AgentName: "a", Mode: ModeAdhoc, CustomPrompt: body[:len(body)-2500]}
	lp := buildLaunchPrompt(req, modes[ModeAdhoc], launchFacts{WorkDir: "/w", Started: time.Unix(0, 0)}, nil, "")
	if lp.TaskInSystem {
		t.Fatalf("message of %d bytes moved to the system file", len(lp.Message))
	}
	out, err := exec.Command("/bin/sh", "-c", "exec "+printf+" '%s' "+shellquote.Quote(lp.Message)).Output()
	if err != nil {
		t.Fatalf("exec with a %d-byte message: %v", len(lp.Message), err)
	}
	if string(out) != lp.Message {
		t.Errorf("message changed through the shell (%d bytes in, %d out)", len(lp.Message), len(out))
	}
}

// TestBareLaunchPrompt:a bare launch's first message is the human's own
// text, with a one-line worktree prefix only when there is a worktree.
func TestBareLaunchPrompt(t *testing.T) {
	spec := modes[ModeBare]
	wt := launchFacts{WorkDir: "/r/.worktrees/l", RepoDir: "/r", Branch: "agent/lead/adhoc-1", InWorktree: true, Started: time.Unix(1, 0)}
	tests := []struct {
		name      string
		req       Request
		facts     launchFacts
		wantMsg   string
		sysHas    []string
		sysHasNot []string
	}{
		{name: "no prompt, shared checkout", req: Request{AgentName: "lead", Mode: ModeBare}, facts: launchFacts{WorkDir: "/r"}, wantMsg: ""},
		{name: "prompt, shared checkout", req: Request{AgentName: "lead", Mode: ModeBare, CustomPrompt: "Plan the release."},
			facts: launchFacts{WorkDir: "/r"}, wantMsg: "Plan the release."},
		{name: "prompt in a worktree", req: Request{AgentName: "lead", Mode: ModeBare, CustomPrompt: "Plan the release."}, facts: wt,
			wantMsg:   "[agentctl launch facts: git worktree /r/.worktrees/l, branch agent/lead/adhoc-1]\n\nPlan the release.",
			sysHas:    []string{"launch facts of your first message"},
			sysHasNot: []string{"/r/.worktrees/l", "agent/lead/adhoc-1"}},
		{name: "no prompt in a worktree", req: Request{AgentName: "lead", Mode: ModeBare}, facts: wt, wantMsg: "",
			sysHas: []string{"git worktree at /r/.worktrees/l on branch agent/lead/adhoc-1"}},
		{name: "extra instructions follow the prompt", req: Request{AgentName: "lead", Mode: ModeBare, CustomPrompt: "Go.", ExtraInstructions: "Be brief."},
			facts: launchFacts{WorkDir: "/r"}, wantMsg: "Go.\n\n" + AdditionalInstructionsHeading + "\n\nBe brief."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lp := buildLaunchPrompt(tt.req, spec, tt.facts, nil, "")
			if lp.Message != tt.wantMsg {
				t.Errorf("message = %q, want %q", lp.Message, tt.wantMsg)
			}
			for _, s := range tt.sysHas {
				if !strings.Contains(lp.System, s) {
					t.Errorf("system file missing %q", s)
				}
			}
			for _, s := range tt.sysHasNot {
				if strings.Contains(lp.System, s) {
					t.Errorf("system file contains %q", s)
				}
			}
			if strings.Contains(lp.System, "Started") || strings.Contains(lp.Message, "## Launch facts") {
				t.Error("a bare launch gets no launch-facts block")
			}
		})
	}

	t.Run("huge prompt goes to the system file", func(t *testing.T) {
		huge := "Huge task.\n" + strings.Repeat("x", maxInlineMessage)
		lp := buildLaunchPrompt(Request{AgentName: "lead", Mode: ModeBare, CustomPrompt: huge}, spec, launchFacts{WorkDir: "/r"}, nil, "")
		if !lp.TaskInSystem || !strings.Contains(lp.System, "## Task\n\nHuge task.") || strings.Contains(lp.Message, "Huge task.") {
			t.Errorf("huge bare prompt not moved: TaskInSystem=%v message=%q", lp.TaskInSystem, lp.Message)
		}
	})
}

// TestTaskBlockPerMode checks each mode's task block lands in the message.
func TestTaskBlockPerMode(t *testing.T) {
	tests := []struct {
		name string
		req  Request
		want []string
	}{
		{"adhoc with source", Request{AgentName: "a", Mode: ModeAdhoc, CustomPrompt: "say hi", PromptKey: "k", Template: "tpl"},
			[]string{"## Task\n\nSource: context key `k`\nSource: template `tpl`\n\nsay hi", `Execute the task under "## Task"`}},
		{"adhoc short prompt", Request{AgentName: "a", Mode: ModeAdhoc, CustomPrompt: "say hi"}, []string{"## Task\n\nsay hi"}},
		{"custom", Request{AgentName: "a", Mode: ModeCustom, CustomPrompt: "Do the custom thing."},
			[]string{"## Instructions\n\nDo the custom thing.", `Follow the instructions under "## Instructions".`}},
		{"custom with ticket", Request{AgentName: "a", Mode: ModeCustom, Ticket: 3, TicketTitle: "T", TicketBody: "CUSTOM-BODY", CustomPrompt: "CUSTOM-PROMPT"},
			[]string{"## Issue #3: T\n\nCUSTOM-BODY", "## Instructions\n\nCUSTOM-PROMPT", "for issue #3 (T)"}},
		{"discuss", Request{AgentName: "a", Mode: ModeDiscuss, Ticket: 4, TicketTitle: "T", TicketBody: "BODY"},
			[]string{"## Issue #4: T\n\nBODY", "Discuss issue #4 (T) with me. Do not implement it yet."}},
		{"triage", Request{AgentName: "a", Mode: ModeTriage, Ticket: 5, TicketBody: "BODY"},
			[]string{"## Issue #5", "Triage issue #5."}},
		{"review", Request{AgentName: "a", Mode: ModeReview, Ticket: 6, TicketTitle: "T", TicketBody: "BODY"},
			[]string{"## Issue #6: T", "Review the work on issue #6 (T)."}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lp := buildLaunchPrompt(tt.req, modes[tt.req.Mode], launchFacts{WorkDir: "/w"}, nil, "")
			for _, w := range tt.want {
				if !strings.Contains(lp.Message, w) {
					t.Errorf("message missing %q:\n%s", w, lp.Message)
				}
			}
			if tt.req.CustomPrompt != "" && strings.Contains(lp.System, tt.req.CustomPrompt) {
				t.Error("system file contains the prompt")
			}
			if tt.req.TicketBody != "" && strings.Contains(lp.System, tt.req.TicketBody) {
				t.Error("system file contains the ticket body")
			}
		})
	}
}

// TestTicketBodyOnlyInMessage covers every mode: a ticket body is part of the
// first message (once) and never of the system file, which must stay stable.
func TestTicketBodyOnlyInMessage(t *testing.T) {
	const sentinel = "SENTINEL-TICKET-BODY"
	for mode, spec := range modes {
		t.Run(string(mode), func(t *testing.T) {
			req := Request{AgentName: "a", Mode: mode, Ticket: 99, TicketTitle: "Sentinel", TicketBody: sentinel}
			lp := buildLaunchPrompt(req, spec, launchFacts{WorkDir: "/w"}, nil, "")
			if strings.Contains(lp.System, sentinel) {
				t.Errorf("system file embeds the ticket body")
			}
			want := 0
			if spec.IncludeTicketBody {
				want = 1
			}
			if got := strings.Count(lp.Message, sentinel); got != want {
				t.Errorf("ticket body appears %d times in the message, want %d", got, want)
			}
		})
	}
}

// TestPromptKeyEntryNotDuplicated: the --prompt-key entry is the task, so it
// is neither repeated in the message nor injected into the system file.
func TestPromptKeyEntryNotDuplicated(t *testing.T) {
	req := Request{AgentName: "a", Mode: ModeAdhoc, CustomPrompt: "THE TASK", PromptKey: "k"}
	lp := buildLaunchPrompt(req, modes[ModeAdhoc], launchFacts{WorkDir: "/w"}, []ctxstore.Entry{{Key: "k", Agent: "a", Content: "THE TASK"}}, "")
	if strings.Count(lp.Message, "THE TASK") != 1 || strings.Contains(lp.System, "THE TASK") {
		t.Errorf("prompt-key entry duplicated:\nsystem:\n%s\nmessage:\n%s", lp.System, lp.Message)
	}
}

// TestSessionBlockRepoFallback: without a slug the repo is named from the
// target checkout, never the per-launch worktree directory.
func TestSessionBlockRepoFallback(t *testing.T) {
	got := sessionBlock(Request{AgentName: "a", Mode: ModeAdhoc}, launchFacts{WorkDir: "/nonexistent/.worktrees/agent-a-adhoc-1234", RepoDir: "/nonexistent/myrepo"})
	if !strings.Contains(got, "- Repo: myrepo") {
		t.Errorf("sessionBlock = %q, want repo myrepo", got)
	}
}
