package cmd

import (
	"fmt"
	"os"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/dispatch"
	"github.com/blsmth/agentctl/internal/dispatchapproval"
	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/mailbox"
	"github.com/blsmth/agentctl/internal/signal"
	"github.com/blsmth/agentctl/internal/tmux"
)

// defaultReviewAgent is the agent `dispatch --review-pr` launches when
// --agent is not given. It lives in config because the dispatch_approval
// review exemption (internal/hooks) keys on the same name.
const defaultReviewAgent = config.DefaultReviewAgent

// Seams for tests: every one of these reaches gh, tmux or another session.
var (
	fetchPR          = dispatch.FetchPR
	launchReview     = dispatch.Launch
	loadFleetGroups  = fleet.Load
	enqueueNoticeIn  = mailbox.EnqueueIn
	reviewerMainPane = func(windowID string) string {
		id, err := tmux.MainPaneOf(tmux.SessionName, windowID, "", "")
		if err != nil {
			logging.Warn("dispatch: main pane of reviewer %s: %v", windowID, err)
		}
		return id
	}
)

// prAuthorWindow is the live window that owns a PR's branch, with what the
// introduction notice needs to reach it.
type prAuthorWindow struct {
	dispatch.PRAuthor
	RuntimeDir string // the author session's .agentctl dir (its mailbox)
	SessionID  string // the author's Claude session, for the notice's ToSession
}

// dispatchReviewPR launches a reviewer on open PR opts.ReviewPR (ModeReviewPR)
// and, when the author's window is known and reachable, introduces the
// reviewer to it with an agentctl notice. opts.Prompt, already resolved from
// --prompt-key, is extra review instructions.
func dispatchReviewPR(opts DispatchOptions) (string, string, error) {
	repoSlug, err := resolveDispatchRepo(opts.Dir, opts.Repo)
	if err != nil {
		return "", "", err
	}
	pr, err := fetchPR(repoSlug, opts.ReviewPR)
	if err != nil {
		return "", "", err
	}
	pr.Extra = opts.Prompt

	agentName := opts.Agent
	if agentName == "" {
		agentName = defaultReviewAgent
		if err := validateAgentName(agentName, opts.Dir); err != nil {
			return "", "", fmt.Errorf("--review-pr launches %s by default: %w", agentName, err)
		}
	}
	workDir := opts.Dir
	if workDir == "" {
		workDir = resolveAgentDir(agentName)
	}
	gate := newReviewerGate(agentName, opts.Detached)
	if err := gate.check(workDir); err != nil {
		return "", "", err
	}

	groups, err := loadFleetGroups()
	if err != nil {
		logging.Warn("dispatch: loading fleet for PR #%d author: %v", pr.Number, err)
	}
	author, err := findPRAuthor(groups, pr.HeadRef, opts.Author)
	if err != nil {
		return "", "", err
	}
	pr.Author = author.PRAuthor
	if author.Known() {
		logging.Log("dispatch: PR #%d author is window %s (pane %s)", pr.Number, author.WindowID, author.Pane)
	} else {
		logging.Log("dispatch: PR #%d: no live window on branch %q", pr.Number, pr.HeadRef)
	}

	useWorktree := !opts.NoWorktree
	res, err := launchReview(dispatch.Request{
		AgentName:   agentName,
		Mode:        dispatch.ModeReviewPR,
		ReviewPR:    &pr,
		PromptKey:   opts.PromptKey,
		WorkDir:     workDir,
		UseWorktree: &useWorktree,
		RepoSlug:    repoSlug,
		Model:       opts.Model,
		Backend:     opts.Backend,
		Account:     opts.Account,
		Harness:     opts.Harness,
		Background:  true,
		VerifyAgent: gate.check,
	})
	if err != nil {
		return "", "", err
	}
	if res.MessagesAuthor {
		introduceReviewer(author, res, pr.Number)
	}
	return res.WindowID, res.AgentName, nil
}

// findPRAuthor returns the window that owns the PR: the --author window when
// override is set (an error when no agentctl session has it), else the first
// live window whose worktree branch is headRef, across every agentctl
// session. The zero value means none was found.
func findPRAuthor(groups []fleet.Group, headRef, override string) (prAuthorWindow, error) {
	var found *prAuthorWindow
	for _, g := range groups {
		for _, r := range g.Rows {
			match := r.WindowID == override
			if override == "" {
				match = headRef != "" && r.Branch == headRef
			}
			if !match || r.WindowID == "" {
				continue
			}
			w := prAuthorWindow{
				PRAuthor:   dispatch.PRAuthor{WindowID: r.WindowID, Pane: r.MainPane, Account: r.Account},
				RuntimeDir: g.RuntimeDir,
				SessionID:  r.SessionID,
			}
			// A window linked into a second session shows up there too,
			// without its record; prefer the row that has one.
			if found == nil || found.SessionID == "" && w.SessionID != "" {
				found = &w
			} else if found.WindowID != w.WindowID {
				logging.Log("dispatch: branch %q is also checked out in window %s; using %s", headRef, w.WindowID, found.WindowID)
			}
		}
	}
	if found == nil {
		if override != "" {
			return prAuthorWindow{}, fmt.Errorf("--author %s: no agentctl agent window with that ID", override)
		}
		return prAuthorWindow{}, nil
	}
	return *found, nil
}

