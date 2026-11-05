package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/alecthomas/kong"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/cmd"
	"github.com/blsmth/agentctl/internal/config"
	ctxstore "github.com/blsmth/agentctl/internal/context"
	"github.com/blsmth/agentctl/internal/dispatch"
	"github.com/blsmth/agentctl/internal/hooks"
	"github.com/blsmth/agentctl/internal/improve"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/sessions"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/statusline"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/tui"
	"github.com/blsmth/agentctl/internal/windowstate"
)

//go:embed banner.txt
var bannerArt string

// Set by goreleaser ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	// Strip legacy --instance flag from os.Args so subcommand switch is unaffected.
	// The flag is no longer used for path disambiguation (CWD provides isolation)
	// but we still accept and ignore it for backwards compatibility.
	for i := 1; i < len(os.Args)-1; i++ {
		if os.Args[i] == "--instance" {
			os.Args = append(os.Args[:i], os.Args[i+2:]...)
			break
		}
	}

	// Tier 0: always needed — paths and logging first.
	//
	// Bare `agentctl` starts or attaches THIS directory's own session, so it
	// resolves its runtime dir with InitForSession: an AGENTCTL_RUNTIME_DIR
	// inherited from some other project's session is ignored rather than
	// silently redirecting the launch into that session. Every other
	// subcommand acts on behalf of an already-running session and must follow
	// the pin unconditionally. See paths.InitForSession.
	cwd, _ := os.Getwd()
	if len(os.Args) == 1 {
		paths.InitForSession(cwd)
	} else {
		paths.Init(cwd)
	}

	// Keybinding-spawned processes start in whatever cwd tmux hands them --
	// the active pane's directory for a popup, the attached client's for a
	// run-shell -- neither of which is this project. The binding cannot pin a
	// cwd (bind-key is server-global; see tmux.keybindingPinArgs), so it sets
	// a flag instead and the relocation happens here, against the runtime dir
	// just resolved from the session environment. No-op for every other
	// invocation.
	paths.HonorCwdPin()

	statuslineIngest := isStatuslineIngestCmd(os.Args)
	fastPath := len(os.Args) > 1 && (isHookCmd(os.Args[1]) || statuslineIngest)
	// Fast-path processes run many times per turn (statusline: every second),
	// so their startup noise is DEBUG-only.
	logging.SetFastPath(fastPath)
	// Everything except known read-only commands may write runtime state, so
	// create the runtime dir (and its .gitignore) up front for them. Help,
	// version, models and status must leave the cwd untouched.
	if !isReadOnlyInvocation(os.Args) {
		// A cloned repository can commit content under .agentctl/ -- most
		// dangerously a symlink that redirects a settings write into the
		// user's shell rc. Refuse such a runtime dir before writing anything.
		//
		// This check is defense in depth: every runtime-dir writer also
		// refuses symlinks on its own (internal/safefs), which is what
		// protects a running session from a mid-session `git pull`.
		switch {
		case hookIsNoop(os.Args):
			// The handler returns without writing anything, so skip the
			// check; these fire for every Claude Code session on the
			// machine, not just agentctl's.
		default:
			// The fast path runs on every hook and every statusline push,
			// so it skips the git subprocess when its runtime dir was pinned
			// through AGENTCTL_RUNTIME_DIR (set by a session start, which ran
			// the full check). The Lstat check always runs.
			withGit := !fastPath || os.Getenv("AGENTCTL_RUNTIME_DIR") == ""
			if err := paths.CheckRuntimeDir(withGit); err != nil {
				fmt.Fprintf(os.Stderr, "agentctl: %v\n", err)
				os.Exit(1)
			}
			paths.EnsureRuntimeDir()
		}
	}

	// File logging is lazy: the first logging.Log call opens (and creates)
	// the log file, so commands that never log leave no files behind. It runs
	// only after the runtime-dir safety check above, since the log lives
	// inside the runtime dir. A no-op hook (outside an agentctl session)
	// skipped that check, and must not create a log in whatever repo the
	// Claude Code session happens to be in.
	if !hookIsNoop(os.Args) {
		logging.Debug("startup: paths resolved (dir=%s)", paths.RuntimeDir())
	}

	// Fast path: hooks (and `agentctl statusline`, see isStatuslineIngestCmd)
	// only need minimal init — skip the heavy bootstrapping sequence
	// (ctxstore, tui, state, tmux.SetSessionName) that they never use. This
	// saves latency on every PreTool/PostTool/etc. call, and matters even
	// more for statusline: Claude Code debounces pushes at 300ms and cancels
	// an in-flight invocation the moment a new one triggers, so init cost
	// eats directly into the window before the process gets killed.
	if fastPath {
		if err := config.Init(); err != nil {
			logging.Error("config init: %v", err)
			os.Exit(1)
		}
		windowstate.Init()

		if statuslineIngest {
			// state.Init() is cheap (paths.RuntimeDir() is already resolved
			// in-memory, no disk I/O) and is required for the "rich" format's
			// turns/duration segments to find the right state.json.
			state.Init()
			dryRun := len(os.Args) == 3 && os.Args[2] == "--dry-run"
			statusline.Run(dryRun)
			return
		}

		// Wire hooks' mode-autonomy predicate to dispatch.IsAutonomous.
		// internal/hooks cannot import internal/dispatch directly (dispatch
		// already imports hooks for EnsureShellPrefixScript, so a direct
		// import the other way would be circular); main can import both,
		// so this is where the single source of truth gets connected.
		hooks.SetAutonomousModeFn(dispatch.IsAutonomous)
		// Wire the worktree-settings planter (hook-worktree-create's step 6)
		// to dispatch.WriteWorktreeSettings. Same import-cycle constraint as
		// SetAutonomousModeFn above: internal/hooks cannot import
		// internal/dispatch directly, so main resolves the agent config
		// (config.Load + resolveAgentName mirrors how other hooks do it) and
		// hands dispatch a plain worktreeDir string.
		hooks.SetWriteWorktreeSettingsFn(func(worktreeDir, repoDir string) error {
			agentName := os.Getenv("AGENTCTL_INSTANCE_NAME")
			if agentName == "" {
				agentName = os.Getenv("AGENTCTL_AGENT_NAME")
			}
			ac := config.Load().ForAgent(agentName)
			return dispatch.WriteWorktreeSettings(worktreeDir, repoDir, ac)
		})
		hooks.Dispatch(os.Args[1])
		return
	}

	// Full init for non-hook commands.
	if err := config.Init(); err != nil {
		fmt.Fprintf(os.Stderr, "agentctl: %v\n", err)
		os.Exit(1)
	}
	logging.Debug("startup: config.Init done (path=%s)", config.FilePath())

	ctxstore.Init()
	logging.Debug("startup: ctxstore.Init done (dir=%s)", ctxstore.Dir())

	tui.InitStyles()
	logging.Debug("startup: tui.InitStyles done")

	state.Init()
	logging.Debug("startup: state.Init done (path=%s)", state.FilePath())

	windowstate.Init()
	logging.Debug("startup: windowstate.Init done (dir=%s)", windowstate.Dir())

	tmux.SetSessionName()
	logging.Debug("startup: tmux.SetSessionName done (session=%s)", tmux.SessionName)

	logging.Debug("main() entered subcommand=%q", func() string {
		if len(os.Args) > 1 {
			return os.Args[1]
		}
		return "(none)"
	}())

	// Default: no subcommand → start or attach to the agentctl session.
	if len(os.Args) == 1 {
		runDefaultSession()
		return
	}

	// Short-circuit legacy version flags before Kong sees them (they look like
	// global flags, not subcommands, so Kong would error on them).
	if os.Args[1] == "--version" || os.Args[1] == "-v" {
		fmt.Println(versionString())
		return
	}

	// Hook subcommands (hook-*) never reach this point: they are all handled
	// by hooks.Dispatch on the fast path above.

	// Kong routing — parse the CLI and call the matched command's Run() method.
	var cli CLI
	ctx := kong.Parse(&cli,
		kong.Name("agentctl"),
		kong.Description("tmux-native command center for managing Claude Code agents"),
		kong.UsageOnError(),
	)
	if err := ctx.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// runDefaultSession starts or attaches to the agentctl tmux session.
