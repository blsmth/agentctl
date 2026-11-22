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
	// launchTemplate dispatches a prompt template and returns the window ID.
	launchTemplate func(dir string, t HubTemplateLaunch) (string, error)
}

var hubNewSessionLive = hubNewSessionDeps{
	runtimeDir:  paths.RuntimeDir,
	sessionName: func() string { return tmux.SessionName },
	// Exact match: tmux.SessionExists resolves "agentctl-foo" by prefix and
	// would find a live agentctl-foobar.
	sessionExists: func() bool { return tmux.HasSessionNamed(tmux.SessionName) },
	sessionPath:   tmuxSessionPath,
	startSession:  func() error { return tmux.StartSessionDetached(config.Load().DefaultAgent) },
	launch: func() {
		// Same as the hub's per-group "+ new" agent: `agentctl launch
		// --background --permission-mode auto`, which prints the window ID.
		RunLaunch("", LaunchOptions{Background: true, PermissionMode: "auto"})
	},
	launchTemplate: launchHubTemplate,
}

// HubTemplateLaunch is a template launch from the hub's new-session form.
// Prompt is extra instructions appended to the template body, as with
// `dispatch --template X --prompt`.
type HubTemplateLaunch struct {
	Template, Agent, Model, Prompt string
}

// launchHubTemplate dispatches t in dir and returns the new window ID.
func launchHubTemplate(dir string, t HubTemplateLaunch) (string, error) {
	opts, err := hubTemplateOptions(dir, t)
	if err != nil {
		return "", err
	}
	wid, _, err := dispatchPromptOptions(opts)
	return wid, err
}

// hubTemplateOptions resolves the template against dir and applies the form's
// overrides: the form values win over the template's, an edited prompt
// replaces its body.
func hubTemplateOptions(dir string, t HubTemplateLaunch) (DispatchOptions, error) {
	opts, err := applyDispatchTemplate(DispatchOptions{
		Template: t.Template, Agent: t.Agent, Model: t.Model, Dir: dir, Prompt: t.Prompt,
	}, dir)
	if err != nil {
		return opts, err
	}
	if err := validateAgentName(opts.Agent, dir); err != nil {
		return opts, err
	}
	return opts, nil
}

// RunHubNewSessionTemplate is `agentctl hub-new-session <dir> --template T`:
// like RunHubNewSession, but the agent is dispatched with the template. The
// window ID is the last stdout line.
func RunHubNewSessionTemplate(dir string, t HubTemplateLaunch) error {
	wid, err := runHubNewSessionTemplate(dir, t, hubNewSessionLive)
	if err != nil {
		return err
	}
	fmt.Println(wid)
	return nil
}

func runHubNewSessionTemplate(dir string, t HubTemplateLaunch, d hubNewSessionDeps) (string, error) {
	if err := ensureHubSession(dir, d); err != nil {
		return "", err
	}
	return d.launchTemplate(dir, t)
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
	if err := ensureHubSession(dir, d); err != nil {
		return err
	}
	d.launch()
	return nil
}

// ensureHubSession checks that this process is bound to dir's project,
// initializes it when needed and starts its tmux session detached when it is
// not running (refusing a same-named session of another directory).
func ensureHubSession(dir string, d hubNewSessionDeps) error {
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
		switch p := d.sessionPath(name); {
		case p == "":
			return fmt.Errorf("tmux session %s exists but its directory is unknown", name)
		case !tmux.SamePath(p, dir):
			return fmt.Errorf("tmux session %s already belongs to %s", name, p)
		}
	} else {
		logging.Log("hub-new-session: starting session %s detached in %s", name, dir)
		if err := d.startSession(); err != nil {
			return fmt.Errorf("starting session %s: %w", name, err)
		}
	}
	return nil
}
