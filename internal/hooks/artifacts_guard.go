package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/blsmth/agentctl/internal/config"
)

// artifactsTamperReason is the deny reason for commands that write the
// artifact store directly.
const artifactsTamperReason = "\U0001F6D1 BLOCKED: this writes agentctl's artifact store directly. " +
	"`agentctl artifact` is the only writer (it enforces size limits and keeps the metadata consistent): " +
	"use `agentctl artifact add|update|pin|rm`, and `agentctl artifact list|show|url` to read them."

// artifactsTargetPattern matches a reference to the artifact store under the
// default and XDG state dirs. The resolved dir is matched literally on top,
// see artifactsStateDir.
const artifactsTargetPattern = `(?:state/agentctl|XDG_STATE_HOME\}?/agentctl)/artifacts\b`

// artifactsStateDir is the resolved <StateDir>/artifacts. A var so tests can
// pin it. Resolved on each call, never at init.
var artifactsStateDir = func() string {
	dir, err := config.StateDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "artifacts")
}

// checkArtifactsTamper reports whether command writes the artifact store: the
// same write shapes checkCronTamper denies (redirect, write verb, in-place
// sed, script interpreter). Reading is fine, and the arguments of agentctl
// commands are masked first, so `agentctl artifact add ...` passes whatever
// its title says. A speed bump, not a sandbox.
func checkArtifactsTamper(command string) string {
	if !strings.Contains(command, "artifacts") {
		return ""
	}
	targets := []string{artifactsTargetPattern}
	if dir := artifactsStateDir(); dir != "" {
		targets = append(targets, regexp.QuoteMeta(dir)+`\b`)
		if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(dir, home+"/") {
			rel := regexp.QuoteMeta(strings.TrimPrefix(dir, home+"/"))
			targets = append(targets, `(?:~|\$HOME|\$\{HOME\})/`+rel+`\b`)
		}
	}
	if matchesWriteTemplates(maskAgentctlArgs(command), targets) {
		return artifactsTamperReason
	}
	return ""
}

// artifactsEditTarget returns the artifact store path a file-editing tool
// call would write ("" when it is not one): the Edit/Write counterpart of
// checkArtifactsTamper. Symlinked parents are resolved too.
func artifactsEditTarget(toolName string, rawInput json.RawMessage) string {
	p := editToolPath(toolName, rawInput)
	if p == "" {
		return ""
	}
	dir := artifactsStateDir()
	if dir == "" {
		return ""
	}
	dir = filepath.Clean(dir)
	sep := string(filepath.Separator)
	inside := func(c, d string) bool { return c == d || strings.HasPrefix(c, d+sep) }
	dirs := []string{dir}
	if rd := resolveExistingPrefix(dir); rd != dir {
		dirs = append(dirs, rd)
	}
	for _, c := range []string{p, resolveExistingPrefix(p)} {
		for _, d := range dirs {
			if inside(c, d) {
				return p
			}
		}
	}
	return ""
}

// artifactsEditDenyReason is shown to the model when it tries to edit the
// artifact store directly.
func artifactsEditDenyReason(path string) string {
	return fmt.Sprintf("\U0001F6D1 BLOCKED: %s is agentctl's artifact store. "+
		"`agentctl artifact` is the only writer: use `agentctl artifact add|update|pin|rm`, "+
		"and `agentctl artifact list|show|url` to read them.", path)
}
