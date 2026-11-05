package hooks

import (
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/notify"
)

// hookNotify sends a desktop notification using the configured notify_cmd.
// It is a thin wrapper around internal/notify.NotifyWithOptions -- the
// command-substitution logic used to live here, but internal/cmd needs to
// fire the same notification (see RunSend's --wait-reply gating in
// internal/cmd/send.go), so it was extracted to internal/notify rather than
// copy-pasted. It does NOT consult per-agent alert settings; hook call sites
// must go through sendAlert.
func hookNotify(opts notify.Options) {
	notify.NotifyWithOptions(opts)
}

// shouldAlert reports whether a notification of the given type should be sent
// for an agent with configuration ac.
//
//   - Alerts (the master switch) false suppresses everything.
//   - "permission_prompt" additionally requires AlertConfig.Blocked.
//   - "idle_prompt" additionally requires AlertConfig.Idle.
//   - Any other type (stop_failure, post_compact) is gated on the master
//     switch only: there is no dedicated toggle for them. AlertConfig.Gone has
//     no hook-driven notification and is intentionally not consulted.
func shouldAlert(ac config.AgentConfig, notificationType string) bool {
	if !ac.Alerts {
		return false
	}
	switch notificationType {
	case "permission_prompt":
		return ac.AlertConfig.Blocked
	case "idle_prompt":
		return ac.AlertConfig.Idle
	default:
		return true
	}
}

// sendAlert is the single gate every hook notification goes through. It
// resolves the agent's config (project overlaid on global, defaults applied),
// and calls send only if shouldAlert allows it. State writes must happen
// before/independently of this call -- only the notification is suppressed.
func sendAlert(agentName, notificationType string, opts notify.Options) {
	var ac config.AgentConfig
	if deps.LoadConfig != nil {
		if cfg := deps.LoadConfig(); cfg != nil {
			ac = cfg.ForAgent(agentName)
		} else {
			ac = config.DefaultAgentConfig()
		}
	} else {
		ac = config.DefaultAgentConfig()
	}
	dispatchAlert(ac, notificationType, agentName, opts, deps.Notify)
}

// dispatchAlert is the testable core of sendAlert with config and sender injected.
func dispatchAlert(ac config.AgentConfig, notificationType, agentName string, opts notify.Options, send func(notify.Options)) {
	if !shouldAlert(ac, notificationType) {
		logging.Log("hook-notify: alert suppressed by config type=%s agent=%s", notificationType, agentName)
		return
	}
	send(opts)
}
