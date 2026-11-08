package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/tokens"
)

// ---------------------------------------------------------------------------
// EffectiveHooks
// ---------------------------------------------------------------------------

func TestEffectiveHooks(t *testing.T) {
	tests := []struct {
		name           string
		globalHooks    HooksConfig
		agentHooks     HooksConfig
		agentDispatch  *DispatchConfig
		globalDispatch DispatchConfig
		event          string
		want           []string
	}{
		{
			name:        "global hooks only",
			globalHooks: HooksConfig{"session_start": {"echo global"}},
			event:       "session_start",
			want:        []string{"echo global"},
		},
		{
			name:       "agent hooks only",
			agentHooks: HooksConfig{"session_start": {"echo agent"}},
			event:      "session_start",
			want:       []string{"echo agent"},
		},
		{
			name:        "global then agent merged",
			globalHooks: HooksConfig{"session_start": {"echo global"}},
			agentHooks:  HooksConfig{"session_start": {"echo agent"}},
			event:       "session_start",
			want:        []string{"echo global", "echo agent"},
		},
		{
			name:  "empty — no hooks configured",
			event: "session_start",
			want:  nil,
		},
		{
			name:        "event not present in map",
			globalHooks: HooksConfig{"other_event": {"echo other"}},
			event:       "session_start",
			want:        nil,
		},
		{
			name:           "dispatch_complete backward compat global on_complete",
			globalDispatch: DispatchConfig{OnComplete: "echo legacy"},
			event:          "dispatch_complete",
			want:           []string{"echo legacy"},
		},
		{
			name:          "dispatch_complete backward compat agent on_complete",
			agentDispatch: &DispatchConfig{OnComplete: "echo agent-legacy"},
			event:         "dispatch_complete",
			want:          []string{"echo agent-legacy"},
		},
		{
			name:           "dispatch_complete hooks.dispatch_complete takes precedence over on_complete",
			globalHooks:    HooksConfig{"dispatch_complete": {"echo hooks"}},
			globalDispatch: DispatchConfig{OnComplete: "echo legacy"},
			event:          "dispatch_complete",
			want:           []string{"echo hooks"},
		},
		{
			name:  "dispatch_complete empty hooks + empty on_complete returns nil",
			event: "dispatch_complete",
			want:  nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				Hooks:    tc.globalHooks,
				Dispatch: tc.globalDispatch,
				Agents:   map[string]AgentConfig{},
			}
			agentCfg := AgentConfig{
				Hooks:    tc.agentHooks,
				Dispatch: tc.agentDispatch,
			}
			got := cfg.EffectiveHooks(tc.event, agentCfg)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("EffectiveHooks(%q) = %v, want %v", tc.event, got, tc.want)
			}
		})
	}
}

func TestDefaultAgentConfig(t *testing.T) {
	d := DefaultAgentConfig()
	if !d.Alerts {
		t.Error("DefaultAgentConfig().Alerts = false, want true")
	}
	if d.AutoImprove {
		t.Error("DefaultAgentConfig().AutoImprove = true, want false")
	}
	if !d.CrossContext {
		t.Error("DefaultAgentConfig().CrossContext = false, want true")
	}
}

func TestForAgent(t *testing.T) {
	cfg := &Config{
		Agents: map[string]AgentConfig{
			"known": {Alerts: false, AutoImprove: true, CrossContext: false},
		},
	}

	t.Run("known agent", func(t *testing.T) {
		got := cfg.ForAgent("known")
		if got.Alerts != false || got.AutoImprove != true || got.CrossContext != false {
			t.Errorf("ForAgent('known') = %+v, want {false, true, false}", got)
		}
	})

	t.Run("unknown agent returns defaults", func(t *testing.T) {
		got := cfg.ForAgent("unknown")
		want := DefaultAgentConfig()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("ForAgent('unknown') = %+v, want %+v", got, want)
		}
	})
}

func TestAgentNames(t *testing.T) {
	cfg := &Config{
		Agents: map[string]AgentConfig{
			"alpha": {Alerts: true},
			"beta":  {Alerts: false},
		},
	}
	names := cfg.AgentNames()
	if len(names) != 2 {
		t.Fatalf("AgentNames() returned %d names, want 2", len(names))
	}
	got := make(map[string]bool)
	for _, n := range names {
		got[n] = true
	}
	if !got["alpha"] || !got["beta"] {
		t.Errorf("AgentNames() = %v, want [alpha, beta]", names)
	}

	// Empty config.
	empty := &Config{Agents: map[string]AgentConfig{}}
	if len(empty.AgentNames()) != 0 {
		t.Errorf("AgentNames() on empty config returned %d, want 0", len(empty.AgentNames()))
	}
}

func TestLoadSaveRoundtrip(t *testing.T) {
	dir := t.TempDir()
	SetConfigPathForTest(filepath.Join(dir, "config.json"))

	// Load from non-existent file should return empty config
	cfg := Load()
	if len(cfg.Agents) != 0 {
		t.Errorf("Load() from empty returned %d agents, want 0", len(cfg.Agents))
	}

	// Save and reload
	cfg.Agents["test-agent"] = AgentConfig{
		Alerts:       false,
		AutoImprove:  true,
		CrossContext: true,
	}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	cfg2 := Load()
	got := cfg2.ForAgent("test-agent")
	if got.Alerts != false || got.AutoImprove != true || got.CrossContext != true {
		t.Errorf("roundtrip ForAgent('test-agent') = %+v", got)
	}
}

func TestToggleAlert(t *testing.T) {
	dir := t.TempDir()
	SetConfigPathForTest(filepath.Join(dir, "config.json"))

	cfg := Load()
	// Default is alerts=true, toggling should give false
	val, err := cfg.ToggleAlert("test-agent")
	if err != nil {
		t.Fatalf("ToggleAlert error: %v", err)
	}
	if val != false {
		t.Errorf("ToggleAlert returned %v, want false (toggled from default true)", val)
	}

	// Reload and verify persisted
	cfg2 := Load()
	if cfg2.ForAgent("test-agent").Alerts != false {
		t.Error("Alerts not persisted after toggle")
	}
}

func TestToggleAutoImprove(t *testing.T) {
	dir := t.TempDir()
	SetConfigPathForTest(filepath.Join(dir, "config.json"))

	cfg := Load()
	// Default is auto_improve=false, toggling should give true
	val, err := cfg.ToggleAutoImprove("test-agent")
	if err != nil {
		t.Fatalf("ToggleAutoImprove error: %v", err)
	}
	if val != true {
		t.Errorf("ToggleAutoImprove returned %v, want true", val)
	}

	cfg2 := Load()
	if cfg2.ForAgent("test-agent").AutoImprove != true {
		t.Error("AutoImprove not persisted after toggle")
	}
}

func TestToggleAutoAccept(t *testing.T) {
	dir := t.TempDir()
	SetConfigPathForTest(filepath.Join(dir, "config.json"))

	cfg := Load()
	// Default is auto_accept=false, toggling should give true
	val, err := cfg.ToggleAutoAccept("test-agent")
	if err != nil {
		t.Fatalf("ToggleAutoAccept error: %v", err)
	}
	if val != true {
		t.Errorf("ToggleAutoAccept returned %v, want true", val)
	}

	cfg2 := Load()
	if cfg2.ForAgent("test-agent").AutoAccept != true {
		t.Error("AutoAccept not persisted after toggle")
	}
}

func TestToggleCrossContext(t *testing.T) {
	dir := t.TempDir()
	SetConfigPathForTest(filepath.Join(dir, "config.json"))

	cfg := Load()
	// Default is cross_context=true, toggling should give false
	val, err := cfg.ToggleCrossContext("test-agent")
	if err != nil {
		t.Fatalf("ToggleCrossContext error: %v", err)
	}
	if val != false {
		t.Errorf("ToggleCrossContext returned %v, want false", val)
	}

	cfg2 := Load()
	if cfg2.ForAgent("test-agent").CrossContext != false {
		t.Error("CrossContext not persisted after toggle")
	}
}

