// Package sweeprun runs `agentctl sweep` for one tmux session as a
// subprocess and parses its JSON report. The hub and `agentctl serve` share
// it.
//
// Sweep runs out of process on purpose: the in-process entry point
// (cmd.RunSweep) assigns the global tmux.SessionName, which would corrupt a
// long-lived hub or serve process that acts on many sessions.
package sweeprun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/selfpath"
)

// Row is one window in a sweep report.
type Row struct {
	WindowID string `json:"window_id"`
	Name     string `json:"name"`
	Agent    string `json:"agent,omitempty"`
	Status   string `json:"status"`
	// Detail is the close reason (Closed), the action (Attention), or empty
	// (in progress / skipped).
	Detail string   `json:"detail,omitempty"`
	Errors []string `json:"errors,omitempty"`
}

// Report is the result of a sweep, as `agentctl sweep --json` prints it.
type Report struct {
	Session    string `json:"session"`
	DryRun     bool   `json:"dry_run,omitempty"`
	Closed     []Row  `json:"closed"`
	Attention  []Row  `json:"attention"`
	InProgress []Row  `json:"in_progress"`
	Skipped    []Row  `json:"skipped"`
}

// Failed returns the closed rows that reported an error.
func (r Report) Failed() []Row {
	var out []Row
	for _, row := range r.Closed {
		if len(row.Errors) > 0 {
			out = append(out, row)
		}
	}
	return out
}

// ClosedNames lists the names of the windows in Closed.
func (r Report) ClosedNames() []string {
	out := make([]string, len(r.Closed))
	for i, row := range r.Closed {
		out[i] = row.Name
	}
	return out
}

// Plan summarises a dry run for a confirm prompt:
// "close 3 (a, b, c), 2 need attention, 1 in progress". Zero counts are left
// out; nothing at all reads "nothing to sweep".
func (r Report) Plan() string {
	var parts []string
	if n := len(r.Closed); n > 0 {
		parts = append(parts, fmt.Sprintf("close %d (%s)", n, strings.Join(r.ClosedNames(), ", ")))
	}
	if n := len(r.Attention); n > 0 {
		verb := "needs"
		if n > 1 {
			verb = "need"
		}
		parts = append(parts, fmt.Sprintf("%d %s attention", n, verb))
	}
	if n := len(r.InProgress); n > 0 {
		parts = append(parts, fmt.Sprintf("%d in progress", n))
	}
	if len(parts) == 0 {
		return "nothing to sweep"
	}
	return strings.Join(parts, ", ")
}

// Target is the session to sweep.
type Target struct {
	// Session is the tmux session name.
	Session string
	// RuntimeDir is the session's .agentctl directory; the subprocess is
	// pinned to it so it reads that project's window records.
	RuntimeDir string
	// ProjectDir is the subprocess's working directory (optional; it
	// resolves PR lookups for windows without a worktree).
	ProjectDir string
	// IncludeBare also closes finished bare (interactive) sessions
	// (`agentctl sweep --include-bare`). The hub's sweep leaves it off.
	IncludeBare bool
}

// HubSession is the fleet hub's tmux session (tmux.HubSessionName), which is
// never a sweep target. Duplicated so this package stays a leaf.
const HubSession = "agentctl-hub"

// ErrBadTarget is returned for a target that is not an agentctl session.
var ErrBadTarget = errors.New("not an agentctl session")

// Validate rejects a target that does not look like an agentctl project
// session: a tmux target spec (":" or "."), the hub session, or a runtime dir
// that is not an absolute ".agentctl" path.
func (t Target) Validate() error {
	if t.Session == "" || t.Session == HubSession || strings.ContainsAny(t.Session, ":.") ||
		strings.HasPrefix(t.Session, "-") || !filepath.IsAbs(t.RuntimeDir) || filepath.Base(t.RuntimeDir) != ".agentctl" {
		return fmt.Errorf("%w: session %q, runtime dir %q", ErrBadTarget, t.Session, t.RuntimeDir)
	}
	return nil
}

// Func runs a sweep (dryRun = plan only). It is the seam the hub and serve
// inject; tests stub it and must never reach Run.
type Func func(ctx context.Context, t Target, dryRun bool) (Report, error)

// Timeout bounds one sweep subprocess. A sweep may look up PRs with gh for
// every finished window, so it can take a while.
const Timeout = 2 * time.Minute

// Args is the argv (after the executable) for sweeping session.
func Args(session string, dryRun, includeBare bool) []string {
	args := []string{"sweep", "--session", session, "--json"}
	if dryRun {
		args = append(args, "--dry-run")
	}
	if includeBare {
		args = append(args, "--include-bare")
	}
	return args
}

// stripEnv are inherited variables that would make the subprocess act on the
// caller's own session or window instead of the target. TMUX_PANE is kept on
// purpose: sweep never closes the window owning it, which is the hub's own
// list window when the hub runs it.
var stripEnv = []string{
	"AGENTCTL_INSTANCE",
	"AGENTCTL_SESSION_ID",
	"AGENTCTL_WINDOW_ID",
	"AGENTCTL_KEY_WINDOW",
	"AGENTCTL_KEY_CLIENT",
	"AGENTCTL_RUNTIME_DIR",
}

// Env builds the subprocess environment from parent: identity variables are
// dropped and AGENTCTL_RUNTIME_DIR is pinned to t's runtime dir.
func Env(t Target, parent []string) []string {
	out := make([]string, 0, len(parent)+1)
	for _, kv := range parent {
		name, _, _ := strings.Cut(kv, "=")
		if slices.Contains(stripEnv, name) {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "AGENTCTL_RUNTIME_DIR="+t.RuntimeDir)
}

// Parse turns the subprocess result into a report. A failed run reports the
// last line of its stderr (or stdout); output that is not a report for
// session is an error too.
func Parse(session string, stdout, stderr []byte, runErr error) (Report, error) {
	if runErr != nil {
		msg := lastLine(string(stderr))
		if msg == "" {
			msg = lastLine(string(stdout))
		}
		if msg == "" {
			return Report{}, fmt.Errorf("sweep %s: %w", session, runErr)
		}
		return Report{}, fmt.Errorf("sweep %s: %s", session, strings.TrimPrefix(msg, "agentctl: "))
	}
	var rep Report
	if err := json.Unmarshal(stdout, &rep); err != nil {
		return Report{}, fmt.Errorf("sweep %s: parsing report: %w", session, err)
	}
	if rep.Session != session {
		return Report{}, fmt.Errorf("sweep %s: report is for session %q", session, rep.Session)
	}
	return rep, nil
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSpace(s)
}

// Run is the production Func: it runs the running agentctl binary as
// `agentctl sweep --session <t.Session> --json [--dry-run] [--include-bare]`. Inside a test
// binary selfpath.Spawnable refuses, so a missed stub fails instead of
// re-running the test suite.
func Run(ctx context.Context, t Target, dryRun bool) (Report, error) {
	if err := t.Validate(); err != nil {
		return Report{}, err
	}
	exe, err := selfpath.Spawnable()
	if err != nil {
		return Report{}, fmt.Errorf("locating agentctl: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	c := exec.CommandContext(ctx, exe, Args(t.Session, dryRun, t.IncludeBare)...)
	c.Dir = t.ProjectDir
	if c.Dir == "" {
		c.Dir = filepath.Dir(t.RuntimeDir)
	}
	c.Env = Env(t, os.Environ())
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	runErr := c.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return Report{}, fmt.Errorf("sweep %s: timed out after %s", t.Session, Timeout)
	}
	return Parse(t.Session, stdout.Bytes(), stderr.Bytes(), runErr)
}
