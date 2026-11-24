package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
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

func spawnHubResume(r closedlog.Record) (string, error) {
	exe, err := selfpath.Spawnable()
	if err != nil {
		return "", fmt.Errorf("locating agentctl: %w", err)
	}
	c := exec.Command(exe, closedlog.ResumeArgs(r)...)
	c.Dir = r.ProjectDir
	c.Env = closedlog.ResumeEnv(r.ProjectDir, os.Environ())
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
