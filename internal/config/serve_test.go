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
	if !s.Push.ArtifactsEnabled() {
		t.Error("artifact pushes should default on")
	}
	if (ServePushConfig{Artifacts: new(false)}).ArtifactsEnabled() {
		t.Error("artifacts: false should disable artifact pushes")
	}
	if got := s.Push.EffectiveBlockedReminderMinutes(); got != 10 {
		t.Errorf("reminder = %d, want 10", got)
	}
	neg := -5
	if got := (ServePushConfig{BlockedReminderMinutes: &neg}).EffectiveBlockedReminderMinutes(); got != 0 {
		t.Errorf("negative reminder = %d, want 0", got)
	}
}

func TestServeLaptopQuiet(t *testing.T) {
	tests := []struct {
		name      string
		push      ServePushConfig
		wantQuiet bool
		wantIdle  int
	}{
		{"defaults", ServePushConfig{}, true, 120},
		{"off", ServePushConfig{QuietWhenLaptopActive: new(false)}, false, 120},
		{"custom idle", ServePushConfig{LaptopIdleSeconds: new(300)}, true, 300},
		{"zero idle is default", ServePushConfig{LaptopIdleSeconds: new(0)}, true, 120},
		{"negative idle is default", ServePushConfig{LaptopIdleSeconds: new(-1)}, true, 120},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.push.QuietWhenLaptopActiveEnabled(); got != tt.wantQuiet {
				t.Errorf("QuietWhenLaptopActiveEnabled = %v, want %v", got, tt.wantQuiet)
			}
			if got := tt.push.EffectiveLaptopIdleSeconds(); got != tt.wantIdle {
				t.Errorf("EffectiveLaptopIdleSeconds = %d, want %d", got, tt.wantIdle)
			}
		})
	}
	cfg := loadWith(t, `{"serve":{"push":{"quiet_when_laptop_active":false,"laptop_idle_seconds":45}}}`, `{}`)
	if cfg.Serve.Push.QuietWhenLaptopActiveEnabled() || cfg.Serve.Push.EffectiveLaptopIdleSeconds() != 45 {
		t.Errorf("parsed push = %+v", cfg.Serve.Push)
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
		{
			name:    "project cannot switch laptop quiet",
			global:  `{"serve":{"push":{"quiet_when_laptop_active":false}}}`,
			project: `{"serve":{"push":{"quiet_when_laptop_active":true,"laptop_idle_seconds":1}}}`,
			want:    ServeConfig{Push: ServePushConfig{QuietWhenLaptopActive: new(false)}},
		},
		{
			name:    "project cannot switch artifact pushes",
			global:  `{"serve":{"push":{"artifacts":false}}}`,
			project: `{"serve":{"push":{"artifacts":true}}}`,
			want:    ServeConfig{Push: ServePushConfig{Artifacts: new(false)}},
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

func TestServePublicHostnameParses(t *testing.T) {
	cfg := loadWith(t, `{"serve":{"public_hostname":"Mac.Example.COM.","acme":{"email":"me@example.com","staging":true}}}`, `{}`)
	h, err := cfg.Serve.EffectivePublicHostname()
	if err != nil || h != "mac.example.com" {
		t.Fatalf("EffectivePublicHostname = %q, %v", h, err)
	}
	if cfg.Serve.ACME.Email != "me@example.com" || !cfg.Serve.ACME.Staging {
		t.Errorf("acme = %+v", cfg.Serve.ACME)
	}
}

func TestProjectCannotSetPublicHostname(t *testing.T) {
	cfg := loadWith(t, `{}`, `{"serve":{"public_hostname":"evil.example.com","acme":{"staging":true}}}`)
	if !reflect.DeepEqual(cfg.Serve, ServeConfig{}) {
		t.Errorf("Serve = %+v, want zero", cfg.Serve)
	}
}

func TestEffectivePublicHostname(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "", want: ""},
		{in: "   ", want: ""},
		{in: "macbookpro.agentctl.blsmth.com", want: "macbookpro.agentctl.blsmth.com"},
		{in: " MacBookPro.Agentctl.Blsmth.com. ", want: "macbookpro.agentctl.blsmth.com"},
		{in: "a-b.example.io", want: "a-b.example.io"},
		{in: "localhost", wantErr: true},
		{in: "blsmth.com", wantErr: true},
		{in: "agentctl.blsmth.com.", want: "agentctl.blsmth.com"},
		{in: "192.168.1.10", wantErr: true},
		{in: "::1", wantErr: true},
		{in: "*.example.com", wantErr: true},
		{in: "-bad.example.com", wantErr: true},
		{in: "bad-.example.com", wantErr: true},
		{in: "a..example.com", wantErr: true},
		{in: "under_score.example.com", wantErr: true},
		{in: "host.example.com:8443", wantErr: true},
		{in: "https://host.example.com", wantErr: true},
		{in: "1.2.3.4.5", wantErr: true},
		{in: strings.Repeat("a", 64) + ".example.com", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ServeConfig{PublicHostname: tt.in}.EffectivePublicHostname()
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
