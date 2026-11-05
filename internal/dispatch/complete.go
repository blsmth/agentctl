package dispatch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/state"
)

// completeHandler holds injectable functions for testing HandleComplete.
type completeHandler struct {
	// resolveRepo returns the GitHub slug for the agent's working dir. When
	// nil, no repo is resolved and repo is passed through as "".
	resolveRepo func(dir string) (string, error)
	lookupPR    func(repo, branch string) int
	runHook     func(cmd string, env []string)
	swapLabels  func(repo string, issueNumber int)
	openPR      func(pr int, workDir string)
}

// defaultHandler is the production implementation used by HandleComplete.
var defaultHandler = &completeHandler{
	resolveRepo: RepoSlug,
	lookupPR:    lookupPRByBranch,
	runHook:     runOnCompleteHook,
	swapLabels:  swapDispatchLabels,
	openPR:      openPRInBrowser,
}

// openedPRs tracks PR numbers already opened in the browser during this
// monitor process, so an agent flapping done->working->done doesn't reopen
// the same tab repeatedly.
var (
	openedPRsMu sync.Mutex
	openedPRs   = make(map[int]bool)
)

// markPROpened records that pr has been opened, returning true if this is
// the first time (i.e. the caller should proceed to open it).
func markPROpened(pr int) bool {
	openedPRsMu.Lock()
	defer openedPRsMu.Unlock()
	if openedPRs[pr] {
		return false
	}
	openedPRs[pr] = true
	return true
}

// ResetOpenedPRsForTest clears the dedupe state. Only for use in tests.
func ResetOpenedPRsForTest() {
	openedPRsMu.Lock()
	defer openedPRsMu.Unlock()
	openedPRs = make(map[int]bool)
}

// HandleComplete fires when a dispatch- or adhoc-mode agent transitions to
// Idle with the Done flag set. It looks up the associated PR, swaps GitHub
// labels (ticketed dispatches only), and runs any configured on_complete
// hooks.
func HandleComplete(t state.Transition, cfg config.AgentConfig) {
	defaultHandler.handle(t, cfg)
}

// handle is the injectable implementation used by both HandleComplete and tests.
func (h *completeHandler) handle(t state.Transition, cfg config.AgentConfig) {
	// Only handle dispatch/adhoc-mode windows that just signaled done.
	if !t.Done || t.To != state.StatusIdle || (t.Mode != "dispatch" && t.Mode != "adhoc") {
		return
	}

	issueNumber := t.TicketNumber
	agentName := t.Agent
	branch := t.Branch
	workDir := t.WorkDir
	windowID := t.WindowID

	logging.Log("dispatch-complete: issue #%d agent=%s branch=%s mode=%s — looking up PR", issueNumber, agentName, branch, t.Mode)

	// PR lookup runs in a goroutine to avoid blocking the monitor tick.
	go func() {
		repo := ""
		repoOK := true
		if h.resolveRepo != nil {
			var err error
			repo, err = h.resolveRepo(workDir)
			if err != nil {
				repoOK = false
				logging.Warn("dispatch-complete: cannot resolve repo for issue #%d, skipping PR lookup and label swap: %v", issueNumber, err)
			}
		}

		pr := 0
		if repoOK && branch != "" {
			pr = h.lookupPR(repo, branch)
		}

		logging.Log("dispatch-complete: PR lookup for issue #%d (PR #%d)", issueNumber, pr)

		// Run user lifecycle hooks for dispatch_complete.
		// We need the full Config to call EffectiveHooks; load it fresh.
		fullCfg := config.Load()

		// Label lifecycle is ticket-specific (Autopilot only) — ad-hoc dispatches
		// have no issue to swap labels on.
		if fullCfg.AutoPoll && pr > 0 && issueNumber > 0 && h.swapLabels != nil {
			logging.Log("dispatch-complete: swapping labels on issue #%d (agentctl:in-progress → agentctl:ready-for-review)", issueNumber)
			h.swapLabels(repo, issueNumber)
		}

		// Open the newly-created PR in the browser, unless disabled or already
		// opened once this process (an agent can go done->working->done again
		// within one session).
		if pr > 0 && fullCfg.OpenPRInBrowserEnabled() && h.openPR != nil && markPROpened(pr) {
			h.openPR(pr, workDir)
		}

		// EffectiveHooks returns global + agent hooks merged, with backward-compat
		// fallback to dispatch.on_complete when hooks.dispatch_complete is unset.
		hookCmds := fullCfg.EffectiveHooks("dispatch_complete", cfg)
		if len(hookCmds) == 0 {
			return
		}

		env := []string{
			fmt.Sprintf("AGENTCTL_ISSUE=%d", issueNumber),
			fmt.Sprintf("AGENTCTL_AGENT=%s", agentName),
			fmt.Sprintf("AGENTCTL_BRANCH=%s", branch),
			fmt.Sprintf("AGENTCTL_WORKDIR=%s", workDir),
			fmt.Sprintf("AGENTCTL_PR=%d", pr),
			fmt.Sprintf("AGENTCTL_WINDOW_ID=%s", windowID),
		}
		for _, hookCmd := range hookCmds {
			logging.Log("dispatch-complete: running dispatch_complete hook for issue #%d: %s", issueNumber, hookCmd)
			h.runHook(hookCmd, env)
		}
	}()
}

