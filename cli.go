package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/audit"
	"github.com/blsmth/agentctl/internal/cmd"
	"github.com/blsmth/agentctl/internal/dispatch"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/safefs"
	"github.com/blsmth/agentctl/internal/sessions"
	"github.com/blsmth/agentctl/internal/statusline"
	"github.com/blsmth/agentctl/internal/tmux"
)

// CLI is the root Kong command struct. Every subcommand is a field here.
// Fields tagged `cmd:""` become subcommands; `hidden:""` hides them from help.
// The `Run()` method on each leaf struct is called by kong after parsing.
type CLI struct {
	// Info
	Version VersionCmd `cmd:"" help:"Print version info"`

	// Session management
	Quit        QuitCmd        `cmd:"" help:"Quit the agentctl session"`
	Restart     RestartCmd     `cmd:"" help:"Kill the agentctl session; relaunch with agentctl, or prefix+R inside tmux"`
	Start       StartCmd       `cmd:"" help:"Print session startup banner" hidden:""`
	QuitMenu    QuitMenuCmd    `cmd:"" name:"quit-menu" hidden:""`
	RestartMenu RestartMenuCmd `cmd:"" name:"restart-menu" hidden:""`
	SaveState   SaveStateCmd   `cmd:"" name:"save-state" hidden:""`
	Restore     RestoreCmd     `cmd:"" hidden:""`

	// Agent windows
	Launch      LaunchCmd      `cmd:"" help:"Launch an agent in a new window"`
	Kill        KillCmd        `cmd:"" help:"Kill an agent window"`
	Logs        LogsCmd        `cmd:"" help:"View agent session logs"`
	Improve     ImproveCmd     `cmd:"" help:"Launch self-improvement session"`
	Cleanup     CleanupCmd     `cmd:"" help:"Reclaim worktrees and windows from finished agents"`
	KillAll     KillAllCmd     `cmd:"" name:"kill-all" hidden:""`
	KillAllMenu KillAllMenuCmd `cmd:"" name:"kill-all-menu" hidden:""`
	KillMenu    KillMenuCmd    `cmd:"" name:"kill-menu" hidden:""`
	LogsMenu    LogsMenuCmd    `cmd:"" name:"logs-menu" hidden:""`
	ImproveMenu ImproveMenuCmd `cmd:"" name:"improve-menu" hidden:""`
	LaunchMenu  LaunchMenuCmd  `cmd:"" name:"launch-menu" hidden:""`

	// Agent management
	Agents           AgentsCmd           `cmd:"" aliases:"agent-manager" help:"List agents (--tui for interactive browser)"`
	Skills           SkillsCmd           `cmd:"" help:"List slash-command skills"`
	Memory           MemoryCmd           `cmd:"" help:"List memory files (CLAUDE.md)"`
	Create           CreateCmd           `cmd:"" aliases:"create-menu" help:"Create a new agent definition"`
	OpenAgentsWindow OpenAgentsWindowCmd `cmd:"" name:"open-agents-window" hidden:""`

	// Dispatch
	Dispatch DispatchCmd `cmd:"" help:"Dispatch an agent to work on a ticket"`

	// Herd hub — cross-session aggregator view
	Hub             HubCmd             `cmd:"" help:"Open the herd hub (cross-session dashboard)"`
	HubView         HubViewCmd         `cmd:"" name:"hub-view" hidden:""`
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

	// Audit trail (hook-sourced -- every tool call, not just Bash)
	Audit AuditCmd `cmd:"" help:"Query the hook-sourced audit trail (--json for machine-readable)"`

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
	Health     HealthCmd     `cmd:"" help:"Run health checks"`
	Status     StatusCmd     `cmd:"" help:"Query agent status across every live session"`
	Dashboards DashboardsCmd `cmd:"" help:"View dashboards"`
	Ctl        CtlCmd        `cmd:"" help:"Send operator control command"`
	Models     ModelsCmd     `cmd:"" help:"Inspect the model registry (context window, pricing, resolution)"`

	// Inter-agent messaging (docs/agent-messaging-design.md)
	Send  SendCmd  `cmd:"" help:"Send a message to another agent's mailbox"`
	Inbox InboxCmd `cmd:"" help:"List messages in the current window's mailbox"`
	Wake  WakeCmd  `cmd:"" help:"Wake an idle agent and deliver its pending mailbox via send-keys"`

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

type RestartMenuCmd struct{}

func (c *RestartMenuCmd) Run() error { cmd.RunRestartMenu(); return nil }

type SaveStateCmd struct{}

func (c *SaveStateCmd) Run() error { cmdSaveState(); return nil }

type RestoreCmd struct{}

func (c *RestoreCmd) Run() error { cmd.RunRestore(); return nil }

// ---------------------------------------------------------------------------
// Agent windows
// ---------------------------------------------------------------------------

type LaunchCmd struct {
	Name       string `arg:"" optional:"" help:"Agent name to launch (reads default_agent from config if omitted)"`
	NoWorktree bool   `name:"no-worktree" help:"Launch directly in the main checkout instead of an isolated git worktree"`
}

func (c *LaunchCmd) Run() error { cmd.RunLaunch(c.Name, c.NoWorktree); return nil }

type KillCmd struct {
	Name string `arg:"" help:"Agent name to kill"`
}

func (c *KillCmd) Run() error { cmd.RunKill(c.Name); return nil }

type LogsCmd struct {
	Name string `arg:"" help:"Agent name or window ID"`
}

func (c *LogsCmd) Run() error { cmd.RunLogs(c.Name); return nil }

type ImproveCmd struct{}

func (c *ImproveCmd) Run() error { cmdImprove(); return nil }

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

type LaunchMenuCmd struct {
	Name string `arg:"" help:"Agent name"`
}

func (c *LaunchMenuCmd) Run() error {
	cmd.RunLaunchMenu(c.Name, dispatch.WriteAgentSettings)
	return nil
}

// ---------------------------------------------------------------------------
// Agent management
// ---------------------------------------------------------------------------

// AgentsCmd implements `agentctl agents`.
// Without flags it prints a text table; --tui launches the interactive browser.
type AgentsCmd struct {
	TUI  bool   `help:"Open interactive TUI browser"`
	JSON bool   `help:"Output as JSON"`
	Name string `arg:"" optional:"" help:"Agent name for detail view"`
}

func (c *AgentsCmd) Run() error {
	if c.TUI {
		cmd.CmdAgentManager()
		return nil
	}
	if c.Name != "" {
		if err := cmd.PrintAgentDetail(c.Name, c.JSON); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return nil
	}
	if err := cmd.PrintAgentsTable(c.JSON); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
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
		cmd.CmdSkillsManager()
		return nil
	}
	if err := cmd.PrintSkillsTable(c.JSON); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
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
		cmd.CmdMemory()
		return nil
	}
	if err := cmd.PrintMemoryTable(c.JSON); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
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
		os.Exit(1)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Dispatch
