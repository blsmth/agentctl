package dispatch

// Role selects which preamble variant a dispatched session receives. Every
// dispatched agent gets sharedRuntimeSections (context store, messaging, git
// conventions); only orchestrator-role sessions also get the delegation
// surface (dispatch/tickets/health), since a worker chewing on a single
// ticket has no business kicking off further dispatches.
type Role string

const (
	// RoleWorker is a session doing the work itself: implementing a ticket,
	// running an ad-hoc task, triaging, or reviewing. It gets the shared
	// runtime tools but not the delegation surface.
	RoleWorker Role = "worker"
	// RoleOrchestrator is a session whose primary job is coordinating other
	// agents rather than writing code directly -- the human-facing `bare`
	// launch (how the lead session starts) is the canonical example. It
	// gets everything RoleWorker gets, plus dispatch/tickets/health.
	RoleOrchestrator Role = "orchestrator"
)

// sharedRuntimeSections is the part of the runtime environment every
// dispatched agent needs regardless of role: the context store, cross-agent
// messaging, and git conventions. Kept as its own constant so both preamble
// variants below stay byte-identical on this part rather than risking drift
// between two independently-maintained copies.
const sharedRuntimeSections = "### Context store\n" +
	"- `agentctl context get <key>` — read a context entry\n" +
	"- `agentctl context set <key> <value>` — write/update a context entry\n" +
	"- `agentctl context list` — list all context entries\n" +
	"\n" +
	"### Messaging\n" +
	"- `agentctl send <addr> \"message\" [--kind done|blocked|fyi|question] [--subject \"...\"] [--wait-reply] [--timeout <dur>]` — send a message to another agent's mailbox (default kind: fyi). `<addr>` is a window ID (`@41`) or a running agent name. `--wait-reply` blocks for a reply instead of guessing with a sleep\n" +
	"- `agentctl inbox [--all] [--json] [--wait [--from <addr>] [--timeout <dur>]]` — list messages sent to your window's mailbox (pending by default, `--all` includes already-read). `--wait` blocks until matching mail arrives instead of polling\n" +
	"- `agentctl wake <addr>` — wake an idle agent and deliver its pending mailbox\n" +
	"\n" +
	"### Git conventions\n" +
	"- Create new commits rather than amending existing ones\n" +
	"- Use imperative mood in commit messages"

// orchestratorDelegationSection documents dispatch as the primary delegation
// path, plus the two other coordination commands (tickets, health).
// --prompt-key exists specifically because the dispatch-preview approval
// popup tokenizes the raw pre-shell command string before a human ever sees
// it, so `--prompt "$(cat file)"` renders in that popup as the literal,
// unexpanded text -- a human approving that has seen nothing meaningful.
// `--prompt-key` reads the same content from the context store, which the
// popup CAN resolve and display for real.
const orchestratorDelegationSection = "### Delegation\n" +
	"- `agentctl dispatch --issue <N>` — dispatch an agent to work on a GitHub issue; this is the primary way work gets assigned to another agent\n" +
	"- `agentctl dispatch --agent <name> --prompt <text>` — dispatch an agent on an ad-hoc prompt (no ticket)\n" +
	"- `agentctl dispatch --agent <name> --prompt-key <key>` — same as `--prompt`, but reads the prompt text from `agentctl context get <key>` instead of an inline string; use this instead of `--prompt \"$(cat file)\"` because the approval popup tokenizes the raw command string and cannot resolve command substitution\n" +
	"- `agentctl tickets` — list GitHub issues\n" +
	"- `agentctl health` — run health checks"

// WorkerPreamble is the system-prompt section injected into worker-role
// sessions -- dispatch, adhoc, triage, review, discuss, and custom. It covers
// the tools every dispatched agent needs regardless of what it's building,
// and deliberately omits the delegation surface: a worker has no business
// kicking off further dispatches.
const WorkerPreamble = "## agentctl Runtime Environment\n\n" +
	"You are running inside an agentctl-managed tmux session. The following tools are available:\n\n" +
	sharedRuntimeSections

// OrchestratorPreamble is the system-prompt section injected into
// orchestrator-role sessions (mode `bare`, i.e. the human-facing lead
// session). It carries everything WorkerPreamble does, plus the delegation
// surface.
const OrchestratorPreamble = "## agentctl Runtime Environment\n\n" +
	"You are running inside an agentctl-managed tmux session. The following tools are available:\n\n" +
	sharedRuntimeSections + "\n\n" + orchestratorDelegationSection

// preambleFor returns the preamble text for a role. An unrecognised or empty
// role (e.g. a modeSpec that forgot to set one) defaults to RoleWorker --
// the more restrictive, safer choice.
func preambleFor(role Role) string {
	if role == RoleOrchestrator {
		return OrchestratorPreamble
	}
	return WorkerPreamble
}
