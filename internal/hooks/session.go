package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/blsmth/agentctl/internal/claude"
	"github.com/blsmth/agentctl/internal/cleanup"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/llm"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/sessions"
	"github.com/blsmth/agentctl/internal/shellquote"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/tui"
	"github.com/blsmth/agentctl/internal/windowstate"
	"github.com/blsmth/agentctl/internal/worktree"
)

// killWindowFunc is the function used to kill a tmux window. It is a
// package-level variable so tests can substitute a spy without shelling out.
var killWindowFunc = tmuxKillWindow

// reclaimSessionWorktreeFunc classifies and, if safe, reclaims the worktree
// belonging to an exiting window. It is a package-level variable -- following
// the killWindowFunc / writeWorktreeSettingsFn seam pattern already used in
// this package -- so tests can substitute a fake without needing a live
// repo, git, or gh.
//
// Returns true if cleanup.Reclaim actually ran. Reclaim kills the window
// itself as one of its steps, so callers must skip their own killWindowFunc
// call in that case to avoid killing the window twice.
var reclaimSessionWorktreeFunc = reclaimSessionWorktree

// worktreePruneFunc is the function used to prune stale worktree
// administrative metadata after a session ends. Package-level variable so
// tests can substitute a spy without shelling out to git.
var worktreePruneFunc = worktree.Prune

// tmuxKillWindow shells out to kill the named tmux window. Errors are logged
// but not propagated -- a failed kill is non-fatal for the hook caller.
func tmuxKillWindow(windowID string) {
	cmd := exec.Command("tmux", "kill-window", "-t", windowID)
	if out, err := cmd.CombinedOutput(); err != nil {
		logging.Log("hook-session-end: kill-window %s: %s: %v", windowID, string(out), err)
	}
}