// ---------------------------------------------------------------------------

// DispatchCmd implements `agentctl dispatch` with full flag support.
type DispatchCmd struct {
	Issue      int           `help:"GitHub issue number" short:"i"`
	Prompt     string        `help:"Ad-hoc prompt text" short:"p"`
	PromptKey  string        `name:"prompt-key" help:"Read ad-hoc prompt from a context store key" short:"k"`
	Agent      string        `help:"Agent name (auto-detected from labels if omitted)" short:"a"`
	Dir        string        `help:"Working directory"`
	Model      string        `help:"Model override (e.g. sonnet, opus, haiku)" short:"m"`
	Repo       string        `help:"GitHub repo as owner/name (default: resolved from the working directory)"`
	NoWorktree bool          `name:"no-worktree" help:"Skip worktree creation"`
	Wait       bool          `help:"Block until agent completes"`
	Timeout    time.Duration `help:"Timeout duration for --wait (e.g. 5m, 1h)"`
}

func (c *DispatchCmd) Run() error {
	cmd.RunDispatch(c.Issue, c.Prompt, c.PromptKey, c.Agent, c.Dir, c.Model, c.NoWorktree, c.Wait, c.Timeout, c.Repo)
	return nil
}

// ---------------------------------------------------------------------------
// Herd hub
// ---------------------------------------------------------------------------

// HubCmd summons the herd hub — creating the agentctl-hub session if it
// doesn't already exist — and switches into it.
type HubCmd struct{}

func (c *HubCmd) Run() error { cmd.RunHub(); return nil }

