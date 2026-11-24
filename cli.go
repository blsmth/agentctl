package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/blsmth/agentctl/internal/audit"
	"github.com/blsmth/agentctl/internal/cliout"
	"github.com/blsmth/agentctl/internal/cmd"
	"github.com/blsmth/agentctl/internal/dispatch"
	"github.com/blsmth/agentctl/internal/hooks"
	"github.com/blsmth/agentctl/internal/improve"
	"github.com/blsmth/agentctl/internal/improverun"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/metricsserve"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/race"
	"github.com/blsmth/agentctl/internal/safefs"
	"github.com/blsmth/agentctl/internal/sessions"
	"github.com/blsmth/agentctl/internal/statusline"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/winname"
)

// CLI is the root Kong command struct. Every subcommand is a field here.
// Fields tagged `cmd:""` become subcommands; `hidden:""` hides them from help.
// The `Run()` method on each leaf struct is called by kong after parsing.
type CLI struct {
	// Info
	Version VersionCmd `cmd:"" help:"Print version info"`

	// Session management
	Quit         QuitCmd         `cmd:"" help:"Quit the agentctl session"`
	Restart      RestartCmd      `cmd:"" help:"Kill the agentctl session; relaunch with agentctl, or prefix+R inside tmux"`
	Start        StartCmd        `cmd:"" help:"Print session startup banner" hidden:""`
	QuitMenu     QuitMenuCmd     `cmd:"" name:"quit-menu" hidden:""`
	HubQuitMenu  HubQuitMenuCmd  `cmd:"" name:"hub-quit-menu" hidden:""`
	RestartMenu  RestartMenuCmd  `cmd:"" name:"restart-menu" hidden:""`
	SaveState    SaveStateCmd    `cmd:"" name:"save-state" hidden:""`
	Restore      RestoreCmd      `cmd:"" hidden:""`
	RelaunchPrep RelaunchPrepCmd `cmd:"" name:"relaunch-prep" hidden:"" help:"Stop the project monitor and kill its session (used by just relaunch)"`

	// Agent windows
	Launch      LaunchCmd      `cmd:"" help:"Launch an agent in a new window"`
	Kill        KillCmd        `cmd:"" help:"Kill an agent window"`
	Logs        LogsCmd        `cmd:"" help:"View agent session logs"`
	Improve     ImproveCmd     `cmd:"" help:"Launch self-improvement session"`
	Cleanup     CleanupCmd     `cmd:"" help:"Reclaim worktrees and windows from finished agents"`
	Sweep       SweepCmd       `cmd:"" help:"Close finished windows in the current tmux session and list what needs attention"`
	KillAll     KillAllCmd     `cmd:"" name:"kill-all" hidden:""`
	KillAllMenu KillAllMenuCmd `cmd:"" name:"kill-all-menu" hidden:""`
	KillMenu    KillMenuCmd    `cmd:"" name:"kill-menu" hidden:""`
	LogsMenu    LogsMenuCmd    `cmd:"" name:"logs-menu" hidden:""`
	ImproveMenu ImproveMenuCmd `cmd:"" name:"improve-menu" hidden:""`

	// Agent management
	Agents           AgentsCmd           `cmd:"" aliases:"agent-manager" help:"List agents (--tui browser, --json) and their change history"`
	Skills           SkillsCmd           `cmd:"" help:"List slash-command skills"`
	Memory           MemoryCmd           `cmd:"" help:"List memory files (CLAUDE.md)"`
	Create           CreateCmd           `cmd:"" aliases:"create-menu" help:"Create a new agent definition"`
	OpenAgentsWindow OpenAgentsWindowCmd `cmd:"" name:"open-agents-window" hidden:""`

	// Backends
	Backend      BackendCmd      `cmd:"" help:"Manage backend credentials (login, logout, status)"`
	Account      AccountCmd      `cmd:"" help:"Manage Claude accounts (one CLAUDE_CONFIG_DIR per login)"`
	BackendToken BackendTokenCmd `cmd:"" name:"backend-token" hidden:"" help:"Print a backend's API key (Claude Code apiKeyHelper)"`

	// Dispatch completion (spawned detached by the Stop hook / signal done)
	DispatchComplete DispatchCompleteCmd `cmd:"" name:"dispatch-complete" hidden:"" help:"Run dispatch completion for a window (PR lookup, labels, hooks)"`
	ImproveFinish    ImproveFinishCmd    `cmd:"" name:"improve-finish" hidden:"" help:"Close out an improve run (spawned detached by hooks)"`
	LaunchWatch      LaunchWatchCmd      `cmd:"" name:"launch-watch" hidden:"" help:"Watch a freshly launched window for the folder-trust prompt"`
	RunUserHooks     RunUserHooksCmd     `cmd:"" name:"run-user-hooks" hidden:"" help:"Run user lifecycle hooks for an event (spawned detached by hooks)"`

	// Dispatch
	Dispatch DispatchCmd `cmd:"" help:"Dispatch an agent to work on a ticket"`

	// Fleet hub — cross-session aggregator view
	Open            OpenCmd            `cmd:"" help:"Start or switch to the agentctl session for a project directory"`
	Hub             HubCmd             `cmd:"" help:"Open the fleet hub (cross-session dashboard)"`
	HubView         HubViewCmd         `cmd:"" name:"hub-view" hidden:""`
	HubNewSession   HubNewSessionCmd   `cmd:"" name:"hub-new-session" hidden:""`
	HubResume       HubResumeCmd       `cmd:"" name:"hub-resume" hidden:""`
	Resume          ResumeCmd          `cmd:"" help:"Resume a closed session by its handle (see the ID column of 'status list --closed')"`
	HubStatus       HubStatusCmd       `cmd:"" name:"hub-status" hidden:""`
	JumpNextBlocked JumpNextBlockedCmd `cmd:"" name:"jump-next-blocked" hidden:""`

	// Tickets
	Ticket            TicketCmd            `cmd:"" aliases:"tickets" help:"List, view, or create GitHub issues"`
	TicketMenu        TicketMenuCmd        `cmd:"" name:"ticket-menu" hidden:""`
	TicketLaunch      TicketLaunchCmd      `cmd:"" name:"ticket-launch" hidden:""`
	OpenTicketsWindow OpenTicketsWindowCmd `cmd:"" name:"open-tickets-window" hidden:""`

	// Sessions
	Sessions           SessionsCmd           `cmd:"" help:"List sessions (--tui for interactive browser, --json for machine-readable)"`
	SessionsMenu       SessionsMenuCmd       `cmd:"" name:"sessions-menu" hidden:""`
	OpenSessionsWindow OpenSessionsWindowCmd `cmd:"" name:"open-sessions-window" hidden:""`
	OpenContextWindow  OpenContextWindowCmd  `cmd:"" name:"open-context-window" hidden:""`

	// Audit trail (hook-sourced -- every tool call, not just Bash)
	Audit AuditCmd `cmd:"" help:"Query the hook-sourced audit trail (default), or grade past sessions with 'audit report'"`

	// File preview popup (used by hook-permission-request for Write)
	FilePreview FilePreviewCmd `cmd:"" name:"file-preview" hidden:""`

	// Dispatch preview popup (used by hook-permission-request for Bash dispatch)
	DispatchPreview DispatchPreviewCmd `cmd:"" name:"dispatch-preview" hidden:""`

	// TUI / internal
	Menu            MenuCmd            `cmd:"" hidden:""`
	Monitor         MonitorCmd         `cmd:"" hidden:""`
	Hud             HudCmd             `cmd:"" hidden:""`
	HudToggle       HudToggleCmd       `cmd:"" name:"hud-toggle" hidden:""`
	ChromeBar       ChromeBarCmd       `cmd:"" name:"chrome-bar" hidden:""`
	ChromeBarPane   ChromeBarPaneCmd   `cmd:"" name:"chrome-bar-pane" hidden:""`
	SetActiveWindow SetActiveWindowCmd `cmd:"" name:"set-active-window" hidden:""`
	Reconcile       ReconcileCmd       `cmd:"" hidden:"" help:"Fire Gone handling for vanished windows (tmux hook, kill, janitor)"`
	EnforceChrome   EnforceChromeCmd   `cmd:"" name:"enforce-chrome" hidden:"" help:"Re-enforce chrome bar pane sizes (tmux client hooks)"`
	ApplyConfig     ApplyConfigCmd     `cmd:"" name:"apply-config" help:"Re-apply tmux hooks and keybindings to the running session (no restart)"`
	Toggle          ToggleCmd          `cmd:"" hidden:""`
	Settings        SettingsCmd        `cmd:"" hidden:""`
	SettingsMenu    SettingsMenuCmd    `cmd:"" name:"settings-menu" hidden:""`
	Permissions     PermissionsCmd     `cmd:"" aliases:"permissions-menu" hidden:""`
	ContextPushMenu ContextPushMenuCmd `cmd:"" name:"context-push-menu" hidden:""`
	Signal          SignalCmd          `cmd:"" hidden:""`
	ThemePicker     ThemePickerCmd     `cmd:"" name:"theme-picker" hidden:""`

	// Tools
	Context    ContextCmd    `cmd:"" help:"Manage cross-agent context store"`
	Usage      UsageCmd      `cmd:"" help:"Show API usage stats"`
	Cost       CostCmd       `cmd:"" help:"Cost report (metered / sub / free), background LLM calls, and model prices"`
	Health     HealthCmd     `cmd:"" help:"Run health checks"`
	Status     StatusCmd     `cmd:"" help:"Query agent status across every live session"`
	Dashboards DashboardsCmd `cmd:"" help:"View dashboards"`
	Ctl        CtlCmd        `cmd:"" help:"Send operator control command"`
	Models     ModelsCmd     `cmd:"" help:"Inspect the model registry (context window, pricing, resolution)"`
	Race       RaceCmd       `cmd:"" help:"Replay a session as a race against a human typing and reading the same work"`
	Artifact   ArtifactCmd   `cmd:"" help:"Publish reports, mockups and charts as artifacts and get a URL to open them (add, update, list, show, url, open, pin, rm, prune)"`

	// Setup
	Init             InitCmd             `cmd:"" help:"Initialize agentctl in current project"`
	New              NewCmd              `cmd:"" help:"Create a new agent in current project"`
	Sync             SyncCmd             `cmd:"" help:"Sync project agents to global registry"`
	HooksInstall     HooksInstallCmd     `cmd:"" name:"hooks-install" help:"Install Claude Code hooks"`
	InstallTemplates InstallTemplatesCmd `cmd:"" name:"install-templates" help:"Install starter agent templates"`
	InstallPlugin    InstallPluginCmd    `cmd:"" name:"install-plugin" help:"Print plugin marketplace install instructions" hidden:""`

	// Claude Code statusLine data source. Bare `agentctl statusline`
	// is normally intercepted by main.go's fast path before Kong ever sees
	// it — see isStatuslineIngestCmd. The group is visible so users can find
	// `agentctl statusline install`; the ingest path itself is machine-invoked.
	Statusline StatuslineCmd `cmd:"" help:"Claude Code statusLine integration (see: statusline install)"`

	// Per-machine processes
	Metrics  MetricsCmd  `cmd:"" help:"Prometheus metrics (serve: the per-machine metrics process)"`
	Serve    ServeCmd    `cmd:"" help:"Run the HTTPS server for the mobile web app (install, pair, devices, status)"`
	Cron     CronCmd     `cmd:"" help:"Scheduled agent runs (add, edit, list, runs, run, pause, resume, rm, install)"`
	Template TemplateCmd `cmd:"" help:"Prompt templates: saved, reusable dispatch prompts (list, show, add, edit, rm)"`
}

// ---------------------------------------------------------------------------
// Info
// ---------------------------------------------------------------------------

type VersionCmd struct{}

func (c *VersionCmd) Run() error {
	fmt.Println(versionString())
	return nil
}

