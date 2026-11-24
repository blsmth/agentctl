package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/blsmth/agentctl/internal/closedlog"
	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/selfpath"
)

// resumeSpawn runs `agentctl hub-resume` for a closed record, the same helper
// the hub's Resume uses, and returns its stdout. A seam for tests.
var resumeSpawn = spawnHubResume

// RunResume is `agentctl resume <handle>`: resume a closed session by its
// handle (or full session ID) through hub-resume, so there is one resume path.
func RunResume(out io.Writer, ref string) error {
	ix := fleet.NewIndex(liveGroups(), allClosed())
	s, err := ix.Resolve(ref)
	if err != nil {
		return err
	}
	if s.Live {
		return fmt.Errorf("session %s %q is still open; resume is for closed sessions (see `agentctl status list --closed`)", s.Handle, s.Label)
	}
	rec := *s.Closed
	if rec.ProjectDir == "" {
		return fmt.Errorf("session %s %q has no recorded project directory to resume in", s.Handle, s.Label)
	}
	if _, err := resumeSpawn(rec); err != nil {
		return fmt.Errorf("resuming %s: %w", s.Handle, err)
	}
	fmt.Fprintf(out, "Resumed %q (%s) in %s\n", s.Label, s.Handle, rec.Project())
	return nil
}

// resumeEnvStrip are the inherited vars that would point the helper at the
// caller's own window instead of the project being resumed.
var resumeEnvStrip = []string{"AGENTCTL_RUNTIME_DIR", "PWD", "AGENTCTL_INSTANCE", "AGENTCTL_SESSION_ID", "AGENTCTL_WINDOW_ID", "TMUX_PANE"}

func spawnHubResume(r closedlog.Record) (string, error) {
	exe, err := selfpath.Spawnable()
	if err != nil {
		return "", fmt.Errorf("locating agentctl: %w", err)
	}
	work := r.WorkDir
	if work == "" {
		work = r.ProjectDir
	}
	c := exec.Command(exe, "hub-resume", r.ProjectDir, "--work-dir", filepath.Clean(work), "--agent", r.Agent, "--session-id", r.SessionID)
	c.Dir = r.ProjectDir
	c.Env = resumeEnv(r.ProjectDir, os.Environ())
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	if err := c.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if i := strings.LastIndex(msg, "\n"); i >= 0 {
			msg = msg[i+1:]
		}
		if msg = strings.TrimPrefix(msg, "Error: "); msg != "" {
			return "", errors.New(msg)
		}
		return "", err
	}
	return stdout.String(), nil
}

// resumeEnv pins the helper to dir's project: identity vars dropped, runtime
// dir and PWD set. Pure: parent is passed in.
func resumeEnv(dir string, parent []string) []string {
	out := make([]string, 0, len(parent)+2)
	for _, kv := range parent {
		name, _, _ := strings.Cut(kv, "=")
		skip := false
		for _, s := range resumeEnvStrip {
			skip = skip || s == name
		}
		if !skip {
			out = append(out, kv)
		}
	}
	return append(out, "PWD="+dir, "AGENTCTL_RUNTIME_DIR="+filepath.Join(dir, ".agentctl"))
}
