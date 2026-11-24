package hooks

import (
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/notify"
)

// hookNotify sends a desktop notification using the configured notify_cmd.
// It is a thin wrapper around internal/notify.NotifyWithOptions, which owns
// the command-substitution logic. It does NOT consult per-agent alert settings; hook call sites
// must go through sendAlert.
func hookNotify(opts notify.Options) {
	notify.NotifyWithOptions(opts)
}

// shouldAlert reports whether a notification of the given type should be sent
// for an agent with configuration ac.
//
//   - Alerts (the master switch) false suppresses everything.
//   - "permission_prompt", "approval_pending" (an agentctl approval popup) and
//     "trust_prompt" (the folder-trust dialog, see LaunchWatch) additionally
//     require AlertConfig.Blocked.
//   - "idle_prompt" additionally requires AlertConfig.Idle.
//   - Any other type (stop_failure, post_compact) is gated on the master
//     switch only: there is no dedicated toggle for them. AlertConfig.Gone has
//     no hook-driven notification and is intentionally not consulted.
func shouldAlert(ac config.AgentConfig, notificationType string) bool {
	if !ac.Alerts {
		return false
	}
	switch notificationType {
	case "permission_prompt", approvalNotificationType, trustNotificationType, attentionNotificationType:
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
// It reports whether an alert was actually sent.
func sendAlert(agentName, notificationType string, opts notify.Options) bool {
	return sendAlertWith(*deps, agentName, notificationType, opts)
}

// sendAlertWith is sendAlert against an explicit Deps snapshot, so a caller
// that sends from a goroutine does not read the package-level deps var.
func sendAlertWith(d Deps, agentName, notificationType string, opts notify.Options) bool {
	var ac config.AgentConfig
	if d.LoadConfig != nil {
		if cfg := d.LoadConfig(); cfg != nil {
			ac = cfg.ForAgent(agentName)
		} else {
			ac = config.DefaultAgentConfig()
		}
	} else {
		ac = config.DefaultAgentConfig()
	}
	return dispatchAlert(ac, notificationType, agentName, opts, d.Notify)
}

// dispatchAlert is the testable core of sendAlert with config and sender injected.
func dispatchAlert(ac config.AgentConfig, notificationType, agentName string, opts notify.Options, send func(notify.Options)) bool {
	if !shouldAlert(ac, notificationType) {
		logging.Log("hook-notify: alert suppressed by config type=%s agent=%s", notificationType, agentName)
		return false
	}
	send(opts)
	return true
}