// ---------------------------------------------------------------------------
// Session management
// ---------------------------------------------------------------------------

type QuitCmd struct{}

func (c *QuitCmd) Run() error { cmd.RunQuit(); return nil }

type RestartCmd struct{}

func (c *RestartCmd) Run() error { cmd.RunRestart(); return nil }

type StartCmd struct{}

func (c *StartCmd) Run() error { cmd.CmdStart(bannerArt); return nil }

type QuitMenuCmd struct{}

func (c *QuitMenuCmd) Run() error { cmd.RunQuitMenu(); return nil }

type HubQuitMenuCmd struct{}

func (c *HubQuitMenuCmd) Run() error { cmd.RunHubQuitMenu(); return nil }

type RestartMenuCmd struct{}

func (c *RestartMenuCmd) Run() error { cmd.RunRestartMenu(); return nil }

type SaveStateCmd struct{}

func (c *SaveStateCmd) Run() error { cmdSaveState(); return nil }

type RelaunchPrepCmd struct{}

func (c *RelaunchPrepCmd) Run() error { return cmd.RunRelaunchPrep() }

type RestoreCmd struct{}

func (c *RestoreCmd) Run() error { cmd.RunRestore(); return nil }

// ---------------------------------------------------------------------------
// Agent windows
// ---------------------------------------------------------------------------

type LaunchCmd struct {
	Name           string `arg:"" optional:"" help:"Agent name to launch (reads default_agent from config if omitted)"`
	NoWorktree     bool   `name:"no-worktree" help:"Launch directly in the main checkout instead of an isolated git worktree"`
	Backend        string `help:"Named backend from config (Ollama, Nous Portal); 'none' forces Anthropic"`
	Account        string `help:"Claude account from config accounts (default: agent setting, account_rules, default_account)"`
	Harness        string `help:"Agent program to run the agent on: claude or pi (default: agent config, then frontmatter, then claude)"`
	Model          string `help:"Model to run (overrides the agent's model; 'inherit' clears it)"`
	Effort         string `help:"Effort level: low, medium, high, xhigh, max, auto ('inherit' clears the agent's)"`
	PermissionMode string `name:"permission-mode" help:"claude --permission-mode for this launch; overrides the agent config"`
	Background     bool   `help:"Create the window without switching to it"`
}

func (c *LaunchCmd) Run() error {
	if c.Effort != "" {
		if err := dispatch.ValidateEffort(c.Effort); err != nil {
			return err
		}
	}
	if err := dispatch.ValidatePermissionMode(c.PermissionMode); err != nil {
		return err
	}
	cmd.RunLaunch(c.Name, cmd.LaunchOptions{
		Backend:        c.Backend,
		Account:        c.Account,
		Harness:        c.Harness,
		Model:          c.Model,
		Effort:         c.Effort,
		PermissionMode: c.PermissionMode,
		NoWorktree:     c.NoWorktree,
		Background:     c.Background,
	})
	return nil
}

// AccountCmd groups the multi-account commands. Each account is a Claude
// Code config dir with its own login; agentctl never handles the credentials.
type AccountCmd struct {
	Add    AccountAddCmd    `cmd:"" help:"Create and register an account dir, then log it in with claude auth login"`
	List   AccountListCmd   `cmd:"" help:"List accounts with their login identity and 5h/7d usage"`
	Doctor AccountDoctorCmd `cmd:"" help:"Check shared symlinks, logins and stray credential env vars"`
	Rm     AccountRmCmd     `cmd:"" help:"Unregister an account (never deletes its dir)"`
}

type AccountAddCmd struct {
	Name      string `arg:"" help:"Account name"`
	ConfigDir string `name:"config-dir" required:"" help:"Claude Code config dir for this account (e.g. ~/.claude-personal)"`
	Label     string `help:"Human-readable label"`
	NoLogin   bool   `name:"no-login" help:"Do not open the login popup inside tmux"`
}

func (c *AccountAddCmd) Run() error {
	return cmd.RunAccountAdd(cmd.AccountAddOptions{Name: c.Name, ConfigDir: c.ConfigDir, Label: c.Label, NoLogin: c.NoLogin}, os.Stdout)
}

type AccountListCmd struct{}

func (c *AccountListCmd) Run() error { return cmd.RunAccountList(os.Stdout) }

type AccountDoctorCmd struct{}

func (c *AccountDoctorCmd) Run() error { return cmd.RunAccountDoctor(os.Stdout) }

type AccountRmCmd struct {
	Name string `arg:"" help:"Account name"`
}

func (c *AccountRmCmd) Run() error { return cmd.RunAccountRm(c.Name, os.Stdout) }

// BackendCmd groups the credential commands for nous backends.
type BackendCmd struct {
	Login  BackendLoginCmd  `cmd:"" help:"Store and verify the API key for a nous backend"`
	Logout BackendLogoutCmd `cmd:"" help:"Remove the stored API key for a backend"`
	Status BackendStatusCmd `cmd:"" help:"Show backends and whether each has a credential"`
}

type BackendLoginCmd struct {
	Name     string `arg:"" help:"Backend name from config"`
	Stdin    bool   `help:"Read the key from stdin instead of prompting"`
	NoVerify bool   `name:"no-verify" help:"Skip the one-token verification request (which bills a fraction of a cent)"`
}

func (c *BackendLoginCmd) Run() error { return cmd.RunBackendLogin(c.Name, c.Stdin, c.NoVerify) }

type BackendLogoutCmd struct {
	Name string `arg:"" help:"Backend name from config"`
}

func (c *BackendLogoutCmd) Run() error { return cmd.RunBackendLogout(c.Name) }

type BackendStatusCmd struct {
	Verify bool `help:"Verify each stored key with a one-token request (bills a fraction of a cent)"`
}

func (c *BackendStatusCmd) Run() error { return cmd.RunBackendStatus(c.Verify) }

// BackendTokenCmd is routed on main's fast path before Kong ever runs; it is
// declared here so it shows in the command tree.
type BackendTokenCmd struct {
	Name string `arg:"" help:"Backend name"`
}

func (c *BackendTokenCmd) Run() error {
	return fmt.Errorf("backend-token is handled before CLI parsing; this should be unreachable")
}

type KillCmd struct {
	Name string `arg:"" help:"Agent name to kill"`
}

func (c *KillCmd) Run() error { cmd.RunKill(c.Name); return nil }

type LogsCmd struct {
	Name string `arg:"" help:"Agent name or window ID"`
}

func (c *LogsCmd) Run() error { cmd.RunLogs(c.Name); return nil }

// ImproveCmd groups `agentctl improve [<agent>]` (start a session, the
// default) and `agentctl improve runs`.
type ImproveCmd struct {
	Start ImproveStartCmd `cmd:"" default:"withargs" help:"Launch a self-improvement session (default; an agent named runs or start needs: improve start <agent>)"`
	Runs  ImproveRunsCmd  `cmd:"" help:"List past improve runs with their changes and history revisions"`
}

// ImproveRunsCmd implements `agentctl improve runs [--since]`.
type ImproveRunsCmd struct {
	Since string `default:"30d" help:"How far back to list (e.g. 24h, 7d)"`
}

func (c *ImproveRunsCmd) Run() error {
	since, err := sessions.ParseSince(c.Since)
	if err != nil {
		return err
	}
	return cmd.RunImproveRuns(os.Stdout, time.Now().Add(-since))
}

// ImproveStartCmd implements `agentctl improve [<agent>]`.
type ImproveStartCmd struct {
	Agent    string   `arg:"" optional:"" help:"Agent to improve (default: the current window's agent)"`
	Fleet    bool     `help:"Review every agent: build the evidence pack across the fleet"`
	Session  []string `help:"Limit the evidence to these session IDs (or prefixes)"`
	Since    string   `default:"7d" help:"Evidence window (e.g. 24h, 7d)"`
	Theme    string   `help:"Improvement focus area"`
	Template string   `help:"Prompt template whose body is added to the prompt"`
	Model    string   `help:"Model for the session (overrides the agent's own model)"`
	Backend  string   `help:"Backend for the session (overrides the agent's own)"`
	Account  string   `help:"Account for the session (overrides the agent's own)"`
	DryRun   bool     `name:"dry-run" help:"Print the evidence and the prompt; launch nothing"`
	Focus    bool     `help:"Switch to the new window (it opens unfocused by default)"`
}

func (c *ImproveStartCmd) Run() error {
	since, err := sessions.ParseSince(c.Since)
	if err != nil {
		return err
	}
	return cmdImprove(improve.Options{
		Agent: c.Agent, Fleet: c.Fleet, SessionIDs: c.Session, Since: since,
		Theme: c.Theme, Template: c.Template, Model: c.Model, Backend: c.Backend,
		Account: c.Account, DryRun: c.DryRun, Focus: c.Focus,
	})
}

// CleanupCmd implements `agentctl cleanup`.
type CleanupCmd struct {
	DryRun         bool `name:"dry-run" help:"Print the plan and exit without acting"`
	Yes            bool `help:"Act without an interactive confirmation prompt"`
	IncludeOpenPRs bool `name:"include-open-prs" help:"Also reclaim worktrees whose branch has an open PR (worktree only; the branch is preserved)"`
	JSON           bool `help:"Output as JSON"`
}

func (c *CleanupCmd) Run() error {
	return cmd.RunCleanup(cmd.CleanupOptions{
		DryRun:         c.DryRun,
		Yes:            c.Yes,
		IncludeOpenPRs: c.IncludeOpenPRs,
		JSON:           c.JSON,
	})
}

// SweepCmd implements `agentctl sweep`.
type SweepCmd struct {
	Session     string `help:"tmux session to sweep (required outside tmux; default: the session this command runs in)"`
	DryRun      bool   `name:"dry-run" help:"Print the plan without closing anything"`
	IncludeBare bool   `name:"include-bare" help:"Also close finished bare (interactive) windows: idle after at least one completed turn, with no pending mail, no live child sessions; worktree/branch/open-PR safety still applies"`
	JSON        bool   `help:"Output as JSON"`
}

func (c *SweepCmd) Run() error {
	return cmd.RunSweep(cmd.SweepOptions{
		Session:     c.Session,
		DryRun:      c.DryRun,
		IncludeBare: c.IncludeBare,
		JSON:        c.JSON,
	})
}

// CostCmd groups `agentctl cost` (the report, default) and `cost prices`.
type CostCmd struct {
	Report CostReportCmd `cmd:"" default:"withargs" help:"Per-session cost report (default)"`
	Prices CostPricesCmd `cmd:"" help:"Model prices ($ per 1M tokens) and where each comes from"`
}

// CostReportCmd implements `agentctl cost [session-id]`.
type CostReportCmd struct {
	Session string `arg:"" optional:"" help:"Session ID (prefix) for a per-model breakdown of one session"`
	Since   string `default:"7d" help:"How far back to look (e.g. 36h, 7d, 2w)"`
	By      string `enum:",agent,model,day,project,account" default:"" help:"Group totals by agent, model, day, project or account"`
	Metered bool   `help:"Only metered sessions (real spend, e.g. Nous)"`
	Account string `help:"Only sessions found under this account"`
	Sort    string `enum:",date,cost,turns,tokens" default:"date" help:"Order sessions (or --by groups) by date, cost, turns or tokens"`
	Top     int    `help:"List only the first N sessions after sorting (totals still cover all)"`
	JSON    bool   `help:"Output as JSON"`
}

func (c *CostReportCmd) Run() error {
	return cmd.RunCost(cmd.CostOptions{Session: c.Session, Since: c.Since, By: c.By, Metered: c.Metered, Account: c.Account, Sort: c.Sort, Top: c.Top, JSON: c.JSON})
}

// CostPricesCmd implements `agentctl cost prices [filter]`.
type CostPricesCmd struct {
	Filter  string `arg:"" optional:"" help:"Only models whose id contains this text"`
	Refresh bool   `help:"Fetch the catalog of every nous backend first and rewrite the price cache"`
	JSON    bool   `help:"Output as JSON"`
}

