package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/dispatch"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/sessions"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/tui"
)

// WriteAgentSettingsFn is the function signature for writing a Claude agent
// settings file. Implementations live in package main; the function is
// injected here to avoid a circular import. workDir must be the directory
// the agent will actually run in — dispatch.WriteAgentSettings uses it to
// decide whether the settings file it writes should include hooks (it omits
// them when workDir's own project-local settings already register agentctl
// hooks, so hooks fire from exactly one source).
type WriteAgentSettingsFn func(name string, ac config.AgentConfig, workDir string) string

// CmdSessionsMenu runs the Bubble Tea sessions menu TUI.
// The optional second CLI argument (os.Args[2]) may be a window-ID ("@N") or
// an agent name; it is used to pre-select the current agent.
func CmdSessionsMenu(writeSettings WriteAgentSettingsFn) {
	currentAgent := ""
	if len(os.Args) > 2 {
		arg := os.Args[2]
		if strings.HasPrefix(arg, "@") {
			// Window ID — resolve via snapshot, fall back to tmux.
			snap, _ := state.LoadSnapshot()
			if !snap.IsStale(3 * time.Second) {
				if as := snap.ByWindowID(arg); as != nil {
					currentAgent = as.AgentName
				}
			}
			if currentAgent == "" {
				currentAgent = tmux.ResolveAgentByWindowID(arg)
			}
		} else {
			currentAgent = arg
		}
	}
	if currentAgent == "" {
		currentAgent = resolveCurrentAgent()
	}
	if currentAgent != "" {
		currentAgent = tmux.BaseAgentName(currentAgent)
	}

	model := tui.NewSessionsModel(currentAgent)
	p := tea.NewProgram(model)
	finalModel, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	result := finalModel.(tui.SessionsModel).Result()
	if result.Cancelled {
		return
	}

	switch result.Action {
	case "resume":
		LaunchWithResume(result.Agent, result.SessionID, result.Project, writeSettings)
	case "log":
		if result.LogFile != "" {
			s := sessions.Session{
				ID:      result.SessionID,
				Agent:   result.Agent,
				Project: result.Project,
				LogFile: result.LogFile,
			}
			report := sessions.BuildReport(result.Agent, []sessions.Session{s})
			tmpFile, err := os.CreateTemp("", "agentctl-log-*.txt")
			if err != nil {
				return
			}
			tmpFile.WriteString(report)
			tmpFile.Close()
			tmux.NewTempWindow("logs-"+result.Agent, lessThenRemove(tmpFile.Name()))
		}
	}
}

// CmdSessions runs the interactive sessions browser TUI.
// It is the default action for `agentctl sessions` when --tui is passed.
func CmdSessions(writeSettings WriteAgentSettingsFn) {
	currentAgent := resolveCurrentAgent()
	if currentAgent != "" {
		currentAgent = tmux.BaseAgentName(currentAgent)
	}

	model := tui.NewSessionsModel(currentAgent)
	p := tea.NewProgram(model)
	finalModel, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	result := finalModel.(tui.SessionsModel).Result()
	if result.Cancelled {
		// Return focus to the window that was active before the sessions window was
		// opened, then remove the now-empty sessions window from the tab bar.
		originWindow := os.Getenv("AGENTCTL_ORIGIN_WINDOW")
		tmux.ReturnFocusAndKillWindow(originWindow, "sessions")
		return
	}

	switch result.Action {
	case "resume":
		LaunchWithResume(result.Agent, result.SessionID, result.Project, writeSettings)
	case "log":
		if result.LogFile != "" {
			s := sessions.Session{
				ID:      result.SessionID,
				Agent:   result.Agent,
				Project: result.Project,
				LogFile: result.LogFile,
			}
			report := sessions.BuildReport(result.Agent, []sessions.Session{s})
			tmpFile, err := os.CreateTemp("", "agentctl-log-*.txt")
			if err != nil {
				return
			}
			tmpFile.WriteString(report)
			tmpFile.Close()
			tmux.NewTempWindow("logs-"+result.Agent, lessThenRemove(tmpFile.Name()))
		}
	}

	// Explicit kill ensures the sessions window is gone from the tab bar even when
	// tmux has remain-on-exit enabled. Focus has already moved to the new agent
	// window or logs window via the action handlers above.
	tmux.ReturnFocusAndKillWindow("", "sessions")
}

// CmdSessionsSync runs the session metadata backfill: reads all Claude JSONL
// files, creates or updates .meta.json sidecars, and purges old metadata.
func CmdSessionsSync() {
	cfg := config.Load()
	added, updated, pruned, err := sessions.Sync(cfg.SessionRetentionDays)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	totalBytes, _, _ := sessions.DiskUsage()
	fmt.Printf("Synced %d sessions (%d updated, %d pruned). Metadata: %.1f KB\n",
		added+updated, updated, pruned, float64(totalBytes)/1024)
}

// CallCmdSessionsSummaryBody is the body of sessions summary after args are resolved.
// Called by runCmdSessionsSummary in cli.go after Kong flag parsing.
func CallCmdSessionsSummaryBody(dir string, queryDate time.Time) {
	metas, err := sessions.FindMirroredSessions(sessions.QueryOpts{
		ProjectDir: dir,
		Date:       queryDate,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if len(metas) == 0 {
		// Check whether the metadata directory exists at all.
		allMetas, _ := sessions.FindMirroredSessions(sessions.QueryOpts{ProjectDir: dir})
		if len(allMetas) == 0 {
			fmt.Printf("No session metadata found. Run \"agentctl sessions sync\" to populate.\n")
		} else {
			fmt.Printf("No sessions found for %s on %s.\n",
				dir, queryDate.Format("01-02-2006"))
		}
		return
	}

	fmt.Print(sessions.FormatDailySummary(dir, queryDate, metas))
}

// ParseDateFlag parses a date string in MM-DD-YYYY format and returns a time.Time.
func ParseDateFlag(s string) (time.Time, error) {
	return time.ParseInLocation("01-02-2006", s, time.Local)
}

// LaunchWithResume launches an agent window and resumes the given session.
func LaunchWithResume(name string, sessionID string, workDir string, writeSettings WriteAgentSettingsFn) {
	cfg := config.Load()
	ac := cfg.ForAgent(name)
	windowName := tmux.NextInstanceName(name)
	// --fork-session generates a new session ID we don't control upfront,
	// so we can't pre-compute the log path. The monitor falls back to findLatestLog.
	shellCmd := dispatch.BuildBaseFlags(dispatch.ClaudeInvocation{
		Agent:        name,
		Resume:       sessionID,
		ForkSession:  true,
		SettingsPath: writeSettings(name, ac, workDir),
	}, ac)
	wid, err := tmux.LaunchAgent(windowName, workDir, shellCmd, "", name, "", nil, true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if wid != "" {
		if err := tmux.SwitchToWindow(wid); err != nil {
			logging.Warn("launchWithResume: switch to window %s: %v", wid, err)
		}
	}
}

// GetSessionPreview returns a short preview string for the given session.
func GetSessionPreview(s sessions.Session) string {
	return sessions.GetSessionPreview(s)
}

// resolveCurrentAgent returns the base agent name from the current tmux window,
// or an empty string if the current window is not an agent window.
func resolveCurrentAgent() string {
	name := tmux.CurrentWindowName()
	if name == "" || name == "shell" {
		return ""
	}
	return tmux.StripAgentPrefix(name)
}
