package dispatch

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/logging"
)

// resolveAgentBody looks up the launching agent's own .md definition body
// (composed base+local body when the agent extends another, otherwise the
// raw body) so its @shared includes can be expanded into the system prompt
// (see prompt.go). extraDirs are additional project directories to check for
// project-local .claude/agents/ (e.g. the dispatch working directory),
// alongside the usual cwd + configured agent dirs.
//
// Returns "" if the agent cannot be resolved for any reason. Per the calling
// contract in Launch(), a resolution failure here must never fail a dispatch
// -- it only means the system prompt won't get the expanded shared
// conventions section, which is a degraded-but-safe outcome (the raw
// "@shared/..." line in the agent's own .md is still there, even though
// Claude Code won't expand it).
func resolveAgentBody(cfg *config.Config, agentName string, extraDirs ...string) string {
	agents, err := agent.DiscoverAll(cfg, projectDirsForBodyResolve(cfg, extraDirs...))
	if err != nil {
		logging.Warn("dispatch: resolving agent body for %q: discover failed: %v", agentName, err)
		return ""
	}

	for _, a := range agents {
		if a.Name != agentName {
			continue
		}
		warnShadow(a)
		if a.ComposedBody != "" {
			return a.ComposedBody
		}
		body, err := agent.ReadBody(a.FilePath)
		if err != nil {
			logging.Warn("dispatch: resolving agent body for %q: ReadBody(%s): %v", agentName, a.FilePath, err)
			return ""
		}
		return body
	}

	logging.Warn("dispatch: resolving agent body for %q: agent not found in discovery results", agentName)
	return ""
}

// shadowWarnOut is where warnShadow writes; a variable so tests can capture it.
var shadowWarnOut io.Writer = os.Stderr

// warnShadow tells the user, visibly, when the agent being launched is a
// project-local definition that replaces a global agent of the same name: a
// cloned repo can ship .claude/agents/<name>.md to swap in its own
// instructions under a trusted name.
func warnShadow(a agent.Agent) {
	if a.Shadows == "" {
		return
	}
	msg := fmt.Sprintf("agentctl: warning: agent %q is defined by project file %s, which shadows your global agent %s",
		a.Name, a.FilePath, a.Shadows)
	fmt.Fprintln(shadowWarnOut, msg)
	logging.Warn("dispatch: %s", msg)
}

// projectDirsForBodyResolve returns the project root directories to pass to
// agent.DiscoverAll so project-local .claude/agents/ definitions correctly
// shadow global ones. extraDirs (typically the dispatch working directory /
// repo directory) take priority, ahead of the current process cwd and any
// agent-configured dirs.
func projectDirsForBodyResolve(cfg *config.Config, extraDirs ...string) []string {
	cwd, _ := os.Getwd()
	first := append(append([]string{}, extraDirs...), cwd)
	return agent.ProjectDirs(cfg, first...)
}

// messagingTools are the cross-session messaging tools an agent needs.
var messagingTools = []string{"SendMessage", "ListAgents"}

// missingMessagingTools returns the messaging tools absent from a frontmatter
// `tools:` value. An empty value means "all tools" and reports none missing.
func missingMessagingTools(tools string) []string {
	if strings.TrimSpace(tools) == "" {
		return nil
	}
	have := map[string]bool{}
	for _, t := range strings.FieldsFunc(tools, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' }) {
		have[t] = true
	}
	var missing []string
	for _, t := range messagingTools {
		if !have[t] {
			missing = append(missing, t)
		}
	}
	return missing
}

// warnMissingMessagingTools prints a one-line stderr warning when the target
// agent's definition has a restrictive `tools:` list that omits SendMessage or
// ListAgents (Claude Code hides unlisted tools). Never fails the dispatch.
func warnMissingMessagingTools(cfg *config.Config, agentName string, extraDirs ...string) {
	agents, err := agent.DiscoverAll(cfg, projectDirsForBodyResolve(cfg, extraDirs...))
	if err != nil {
		return
	}
	for _, a := range agents {
		if a.Name != agentName {
			continue
		}
		if missing := missingMessagingTools(a.Tools); len(missing) > 0 {
			fmt.Fprintf(shadowWarnOut, "agentctl: warning: %s has a tools: list without %s; the agent cannot message other sessions (add it to the frontmatter)\n", a.FilePath, strings.Join(missing, " and "))
		}
		return
	}
}
