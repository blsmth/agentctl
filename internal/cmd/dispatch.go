package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/agentlabel"
	"github.com/blsmth/agentctl/internal/cliout"
	"github.com/blsmth/agentctl/internal/config"
	ctxstore "github.com/blsmth/agentctl/internal/context"
	"github.com/blsmth/agentctl/internal/dispatch"
	"github.com/blsmth/agentctl/internal/dispatchplan"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/prompttemplate"
	"github.com/blsmth/agentctl/internal/textutil"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// repoFlagRE matches a GitHub owner/name slug.
var repoFlagRE = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)

// DispatchOptions holds the dispatch command's flags.
type DispatchOptions struct {
	Issue int
	// ReviewPR is `--review-pr`: review this open pull request (ModeReviewPR).
	ReviewPR int
	// Author is `--author @N`, the --review-pr author's window, overriding
	// detection from the PR branch.
	Author    string
	Prompt    string
	PromptKey string
	// Template is `--template`: a prompt template (internal/prompttemplate)
	// whose body is the prompt and whose agent/model/dir/effort fill unset
	// flags. Prompt, when also set, is added as additional instructions.
	Template   string
	Agent      string
	Dir        string
	Model      string
	Backend    string
	Account    string
	Harness    string
	NoWorktree bool
	Wait       bool
	Timeout    time.Duration
	Repo       string
	// Effort overrides the agent's effort_level (cron jobs; no CLI flag yet).
	Effort string
	// Branch overrides the generated worktree branch (cron).
	Branch string
	// ScheduledBy is the cron job behind this launch, recorded in
	// windowstate.
	ScheduledBy string
	// Detached launches with no parent window (cron).
	Detached bool
}