func (c *CostPricesCmd) Run() error {
	return cmd.RunCostPrices(cmd.PricesOptions{Filter: c.Filter, Refresh: c.Refresh, JSON: c.JSON})
}

type KillAllCmd struct{}

func (c *KillAllCmd) Run() error { cmd.RunKillAll(); return nil }

type KillAllMenuCmd struct{}

func (c *KillAllMenuCmd) Run() error { cmd.RunKillAllMenu(); return nil }

type KillMenuCmd struct {
	// Optional window-id arg (from tmux keybinding #{window_id})
	NameOrID string `arg:"" optional:"" help:"Agent name or window ID"`
}

func (c *KillMenuCmd) Run() error { cmd.RunKillMenu(c.NameOrID); return nil }

type LogsMenuCmd struct{}

func (c *LogsMenuCmd) Run() error { cmdLogsMenu(); return nil }

type ImproveMenuCmd struct {
	// Optional window-id arg (from tmux keybinding #{window_id})
	WindowID string `arg:"" optional:"" help:"Window ID"`
}

func (c *ImproveMenuCmd) Run() error { cmdImproveMenu(c.WindowID); return nil }

// ---------------------------------------------------------------------------
// Agent management
// ---------------------------------------------------------------------------

// AgentsCmd implements `agentctl agents`: the agent list (the default
// subcommand, so `agents`, `agents --tui` and `agents <name>` are unchanged)
// and the history of the agent definitions.
type AgentsCmd struct {
	List    AgentsListCmd    `cmd:"" default:"withargs" help:"List agents (--tui for interactive browser)"`
	History AgentsHistoryCmd `cmd:"" help:"Show the change history of agent definitions"`
	Diff    AgentsDiffCmd    `cmd:"" help:"Diff agent definitions against a past revision"`
	Revert  AgentsRevertCmd  `cmd:"" help:"Restore an agent definition from the history (recorded as a new snapshot)"`
}

// AgentsListCmd implements `agentctl agents`.
// Without flags it prints a text table; --tui launches the interactive browser.
type AgentsListCmd struct {
	TUI  bool   `help:"Open interactive TUI browser"`
	JSON bool   `help:"Output as JSON"`
	Name string `arg:"" optional:"" help:"Agent name for detail view"`
}

func (c *AgentsListCmd) Run() error {
	if c.TUI {
		return cliout.RunTUI(cmd.CmdAgentManager, c.table)
	}
	return c.table()
}

// table prints the non-TUI output.
func (c *AgentsListCmd) table() error {
	if c.Name != "" {
		if err := cmd.PrintAgentDetail(c.Name, c.JSON); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			cliout.Exit(1)
		}
		return nil
	}
	if err := cmd.PrintAgentsTable(c.JSON); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		cliout.Exit(1)
	}
	return nil
}

// AgentsHistoryCmd implements `agentctl agents history [name]`.
type AgentsHistoryCmd struct {
	Store string `default:"" help:"History store: agents (~/.claude: agents, shared, CLAUDE.md, settings.json), config (agentctl config.json and templates) or a project dir; default: by path"`
	Name  string `arg:"" optional:"" help:"Agent name or path under ~/.claude (default: everything tracked)"`
	Limit int    `short:"n" default:"30" help:"Maximum entries (0 = all)"`
}

func (c *AgentsHistoryCmd) Run() error {
	return agentsHistoryExit(cmd.RunAgentsHistory(os.Stdout, c.Store, c.Name, c.Limit))
}

// AgentsDiffCmd implements `agentctl agents diff [name] [rev]`.
type AgentsDiffCmd struct {
	Store string `default:"" help:"History store: agents (~/.claude: agents, shared, CLAUDE.md, settings.json), config (agentctl config.json and templates) or a project dir; default: by path"`
	Name  string `arg:"" optional:"" help:"Agent name or path under ~/.claude (default: everything tracked)"`
	Rev   string `arg:"" optional:"" help:"Revision to compare with the current files (default: the last snapshot)"`
}

func (c *AgentsDiffCmd) Run() error {
	return agentsHistoryExit(cmd.RunAgentsDiff(os.Stdout, c.Store, c.Name, c.Rev))
}

// AgentsRevertCmd implements `agentctl agents revert <name|path> [rev]`.
// From an agent session it goes through the approval popup (hooks).
type AgentsRevertCmd struct {
	Store string `default:"" help:"History store: agents (~/.claude: agents, shared, CLAUDE.md, settings.json), config (agentctl config.json and templates) or a project dir; default: by path"`
	Name  string `arg:"" help:"Agent name or path under ~/.claude"`
	Rev   string `arg:"" optional:"" help:"Revision to restore (default: undo the latest change)"`
}

func (c *AgentsRevertCmd) Run() error {
	return agentsHistoryExit(cmd.RunAgentsRevert(os.Stdout, c.Store, c.Name, c.Rev))
}

func agentsHistoryExit(err error) error {
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		cliout.Exit(1)
	}
	return nil
}

// SkillsCmd implements `agentctl skills` — lists Claude Code slash-command files.
// Without flags it prints a text table; --tui launches the interactive browser.
type SkillsCmd struct {
	TUI  bool `help:"Open interactive TUI browser"`
	JSON bool `help:"Output as JSON"`
}

func (c *SkillsCmd) Run() error {
	if c.TUI {
		return cliout.RunTUI(cmd.CmdSkillsManager, c.table)
	}
	return c.table()
}

func (c *SkillsCmd) table() error {
	if err := cmd.PrintSkillsTable(c.JSON); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		cliout.Exit(1)
	}
	return nil
}

// MemoryCmd implements `agentctl memory` — lists CLAUDE.md memory files.
// Without flags it prints a text table; --tui launches the interactive browser.
type MemoryCmd struct {
	TUI  bool `help:"Open interactive TUI browser"`
	JSON bool `help:"Output as JSON"`
}

func (c *MemoryCmd) Run() error {
	if c.TUI {
		return cliout.RunTUI(cmd.CmdMemory, c.table)
	}
	return c.table()
}

func (c *MemoryCmd) table() error {
	if err := cmd.PrintMemoryTable(c.JSON); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		cliout.Exit(1)
	}
	return nil
}

type CreateCmd struct {
	Name string `arg:"" optional:"" help:"Pre-fill agent name"`
}

func (c *CreateCmd) Run() error { cmd.CmdCreateMenu(c.Name); return nil }

type OpenAgentsWindowCmd struct{}

func (c *OpenAgentsWindowCmd) Run() error {
	if err := tmux.OpenOrFocusAgentsWindow(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		cliout.Exit(1)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Dispatch
// ---------------------------------------------------------------------------

// DispatchCmd implements `agentctl dispatch` with full flag support.
type DispatchCmd struct {
	Issue      int           `help:"GitHub issue number" short:"i"`
	ReviewPR   int           `name:"review-pr" help:"Review an open pull request; the verdict goes to the dispatching session (default agent: code-reviewer; --prompt/--prompt-key add review instructions)"`
	Author     string        `help:"Session of the --review-pr author: a handle such as a3f9, or a window ID such as @12 (default: the live window on the PR branch)"`
	Prompt     string        `help:"Ad-hoc prompt text" short:"p"`
	PromptKey  string        `name:"prompt-key" help:"Read ad-hoc prompt from a context store key" short:"k"`
	Template   string        `help:"Prompt template to dispatch (agentctl template list); its agent/model/dir/effort apply when those flags are unset, and --prompt adds instructions"`
	Agent      string        `help:"Agent name (auto-detected from labels if omitted)" short:"a"`
	Dir        string        `help:"Working directory"`
	Model      string        `help:"Model override (e.g. sonnet, opus, haiku)" short:"m"`
	Backend    string        `help:"Named backend from config (Ollama, Nous Portal); 'none' forces Anthropic"`
	Account    string        `help:"Claude account from config accounts (default: agent setting, account_rules, dispatching window, default_account)"`
	Harness    string        `help:"Agent program to run the agent on: claude or pi (default: agent config, then frontmatter, then claude)"`
	Repo       string        `help:"GitHub repo as owner/name (default: resolved from the working directory)"`
	NoWorktree bool          `name:"no-worktree" help:"Skip worktree creation"`
	Wait       bool          `help:"Block until agent completes"`
	Timeout    time.Duration `help:"Timeout duration for --wait (e.g. 5m, 1h)"`
}

func (c *DispatchCmd) Run() error {
	cmd.RunDispatch(cmd.DispatchOptions{
		Issue: c.Issue, ReviewPR: c.ReviewPR, Author: c.Author, Prompt: c.Prompt, PromptKey: c.PromptKey, Template: c.Template, Agent: c.Agent, Dir: c.Dir,
		Model: c.Model, Backend: c.Backend, Account: c.Account, Harness: c.Harness, NoWorktree: c.NoWorktree,
		Wait: c.Wait, Timeout: c.Timeout, Repo: c.Repo,
	})
	return nil
}

// ---------------------------------------------------------------------------
// Fleet hub
// ---------------------------------------------------------------------------

// HubCmd summons the fleet hub — creating the agentctl-hub session if it
// doesn't already exist — and switches into it.
type HubCmd struct {
	// Client is the invoking tmux client (the prefix+g binding passes
	// #{client_name}); empty when run from a shell or pane.
	Client string `name:"client" hidden:"" help:"Invoking client's tmux client name"`
}

func (c *HubCmd) Run() error { cmd.RunHub(c.Client); return nil }

// HubViewCmd is what actually runs *inside* the hub session's sole window,
// rendering the cross-session aggregator. Mirrors how `agentctl tickets
// --tui` is launched inside its own dedicated window.
type HubViewCmd struct{}

func (c *HubViewCmd) Run() error { cmd.RunHubView(); return nil }

// HubStatusCmd runs in the hub's status pane, above the live viewer: the
// selected row's title, recap, activity line and chips.
type HubStatusCmd struct{}

func (c *HubStatusCmd) Run() error { cmd.CmdHubStatus(); return nil }

// OpenCmd is `agentctl open [dir]`.
type OpenCmd struct {
	Dir    string `arg:"" optional:"" help:"Project directory (default: the current directory; a repo subdirectory or worktree opens the main repo)"`
	Detach bool   `help:"Start the session if needed but do not switch or attach; print its name"`
	Ensure bool   `hidden:"" help:"Internal: prepare the session from a process pinned to Dir"`
}

func (c *OpenCmd) Run() error {
	return cmd.RunOpen(c.Dir, cmd.OpenOptions{Detach: c.Detach, Ensure: c.Ensure})
}

// HubNewSessionCmd is the hub's "+ new session" helper: run with cwd = Dir,
// it initializes the project if needed, starts its tmux session detached and
// launches an agent there in the background, printing the window ID.
type HubNewSessionCmd struct {
	Dir string `arg:"" help:"Absolute project directory"`
	// Template launches a prompt template instead of a bare agent. Agent,
	// Model override its values; Prompt is appended as extra instructions.
	Template string `help:"Launch this prompt template"`
	Agent    string `help:"Agent for the template launch"`
	Model    string `help:"Model for the template launch"`
	Prompt   string `help:"Extra instructions appended to the template prompt"`
}

func (c *HubNewSessionCmd) Run() error {
	if c.Template == "" {
		return cmd.RunHubNewSession(c.Dir)
	}
	return cmd.RunHubNewSessionTemplate(c.Dir, cmd.HubTemplateLaunch{
		Template: c.Template, Agent: c.Agent, Model: c.Model, Prompt: c.Prompt,
	})
}

// HubResumeCmd is the hub's Resume for a closed row: run with cwd = Dir, it
// starts the project's tmux session if needed and relaunches the agent
// resuming the recorded session, printing the window ID.
type HubResumeCmd struct {
	Dir       string `arg:"" help:"Absolute project directory"`
	WorkDir   string `name:"work-dir" help:"Directory the agent ran in (default: the project)"`
	Agent     string `required:"" help:"Agent to relaunch"`
	SessionID string `name:"session-id" required:"" help:"Session to resume"`
}

func (c *HubResumeCmd) Run() error {
	return cmd.RunHubResume(os.Stdout, cmd.HubResumeOptions{ProjectDir: c.Dir, WorkDir: c.WorkDir, Agent: c.Agent, SessionID: c.SessionID})
}

// ResumeCmd implements `agentctl resume <handle>`: it relaunches a closed
// session through the same path as the hub's Resume (hub-resume).
type ResumeCmd struct {
	Handle string `arg:"" help:"Session handle (or full session ID) of a closed session"`
}

func (c *ResumeCmd) Run() error { return cmd.RunResume(os.Stdout, c.Handle) }

// JumpNextBlockedCmd finds the first blocked agent across every live
// agentctl session and switches to it. Works from any session.
type JumpNextBlockedCmd struct {
	// ClientSession is the invoking client's session (the prefix+b binding
	// passes #{client_session}); when it is the hub the hub does the jump.
	ClientSession string `name:"client-session" hidden:"" help:"Invoking client's tmux session"`
	// Client is the invoking client's name, so the switch targets it.
	Client string `name:"client" hidden:"" help:"Invoking client's tmux client name"`
}

func (c *JumpNextBlockedCmd) Run() error {
	cmd.RunJumpNextBlocked(c.ClientSession, c.Client)
	return nil
}

// ---------------------------------------------------------------------------
// Tickets
// ---------------------------------------------------------------------------

// TicketCmd handles `agentctl ticket` (alias `tickets`): browse is the
// default subcommand, create opens the creation form. Browse is visible
// (not hidden) so `ticket --help` lists its --tui/--json flags, matching
// StatusCmd.List.
type TicketCmd struct {
	Browse TicketBrowseCmd `cmd:"" default:"withargs" help:"List issues, or show one by number (default)"`
	Create TicketCreateCmd `cmd:"" help:"Create a new ticket"`
}

// TicketBrowseCmd is the default subcommand for `agentctl ticket` (alias `tickets`).
// Without flags it prints a text table; --tui launches the interactive viewer.
type TicketBrowseCmd struct {
	TUI    bool `help:"Open interactive TUI viewer"`
	JSON   bool `help:"Output as JSON"`
	Number int  `arg:"" optional:"" help:"Issue number for detail view"`
}

func (c *TicketBrowseCmd) Run() error {
	if c.TUI {
		return cliout.RunTUI(cmd.RunTickets, c.table)
	}
	return c.table()
}

func (c *TicketBrowseCmd) table() error {
	if c.Number > 0 {
		if err := cmd.PrintTicketDetail(c.Number, c.JSON); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			cliout.Exit(1)
		}
		return nil
	}
	if err := cmd.PrintTicketsTable(c.JSON); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		cliout.Exit(1)
	}
	return nil
}

