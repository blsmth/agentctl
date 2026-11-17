package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/dispatch"
	"github.com/blsmth/agentctl/internal/herd"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/sessionname"
	"github.com/blsmth/agentctl/internal/sessions"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/tui"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// RunMenu shows the filter-first launch menu: agent list with a detail strip,
// an options sheet and recent sessions for the highlighted agent.
// writeSettings is injected from package main to avoid a circular import.
// createMenu is called when the user selects "create"; it receives the agent
// name to pre-fill (typically "").
func RunMenu(writeSettings WriteAgentSettingsFn, createMenu func(name string)) {
	done := logging.Timing("RunMenu: agent.DiscoverAll")
	cfg := config.Load()
	agents, err := agent.DiscoverFromCwd(cfg)
	done()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	done = logging.Timing("RunMenu: running agents")
	// runningByID maps windowID → instance name, built from tmux's window
	// list joined with windowstate; falls back to a plain tmux query.
	runningByID := make(map[string]string)
	liveSessions := make(map[string]bool)
	if built, err := herd.LocalAgents(); err == nil {
		for _, as := range built {
			if as.WindowID != "" {
				runningByID[as.WindowID] = as.Name
			}
			if as.SessionID != "" {
				liveSessions[as.SessionID] = true
			}
		}
	}
	if len(runningByID) == 0 {
		runningByID = tmux.RunningAgents()
	}
	done()

	mcfg := tui.MenuConfig{
		Agents:   buildMenuAgents(cfg, agents, runningByID, windowActivity()),
		Backends: cfg.Backends,
		FetchOllama: func(ctx context.Context, name string) ([]string, error) {
			return dispatch.ListOllamaModels(ctx, cfg.Backends[name])
		},
		LoadSessions: newSessionLoader(liveSessions),
	}
	p := tea.NewProgram(tui.NewMenuModel(mcfg))
	finalModel, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	result := finalModel.(tui.MenuModel).Result()
	if result.Cancelled {
		return
	}

	switch result.Action {
	case "switch":
		// Switch to running agent window by ID when available, fall back to name.
		switchID := ""
		for id, name := range runningByID {
			if name == result.Instance {
				switchID = id
				break
			}
		}
		if switchID != "" {
			if err := tmux.SwitchToWindow(switchID); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		} else {
			if err := tmux.SwitchToAgent(result.Instance); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}
	case "launch":
		// Clear the terminal so the launch output replaces this TUI instead
		// of rendering below it (we're inside the same display-popup).
		fmt.Print("\033[H\033[2J")
		RunLaunchAgentOpts(result.Agent, result.Dir, LaunchOptions{
			Backend:    result.Backend,
			Model:      result.Model,
			Effort:     result.Effort,
			NoWorktree: result.NoWorktree,
		})
	case "resume":
		fmt.Print("\033[H\033[2J")
		LaunchWithResume(result.Agent, result.SessionID, result.Dir, writeSettings)
	case "create":
		fmt.Print("\033[H\033[2J")
		createMenu("")
	}
}

// windowActivity maps window ID to the unix time of its latest recorded
// activity (last tool call or state write), from windowstate.
func windowActivity() map[string]int64 {
	out := map[string]int64{}
	for id, ws := range windowstate.AllFrom(paths.RuntimeDir()) {
		t := ws.UpdatedAt.Unix()
		if ws.LastToolTime > t {
			t = ws.LastToolTime
		}
		out[id] = t
	}
	return out
}

// buildMenuAgents turns discovered agents (plus Claude Code's built-ins) into
// menu entries, prefilled with each agent's effective config. Instances are
// ordered most recently active first (activity is keyed by window ID).
func buildMenuAgents(cfg *config.Config, agents []agent.Agent, runningByID map[string]string, activity map[string]int64) []tui.MenuAgent {
	ids := make([]string, 0, len(runningByID))
	for id := range runningByID {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if ai, aj := activity[ids[i]], activity[ids[j]]; ai != aj {
			return ai > aj
		}
		return ids[i] < ids[j]
	})
	cwd, _ := os.Getwd()
	if cwd == "" {
		cwd, _ = os.UserHomeDir()
	}
	entry := func(name, desc, dir string, section tui.MenuSection) tui.MenuAgent {
		var instances []string
		for _, id := range ids {
			inst := runningByID[id]
			if tmux.BaseAgentName(inst) == name {
				instances = append(instances, inst)
			}
		}
		if name == cfg.DefaultAgent {
			section = tui.SectionPinned
		}
		ac := cfg.ForAgent(name)
		if dir == "" {
			dir = cwd
		}
		return tui.MenuAgent{
			Name:        name,
			Description: desc,
			Instances:   instances,
			Section:     section,
			Backend:     ac.Backend,
			Model:       ac.Model,
			Effort:      ac.EffortLevel,
			Dir:         dir,
		}
	}
	var out []tui.MenuAgent
	seen := map[string]bool{}
	for _, a := range agents {
		section := menuSection(a, cwd)
		seen[a.Name] = true
		out = append(out, entry(a.Name, a.Description, a.Dir, section))
	}
	for _, name := range claudeBuiltinAgents {
		if !seen[name] {
			out = append(out, entry(name, "", "", tui.SectionBuiltin))
		}
	}
	return out
}