// SessionStart is called by Claude Code's SessionStart hook.
// It writes dynamic identity env vars (window ID, index) to $CLAUDE_ENV_FILE
// so they persist across all Bash tool invocations in the session.
func SessionStart() {
	if !guardAgentctl() {
		return
	}

	doneTimer := logging.Timing("hook/session-start")
	defer doneTimer()

	// Read stdin JSON to get session_id and cwd. Claude Code sends these on
	// every SessionStart, including after /clear. Failure is non-fatal -- the
	// rest of the function still runs.
	var input struct {
		SessionID string `json:"session_id"`
		Cwd       string `json:"cwd"`
	}
	decodeInput(&input) // non-fatal: ignore return value

	envFile := os.Getenv("CLAUDE_ENV_FILE")
	if envFile == "" {
		logging.Log("hook-session-start: CLAUDE_ENV_FILE not set, skipping")
		return
	}

	// Resolve dynamic values from tmux. We must target the hook's own pane
	// via TMUX_PANE -- plain display-message resolves to the client's focused
	// window, which may be a different agent.
	//
	// This is deliberately not signal.ResolveWindowID, whose first step is
	// the AGENTCTL_SESSION_ID -> windowstate lookup: SessionStart is the hook
	// that records the session ID into windowstate in the first place, so
	// that mapping cannot yet be trusted for this session. The pane comes
	// first here, with AGENTCTL_WINDOW_ID as the last resort.
	pane := os.Getenv("TMUX_PANE")
	var windowID, windowIndex string
	if pane != "" {
		windowID = tmux.DisplayTarget(pane, "#{window_id}")
		windowIndex = tmux.DisplayTarget(pane, "#{window_index}")
	}

	// Fall back to the env var written at launch time. This covers edge cases
	// where TMUX_PANE is unavailable (e.g. tests, non-interactive contexts).
	if windowID == "" {
		windowID = os.Getenv("AGENTCTL_WINDOW_ID")
	}

	if windowID == "" {
		logging.Log("hook-session-start: could not resolve window_id")
		return
	}

	// Link this window to its JSONL log file so the monitor can find the
	// correct log without scanning. This fixes multi-instance confusion where
	// findLatestLog picks the wrong file when multiple instances of the same
	// agent run simultaneously.
	//
	// SessionID and LogPath must stay atomically consistent: LogPath is always
	// derived directly from SessionID (claude.LogPath builds the filename from
	// it), so filepath.Base(LogPath) must equal SessionID + ".jsonl". If we
	// can't compute a fresh LogPath (e.g. input.Cwd is empty), we must CLEAR
	// the field rather than leave a stale value in place -- a stale log_path
	// can point at a completely different session (even a different repo),
	// which silently corrupts the monitor's token/cost/context% attribution
	// for this window. An empty log_path just makes the monitor fall back to
	// its filesystem scan, which is degraded but at least not wrong.
	if input.SessionID != "" {
		logPath := ""
		if input.Cwd != "" {
			logPath = claude.LogPath(input.Cwd, input.SessionID)
		}
		if err := windowstate.Update(windowID, func(ws *windowstate.WindowState) {
			ws.SessionID = input.SessionID
			ws.LogPath = logPath
		}); err != nil {
			logging.Log("hook-session-start: windowstate.Update error: %v", err)
		} else {
			if logPath == "" {
				logging.Log("hook-session-start: cleared log_path for window=%s session=%s (cwd unknown, avoiding stale cross-session path)", windowID, input.SessionID)
			}
			logging.Log("hook-session-start: linked session_id=%s log=%s window=%s", input.SessionID, logPath, windowID)
		}
	}

	// Write to CLAUDE_ENV_FILE so these persist across Bash commands.
	// Use O_TRUNC (not O_APPEND) so stale values from previous sessions
	// don't accumulate across /clear invocations.
	f, err := os.OpenFile(envFile, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		logging.Log("hook-session-start: open env file: %v", err)
		return
	}
	defer f.Close()

	vars := map[string]string{
		"AGENTCTL_WINDOW_ID":    windowID,
		"AGENTCTL_WINDOW_INDEX": windowIndex,
	}
	// Propagate dispatch metadata so `agentctl start` (run via Bash tool)
	// can read ticket/mode context. These are set as tmux window env vars
	// at launch time but don't survive into Claude Code's Bash subprocesses
	// without being written to CLAUDE_ENV_FILE here.
	for _, k := range []string{"AGENTCTL_TICKET", "AGENTCTL_TICKET_TITLE", "AGENTCTL_MODE", "AGENTCTL_SPAWNED_BY", "AGENTCTL_SPAWNED_BY_SESSION"} {
		if v := os.Getenv(k); v != "" {
			vars[k] = v
		}
	}
	for k, v := range vars {
		fmt.Fprintf(f, "export %s=%s\n", k, shellquote.Quote(v))
	}

	logging.Log("hook-session-start: wrote WINDOW_ID=%s WINDOW_INDEX=%s to %s", windowID, windowIndex, envFile)

	// Fire user lifecycle hooks (async, non-blocking).
	runUserHooks("session_start", map[string]string{
		"AGENTCTL_TICKET":       os.Getenv("AGENTCTL_TICKET"),
		"AGENTCTL_TICKET_TITLE": os.Getenv("AGENTCTL_TICKET_TITLE"),
		"AGENTCTL_MODE":         os.Getenv("AGENTCTL_MODE"),
		"AGENTCTL_AGENT_NAME":   os.Getenv("AGENTCTL_AGENT_NAME"),
		"AGENTCTL_WINDOW_ID":    windowID,
	})

	// Emit JSON on stdout with context for the session.
	// This becomes additional context visible to the agent.
	agentName := os.Getenv("AGENTCTL_AGENT_NAME")
	windowName := os.Getenv("AGENTCTL_WINDOW_NAME")
	if agentName != "" {
		// Read ticket metadata from window state file (written at launch time).
		wsCtx, _ := windowstate.Read(windowID)
		ticket := wsCtx.Ticket
		ticketTitle := wsCtx.TicketTitle
		mode := wsCtx.Mode

		var ctx string
		if ticket != "" && ticketTitle != "" && mode != "" {
			ctx = fmt.Sprintf(
				"You are agent %q, working on ticket #%s: %q (mode: %s).\n"+
					"You are running inside agentctl window %q (tmux index %s). "+
					"To target your own window: `-t agentctl:%s`.",
				agentName, ticket, ticketTitle, mode,
				windowID, windowIndex, windowID,
			)
		} else {
			ctx = fmt.Sprintf(
				"You are running inside agentctl window %q (tmux ID: %s, index: %s). "+
					"To target your own window in tmux commands, use `-t agentctl:%s`.",
				windowName, windowID, windowIndex, windowID,
			)
		}
		output := map[string]string{"additionalContext": ctx}
		json.NewEncoder(os.Stdout).Encode(output) //nolint:errcheck
	}
}