// TicketCreateCmd implements `agentctl ticket create [flags]`.
type TicketCreateCmd struct {
	Title    string `help:"Pre-fill the issue title"`
	Body     string `help:"Pre-fill the issue body (inline string)"`
	BodyFile string `name:"body-file" help:"Pre-fill body from file (takes precedence over --body)"`
	Labels   string `help:"Comma-separated label names to pre-select"`
}

func (c *TicketCreateCmd) Run() error {
	runCmdTicketCreate(c.Title, c.Body, c.BodyFile, c.Labels)
	return nil
}

type TicketMenuCmd struct{}

func (c *TicketMenuCmd) Run() error { cmd.RunTicketMenu(); return nil }

type TicketLaunchCmd struct{}

func (c *TicketLaunchCmd) Run() error { cmd.RunTicketLaunch(); return nil }

type OpenTicketsWindowCmd struct{}

func (c *OpenTicketsWindowCmd) Run() error {
	if err := tmux.OpenOrFocusTicketsWindow(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		cliout.Exit(1)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

// SessionsCmd handles `agentctl sessions` (shows browser) and sub-subcommands.
type SessionsCmd struct {
	Browse  SessionsBrowseCmd  `cmd:"" default:"withargs" hidden:""`
	Sync    SessionsSyncCmd    `cmd:"" help:"Sync session metadata"`
	Summary SessionsSummaryCmd `cmd:"" help:"Generate session summary"`
	Search  SessionsSearchCmd  `cmd:"" help:"Search session transcripts for text"`
}

// SessionsBrowseCmd is the default subcommand for `agentctl sessions`.
// Without flags it prints a text table; --tui launches the interactive viewer.
type SessionsBrowseCmd struct {
	TUI  bool   `help:"Open interactive TUI viewer"`
	JSON bool   `help:"Output as JSON"`
	ID   string `arg:"" optional:"" help:"Session ID (or prefix) for detail view"`
}

func (c *SessionsBrowseCmd) Run() error {
	if c.TUI {
		return cliout.RunTUI(func() { cmd.CmdSessions(dispatch.WriteAgentSettings) }, c.table)
	}
	return c.table()
}

func (c *SessionsBrowseCmd) table() error {
	if c.ID != "" {
		if err := cmd.PrintSessionDetail(c.ID, c.JSON); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			cliout.Exit(1)
		}
		return nil
	}
	if err := cmd.PrintSessionsTable(c.JSON); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		cliout.Exit(1)
	}
	return nil
}

// SessionsSyncCmd implements `agentctl sessions sync`.
type SessionsSyncCmd struct {
	Session    string `help:"Sync only this session id (used by the Stop/SessionEnd hooks)" hidden:""`
	Transcript string `help:"Transcript path from the hook payload (skips the log scan)" hidden:""`
}

func (c *SessionsSyncCmd) Run() error {
	if c.Session != "" {
		if err := sessions.SyncOne(c.Session, c.Transcript); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			cliout.Exit(1)
		}
		return nil
	}
	cmd.CmdSessionsSync()
	return nil
}

// SessionsSummaryCmd implements `agentctl sessions summary [--date MM-DD-YYYY] [--dir /path]`.
type SessionsSummaryCmd struct {
	Date string `help:"Date in MM-DD-YYYY format (default: today)"`
	Dir  string `help:"Project directory (default: cwd)"`
}

func (c *SessionsSummaryCmd) Run() error {
	runCmdSessionsSummary(c.Date, c.Dir)
	return nil
}

// SessionsSearchCmd implements `agentctl sessions search <query> [flags]`.
type SessionsSearchCmd struct {
	Query         string `arg:"" help:"Text (or regex with --regex) to search for"`
	Role          string `help:"Filter by role" enum:"user,assistant,any" default:"any"`
	Tool          string `help:"Also match tool_use inputs for this tool (e.g. Bash)"`
	IncludeOutput bool   `name:"include-output" help:"Also match tool_result content (file contents, command output)"`
	Agent         string `help:"Filter by agent name"`
	Project       string `help:"Filter by project path (\".\" for cwd); default is all projects"`
	Since         string `help:"Only include sessions active since this long ago (e.g. 24h, 7d, 30d)"`
	Context       int    `help:"Number of surrounding messages of context to show around each hit"`
	Regex         bool   `help:"Treat query as a regular expression instead of a plain substring"`
	Limit         int    `help:"Maximum number of hits to show" default:"50"`
	JSON          bool   `help:"Output as JSON"`
}

func (c *SessionsSearchCmd) Run() error {
	since, err := sessions.ParseSince(c.Since)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		cliout.Exit(1)
	}
	opts := sessions.SearchOptions{
		Query:         c.Query,
		Role:          c.Role,
		Tool:          c.Tool,
		IncludeOutput: c.IncludeOutput,
		Agent:         c.Agent,
		Project:       c.Project,
		Since:         since,
		Context:       c.Context,
		Regex:         c.Regex,
		Limit:         c.Limit,
	}
	if err := cmd.PrintSessionsSearch(opts, c.JSON); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		cliout.Exit(1)
	}
	return nil
}

type SessionsMenuCmd struct {
	// Optional arg: window-id or agent name
	Arg string `arg:"" optional:"" help:"Agent name or window ID"`
}

func (c *SessionsMenuCmd) Run() error {
	// cmd.CmdSessionsMenu reads os.Args[2] itself; Arg exists so Kong accepts it.
	cmd.CmdSessionsMenu(dispatch.WriteAgentSettings)
	return nil
}

type OpenSessionsWindowCmd struct{}

func (c *OpenSessionsWindowCmd) Run() error {
	if err := tmux.OpenOrFocusSessionsWindow(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		cliout.Exit(1)
	}
	return nil
}

type OpenContextWindowCmd struct{}

func (c *OpenContextWindowCmd) Run() error {
	if err := tmux.OpenOrFocusContextWindow(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		cliout.Exit(1)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Audit trail
// ---------------------------------------------------------------------------

// AuditCmd groups `agentctl audit`: the trail query (`audit log`, the
// default, so bare `agentctl audit --tool Bash` keeps working) and the
// per-session grades (`audit report`).
type AuditCmd struct {
	Log      AuditLogCmd      `cmd:"" default:"withargs" help:"Query the hook-sourced audit trail (default; --json for machine-readable)"`
	Report   AuditReportCmd   `cmd:"" help:"Grade a past session (or --all) for safety and efficiency"`
	Verdicts AuditVerdictsCmd `cmd:"" help:"List PR review verdicts with the BLOCKED rate per author agent"`
}

// AuditReportCmd implements `agentctl audit report [<target>]`.
type AuditReportCmd struct {
	Target   string `arg:"" optional:"" help:"Window (@N), session ID, or transcript path (default: this window)"`
	All      bool   `help:"Grade many sessions: a leaderboard plus the most frequent findings"`
	Since    string `default:"7d" help:"With --all: how far back to look (e.g. 36h, 7d)"`
	Agent    string `help:"With --all: only sessions of this agent"`
	Project  string `help:"With --all: only sessions in this project (directory basename)"`
	Limit    int    `default:"20" help:"With --all: at most this many sessions, newest first"`
	Severity string `default:"low" enum:"high,med,medium,low" help:"Hide findings below this severity (high, med, low); scores still count them"`
	JSON     bool   `help:"Output as JSON (schema 1)"`
	Narrate  bool   `help:"Add a short written verdict from the nous backend (cached per session; sessions with no findings are skipped)"`
}

func (c *AuditReportCmd) Run() error {
	err := cmd.RunAuditReport(os.Stdout, cmd.AuditReportOptions{
		Target: c.Target, All: c.All, Since: c.Since, Agent: c.Agent, Project: c.Project,
		Limit: c.Limit, Severity: c.Severity, JSON: c.JSON, Narrate: c.Narrate,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		cliout.Exit(1)
	}
	return nil
}

// AuditLogCmd implements `agentctl audit [log]`, a reader over the hook-sourced audit
// trail written by the PostTool/PostToolFailure hook handlers into
// <runtimeDir>/audit/<session-id>.jsonl (see internal/audit). Without --json
// it prints a text table; --json emits the matching raw records and IS the
// export path for this command -- there is deliberately no separate export
// subcommand.
//
// --project filters on the project attribution every record now carries
// (audit.Record.Project). --all-projects broadens the scan itself from the
// current project's audit dir to every project the session registry knows
// about -- registered sessions only, see AllProjects's help text and
// audit.AllProjectAuditDirs's doc comment for why that is an accepted,
// documented gap rather than a bug.
type AuditLogCmd struct {
	Session     string `help:"Filter to a session ID"`
	Agent       string `help:"Filter to an agent name"`
	Window      string `help:"Filter to a window: a session handle or ID (e.g. a3f9), or a tmux window ID (e.g. @5)"`
	Tool        string `help:"Filter to a tool name (e.g. Bash, Edit, Read)"`
	Project     string `help:"Filter to a project directory basename (e.g. agentctl). Only matches records written since schema v2; older records have no project and are never matched."`
	Mode        string `help:"Filter to a permission_mode value (e.g. acceptEdits). Applied client-side, not part of the audit query." name:"mode"`
	AllProjects bool   `help:"Scan every project's audit trail, not just the current one, via the session registry (<global config dir>/registry). Covers registered sessions only -- a project that was never registered, or whose session has since ended, will not appear." name:"all-projects"`
	Failed      bool   `help:"Only show failed tool calls"`
	Denied      bool   `help:"Only show tool calls that were denied before running"`
	Event       string `help:"Filter to an event type (tool, tool_failure, tool_denied, tool_approved)"`
	Since       string `help:"Only show records at or after this time (RFC3339 timestamp or a duration like 1h, 30m)"`
	Limit       int    `help:"Max number of records to show, most recent first. 0 means unlimited." default:"100"`
	Full        bool   `help:"Print the DETAIL column untruncated instead of capping it to the terminal width"`
	JSON        bool   `help:"Output matching raw records as JSON instead of a table"`
}

// AuditVerdictsCmd implements `agentctl audit verdicts`, a reader over the
// review-verdict ledger (internal/verdicts) the PostToolUse hook appends to.
type AuditVerdictsCmd struct {
	Since string `help:"Only show verdicts from this far back (e.g. 7d, 36h)" default:"30d"`
	JSON  bool   `help:"Output as JSON instead of a table"`
}

func (c *AuditVerdictsCmd) Run() error {
	if err := cmd.RunAuditVerdicts(os.Stdout, c.Since, c.JSON); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		cliout.Exit(1)
	}
	return nil
}

// buildFilter parses c.Since and assembles the audit.Filter that Run passes
// to cmd.PrintAuditTable. Split out from Run so the flag-to-filter wiring can
// be tested directly, without going through Run's os.Exit-on-error path.
func (c *AuditLogCmd) buildFilter() (audit.Filter, error) {
	since, err := cmd.ParseAuditSince(c.Since)
	if err != nil {
		return audit.Filter{}, err
	}

	session, window := c.Session, c.Window
	if id, ok, err := cmd.ResolveSessionFilter(c.Window); err != nil {
		return audit.Filter{}, err
	} else if ok {
		if c.Session != "" {
			return audit.Filter{}, fmt.Errorf("--session and --window <handle> both name a session; use one")
		}
		session, window = id, ""
	}

	return audit.Filter{
		Session:    session,
		Agent:      c.Agent,
		Window:     window,
		Tool:       c.Tool,
		Project:    c.Project,
		Event:      c.Event,
		FailedOnly: c.Failed,
		DeniedOnly: c.Denied,
		Since:      since,
		Limit:      c.Limit,
	}, nil
}

func (c *AuditLogCmd) Run() error {
	filter, err := c.buildFilter()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		cliout.Exit(1)
	}

	if err := cmd.PrintAuditTable(filter, c.JSON, c.AllProjects, c.Full, c.Mode); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		cliout.Exit(1)
	}
	return nil
}