// Called when agentctl is run with no arguments.
func runDefaultSession() {
	done := logging.Timing("tmux.SessionExists")
	exists := tmux.SessionExists()
	done()

	if exists {
		logging.Log("session exists, attaching")
		if err := tmux.AttachSession(); err != nil {
			logging.Error("attach session failed: %v", err)
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	logging.Log("no session, calling StartSession")
	cfg := config.Load()
	if err := tmux.StartSession(cfg.DefaultAgent); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func cmdLogsMenu() {
	cfg := config.Load()
	agents, err := agent.DiscoverFromCwd(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	var names []string
	for _, a := range agents {
		names = append(names, a.Name)
	}

	model := tui.NewLogsMenuModel(names)
	p := tea.NewProgram(model)
	finalModel, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	result := finalModel.(tui.LogsMenuModel).Result()
	if result.Cancelled || result.Agent == "" {
		return
	}

	cmd.RunLogs(result.Agent)
}

// cmdImprove launches a full improvement session for the current window's agent.
func cmdImprove() {
	agentName := resolveCurrentAgent()
	if agentName == "" {
		tmux.DisplayMessage("Switch to an agent window first")
		return
	}
	agentName = tmux.BaseAgentName(agentName)

	if err := improve.LaunchSession(agentName, "", false); err != nil {
		tmux.DisplayMessage(fmt.Sprintf("Error: %v", err))
	}
}

// cmdImproveMenu shows the Bubble Tea improve popup. windowID is the optional
// #{window_id} passed by the keybinding; it is ignored unless it looks like a
// tmux window ID ("@N").
func cmdImproveMenu(windowID string) {
	var agentName string
	if strings.HasPrefix(windowID, "@") {
		snap, _ := state.LoadSnapshot()
		if !snap.IsStale(3 * time.Second) {
			if as := snap.ByWindowID(windowID); as != nil {
				agentName = as.AgentName
			}
		}
		if agentName == "" {
			agentName = tmux.ResolveAgentByWindowID(windowID)
		}
	}
	if agentName == "" {
		agentName = resolveCurrentAgent()
	}
	logging.Log("improve-menu: resolved agentName=%q", agentName)
	if agentName == "" {
		logging.Log("improve-menu: no agent resolved, aborting")
		fmt.Fprintln(os.Stderr, "Switch to an agent window first")
		os.Exit(1)
	}
	// Improvement applies to the base agent definition, not per-instance
	agentName = tmux.BaseAgentName(agentName)

	model := tui.NewImproveModel(agentName)
	p := tea.NewProgram(model)
	finalModel, err := p.Run()
	if err != nil {
		logging.Log("improve-menu: bubbletea error: %v", err)
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	result := finalModel.(tui.ImproveModel).Result()
	logging.Log("improve-menu: result cancelled=%v mode=%q theme=%q", result.Cancelled, result.Mode, result.Theme)
	if result.Cancelled {
		return
	}

	logging.Log("improve-menu: launching improvement session for %q", agentName)
	if err := improve.LaunchSession(agentName, result.Theme, result.Mode == "quick"); err != nil {
		logging.Log("improve-menu: LaunchSession error: %v", err)
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	logging.Log("improve-menu: done")
}

// resolveCurrentAgent gets the base agent name from the current tmux window.
func resolveCurrentAgent() string {
	name := tmux.CurrentWindowName()
	if name == "" || name == "shell" {
		return ""
	}
	return tmux.StripAgentPrefix(name)
}

func cmdSaveState() {
	running := tmux.RunningAgents() // map[windowID]instanceName
	var agents []state.AgentState
	for windowID, name := range running {
		baseName := tmux.BaseAgentName(name)

		// Read the authoritative log path from the window state file.
		// Fall back to filesystem scan for windows without hook-driven state.
		ws, _ := windowstate.Read(windowID)
		logPath := ws.LogPath
		if logPath == "" {
			logPath = sessions.FindLatestLog(baseName, time.Time{}, nil)
		}
		sessionID := ""
		if logPath != "" {
			sessionID = strings.TrimSuffix(filepath.Base(logPath), ".jsonl")
		}
		workDir := tmux.PaneWorkDir(windowID)

		agents = append(agents, state.AgentState{
			Name:      name,
			AgentName: baseName,
			SessionID: sessionID,
			LogPath:   logPath,
			WorkDir:   workDir,
			WindowID:  windowID,
		})
	}
	if err := state.Save(&state.State{Agents: agents, Timestamp: time.Now()}); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving state: %v\n", err)
		os.Exit(1)
	}
	logging.Log("save-state: saved %d agents", len(agents))
}

// cmdThemePicker runs the quick theme picker popup (prefix+p).
// It displays a scrollable list of all available themes with color swatches
// and writes the hovered theme to config on every ↑/↓ keypress for live preview.
// On cancel (esc/q) the original theme is restored; on enter the selection persists.
func cmdThemePicker() {
	m := tui.NewThemePickerModel()
	p := tea.NewProgram(m)
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "theme-picker: %v\n", err)
		os.Exit(1)
	}
}

// runCmdFilePreview runs the file-preview popup inside a tmux display-popup.
// The content is read from the temp file at `path`; `name` is the original
// filename used for syntax detection and display. contentWidth/vpHeight are
// the popup-sizing inputs resolved from the attached tmux client's
// dimensions by the hook (0 => use the model's own defaults).
// Exits 0 if the user allows, exits 1 if the user denies.
func runCmdFilePreview(path, name, agentName string, contentWidth, vpHeight int) {
	content, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "file-preview: read %s: %v\n", path, err)
		os.Exit(1)
	}

	tui.InitStyles()

	m := tui.NewFilePreview(name, string(content), agentName,
		tui.WithFileContentWidth(contentWidth),
		tui.WithFileMaxViewportHeight(vpHeight),
	)
	p := tea.NewProgram(m)
	finalModel, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "file-preview: %v\n", err)
		os.Exit(1)
	}

	fm := finalModel.(tui.FilePreviewModel)
	if fm.Decision() == "allow" {
		os.Exit(0)
	}
	// deny or empty (user closed without deciding) → exit 1
	os.Exit(1)
}