// RunDispatch is the entry point for the dispatch command.
// Flag parsing is handled by Kong via DispatchCmd in cli.go; this function
// contains the business logic (validation + dispatch).
func RunDispatch(opts DispatchOptions) {
	if err := validateDispatchOptions(opts); err != nil {
		fmt.Fprintln(os.Stderr, "Error: "+err.Error()+"\n"+dispatchUsage)
		cliout.Exit(1)
	}
	// A template fills the prompt and any unset agent/model/dir/effort
	// before anything else reads them.
	if opts.Template != "" {
		cwd, _ := os.Getwd()
		var err error
		if opts, err = applyDispatchTemplate(opts, cwd); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			cliout.Exit(1)
		}
	}
	issueNumber, promptText, promptKey, agentName := opts.Issue, opts.Prompt, opts.PromptKey, opts.Agent
	workDir, model, backend, account, harnessName := opts.Dir, opts.Model, opts.Backend, opts.Account, opts.Harness
	noWorktree, wait, timeoutDur, repo := opts.NoWorktree, opts.Wait, opts.Timeout, opts.Repo

	// An explicit --agent must name a discovered agent. Besides catching
	// typos before a worktree is created, this keeps arbitrary strings out of
	// the claude invocation and tmux window names.
	if agentName != "" {
		if err := validateAgentName(agentName, workDir); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			cliout.Exit(1)
		}
	}

	// Resolve --prompt-key into promptText before any dispatch work begins, so
	// everything downstream (dispatchPrompt, dispatch.Launch and the task
	// block of the first message) sees the resolved prompt.
	if promptKey != "" {
		resolved, err := resolvePromptKey(promptKey)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			cliout.Exit(1)
		}
		promptText = resolved
	}

	// waitSubject returns a human-readable description of the dispatch target
	// for use in --wait status lines (avoids printing "#0" for ad-hoc).
	waitSubject := func() string {
		if opts.ReviewPR != 0 {
			return fmt.Sprintf("PR #%d", opts.ReviewPR)
		}
		if issueNumber != 0 {
			return fmt.Sprintf("#%d", issueNumber)
		}
		return fmt.Sprintf("%q", textutil.Truncate(promptText, 30, "..."))
	}

	var wid, resolvedAgent string
	var err error

	switch {
	case opts.ReviewPR != 0:
		ropts := opts
		ropts.Prompt = promptText
		wid, resolvedAgent, err = dispatchReviewPR(ropts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			cliout.Exit(1)
		}
		fmt.Printf("Dispatched review of PR #%d → %s (window %s)\n", opts.ReviewPR, resolvedAgent, linkWindow(wid, wid))
	case issueNumber != 0:
		wid, resolvedAgent, err = dispatchIssue(issueNumber, agentName, workDir, model, backend, account, harnessName, repo, !noWorktree)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			cliout.Exit(1)
		}
		fmt.Printf("Dispatched #%d → %s (window %s)\n", issueNumber, resolvedAgent, linkWindow(wid, wid))
	default:
		popts := opts
		popts.Prompt = promptText
		wid, resolvedAgent, err = dispatchPromptOptions(popts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			cliout.Exit(1)
		}
		fmt.Printf("Dispatched prompt → %s (window %s)\n", resolvedAgent, linkWindow(wid, wid))
	}

	// The launch succeeded (every failure above exits): record the key's use
	// for `context prune --used`. The approval preview hook never gets here.
	if promptKey != "" {
		markPromptKeyUsed(promptKey)
	}

	if !wait {
		return
	}

	// --wait: block until the agent signals done via @agentctl_done=1, the
	// window disappears, or the optional timeout elapses.
	//
	// Exit codes:
	//   0 — @agentctl_done = "1" received
	//   1 — window closed/killed before signaling done
	//   2 — timeout elapsed
	const pollInterval = 2 * time.Second

	var deadline <-chan time.Time
	if timeoutDur > 0 {
		deadline = time.After(timeoutDur)
	}

	start := time.Now()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	subject := waitSubject()

	// Print initial status immediately.
	fmt.Printf("\rWaiting for %s (%s, window %s)… 0s", subject, resolvedAgent, linkWindow(wid, wid))

	for {
		select {
		case <-deadline:
			fmt.Printf("\rWaiting for %s (%s, window %s)… %s — timeout\n",
				subject, resolvedAgent, linkWindow(wid, wid), formatElapsed(time.Since(start)))
			cliout.Exit(2)

		case <-ticker.C:
			elapsed := time.Since(start)

			// Check if window still exists.
			if tmux.WindowNameByID(wid) == "" {
				fmt.Printf("\rWaiting for %s (%s, window %s)… %s — window gone\n",
					subject, resolvedAgent, linkWindow(wid, wid), formatElapsed(elapsed))
				cliout.Exit(1)
			}

			// Check for done signal.
			if ws, _ := windowstate.Read(wid); ws.Done {
				fmt.Printf("\rWaiting for %s (%s, window %s)… %s — done\n",
					subject, resolvedAgent, linkWindow(wid, wid), formatElapsed(elapsed))
				cliout.Exit(0)
			}

			// Still running — update status line in place.
			fmt.Printf("\rWaiting for %s (%s, window %s)… %s",
				subject, resolvedAgent, linkWindow(wid, wid), formatElapsed(elapsed))
		}
	}
}

// dispatchUsage is the dispatch command's one-line usage.
const dispatchUsage = "Usage: agentctl dispatch (--issue <N> | --review-pr <N> | --prompt <text> | --prompt-key <key> | --template <name> [--prompt <extra>]) [--agent <name>] [--author @N] [--dir <path>] [--model <name>] [--backend <name>|none] [--account <name>] [--harness <name>] [--repo <owner/name>] [--no-worktree] [--wait] [--timeout <duration>]"

// authorFlagRE matches an --author window ID.
var authorFlagRE = regexp.MustCompile(`^@[0-9]+$`)

