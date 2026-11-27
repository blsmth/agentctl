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

// agentBody is the launching agent's definition text as discovery resolved
// it: its own .md body and, when it extends a base that may be used, the
// base's body.
type agentBody struct {
	Own      string // the agent's own .md body (what --agent loads)
	BaseName string // "" when standalone, unresolved or refused
	Base     string // the base's body; "" with BaseName
}

// composed is the merged base+own body (agent.Compose's layout), or Own.
func (b agentBody) composed() string {
	if b.Base == "" {
		return b.Own
	}
	if b.Own == "" {
		return b.Base
	}
	return b.Base + "\n\n---\n\n## Local Overrides\n\n" + b.Own
}

// resolveAgentBody looks up the launching agent's own .md definition body
// and, when it extends another agent, the base's body (see agentPromptFor
// for where each goes). extraDirs are additional project directories to
// check for project-local .claude/agents/ (e.g. the dispatch working
// directory), alongside the usual cwd + configured agent dirs.
//
// Returns the zero agentBody if the agent cannot be resolved for any
// reason. Per the calling contract in Launch(), a resolution failure here
// must never fail a dispatch -- it only means the system prompt won't get
// the expanded shared conventions section or the base agent, which is a
// degraded-but-safe outcome (the raw "@shared/..." line in the agent's own
// .md is still there, even though Claude Code won't expand it).
func resolveAgentBody(cfg *config.Config, agentName string, extraDirs ...string) agentBody {
	agents, err := agent.DiscoverAll(cfg, projectDirsForBodyResolve(cfg, extraDirs...))
	if err != nil {
		logging.Warn("dispatch: resolving agent body for %q: discover failed: %v", agentName, err)
		return agentBody{}
	}

	for _, a := range agents {
		if a.Name != agentName {
			continue
		}
		warnShadow(a)
		body, err := agent.ReadBody(a.FilePath)
		if err != nil {
			logging.Warn("dispatch: resolving agent body for %q: ReadBody(%s): %v", agentName, a.FilePath, err)
			return agentBody{}
		}
		out := agentBody{Own: strings.TrimRight(body, "\n")}
		if a.BasePath != "" && baseTrusted(a, agents) {
			out.BaseName, out.Base = a.BaseName, a.BaseBody
		}
		return out
	}

	logging.Warn("dispatch: resolving agent body for %q: agent not found in discovery results", agentName)
	return agentBody{}
}

// baseTrusted reports whether a's resolved base may be injected, warning
// (like warnShadow) when it is not or when it is a project file. A base is
// never less trusted than the agent extending it: a global agent (a regular
// file in ~/.claude/agents) gets only a global base, so a cloned repo's
// .claude/agents/<base>.md cannot slip text in under a trusted global
// agent's name. A project-local agent is already project-controlled, so
// its base may be local too, but a local base that shadows a global agent
// of the same name is reported.
func baseTrusted(a agent.Agent, agents []agent.Agent) bool {
	if agent.IsRegistryFile(a.BasePath) {
		return true
	}
	if agent.IsRegistryFile(a.FilePath) {
		msg := fmt.Sprintf("agentctl: warning: global agent %q extends %q, which resolves to %s outside your global agents dir; the base is not loaded",
			a.Name, a.BaseName, a.BasePath)
		fmt.Fprintln(shadowWarnOut, msg)
		logging.Warn("dispatch: %s", msg)
		return false
	}
	for _, b := range agents {
		if b.FilePath == a.BasePath && b.Shadows != "" {
			msg := fmt.Sprintf("agentctl: warning: agent %q extends %q, defined by project file %s, which shadows your global agent %s",
				a.Name, b.Name, b.FilePath, b.Shadows)
			fmt.Fprintln(shadowWarnOut, msg)
			logging.Warn("dispatch: %s", msg)
			break
		}
	}
	return true
}

// agentPrompt is what a launch injects into the system prompt file from the
// launching agent's definition.
type agentPrompt struct {
	// Persona is the full (base+own) body, for harnesses without --agent.
	Persona string
	// Base and BaseName are the extended base's body, for harnesses that
	// load the agent's own file by name (--agent) and so never see the base.
	Base, BaseName string
	// SharedConventions is the expanded @shared includes of base and own
	// body together, each file once.
	SharedConventions string
}

// agentPromptFor resolves agentName's definition and splits it into the
// system prompt sections a harness needs: with agentFlag (Claude's
// --agent, which loads only the agent's own .md body) the base body is
// injected separately and the own body is not repeated; without it (pi)
// the whole composed body travels as the persona.
func agentPromptFor(cfg *config.Config, agentName string, agentFlag bool, extraDirs ...string) agentPrompt {
	b := resolveAgentBody(cfg, agentName, extraDirs...)
	body := b.composed()
	if body == "" {
		return agentPrompt{}
	}
	var p agentPrompt
	expanded, missing := agent.ExpandSharedRefs(body)
	p.SharedConventions = expanded
	for _, ref := range missing {
		logging.Warn("dispatch: agent %q references unresolved shared include %q", agentName, ref)
	}
	switch {
	case !agentFlag:
		p.Persona = body
	case b.Base != "":
		p.Base, p.BaseName = b.Base, b.BaseName
		logging.Log("dispatch: injecting base agent %q for %s (%d bytes)", b.BaseName, agentName, len(b.Base))
	}
	if expanded != "" {
		logging.Log("dispatch: injecting shared conventions for %s (%d bytes)", agentName, len(expanded))
	}
	return p
}

// AgentSystemPrompt is the part of a launch's system prompt file that comes
// from agentName's own definition (its base agent, or its persona for a
// harness without --agent, then its expanded shared conventions), for launch
// paths outside this package (improve). "" when there is nothing to add or
// the agent cannot be resolved.
func AgentSystemPrompt(cfg *config.Config, agentName string, agentFlag bool, dirs ...string) string {
	p := agentPromptFor(cfg, agentName, agentFlag, dirs...)
	return joinParts(agentDefinitionSections(agentName, p)...)
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
