package closedlog

import (
	"path/filepath"
	"strings"
)

// resumeEnvStrip are the inherited vars that would point the resume helper at
// the caller's own window instead of the project being resumed.
var resumeEnvStrip = map[string]bool{
	"AGENTCTL_RUNTIME_DIR": true,
	"PWD":                  true,
	"AGENTCTL_INSTANCE":    true,
	"AGENTCTL_SESSION_ID":  true,
	"AGENTCTL_WINDOW_ID":   true,
	"TMUX_PANE":            true,
}

// ResumeArgs is the `agentctl` argument list that resumes r through the
// hidden hub-resume helper. The hub and `agentctl resume` share it.
func ResumeArgs(r Record) []string {
	work := r.WorkDir
	if work == "" {
		work = r.ProjectDir
	}
	return []string{"hub-resume", r.ProjectDir, "--work-dir", filepath.Clean(work), "--agent", r.Agent, "--session-id", r.SessionID}
}

// ResumeEnv pins the helper to dir's project: the caller's identity vars are
// dropped and the runtime dir and PWD point at dir. Pure: parent is passed in.
func ResumeEnv(dir string, parent []string) []string {
	out := make([]string, 0, len(parent)+2)
	for _, kv := range parent {
		name, _, _ := strings.Cut(kv, "=")
		if !resumeEnvStrip[name] {
			out = append(out, kv)
		}
	}
	return append(out, "PWD="+dir, "AGENTCTL_RUNTIME_DIR="+filepath.Join(dir, ".agentctl"))
}
