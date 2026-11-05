package hooks

import (
	"testing"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/notify"
)

func TestDispatchAlert(t *testing.T) {
	def := config.DefaultAgentConfig()
	off := def
	off.Alerts = false
	noBlocked := def
	noBlocked.AlertConfig.Blocked = false
	noIdle := def
	noIdle.AlertConfig.Idle = false

	types := []string{"permission_prompt", "idle_prompt", "stop_failure", "post_compact"}
	tests := []struct {
		name string
		ac   config.AgentConfig
		want map[string]bool
	}{
		{"alerts off", off, map[string]bool{}},
		{"defaults", def, map[string]bool{"permission_prompt": true, "idle_prompt": true, "stop_failure": true, "post_compact": true}},
		{"blocked off", noBlocked, map[string]bool{"idle_prompt": true, "stop_failure": true, "post_compact": true}},
		{"idle off", noIdle, map[string]bool{"permission_prompt": true, "stop_failure": true, "post_compact": true}},
	}
	for _, tt := range tests {
		for _, typ := range types {
			t.Run(tt.name+"/"+typ, func(t *testing.T) {
				sent := false
				dispatchAlert(tt.ac, typ, "a", notify.Options{}, func(notify.Options) { sent = true })
				if sent != tt.want[typ] {
					t.Errorf("sent=%v want %v", sent, tt.want[typ])
				}
			})
		}
	}
}

func TestShouldAlert_ZeroAlertConfigFilledByForAgent(t *testing.T) {
	cfg := &config.Config{Agents: map[string]config.AgentConfig{"a": {Alerts: true}}}
	ac := cfg.ForAgent("a")
	for _, typ := range []string{"permission_prompt", "idle_prompt"} {
		if !shouldAlert(ac, typ) {
			t.Errorf("%s suppressed with defaulted AlertConfig", typ)
		}
	}
}
