package dispatch

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/dispatchplan"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/paths"
)

// AdhocRequest is an ad-hoc prompt dispatch: `agentctl dispatch --prompt`
// (or --prompt-key) and agentctl serve's POST /v1/dispatch.
type AdhocRequest struct {
	Prompt string
	// ExtraInstructions is appended to the agent's system prompt (see
	// Request.ExtraInstructions); empty leaves the prompt unchanged.
	ExtraInstructions string
	// PRReview overrides config pr_review for this launch ("" = use config).
	PRReview string
	// PromptKey is the context store key the prompt was resolved from, or ""
	// (display-only; see Request.PromptKey).
	PromptKey string
	// Template is the prompt template the prompt came from, or ""
	// (display-only; see Request.Template).
	Template  string
	AgentName string
	WorkDir   string // required; the CLI resolves its default before calling
	Model     string
	Backend   string
	// Account overrides the resolved Claude account (see Request.Account).
	Account string
	// Effort overrides the agent's effort_level (see Request.Effort).
	Effort string
	// Harness overrides the agent's harness (see Request.Harness).
	Harness     string
	UseWorktree bool
	// Detached launches with no parent window (see Request.Detached).
	Detached bool
	// RepoSlug is the GitHub "owner/repo" for WorkDir when RepoSlugKnown is
	// set: the caller resolved it already (possibly to "", no GitHub repo),
	// typically before taking a lock that a slow `gh repo view` must not be
	// held under. LaunchAdhoc then does not look it up again.
	RepoSlug      string
	RepoSlugKnown bool
	// Branch and ScheduledBy pass through to Request (see there).
	Branch      string
	ScheduledBy string
}

// Seams for tests.
var (
	adhocRepoSlug = RepoSlug
	adhocLaunch   = Launch
)

// LaunchAdhoc launches an agent in a new tmux window on an inline prompt
// rather than a GitHub issue, in the background (focus is never switched).
func LaunchAdhoc(req AdhocRequest) (Result, error) {
	if req.WorkDir == "" {
		return Result{}, errors.New("ad-hoc dispatch: WorkDir is required")
	}

	// Determine the target repo from the working directory so that env vars
	// and dispatch-log entries reference the correct repository. For ad-hoc
	// dispatch the slug is informational only, and dispatching into a
	// non-GitHub directory is legitimate, so a failure is not fatal: warn and
	// continue with an empty slug (env var and --repo usage are omitted).
	repoSlug := req.RepoSlug
	if !req.RepoSlugKnown {
		repoSlug = resolveAdhocRepoSlug(req.WorkDir)
	}

	useWorktree := req.UseWorktree
	return adhocLaunch(Request{
		AgentName:         req.AgentName,
		Mode:              ModeAdhoc,
		CustomPrompt:      req.Prompt,
		ExtraInstructions: req.ExtraInstructions,
		PRReviewOverride:  req.PRReview,
		PromptKey:         req.PromptKey,
		Template:          req.Template,
		WorkDir:           req.WorkDir,
		UseWorktree:       &useWorktree,
		RepoSlug:          repoSlug,
		Model:             req.Model,
		Backend:           req.Backend,
		Account:           req.Account,
		Effort:            req.Effort,
		Harness:           req.Harness,
		Background:        true,
		Detached:          req.Detached,
		Branch:            req.Branch,
		ScheduledBy:       req.ScheduledBy,
	})
}

// resolveAdhocRepoSlug looks up WorkDir's repo slug, or "" when it has none.
func resolveAdhocRepoSlug(workDir string) string {
	repoSlug, err := adhocRepoSlug(workDir)
	if err != nil {
		logging.Warn("dispatch: no GitHub repo for %s, continuing without repo slug: %v", workDir, err)
		return ""
	}
	return repoSlug
}

// DiscoverAgents lists the agents visible to a dispatch targeting workDir:
// the global registry, the cwd and agent-configured directories
// (agent.DiscoverFromCwd), plus workDir's own project-local agents.
func DiscoverAgents(workDir string) ([]agent.Agent, error) {
	cfg := config.Load()
	agents, err := agent.DiscoverFromCwd(cfg)
	if err != nil {
		return nil, err
	}
	if workDir == "" {
		return agents, nil
	}
	local, err := agent.DiscoverAll(cfg, []string{paths.ExpandHome(workDir)})
	if err != nil {
		return agents, nil //nolint:nilerr // --dir discovery is additive; the base list still stands
	}
	return append(agents, local...), nil
}

// discoverAgents is the seam ValidateAgentName discovers through.
var discoverAgents = DiscoverAgents

// claudeBuiltinAgents are the agents Claude Code ships with. They appear in
// `claude --agent`'s "Available agents" list but have no .md file for
// agentctl's discovery to find.
var claudeBuiltinAgents = dispatchplan.BuiltinAgents

// ClaudeBuiltinAgents returns a copy of the Claude Code built-in agent names.
func ClaudeBuiltinAgents() []string { return slices.Clone(claudeBuiltinAgents) }

// isPluginAgent reports whether name has Claude Code's plugin-scoped
// `plugin:name` form. Plugin agents live in Claude Code's plugin cache, which
// agentctl does not index, so they pass through unvalidated (the name is
// still shell-quoted at launch).
func isPluginAgent(name string) bool {
	plugin, agentName, ok := strings.Cut(name, ":")
	return ok && plugin != "" && agentName != ""
}

// ErrAgentDiscovery marks a ValidateAgentName failure to list agents at all
// (as opposed to the name being unknown).
var ErrAgentDiscovery = errors.New("discovering agents")

// ValidateAgentName returns an error naming the available agents unless name
// is a discovered agent, a Claude Code built-in, or a plugin agent.
func ValidateAgentName(name, workDir string) error {
	if slices.Contains(claudeBuiltinAgents, name) || isPluginAgent(name) {
		return nil
	}
	agents, err := discoverAgents(workDir)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrAgentDiscovery, err)
	}
	names := make([]string, 0, len(agents)+len(claudeBuiltinAgents))
	for _, a := range agents {
		if a.Name == name {
			return nil
		}
		names = append(names, a.Name)
	}
	names = append(names, claudeBuiltinAgents...)
	slices.Sort(names)
	names = slices.Compact(names)
	return fmt.Errorf("unknown agent %q; available agents: %s (or plugin:name)", name, strings.Join(names, ", "))
}

// AgentNames lists the agent names a schedule or dispatch from workDir can
// name (discovered agents plus the Claude Code built-ins), sorted. Plugin
// agents are not listed; ValidateAgentName still accepts them.
func AgentNames(workDir string) []string {
	agents, _ := discoverAgents(workDir)
	names := slices.Clone(claudeBuiltinAgents)
	for _, a := range agents {
		names = append(names, a.Name)
	}
	slices.Sort(names)
	return slices.Compact(names)
}
