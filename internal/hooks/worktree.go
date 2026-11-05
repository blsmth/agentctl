package hooks

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/worktree"
)

// worktreeCreateInput is a permissive decode target for WorktreeCreate's
// stdin payload. The exact schema is NOT documented (see
// code.claude.com/docs/en/hooks). Verified empirically on 2026-08-20 against
// a real failing run: the actual payload carries session_id,
// transcript_path, cwd, prompt_id, agent_type, hook_event_name, and name --
// and NO branch field of any spelling:
//
//	{"session_id":"bc654418-...","transcript_path":"...",
//	 "cwd":"/Users/.../side-project","prompt_id":"65a8288b-...",
//	 "agent_type":"lead","hook_event_name":"WorktreeCreate",
//	 "name":"agent-a6a3ff01c3c89551d"}
//
// `branch`/`branchName` are kept in the decode target in case a future
// Claude Code version adds one -- see resolveBranch. Unknown fields are
// ignored (forward-compat), and every field has a fallback so a decode
// failure or a missing field is never fatal on its own.
type worktreeCreateInput struct {
	SessionID  string `json:"session_id"`
	Cwd        string `json:"cwd"`
	Branch     string `json:"branch"`
	BranchName string `json:"branchName"`
	Name       string `json:"name"`
}

// worktreeRemoveInput is the permissive decode target for WorktreeRemove.
type worktreeRemoveInput struct {
	SessionID         string `json:"session_id"`
	Cwd               string `json:"cwd"`
	WorktreePath      string `json:"worktree_path"`
	WorktreePathCamel string `json:"worktreePath"`
}

// writeWorktreeSettingsFn plants Claude Code hook settings into a freshly
// created worktree (the .claude/settings.local.json workaround for #37730 --
// see WriteWorktreeSettings's doc comment). It defaults to a no-op that logs
// a WARN: internal/hooks cannot import internal/dispatch (dispatch already
// imports hooks for EnsureShellPrefixScript, so the reverse import would be
// circular), so production wiring happens in main.go, mirroring the
// autonomousModeFn / SetAutonomousModeFn pattern in control.go.
//
// repoDir is passed through unchanged to dispatch.WriteWorktreeSettings as
// its ancestor-walk boundary -- this worktree, like an agentctl dispatch
// worktree, lives at <repoDir>/.worktrees/<slug>/, nested INSIDE the repo, so
// a hand-maintained <repoDir>/.claude/settings.local.json that already
// registers agentctl hooks is a second settings source Claude Code loads in
// addition to worktreeDir's own settings file. Without repoDir, the hooks
// decision could only see worktreeDir itself and would double-register hooks
// exactly like the bug this fixes in internal/dispatch.
//
// A worktree created without hook settings still works -- it just won't
// report Done or get a checkmark. A failed *creation* breaks the launch
// entirely. Degrade, never abort: this is why WorktreeCreate treats a
// writeWorktreeSettingsFn error as non-fatal.
var writeWorktreeSettingsFn = defaultWriteWorktreeSettings

// defaultWriteWorktreeSettings is the unwired default for
// writeWorktreeSettingsFn. It is a named function (rather than an inline
// closure) specifically so tests can call it directly and assert on the
// real default behavior, instead of only ever testing a stub they installed
// themselves.
func defaultWriteWorktreeSettings(worktreeDir, repoDir string) error {
	logging.Warn("hook-worktree-create: writeWorktreeSettingsFn not wired, skipping settings plant for %s (repoDir=%s)", worktreeDir, repoDir)
	return nil
}

// SetWriteWorktreeSettingsFn overrides the worktree-settings writer used by
// WorktreeCreate. Production code (main.go's hook fast path) wires this to a
// closure around dispatch.WriteWorktreeSettings; tests inject a stub.
func SetWriteWorktreeSettingsFn(fn func(worktreeDir, repoDir string) error) {
	writeWorktreeSettingsFn = fn
}

