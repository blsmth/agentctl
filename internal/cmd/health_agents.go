package cmd

import (
	"fmt"
	"io"
	"strings"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/config"
)

// healthDeferredTools are Claude Code tools that load only through ToolSearch.
var healthDeferredTools = []string{"CronCreate", "CronList", "CronDelete", "Monitor"}

// healthFinding is one agent-definition warning.
type healthFinding struct {
	Agent    string
	FilePath string
	Message  string
}

// splitToolList splits a raw comma-separated tools: value into trimmed,
// non-empty entries. Commas inside parentheses (as in `Bash(git *, ls)`)
// do not split, so patterns stay one entry.
func splitToolList(raw string) []string {
	var out []string
	depth, start := 0, 0
	flush := func(end int) {
		if s := strings.TrimSpace(raw[start:end]); s != "" {
			out = append(out, s)
		}
	}
	for i, r := range raw {
		switch r {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				flush(i)
				start = i + 1
			}
		}
	}
	flush(len(raw))
	return out
}

// checkAgentTools flags tools: lists that contradict an agent's role.
// Built-in agents without a definition file are skipped.
func checkAgentTools(agents []agent.Agent) []healthFinding {
	var out []healthFinding
	for _, a := range agents {
		if a.FilePath == "" {
			continue
		}
		tools := splitToolList(a.Tools)
		if len(tools) == 0 {
			continue
		}
		have := make(map[string]bool, len(tools))
		for _, t := range tools {
			have[t] = true
		}
		if a.Role == agent.RoleOrchestrator {
			out = append(out, healthFinding{a.Name, a.FilePath,
				"orchestrator with a tools: list; remove it so it gets all tools"})
		}
		if have["ToolSearch"] {
			continue
		}
		var listed []string
		for _, d := range healthDeferredTools {
			if have[d] {
				listed = append(listed, d)
			}
		}
		if len(listed) > 0 {
			out = append(out, healthFinding{a.Name, a.FilePath,
				fmt.Sprintf("deferred tools listed without ToolSearch (%s)", strings.Join(listed, ", "))})
		}
	}
	return out
}

// printAgentToolsHealth discovers agents as dispatch does (global plus the
// project-local dir under cwd) and reports tools: findings. Warnings never
// affect the exit code.
func printAgentToolsHealth(w io.Writer, cwd string) {
	cfg := config.Load()
	agents, err := agent.DiscoverAll(cfg, agent.ProjectDirs(cfg, cwd))
	if err != nil {
		fmt.Fprintf(w, "Agents:  could not discover agents: %v\n", err)
		return
	}
	findings := checkAgentTools(agents)
	if len(findings) == 0 {
		fmt.Fprintf(w, "Agents:  ok (%d checked, tools: lists consistent with roles)\n", len(agents))
		return
	}
	fmt.Fprintf(w, "Agents:  warn: %d tools: problem(s)\n", len(findings))
	for _, f := range findings {
		fmt.Fprintf(w, "    warn  %-24s %s (%s)\n", f.Agent, f.Message, f.FilePath)
	}
}