// SessionEnd is called by Claude Code's SessionEnd hook.
// It fires a notification when an agent session ends and logs the event.
func SessionEnd() {
	if !guardAgentctl() {
		return
	}

	var input struct {
		Reason    string `json:"reason"`
		SessionID string `json:"session_id"`
	}
	if !decodeInput(&input) {
		return
	}

	agentName := resolveAgentName()

	// Find the window by scanning windowstate files for the session ID from
	// the hook payload. This is set in hook-session-start and is immune to
	// stale AGENTCTL_WINDOW_ID env values.
	windowID := windowstate.FindBySessionID(input.SessionID)

	logging.Log("hook-session-end: agent=%s window=%s reason=%s", agentName, windowID, input.Reason)

	// Clear working state so the spinner stops. idle_prompt only fires when
	// Claude is waiting for interactive input; ticket agents that finish and
	// exit cleanly reach SessionEnd without ever firing idle_prompt.
	// Visual updates (window renaming, tab style) are owned by the Bubble Tea monitor view.
	if windowID != "" {
		if err := windowstate.Update(windowID, func(ws *windowstate.WindowState) {
			ws.State = "idle"
		}); err != nil {
			logging.Log("hook-session-end: windowstate.Update error: %v", err)
		}
		logging.Log("hook-session-end: cleared state=idle window=%s agent=%s", windowID, agentName)
	}

	// Fire user lifecycle hooks (async, non-blocking).
	runUserHooks("session_end", map[string]string{
		"AGENTCTL_AGENT_NAME": agentName,
		"AGENTCTL_WINDOW_ID":  windowID,
		"AGENTCTL_SESSION_ID": input.SessionID,
		"AGENTCTL_TICKET":     os.Getenv("AGENTCTL_TICKET"),
		"AGENTCTL_MODE":       os.Getenv("AGENTCTL_MODE"),
	})

	// Auto-journal: generate an LLM summary if enabled for this agent.
	// This is a synchronous call -- the hook process must not exit before the
	// completion returns. It MUST stay before the kill-window call below: this
	// hook process lives inside the tmux window it is about to kill, so
	// anything sequenced after the kill may never run. Consequence: journal
	// latency is how long the window lingers after /exit (hence the 30s default
	// timeout). Claude Code already invokes this hook asynchronously ("async": true),
	// so there is no benefit to an extra goroutine and doing so would kill the call
	// when the process exits.
	cfg := config.Load()
	if cfg.EffectiveWorkJournal(agentName) {
		if err := generateWorkJournal(agentName, input.SessionID, windowID); err != nil {
			logging.Log("hook-session-end: journal error agent=%s: %v", agentName, err)
		}
	}

	// Kill the tmux window for terminal exit reasons. Non-terminal reasons
	// (clear, resume) keep the window alive — Claude is still running.
	terminalReasons := map[string]bool{
		"prompt_input_exit":           true, // /exit
		"logout":                      true, // session logout
		"bypass_permissions_disabled": true, // permissions revoked
		// NOTE: "other" intentionally excluded — it's a catch-all that fires on
		// crashes, transient errors, and unexpected exits. Killing the window on
		// "other" causes cascading session death when agents exit unexpectedly.
	}
	if windowID != "" && terminalReasons[input.Reason] {
		logging.Log("hook-session-end: killing window %s (reason=%s)", windowID, input.Reason)

		// Self-reclaim: hook-worktree-remove is a Claude Code feature and
		// never fires for agentctl-created worktrees, so without this an
		// agentctl dispatch worktree is left behind forever once its session
		// exits. Only attempt this for windows that actually have a
		// worktree recorded (dispatches that fell back to the shared repo
		// dir have none, and there is nothing to reclaim for them).
		ws, wsErr := windowstate.Read(windowID)
		if wsErr != nil {
			logging.Log("hook-session-end: windowstate.Read(%s) for reclaim: %v", windowID, wsErr)
		}

		reclaimed := false
		if ws.WorktreeDir == "" {
			logging.Log("hook-session-end: window=%s has no recorded worktree, skipping self-reclaim", windowID)
		} else if !cfg.EffectiveAutoReclaimWorktree(agentName) {
			logging.Log("hook-session-end: auto_reclaim_worktree disabled for agent=%s, skipping self-reclaim of worktree=%s", agentName, ws.WorktreeDir)
		} else {
			reclaimed = reclaimSessionWorktreeFunc(windowID, ws)
		}

		// Reclaim already kills the window as one of its steps -- killing it
		// again here would be redundant (and tmux kill-window on an already
		// -gone window is a harmless no-op, but there's no reason to invite
		// it).
		if !reclaimed {
			killWindowFunc(windowID)
		}

		// Prune stale worktree administrative metadata regardless of whether
		// a reclaim happened above -- e.g. some other worktree's directory
		// may have been deleted out from under git independently. Non-fatal:
		// a prune failure must never affect session teardown.
		if ws.WorktreeDir != "" {
			repoRoot := worktree.MainRepoRoot(ws.WorktreeDir)
			if err := worktreePruneFunc(repoRoot); err != nil {
				logging.Log("hook-session-end: worktree.Prune(%s): %v", repoRoot, err)
			}
		}
	}
}