// HubViewCmd is what actually runs *inside* the hub session's sole window,
// rendering the cross-session aggregator. Mirrors how `agentctl tickets
// --tui` is launched inside its own dedicated window.
type HubViewCmd struct{}

func (c *HubViewCmd) Run() error { cmd.RunHubView(); return nil }

// JumpNextBlockedCmd finds the first blocked agent across every live
// agentctl session and switches to it. Works from any session.
type JumpNextBlockedCmd struct{}

func (c *JumpNextBlockedCmd) Run() error { cmd.RunJumpNextBlocked(); return nil }

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
		cmd.RunTickets()
		return nil
	}
	if c.Number > 0 {
		if err := cmd.PrintTicketDetail(c.Number, c.JSON); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return nil
	}
	if err := cmd.PrintTicketsTable(c.JSON); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
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
		os.Exit(1)
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
		cmd.CmdSessions(dispatch.WriteAgentSettings)
		return nil
	}
	if c.ID != "" {
		if err := cmd.PrintSessionDetail(c.ID, c.JSON); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return nil
	}
	if err := cmd.PrintSessionsTable(c.JSON); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	return nil
}

// SessionsSyncCmd implements `agentctl sessions sync`.
type SessionsSyncCmd struct{}

func (c *SessionsSyncCmd) Run() error { cmd.CmdSessionsSync(); return nil }

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
		os.Exit(1)
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
		os.Exit(1)
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
		os.Exit(1)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Audit trail
// ---------------------------------------------------------------------------

// AuditCmd implements `agentctl audit`, a reader over the hook-sourced audit
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
type AuditCmd struct {
	Session     string `help:"Filter to a session ID"`
	Agent       string `help:"Filter to an agent name"`
	Window      string `help:"Filter to a tmux window ID (e.g. @5)"`
	Tool        string `help:"Filter to a tool name (e.g. Bash, Edit, Read)"`
	Project     string `help:"Filter to a project directory basename (e.g. agentctl). Only matches records written since schema v2; older records have no project and are never matched."`
	Mode        string `help:"Filter to a permission_mode value (e.g. acceptEdits). Applied client-side, not part of the audit query." name:"mode"`
	AllProjects bool   `help:"Scan every project's audit trail, not just the current one, via the session registry (<global config dir>/registry). Covers registered sessions only -- a project that was never registered, or whose session has since ended, will not appear." name:"all-projects"`
	Failed      bool   `help:"Only show failed tool calls"`
	Denied      bool   `help:"Only show tool calls that were denied before running"`
	Event       string `help:"Filter to an event type (tool, tool_failure, tool_denied)"`
	Since       string `help:"Only show records at or after this time (RFC3339 timestamp or a duration like 1h, 30m)"`
	Limit       int    `help:"Max number of records to show, most recent first. 0 means unlimited." default:"100"`
	Full        bool   `help:"Print the DETAIL column untruncated instead of capping it to the terminal width"`
	JSON        bool   `help:"Output matching raw records as JSON instead of a table"`
}

// buildFilter parses c.Since and assembles the audit.Filter that Run passes
// to cmd.PrintAuditTable. Split out from Run so the flag-to-filter wiring can
// be tested directly, without going through Run's os.Exit-on-error path.
func (c *AuditCmd) buildFilter() (audit.Filter, error) {
	since, err := cmd.ParseAuditSince(c.Since)
	if err != nil {
		return audit.Filter{}, err
	}

	return audit.Filter{
		Session:    c.Session,
		Agent:      c.Agent,
		Window:     c.Window,
		Tool:       c.Tool,
		Project:    c.Project,
		Event:      c.Event,
		FailedOnly: c.Failed,
		DeniedOnly: c.Denied,
		Since:      since,
		Limit:      c.Limit,
	}, nil
}

func (c *AuditCmd) Run() error {
	filter, err := c.buildFilter()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if err := cmd.PrintAuditTable(filter, c.JSON, c.AllProjects, c.Full, c.Mode); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
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
}

func (c *FilePreviewCmd) Run() error {
	runCmdFilePreview(c.Path, c.Name, c.Agent, c.ContentWidth, c.VPHeight)
	return nil
}