// validateDispatchOptions checks the flag combination: one target (--issue,
// --review-pr, or a prompt), --issue/--prompt/--prompt-key mutually exclusive,
// --review-pr exclusive with --issue (a prompt with it adds review
// instructions), --agent required for a plain prompt, --author only with
// --review-pr.
func validateDispatchOptions(opts DispatchOptions) error {
	if opts.ReviewPR < 0 || opts.Issue < 0 {
		return errors.New("--issue and --review-pr take a positive number")
	}
	if opts.ReviewPR != 0 && opts.Issue != 0 {
		return errors.New("--issue and --review-pr are mutually exclusive")
	}
	if opts.Template != "" {
		switch {
		case opts.Issue != 0, opts.PromptKey != "", opts.ReviewPR != 0:
			return errors.New("--template is mutually exclusive with --issue, --review-pr and --prompt-key")
		case !prompttemplate.ValidName(opts.Template):
			return fmt.Errorf("invalid --template %q", opts.Template)
		}
		// --prompt adds instructions; the agent may come from the template
		// (checked once it is loaded).
		return validateAuthor(opts)
	}
	supplied := 0
	for _, v := range []bool{opts.Issue != 0, opts.Prompt != "", opts.PromptKey != ""} {
		if v {
			supplied++
		}
	}
	if supplied > 1 {
		return errors.New("--issue, --prompt, and --prompt-key are mutually exclusive")
	}
	if supplied == 0 && opts.ReviewPR == 0 {
		return errors.New("one of --issue, --review-pr, --prompt, --prompt-key, or --template is required")
	}
	// No label-based agent inference without an issue; --review-pr defaults
	// to code-reviewer.
	if (opts.Prompt != "" || opts.PromptKey != "") && opts.ReviewPR == 0 && opts.Agent == "" {
		return errors.New("--agent is required when using --prompt or --prompt-key")
	}
	return validateAuthor(opts)
}

// validateAuthor checks --author: only with --review-pr, and a handle or window ID.
func validateAuthor(opts DispatchOptions) error {
	if opts.Author != "" {
		if opts.ReviewPR == 0 {
			return errors.New("--author requires --review-pr")
		}
		if !authorFlagRE.MatchString(opts.Author) && !handleLike(opts.Author) {
			return fmt.Errorf("invalid --author %q: want a session handle such as a3f9, or a window ID such as @12", opts.Author)
		}
	}
	return nil
}

// loadPromptTemplate loads a --template. Seam for tests.
var loadPromptTemplate = prompttemplate.Load

// applyDispatchTemplate resolves opts.Template from cwd's project (shadowing
// the global templates) and returns opts with the template applied: the
// prompt is the template body, with any --prompt appended as additional
// instructions, and the template's agent, model, dir and effort fill only
// the flags left unset. It errors when neither the template nor --agent
// names an agent.
func applyDispatchTemplate(opts DispatchOptions, cwd string) (DispatchOptions, error) {
	t, err := loadPromptTemplate(opts.Template, cwd)
	if err != nil {
		return opts, fmt.Errorf("--template %s: %w", opts.Template, err)
	}
	if opts.Agent == "" {
		opts.Agent = t.Agent
	}
	if opts.Agent == "" {
		return opts, fmt.Errorf("template %q sets no agent; pass --agent", t.Name)
	}
	if opts.Model == "" {
		opts.Model = t.Model
	}
	if opts.Dir == "" {
		opts.Dir = t.ExpandedDir()
	}
	if opts.Effort == "" {
		opts.Effort = t.Effort
	}
	opts.Prompt = prompttemplate.Compose(t.Prompt, opts.Prompt)
	return opts, nil
}

// markPromptKeyUsed records that a dispatch launched with key. A failure only
// costs the usage record, so it is logged and never fails the dispatch.
var markPromptKeyUsed = func(key string) {
	if err := ctxstore.MarkUsed(key, time.Now()); err != nil {
		logging.Warn("dispatch: recording use of context key %q: %v", key, err)
	}
}