// WorktreeCreate is Claude Code's WorktreeCreate hook handler. It fires for
// --worktree, isolation: "worktree", and background sessions, and REPLACES
// Claude's default git worktree creation -- so it must never exit 0 without
// printing a worktree path on stdout. Every failure path here prints a
// diagnostic to stderr and exits non-zero; that is the actual bug this hook
// fixes: a prior Noop wiring exited 0 with empty stdout, which is
// the one outcome the caller cannot act on.
//
// Deliberately does NOT call guardAgentctl(): that helper drains stdin and
// returns early when AGENTCTL != 1, which would reproduce the exact bug for
// any caller invoking this hook outside an agentctl-managed window. This
// hook is about git mechanics, not agentctl window state.
func WorktreeCreate() {
	os.Exit(worktreeCreate(os.Stdin, os.Stdout, os.Stderr))
}

// worktreeCreate is the injectable, testable core of WorktreeCreate. It
// returns the process exit code rather than calling os.Exit directly.
func worktreeCreate(r io.Reader, out, errOut io.Writer) int {
	raw, _ := io.ReadAll(r)
	logging.Log("hook-worktree-create: raw payload: %s", string(raw))

	var input worktreeCreateInput
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &input); err != nil {
			// Not fatal by itself -- fall through with whatever fields we have
			// (likely none), and still try to produce a usable worktree.
			logging.Warn("hook-worktree-create: decode stdin: %v", err)
		}
	}

	repoDir, err := resolveRepoDir(input.Cwd)
	if err != nil {
		msg := fmt.Sprintf("hook-worktree-create: cannot resolve git repo (cwd=%q): %v\n", input.Cwd, err)
		logging.Error("%s", strings.TrimSpace(msg))
		fmt.Fprint(errOut, msg)
		return 1
	}

	branch := resolveBranch(input.Branch, input.BranchName, input.Name, input.SessionID)
	logging.Log("hook-worktree-create: repoDir=%s branch=%s", repoDir, branch)

	path, err := worktree.Create(repoDir, branch)
	if err != nil {
		msg := fmt.Sprintf("hook-worktree-create: worktree.Create failed (repo=%s branch=%s): %v\n", repoDir, branch, err)
		logging.Error("%s", strings.TrimSpace(msg))
		fmt.Fprint(errOut, msg)
		return 1
	}

	// Plant hook settings into the worktree -- NOT fatal on failure. A
	// worktree without hook settings still works; degrade, never abort.
	if err := writeWorktreeSettingsFn(path, repoDir); err != nil {
		logging.Warn("hook-worktree-create: writeWorktreeSettingsFn(%s): %v", path, err)
	}

	// Only a bare absolute path on stdout is accepted -- verified empirically
	// on 2026-08-20 against a real failing run (session bc654418): a
	// {"hookSpecificOutput": ...} JSON envelope was NOT accepted. The
	// consumer treated the whole JSON string as a relative path, joined it to
	// the repo root, and aborted the agent launch with a misleading "the hook
	// must create the directory before echoing its path" error -- even though
	// the directory had, in fact, already been created.
	fmt.Fprintln(out, path)
	logging.Log("hook-worktree-create: created worktree path=%s", path)
	return 0
}

