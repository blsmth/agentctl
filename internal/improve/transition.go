package improve

import (
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/state"
)

// launchDelay is the time to wait after a session ends before launching an
// improvement session. Override in tests to avoid slow test runs.
var launchDelay = 2 * time.Second

// launchSessionFn is the function used to launch improvement sessions.
// It is a package-level variable to allow tests to inject a mock.
var launchSessionFn = LaunchSession

// HandleTransition is the auto-improve transition handler. When an agent's
// session ends (state → Gone) and AutoImprove is enabled, launches an
// improvement session in the background.
func HandleTransition(t state.Transition, agentCfg config.AgentConfig) {
	if !wantsImprove(t, agentCfg) {
		return
	}
	go launchImprove(t)
}

// HandleTransitionSync is HandleTransition for a short-lived process (the
// detached `agentctl reconcile`): it returns only once the improvement session
// has been launched, since a goroutine would die with the process.
func HandleTransitionSync(t state.Transition, agentCfg config.AgentConfig) {
	if !wantsImprove(t, agentCfg) {
		return
	}
	launchImprove(t)
}

func wantsImprove(t state.Transition, agentCfg config.AgentConfig) bool {
	if t.To != state.StatusGone {
		return false
	}
	if strings.HasPrefix(t.Agent, "improve~") {
		return false // prevent recursion
	}
	return agentCfg.AutoImprove
}

func launchImprove(t state.Transition) {
	logging.Log("improve: triggering auto-improve session for agent=%q (session ended)", t.Agent)
	time.Sleep(launchDelay)
	if err := launchSessionFn(t.Agent, "", true); err != nil {
		logging.Error("improve: failed to launch improvement session for agent=%q: %v", t.Agent, err)
	}
}