// reclaimSessionWorktree gathers the same Facts `agentctl cleanup` would for
// a single worktree, forces Window.Live=false (the window is exiting) and
// IncludeOpenPRs=false (an agent that just opened a PR must keep its
// worktree -- there is no flag to override this at session exit), and, if
// cleanup.Classify judges the result reclaimable, calls cleanup.Reclaim.
//
// Every safety rail lives in cleanup.Classify (dirty tree, unpushed commits,
// no upstream with commits, open PR, unknown gh answer, etc.) -- this
// function only gathers facts and acts on the verdict, exactly like
// cleanup.Gather does for the CLI.
func reclaimSessionWorktree(windowID string, ws windowstate.WindowState) bool {
	repoDir := worktree.MainRepoRoot(ws.WorktreeDir)
	baseRef := worktree.ResolveBaseRef(ws.WorktreeDir)

	facts := cleanup.Facts{
		Window:         cleanup.WindowFacts{Live: false},
		IncludeOpenPRs: false,
	}

	if dirty, err := cleanup.IsDirty(ws.WorktreeDir); err != nil {
		logging.Log("hook-session-end: IsDirty(%s): %v -- treating as dirty", ws.WorktreeDir, err)
		facts.Dirty = true
	} else {
		facts.Dirty = dirty
	}

	facts.HasUpstream = cleanup.HasUpstream(ws.WorktreeDir)
	if facts.HasUpstream {
		if n, err := cleanup.UnpushedCount(ws.WorktreeDir); err == nil {
			facts.UnpushedCount = n
		} else {
			logging.Log("hook-session-end: UnpushedCount(%s): %v -- treating as unpushed", ws.WorktreeDir, err)
			facts.UnpushedCount = 1
		}
	}

	if n, err := cleanup.AheadOfBase(ws.WorktreeDir, baseRef); err == nil {
		facts.AheadOfBase = n
	} else {
		logging.Log("hook-session-end: AheadOfBase(%s, %s): %v -- treating as ahead of base", ws.WorktreeDir, baseRef, err)
		facts.AheadOfBase = 1 // never treat an unknown ahead-count as "empty"
	}
	facts.MergedIntoBase = cleanup.MergedIntoBase(ws.WorktreeDir, baseRef)

	if repoSlug := tui.TLRepoSlug(ws.WorktreeDir); repoSlug != "" && ws.Branch != "" {
		facts.PR = cleanup.LookupPRState(repoSlug, ws.Branch)
	} else {
		facts.PR = cleanup.PRUnknown
	}

	verdict := cleanup.Classify(facts)
	logging.Log("hook-session-end: self-reclaim classify window=%s worktree=%s disposition=%s reason=%q",
		windowID, ws.WorktreeDir, verdict.Disposition, verdict.Reason)

	if !verdict.Disposition.Reclaimable() {
		return false
	}

	c := &cleanup.Candidate{
		WorktreePath:   ws.WorktreeDir,
		Branch:         ws.Branch,
		RepoDir:        repoDir,
		WindowID:       windowID,
		Disposition:    verdict.Disposition,
		Reason:         verdict.Reason,
		PreserveBranch: verdict.PreserveBranch,
	}
	cleanup.Reclaim(c)
	for _, step := range c.StepsDone {
		logging.Log("hook-session-end: self-reclaim step done window=%s: %s", windowID, step)
	}
	for _, e := range c.Errors {
		logging.Log("hook-session-end: self-reclaim step FAILED window=%s: %s", windowID, e)
	}
	return true
}

// newCompleterFunc constructs the LLM completer for journaling. Package-level
// variable so tests can substitute a fake Completer without HTTP or a model.
var newCompleterFunc = llm.New

// defaultJournalTimeout bounds the journal completion when the configured
// timeout is absent or unparseable. It is deliberately 30s, not 60s: the
// journal runs BEFORE kill-window in SessionEnd, so its latency is exactly how
// long the tmux window lingers after /exit.
const defaultJournalTimeout = 30 * time.Second