// Seams for tests.
var (
	resolveRepoSlug = dispatch.RepoSlug
	launchAdhoc     = dispatch.LaunchAdhoc
)

// dispatchPrompt launches an agent in a new tmux window using an inline prompt
// instead of a GitHub issue. It resolves the default working directory and
// delegates to dispatch.LaunchAdhoc, which agentctl serve shares.
// promptKey is the context store key the prompt was resolved from (via
// --prompt-key), or "" when the caller used --prompt directly; it is passed
// through as display-only metadata (see dispatch.Request.PromptKey).
// Returns the tmux window ID, the resolved agent name, and an error.
func dispatchPrompt(prompt, promptKey, agentName, workDir, model, backend, account, harnessName string, useWorktree bool) (string, string, error) {
	return dispatchPromptOptions(DispatchOptions{
		Prompt: prompt, PromptKey: promptKey, Agent: agentName, Dir: workDir,
		Model: model, Backend: backend, Account: account, Harness: harnessName, NoWorktree: !useWorktree,
	})
}

// dispatchPromptOptions is dispatchPrompt over the full options, including
// the cron-only Effort, Branch, ScheduledBy and Detached. opts.Prompt must
// already be resolved.
func dispatchPromptOptions(opts DispatchOptions) (string, string, error) {
	res, err := dispatchPromptResult(opts)
	if err != nil {
		return "", "", err
	}
	return res.WindowID, res.AgentName, nil
}

// dispatchPromptResult is dispatchPromptOptions returning the whole launch
// result (session ID included).
func dispatchPromptResult(opts DispatchOptions) (dispatch.Result, error) {
	workDir := opts.Dir
	if workDir == "" {
		workDir = resolveAgentDir(opts.Agent)
	}
	res, err := launchAdhoc(dispatch.AdhocRequest{
		Prompt:      opts.Prompt,
		PromptKey:   opts.PromptKey,
		Template:    opts.Template,
		AgentName:   opts.Agent,
		WorkDir:     workDir,
		Model:       opts.Model,
		Backend:     opts.Backend,
		Account:     opts.Account,
		Harness:     opts.Harness,
		Effort:      opts.Effort,
		UseWorktree: !opts.NoWorktree,
		Detached:    opts.Detached,
		Branch:      opts.Branch,
		ScheduledBy: opts.ScheduledBy,
	})
	return res, err
}