func TestEffectiveAutoPollInterval(t *testing.T) {
	tests := []struct {
		name     string
		interval string
		want     time.Duration
	}{
		{
			name:     "empty string defaults to 5m",
			interval: "",
			want:     5 * time.Minute,
		},
		{
			name:     "valid duration string parsed",
			interval: "10m",
			want:     10 * time.Minute,
		},
		{
			name:     "30s interval",
			interval: "30s",
			want:     30 * time.Second,
		},
		{
			name:     "invalid duration string defaults to 5m",
			interval: "not-a-duration",
			want:     5 * time.Minute,
		},
		{
			name:     "1h interval",
			interval: "1h",
			want:     time.Hour,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{AutoPollInterval: tc.interval}
			got := cfg.EffectiveAutoPollInterval()
			if got != tc.want {
				t.Errorf("EffectiveAutoPollInterval() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNotifyCmdRoundtrip(t *testing.T) {
	dir := t.TempDir()
	SetConfigPathForTest(filepath.Join(dir, "config.json"))

	const cmd = "alerter --title {{title}} --message {{message}} --sound default --timeout 10"

	cfg := Load()
	cfg.NotifyCmd = StringPtr(cmd)
	if err := Save(cfg); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	cfg2 := Load()
	if got, set := cfg2.EffectiveNotifyCmd(); !set || got != cmd {
		t.Errorf("NotifyCmd roundtrip = %q (set=%v), want %q", got, set, cmd)
	}
}

func TestNotifyCmdOmittedWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	SetConfigPathForTest(filepath.Join(dir, "config.json"))

	cfg := Load()
	// NotifyCmd not set — field should be omitted from JSON (omitempty).
	if err := Save(cfg); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	cfg2 := Load()
	if cfg2.NotifyCmd != nil {
		t.Errorf("NotifyCmd = %q after empty roundtrip, want nil", *cfg2.NotifyCmd)
	}
}

func TestToggleAutoPoll(t *testing.T) {
	dir := t.TempDir()
	SetConfigPathForTest(filepath.Join(dir, "config.json"))

	cfg := Load()
	// Default is auto_poll=false, toggling should give true.
	val, err := cfg.ToggleAutoPoll()
	if err != nil {
		t.Fatalf("ToggleAutoPoll error: %v", err)
	}
	if val != true {
		t.Errorf("ToggleAutoPoll returned %v, want true", val)
	}

	// Reload and verify persisted.
	cfg2 := Load()
	if cfg2.AutoPoll != true {
		t.Error("AutoPoll not persisted after first toggle")
	}

	// Toggle back off.
	val2, err := cfg2.ToggleAutoPoll()
	if err != nil {
		t.Fatalf("ToggleAutoPoll (second) error: %v", err)
	}
	if val2 != false {
		t.Errorf("ToggleAutoPoll (second) returned %v, want false", val2)
	}

	cfg3 := Load()
	if cfg3.AutoPoll != false {
		t.Error("AutoPoll not persisted after second toggle")
	}
}

func TestRateLimitConfigDefaults(t *testing.T) {
	tests := []struct {
		name     string
		tokenCap int
		want     int
	}{
		{"zero defaults to 88000", 0, 88000},
		{"negative defaults to 88000", -1, 88000},
		{"custom value preserved", 44000, 44000},
		{"large value preserved", 200000, 200000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rl := RateLimitConfig{TokenCap: tt.tokenCap}
			got := rl.EffectiveTokenCap()
			if got != tt.want {
				t.Errorf("EffectiveTokenCap() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestRateLimitConfigRoundtrip(t *testing.T) {
	dir := t.TempDir()
	SetConfigPathForTest(filepath.Join(dir, "config.json"))

	cfg := Load()
	cfg.RateLimit = RateLimitConfig{TokenCap: 44000}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	cfg2 := Load()
	if cfg2.RateLimit.TokenCap != 44000 {
		t.Errorf("RateLimit.TokenCap = %d after roundtrip, want 44000", cfg2.RateLimit.TokenCap)
	}
}

func TestRateLimitConfigOmittedWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	SetConfigPathForTest(filepath.Join(dir, "config.json"))

	cfg := Load()
	// RateLimit not set — should be omitted from JSON.
	if err := Save(cfg); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	cfg2 := Load()
	if cfg2.RateLimit.TokenCap != 0 {
		t.Errorf("RateLimit.TokenCap = %d after empty roundtrip, want 0", cfg2.RateLimit.TokenCap)
	}
	// EffectiveTokenCap should still return the default.
	if got := cfg2.RateLimit.EffectiveTokenCap(); got != 88000 {
		t.Errorf("EffectiveTokenCap() = %d, want 88000 (default)", got)
	}
}

// ---------------------------------------------------------------------------
// MetricsConfig
// ---------------------------------------------------------------------------

func TestMetricsConfigEffectivePushInterval(t *testing.T) {
	tests := []struct {
		name     string
		interval string
		want     time.Duration
	}{
		{"empty defaults to 15s", "", 15 * time.Second},
		{"valid duration parsed", "30s", 30 * time.Second},
		{"1m interval", "1m", time.Minute},
		{"invalid defaults to 15s", "not-a-duration", 15 * time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := &MetricsConfig{PushInterval: tc.interval}
			got := m.EffectivePushInterval()
			if got != tc.want {
				t.Errorf("EffectivePushInterval() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMetricsConfigEffectivePort(t *testing.T) {
	tests := []struct {
		name string
		port int
		want int
	}{
		{"zero defaults to 9101", 0, 9101},
		{"custom port preserved", 9200, 9200},
		{"negative defaults to 9101", -1, 9101},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := &MetricsConfig{Port: tc.port}
			got := m.EffectivePort()
			if got != tc.want {
				t.Errorf("EffectivePort() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestMetricsConfigHasRemoteWrite(t *testing.T) {
	t.Run("empty endpoint returns false", func(t *testing.T) {
		m := &MetricsConfig{}
		if m.HasRemoteWrite() {
			t.Error("HasRemoteWrite() = true, want false when Endpoint is empty")
		}
	})
	t.Run("non-empty endpoint returns true", func(t *testing.T) {
		m := &MetricsConfig{Endpoint: "http://localhost:9009/api/prom/push"}
		if !m.HasRemoteWrite() {
			t.Error("HasRemoteWrite() = false, want true when Endpoint is set")
		}
	})
}

func TestMetricsConfigLegacyFieldsIgnored(t *testing.T) {
	dir := t.TempDir()
	SetConfigPathForTest(filepath.Join(dir, "config.json"))

	// Write raw JSON with the old flat fields; Load should ignore them
	// (unknown fields are silently dropped by json.Unmarshal).
	raw := `{"metrics_enabled":true,"metrics_port":9200,"agents":{}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(raw), 0o644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	cfg := Load()
	if cfg.Metrics.Enabled {
		t.Error("legacy metrics_enabled should not affect Metrics.Enabled")
	}
	if cfg.Metrics.Port != 0 {
		t.Errorf("legacy metrics_port should not affect Metrics.Port, got %d", cfg.Metrics.Port)
	}
}

func TestEffectiveMetricsConfig(t *testing.T) {
	t.Run("returns Metrics struct with defaults via helpers", func(t *testing.T) {
		cfg := &Config{
			Metrics: MetricsConfig{
				Enabled:      true,
				Endpoint:     "http://remote:9009/push",
				PushInterval: "30s",
				Port:         9200,
			},
		}
		m := cfg.EffectiveMetricsConfig()
		if !m.Enabled {
			t.Error("EffectiveMetricsConfig().Enabled = false, want true")
		}
		if m.Endpoint != "http://remote:9009/push" {
			t.Errorf("Endpoint = %q, want http://remote:9009/push", m.Endpoint)
		}
		if m.EffectivePushInterval() != 30*time.Second {
			t.Errorf("EffectivePushInterval() = %v, want 30s", m.EffectivePushInterval())
		}
		if m.EffectivePort() != 9200 {
			t.Errorf("EffectivePort() = %d, want 9200", m.EffectivePort())
		}
		if !m.HasRemoteWrite() {
			t.Error("HasRemoteWrite() = false, want true")
		}
	})

	t.Run("zero config uses defaults", func(t *testing.T) {
		cfg := &Config{}
		m := cfg.EffectiveMetricsConfig()
		if m.EffectivePort() != 9101 {
			t.Errorf("EffectivePort() = %d, want 9101", m.EffectivePort())
		}
		if m.EffectivePushInterval() != 15*time.Second {
			t.Errorf("EffectivePushInterval() = %v, want 15s", m.EffectivePushInterval())
		}
		if m.HasRemoteWrite() {
			t.Error("HasRemoteWrite() = true, want false")
		}
	})
}

func TestMetricsConfigRoundtrip(t *testing.T) {
	dir := t.TempDir()
	SetConfigPathForTest(filepath.Join(dir, "config.json"))

	cfg := Load()
	cfg.Metrics = MetricsConfig{
		Enabled:      true,
		Endpoint:     "http://remote:9009/push",
		PushInterval: "30s",
		Port:         9200,
		Labels:       map[string]string{"instance": "prod"},
		BasicAuth:    &BasicAuthConfig{Username: "user", Password: "pass"},
	}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	cfg2 := Load()
	m := cfg2.Metrics
	if !m.Enabled {
		t.Error("roundtrip: Enabled = false, want true")
	}
	if m.Endpoint != "http://remote:9009/push" {
		t.Errorf("roundtrip: Endpoint = %q", m.Endpoint)
	}
	if m.PushInterval != "30s" {
		t.Errorf("roundtrip: PushInterval = %q", m.PushInterval)
	}
	if m.Port != 9200 {
		t.Errorf("roundtrip: Port = %d", m.Port)
	}
	if m.Labels["instance"] != "prod" {
		t.Errorf("roundtrip: Labels[instance] = %q", m.Labels["instance"])
	}
	if m.BasicAuth == nil {
		t.Fatal("roundtrip: BasicAuth = nil")
	}
	if m.BasicAuth.Username != "user" || m.BasicAuth.Password != "pass" {
		t.Errorf("roundtrip: BasicAuth = %+v", m.BasicAuth)
	}
}

// ---------------------------------------------------------------------------
// Init — project-local config in .agentctl/
// ---------------------------------------------------------------------------

func TestInitUsesProjectLocalPath(t *testing.T) {
	// Create a temp directory named "acme" to simulate a project dir.
	base := t.TempDir()
	projectDir := filepath.Join(base, "acme")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Reset paths so Init can run fresh.
	paths.ResetForTest()
	// AGENTCTL_RUNTIME_DIR is inherited from any agentctl-managed tmux window
	// (agentctl exports it into every window it creates), and paths.Init
	// prefers it over the baseDir argument we pass below. Pin it explicitly
	// so this test resolves paths inside our temp projectDir rather than
	// whatever real project runtime dir happens to be ambient.
	t.Setenv("AGENTCTL_RUNTIME_DIR", "")
	paths.Init(projectDir)
	t.Cleanup(paths.ResetForTest)

	// Reset configPath and globalConfigPath so Init re-derives them.
	configPath = ""
	globalConfigPath = ""
	t.Cleanup(func() { configPath = ""; globalConfigPath = "" })

	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	if err := Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	got := FilePath()
	// FilePath() should now point to <projectDir>/.agentctl/config.json
	wantSuffix := filepath.Join("acme", ".agentctl", "config.json")
	if !strings.HasSuffix(got, wantSuffix) {
		t.Errorf("FilePath() = %q, want path ending with %q", got, wantSuffix)
	}
	// Must not be in the old ~/.config/agentctl/<project>/ location
	if strings.Contains(got, ".config") {
		t.Errorf("FilePath() = %q, must NOT be in ~/.config (old per-project location)", got)
	}
}

func TestInitGlobalFilePathIsUserConfig(t *testing.T) {
	// globalConfigPath should always point to ~/.config/agentctl/config.json.
	base := t.TempDir()
	projectDir := filepath.Join(base, "myproject")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	paths.ResetForTest()
	// See TestInitUsesProjectLocalPath: pin against an inherited
	// AGENTCTL_RUNTIME_DIR so paths.Init actually uses projectDir below.
	t.Setenv("AGENTCTL_RUNTIME_DIR", "")
	paths.Init(projectDir)
	t.Cleanup(paths.ResetForTest)

	configPath = ""
	globalConfigPath = ""
	t.Cleanup(func() { configPath = ""; globalConfigPath = "" })

	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	if err := Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	got := globalConfigPath
	if !strings.HasSuffix(got, filepath.Join(".config", "agentctl", "config.json")) {
		t.Errorf("globalConfigPath = %q, want path ending with .config/agentctl/config.json", got)
	}
}

func TestInitDefaultDirNameUnchanged(t *testing.T) {
	// When launched from a directory named "agentctl", the project-local config
	// should still be in .agentctl/config.json inside that dir.
	base := t.TempDir()
	projectDir := filepath.Join(base, "agentctl")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	paths.ResetForTest()
	// See TestInitUsesProjectLocalPath: pin against an inherited
	// AGENTCTL_RUNTIME_DIR so paths.Init actually uses projectDir below.
	t.Setenv("AGENTCTL_RUNTIME_DIR", "")
	paths.Init(projectDir)
	t.Cleanup(paths.ResetForTest)

	configPath = ""
	globalConfigPath = ""
	t.Cleanup(func() { configPath = ""; globalConfigPath = "" })

	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	if err := Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	got := FilePath()
	wantSuffix := filepath.Join("agentctl", ".agentctl", "config.json")
	if !strings.HasSuffix(got, wantSuffix) {
		t.Errorf("FilePath() = %q, want path ending with %q", got, wantSuffix)
	}
}

// ---------------------------------------------------------------------------
// EffectiveWorkJournal
// ---------------------------------------------------------------------------

func TestEffectiveWorkJournal(t *testing.T) {
	tests := []struct {
		name     string
		globalWJ bool
		agentWJ  bool
		agentSet bool // whether agent exists in config
		want     bool
	}{
		{"global true, no agent", true, false, false, true},
		{"global true, agent true", true, true, true, true},
		{"global false, agent true", false, true, true, true},
		{"global false, agent false", false, false, true, false},
		{"global false, no agent", false, false, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{WorkJournal: tc.globalWJ}
			if tc.agentSet {
				cfg.Agents = map[string]AgentConfig{
					"test": {WorkJournal: tc.agentWJ},
				}
			}
			if got := cfg.EffectiveWorkJournal("test"); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// EffectiveAutoReclaimWorktree
// ---------------------------------------------------------------------------

func TestEffectiveAutoReclaimWorktree(t *testing.T) {
	trueVal := true
	falseVal := false

	tests := []struct {
		name     string
		agentSet bool
		val      *bool
		want     bool
	}{
		{"agent absent defaults to true", false, nil, true},
		{"agent present, field unset defaults to true", true, nil, true},
		{"agent present, explicit true", true, &trueVal, true},
		{"agent present, explicit false is honored", true, &falseVal, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Agents: map[string]AgentConfig{}}
			if tc.agentSet {
				cfg.Agents["test"] = AgentConfig{AutoReclaimWorktree: tc.val}
			}
			if got := cfg.EffectiveAutoReclaimWorktree("test"); got != tc.want {
				t.Errorf("EffectiveAutoReclaimWorktree() = %v, want %v", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// mergeConfig
// ---------------------------------------------------------------------------

func TestMergeConfig(t *testing.T) {
	t.Run("project overlay takes precedence over global", func(t *testing.T) {
		base := &Config{
			DefaultAgent: "global-agent",
			Theme:        "dark",
			Agents:       map[string]AgentConfig{},
		}
		overlay := &Config{
			DefaultAgent: "project-agent",
			Agents:       map[string]AgentConfig{},
		}
		mergeConfig(base, overlay)
		if base.DefaultAgent != "project-agent" {
			t.Errorf("DefaultAgent = %q, want project-agent", base.DefaultAgent)
		}
		// Theme not set in overlay — global value preserved.
		if base.Theme != "dark" {
			t.Errorf("Theme = %q, want dark (global preserved)", base.Theme)
		}
	})

	t.Run("zero values in overlay do not clobber global", func(t *testing.T) {
		base := &Config{
			DefaultAgent:         "global-agent",
			Theme:                "dark",
			AutoPoll:             true,
			SessionRetentionDays: 30,
			Agents:               map[string]AgentConfig{},
		}
		overlay := &Config{
			Agents: map[string]AgentConfig{},
		}
		mergeConfig(base, overlay)
		if base.DefaultAgent != "global-agent" {
			t.Errorf("DefaultAgent = %q, want global-agent", base.DefaultAgent)
		}
		if base.Theme != "dark" {
			t.Errorf("Theme = %q, want dark", base.Theme)
		}
		if !base.AutoPoll {
			t.Error("AutoPoll = false, want true (global preserved)")
		}
		if base.SessionRetentionDays != 30 {
			t.Errorf("SessionRetentionDays = %d, want 30", base.SessionRetentionDays)
		}
	})

	t.Run("project agents override global per-agent", func(t *testing.T) {
		base := &Config{
			Agents: map[string]AgentConfig{
				"go-engineer": {Model: "sonnet", AutoImprove: false},
				"py-engineer": {Model: "haiku"},
			},
		}
		overlay := &Config{
			Agents: map[string]AgentConfig{
				"go-engineer": {Model: "opus", AutoImprove: true},
			},
		}
		mergeConfig(base, overlay)
		if base.Agents["go-engineer"].Model != "opus" {
			t.Errorf("go-engineer.Model = %q, want opus", base.Agents["go-engineer"].Model)
		}
		if !base.Agents["go-engineer"].AutoImprove {
			t.Error("go-engineer.AutoImprove = false, want true")
		}
		// py-engineer not in overlay — global value preserved.
		if base.Agents["py-engineer"].Model != "haiku" {
			t.Errorf("py-engineer.Model = %q, want haiku", base.Agents["py-engineer"].Model)
		}
	})

	t.Run("metrics overlay when enabled", func(t *testing.T) {
		base := &Config{
			Metrics: MetricsConfig{Port: 9101},
			Agents:  map[string]AgentConfig{},
		}
		overlay := &Config{
			Metrics: MetricsConfig{Enabled: true, Port: 9200},
			Agents:  map[string]AgentConfig{},
		}
		mergeConfig(base, overlay)
		if base.Metrics.Port != 9200 {
			t.Errorf("Metrics.Port = %d, want 9200", base.Metrics.Port)
		}
		if !base.Metrics.Enabled {
			t.Error("Metrics.Enabled = false, want true")
		}
	})

	t.Run("metrics not overlaid when disabled in overlay", func(t *testing.T) {
		base := &Config{
			Metrics: MetricsConfig{Enabled: true, Port: 9101},
			Agents:  map[string]AgentConfig{},
		}
		overlay := &Config{
			Metrics: MetricsConfig{Enabled: false, Port: 9999},
			Agents:  map[string]AgentConfig{},
		}
		mergeConfig(base, overlay)
		// overlay.Metrics.Enabled=false — global metrics preserved.
		if !base.Metrics.Enabled {
			t.Error("Metrics.Enabled = false, want true (global preserved)")
		}
		if base.Metrics.Port != 9101 {
			t.Errorf("Metrics.Port = %d, want 9101 (global preserved)", base.Metrics.Port)
		}
	})

	t.Run("hooks overlay replaces entirely when non-empty", func(t *testing.T) {
		base := &Config{
			Hooks:  HooksConfig{"session_start": {"echo global"}},
			Agents: map[string]AgentConfig{},
		}
		overlay := &Config{
			Hooks:  HooksConfig{"session_end": {"echo project"}},
			Agents: map[string]AgentConfig{},
		}
		mergeConfig(base, overlay)
		if len(base.Hooks["session_start"]) != 0 {
			t.Errorf("session_start hooks = %v, want empty (replaced by overlay)", base.Hooks["session_start"])
		}
		if base.Hooks["session_end"][0] != "echo project" {
			t.Errorf("session_end hooks = %v, want [echo project]", base.Hooks["session_end"])
		}
	})

	t.Run("rate_limit overlay when non-zero", func(t *testing.T) {
		base := &Config{
			RateLimit: RateLimitConfig{TokenCap: 88000},
			Agents:    map[string]AgentConfig{},
		}
		overlay := &Config{
			RateLimit: RateLimitConfig{TokenCap: 44000},
			Agents:    map[string]AgentConfig{},
		}
		mergeConfig(base, overlay)
		if base.RateLimit.TokenCap != 44000 {
			t.Errorf("RateLimit.TokenCap = %d, want 44000", base.RateLimit.TokenCap)
		}
	})
}

// ---------------------------------------------------------------------------
// Load with global + project configs
// ---------------------------------------------------------------------------

// setTestPaths is a helper that overrides both config paths for testing and
// registers a cleanup to restore empty strings.
func setTestPaths(t *testing.T, projectPath, globalPath string) {
	t.Helper()
	configPath = projectPath
	globalConfigPath = globalPath
	t.Cleanup(func() {
		configPath = ""
		globalConfigPath = ""
	})
}

func TestLoadMergesGlobalAndProject(t *testing.T) {
	dir := t.TempDir()
	globalPath := filepath.Join(dir, "global", "config.json")
	projectPath := filepath.Join(dir, "project", "config.json")

	// Write global config with theme and a go-engineer agent.
	if err := os.MkdirAll(filepath.Dir(globalPath), 0o755); err != nil {
		t.Fatal(err)
	}
	globalData := `{
		"theme": "catppuccin-mocha",
		"default_agent": "go-engineer",
		"agents": {
			"go-engineer": {"model": "sonnet", "alerts": true}
		}
	}`
	if err := os.WriteFile(globalPath, []byte(globalData), 0o644); err != nil {
		t.Fatal(err)
	}

	// Write project config that overrides theme and go-engineer model.
	if err := os.MkdirAll(filepath.Dir(projectPath), 0o755); err != nil {
		t.Fatal(err)
	}
	projectData := `{
		"theme": "nord",
		"agents": {
			"go-engineer": {"model": "opus"}
		}
	}`
	if err := os.WriteFile(projectPath, []byte(projectData), 0o644); err != nil {
		t.Fatal(err)
	}

	setTestPaths(t, projectPath, globalPath)

	cfg := Load()

	// Project overrides global.
	if cfg.Theme != "nord" {
		t.Errorf("Theme = %q, want nord (project override)", cfg.Theme)
	}
	// Global value not overridden by project.
	if cfg.DefaultAgent != "go-engineer" {
		t.Errorf("DefaultAgent = %q, want go-engineer (global)", cfg.DefaultAgent)
	}
	// Project agent fully replaces global agent entry.
	if cfg.Agents["go-engineer"].Model != "opus" {
		t.Errorf("go-engineer.Model = %q, want opus (project override)", cfg.Agents["go-engineer"].Model)
	}
}

func TestLoadWithOnlyGlobalConfig(t *testing.T) {
	dir := t.TempDir()
	globalPath := filepath.Join(dir, "global", "config.json")
	projectPath := filepath.Join(dir, "project", "config.json") // does not exist

	if err := os.MkdirAll(filepath.Dir(globalPath), 0o755); err != nil {
		t.Fatal(err)
	}
	globalData := `{
		"theme": "catppuccin-mocha",
		"default_agent": "go-engineer"
	}`
	if err := os.WriteFile(globalPath, []byte(globalData), 0o644); err != nil {
		t.Fatal(err)
	}

	setTestPaths(t, projectPath, globalPath)

	cfg := Load()

	if cfg.Theme != "catppuccin-mocha" {
		t.Errorf("Theme = %q, want catppuccin-mocha (global)", cfg.Theme)
	}
	if cfg.DefaultAgent != "go-engineer" {
		t.Errorf("DefaultAgent = %q, want go-engineer (global)", cfg.DefaultAgent)
	}
}

func TestLoadWithOnlyProjectConfig(t *testing.T) {
	dir := t.TempDir()
	globalPath := filepath.Join(dir, "global", "config.json") // does not exist
	projectPath := filepath.Join(dir, "project", "config.json")

	if err := os.MkdirAll(filepath.Dir(projectPath), 0o755); err != nil {
		t.Fatal(err)
	}
	projectData := `{
		"theme": "nord",
		"agents": {"go-engineer": {"model": "opus"}}
	}`
	if err := os.WriteFile(projectPath, []byte(projectData), 0o644); err != nil {
		t.Fatal(err)
	}

	setTestPaths(t, projectPath, globalPath)

	cfg := Load()

	if cfg.Theme != "nord" {
		t.Errorf("Theme = %q, want nord (project)", cfg.Theme)
	}
	if cfg.Agents["go-engineer"].Model != "opus" {
		t.Errorf("go-engineer.Model = %q, want opus", cfg.Agents["go-engineer"].Model)
	}
}

// ---------------------------------------------------------------------------
// FilePath and GlobalFilePath
// ---------------------------------------------------------------------------

func TestFilePathAndGlobalFilePath(t *testing.T) {
	dir := t.TempDir()
	projectPath := filepath.Join(dir, ".agentctl", "config.json")
	globalPath := filepath.Join(dir, ".config", "agentctl", "config.json")

	setTestPaths(t, projectPath, globalPath)

	if got := FilePath(); got != projectPath {
		t.Errorf("FilePath() = %q, want %q", got, projectPath)
	}
	if got := globalConfigPath; got != globalPath {
		t.Errorf("globalConfigPath = %q, want %q", got, globalPath)
	}
}

// ---------------------------------------------------------------------------
// Models config
// ---------------------------------------------------------------------------

func resetTokenOverrides(t *testing.T) {
	t.Helper()
	tokens.SetOverrides(nil, 0)
	t.Cleanup(func() { tokens.SetOverrides(nil, 0) })
}

func TestMergeConfig_ModelsOverridesMergePerKey(t *testing.T) {
	base := &Config{
		Agents: map[string]AgentConfig{},
		Models: ModelsConfig{
			Overrides: map[string]ModelOverride{
				"claude-sonnet-4": {ContextWindow: 111},
				"claude-opus-4":   {ContextWindow: 222},
			},
		},
	}
	overlay := &Config{
		Agents: map[string]AgentConfig{},
		Models: ModelsConfig{
			Overrides: map[string]ModelOverride{
				"claude-sonnet-4": {ContextWindow: 999}, // local wins for this key
				"claude-opus-5":   {ContextWindow: 333}, // new key added
			},
		},
	}

	mergeConfig(base, overlay)

	if base.Models.Overrides["claude-sonnet-4"].ContextWindow != 999 {
		t.Errorf("claude-sonnet-4 ContextWindow = %d, want 999 (local wins)",
			base.Models.Overrides["claude-sonnet-4"].ContextWindow)
	}
	if base.Models.Overrides["claude-opus-4"].ContextWindow != 222 {
		t.Errorf("claude-opus-4 ContextWindow = %d, want 222 (untouched global key preserved)",
			base.Models.Overrides["claude-opus-4"].ContextWindow)
	}
	if base.Models.Overrides["claude-opus-5"].ContextWindow != 333 {
		t.Errorf("claude-opus-5 ContextWindow = %d, want 333 (new key merged in)",
			base.Models.Overrides["claude-opus-5"].ContextWindow)
	}
}

func TestMergeConfig_ModelsScalarFieldsOverlay(t *testing.T) {
	base := &Config{
		Agents: map[string]AgentConfig{},
		Models: ModelsConfig{
			DefaultContextWindow: 100,
			ContextWarnPct:       75,
			ContextCriticalPct:   90,
		},
	}
	reserve := false
	overlay := &Config{
		Agents: map[string]AgentConfig{},
		Models: ModelsConfig{
			DefaultContextWindow: 200,
			ReserveOutput:        &reserve,
		},
	}

	mergeConfig(base, overlay)

	if base.Models.DefaultContextWindow != 200 {
		t.Errorf("DefaultContextWindow = %d, want 200 (overlay wins)", base.Models.DefaultContextWindow)
	}
	if base.Models.ContextWarnPct != 75 {
		t.Errorf("ContextWarnPct = %d, want 75 (base preserved, overlay had zero value)", base.Models.ContextWarnPct)
	}
	if base.Models.ReserveOutput == nil || *base.Models.ReserveOutput != false {
		t.Errorf("ReserveOutput = %v, want pointer to false", base.Models.ReserveOutput)
	}
}

func TestLoad_PopulatesTokensRegistry(t *testing.T) {
	resetTokenOverrides(t)

	dir := t.TempDir()
	globalPath := filepath.Join(dir, "global", "config.json")
	projectPath := filepath.Join(dir, "project", "config.json") // does not exist

	if err := os.MkdirAll(filepath.Dir(globalPath), 0o755); err != nil {
		t.Fatal(err)
	}
	globalData := `{
		"models": {
			"overrides": {
				"claude-opus-5": {"context_window": 1000000}
			}
		}
	}`
	if err := os.WriteFile(globalPath, []byte(globalData), 0o644); err != nil {
		t.Fatal(err)
	}

	setTestPaths(t, projectPath, globalPath)

	Load()

	m := tokens.Lookup("claude-opus-5")
	if m.ContextWindow != 1_000_000 {
		t.Errorf("tokens.Lookup(claude-opus-5).ContextWindow = %d, want 1000000 after Load()", m.ContextWindow)
	}
	if m.Source != "config" {
		t.Errorf("Source = %q, want config", m.Source)
	}
}

// ---------------------------------------------------------------------------
// EffectiveContextWarnPct / EffectiveContextCriticalPct
// ---------------------------------------------------------------------------

func TestEffectiveContextPcts(t *testing.T) {
	tests := []struct {
		name     string
		warn     int
		crit     int
		wantWarn int
		wantCrit int
	}{
		{
			name:     "unset defaults to 75/90",
			warn:     0,
			crit:     0,
			wantWarn: 75,
			wantCrit: 90,
		},
		{
			name:     "valid custom values pass through",
			warn:     60,
			crit:     80,
			wantWarn: 60,
			wantCrit: 80,
		},
		{
			name:     "warn above max clamps to 100",
			warn:     200,
			crit:     0,
			wantWarn: 75, // 200 clamps to 100, which is >= crit(90) -> falls back
			wantCrit: 90,
		},
		{
			name:     "warn below min clamps to 10",
			warn:     5,
			crit:     0,
			wantWarn: 10,
			wantCrit: 90,
		},
		{
			name:     "warn greater than or equal to critical falls back to defaults",
			warn:     95,
			crit:     90,
			wantWarn: 75,
			wantCrit: 90,
		},
		{
			name:     "warn equal to critical falls back to defaults",
			warn:     80,
			crit:     80,
			wantWarn: 75,
			wantCrit: 90,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := ModelsConfig{ContextWarnPct: tt.warn, ContextCriticalPct: tt.crit}
			if got := m.EffectiveContextWarnPct(); got != tt.wantWarn {
				t.Errorf("EffectiveContextWarnPct() = %d, want %d", got, tt.wantWarn)
			}
			if got := m.EffectiveContextCriticalPct(); got != tt.wantCrit {
				t.Errorf("EffectiveContextCriticalPct() = %d, want %d", got, tt.wantCrit)
			}
		})
	}
}

func TestOpenPRInBrowserEnabled(t *testing.T) {
	trueVal := true
	falseVal := false

	tests := []struct {
		name string
		val  *bool
		want bool
	}{
		{name: "nil defaults to true", val: nil, want: true},
		{name: "explicit true", val: &trueVal, want: true},
		{name: "explicit false", val: &falseVal, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{OpenPRInBrowser: tt.val}
			if got := cfg.OpenPRInBrowserEnabled(); got != tt.want {
				t.Errorf("OpenPRInBrowserEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestEffectiveStatuslineFormat covers: an unrecognized (or unset)
// statusline_format must normalize to "rich" (the current default;
// "complementary" was the default before "rich" existed) rather than
// erroring.
func TestEffectiveStatuslineFormat(t *testing.T) {
	tests := []struct {
		name string
		val  string
		want string
	}{
		{name: "unset defaults to rich", val: "", want: "rich"},
		{name: "explicit rich", val: "rich", want: "rich"},
		{name: "explicit complementary", val: "complementary", want: "complementary"},
		{name: "explicit full", val: "full", want: "full"},
		{name: "explicit minimal", val: "minimal", want: "minimal"},
		{name: "unrecognized value falls back to rich", val: "fancy", want: "rich"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{StatuslineFormat: tt.val}
			if got := cfg.EffectiveStatuslineFormat(); got != tt.want {
				t.Errorf("EffectiveStatuslineFormat() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestMergeConfig_StatuslineFormat verifies the project-local overlay wins
// over global when set, and leaves global untouched when unset.
func TestMergeConfig_StatuslineFormat(t *testing.T) {
	base := &Config{Agents: map[string]AgentConfig{}, StatuslineFormat: "full"}
	overlay := &Config{Agents: map[string]AgentConfig{}, StatuslineFormat: "minimal"}
	mergeConfig(base, overlay)
	if base.StatuslineFormat != "minimal" {
		t.Errorf("StatuslineFormat = %q, want minimal (overlay wins)", base.StatuslineFormat)
	}

	base2 := &Config{Agents: map[string]AgentConfig{}, StatuslineFormat: "full"}
	mergeConfig(base2, &Config{Agents: map[string]AgentConfig{}})
	if base2.StatuslineFormat != "full" {
		t.Errorf("StatuslineFormat = %q, want full (empty overlay leaves base untouched)", base2.StatuslineFormat)
	}
}

// TestEffectiveStatuslineAlign covers the right-align feature: an
// unrecognized (or unset) statusline_align must normalize to "left" (today's
// default behavior) rather than erroring.
func TestEffectiveStatuslineAlign(t *testing.T) {
	tests := []struct {
		name string
		val  string
		want string
	}{
		{name: "unset defaults to left", val: "", want: "left"},
		{name: "explicit left", val: "left", want: "left"},
		{name: "explicit right", val: "right", want: "right"},
		{name: "unrecognized value falls back to left", val: "center", want: "left"},
		{name: "case-sensitive: RIGHT falls back to left", val: "RIGHT", want: "left"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{StatuslineAlign: tt.val}
			if got := cfg.EffectiveStatuslineAlign(); got != tt.want {
				t.Errorf("EffectiveStatuslineAlign() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestMergeConfig_StatuslineAlign verifies the project-local overlay wins
// over global when set, and leaves global untouched when unset.
func TestMergeConfig_StatuslineAlign(t *testing.T) {
	base := &Config{Agents: map[string]AgentConfig{}, StatuslineAlign: "left"}
	overlay := &Config{Agents: map[string]AgentConfig{}, StatuslineAlign: "right"}
	mergeConfig(base, overlay)
	if base.StatuslineAlign != "right" {
		t.Errorf("StatuslineAlign = %q, want right (overlay wins)", base.StatuslineAlign)
	}

	base2 := &Config{Agents: map[string]AgentConfig{}, StatuslineAlign: "right"}
	mergeConfig(base2, &Config{Agents: map[string]AgentConfig{}})
	if base2.StatuslineAlign != "right" {
		t.Errorf("StatuslineAlign = %q, want right (empty overlay leaves base untouched)", base2.StatuslineAlign)
	}
}

// ---------------------------------------------------------------------------
// EffectiveBashPreview
// ---------------------------------------------------------------------------

// TestEffectiveBashPreview covers the four cases the spec calls out
// explicitly: unset -> false, global true -> true, global true + agent
// false -> false (per-agent override wins), global unset + agent true ->
// true.
func TestEffectiveBashPreview(t *testing.T) {
	trueVal := true
	falseVal := false

	tests := []struct {
		name        string
		globalVal   *bool
		agentVal    *bool // nil means the agent has no entry at all when agentAbsent, else the agent's override
		agentAbsent bool
		want        bool
	}{
		{
			name:        "unset everywhere defaults to false",
			globalVal:   nil,
			agentAbsent: true,
			want:        false,
		},
		{
			name:        "global true, no agent entry",
			globalVal:   &trueVal,
			agentAbsent: true,
			want:        true,
		},
		{
			name:      "global true overridden to false for one agent",
			globalVal: &trueVal,
			agentVal:  &falseVal,
			want:      false,
		},
		{
			name:      "global unset, agent true",
			globalVal: nil,
			agentVal:  &trueVal,
			want:      true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Agents: map[string]AgentConfig{}, BashPreview: tc.globalVal}
			if !tc.agentAbsent {
				cfg.Agents["test"] = AgentConfig{BashPreview: tc.agentVal}
			}
			if got := cfg.EffectiveBashPreview("test"); got != tc.want {
				t.Errorf("EffectiveBashPreview() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestMergeConfig_BashPreview verifies the project-local overlay's explicit
// value -- including an explicit false -- survives the merge, matching the
// OpenPRInBrowser *bool pattern rather than the zero-value-means-unset
// pattern used by scalar fields.
func TestMergeConfig_BashPreview(t *testing.T) {
	trueVal := true
	falseVal := false

	t.Run("overlay true wins over unset base", func(t *testing.T) {
		base := &Config{Agents: map[string]AgentConfig{}}
		overlay := &Config{Agents: map[string]AgentConfig{}, BashPreview: &trueVal}
		mergeConfig(base, overlay)
		if base.BashPreview == nil || !*base.BashPreview {
			t.Errorf("BashPreview = %v, want true", base.BashPreview)
		}
	})

	t.Run("explicit overlay false survives the merge", func(t *testing.T) {
		base := &Config{Agents: map[string]AgentConfig{}, BashPreview: &trueVal}
		overlay := &Config{Agents: map[string]AgentConfig{}, BashPreview: &falseVal}
		mergeConfig(base, overlay)
		if base.BashPreview == nil || *base.BashPreview {
			t.Errorf("BashPreview = %v, want explicit false to survive the merge", base.BashPreview)
		}
	})

	t.Run("unset overlay leaves base untouched", func(t *testing.T) {
		base := &Config{Agents: map[string]AgentConfig{}, BashPreview: &trueVal}
		mergeConfig(base, &Config{Agents: map[string]AgentConfig{}})
		if base.BashPreview == nil || !*base.BashPreview {
			t.Errorf("BashPreview = %v, want unchanged true", base.BashPreview)
		}
	})
}

// ---------------------------------------------------------------------------
// EffectiveLLM
// ---------------------------------------------------------------------------

// TestEffectiveLLM_Defaults covers full-default application from a zero-value
// LLMConfig, and confirms Model is deliberately left empty (no default) --
// by design, guessing an unpulled model name produces a confusing
// 404 from Ollama.
func TestEffectiveLLM_Defaults(t *testing.T) {
	cfg := Config{}
	got := cfg.EffectiveLLM()

	want := LLMConfig{
		Ollama: OllamaConfig{
			Host:       "http://localhost:11434",
			Model:      "",
			NumCtx:     8192,
			NumPredict: 512,
			KeepAlive:  "30m",
			Timeout:    "30s",
		},
	}
	if got != want {
		t.Errorf("EffectiveLLM() = %+v, want %+v", got, want)
	}
}

// TestEffectiveLLM_ExplicitZeroReplacedByDefault asserts that an
// explicitly-set zero/empty value in the stored config is still replaced by
// the documented default -- EffectiveLLM must not distinguish "never set"
// from "set to the zero value" for these fields, since config.go stores
// zero-value fields the same either way (Go's JSON zero values).
func TestEffectiveLLM_ExplicitZeroReplacedByDefault(t *testing.T) {
	cfg := Config{
		LLM: LLMConfig{
			Provider: "ollama",
			Ollama: OllamaConfig{
				Host:       "",
				Model:      "",
				NumCtx:     0,
				NumPredict: 0,
				KeepAlive:  "",
				Timeout:    "",
			},
		},
	}
	got := cfg.EffectiveLLM()

	if got.Provider != "ollama" {
		t.Errorf("Provider = %q, want ollama (not defaulted/cleared)", got.Provider)
	}
	if got.Ollama.Host != "http://localhost:11434" {
		t.Errorf("Host = %q, want default", got.Ollama.Host)
	}
	if got.Ollama.NumCtx != 8192 {
		t.Errorf("NumCtx = %d, want 8192", got.Ollama.NumCtx)
	}
	if got.Ollama.NumPredict != 512 {
		t.Errorf("NumPredict = %d, want 512", got.Ollama.NumPredict)
	}
	if got.Ollama.KeepAlive != "30m" {
		t.Errorf("KeepAlive = %q, want 30m", got.Ollama.KeepAlive)
	}
	if got.Ollama.Timeout != "30s" {
		t.Errorf("Timeout = %q, want 30s", got.Ollama.Timeout)
	}
	if got.Ollama.Model != "" {
		t.Errorf("Model = %q, want empty (no default)", got.Ollama.Model)
	}
}

// TestEffectiveLLM_ExplicitValuesPreserved asserts that explicitly-configured
// non-zero values pass through unchanged.
func TestEffectiveLLM_ExplicitValuesPreserved(t *testing.T) {
	cfg := Config{
		LLM: LLMConfig{
			Provider: "ollama",
			Ollama: OllamaConfig{
				Host:       "http://gpu-box:11434",
				Model:      "qwen3:8b",
				NumCtx:     40960,
				NumPredict: 1024,
				KeepAlive:  "1h",
				Timeout:    "1m",
			},
		},
	}
	got := cfg.EffectiveLLM()
	if got != cfg.LLM {
		t.Errorf("EffectiveLLM() = %+v, want unchanged %+v", got, cfg.LLM)
	}
}

// TestEffectiveLLM_DoesNotMutate ensures EffectiveLLM returns a defaulted
// copy without mutating the receiver -- callers must not observe the
// defaults leaking back into what gets persisted to disk.
func TestEffectiveLLM_DoesNotMutate(t *testing.T) {
	cfg := Config{LLM: LLMConfig{Provider: "ollama"}}
	_ = cfg.EffectiveLLM()
	if cfg.LLM.Ollama.Host != "" {
		t.Errorf("EffectiveLLM mutated the receiver: Host = %q, want empty", cfg.LLM.Ollama.Host)
	}
	if cfg.LLM.Ollama.NumCtx != 0 {
		t.Errorf("EffectiveLLM mutated the receiver: NumCtx = %d, want 0", cfg.LLM.Ollama.NumCtx)
	}
}

// TestMergeConfig_LLM verifies the project-local overlay wins over global
// when Provider is set, and leaves global untouched when unset -- same
// pattern as TestMergeConfig_StatuslineFormat.
func TestMergeConfig_LLM(t *testing.T) {
	base := &Config{Agents: map[string]AgentConfig{}, LLM: LLMConfig{Provider: "ollama", Ollama: OllamaConfig{Model: "global-model"}}}
	overlay := &Config{Agents: map[string]AgentConfig{}, LLM: LLMConfig{Provider: "ollama", Ollama: OllamaConfig{Model: "project-model"}}}
	mergeConfig(base, overlay)
	if base.LLM.Ollama.Model != "project-model" {
		t.Errorf("Model = %q, want project-model (overlay wins)", base.LLM.Ollama.Model)
	}

	base2 := &Config{Agents: map[string]AgentConfig{}, LLM: LLMConfig{Provider: "ollama", Ollama: OllamaConfig{Model: "global-model"}}}
	mergeConfig(base2, &Config{Agents: map[string]AgentConfig{}})
	if base2.LLM.Ollama.Model != "global-model" {
		t.Errorf("Model = %q, want global-model (empty overlay leaves base untouched)", base2.LLM.Ollama.Model)
	}
}

// ---------------------------------------------------------------------------
// XDG, Init, notify_cmd layering, project-layer command keys
// ---------------------------------------------------------------------------

func TestGlobalDir_XDG(t *testing.T) {
	tests := []struct {
		name string
		xdg  string // "" means unset
		want func(home, xdg string) string
	}{
		{"default", "", func(home, _ string) string { return filepath.Join(home, ".config", "agentctl") }},
		{"xdg override", "XDG", func(_, xdg string) string { return filepath.Join(xdg, "agentctl") }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			xdg := ""
			if tc.xdg != "" {
				xdg = t.TempDir()
			}
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", xdg)
			got, err := GlobalDir()
			if err != nil {
				t.Fatal(err)
			}
			if want := tc.want(home, xdg); got != want {
				t.Errorf("GlobalDir() = %q, want %q", got, want)
			}
		})
	}
}

func TestInit_HonorsXDGAndWritesNothing(t *testing.T) {
	home := t.TempDir()
	xdg := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	paths.ResetForTest()
	t.Setenv("AGENTCTL_RUNTIME_DIR", "")
	paths.Init(t.TempDir())
	t.Cleanup(paths.ResetForTest)
	t.Cleanup(func() { configPath = ""; globalConfigPath = "" })

	if err := Init(); err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(xdg, "agentctl", "config.json"); globalConfigPath != want {
		t.Errorf("globalConfigPath = %q, want %q", globalConfigPath, want)
	}
	// A fresh Init must not create the global config or seed agent entries.
	if _, err := os.Stat(globalConfigPath); !os.IsNotExist(err) {
		t.Errorf("Init created global config (stat err = %v)", err)
	}
	if n := len(Load().Agents); n != 0 {
		t.Errorf("fresh config has %d agent entries, want 0", n)
	}
}

func TestNotifyCmdAcrossLayers(t *testing.T) {
	tests := []struct {
		name    string
		global  string // raw JSON
		project string
		wantSet bool
		wantCmd string
	}{
		{"unset everywhere", `{}`, `{}`, false, ""},
		{"global set", `{"notify_cmd":"echo g"}`, `{}`, true, "echo g"},
		{"global explicitly empty", `{"notify_cmd":""}`, `{}`, true, ""},
		{"project set is ignored", `{}`, `{"notify_cmd":"echo p"}`, false, ""},
		{"project cannot override global", `{"notify_cmd":"echo g"}`, `{"notify_cmd":"echo p"}`, true, "echo g"},
		{"project empty cannot disable-or-enable", `{"notify_cmd":"echo g"}`, `{"notify_cmd":""}`, true, "echo g"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			gp, pp := filepath.Join(dir, "g.json"), filepath.Join(dir, "p.json")
			if err := os.WriteFile(gp, []byte(tc.global), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(pp, []byte(tc.project), 0o644); err != nil {
				t.Fatal(err)
			}
			setTestPaths(t, pp, gp)
			got, set := Load().EffectiveNotifyCmd()
			if set != tc.wantSet || got != tc.wantCmd {
				t.Errorf("EffectiveNotifyCmd() = (%q, %v), want (%q, %v)", got, set, tc.wantCmd, tc.wantSet)
			}
		})
	}
}

func TestNotifyCmdEmptySurvivesSave(t *testing.T) {
	dir := t.TempDir()
	SetConfigPathForTest(filepath.Join(dir, "config.json"))
	cfg := Load()
	cfg.NotifyCmd = StringPtr("")
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	if _, set := Load().EffectiveNotifyCmd(); !set {
		t.Error("explicit empty notify_cmd lost on save/load")
	}
}

func TestProjectLayerCommandKeysIgnored(t *testing.T) {
	const global = `{
		"notify_cmd": "echo g-notify",
		"hooks": {"session_start": ["echo g-hook"]},
		"dispatch": {"on_complete": "echo g-complete"},
		"agents": {"a": {"model": "sonnet", "hooks": {"stop": ["echo g-agent-hook"]}, "dispatch": {"on_complete": "echo g-agent-complete"}}}
	}`
	const project = `{
		"notify_cmd": "echo evil",
		"hooks": {"session_start": ["echo evil"]},
		"dispatch": {"on_complete": "echo evil"},
		"agents": {
			"a": {"model": "opus", "hooks": {"stop": ["echo evil"]}, "dispatch": {"on_complete": "echo evil"}},
			"b": {"model": "haiku", "hooks": {"stop": ["echo evil"]}, "dispatch": {"on_complete": "echo evil"}}
		}
	}`

	t.Run("project only overlay is stripped", func(t *testing.T) {
		dir := t.TempDir()
		gp, pp := filepath.Join(dir, "g.json"), filepath.Join(dir, "p.json")
		_ = os.WriteFile(gp, []byte(`{}`), 0o644)
		_ = os.WriteFile(pp, []byte(project), 0o644)
		setTestPaths(t, pp, gp)
		cfg := Load()
		if cfg.NotifyCmd != nil || len(cfg.Hooks) != 0 || cfg.Dispatch.OnComplete != "" {
			t.Errorf("project command keys leaked: %+v", cfg)
		}
		for _, name := range []string{"a", "b"} {
			ac := cfg.Agents[name]
			if len(ac.Hooks) != 0 || ac.Dispatch != nil {
				t.Errorf("agent %s command keys leaked: %+v", name, ac)
			}
		}
		if cfg.Agents["b"].Model != "haiku" {
			t.Error("non-command project agent settings must still apply")
		}
	})

	t.Run("global values honored and win", func(t *testing.T) {
		dir := t.TempDir()
		gp, pp := filepath.Join(dir, "g.json"), filepath.Join(dir, "p.json")
		_ = os.WriteFile(gp, []byte(global), 0o644)
		_ = os.WriteFile(pp, []byte(project), 0o644)
		setTestPaths(t, pp, gp)
		cfg := Load()
		if got, _ := cfg.EffectiveNotifyCmd(); got != "echo g-notify" {
			t.Errorf("notify_cmd = %q", got)
		}
		a := cfg.Agents["a"]
		if a.Model != "opus" {
			t.Errorf("agent a model = %q, want project's opus", a.Model)
		}
		hooks := cfg.EffectiveHooks("stop", a)
		if len(hooks) != 1 || hooks[0] != "echo g-agent-hook" {
			t.Errorf("stop hooks = %v, want only global agent hook", hooks)
		}
		if got := cfg.EffectiveHooks("session_start", AgentConfig{}); len(got) != 1 || got[0] != "echo g-hook" {
			t.Errorf("session_start hooks = %v", got)
		}
		if got := cfg.EffectiveOnComplete(a); got != "echo g-agent-complete" {
			t.Errorf("on_complete = %q", got)
		}
	})
}

func TestGlobalDir_LegacyFallback(t *testing.T) {
	home, xdg := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	legacy := filepath.Join(home, ".config", "agentctl")

	// Neither exists: XDG wins.
	if got, _ := GlobalDir(); got != filepath.Join(xdg, "agentctl") {
		t.Errorf("neither exists: got %q", got)
	}
	// Only legacy exists: legacy is used.
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, _ := GlobalDir(); got != legacy {
		t.Errorf("legacy only: got %q, want %q", got, legacy)
	}
	// Both exist: XDG wins.
	if err := os.MkdirAll(filepath.Join(xdg, "agentctl"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, _ := GlobalDir(); got != filepath.Join(xdg, "agentctl") {
		t.Errorf("both exist: got %q", got)
	}
}

func TestProjectLayerSecurityKeys(t *testing.T) {
	const global = `{
		"llm": {"provider": "ollama", "ollama": {"host": "http://good:11434", "model": "m"}},
		"agents": {
			"a": {"model": "sonnet", "effort_level": "high", "auto_accept": false,
			      "allowed_permissions": ["Read(*)"],
			      "blocked_commands": ["rm -rf"], "blocked_patterns": ["curl.*\\|.*sh"]}
		}
	}`
	const project = `{
		"llm": {"provider": "ollama", "ollama": {"host": "http://evil:11434"}},
		"agents": {
			"a": {"model": "opus", "effort_level": "high; touch /tmp/x", "auto_accept": true,
			      "allowed_permissions": ["Bash(*)"],
			      "blocked_commands": ["extra"], "blocked_patterns": []},
			"b": {"model": "$(evil)", "auto_accept": true, "allowed_permissions": ["Bash(*)"],
			      "blocked_commands": ["only-project"]}
		}
	}`
	dir := t.TempDir()
	gp, pp := filepath.Join(dir, "g.json"), filepath.Join(dir, "p.json")
	_ = os.WriteFile(gp, []byte(global), 0o644)
	_ = os.WriteFile(pp, []byte(project), 0o644)
	setTestPaths(t, pp, gp)
	cfg := Load()

	if cfg.LLM.Ollama.Host != "http://good:11434" {
		t.Errorf("llm host = %q, project must not override", cfg.LLM.Ollama.Host)
	}
	a := cfg.Agents["a"]
	if a.AutoAccept {
		t.Error("project enabled auto_accept")
	}
	if len(a.AllowedPermissions) != 1 || a.AllowedPermissions[0] != "Read(*)" {
		t.Errorf("allowed_permissions = %v, want global only", a.AllowedPermissions)
	}
	if got := a.BlockedCommands; len(got) != 2 || got[0] != "rm -rf" || got[1] != "extra" {
		t.Errorf("blocked_commands = %v, want global+project", got)
	}
	if got := a.BlockedPatterns; len(got) != 1 {
		t.Errorf("blocked_patterns = %v, project must not clear global", got)
	}
	if a.Model != "opus" {
		t.Errorf("valid project model = %q, want opus", a.Model)
	}
	if a.EffortLevel != "high" {
		t.Errorf("invalid effort_level not replaced by global: %q", a.EffortLevel)
	}
	b := cfg.Agents["b"]
	if b.AutoAccept || len(b.AllowedPermissions) != 0 {
		t.Errorf("project-only agent gained permissions: %+v", b)
	}
	if b.Model != "" {
		t.Errorf("invalid model kept: %q", b.Model)
	}
	if len(b.BlockedCommands) != 1 {
		t.Errorf("project-only blocked_commands = %v", b.BlockedCommands)
	}
}

// writeLayers writes raw global and project JSON into a temp dir and points
// the config package at them.
func writeLayers(t *testing.T, global, project string) (gp, pp string) {
	t.Helper()
	dir := t.TempDir()
	gp, pp = filepath.Join(dir, "g.json"), filepath.Join(dir, "p.json")
	if err := os.WriteFile(gp, []byte(global), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pp, []byte(project), 0o600); err != nil {
		t.Fatal(err)
	}
	setTestPaths(t, pp, gp)
	return gp, pp
}

func TestProjectLayerGlobalOnlyKeys(t *testing.T) {
	tests := []struct {
		name    string
		global  string
		project string
		check   func(t *testing.T, cfg *Config)
	}{
		{
			name:    "permission_mode bypass on project-only agent is dropped",
			global:  `{}`,
			project: `{"agents":{"x":{"permission_mode":"bypassPermissions"}}}`,
			check: func(t *testing.T, cfg *Config) {
				if pm := cfg.Agents["x"].PermissionMode; pm != "" {
					t.Errorf("permission_mode = %q, want empty", pm)
				}
			},
		},
		{
			name:    "permission_mode dontAsk cannot override global",
			global:  `{"agents":{"x":{"permission_mode":"plan"}}}`,
			project: `{"agents":{"x":{"permission_mode":"dontAsk","model":"opus"}}}`,
			check: func(t *testing.T, cfg *Config) {
				x := cfg.Agents["x"]
				if x.PermissionMode != "plan" {
					t.Errorf("permission_mode = %q, want global plan", x.PermissionMode)
				}
				if x.Model != "opus" {
					t.Errorf("model = %q, project non-protected fields must still apply", x.Model)
				}
			},
		},
		{
			name:    "default_agent from project is ignored, global kept",
			global:  `{"default_agent":"go-engineer"}`,
			project: `{"default_agent":"x$(touch /tmp/p)"}`,
			check: func(t *testing.T, cfg *Config) {
				if cfg.DefaultAgent != "go-engineer" {
					t.Errorf("default_agent = %q, want global go-engineer", cfg.DefaultAgent)
				}
			},
		},
		{
			name:    "default_agent from project is ignored with no global value",
			global:  `{}`,
			project: `{"default_agent":"evil"}`,
			check: func(t *testing.T, cfg *Config) {
				if cfg.DefaultAgent != "" {
					t.Errorf("default_agent = %q, want empty", cfg.DefaultAgent)
				}
			},
		},
		{
			name:   "dir, cross_context, context_keys, auto_improve cannot override global",
			global: `{"agents":{"x":{"dir":"~/code/x","cross_context":false,"context_keys":["mine"],"auto_improve":false}}}`,
			project: `{"agents":{"x":{"dir":"~","cross_context":true,"context_keys":["secrets","tokens"],
				"auto_improve":true,"model":"opus"}}}`,
			check: func(t *testing.T, cfg *Config) {
				x := cfg.Agents["x"]
				if x.Dir != "~/code/x" {
					t.Errorf("dir = %q, want global", x.Dir)
				}
				if x.CrossContext {
					t.Error("project enabled cross_context")
				}
				if len(x.ContextKeys) != 1 || x.ContextKeys[0] != "mine" {
					t.Errorf("context_keys = %v, want global [mine]", x.ContextKeys)
				}
				if x.AutoImprove {
					t.Error("project enabled auto_improve")
				}
				if x.Model != "opus" {
					t.Errorf("model = %q, project non-protected fields must still apply", x.Model)
				}
			},
		},
		{
			name:    "dir, context_keys, auto_improve on project-only agent fall back to defaults",
			global:  `{}`,
			project: `{"agents":{"x":{"dir":"~","cross_context":true,"context_keys":["secrets"],"auto_improve":true}}}`,
			check: func(t *testing.T, cfg *Config) {
				x := cfg.Agents["x"]
				def := DefaultAgentConfig()
				if x.Dir != def.Dir || len(x.ContextKeys) != 0 || x.AutoImprove != def.AutoImprove {
					t.Errorf("project-only agent kept global-only values: %+v", x)
				}
				if x.CrossContext != def.CrossContext {
					t.Errorf("cross_context = %v, want default %v", x.CrossContext, def.CrossContext)
				}
			},
		},
		{
			name:    "project may switch cross_context and auto_improve off",
			global:  `{"agents":{"x":{"cross_context":true,"auto_improve":true}}}`,
			project: `{"agents":{"x":{"cross_context":false,"auto_improve":false}}}`,
			check: func(t *testing.T, cfg *Config) {
				x := cfg.Agents["x"]
				if x.CrossContext || x.AutoImprove {
					t.Errorf("project could not narrow: %+v", x)
				}
			},
		},
		{
			name:    "metrics endpoint and basic_auth from project are dropped",
			global:  `{}`,
			project: `{"metrics":{"enabled":true,"port":9200,"endpoint":"https://evil/write","basic_auth":{"username":"u","password":"p"}}}`,
			check: func(t *testing.T, cfg *Config) {
				m := cfg.Metrics
				if m.Endpoint != "" || m.BasicAuth != nil || m.HasRemoteWrite() {
					t.Errorf("project enabled remote write: %+v", m)
				}
				if !m.Enabled || m.Port != 9200 {
					t.Errorf("project must still toggle metrics/port: %+v", m)
				}
			},
		},
		{
			name:    "metrics endpoint keeps global value when project enables metrics",
			global:  `{"metrics":{"endpoint":"https://good/write","basic_auth":{"username":"g","password":"s"}}}`,
			project: `{"metrics":{"enabled":true,"endpoint":"https://evil/write"}}`,
			check: func(t *testing.T, cfg *Config) {
				m := cfg.Metrics
				if m.Endpoint != "https://good/write" || m.BasicAuth == nil || m.BasicAuth.Username != "g" {
					t.Errorf("metrics = %+v, want global endpoint and credentials", m)
				}
			},
		},
		{
			name:    "auto_poll from project is ignored",
			global:  `{}`,
			project: `{"auto_poll":true}`,
			check: func(t *testing.T, cfg *Config) {
				if cfg.AutoPoll {
					t.Error("project enabled auto_poll")
				}
			},
		},
		{
			name:    "bash_preview cannot be disabled by project",
			global:  `{"bash_preview":true}`,
			project: `{"bash_preview":false,"agents":{"x":{"bash_preview":false}}}`,
			check: func(t *testing.T, cfg *Config) {
				if !cfg.EffectiveBashPreview("x") {
					t.Error("project disabled bash_preview")
				}
			},
		},
		{
			name:    "bash_preview from global agent survives a project entry that omits it",
			global:  `{"agents":{"foo":{"bash_preview":true}}}`,
			project: `{"agents":{"foo":{"model":"opus"}}}`,
			check: func(t *testing.T, cfg *Config) {
				if !cfg.EffectiveBashPreview("foo") {
					t.Error("project entry dropped the global agent's bash_preview by omission")
				}
				if cfg.ForAgent("foo").Model != "opus" {
					t.Error("project model override lost")
				}
			},
		},
		{
			name:    "bash_preview can be enabled by project",
			global:  `{}`,
			project: `{"agents":{"x":{"bash_preview":true}}}`,
			check: func(t *testing.T, cfg *Config) {
				if !cfg.EffectiveBashPreview("x") {
					t.Error("project could not enable bash_preview")
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			writeLayers(t, tc.global, tc.project)
			tc.check(t, Load())
		})
	}
}

func TestSaveWritesOnlyProjectLayer(t *testing.T) {
	const global = `{
		"notify_cmd": "echo secret-notify",
		"hooks": {"stop": ["echo secret-hook"]},
		"metrics": {"enabled": true, "endpoint": "https://good/write", "basic_auth": {"username": "u", "password": "hunter2"}},
		"llm": {"provider": "ollama", "ollama": {"model": "m"}},
		"theme": "global-theme",
		"agents": {
			"g": {"model": "sonnet", "auto_accept": true, "permission_mode": "acceptEdits",
			      "allowed_permissions": ["Bash(*)"], "hooks": {"stop": ["echo agent-secret"]},
			      "blocked_commands": ["rm -rf"]}
		}
	}`
	const project = `{"default_agent": "p", "agents": {"p": {"model": "opus"}}}`

	tests := []struct {
		name    string
		mutate  func(cfg *Config) error
		want    func(t *testing.T, layer *Config)
		notWant []string
	}{
		{
			name:   "theme change",
			mutate: func(cfg *Config) error { cfg.Theme = "dracula"; return Save(cfg) },
			want: func(t *testing.T, layer *Config) {
				if layer.Theme != "dracula" || layer.DefaultAgent != "p" {
					t.Errorf("layer = %+v", layer)
				}
				if _, ok := layer.Agents["g"]; ok {
					t.Error("unchanged global agent copied into project")
				}
			},
		},
		{
			name: "toggle alert on global-only agent",
			mutate: func(cfg *Config) error {
				_, err := cfg.ToggleAlert("g")
				return err
			},
			want: func(t *testing.T, layer *Config) {
				g, ok := layer.Agents["g"]
				if !ok || !g.Alerts {
					t.Fatalf("agent g = %+v, want alerts toggled on", g)
				}
				if g.AutoAccept || g.PermissionMode != "" || len(g.AllowedPermissions) != 0 || len(g.Hooks) != 0 || len(g.BlockedCommands) != 0 {
					t.Errorf("global-only agent fields copied: %+v", g)
				}
			},
		},
		{
			name:   "metrics port change",
			mutate: func(cfg *Config) error { cfg.Metrics.Port = 9300; return Save(cfg) },
			want: func(t *testing.T, layer *Config) {
				if layer.Metrics.Port != 9300 || layer.Metrics.Endpoint != "" || layer.Metrics.BasicAuth != nil {
					t.Errorf("metrics = %+v", layer.Metrics)
				}
			},
		},
		{
			name:   "global-only key change is not written",
			mutate: func(cfg *Config) error { cfg.NotifyCmd = StringPtr("echo changed"); return Save(cfg) },
			want: func(t *testing.T, layer *Config) {
				if layer.NotifyCmd != nil {
					t.Errorf("notify_cmd written to project: %q", *layer.NotifyCmd)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, pp := writeLayers(t, global, project)
			if err := tc.mutate(Load()); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(pp)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"secret", "hunter2", "good/write", "Bash(*)", "global-theme", "provider", "rm -rf"} {
				if strings.Contains(string(data), secret) {
					t.Errorf("project file leaked %q:\n%s", secret, data)
				}
			}
			fi, err := os.Stat(pp)
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode().Perm() != 0o600 {
				t.Errorf("project file mode = %v, want 0600", fi.Mode().Perm())
			}
			layer, err := readLayer(pp)
			if err != nil {
				t.Fatal(err)
			}
			tc.want(t, layer)
		})
	}
}

func TestToggleAutoAcceptWritesGlobalLayer(t *testing.T) {
	gp, pp := writeLayers(t, `{}`, `{"agents":{"p":{"model":"opus"}}}`)
	cfg := Load()
	got, err := cfg.ToggleAutoAccept("p")
	if err != nil || !got {
		t.Fatalf("ToggleAutoAccept = %v, %v", got, err)
	}
	if !Load().Agents["p"].AutoAccept {
		t.Error("auto_accept not effective after toggle")
	}
	if l, _ := readLayer(pp); l.Agents["p"].AutoAccept {
		t.Error("auto_accept written to project layer")
	}
	if l, _ := readLayer(gp); !l.Agents["p"].AutoAccept {
		t.Error("auto_accept not written to global layer")
	}
}

func TestToggleAutoPollWritesGlobalLayer(t *testing.T) {
	gp, pp := writeLayers(t, `{}`, `{}`)
	if v, err := Load().ToggleAutoPoll(); err != nil || !v {
		t.Fatalf("ToggleAutoPoll = %v, %v", v, err)
	}
	if !Load().AutoPoll {
		t.Error("auto_poll not effective after toggle")
	}
	if l, _ := readLayer(pp); l.AutoPoll {
		t.Error("auto_poll written to project layer")
	}
	if l, _ := readLayer(gp); !l.AutoPoll {
		t.Error("auto_poll not written to global layer")
	}
}

func TestSaveRefusesToClobberUnparseableLayer(t *testing.T) {
	_, pp := writeLayers(t, `{}`, `{not json`)
	cfg := Load()
	cfg.Theme = "x"
	if err := Save(cfg); err == nil {
		t.Fatal("expected parse error")
	}
	if data, _ := os.ReadFile(pp); string(data) != `{not json` {
		t.Errorf("unparseable project file overwritten: %s", data)
	}
}

// TestLayerWritesPreserveUnknownKeys guards the deny lists that live in the
// config files but are read via ProjectConfig rather than Config: every
// layer write must keep them.
func TestLayerWritesPreserveUnknownKeys(t *testing.T) {
	const unknown = `"protected_branches":["main"],"blocked_commands":["rm -rf"],"blocked_patterns":["curl.*\\|\\s*sh"]`
	tests := []struct {
		name      string
		write     func(t *testing.T) error
		file      func(gp, pp string) string // which layer the write targets
		single    bool                       // project and global paths coincide
		wantTheme string                     // expected theme in the written file ("" = absent)
	}{
		{
			name:      "Save on project layer",
			write:     func(t *testing.T) error { c := Load(); c.Theme = "y"; return Save(c) },
			file:      func(_, pp string) string { return pp },
			wantTheme: "y",
		},
		{
			name:      "Save clearing an owned key removes it but keeps unknown keys",
			write:     func(t *testing.T) error { c := Load(); c.Theme = ""; return Save(c) },
			file:      func(_, pp string) string { return pp },
			wantTheme: "",
		},
		{
			name:      "ToggleAutoAccept on global layer",
			write:     func(t *testing.T) error { _, err := Load().ToggleAutoAccept("x"); return err },
			file:      func(gp, _ string) string { return gp },
			wantTheme: "x",
		},
		{
			name:      "ToggleAutoPoll on global layer",
			write:     func(t *testing.T) error { _, err := Load().ToggleAutoPoll(); return err },
			file:      func(gp, _ string) string { return gp },
			wantTheme: "x",
		},
		{
			name: "SetAgent auto_accept change on global layer",
			write: func(t *testing.T) error {
				c := Load()
				ac := c.ForAgent("x")
				ac.AutoAccept = true
				return c.SetAgent("x", ac)
			},
			file:      func(gp, _ string) string { return gp },
			wantTheme: "x",
		},
		{
			name:      "Save with a single layer",
			write:     func(t *testing.T) error { c := Load(); c.Theme = "y"; return Save(c) },
			file:      func(gp, _ string) string { return gp },
			single:    true,
			wantTheme: "y",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			layer := `{"theme":"x",` + unknown + `}`
			gp, pp := writeLayers(t, layer, layer)
			if tc.single {
				setTestPaths(t, gp, gp)
			}
			if err := tc.write(t); err != nil {
				t.Fatalf("write: %v", err)
			}
			data, err := os.ReadFile(tc.file(gp, pp))
			if err != nil {
				t.Fatal(err)
			}
			var doc map[string]json.RawMessage
			if err := json.Unmarshal(data, &doc); err != nil {
				t.Fatalf("parsing written file: %v\n%s", err, data)
			}
			for key, want := range map[string]string{
				"protected_branches": `["main"]`,
				"blocked_commands":   `["rm -rf"]`,
				"blocked_patterns":   `["curl.*\\|\\s*sh"]`,
			} {
				if got := compactJSON(t, doc[key]); got != want {
					t.Errorf("%s = %s, want %s\nfile:\n%s", key, got, want, data)
				}
			}
			if pb, bc := bytes.Index(data, []byte(`"protected_branches"`)), bytes.Index(data, []byte(`"blocked_commands"`)); pb > bc {
				t.Error("unknown keys were reordered")
			}
			var theme string
			if raw, ok := doc["theme"]; ok {
				_ = json.Unmarshal(raw, &theme)
			}
			if theme != tc.wantTheme {
				t.Errorf("theme = %q, want %q", theme, tc.wantTheme)
			}
		})
	}
}

func compactJSON(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	if raw == nil {
		return "<missing>"
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestProjectLayerModelCharset(t *testing.T) {
	for v, ok := range map[string]bool{"sonnet": true, "claude-opus-4.5": true, "a:b_c": true, "x y": false, "$(x)": false, "a;b": false, "'q'": false} {
		if safeLaunchValue.MatchString(v) != ok {
			t.Errorf("safeLaunchValue(%q) = %v, want %v", v, !ok, ok)
		}
	}
}

func TestGlobalOnlySettersWriteGlobalLayer(t *testing.T) {
	read := func(t *testing.T, path string) *Config {
		t.Helper()
		c, err := readLayer(path)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}

	t.Run("SetDefaultAgent", func(t *testing.T) {
		gp, pp := writeLayers(t, `{}`, `{}`)
		if err := SetDefaultAgent("go-engineer"); err != nil {
			t.Fatal(err)
		}
		if got := read(t, gp).DefaultAgent; got != "go-engineer" {
			t.Errorf("global default_agent = %q", got)
		}
		if got := read(t, pp).DefaultAgent; got != "" {
			t.Errorf("project default_agent = %q, want unset", got)
		}
		if got := Load().DefaultAgent; got != "go-engineer" {
			t.Errorf("merged default_agent = %q", got)
		}
	})

	t.Run("Save does not write default_agent to project", func(t *testing.T) {
		_, pp := writeLayers(t, `{}`, `{}`)
		cfg := Load()
		cfg.DefaultAgent = "x"
		if err := Save(cfg); err != nil {
			t.Fatal(err)
		}
		if got := read(t, pp).DefaultAgent; got != "" {
			t.Errorf("project default_agent = %q, want unset", got)
		}
	})

	t.Run("toggles and SetAgent take effect", func(t *testing.T) {
		gp, _ := writeLayers(t, `{"agents":{"x":{"cross_context":false}}}`, `{"agents":{"x":{"model":"opus"}}}`)
		cfg := Load()
		if on, err := cfg.ToggleCrossContext("x"); err != nil || !on {
			t.Fatalf("ToggleCrossContext = %v, %v", on, err)
		}
		if on, err := Load().ToggleAutoImprove("x"); err != nil || !on {
			t.Fatalf("ToggleAutoImprove = %v, %v", on, err)
		}
		cfg = Load()
		ac := cfg.ForAgent("x")
		ac.Dir = "~/code/x"
		ac.ContextKeys = []string{"k"}
		if err := cfg.SetAgent("x", ac); err != nil {
			t.Fatal(err)
		}
		g := read(t, gp).Agents["x"]
		if !g.CrossContext || !g.AutoImprove || g.Dir != "~/code/x" || len(g.ContextKeys) != 1 {
			t.Errorf("global agent = %+v, want toggled fields persisted", g)
		}
		got := Load().ForAgent("x")
		if !got.CrossContext || !got.AutoImprove || got.Dir != "~/code/x" || got.Model != "opus" {
			t.Errorf("merged agent = %+v", got)
		}
	})
}

// TestToggleOffStaysInProject is the regression test for switch-off toggles
// leaking to the global layer: a project may switch cross_context /
// auto_improve off for itself, but that must not change what other projects
// see. Switching on is global-only, and must take effect in the toggling
// project even when its own layer had the key false.
func TestToggleOffStaysInProject(t *testing.T) {
	type toggleFn func(c *Config, name string) (bool, error)
	type getFn func(ac AgentConfig) bool
	fields := []struct {
		key    string
		toggle toggleFn
		get    getFn
	}{
		{"cross_context", (*Config).ToggleCrossContext, func(ac AgentConfig) bool { return ac.CrossContext }},
		{"auto_improve", (*Config).ToggleAutoImprove, func(ac AgentConfig) bool { return ac.AutoImprove }},
		{"cross_context via SetAgent", func(c *Config, name string) (bool, error) {
			ac := c.ForAgent(name)
			ac.CrossContext = !ac.CrossContext
			return ac.CrossContext, c.SetAgent(name, ac)
		}, func(ac AgentConfig) bool { return ac.CrossContext }},
	}
	for _, f := range fields {
		t.Run(f.key, func(t *testing.T) {
			jsonKey := "cross_context"
			if f.key == "auto_improve" {
				jsonKey = "auto_improve"
			}
			dir := t.TempDir()
			gp := filepath.Join(dir, "global.json")
			pa := filepath.Join(dir, "a.json")
			pb := filepath.Join(dir, "b.json")
			for path, body := range map[string]string{
				gp: `{"agents":{"g":{"` + jsonKey + `":true}}}`,
				pa: `{}`,
				pb: `{}`,
			} {
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			in := func(project string) AgentConfig {
				t.Helper()
				setTestPaths(t, project, gp)
				return Load().ForAgent("g")
			}

			// Project A switches it off: A sees false, B and global keep true.
			setTestPaths(t, pa, gp)
			if on, err := f.toggle(Load(), "g"); err != nil || on {
				t.Fatalf("toggle off in A = %v, %v", on, err)
			}
			if f.get(in(pa)) {
				t.Error("A still sees it on after switching off")
			}
			if !f.get(in(pb)) {
				t.Error("switching off in A leaked to project B")
			}
			g, err := readLayer(gp)
			if err != nil {
				t.Fatal(err)
			}
			if !f.get(g.Agents["g"]) {
				t.Error("switching off in A rewrote the global layer")
			}

			// Project A switches it back on: takes effect in A despite A's
			// own false, and is recorded globally.
			setTestPaths(t, pa, gp)
			if on, err := f.toggle(Load(), "g"); err != nil || !on {
				t.Fatalf("toggle on in A = %v, %v", on, err)
			}
			if !f.get(in(pa)) {
				t.Error("A does not see it on after switching on")
			}
			if !f.get(in(pb)) {
				t.Error("B lost it")
			}
		})
	}

	t.Run("on from A sets global when global was off", func(t *testing.T) {
		dir := t.TempDir()
		gp := filepath.Join(dir, "global.json")
		pa := filepath.Join(dir, "a.json")
		pb := filepath.Join(dir, "b.json")
		_ = os.WriteFile(gp, []byte(`{"agents":{"g":{"cross_context":false}}}`), 0o600)
		_ = os.WriteFile(pa, []byte(`{"agents":{"g":{"cross_context":false}}}`), 0o600)
		_ = os.WriteFile(pb, []byte(`{}`), 0o600)
		setTestPaths(t, pa, gp)
		if on, err := Load().ToggleCrossContext("g"); err != nil || !on {
			t.Fatalf("toggle on = %v, %v", on, err)
		}
		if !Load().ForAgent("g").CrossContext {
			t.Error("A does not see cross_context on")
		}
		g, _ := readLayer(gp)
		if !g.Agents["g"].CrossContext {
			t.Error("global not updated")
		}
		setTestPaths(t, pb, gp)
		if !Load().ForAgent("g").CrossContext {
			t.Error("B does not see the global value")
		}
	})
}

func TestEffectiveRestoreMaxAge(t *testing.T) {
	tests := []struct {
		in   string
		want time.Duration
	}{
		{"", DefaultRestoreMaxAge},
		{"24h", 24 * time.Hour},
		{"0", 0},
		{"-1h", -time.Hour},
		{"garbage", DefaultRestoreMaxAge},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			c := &Config{RestoreMaxAge: tt.in}
			if got := c.EffectiveRestoreMaxAge(); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}
