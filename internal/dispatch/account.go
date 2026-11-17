package dispatch

import (
	"fmt"

	"github.com/blsmth/agentctl/internal/claudehome"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/sessions"
	"github.com/blsmth/agentctl/internal/signal"
)

// resolveAccountFn is the seam Launch resolves the account through.
var resolveAccountFn = claudehome.ForLaunch

// parentAccountFn returns the dispatching window's account (seam for tests).
var parentAccountFn = func() string { return claudehome.ParentAccount(signal.ResolveWindowID()) }

// ApplyAccountEnv points the launch at the resolved account's config dir:
// CLAUDE_CONFIG_DIR (absolute) and AGENTCTL_ACCOUNT, which LaunchAgent
// records in windowstate. A zero Resolution (no accounts configured) is a
// no-op, so the window keeps its inherited environment. Credential variables
// that would outrank the per-dir login are scrubbed by tmux.LaunchAgent
// whenever CLAUDE_CONFIG_DIR is set here.
//
// Call it before PrepareBackend so a nous preflight checks the right
// account's settings.json.
func ApplyAccountEnv(env map[string]string, r claudehome.Resolution) {
	if r.Dir == "" {
		return
	}
	env[claudehome.EnvConfigDir] = r.Dir
	if r.Name != "" {
		env[claudehome.EnvAccount] = r.Name
	}
}

// restoreAccount returns the account a restored window relaunches under. A
// recorded account must still exist (never relaunch under another login). A
// window with no recorded account resumes from wherever its transcript
// lives, so --resume finds the session; with no accounts configured the
// zero Resolution keeps the inherited environment.
func restoreAccount(cfg *config.Config, account, logPath string) (claudehome.Resolution, error) {
	if !cfg.HasAccounts() {
		return claudehome.Resolution{}, nil
	}
	if account != "" {
		dir, err := claudehome.Dir(account)
		if err != nil {
			return claudehome.Resolution{}, fmt.Errorf("restoring under account %q: %w", account, err)
		}
		return claudehome.Resolution{Name: account, Dir: dir, Source: claudehome.SourceFlag}, nil
	}
	if logPath == "" {
		return claudehome.Resolution{}, nil
	}
	if name := claudehome.AccountForPath(logPath); name != "" {
		dir, err := claudehome.Dir(name)
		if err != nil {
			return claudehome.Resolution{}, err
		}
		return claudehome.Resolution{Name: name, Dir: dir, Source: claudehome.SourceDefault}, nil
	}
	return claudehome.Resolution{}, nil
}

// ResumeAccount returns the account to resume sessionID under: the account
// whose projects dir holds its transcript. The zero Resolution means
// "inherit" (no accounts, or the transcript is in an unnamed default dir).
func ResumeAccount(cfg *config.Config, sessionID string) (claudehome.Resolution, error) {
	return restoreAccount(cfg, "", sessions.FindLogBySessionID(sessionID))
}

// crossAccount reports whether a worker launched under workerDir runs under
// a different Claude login than its dispatcher (parentDir). Accounts do not
// message each other, so such a worker gets no orchestrator to SendMessage.
func crossAccount(workerDir, parentDir string) bool {
	if workerDir == "" || parentDir == "" {
		return false
	}
	return !claudehome.SameDir(workerDir, parentDir)
}

// dropCrossAccountOrchestrator clears req.Orchestrator and sets
// req.CrossAccount when the worker (workerDir) and its dispatcher
// (parentDir) run under different Claude accounts. Accounts do not message
// each other: the worker gets no SendMessage report step and no @N.%P
// pointer. spawned_by (applyParentEnv) still links the windows, so
// agentctl's own done/blocked notices reach the dispatcher.
func dropCrossAccountOrchestrator(req *Request, workerDir, parentDir string) {
	if !req.Orchestrator.Known() || !crossAccount(workerDir, parentDir) {
		return
	}
	logging.Log("dispatch: %s runs under a different account than its dispatcher; omitting the orchestrator section", req.AgentName)
	req.Orchestrator = Orchestrator{}
	req.CrossAccount = true
}