// journalTimeout returns the completion deadline for cfg.
func journalTimeout(cfg config.LLMConfig) time.Duration {
	if cfg.Ollama.Timeout != "" {
		if d, err := time.ParseDuration(cfg.Ollama.Timeout); err == nil && d > 0 {
			return d
		}
	}
	return defaultJournalTimeout
}

// generateWorkJournal parses the session log, asks the configured LLM provider
// for a summary, and appends the result to
// ~/.local/share/agentctl/journal/<agentName>.md plus a JSONL sidecar line.
//
// When no provider is configured, journaling is disabled and this returns nil.
// A completion failure returns an error and writes nothing.
func generateWorkJournal(agentName, sessionID, windowID string) error {
	llmCfg := config.Load().EffectiveLLM()
	if llmCfg.Provider == "" {
		logging.Log("hook-session-end: journal: no llm.provider configured, journaling disabled")
		return nil
	}

	// Read window state once; use it for both log path and ticket metadata.
	var ws windowstate.WindowState
	if windowID != "" {
		ws, _ = windowstate.Read(windowID)
	}

	// Resolve the log file path from window state.
	logPath := ws.LogPath
	if logPath == "" && ws.SessionID != "" {
		// Try to find via session ID -- log path may not have been set.
		sessionID = ws.SessionID
	}

	if logPath == "" {
		logging.Log("hook-session-end: journal: no log path for window=%s agent=%s, skipping", windowID, agentName)
		return nil
	}

	// Parse the session summary.
	sess := sessions.Session{
		ID:      sessionID,
		Agent:   agentName,
		LogFile: logPath,
	}
	summary, err := sessions.ParseSummary(sess)
	if err != nil {
		return fmt.Errorf("parse summary: %w", err)
	}

	prompt := buildJournalPrompt(agentName, sessionID, summary, ws.Ticket, ws.TicketTitle)

	completer, err := newCompleterFunc(llmCfg)
	if err != nil {
		return fmt.Errorf("llm client: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), journalTimeout(llmCfg))
	defer cancel()
	aiSummary, err := completer.Complete(ctx, prompt)
	if err != nil {
		return fmt.Errorf("llm completion: %w", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("home dir: %w", err)
	}
	// Project comes from paths.ProjectDir(), NOT cwd: for a dispatched agent
	// cwd is the worktree (<repo>/.worktrees/agent-foo-issue-N), whereas
	// ProjectDir resolves to the owning project.
	return writeJournalEntry(home, agentName, sessionID, paths.ProjectDir(), ws.Ticket, summary, aiSummary)
}

// buildJournalPrompt builds the text prompt sent to the LLM. It is capped at
// ~4000 chars to fit comfortably in the configured context window.
func buildJournalPrompt(agentName, sessionID string, summary *sessions.Summary, ticket, ticketTitle string) string {
	const maxPromptLen = 4000

	var sb strings.Builder
	sb.WriteString("Summarize this agent work session for a work log. Be terse -- 2-4 bullet points max. Focus on what was implemented/changed, not process.\n\n")
	sb.WriteString("--- SESSION FACTS ---\n")
	fmt.Fprintf(&sb, "Agent: %s\n", agentName)
	fmt.Fprintf(&sb, "Session ID: %s\n", sessionID)

	if ticket != "" {
		if ticketTitle != "" {
			fmt.Fprintf(&sb, "Ticket: #%s -- %s\n", ticket, ticketTitle)
		} else {
			fmt.Fprintf(&sb, "Ticket: #%s\n", ticket)
		}
	}

	if !summary.Session.StartTime.IsZero() {
		dur := summary.Session.Duration()
		fmt.Fprintf(&sb, "Duration: %s\n", formatDuration(dur))
	}

	fmt.Fprintf(&sb, "Turns: %d\n", summary.TurnCount)
	fmt.Fprintf(&sb, "Tool calls: %d\n", len(summary.ToolCalls))
	fmt.Fprintf(&sb, "Input tokens: %d, Output tokens: %d\n", summary.TotalInputTokens, summary.TotalOutputTokens)

	// Tool call breakdown by type.
	if len(summary.ToolCalls) > 0 {
		counts := make(map[string]int)
		for _, tc := range summary.ToolCalls {
			counts[tc.Name]++
		}
		sb.WriteString("Tool breakdown: ")
		first := true
		for name, count := range counts {
			if !first {
				sb.WriteString(", ")
			}
			fmt.Fprintf(&sb, "%s\u00d7%d", name, count)
			first = false
		}
		sb.WriteString("\n")
	}

	// Files edited (most informative).
	if len(summary.FilesEdited) > 0 {
		files := summary.FilesEdited
		if len(files) > 10 {
			files = files[:10]
		}
		fmt.Fprintf(&sb, "Files edited: %s\n", strings.Join(files, ", "))
	}

	// Files read (truncated).
	if len(summary.FilesRead) > 0 {
		files := summary.FilesRead
		if len(files) > 5 {
			files = files[:5]
		}
		fmt.Fprintf(&sb, "Files read: %s\n", strings.Join(files, ", "))
	}

	// First user message is the task description.
	if len(summary.UserMsgs) > 0 {
		fmt.Fprintf(&sb, "Task: %s\n", summary.UserMsgs[0])
	}

	result := sb.String()
	if len(result) > maxPromptLen {
		result = result[:maxPromptLen]
	}
	return result
}

// journalRecord is one line of the machine-readable entries.jsonl sidecar.
type journalRecord struct {
	Timestamp string `json:"ts"`
	Agent     string `json:"agent"`
	SessionID string `json:"session_id"`
	Project   string `json:"project"`
	Ticket    string `json:"ticket"`
	DurationS int64  `json:"duration_s"`
	Turns     int    `json:"turns"`
	Model     string `json:"model"`
	Summary   string `json:"summary"`
}

// journalDir returns the durable journal directory: <config.DataDirIn(home)>/journal,
// i.e. $XDG_DATA_HOME/agentctl/journal, else ~/.local/share/agentctl/journal.
// It is under the data dir, not ~/.cache, because cache cleaners may delete it.
// A journal left in the old config-dir location is moved once; if that move
// fails the old location keeps being used so nothing is orphaned.
func journalDir(home string) string {
	newPath := filepath.Join(config.DataDirIn(home), "journal")
	return config.MigrateLegacy(filepath.Join(config.GlobalDirIn(home), "journal"), newPath)
}

// appendFile appends data to path with a single write call. The journal
// lives in the user's own config dir (not the repo-controlled runtime dir),
// whose ancestors dotfile managers commonly symlink, so only the file itself
// is opened O_NOFOLLOW: a symlinked journal file is refused rather than
// appended through.
func appendFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// writeJournalEntry appends a journal entry to the agent's markdown journal
// and one JSON line to entries.jsonl. An empty/whitespace-only aiSummary is
// rejected before anything is written, so a failed summary never leaves a
// blank or partial entry. Extracted to make it testable without an LLM.
func writeJournalEntry(home, agentName, sessionID, project, ticket string, summary *sessions.Summary, aiSummary string) error {
	aiSummary = strings.TrimSpace(aiSummary)
	if aiSummary == "" {
		return fmt.Errorf("empty journal summary")
	}

	dir := journalDir(home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir journal: %w", err)
	}
	journalPath := filepath.Join(dir, agentName+".md")

	now := time.Now()
	var durS int64
	// Format: ## 2026-03-31 15:04 -- session abc123 (45m)
	var header string
	if !summary.Session.StartTime.IsZero() {
		dur := summary.Session.Duration()
		durS = int64(dur.Seconds())
		header = fmt.Sprintf("## %s -- session %s (%s)\n",
			now.Format("2006-01-02 15:04"),
			sessionID,
			formatDuration(dur),
		)
	} else {
		header = fmt.Sprintf("## %s -- session %s\n",
			now.Format("2006-01-02 15:04"),
			sessionID,
		)
	}
	entry := header + "\n" + aiSummary + "\n\n---\n\n"

	line, err := json.Marshal(journalRecord{
		Timestamp: now.UTC().Format(time.RFC3339),
		Agent:     agentName,
		SessionID: sessionID,
		Project:   project,
		Ticket:    ticket,
		DurationS: durS,
		Turns:     summary.TurnCount,
		Model:     summary.ModelName,
		Summary:   aiSummary,
	})
	if err != nil {
		return fmt.Errorf("marshal journal record: %w", err)
	}

	if err := appendFile(journalPath, []byte(entry)); err != nil {
		return fmt.Errorf("write journal: %w", err)
	}
	if err := appendFile(filepath.Join(dir, "entries.jsonl"), append(line, '\n')); err != nil {
		return fmt.Errorf("write journal sidecar: %w", err)
	}

	logging.Log("hook-session-end: journal written agent=%s session=%s path=%s", agentName, sessionID, journalPath)
	return nil
}

// formatDuration formats a duration as a human-readable string.
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	return fmt.Sprintf("%dh %dm", h, m)
}