// ---------------------------------------------------------------------------
// File preview popup
// ---------------------------------------------------------------------------

// FilePreviewCmd implements `agentctl file-preview --path <tmpfile> --name <display-name>`.
// It is launched by the hook-permission-request handler inside a tmux popup.
// Exits 0 if the user allows, exits 1 if the user denies.
type FilePreviewCmd struct {
	Path         string `name:"path" required:"" help:"Temp file path containing file content"`
	Name         string `name:"name" required:"" help:"Display name (original filename) for syntax detection"`
	Agent        string `name:"agent" help:"Agent name shown in the title bar"`
	ContentWidth int    `name:"content-width" help:"Resolved popup content width (0 = model default)"`
	VPHeight     int    `name:"vp-height" help:"Resolved max viewport height (0 = model default)"`
	ResultFile   string `name:"result-file" help:"Write the allow/deny verdict to this file (0600)"`
}

func (c *FilePreviewCmd) Run() error {
	runCmdFilePreview(c.Path, c.Name, c.Agent, c.ContentWidth, c.VPHeight, c.ResultFile)
	return nil
}

// DispatchPreviewCmd implements `agentctl dispatch-preview --args-file <tmpfile>`.
// It is launched by the hook-permission-request handler when Claude Code requests
// permission for a Bash(agentctl dispatch ...) command.
// Exits 0 if the user allows, exits 1 if the user denies.
type DispatchPreviewCmd struct {
	ArgsFile   string `name:"args-file" required:"" help:"Temp file path containing dispatch preview args JSON"`
	ResultFile string `name:"result-file" help:"Write the allow/deny verdict to this file (0600)"`
}

func (c *DispatchPreviewCmd) Run() error { runCmdDispatchPreview(c.ArgsFile, c.ResultFile); return nil }

// ---------------------------------------------------------------------------
// TUI / internal
// ---------------------------------------------------------------------------

type MenuCmd struct{}

func (c *MenuCmd) Run() error {
	cmd.RunMenu(dispatch.WriteAgentSettings, cmd.CmdCreateMenu)
	return nil
}

type MonitorCmd struct{}

func (c *MonitorCmd) Run() error { cmdMonitor(); return nil }

type HudCmd struct {
	WindowID  string `arg:"" optional:"" help:"Tmux window ID"`
	FollowHub bool   `name:"follow-hub" hidden:"" help:"Follow the hub list's selected agent instead of one window"`
}

// Run runs the HUD for one window, or, with --follow-hub, for whichever agent
// the hub has selected.
func (c *HudCmd) Run() error {
	if c.FollowHub {
		cmd.CmdHudFollowHub()
		return nil
	}
	if c.WindowID == "" {
		return fmt.Errorf("hud: a window ID is required without --follow-hub")
	}
	cmd.CmdHud(c.WindowID)
	return nil
}

type HudToggleCmd struct {
	WindowID string `arg:"" optional:"" help:"Tmux window ID"`
}

// Run toggles the HUD for the given window. A WindowID that does not look
// like a tmux window ID ("@N") is ignored and the active window is used.
func (c *HudToggleCmd) Run() error {
	windowID := c.WindowID
	if !strings.HasPrefix(windowID, "@") {
		windowID = ""
	}
	cmd.CmdHudToggle(windowID)
	return nil
}

type ChromeBarCmd struct {
	WindowID string `arg:"" help:"Tmux window ID"`
}

func (c *ChromeBarCmd) Run() error { cmd.CmdChromeBar(c.WindowID); return nil }

type ChromeBarPaneCmd struct {
	WindowID string `arg:"" help:"Tmux window ID"`
}

func (c *ChromeBarPaneCmd) Run() error { cmd.CmdChromeBarPane(c.WindowID); return nil }

// SetActiveWindowCmd records the active window ID in the runtime dir. It is
// run by the after-select-window tmux hook (see tmux.activeWindowHookArgs).
type SetActiveWindowCmd struct {
	WindowID string `arg:"" help:"Tmux window ID"`
}

func (c *SetActiveWindowCmd) Run() error {
	path := tmux.ActiveWindowPath()
	// The file lives in the repo-controlled runtime dir: refuse to create it
	// through a symlinked dir (rename already never follows a symlinked
	// target).
	if err := safefs.CheckDir(paths.RuntimeDir(), filepath.Dir(path)); err != nil {
		return fmt.Errorf("set-active-window: %w", err)
	}
	if err := tmux.WriteActiveWindowFile(path, c.WindowID); err != nil {
		return err
	}
	// Focusing a window marks it seen and clears a pending ✓. Best effort: the
	// active-window file above is what matters for navigation.
	if err := winname.MarkSeen(c.WindowID); err != nil {
		logging.Debug("set-active-window: marking %s seen: %v", c.WindowID, err)
	}
	return nil
}

// ReconcileCmd runs Gone detection once (idempotent, flock-guarded); with
// --housekeeping it also does the janitor's periodic sweeps.
type ReconcileCmd struct {
	Housekeeping bool `help:"Also reap orphans and prune mailboxes and session logs"`
}

func (c *ReconcileCmd) Run() error { cmd.RunReconcile(c.Housekeeping); return nil }

// EnforceChromeCmd is run by the client-resized, client-attached and
// client-session-changed tmux hooks.
type EnforceChromeCmd struct{}

func (c *EnforceChromeCmd) Run() error { cmd.RunEnforceChrome(); return nil }

// ApplyConfigCmd re-applies session hooks and keybindings to the running
// session without stopping or starting anything.
type ApplyConfigCmd struct{}

func (c *ApplyConfigCmd) Run() error { return cmd.RunApplyConfig() }

type ToggleCmd struct {
	AgentName string `arg:"" help:"Agent name"`
	Setting   string `arg:"" help:"Setting name (alerts, cross_context, auto_accept)"`
}

func (c *ToggleCmd) Run() error { cmd.CmdToggle(c.AgentName, c.Setting); return nil }

type SettingsCmd struct{}

func (c *SettingsCmd) Run() error { cmd.CmdSettingsMenu(); return nil }

type SettingsMenuCmd struct {
	WindowIDOrName string `arg:"" help:"Window ID or agent name"`
}

func (c *SettingsMenuCmd) Run() error { cmd.CmdSettingsPopup(c.WindowIDOrName); return nil }

type PermissionsCmd struct{}

func (c *PermissionsCmd) Run() error { cmd.CmdPermissionsPopup(); return nil }

type ContextPushMenuCmd struct{}

func (c *ContextPushMenuCmd) Run() error { cmd.RunContextPushMenu(); return nil }

// SignalCmd groups the signal subcommands.
type SignalCmd struct {
	Done  SignalDoneCmd  `cmd:"" help:"Signal agent completion (sets state to done)"`
	Start SignalStartCmd `cmd:"" help:"Signal agent start (sets state to working)"`
	State SignalStateCmd `cmd:"" help:"Set explicit agent state"`
}

// SignalDoneCmd handles "agentctl signal done".
type SignalDoneCmd struct{}

func (c *SignalDoneCmd) Run() error { cmd.RunSignal("done", "done"); return nil }

// SignalStartCmd handles "agentctl signal start".
type SignalStartCmd struct{}

func (c *SignalStartCmd) Run() error { cmd.RunSignal("start", "working"); return nil }

// SignalStateCmd handles "agentctl signal state <value>".
type SignalStateCmd struct {
	Value string `arg:"" help:"State value: done, idle, blocked, or working" enum:"done,idle,blocked,working"`
}

func (c *SignalStateCmd) Run() error { cmd.RunSignal("state", c.Value); return nil }

type ThemePickerCmd struct{}

func (c *ThemePickerCmd) Run() error { cmdThemePicker(); return nil }

// ---------------------------------------------------------------------------
// Tools
// ---------------------------------------------------------------------------

// ContextCmd uses passthrough to preserve os.Args for cmd.RunContext's own routing.
type ContextCmd struct {
	Args []string `arg:"" optional:"" passthrough:""`
}

func (c *ContextCmd) Run() error { cmd.RunContext(); return nil }

type UsageCmd struct{}

func (c *UsageCmd) Run() error { cmd.RunUsage(); return nil }

// StatusCmd implements `agentctl status`: the non-interactive counterpart to
// the hub. `status` lists every agent across every live session, `status
// <window>` details one, `status peek` shows a pane, and `status whoami`
// answers "which window am I?".
type StatusCmd struct {
	// List is deliberately visible rather than a hidden default. Kong renders
	// a parent command's help as the list of its subcommands, so hiding the
	// default (as `sessions` does) leaves `agentctl status --help` showing no
	// flags at all — --blocked and --verify become undiscoverable. Naming it
	// keeps `agentctl status` and `agentctl status --blocked` working exactly
	// as before while making `status list --help` a real page.
	List   StatusListCmd   `cmd:"" default:"withargs" help:"List agents across every session (default)"`
	Peek   StatusPeekCmd   `cmd:"" help:"Show a window's pane contents and what they imply"`
	Whoami StatusWhoamiCmd `cmd:"" help:"Report which tmux window this process is in"`
}

