package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/blsmth/agentctl/internal/dispatch"
)

// HubResumeOptions is one closed window to resume (see RunHubResume).
type HubResumeOptions struct {
	ProjectDir string // the project whose tmux session gets the window
	WorkDir    string // where the agent ran; "" means ProjectDir
	Agent      string
	SessionID  string
}

// hubResumeLaunch launches the resumed session in the bound project and
// returns its window ID; a seam for tests.
var hubResumeLaunch = func(agent, sessionID, workDir string) (string, error) {
	return launchResume(agent, sessionID, workDir, dispatch.WriteAgentSettings, false)
}

// RunHubResume is `agentctl hub-resume <project>`, the hub's Resume for a
// closed row. Like hub-new-session it runs with cwd = the project and
// AGENTCTL_RUNTIME_DIR = <project>/.agentctl: it starts the project's tmux
// session detached when it is not running, then relaunches the recorded
// agent resuming its session (forked, as `sessions` resume does) in the
// directory it ran in, printing the new window ID as the last stdout line.
func RunHubResume(out io.Writer, o HubResumeOptions) error {
	return runHubResume(out, o, hubNewSessionLive)
}

func runHubResume(out io.Writer, o HubResumeOptions, d hubNewSessionDeps) error {
	if o.SessionID == "" {
		return errors.New("no session id recorded for this window")
	}
	if o.Agent == "" {
		return errors.New("no agent recorded for this window")
	}
	work := o.WorkDir
	if work == "" {
		work = o.ProjectDir
	}
	if fi, err := os.Stat(work); err != nil || !fi.IsDir() {
		return fmt.Errorf("directory %s is gone", work)
	}
	if err := ensureHubSession(o.ProjectDir, d); err != nil {
		return err
	}
	wid, err := hubResumeLaunch(o.Agent, o.SessionID, work)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, wid)
	return nil
}
