package config

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

const serveGlobal = `{
	"serve": {
		"listen": ":9443",
		"hostnames": ["agentctl.home"],
		"allow_cidrs": ["192.168.1.0/24"],
		"allow_write": true,
		"keep_awake": false,
		"push": {"blocked": false, "done": true, "blocked_reminder_minutes": 0, "quiet_hours": "23:00-07:00", "muted_projects": ["x"]},
		"ntfy_topic": "topic"
	}
}`

func TestServeGlobalParses(t *testing.T) {
	cfg := loadWith(t, serveGlobal, `{}`)
	s := cfg.Serve
	if s.EffectiveListen() != ":9443" || !s.AllowWrite || s.KeepAwakeEnabled() || s.NtfyTopic != "topic" {
		t.Errorf("unexpected serve config %+v", s)
	}
	if !reflect.DeepEqual(s.Hostnames, []string{"agentctl.home"}) || !reflect.DeepEqual(s.EffectiveAllowCIDRs(), []string{"192.168.1.0/24"}) {
		t.Errorf("hostnames/cidrs = %v %v", s.Hostnames, s.AllowCIDRs)
	}
	p := s.Push
	if p.BlockedEnabled() || !p.DoneEnabled() || p.EffectiveBlockedReminderMinutes() != 0 || p.QuietHours != "23:00-07:00" || len(p.MutedProjects) != 1 {
		t.Errorf("unexpected push config %+v", p)
	}
}

func TestServeDefaults(t *testing.T) {
	var s ServeConfig
	if s.EffectiveListen() != DefaultServeListen {
		t.Errorf("listen = %q", s.EffectiveListen())
	}
	if !reflect.DeepEqual(s.EffectiveAllowCIDRs(), DefaultServeAllowCIDRs) {
		t.Errorf("cidrs = %v", s.EffectiveAllowCIDRs())
	}
	got := s.EffectiveAllowCIDRs()
	got[0] = "mutated"
	if DefaultServeAllowCIDRs[0] == "mutated" {
		t.Error("EffectiveAllowCIDRs returned the shared default slice")
	}
	if s.AllowWrite || !s.KeepAwakeEnabled() || !s.Push.BlockedEnabled() || !s.Push.DoneEnabled() {
		t.Errorf("unexpected defaults %+v", s)
	}
	if got := s.Push.EffectiveBlockedReminderMinutes(); got != 10 {
		t.Errorf("reminder = %d, want 10", got)
	}
	neg := -5
	if got := (ServePushConfig{BlockedReminderMinutes: &neg}).EffectiveBlockedReminderMinutes(); got != 0 {
		t.Errorf("negative reminder = %d, want 0", got)
	}
}

func TestProjectServeIgnored(t *testing.T) {
	tests := []struct {
		name    string
		global  string
		project string
		want    ServeConfig
	}{
		{
			name:    "project cannot open write or cidrs",
			global:  `{}`,
			project: `{"serve":{"allow_write":true,"allow_cidrs":["0.0.0.0/0"]}}`,
			want:    ServeConfig{},
		},
		{
			name:    "project cannot override global",
			global:  `{"serve":{"listen":":9443"}}`,
			project: `{"serve":{"listen":":1","hostnames":["evil"],"ntfy_topic":"leak"}}`,
			want:    ServeConfig{Listen: ":9443"},
		},
		{
			name:    "project push block ignored",
			global:  `{}`,
			project: `{"serve":{"push":{"quiet_hours":"00:00-23:59"}}}`,
			want:    ServeConfig{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := loadWith(t, tt.global, tt.project)
			if !reflect.DeepEqual(cfg.Serve, tt.want) {
				t.Errorf("Serve = %+v, want %+v", cfg.Serve, tt.want)
			}
			if !warnedContaining(`"serve"`) {
				t.Error("expected a global-only warning for serve")
			}
		})
	}
}

func TestProjectWithoutServeDoesNotWarn(t *testing.T) {
	loadWith(t, serveGlobal, `{"theme":"x"}`)
	if warnedContaining(`"serve"`) {
		t.Error("unexpected serve warning")
	}
}

func TestSaveDoesNotWriteServeToProject(t *testing.T) {
	cfg := loadWith(t, serveGlobal, `{}`)
	cfg.Theme = "changed"
	cfg.Serve.Listen = ":1"
	cfg.Serve.AllowWrite = false
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"serve"`) {
		t.Errorf("project layer gained serve: %s", data)
	}
}

func TestServeOmittedWhenZero(t *testing.T) {
	data, err := json.Marshal(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "serve") {
		t.Errorf("zero serve block serialized: %s", data)
	}
}