// dispatchIssue fetches a GitHub issue and launches an agent via dispatch.Launch().
// The GH fetch and label-based agent inference happen here; the prompt assembly,
// worktree creation, settings generation, env vars, and tmux launch are all
// handled by dispatch.Launch().
// Returns the tmux window ID, the resolved agent name, and an error.
func dispatchIssue(issueNumber int, agentName, workDir, model, backend, account, harnessName, repo string, useWorktree bool) (string, string, error) {
	// 1-2. Resolve the GitHub repo slug from the target directory.
	repoSlug, err := resolveDispatchRepo(workDir, repo)
	if err != nil {
		return "", "", err
	}

	// 3. Fetch issue via gh CLI using the resolved repo.
	out, err := exec.Command("gh", "issue", "view", fmt.Sprintf("%d", issueNumber),
		"--repo", repoSlug, "--json", "title,body,labels").Output()
	if err != nil {
		return "", "", fmt.Errorf("failed to fetch issue #%d from %s: %w", issueNumber, repoSlug, err)
	}

	var issue struct {
		Title  string `json:"title"`
		Body   string `json:"body"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := json.Unmarshal(out, &issue); err != nil {
		return "", "", fmt.Errorf("failed to parse issue #%d: %w", issueNumber, err)
	}
	labelNames := make([]string, 0, len(issue.Labels))
	for _, l := range issue.Labels {
		labelNames = append(labelNames, l.Name)
	}
	logging.Log("dispatch: fetched #%d %q labels=%v from %s", issueNumber, issue.Title, labelNames, repoSlug)

	// 4. Infer agent from labels if not specified.
	if agentName == "" {
		agentName = agentlabel.InferOrDefault(labelNames)
	}

	// 5. Resolve working directory (now that agentName is known).
	if workDir == "" {
		workDir = resolveAgentDir(agentName)
	}

	// 6. Delegate to dispatch.Launch().
	res, err := dispatch.Launch(dispatch.Request{
		AgentName:   agentName,
		Mode:        dispatch.ModeDispatch,
		Ticket:      issueNumber,
		TicketTitle: issue.Title,
		TicketBody:  issue.Body,
		WorkDir:     workDir,
		UseWorktree: &useWorktree,
		RepoSlug:    repoSlug,
		Model:       model,
		Backend:     backend,
		Account:     account,
		Harness:     harnessName,
		Background:  true,
	})
	if err != nil {
		return "", "", err
	}
	return res.WindowID, res.AgentName, nil
}

// resolveDispatchRepo returns the GitHub repo slug a dispatch targets: --repo
// when given, else the repo of workDir (or the cwd when workDir is empty), so
// a dispatch into a foreign codebase resolves against that project.
func resolveDispatchRepo(workDir, repo string) (string, error) {
	if repo != "" {
		if !repoFlagRE.MatchString(repo) {
			return "", fmt.Errorf("invalid --repo %q: want owner/name", repo)
		}
		return repo, nil
	}
	repoDir := workDir
	if repoDir == "" {
		repoDir, _ = os.Getwd()
	}
	slug, err := resolveRepoSlug(repoDir)
	if err != nil {
		logging.Warn("dispatch: resolving repo for %s: %v", repoDir, err)
		return "", fmt.Errorf("could not determine GitHub repo for %s; pass --repo or run inside a GitHub checkout", repoDir)
	}
	return slug, nil
}

// resolveAgentDir returns the working directory for the given agent name.
// For agents with a fixed dir in the agent registry, returns that dir.
// For portable agents, returns the current working directory (or $HOME).
func resolveAgentDir(name string) string {
	cfg := config.Load()
	agents, _ := agent.DiscoverFromCwd(cfg)
	cwd, _ := os.Getwd()
	dir, _ := dispatchplan.AgentDir(agents, name, cwd)
	return dir
}

// resolvePromptKey reads and validates a context store entry for use as an
// ad-hoc dispatch prompt. The returned error's message is already a complete,
// user-facing sentence (no leading "Error:" — callers add that when printing).
// Kept as a small standalone function, separate from RunDispatch's os.Exit
// calls, so the found / not-found / empty-content branches are testable
// without spawning a subprocess.
func resolvePromptKey(key string) (string, error) {
	entry, ok, err := ctxstore.Get(key)
	if err != nil {
		return "", fmt.Errorf("reading context key %q: %w", key, err)
	}
	if !ok {
		return "", fmt.Errorf("context key %q not found. Run agentctl context list to see available keys.", key)
	}
	if strings.TrimSpace(entry.Content) == "" {
		return "", fmt.Errorf("context key %q is empty", key)
	}
	return entry.Content, nil
}

// formatElapsed returns a human-readable elapsed duration with one decimal of
// seconds up to a minute, then transitions to m:ss format.
func formatElapsed(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%dm%02ds", m, s)
}

// discoverDispatchAgents lists the agents visible to a dispatch targeting
// workDir (see dispatch.DiscoverAgents).
var discoverDispatchAgents = dispatch.DiscoverAgents

// claudeBuiltinAgents are the agents Claude Code ships with (see
// dispatch.ClaudeBuiltinAgents).
var claudeBuiltinAgents = dispatch.ClaudeBuiltinAgents()

// validateAgentName returns an error naming the available agents unless name
// is a discovered agent, a Claude Code built-in, or a plugin agent (see
// dispatch.ValidateAgentName).
var validateAgentName = dispatch.ValidateAgentName
