package cronedit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/cron"
	"github.com/blsmth/agentctl/internal/selfpath"
)

// RunFunc fires a job now with plain `cron run` semantics: overlap,
// max_concurrent and the usage gate all apply, and there is no force. It
// returns the run entry (fired, skipped:* or failed:*). It is the seam the
// hub and serve inject; tests stub it and must never reach RunSubprocess.
type RunFunc func(ctx context.Context, name string) (cron.Entry, error)

// RunTimeout bounds one run-now subprocess (starting a detached session
// and a worktree can take a while).
const RunTimeout = 2 * time.Minute

// RunOutcome is what a run-now reports to a UI.
type RunOutcome struct {
	// Fired is set when a window was launched.
	Fired bool `json:"fired"`
	// Result is the run entry's result (fired, skipped:overlap, ...).
	Result string `json:"result"`
	// Reason explains a skip or failure (the entry's note).
	Reason   string     `json:"reason,omitempty"`
	WindowID string     `json:"window_id,omitempty"`
	Session  string     `json:"session,omitempty"`
	Entry    cron.Entry `json:"entry"`
}

// OutcomeOf turns a run entry into a RunOutcome.
func OutcomeOf(e cron.Entry) RunOutcome {
	return RunOutcome{
		Fired: e.Result == cron.ResultFired, Result: e.Result, Reason: e.Note,
		WindowID: e.WindowID, Session: e.Session, Entry: e,
	}
}

// RunArgs is the argv (after the executable) of a run-now.
func RunArgs(name string) []string { return []string{"cron", "run", "--json", "--", name} }

// runNameRE is cron's job name rule; anything else never reaches exec.
var runNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,47}$`)

// stripRunEnv are inherited variables that would tie the subprocess to the
// caller's own window or project rather than the job's, unlike the launchd
// tick it imitates.
var stripRunEnv = []string{
	"AGENTCTL_INSTANCE", "AGENTCTL_SESSION_ID", "AGENTCTL_WINDOW_ID", "AGENTCTL_KEY_WINDOW",
	"AGENTCTL_KEY_CLIENT", "AGENTCTL_RUNTIME_DIR", "TMUX_PANE",
}

// RunEnv builds the subprocess environment from parent.
func RunEnv(parent []string) []string {
	out := make([]string, 0, len(parent))
	for _, kv := range parent {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(stripRunEnv, name) {
			out = append(out, kv)
		}
	}
	return out
}

// ParseRunOutput turns the subprocess result into its entry. A failed run
// reports the last line of its stderr.
func ParseRunOutput(name string, stdout, stderr []byte, runErr error) (cron.Entry, error) {
	if runErr != nil {
		msg := lastLine(string(stderr))
		if msg == "" {
			msg = lastLine(string(stdout))
		}
		if msg == "" {
			return cron.Entry{}, fmt.Errorf("cron run %s: %w", name, runErr)
		}
		return cron.Entry{}, fmt.Errorf("cron run %s: %s", name, strings.TrimPrefix(msg, "agentctl: "))
	}
	var e cron.Entry
	if err := json.Unmarshal(bytes.TrimSpace(stdout), &e); err != nil {
		return cron.Entry{}, fmt.Errorf("cron run %s: parsing result: %w", name, err)
	}
	if e.Job != name || e.Result == "" {
		return cron.Entry{}, fmt.Errorf("cron run %s: unexpected result for job %q", name, e.Job)
	}
	return e, nil
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSpace(s)
}

// RunSubprocess is the production RunFunc: it runs the running agentctl
// binary as `agentctl cron run --json -- <name>`, which binds the job's
// project and launches through the same path as a scheduled fire. Running
// out of process keeps the project binding (process-global runtime dir and
// tmux session) out of the long-lived hub or serve process. Inside a test
// binary selfpath.Spawnable refuses, so a missed stub fails instead of
// re-running the test suite.
func RunSubprocess(ctx context.Context, name string) (cron.Entry, error) {
	if !runNameRE.MatchString(name) {
		return cron.Entry{}, &NotFoundError{Name: name}
	}
	exe, err := selfpath.Spawnable()
	if err != nil {
		return cron.Entry{}, fmt.Errorf("locating agentctl: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, RunTimeout)
	defer cancel()
	c := exec.CommandContext(ctx, exe, RunArgs(name)...)
	if home, err := os.UserHomeDir(); err == nil {
		c.Dir = home
	}
	c.Env = RunEnv(os.Environ())
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	runErr := c.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return cron.Entry{}, fmt.Errorf("cron run %s: timed out after %s", name, RunTimeout)
	}
	return ParseRunOutput(name, stdout.Bytes(), stderr.Bytes(), runErr)
}