// runCmdDispatchPreview runs the dispatch-preview popup inside a tmux display-popup.
// The args are read from the JSON temp file at argsFile.
// Exits 0 if the user allows, exits 1 if the user denies.
func runCmdDispatchPreview(argsFile string) {
	data, err := os.ReadFile(argsFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dispatch-preview: read %s: %v\n", argsFile, err)
		os.Exit(1)
	}

	var args hooks.DispatchPreviewArgs
	if err := json.Unmarshal(data, &args); err != nil {
		fmt.Fprintf(os.Stderr, "dispatch-preview: parse args: %v\n", err)
		os.Exit(1)
	}

	tui.InitStyles()

	m := tui.NewDispatchPreview(args.AgentName, args.TargetAgent, args.Issue, args.Prompt, args.PromptKey, args.Command,
		tui.WithContentWidth(args.ContentWidth),
		tui.WithMaxViewportHeight(args.VPHeight),
		tui.WithKind(string(args.Kind)),
	)
	p := tea.NewProgram(m)
	finalModel, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "dispatch-preview: %v\n", err)
		os.Exit(1)
	}

	dm := finalModel.(tui.DispatchPreviewModel)
	if dm.Decision() == "allow" {
		os.Exit(0)
	}
	// deny or empty (user closed without deciding) → exit 1
	os.Exit(1)
}