// swapDispatchLabels removes agentctl:in-progress and adds agentctl:ready-for-review
// on the given issue.
func swapDispatchLabels(repo string, issueNumber int) {
	issueStr := strconv.Itoa(issueNumber)

	// Ensure the target label exists (idempotent via --force).
	if err := exec.Command("gh", "label", "create", "agentctl:ready-for-review",
		"--repo", repo,
		"--color", "0E8A16",
		"--description", "Dispatch complete — PR ready for human review",
		"--force",
	).Run(); err != nil {
		logging.Warn("dispatch-complete: failed to ensure agentctl:ready-for-review label: %v", err)
	}

	// Remove in-progress.
	if err := exec.Command("gh", "issue", "edit", issueStr,
		"--repo", repo,
		"--remove-label", "agentctl:in-progress",
	).Run(); err != nil {
		logging.Warn("dispatch-complete: failed to remove agentctl:in-progress from #%d: %v", issueNumber, err)
	}

	// Add ready-for-review.
	if err := exec.Command("gh", "issue", "edit", issueStr,
		"--repo", repo,
		"--add-label", "agentctl:ready-for-review",
	).Run(); err != nil {
		logging.Warn("dispatch-complete: failed to add agentctl:ready-for-review to #%d: %v", issueNumber, err)
	}

	logging.Log("dispatch-complete: labels swapped on issue #%d (agentctl:in-progress → agentctl:ready-for-review)", issueNumber)
}

// lookupPRByBranch shells out to gh to find the open or merged PR for branch.
// Returns 0 if not found or on error.
func lookupPRByBranch(repo, branch string) int {
	out, err := exec.Command("gh", "pr", "list",
		"--repo", repo,
		"--head", branch,
		"--state", "all",
		"--json", "number",
		"--jq", ".[0].number",
	).Output()
	if err != nil {
		logging.Warn("dispatch-complete: gh pr list failed for branch %s: %v", branch, err)
		return 0
	}
	numStr := strings.TrimSpace(string(out))
	if numStr == "" || numStr == "null" {
		return 0
	}
	n, err := strconv.Atoi(numStr)
	if err != nil {
		return 0
	}
	return n
}

// openPRInBrowser shells out to `gh pr view --web` to open pr in the user's
// default browser. gh resolves the repo from workDir's git remote; if workDir
// is unusable the open is skipped rather than guessing a repo. Failures are
// logged as warnings, never fatal.
func openPRInBrowser(pr int, workDir string) {
	args := []string{"pr", "view", strconv.Itoa(pr), "--web"}

	if !workDirUsable(workDir) {
		logging.Warn("dispatch-complete: not opening PR #%d: working dir %q unusable", pr, workDir)
		return
	}
	cmd := exec.Command("gh", args...)
	cmd.Dir = workDir

	if err := cmd.Run(); err != nil {
		logging.Warn("dispatch-complete: failed to open PR #%d in browser: %v", pr, err)
		return
	}
	logging.Log("dispatch-complete: opened PR #%d in browser", pr)
}

// workDirUsable reports whether dir is a non-empty path pointing at an
// existing directory, i.e. one gh can resolve a git remote from.
func workDirUsable(dir string) bool {
	if dir == "" {
		return false
	}
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}

// onCompleteHookTimeout bounds each dispatch_complete hook command. It is a
// variable so tests can shorten it.
var onCompleteHookTimeout = 30 * time.Second

// runOnCompleteHook runs one dispatch_complete shell command to completion (or
// timeout) and logs its output. It is called from HandleComplete's background
// goroutine, so blocking here never stalls the monitor tick.
//
// Mirrors hooks.runSingleUserHook: the command runs in its own process group
// so the timeout kills everything the shell spawned, and WaitDelay force-closes
// pipes a surviving descendant might hold open.
func runOnCompleteHook(cmd string, env []string) {
	out, err := runHookCommand(cmd, env, onCompleteHookTimeout)
	if len(out) > 0 {
		logging.Log("dispatch-complete: on_complete hook output: %s", strings.TrimSpace(string(out)))
	}
	if err != nil {
		logging.Warn("dispatch-complete: on_complete hook %q error: %v", cmd, err)
	}
}

// runHookCommand runs cmd via "sh -c" with env appended to os.Environ(),
// killing its whole process group after timeout.
func runHookCommand(cmd string, env []string, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c := exec.CommandContext(ctx, "sh", "-c", cmd)
	c.Env = append(os.Environ(), env...)
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error {
		if c.Process != nil {
			// Negative PID targets the whole process group.
			_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		}
		return os.ErrProcessDone
	}
	c.WaitDelay = 2 * time.Second
	return c.CombinedOutput()
}
