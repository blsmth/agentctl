package dispatch

import (
	"fmt"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/logging"
)

// frontmatterRoleFn returns the role in agentName's definition frontmatter
// (agent.RoleOrchestrator, agent.RoleWorker or ""). A var for tests.
var frontmatterRoleFn = frontmatterRole

func frontmatterRole(cfg *config.Config, agentName, workDir string) string {
	agents, err := agent.DiscoverAll(cfg, projectDirsForBodyResolve(cfg, workDir))
	if err != nil {
		return ""
	}
	for _, a := range agents {
		if a.Name == agentName {
			return a.Role
		}
	}
	return ""
}

// applyAgentRole adapts a launch to the agent's frontmatter role. Workers
// (role "worker" or empty) are returned unchanged. An orchestrator never gets
// the worker tail: no finishing steps (commit, push, open a PR) and no "do not
// merge to main" worktree line, and its preamble is the orchestrator one. The
// role only ever upgrades the mode's preamble role, never downgrades it.
//
// How it gets there depends on who is launching:
//
//   - A human-initiated ad-hoc launch (the phone's new-session button,
//     `dispatch --prompt`) is converted to ModeBare: an interactive session
//     with the prompt as its first user message, no execInstructions and the
//     interactive permission mode. The caller's worktree choice is kept.
//   - An unattended launch (ad-hoc with ScheduledBy set, i.e. cron, and
//     ModeDispatch) keeps its mode and autonomous instructions; only the
//     preamble role and the finishing steps change.
func applyAgentRole(req Request, spec modeSpec, role string) (Request, modeSpec) {
	if role != agent.RoleOrchestrator {
		return req, spec
	}
	if req.Mode == ModeAdhoc && req.ScheduledBy == "" {
		if req.UseWorktree == nil {
			// Keep the ad-hoc default rather than inheriting bare's.
			useWorktree := spec.DefaultWorktree
			req.UseWorktree = &useWorktree
		}
		logging.Log("dispatch: %s is an orchestrator; launching the ad-hoc prompt as an interactive session", req.AgentName)
		req.Mode = ModeBare
		return req, modes[ModeBare]
	}
	spec.Role = RoleOrchestrator
	spec.IncludeFinishingSteps = false
	if req.Mode == ModeDispatch {
		spec.DefaultUserMessage = orchestratorTicketMessage
	}
	return req, spec
}

// orchestratorTicketMessage replaces ModeDispatch's "open a PR when done"
// first message for an orchestrator, which delegates rather than implements.
func orchestratorTicketMessage(req Request) string {
	if req.TicketTitle != "" {
		return fmt.Sprintf("Handle #%d: %s. The issue is in your system prompt.", req.Ticket, req.TicketTitle)
	}
	return fmt.Sprintf("Handle #%d. The issue is in your system prompt.", req.Ticket)
}
