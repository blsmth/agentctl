package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/tmux"
)

// hubNewSessionDeps are the side effects of RunHubNewSession, injectable so
// tests never touch tmux or launch an agent.
type hubNewSessionDeps struct {
	runtimeDir    func() string
	sessionName   func() string
	sessionExists func() bool
	sessionPath   func(name string) string
	startSession  func() error
	launch        func()
}

var hubNewSessionLive = hubNewSessionDeps{
	runtimeDir:    paths.RuntimeDir,
	sessionName:   func() string { return tmux.SessionName },
	sessionExists: tmux.SessionExists,
	sessionPath:   tmuxSessionPath,
	startSession:  func() error { return tmux.StartSessionDetached(config.Load().DefaultAgent) },
	launch: func() {
		// Same as the hub's per-group "+ new" agent: `agentctl launch
		// --background --permission-mode auto`, which prints the window ID.
		RunLaunch("", LaunchOptions{Background: true, PermissionMode: "auto"})
	},
}

// tmuxSessionPath is a live session's #{session_path}, or "".
func tmuxSessionPath(name string) string {
	for _, s := range tmux.ListSessionsWithPaths() {
		if s.Name == name {
			return s.Path
		}
	}
	return ""
}

// RunHubNewSession is `agentctl hub-new-session <dir>`, the hub's "+ new
// session" helper. The hub runs it with cwd = dir and AGENTCTL_RUNTIME_DIR =
// <dir>/.agentctl, so this process is already bound to that project, the way
// bare `agentctl` run in dir would be. It initializes the project when
// .claude/agents/ is missing (without templates), starts the project's tmux
// session detached when it is not running, and launches an agent in it,
// printing the new window ID as the last stdout line.
func RunHubNewSession(dir string) error {
	return runHubNewSession(dir, hubNewSessionLive)
}

func runHubNewSession(dir string, d hubNewSessionDeps) error {
	if err := tmux.CheckNewSessionDir(dir); err != nil {
		return err
	}
	want := filepath.Join(dir, ".agentctl")
	if got := d.runtimeDir(); !tmux.SamePath(got, want) {
		return fmt.Errorf("runtime dir is %s, want %s", got, want)
	}
	if needsInit(dir) {
		if _, err := InitProject(dir, false); err != nil {
			return fmt.Errorf("initializing %s: %w", dir, err)
		}
		logging.Log("hub-new-session: initialized %s", dir)
	}
	name := d.sessionName()
	if d.sessionExists() {
		// Two projects with the same basename share a session name; never
		// launch into the other one.
		if p := d.sessionPath(name); p != "" && !tmux.SamePath(p, dir) {
			return fmt.Errorf("tmux session %s already belongs to %s", name, p)
		}
	} else {
		logging.Log("hub-new-session: starting session %s detached in %s", name, dir)
		if err := d.startSession(); err != nil {
			return fmt.Errorf("starting session %s: %w", name, err)
		}
	}
	d.launch()
	return nil
}
