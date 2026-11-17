package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/agentlabel"
	"github.com/blsmth/agentctl/internal/config"
	ctxstore "github.com/blsmth/agentctl/internal/context"
	"github.com/blsmth/agentctl/internal/dispatch"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/textutil"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// repoFlagRE matches a GitHub owner/name slug.
var repoFlagRE = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)

// DispatchOptions holds the dispatch command's flags.
type DispatchOptions struct {
	Issue      int
	Prompt     string
	PromptKey  string
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
}

// RunDispatch is the entry point for the dispatch command.
// Flag parsing is handled by Kong via DispatchCmd in cli.go; this function
// contains the business logic (validation + dispatch).
func RunDispatch(opts DispatchOptions) {
	issueNumber, promptText, promptKey, agentName := opts.Issue, opts.Prompt, opts.PromptKey, opts.Agent
	workDir, model, backend, account, harnessName := opts.Dir, opts.Model, opts.Backend, opts.Account, opts.Harness
	noWorktree, wait, timeoutDur, repo := opts.NoWorktree, opts.Wait, opts.Timeout, opts.Repo
	const usageStr = "Usage: agentctl dispatch (--issue <N> | --prompt <text> | --prompt-key <key>) [--agent <name>] [--dir <path>] [--model <name>] [--backend <name>|none] [--account <name>] [--harness <name>] [--repo <owner/name>] [--no-worktree] [--wait] [--timeout <duration>]"

	// Validate: exactly one of --issue, --prompt, or --prompt-key must be supplied.
	suppliedCount := 0
	for _, v := range []bool{issueNumber != 0, promptText != "", promptKey != ""} {
		if v {
			suppliedCount++
		}
	}
	if suppliedCount > 1 {
		fmt.Fprintln(os.Stderr, "Error: --issue, --prompt, and --prompt-key are mutually exclusive\n"+usageStr)
		os.Exit(1)
	}
	if suppliedCount == 0 {
		fmt.Fprintln(os.Stderr, "Error: one of --issue, --prompt, or --prompt-key is required\n"+usageStr)
		os.Exit(1)
	}

	// --agent is required when --prompt or --prompt-key is used (no label-based
	// inference without an issue).
	if (promptText != "" || promptKey != "") && agentName == "" {
		fmt.Fprintln(os.Stderr, "Error: --agent is required when using --prompt or --prompt-key\n"+usageStr)
		os.Exit(1)
	}

	// An explicit --agent must name a discovered agent. Besides catching
	// typos before a worktree is created, this keeps arbitrary strings out of
	// the claude invocation and tmux window names.
	if agentName != "" {
		if err := validateAgentName(agentName, workDir); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	}

	// Resolve --prompt-key into promptText before any dispatch work begins, so
	// everything downstream (dispatchPrompt, dispatch.Launch, the
	// longPromptThreshold system-prompt vs. user-message split) is unchanged.
	if promptKey != "" {
		resolved, err := resolvePromptKey(promptKey)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		promptText = resolved
	}

	// waitSubject returns a human-readable description of the dispatch target
	// for use in --wait status lines (avoids printing "#0" for ad-hoc).
	waitSubject := func() string {
		if issueNumber != 0 {
			return fmt.Sprintf("#%d", issueNumber)
		}
		return fmt.Sprintf("%q", textutil.Truncate(promptText, 30, "..."))
	}

	var wid, resolvedAgent string
	var err error

	if issueNumber != 0 {
		wid, resolvedAgent, err = dispatchIssue(issueNumber, agentName, workDir, model, backend, account, harnessName, repo, !noWorktree)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Dispatched #%d → %s (window %s)\n", issueNumber, resolvedAgent, linkWindow(wid, wid))
	} else {
		wid, resolvedAgent, err = dispatchPrompt(promptText, promptKey, agentName, workDir, model, backend, account, harnessName, !noWorktree)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Dispatched prompt → %s (window %s)\n", resolvedAgent, linkWindow(wid, wid))
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
			os.Exit(2)

		case <-ticker.C:
			elapsed := time.Since(start)

			// Check if window still exists.
			if tmux.WindowNameByID(wid) == "" {
				fmt.Printf("\rWaiting for %s (%s, window %s)… %s — window gone\n",
					subject, resolvedAgent, linkWindow(wid, wid), formatElapsed(elapsed))
				os.Exit(1)
			}

			// Check for done signal.
			if ws, _ := windowstate.Read(wid); ws.Done {
				fmt.Printf("\rWaiting for %s (%s, window %s)… %s — done\n",
					subject, resolvedAgent, linkWindow(wid, wid), formatElapsed(elapsed))
				os.Exit(0)
			}

			// Still running — update status line in place.
			fmt.Printf("\rWaiting for %s (%s, window %s)… %s",
				subject, resolvedAgent, linkWindow(wid, wid), formatElapsed(elapsed))
		}
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
	if workDir == "" {
		workDir = resolveAgentDir(agentName)
	}
	res, err := launchAdhoc(dispatch.AdhocRequest{
		Prompt:      prompt,
		PromptKey:   promptKey,
		AgentName:   agentName,
		WorkDir:     workDir,
		Model:       model,
		Backend:     backend,
		Account:     account,
		Harness:     harnessName,
		UseWorktree: useWorktree,
	})
	if err != nil {
		return "", "", err
	}
	return res.WindowID, res.AgentName, nil
}

// dispatchIssue fetches a GitHub issue and launches an agent via dispatch.Launch().
// The GH fetch and label-based agent inference happen here; the prompt assembly,
// worktree creation, settings generation, env vars, and tmux launch are all
// handled by dispatch.Launch().
// Returns the tmux window ID, the resolved agent name, and an error.
func dispatchIssue(issueNumber int, agentName, workDir, model, backend, account, harnessName, repo string, useWorktree bool) (string, string, error) {
	// 1. Determine the directory for repo resolution.
	//    Use the explicit --dir when provided; otherwise fall back to cwd so
	//    that running from inside a project directory picks up its remote.
	repoDir := workDir
	if repoDir == "" {
		repoDir, _ = os.Getwd()
	}

	// 2. Resolve the GitHub repo slug from the target directory.
	//    This ensures --issue N resolves against the correct project, not the
	//    agentctl repo, when dispatching into a foreign codebase.
	repoSlug := repo
	if repoSlug != "" && !repoFlagRE.MatchString(repoSlug) {
		return "", "", fmt.Errorf("invalid --repo %q: want owner/name", repoSlug)
	}
	if repoSlug == "" {
		var err error
		repoSlug, err = resolveRepoSlug(repoDir)
		if err != nil {
			logging.Warn("dispatch: resolving repo for %s: %v", repoDir, err)
			return "", "", fmt.Errorf("could not determine GitHub repo for %s; pass --repo or run inside a GitHub checkout", repoDir)
		}
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

// resolveAgentDir returns the working directory for the given agent name.
// For agents with a fixed dir in the agent registry, returns that dir.
// For portable agents, returns the current working directory (or $HOME).
func resolveAgentDir(name string) string {
	cfg := config.Load()
	agents, _ := agent.DiscoverFromCwd(cfg)
	for _, a := range agents {
		if a.Name == name && a.Dir != "" {
			return a.Dir
		}
	}
	dir, _ := os.Getwd()
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = home
	}
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
