package dispatch

import (
	"fmt"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/logging"
)

// humanLaunch reports whether req was issued by a person rather than by an
// agent session: a Detached launch (the phone) or one with no tracked
// dispatching session.
func humanLaunch(req Request) bool {
	return req.Detached || !resolveOrchestratorFn().Known()
}

// frontmatterRoleFn returns the role in agentName's definition frontmatter
// (agent.RoleOrchestrator, agent.RoleWorker, agent.RoleReporter or ""). A
// var for tests.
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
// (role "worker" or empty) are returned unchanged; a reporter is handled by
// applyReporterRole. An orchestrator never gets
// the worker tail: no finishing steps (commit, push, open a PR) and no "do not
// merge to main" worktree line, and its preamble is the orchestrator one. The
// role only ever upgrades the mode's preamble role, never downgrades it.
//
// How it gets there depends on who is launching:
//
//   - A human-initiated ad-hoc launch (the phone's new-session button,
//     `dispatch --prompt` from a plain shell) is converted to ModeBare: an interactive session
//     with the prompt as its first user message, no execInstructions and the
//     worker permission mode (override, then agent config, then `auto`). The
//     caller's worktree choice is kept.
//   - An unattended launch (ad-hoc with ScheduledBy set, i.e. cron; ad-hoc
//     dispatched by another agent session; and ModeDispatch) keeps its mode and autonomous instructions; only the
//     preamble role and the finishing steps change.
//
// human reports whether a person issued the launch: the phone (Detached) or a
// shell with no tracked Claude session behind it. An ad-hoc dispatch from
// another agent session is unattended: it must stay autonomous so the done
// state, the dispatcher's done notice and `dispatch --wait` keep working.
func applyAgentRole(req Request, spec modeSpec, role string, human bool) (Request, modeSpec) {
	if role == agent.RoleReporter {
		return req, applyReporterRole(req, spec)
	}
	if role != agent.RoleOrchestrator {
		return req, spec
	}
	if req.Mode == ModeAdhoc && req.ScheduledBy == "" && human {
		if req.UseWorktree == nil {
			// Keep the ad-hoc default rather than inheriting bare's.
			useWorktree := spec.DefaultWorktree
			req.UseWorktree = &useWorktree
		}
		logging.Log("dispatch: %s is an orchestrator; launching the ad-hoc prompt as an interactive session", req.AgentName)
		req.Mode = ModeBare
		bare := modes[ModeBare]
		bare.WorkerPermissionMode = true
		return req, bare
	}
	spec.Role = RoleOrchestrator
	spec.IncludeFinishingSteps = false
	if req.Mode == ModeDispatch {
		spec.Directive = orchestratorTicketDirective
	}
	return req, spec
}

// orchestratorTicketDirective replaces ModeDispatch's "open a PR when done"
// directive for an orchestrator, which delegates rather than implements.
func orchestratorTicketDirective(req Request) string {
	return fmt.Sprintf("Handle %s.", issueRef(req))
}

// applyReporterRole adapts a worker-role launch for a `role: reporter`
// agent, one that never changes code: its finishing steps become a report
// step (reporterFinishingSteps) and its orchestrator section drops the
// "stay responsive until the PR merges" paragraph. Everything else stays as
// for a worker. An orchestrator-role mode (bare) is never downgraded, and
// --review-pr, which has its own reviewer prompt, is left alone.
func applyReporterRole(req Request, spec modeSpec) modeSpec {
	if req.Mode == ModeReviewPR || spec.Role == RoleOrchestrator {
		return spec
	}
	spec.Role = RoleReporter
	if req.Mode == ModeDispatch {
		spec.Directive = reporterTicketDirective
	}
	return spec
}

// reporterTicketDirective replaces ModeDispatch's "open a PR when done"
// directive for a reporter, which normally opens no PR.
func reporterTicketDirective(req Request) string {
	return fmt.Sprintf("Work on %s and report the result.", issueRef(req))
}
