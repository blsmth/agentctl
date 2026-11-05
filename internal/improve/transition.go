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
	if t.To != state.StatusGone {
		return
	}
	if strings.HasPrefix(t.Agent, "improve~") {
		return // prevent recursion
	}
	if !agentCfg.AutoImprove {
		return
	}
	logging.Log("improve: triggering auto-improve session for agent=%q (session ended)", t.Agent)
	go func() {
		time.Sleep(launchDelay)
		if err := launchSessionFn(t.Agent, "", true); err != nil {
			logging.Error("improve: failed to launch improvement session for agent=%q: %v", t.Agent, err)
		}
	}()
}