// isHookCmd reports whether the subcommand is a hook handler.
// Hook commands only need minimal initialisation (paths, logging, config,
// windowstate) — they never use ctxstore, tui, state, or
// tmux.SetSessionName.
func isHookCmd(subcmd string) bool {
	return strings.HasPrefix(subcmd, "hook-")
}

// unguardedHooks are hook handlers that run (and write) even outside an
// agentctl-launched session. Every other hook handler returns immediately
// unless AGENTCTL=1 (hooks.guardAgentctl, hooks.Noop).
var unguardedHooks = map[string]bool{
	"hook-worktree-create":    true,
	"hook-worktree-remove":    true,
	"hook-permission-request": true,
}

// hookIsNoop reports whether args invoke a hook whose handler will return
// without doing anything: a guarded hook outside an agentctl session.
func hookIsNoop(args []string) bool {
	return len(args) > 1 && isHookCmd(args[1]) && !unguardedHooks[args[1]] && os.Getenv("AGENTCTL") != "1"
}

// isStatuslineIngestCmd reports whether args is a bare `agentctl statusline`
// invocation (optionally with `--dry-run`) — the form Claude
// Code's settings.json actually runs, with the JSON payload arriving on
// stdin and no other arguments. It deliberately does NOT match `agentctl
// statusline install` (and any future statusline subcommand): those are
// interactive setup commands, not on Claude Code's 300ms debounce path, and
// route through Kong normally so StatuslineCmd/StatuslineInstallCmd in
// cli.go can parse their own flags.
//
// `--dry-run` is parsed here rather than via Kong deliberately: routing it
// onto the Kong path would add Kong's init cost to a path that Claude Code
// debounces at 300ms and cancels mid-flight on every new update.
//
// The subcommand is intentionally named "statusline", not "hook-statusline"
// — it is user-visible in settings.json and it is not a Claude Code hook —
// so it can't share isHookCmd's prefix match and needs its own predicate.
func isStatuslineIngestCmd(args []string) bool {
	if len(args) < 2 || args[1] != "statusline" {
		return false
	}
	switch len(args) {
	case 2:
		return true
	case 3:
		return args[2] == "--dry-run"
	default:
		return false
	}
}

// readOnlyCommands are subcommands that only read state (or print static
// text). They must not create .agentctl/ or a log file in the cwd.
var readOnlyCommands = map[string]bool{
	"version": true, "models": true, "status": true, "help": true,
}

// isReadOnlyInvocation reports whether args name a read-only invocation:
// a read-only subcommand, or any --help/-h/--version/-v request.
func isReadOnlyInvocation(args []string) bool {
	if len(args) < 2 {
		return false
	}
	if readOnlyCommands[args[1]] {
		return true
	}
	for _, a := range args[1:] {
		switch a {
		case "--help", "-h", "--version", "-v":
			return true
		}
	}
	return false
}