// menuSection picks the list section of a discovered agent. ScopeLocal covers
// both agents defined in the current project's .claude/agents and registry
// symlinks into other repos, so the definition's resolved path decides: only
// a file under cwd's .claude/agents is "Project"; other ScopeLocal agents are
// "Other projects" (their fixed dir is the useful part there).
func menuSection(a agent.Agent, cwd string) tui.MenuSection {
	if a.Scope != agent.ScopeLocal {
		return tui.SectionUser
	}
	if cwd != "" {
		dir := filepath.Join(cwd, ".claude", "agents")
		if rel, err := filepath.Rel(dir, a.FilePath); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return tui.SectionProject
		}
	}
	return tui.SectionOther
}

// newSessionLoader returns the menu's lazy session loader. The log scan runs
// once, on first use; live holds session IDs open in a current window.
func newSessionLoader(live map[string]bool) func(agent string) []tui.MenuSession {
	var (
		once  sync.Once
		byAgt map[string][]sessions.Session
	)
	return func(name string) []tui.MenuSession {
		once.Do(func() { byAgt, _ = sessions.FindSessionsByAgent() })
		return menuSessions(byAgt[name], live, sessionLabel)
	}
}

// menuSessions converts sessions to menu entries, flagging the live ones. The
// preview is the first real human text, falling back to label (the session
// name). Only the entries NormalizeSessions will keep pay for a preview.
func menuSessions(all []sessions.Session, live map[string]bool, label func(sessions.Session) string) []tui.MenuSession {
	var out []tui.MenuSession
	for _, s := range all {
		when := s.EndTime
		if when.IsZero() {
			when = s.StartTime
		}
		out = append(out, tui.MenuSession{ID: s.ID, When: when, Project: s.Project, Live: live[s.ID]})
	}
	keep := tui.NormalizeSessions(out, time.Now(), 7*24*time.Hour, 5)
	byID := make(map[string]sessions.Session, len(all))
	for _, s := range all {
		byID[s.ID] = s
	}
	for i := range keep {
		s := byID[keep[i].ID]
		keep[i].Preview = GetSessionPreview(s)
		if keep[i].Preview == "" {
			keep[i].Preview = label(s)
		}
	}
	return keep
}

// sessionLabel is the recorded name of a session, "" when there is none.
func sessionLabel(s sessions.Session) string { return sessionname.Lookup(s.ID) }

// LaunchOptions are the per-launch overrides of a fresh agent launch. The zero
// value defers everything to the agent config and the mode defaults.
type LaunchOptions struct {
	// Backend is the --backend override: a name selects that backend, "none"
	// forces Anthropic, and empty defers to the agent config.
	Backend string
	// Model and Effort override the agent's configured model / effort_level;
	// dispatch.Inherit clears the configured value.
	Model  string
	Effort string
	// NoWorktree is the CLI's --no-worktree escape hatch: when true, the agent
	// launches directly in the checkout instead of an isolated worktree.
	NoWorktree bool
	// Background creates the window without switching the client to it.
	Background bool
}

// RunLaunch launches a fresh interactive agent session for the named agent.
// If name is empty it falls back to the configured default_agent.
func RunLaunch(name string, opts LaunchOptions) {
	if name == "" {
		cfg := config.Load()
		name = usableDefaultAgent(cfg, os.Stderr)
		if name == "" {
			fmt.Fprintln(os.Stderr, "No agent specified and no usable default_agent configured")
			os.Exit(1)
		}
	}
	// Resolve base agent name (strip ~N if someone passes an instance name).
	baseName := tmux.BaseAgentName(name)
	if !agent.ValidLaunchName(baseName) {
		fmt.Fprintf(os.Stderr, "Error: invalid agent name %q\n", baseName)
		os.Exit(1)
	}
	workDir := resolveAgentDir(baseName)
	RunLaunchAgentOpts(baseName, workDir, opts)
}