// DispatchPreviewCmd implements `agentctl dispatch-preview --args-file <tmpfile>`.
// It is launched by the hook-permission-request handler when Claude Code requests
// permission for a Bash(agentctl dispatch ...) command.
// Exits 0 if the user allows, exits 1 if the user denies.
type DispatchPreviewCmd struct {
	ArgsFile string `name:"args-file" required:"" help:"Temp file path containing dispatch preview args JSON"`
}

func (c *DispatchPreviewCmd) Run() error { runCmdDispatchPreview(c.ArgsFile); return nil }

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
	WindowID string `arg:"" help:"Tmux window ID"`
}

func (c *HudCmd) Run() error { cmd.CmdHud(c.WindowID); return nil }

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
	return tmux.WriteActiveWindowFile(path, c.WindowID)
}

type ToggleCmd struct {
	AgentName string `arg:"" help:"Agent name"`
	Setting   string `arg:"" help:"Setting name (alerts, auto_improve, cross_context, auto_accept)"`
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
	Window  string `arg:"" optional:"" help:"Window ID (@275), index (275), or agent name for detail view"`
	JSON    bool   `help:"Output as JSON"`
	Session string `help:"Only show agents in sessions matching this name or project"`
	State   string `help:"Only show agents in this state: blocked, working, idle, done" enum:"blocked,working,idle,done," default:""`
	Blocked bool   `help:"Only show blocked agents (shorthand for --state blocked)"`
	Verify  bool   `help:"Capture each pane and flag any that contradict the recorded state"`
}

func (c *StatusListCmd) Run() error {
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
	Window string `arg:"" help:"Window ID (@275), index (275), or agent name"`
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
// Messaging
// ---------------------------------------------------------------------------

// SendCmd implements `agentctl send <addr> <message> [--kind fyi] [--subject
// "..."] [--wait-reply] [--timeout <dur>]`. <addr> is either a tmux window
// ID (e.g. "@41") or a running agent name. --wait-reply is sugar for
// enqueuing the message, then blocking exactly like `inbox --wait --from
// <addr>` for the reply.
type SendCmd struct {
	Addr      string        `arg:"" help:"Window ID (@41) or agent name"`
	Message   string        `arg:"" help:"Message body"`
	Kind      string        `help:"Message kind: done, blocked, fyi, or question" default:"fyi"`
	Subject   string        `help:"Optional subject line"`
	WaitReply bool          `name:"wait-reply" help:"Block until <addr> replies (same wait as inbox --wait --from <addr>)"`
	Timeout   time.Duration `help:"Timeout for --wait-reply (default 5m)"`
}

func (c *SendCmd) Run() error {
	cmd.RunSend(c.Addr, c.Message, c.Kind, c.Subject, c.WaitReply, c.Timeout)
	return nil
}

// WakeCmd implements `agentctl wake <addr>`. <addr> is either a tmux window
// ID (e.g. "@41") or a running agent name, resolved the same way as Send.
// It refuses to wake a window that is still working, and refuses when no
// live Claude process is found in the target pane -- see
// docs/agent-messaging-design.md §3 and internal/cmd/wake.go for why.
type WakeCmd struct {
	Addr string `arg:"" help:"Window ID (@41) or agent name"`
}

func (c *WakeCmd) Run() error {
	cmd.RunWake(c.Addr)
	return nil
}

// InboxCmd implements `agentctl inbox [--all] [--json] [--wait] [--from
// <addr>] [--timeout <dur>]`. It always resolves to the current window's
// mailbox. --wait blocks until at least one matching pending message
// exists, then drains and prints it -- see internal/mailbox.WaitAndClaim.
type InboxCmd struct {
	All     bool          `help:"Also show already-read messages"`
	JSON    bool          `help:"Output as JSON"`
	Wait    bool          `help:"Block until a matching message arrives, then drain and print it"`
	From    string        `help:"With --wait, only match messages from this sender (window ID or agent name)"`
	Timeout time.Duration `help:"Timeout for --wait (default 5m)"`
}

func (c *InboxCmd) Run() error {
	if c.Wait {
		if err := cmd.RunInboxWait(c.From, c.Timeout); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return nil
	}
	if err := cmd.RunInbox(c.All, c.JSON); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
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
		os.Exit(1)
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
		os.Exit(1)
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
			os.Exit(1)
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
			os.Exit(1)
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
			os.Exit(1)
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