// introduceReviewer queues an agentctl notice in the author's window (in the
// author session's own mailbox) naming the reviewer by window and pane, so
// the author can tell the reviewer's messages apart from anyone else's.
func introduceReviewer(author prAuthorWindow, res dispatch.Result, pr int) {
	pane := reviewerMainPane(res.WindowID)
	_, err := enqueueNoticeIn(author.RuntimeDir, mailbox.Message{
		From:      res.WindowID,
		To:        author.WindowID,
		ToSession: author.SessionID,
		Kind:      mailbox.KindFYI,
		Subject:   fmt.Sprintf("review of PR #%d", pr),
		Body:      dispatch.ReviewAuthorNotice(res.AgentName, res.WindowID, pane, pr),
	})
	if err != nil {
		logging.Warn("dispatch: introducing reviewer %s to author %s: %v", res.WindowID, author.WindowID, err)
		return
	}
	logging.Log("dispatch: queued reviewer notice for author %s (PR #%d)", author.WindowID, pr)
}

// Seams for the reviewer approval check (reviewerGate).
var (
	callerIsAgentSession = dispatch.CallerIsAgentSession
	approvalWindowID     = signal.ResolveWindowID
	consumeApproval      = dispatchapproval.Consume
	dispatchArgs         = func() []string { return dispatchArgsFrom(os.Args) }
	reviewerResolution   = agent.ResolvesGlobal
	loadGateConfig       = config.Load
)

// reviewerGate closes the gap in the dispatch_approval review exemption. The
// hook exempts a review by the NAME config.DefaultReviewAgent, but discovery
// lets a project-local .claude/agents/<name>.md (or an extends chain through
// one) shadow the global definition, and the hook cannot see the directory
// this process runs in. So when the reviewer does not resolve to the global
// file, a launch from an agent session needs proof that a human approved
// THIS dispatch: a dispatchapproval marker the hook recorded when the popup
// was approved, keyed by window and arguments, consumed here.
//
// No proof is needed for any other agent (the hook always gates those
// unless dispatch_approval is "off"), under "off", for a Detached launch,
// or for a person in a plain shell (no tracked Claude session), whom the
// popup never applies to.
type reviewerGate struct {
	agent  string
	cfg    *config.Config
	needed bool // proof is required when the reviewer is local
	// claimed is set once the approval marker has been looked up; approval
	// is the result (nil = approved). The marker is consumed on the first
	// check whatever the reviewer resolves to, so an approval is never left
	// live for a later run of the same command.
	claimed  bool
	approval error
}

func newReviewerGate(agentName string, detached bool) *reviewerGate {
	g := &reviewerGate{agent: agentName}
	if agentName != defaultReviewAgent || detached {
		return g
	}
	g.cfg = loadGateConfig()
	if g.cfg.EffectiveDispatchApproval() == config.DispatchApprovalOff {
		return g
	}
	g.needed = callerIsAgentSession()
	return g
}

// check resolves the reviewer from dirs (plus the cwd, as the launch's body
// resolution does) and, when it is not the global definition, consumes the
// approval marker or refuses. Launch calls it again once the worktree exists;
// one consumed marker covers both calls.
func (g *reviewerGate) check(dirs ...string) error {
	if !g.needed {
		return nil
	}
	if !g.claimed {
		g.claimed = true
		g.approval = consumeApproval(approvalWindowID(), dispatchArgs())
	}
	if cwd, err := os.Getwd(); err == nil {
		dirs = append(dirs, cwd)
	}
	res := reviewerResolution(g.cfg, g.agent, dirs...)
	if res.Global {
		return nil
	}
	if err := g.approval; err != nil {
		logging.Warn("dispatch: refusing review by %s (%s): %v", g.agent, res.Reason, err)
		return fmt.Errorf("--review-pr: %s here is not your global agent definition (%s), and no human approval was recorded for this dispatch (%v). "+
			"A project-local reviewer launches only after the approval popup: rerun this exact command as its own Bash call, "+
			"starting with agentctl (no cd, env, variable assignments or other commands before it), from the directory it should run in "+
			"(cd there in a separate call first), so the popup is shown", g.agent, res.Reason, err)
	}
	logging.Log("dispatch: review by %s (%s) launched with a recorded human approval", g.agent, res.Reason)
	return nil
}

// dispatchArgsFrom returns the words after "dispatch" in argv, which is what
// the hook keyed the approval on; nil when argv is not a dispatch.
func dispatchArgsFrom(argv []string) []string {
	for i := 1; i < len(argv); i++ {
		if argv[i] == "dispatch" {
			return append([]string{}, argv[i+1:]...)
		}
	}
	return nil
}