// RunLaunchAgentOpts launches a fresh interactive agent session in workDir.
// The agent receives the agentctl preamble but no ticket, no autonomous
// execution instructions, and no initial user message — it starts
// interactively and waits for the user. When opts.NoWorktree is false,
// UseWorktree is left nil so the launch inherits the mode default.
func RunLaunchAgentOpts(name string, workDir string, opts LaunchOptions) {
	req := dispatch.Request{
		Background: opts.Background,
		AgentName:  name,
		Mode:       dispatch.ModeBare,
		WorkDir:    workDir,
		Backend:    opts.Backend,
		Model:      opts.Model,
		Effort:     opts.Effort,
	}
	if opts.NoWorktree {
		useWorktree := false
		req.UseWorktree = &useWorktree
	}
	res, err := dispatch.Launch(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	// Machine-readable: the hub parses the last stdout line to focus the new
	// window. Only emitted for background launches; interactive output is
	// unchanged.
	if opts.Background && res.WindowID != "" {
		fmt.Println(res.WindowID)
	}
}

// discoverUserAgents lists the agents in the user-level registry
// (~/.claude/agents) only, without any project-local .claude/agents. It is a
// package-level var so tests can stub discovery.
var discoverUserAgents = func() ([]agent.Agent, error) {
	return agent.DiscoverAll(config.Load(), nil)
}

// approvalModes are the permission_mode values that keep claude asking before
// it acts. Every other value (auto, acceptEdits, dontAsk, bypassPermissions,
// or anything unrecognized) approves at least some actions on its own.
var approvalModes = []string{"", "default", "manual", "plan"}

// usableDefaultAgent returns the agent prefix+A should launch, or "" when
// there is none it can safely launch. Every rejection is reported on w and
// in the log rather than launched: default_agent drives the quick-launch, so
// it must never be handed to a launch unchecked.
//
// A default_agent that came from the project-local config (it differs from
// cfg.GlobalDefaultAgent) is untrusted, since a cloned repo commits it. It is
// honored only when projectDefaultRejection finds nothing wrong; otherwise
// the global default_agent is used instead.
func usableDefaultAgent(cfg *config.Config, w io.Writer) string {
	name := cfg.DefaultAgent
	if name != "" && name != cfg.GlobalDefaultAgent {
		why := projectDefaultRejection(cfg, name)
		if why == "" {
			return name
		}
		fallback := "no global default_agent is set"
		if cfg.GlobalDefaultAgent != "" {
			fallback = fmt.Sprintf("using global default_agent %q", cfg.GlobalDefaultAgent)
		}
		warnDefaultAgent(w, fmt.Sprintf("ignoring project default_agent %q: %s; %s", name, why, fallback))
		name = cfg.GlobalDefaultAgent
	}
	if name == "" {
		return ""
	}
	if why := defaultAgentRejection(name); why != "" {
		warnDefaultAgent(w, fmt.Sprintf("ignoring default_agent %q: %s", name, why))
		return ""
	}
	return name
}

// defaultAgentRejection returns why name cannot be launched as the default
// agent, or "" when it is a valid agent name that discovery actually finds,
// a Claude Code built-in, or a valid plugin:agent reference.
func defaultAgentRejection(name string) string {
	if !agent.ValidLaunchName(name) {
		return "invalid agent name"
	}
	// Built-ins and plugin agents have no .md for discovery to find; they
	// pass through by name exactly as `dispatch --agent` accepts them.
	if slices.Contains(claudeBuiltinAgents, name) || agent.ValidPluginName(name) {
		return ""
	}
	agents, err := discoverDispatchAgents("")
	if err != nil {
		return fmt.Sprintf("discovering agents: %v", err)
	}
	for _, a := range agents {
		if a.Name == name {
			return ""
		}
	}
	return "no such agent"
}

// projectDefaultRejection returns why a project-local default_agent must not
// be honored, or "" when it is safe. Safe means the name is valid, names an
// agent the repo does not control (a built-in, a plugin agent, or one in the
// user-level registry that no repo-local definition shadows), and that agent
// does not auto-approve unless it is also the global default.
func projectDefaultRejection(cfg *config.Config, name string) string {
	if !agent.ValidLaunchName(name) {
		return "invalid agent name"
	}
	if !slices.Contains(claudeBuiltinAgents, name) && !agent.ValidPluginName(name) {
		if why := repoControlledRejection(name); why != "" {
			return why
		}
	}
	if name == cfg.GlobalDefaultAgent {
		return ""
	}
	ac := cfg.ForAgent(name)
	if ac.AutoAccept {
		return "agent has auto_accept enabled"
	}
	if !slices.Contains(approvalModes, ac.PermissionMode) {
		return fmt.Sprintf("agent has permission_mode %q, which skips approvals", ac.PermissionMode)
	}
	return ""
}

// repoControlledRejection returns "" when name resolves to the same file in
// the user-level registry and in cwd discovery, and why not otherwise. A name
// that is missing from the registry, or that a repo-local .claude/agents file
// shadows, would launch a definition the repo wrote.
func repoControlledRejection(name string) string {
	user, err := discoverUserAgents()
	if err != nil {
		return fmt.Sprintf("discovering user agents: %v", err)
	}
	userPath, ok := agentPath(user, name)
	if !ok {
		return "not defined in the user-level agents dir"
	}
	all, err := discoverDispatchAgents("")
	if err != nil {
		return fmt.Sprintf("discovering agents: %v", err)
	}
	if path, ok := agentPath(all, name); !ok || path != userPath {
		return "shadowed by a repo-local agent definition"
	}
	return ""
}

// agentPath returns the FilePath of the first agent in agents named name.
func agentPath(agents []agent.Agent, name string) (string, bool) {
	for _, a := range agents {
		if a.Name == name {
			return a.FilePath, true
		}
	}
	return "", false
}

// warnDefaultAgent reports msg on w and in the log.
func warnDefaultAgent(w io.Writer, msg string) {
	msg = "agentctl: warning: " + msg
	fmt.Fprintln(w, msg)
	logging.Warn("%s", msg)
}