// StatusListCmd is the default subcommand for `agentctl status`. With no
// argument it lists every agent; with a window reference it prints detail.
type StatusListCmd struct {
	Window  string `arg:"" optional:"" help:"Session handle, window ID (@275), index (275), or agent name for detail view"`
	JSON    bool   `help:"Output as JSON"`
	Session string `help:"Only show agents in sessions matching this name or project"`
	State   string `help:"Only show agents in this state: blocked, working, attention, idle, done" enum:"blocked,working,attention,idle,done," default:""`
	Blocked bool   `help:"Only show blocked agents (shorthand for --state blocked)"`
	Verify  bool   `help:"Capture each pane and flag any that contradict the recorded state"`
	Closed  bool   `help:"List agent windows closed recently (newest first) instead of live ones"`
	Since   string `help:"With --closed: how far back to look (e.g. 36h, 7d, 2w; default 7d)"`
}

func (c *StatusListCmd) Run() error {
	if c.Closed {
		if c.Window != "" || c.State != "" || c.Blocked || c.Verify {
			return fmt.Errorf("--closed cannot be combined with a window, --state, --blocked or --verify")
		}
		return cmd.RunStatusClosed(os.Stdout, cmd.ClosedOptions{Since: c.Since, JSON: c.JSON, Session: c.Session})
	}
	if c.Since != "" {
		return fmt.Errorf("--since needs --closed")
	}
	if c.Window != "" {
		return cmd.RunStatusDetail(c.Window, c.JSON)
	}
	return cmd.RunStatus(cmd.StatusOptions{
		JSON:    c.JSON,
		Session: c.Session,
		State:   c.State,
		Blocked: c.Blocked,
		Verify:  c.Verify,
	})
}

type StatusPeekCmd struct {
	Window string `arg:"" help:"Session handle, window ID (@275), index (275), or agent name"`
	Lines  int    `help:"How many trailing pane lines to show" default:"40"`
	JSON   bool   `help:"Output as JSON"`
}

func (c *StatusPeekCmd) Run() error { return cmd.RunPeek(c.Window, c.Lines, c.JSON) }

type StatusWhoamiCmd struct {
	JSON bool `help:"Output as JSON"`
}

func (c *StatusWhoamiCmd) Run() error { return cmd.RunWhoami(c.JSON) }

type HealthCmd struct{}

func (c *HealthCmd) Run() error { cmd.RunHealth(); return nil }

// ModelsCmd implements `agentctl models`. Without flags it prints the
// resolved model registry (built-ins + config overrides). --resolve runs a
// single model name through the resolution algorithm and explains which step
// matched.
type ModelsCmd struct {
	Resolve string `help:"Resolve a single model name and show which registry step matched"`
}

func (c *ModelsCmd) Run() error { return cmd.RunModels(c.Resolve) }

// RaceCmd implements `agentctl race [<target>]`.
type RaceCmd struct {
	Target   string        `arg:"" optional:"" help:"Window (@N), session handle or ID, or transcript path (default: this window)"`
	WPM      float64       `name:"wpm" default:"60" help:"Human typing speed, words per minute"`
	ReadWPM  float64       `name:"read-wpm" default:"250" help:"Human reading speed, words per minute"`
	Think    time.Duration `default:"0s" help:"Human pause per step (0 = no pauses)"`
	MaxGap   time.Duration `name:"max-gap" default:"60s" help:"Longest gap between entries charged to the agent"`
	Speed    float64       `help:"Replay at N times real time (overrides --duration)"`
	Duration time.Duration `default:"45s" help:"Fit the agent's whole timeline into this long a replay"`
	JSON     bool          `help:"Print totals as JSON and exit (no TUI)"`

	RealLife    bool    `name:"real-life" help:"Also show the human's time as calendar time: breaks, lunch, nights and weekends"`
	FocusHours  float64 `name:"focus-hours" default:"6" help:"With --real-life: hours of real keyboard work per day"`
	DaysPerWeek int     `name:"days-per-week" default:"5" help:"With --real-life: working days per week"`
}

func (c *RaceCmd) Run() error {
	return cmd.RunRace(os.Stdout, cmd.RaceOptions{
		Target: c.Target,
		Params: race.Params{WPM: c.WPM, ReadWPM: c.ReadWPM, Think: c.Think, MaxGap: c.MaxGap,
			RealLife: c.RealLife, Schedule: race.Schedule{FocusHours: c.FocusHours, DaysPerWeek: c.DaysPerWeek}},
		Speed:    c.Speed,
		Duration: c.Duration,
		JSON:     c.JSON,
	})
}

// DashboardsCmd uses passthrough to preserve os.Args for cmd.RunDashboards's own routing.
type DashboardsCmd struct {
	Args []string `arg:"" optional:"" passthrough:""`
}

func (c *DashboardsCmd) Run() error { cmd.RunDashboards(); return nil }

// CtlCmd implements `agentctl ctl [--async] <command> [args...]`.
type CtlCmd struct {
	Async   bool     `help:"Return immediately without waiting for popup to close"`
	Command []string `arg:"" help:"Control command and optional extra args" passthrough:""`
}

func (c *CtlCmd) Run() error {
	cmd.RunCmdCtl(c.Async, c.Command)
	return nil
}

// ---------------------------------------------------------------------------
// Setup
// ---------------------------------------------------------------------------

type InitCmd struct{}

func (c *InitCmd) Run() error { cmd.RunInit(); return nil }

// NewCmd implements `agentctl new <name> [--extends <base>]`.
type NewCmd struct {
	Name    string `arg:"" help:"Agent name"`
	Extends string `help:"Base agent to extend"`
}

func (c *NewCmd) Run() error {
	cmd.RunNew(c.Name, c.Extends)
	return nil
}

// SyncCmd implements `agentctl sync [--dry-run] [--clean] [--unlink]`.
type SyncCmd struct {
	DryRun bool `name:"dry-run" help:"Print what would happen without making changes"`
	Clean  bool `help:"Remove stale symlinks whose targets no longer exist"`
	Unlink bool `help:"Remove all symlinks pointing into the current project"`
}

func (c *SyncCmd) Run() error {
	cmd.RunSync(c.DryRun, c.Clean, c.Unlink)
	return nil
}

type HooksInstallCmd struct {
	Global bool `name:"global" help:"Install hooks globally (~/.claude/settings.json) instead of project-local (.claude/settings.local.json)"`
}

func (c *HooksInstallCmd) Run() error {
	path, err := cmd.InstallHooks(c.Global)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		cliout.Exit(1)
	}
	fmt.Printf("Hooks configured in %s\n", path)
	return nil
}

// InstallTemplatesCmd implements `agentctl install-templates [--force]`.
type InstallTemplatesCmd struct {
	Force bool `short:"f" help:"Overwrite existing files"`
}

func (c *InstallTemplatesCmd) Run() error {
	cmd.RunCmdInstallTemplates(c.Force)
	return nil
}

// InstallPluginCmd implements the `agentctl install-plugin` stub, which prints
// the Claude Code marketplace install commands.
type InstallPluginCmd struct{}

func (c *InstallPluginCmd) Run() error {
	cmd.RunCmdInstallPlugin()
	return nil
}

// ---------------------------------------------------------------------------
// Claude Code statusLine data source
// ---------------------------------------------------------------------------

// StatuslineCmd groups the statusline subcommands. It deliberately has no
// Run() method of its own -- Kong calls Run() on every node along a matched
// path, not just the leaf, so a parent Run() here would execute *in addition
// to* StatuslineInstallCmd.Run() on `agentctl statusline install` (this was
// caught manually: it printed both "Installed statusLine command in ..."
// and a stray statusline ingestion line). A bare `agentctl statusline`
// invocation (no subcommand) is intercepted by main.go's fast path
// (isStatuslineIngestCmd) before Kong ever sees it -- Claude Code cancels an
// in-flight statusLine script the instant a new update triggers, so that
// path can't afford Kong's init cost. If that fast path is ever bypassed,
// Kong will report "expected one of the following: install" for a bare
// invocation, which is an acceptable failure mode for a path that should
// never be reached in production.
type StatuslineCmd struct {
	Install StatuslineInstallCmd `cmd:"" help:"Install agentctl as Claude Code's statusLine script"`
}

// StatuslineInstallCmd implements `agentctl statusline install [--force] [--uninstall]`.
type StatuslineInstallCmd struct {
	Force     bool `help:"Overwrite an existing, differing statusLine entry"`
	Uninstall bool `help:"Remove agentctl's statusLine entry instead of installing it"`
}

func (c *StatuslineInstallCmd) Run() error {
	if err := statusline.Install(c.Force, c.Uninstall); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		cliout.Exit(1)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Helpers used by multiple Run() methods
// ---------------------------------------------------------------------------

// runCmdSessionsSummary is the parameter-based entry point for sessions summary.
// It replaces the flag-parsing that was in cmdSessionsSummary.
func runCmdSessionsSummary(dateFlag, dirFlag string) {
	// Resolve directory.
	dir := dirFlag
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: could not determine working directory: %v\n", err)
			cliout.Exit(1)
		}
	}

	// Resolve date.
	var queryDate time.Time
	if dateFlag == "" {
		queryDate = time.Now()
	} else {
		var err error
		queryDate, err = cmd.ParseDateFlag(dateFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Invalid date format. Expected MM-DD-YYYY, got: %s\n", dateFlag)
			cliout.Exit(1)
		}
	}

	cmd.CallCmdSessionsSummaryBody(dir, queryDate)
}

// runCmdTicketCreate is the parameter-based entry point for ticket create.
// It replaces the flag.NewFlagSet parsing in cmdTicketCreate.
func runCmdTicketCreate(title, body, bodyFile, labelsStr string) {
	// Resolve body: --body-file takes precedence.
	bodyContent := body
	if bodyFile != "" {
		data, err := os.ReadFile(bodyFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading --body-file: %v\n", err)
			cliout.Exit(1)
		}
		bodyContent = string(data)
	}

	// Parse comma-separated labels.
	var labels []string
	if labelsStr != "" {
		for _, l := range strings.Split(labelsStr, ",") {
			if l = strings.TrimSpace(l); l != "" {
				labels = append(labels, l)
			}
		}
	}

	cmd.RunTicketCreateBody(title, bodyContent, labels)
}

// DispatchCompleteCmd runs dispatch completion synchronously for one window.
type DispatchCompleteCmd struct {
	WindowID string `arg:"" help:"tmux window ID (e.g. @5)"`
}

func (c *DispatchCompleteCmd) Run() error { cmd.RunDispatchComplete(c.WindowID); return nil }

// ImproveFinishCmd takes the end snapshot of an improve run and sends its
// summary (improverun.Finish). Spawned detached by the Stop/SessionEnd hooks.
type ImproveFinishCmd struct {
	RunID string `arg:"" help:"improve run ID"`
}

func (c *ImproveFinishCmd) Run() error { return improverun.Finish(c.RunID) }

// LaunchWatchCmd watches a freshly launched agent window for Claude Code's
// folder-trust dialog and reports it (hooks.LaunchWatch). Spawned detached by
// internal/launchwatch after each launch; never run by hand.
type LaunchWatchCmd struct {
	WindowID string `arg:"" help:"tmux window ID (e.g. @5)"`
}

func (c *LaunchWatchCmd) Run() error { hooks.LaunchWatch(c.WindowID); return nil }

// RunUserHooksCmd runs the configured user hooks for one lifecycle event,
// using the environment inherited from the spawning hook.
type RunUserHooksCmd struct {
	Event string `arg:"" help:"Lifecycle event (stop, session_end, ...)"`
}