// resolveRepoDir determines the git repo root for the worktree to be
// branched from. cwd is the input payload's working directory; if empty,
// falls back to the hook process's own os.Getwd().
func resolveRepoDir(cwd string) (string, error) {
	if cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("cwd empty and os.Getwd failed: %w", err)
		}
		cwd = wd
	}
	out, err := exec.Command("git", "-C", cwd, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("git -C %s rev-parse --show-toplevel: %w", cwd, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// randomSuffixFn generates the short random suffix appended to the
// session-ID fallback branch name in resolveBranch, so two concurrent agents
// in the same session never collide on the same branch (and therefore the
// same reused worktree directory -- see worktree.Create's directory-reuse
// fast path). It is a package-level function variable, mirroring the
// logBaseRef/warnBaseRefFallback seam in internal/worktree/worktree.go and
// the autonomousModeFn seam in control.go, purely so resolveBranch stays a
// pure function that tests can make deterministic instead of calling a rand
// source or time.Now() inline.
var randomSuffixFn = defaultRandomSuffix

func defaultRandomSuffix() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is essentially unheard-of; fall back to the
		// wall clock rather than propagating an error out of a function that
		// must never fail to produce a usable branch name.
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// resolveBranch picks the branch name per this fallback chain: explicit
// branch -> branchName alias -> name -> a generated name derived from the
// session ID. Never returns "" -- a generated branch name is always better
// than aborting the launch.
//
// `branch`/`branchName` are kept first in the chain purely as a forward-compat
// hook in case a future Claude Code version starts sending one -- the
// verified real-world payload (see worktreeCreateInput's doc comment) has
// neither.
//
// `name` is what the real payload actually carries, and it is unique per
// agent (e.g. "agent-a6a3ff01c3c89551d"), unlike session_id, which is shared
// by every concurrent agent dispatched within one session. Branching on
// "worktree/" + name keeps worktree branches in one namespace; branchSlug
// (internal/worktree/worktree.go) turns that into a directory name like
// "worktree-agent-a6a3ff01c3c89551d", which is unique per agent -- the whole
// point: a constant per-session branch name would make concurrent agents in
// one session silently share a single worktree.
//
// The session-ID fallback only fires when even `name` is absent -- a case
// not observed against any known Claude Code version. Because session_id IS
// constant across concurrent agents, that fallback alone would reproduce
// exactly the collision this fix exists to prevent, so it appends a short
// random suffix via randomSuffixFn. The tradeoff: a *resumed* session with no
// `name` gets a brand-new worktree each time rather than reusing one. That is
// the correct choice here -- silently sharing a working tree between two
// agents is the data-loss bug being fixed, and this path is a last resort
// that should not fire in practice.
func resolveBranch(branch, branchName, name, sessionID string) string {
	if branch != "" {
		return branch
	}
	if branchName != "" {
		return branchName
	}
	if name != "" {
		return "worktree/" + name
	}
	shortID := sessionID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}
	if shortID == "" {
		shortID = fmt.Sprintf("%d", time.Now().Unix())
	}
	return "worktree/" + shortID + "-" + randomSuffixFn()
}

// WorktreeRemove is Claude Code's WorktreeRemove hook handler. It fires at
// session exit, subagent finish, and background session delete. Per the
// hook contract this event has no structured output and cannot block --
// failures are debug-logged only -- so this handler always exits 0.
func WorktreeRemove() {
	worktreeRemove(os.Stdin)
}

// worktreeRemove is the injectable, testable core of WorktreeRemove.
func worktreeRemove(r io.Reader) {
	raw, _ := io.ReadAll(r)
	logging.Log("hook-worktree-remove: raw payload: %s", string(raw))

	var input worktreeRemoveInput
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &input); err != nil {
			logging.Warn("hook-worktree-remove: decode stdin: %v", err)
		}
	}

	path := resolveWorktreePath(input)
	if path == "" {
		logging.Log("hook-worktree-remove: no worktree path found in payload, nothing to remove")
		return
	}

	if err := worktree.Remove(path); err != nil {
		logging.Warn("hook-worktree-remove: worktree.Remove(%s): %v", path, err)
		return
	}
	logging.Log("hook-worktree-remove: removed worktree path=%s", path)
}

// resolveWorktreePath looks for the worktree path under any of the plausible
// aliases the undocumented schema might use, falling back to cwd.
func resolveWorktreePath(input worktreeRemoveInput) string {
	if input.WorktreePath != "" {
		return input.WorktreePath
	}
	if input.WorktreePathCamel != "" {
		return input.WorktreePathCamel
	}
	return input.Cwd
}