func (c *RunUserHooksCmd) Run() error { hooks.RunUserHooksSync(c.Event); return nil }

// ---------------------------------------------------------------------------
// Metrics serve
// ---------------------------------------------------------------------------

// MetricsCmd groups the metrics subcommands.
type MetricsCmd struct {
	Serve MetricsServeCmd `cmd:"" help:"Run the per-machine metrics process (HTTP /metrics, remote write); exits 0 if one is already running"`
}

// MetricsServeCmd implements `agentctl metrics serve`. Hooks and session start
// spawn it detached when metrics.enabled is set; it replaces the per-project
// monitor's metrics duties.
type MetricsServeCmd struct{}

func (c *MetricsServeCmd) Run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return metricsserve.Run(ctx, metricsserve.Options{})
}

// ---------------------------------------------------------------------------
// Serve (mobile web app server)
// ---------------------------------------------------------------------------

// ServeCmd groups `agentctl serve` and its management subcommands. Bare
// `agentctl serve` runs the server in the foreground (what launchd runs).
type ServeCmd struct {
	Run       ServeRunCmd       `cmd:"" default:"1" hidden:"" help:"Run the server in the foreground"`
	Install   ServeInstallCmd   `cmd:"" help:"Install the launchd LaunchAgent (prints a systemd unit on Linux)"`
	Uninstall ServeUninstallCmd `cmd:"" help:"Remove the launchd LaunchAgent"`
	Start     ServeStartCmd     `cmd:"" help:"Turn phone access on, starting the installed LaunchAgent if serve is down (prints the systemctl command on Linux)"`
	Stop      ServeStopCmd      `cmd:"" help:"Turn phone access off (no notifications or app; artifact links keep working); --all stops the server until next login"`
	Toggle    ServeToggleCmd    `cmd:"" help:"Turn phone access off if it is on, else on (starting serve if it is down)"`
	Status    ServeStatusCmd    `cmd:"" help:"Show whether the server runs, its URL, devices, cert expiry and CA fingerprint"`
	Logs      ServeLogsCmd      `cmd:"" help:"Print the tail of serve.log (or audit.jsonl with --audit); read-only"`
	Pair      ServePairCmd      `cmd:"" help:"Open a 5-minute pairing window and print QR codes for a phone"`
	Devices   ServeDevicesCmd   `cmd:"" help:"List or revoke paired devices"`
	CA        ServeCACmd        `cmd:"" name:"ca" help:"Manage the local certificate authority"`
}

type ServeRunCmd struct{}

func (c *ServeRunCmd) Run() error {
	return cmd.RunServe(strings.TrimPrefix(versionString(), "agentctl "))
}

type ServeInstallCmd struct{}

func (c *ServeInstallCmd) Run() error { return cmd.RunServeInstall(os.Stdout) }

type ServeUninstallCmd struct{}

func (c *ServeUninstallCmd) Run() error { return cmd.RunServeUninstall(os.Stdout) }

type ServeStartCmd struct{}

func (c *ServeStartCmd) Run() error { return cmd.RunServeStart(os.Stdout) }

// ServeStopCmd implements `agentctl serve stop`: phone access off, or with
// --all the old full stop (launchctl bootout; the LaunchAgent stays
// installed and starts again at login).
type ServeStopCmd struct {
	All bool `help:"Stop the server process itself (artifact links too) until next login; the LaunchAgent stays installed"`
}

func (c *ServeStopCmd) Run() error { return cmd.RunServeStop(os.Stdout, c.All) }

// ServeToggleCmd implements `agentctl serve toggle`. The prefix-key binding
// passes --message-client so the result shows as a tmux message.
type ServeToggleCmd struct {
	MessageClient string `name:"message-client" hidden:"" help:"Also show the result as a tmux message on this client"`
}

func (c *ServeToggleCmd) Run() error { return cmd.RunServeToggle(os.Stdout, c.MessageClient) }

type ServeStatusCmd struct{}

func (c *ServeStatusCmd) Run() error { return cmd.RunServeStatus(os.Stdout) }

// ServeLogsCmd implements `agentctl serve logs`, the read-only way to read
// serve's logs (the Bash validator blocks direct access to the serve dir).
type ServeLogsCmd struct {
	Lines  int  `short:"n" default:"50" help:"Number of trailing lines to print"`
	Audit  bool `help:"Print audit.jsonl instead of serve.log"`
	Follow bool `short:"f" help:"Keep printing new lines until interrupted"`
}

func (c *ServeLogsCmd) Run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return cmd.RunServeLogs(ctx, os.Stdout, cmd.ServeLogsOptions{Lines: c.Lines, Audit: c.Audit, Follow: c.Follow})
}

type ServePairCmd struct {
	Name string `help:"Name for the paired device (default: device-<id>)"`
}

func (c *ServePairCmd) Run() error { return cmd.RunServePair(os.Stdout, c.Name) }

type ServeDevicesCmd struct {
	List ServeDevicesListCmd `cmd:"" default:"1" hidden:"" help:"List paired devices"`
	Rm   ServeDevicesRmCmd   `cmd:"" help:"Revoke a paired device"`
}

type ServeCACmd struct {
	Rotate ServeCARotateCmd `cmd:"" help:"Replace the CA and server certificate; every device must reinstall the profile and pair again"`
}

type ServeCARotateCmd struct{}

func (c *ServeCARotateCmd) Run() error { return cmd.RunServeCARotate(os.Stdout) }

type ServeDevicesListCmd struct{}

func (c *ServeDevicesListCmd) Run() error { return cmd.RunServeDevices(os.Stdout) }

type ServeDevicesRmCmd struct {
	Device string `arg:"" help:"Device id or name"`
}

func (c *ServeDevicesRmCmd) Run() error { return cmd.RunServeDevicesRm(os.Stdout, c.Device) }

// ---------------------------------------------------------------------------
// Prompt templates
// ---------------------------------------------------------------------------

// TemplateCmd groups `agentctl template`. Templates live in
// ~/.config/agentctl/templates/<name>.md and <project>/.agentctl/templates/
// (project shadows global); `dispatch --template <name>` runs one.
type TemplateCmd struct {
	List TemplateListCmd `cmd:"" help:"List templates (project shadows global)"`
	Show TemplateShowCmd `cmd:"" help:"Print a template"`
	Add  TemplateAddCmd  `cmd:"" help:"Add a template (prompt from --prompt, --prompt-file or $EDITOR)"`
	Edit TemplateEditCmd `cmd:"" help:"Change a template: flags patch fields; with none, opens $EDITOR"`
	Rm   TemplateRmCmd   `cmd:"" help:"Remove a template"`
}

type TemplateListCmd struct {
	JSON bool `name:"json" help:"Print JSON"`
}

func (c *TemplateListCmd) Run() error { return cmd.RunTemplateList(os.Stdout, c.JSON) }

type TemplateShowCmd struct {
	Name string `arg:"" help:"Template name"`
}

func (c *TemplateShowCmd) Run() error { return cmd.RunTemplateShow(os.Stdout, c.Name) }

type TemplateAddCmd struct {
	Name        string `arg:"" help:"Template name (letters, digits, '.', '_' and '-')"`
	Description string `help:"One-line description shown in pickers"`
	Agent       string `help:"Default agent"`
	Model       string `help:"Default model"`
	Dir         string `help:"Default project directory (~ expanded at dispatch)"`
	Effort      string `help:"Default effort (low, medium, high, xhigh, max, auto)"`
	Prompt      string `help:"Prompt text"`
	PromptFile  string `name:"prompt-file" help:"File holding the prompt (copied now)"`
	Project     bool   `help:"Write to the project's .agentctl/templates instead of the global dir"`
}

func (c *TemplateAddCmd) Run() error {
	return cmd.RunTemplateAdd(os.Stdout, cmd.TemplateAddOptions{
		Name: c.Name, Description: c.Description, Agent: c.Agent, Model: c.Model, Dir: c.Dir,
		Effort: c.Effort, Prompt: c.Prompt, PromptFile: c.PromptFile, Project: c.Project,
	})
}

type TemplateEditCmd struct {
	Name        string  `arg:"" help:"Template name"`
	Description *string `help:"One-line description (empty clears it)"`
	Agent       *string `help:"Default agent (empty clears it)"`
	Model       *string `help:"Default model (empty clears it)"`
	Dir         *string `help:"Default project directory (empty clears it)"`
	Effort      *string `help:"Default effort (empty clears it)"`
	Prompt      *string `help:"Replace the prompt"`
	PromptFile  *string `name:"prompt-file" help:"Replace the prompt with this file's content"`
	Project     bool    `help:"Edit the project's template instead of the global one"`
}

func (c *TemplateEditCmd) Run() error {
	return cmd.RunTemplateEdit(os.Stdout, cmd.TemplateEditOptions{
		Name: c.Name, Description: c.Description, Agent: c.Agent, Model: c.Model, Dir: c.Dir,
		Effort: c.Effort, Prompt: c.Prompt, PromptFile: c.PromptFile, Project: c.Project,
	})
}

type TemplateRmCmd struct {
	Name    string `arg:"" help:"Template name"`
	Project bool   `help:"Remove the project's template instead of the global one"`
}

func (c *TemplateRmCmd) Run() error { return cmd.RunTemplateRm(os.Stdout, c.Name, c.Project) }

// ---------------------------------------------------------------------------
// Artifacts
// ---------------------------------------------------------------------------

// ArtifactCmd groups `agentctl artifact`: the one writer of the artifact
// store (<state dir>/artifacts), which `agentctl serve` shows on the phone.
type ArtifactCmd struct {
	Add      ArtifactAddCmd      `cmd:"" help:"Publish a file, a directory (with index.html) or stdin (-) as an artifact"`
	Update   ArtifactUpdateCmd   `cmd:"" help:"Add a revision to an artifact (the latest is the default view)"`
	List     ArtifactListCmd     `cmd:"" help:"List artifacts (this project; --all for every project)"`
	Show     ArtifactShowCmd     `cmd:"" help:"Show an artifact's metadata, revisions and URL"`
	Comments ArtifactCommentsCmd `cmd:"" help:"List the feedback sent on an artifact from the viewer"`
	URL      ArtifactURLCmd      `cmd:"" name:"url" help:"Print an artifact's URL"`
	Open     ArtifactOpenCmd     `cmd:"" help:"Open an artifact in the laptop browser"`
	Pin      ArtifactPinCmd      `cmd:"" help:"Pin an artifact so retention never prunes it"`
	Unpin    ArtifactUnpinCmd    `cmd:"" help:"Unpin an artifact"`
	Rm       ArtifactRmCmd       `cmd:"" help:"Delete an artifact and all its revisions"`
	Prune    ArtifactPruneCmd    `cmd:"" help:"Delete unpinned artifacts not updated recently"`
}

type ArtifactAddCmd struct {
	Path        string `arg:"" help:"File, directory, or - to read stdin"`
	Title       string `help:"Title (default: HTML <title>, first markdown # heading, else the file name)"`
	Description string `help:"One-line description"`
	Kind        string `enum:",html,markdown,image,text" default:"" help:"Override the kind inferred from the entry's extension (html, markdown, image, text)"`
	Entry       string `help:"File to open, relative to the directory (default: index.html); with stdin, the name to store it under (e.g. report.html)"`
	Pin         bool   `help:"Pin so retention never prunes it"`
	NoNotify    bool   `name:"no-notify" help:"Do not send a push notification for this artifact"`
	JSON        bool   `help:"Print the result as JSON"`
}

func (c *ArtifactAddCmd) Run() error {
	return cmd.RunArtifactAdd(os.Stdout, cmd.ArtifactAddOptions{
		Path: c.Path, Title: c.Title, Description: c.Description, Kind: c.Kind, Entry: c.Entry,
		Pin: c.Pin, NoNotify: c.NoNotify, JSON: c.JSON,
	})
}

type ArtifactUpdateCmd struct {
	ID    string `arg:"" help:"Artifact id"`
	Path  string `arg:"" help:"File, directory, or - to read stdin"`
	Note  string `help:"What changed in this revision"`
	Title string `help:"New title (default: keep the current one)"`
	JSON  bool   `help:"Print the result as JSON"`
}

func (c *ArtifactUpdateCmd) Run() error {
	return cmd.RunArtifactUpdate(os.Stdout, cmd.ArtifactUpdateOptions{ID: c.ID, Path: c.Path, Note: c.Note, Title: c.Title, JSON: c.JSON})
}

type ArtifactListCmd struct {
	Project string `help:"Project name or directory (default: the current project)"`
	All     bool   `help:"Every project"`
	Agent   string `help:"Only artifacts from this agent"`
	Since   string `help:"Only artifacts updated within this long (e.g. 7d, 12h)"`
	JSON    bool   `help:"Print as JSON"`
}

func (c *ArtifactListCmd) Run() error {
	return cmd.RunArtifactList(os.Stdout, cmd.ArtifactListOptions{Project: c.Project, All: c.All, Agent: c.Agent, Since: c.Since, JSON: c.JSON})
}

type ArtifactShowCmd struct {
	ID   string `arg:"" help:"Artifact id"`
	JSON bool   `help:"Print as JSON"`
}

func (c *ArtifactShowCmd) Run() error { return cmd.RunArtifactShow(os.Stdout, c.ID, c.JSON) }

type ArtifactCommentsCmd struct {
	ID   string `arg:"" help:"Artifact id"`
	JSON bool   `help:"Print as JSON"`
}

func (c *ArtifactCommentsCmd) Run() error { return cmd.RunArtifactComments(os.Stdout, c.ID, c.JSON) }

type ArtifactURLCmd struct {
	ID  string `arg:"" help:"Artifact id"`
	Rev int    `help:"Revision (default: the latest)"`
}

func (c *ArtifactURLCmd) Run() error { return cmd.RunArtifactURL(os.Stdout, c.ID, c.Rev) }

type ArtifactOpenCmd struct {
	ID  string `arg:"" help:"Artifact id"`
	Rev int    `help:"Revision (default: the latest)"`
}

func (c *ArtifactOpenCmd) Run() error { return cmd.RunArtifactOpen(os.Stdout, c.ID, c.Rev) }

type ArtifactPinCmd struct {
	ID string `arg:"" help:"Artifact id"`
}

func (c *ArtifactPinCmd) Run() error { return cmd.RunArtifactPin(os.Stdout, c.ID, true) }

type ArtifactUnpinCmd struct {
	ID string `arg:"" help:"Artifact id"`
}

func (c *ArtifactUnpinCmd) Run() error { return cmd.RunArtifactPin(os.Stdout, c.ID, false) }

type ArtifactRmCmd struct {
	ID  string `arg:"" help:"Artifact id"`
	Yes bool   `short:"y" help:"Do not ask for confirmation"`
}

func (c *ArtifactRmCmd) Run() error { return cmd.RunArtifactRm(os.Stdout, c.ID, c.Yes) }

type ArtifactPruneCmd struct {
	OlderThan string `name:"older-than" default:"30d" help:"Prune unpinned artifacts not updated for this long (e.g. 30d, 12h)"`
	DryRun    bool   `name:"dry-run" help:"List what would be pruned without deleting"`
}

func (c *ArtifactPruneCmd) Run() error { return cmd.RunArtifactPrune(os.Stdout, c.OlderThan, c.DryRun) }

// ---------------------------------------------------------------------------
// Cron (scheduled agent runs)
// ---------------------------------------------------------------------------

// CronCmd groups `agentctl cron`. Jobs live in ~/.config/agentctl/schedules.json
// (global only); launchd runs the hidden `cron tick` every minute.
type CronCmd struct {
	Add       CronAddCmd       `cmd:"" help:"Add a scheduled job"`
	Edit      CronEditCmd      `cmd:"" help:"Change fields of a scheduled job (only the flags given change; the name cannot)"`
	List      CronListCmd      `cmd:"" help:"List jobs with next fire, last result and throttles"`
	Runs      CronRunsCmd      `cmd:"" help:"Show run history (fired, skipped, failed)"`
	Run       CronRunCmd       `cmd:"" help:"Fire a job now (honors overlap and the usage gate unless --force)"`
	Pause     CronPauseCmd     `cmd:"" help:"Pause a job"`
	Resume    CronResumeCmd    `cmd:"" help:"Resume a paused job (missed fires are not caught up)"`
	Rm        CronRmCmd        `cmd:"" help:"Remove a job"`
	Install   CronInstallCmd   `cmd:"" help:"Install the launchd ticker (prints a crontab line on Linux)"`
	Uninstall CronUninstallCmd `cmd:"" help:"Remove the launchd ticker"`
	Tick      CronTickCmd      `cmd:"" hidden:"" help:"Run one scheduler pass (what launchd runs every minute)"`
}

type CronAddCmd struct {
	Name       string `arg:"" help:"Job name (letters, digits, - and _)"`
	Schedule   string `required:"" help:"5-field cron expression in local time, or @hourly/@daily/@weekly"`
	Agent      string `short:"a" help:"Agent to run (required unless --template names one)"`
	Dir        string `required:"" help:"Project directory (also where --template is looked up)"`
	Prompt     string `short:"p" help:"Prompt text"`
	PromptKey  string `name:"prompt-key" short:"k" help:"Context store key holding the prompt (read at each fire)"`
	PromptFile string `name:"prompt-file" type:"path" help:"File holding the prompt (copied into the job now)"`
	Template   string `help:"Prompt template (agentctl template list), read at each fire; its agent/model/effort fill unset flags"`
	NoWorktree bool   `name:"no-worktree" help:"Run in the project checkout instead of a worktree per fire"`
	Model      string `short:"m" help:"Model override"`
	Effort     string `help:"Effort override (low, medium, high, xhigh, max, auto)"`
	Backend    string `help:"Named backend from config; 'none' forces Anthropic"`
	Account    string `help:"Claude account to run under"`
	Timeout    string `help:"Interrupt a run after this long (e.g. 45m); default none"`
	Overlap    string `enum:"skip,allow" default:"skip" help:"When the previous run is still active: skip or allow"`
	Catchup    string `enum:"latest,none" default:"latest" help:"After missed fires: run the latest within catchup_window, or none"`
	// Mode window (default) dispatches the prompt; improve runs a fleet improve run.
	Mode         string `enum:"window,improve" default:"window" help:"What a fire does: window (dispatch the prompt) or improve (a fleet improve run; no prompt needed)"`
	ImproveFleet bool   `name:"improve-fleet" help:"Shorthand for --mode improve: run 'agentctl improve --fleet' over the schedule interval (24h for a daily job); --prompt or --template adds a theme"`
}

func (c *CronAddCmd) Run() error {
	mode := c.Mode
	if c.ImproveFleet {
		mode = "improve"
	}
	return cmd.RunCronAdd(os.Stdout, cmd.CronAddOptions{
		Mode: mode,
		Name: c.Name, Schedule: c.Schedule, Agent: c.Agent, Dir: c.Dir,
		Prompt: c.Prompt, PromptKey: c.PromptKey, PromptFile: c.PromptFile, Template: c.Template, NoWorktree: c.NoWorktree,
		Model: c.Model, Effort: c.Effort, Backend: c.Backend, Account: c.Account,
		Timeout: c.Timeout, Overlap: c.Overlap, Catchup: c.Catchup,
	})
}

type CronEditCmd struct {
	Name         string  `arg:"" help:"Job to edit (the name cannot change)"`
	Schedule     *string `help:"5-field cron expression in local time, or @hourly/@daily/@weekly (re-seeds the schedule: earlier fires are not caught up)"`
	Agent        *string `short:"a" help:"Agent to run"`
	Dir          *string `help:"Project directory"`
	Prompt       *string `short:"p" help:"Prompt text (replaces a prompt key)"`
	PromptKey    *string `name:"prompt-key" short:"k" help:"Context store key holding the prompt (read at each fire)"`
	PromptFile   *string `name:"prompt-file" type:"path" help:"File holding the prompt (copied into the job now)"`
	Template     *string `help:"Prompt template, read at each fire (replaces the prompt or prompt key)"`
	Worktree     bool    `help:"Run each fire in its own worktree" xor:"worktree"`
	NoWorktree   bool    `name:"no-worktree" help:"Run in the project checkout instead of a worktree per fire" xor:"worktree"`
	Model        *string `short:"m" help:"Model override (empty clears it)"`
	Effort       *string `help:"Effort override (low, medium, high, xhigh, max, auto; empty clears it)"`
	Backend      *string `help:"Named backend from config; 'none' forces Anthropic (empty clears it)"`
	Account      *string `help:"Claude account to run under (empty clears it)"`
	Timeout      *string `help:"Interrupt a run after this long (e.g. 45m; empty for none)"`
	Overlap      *string `help:"When the previous run is still active: skip or allow"`
	Catchup      *string `help:"After missed fires: latest or none"`
	Mode         *string `enum:"window,improve" help:"What a fire does: window (dispatch the prompt) or improve (a fleet improve run)"`
	ImproveFleet bool    `name:"improve-fleet" help:"Shorthand for --mode improve"`
}

func (c *CronEditCmd) Run() error {
	mode := c.Mode
	if c.ImproveFleet {
		m := "improve"
		mode = &m
	}
	return cmd.RunCronEdit(os.Stdout, cmd.CronEditOptions{
		Mode: mode,
		Name: c.Name, Schedule: c.Schedule, Agent: c.Agent, Dir: c.Dir,
		Prompt: c.Prompt, PromptKey: c.PromptKey, PromptFile: c.PromptFile, Template: c.Template,
		Worktree: c.Worktree, NoWorktree: c.NoWorktree,
		Model: c.Model, Effort: c.Effort, Backend: c.Backend, Account: c.Account,
		Timeout: c.Timeout, Overlap: c.Overlap, Catchup: c.Catchup,
	})
}

type CronListCmd struct{}

func (c *CronListCmd) Run() error { return cmd.RunCronList(os.Stdout) }

type CronRunsCmd struct {
	Name  string `arg:"" optional:"" help:"Only this job"`
	Limit int    `short:"n" default:"20" help:"Show the last N entries (0 for all)"`
}

func (c *CronRunsCmd) Run() error { return cmd.RunCronRuns(os.Stdout, c.Name, c.Limit) }

type CronRunCmd struct {
	Name  string `arg:"" help:"Job to fire"`
	Force bool   `help:"Ignore overlap, max_concurrent and the usage gate"`
	JSON  bool   `name:"json" hidden:"" help:"Print the run entry as JSON, whatever the result (for the hub and serve)"`
}

func (c *CronRunCmd) Run() error { return cmd.RunCronRun(os.Stdout, c.Name, c.Force, c.JSON) }

type CronPauseCmd struct {
	Name string `arg:"" help:"Job to pause"`
}

func (c *CronPauseCmd) Run() error { return cmd.RunCronSetEnabled(os.Stdout, c.Name, false) }

type CronResumeCmd struct {
	Name string `arg:"" help:"Job to resume"`
}

func (c *CronResumeCmd) Run() error { return cmd.RunCronSetEnabled(os.Stdout, c.Name, true) }

type CronRmCmd struct {
	Name string `arg:"" help:"Job to remove"`
}

func (c *CronRmCmd) Run() error { return cmd.RunCronRemove(os.Stdout, c.Name) }

type CronInstallCmd struct{}

func (c *CronInstallCmd) Run() error { return cmd.RunCronInstall(os.Stdout) }

type CronUninstallCmd struct{}

func (c *CronUninstallCmd) Run() error { return cmd.RunCronUninstall(os.Stdout) }

type CronTickCmd struct{}

func (c *CronTickCmd) Run() error { return cmd.RunCronTick() }
