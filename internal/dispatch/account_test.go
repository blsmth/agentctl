package dispatch

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/claudehome"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/harness"
	"github.com/blsmth/agentctl/internal/tmux"
)

func TestApplyAccountEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	def := filepath.Join(home, ".claude")
	personal := filepath.Join(home, ".claude-personal")
	tests := []struct {
		name      string
		start     map[string]string
		res       claudehome.Resolution
		wantDir   string // "" = not set
		wantUnset string // tmux.EnvUnset value
		wantAcct  string
	}{
		{"zero resolution is a no-op", map[string]string{}, claudehome.Resolution{}, "", "", ""},
		{"other dir is set", map[string]string{}, claudehome.Resolution{Name: "personal", Dir: personal}, personal, "", "personal"},
		{"default dir is unset", map[string]string{}, claudehome.Resolution{Name: "ll", Dir: def}, "", "CLAUDE_CONFIG_DIR", "ll"},
		{"default dir with trailing slash is unset", map[string]string{}, claudehome.Resolution{Name: "ll", Dir: def + "/"}, "", "CLAUDE_CONFIG_DIR", "ll"},
		{
			"default dir drops a dir already in env",
			map[string]string{"CLAUDE_CONFIG_DIR": personal},
			claudehome.Resolution{Name: "ll", Dir: def}, "", "CLAUDE_CONFIG_DIR", "ll",
		},
		{
			"unnamed default dir also unsets an inherited account name",
			map[string]string{},
			claudehome.Resolution{Dir: def}, "", "CLAUDE_CONFIG_DIR AGENTCTL_ACCOUNT", "",
		},
		{
			"other dir clears a previous unset",
			map[string]string{tmux.EnvUnset: "CLAUDE_CONFIG_DIR"},
			claudehome.Resolution{Name: "personal", Dir: personal}, personal, "", "personal",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := tt.start
			ApplyAccountEnv(env, tt.res)
			if got := env["CLAUDE_CONFIG_DIR"]; got != tt.wantDir {
				t.Errorf("CLAUDE_CONFIG_DIR = %q, want %q", got, tt.wantDir)
			}
			if got := env[tmux.EnvUnset]; got != tt.wantUnset {
				t.Errorf("%s = %q, want %q", tmux.EnvUnset, got, tt.wantUnset)
			}
			if got := env["AGENTCTL_ACCOUNT"]; got != tt.wantAcct {
				t.Errorf("AGENTCTL_ACCOUNT = %q, want %q", got, tt.wantAcct)
			}
		})
	}
}

// A nous backend's unset list must not clobber the account's request to
// unset CLAUDE_CONFIG_DIR, and the launch dir must still resolve to ~/.claude.
func TestDefaultDirAccountWithNousBackend(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	def := filepath.Join(home, ".claude")
	env := map[string]string{}
	ApplyAccountEnv(env, claudehome.Resolution{Name: "ll", Dir: def})
	ApplyBackendEnv(env, &Backend{Name: "nous", BackendConfig: config.BackendConfig{Kind: config.BackendKindNous, Model: "x/y"}})
	names := strings.Fields(env[tmux.EnvUnset])
	for _, want := range []string{"CLAUDE_CONFIG_DIR", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"} {
		if !slices.Contains(names, want) {
			t.Errorf("%s = %q, missing %s", tmux.EnvUnset, env[tmux.EnvUnset], want)
		}
	}
	if got := accountDirFromEnv(env); got != def {
		t.Errorf("accountDirFromEnv = %q, want %q", got, def)
	}
	if got := accountDirFromEnv(map[string]string{}); got != "" {
		t.Errorf("accountDirFromEnv(empty) = %q, want \"\"", got)
	}
}

// A worker under another account must not be told to SendMessage its
// dispatcher, nor be given its @N.%P pointer.
func TestCrossAccountWorkerPreamble(t *testing.T) {
	home := t.TempDir()
	ll, personal := filepath.Join(home, ".claude"), filepath.Join(home, ".claude-personal")
	for _, d := range []string{ll, personal} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	orch := Orchestrator{WindowID: "@809", Pane: "%1151", Name: "lead"}
	tests := []struct {
		name       string
		worker     string
		parent     string
		wantOrch   bool
		wantReport bool
	}{
		{"same account keeps orchestrator", ll, ll, true, false},
		{"different account drops it", personal, ll, false, true},
		{"unknown worker dir keeps it", "", ll, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := Request{AgentName: "go-engineer", Mode: ModeDispatch, Ticket: 1, TicketTitle: "t", Orchestrator: orch}
			dropCrossAccountOrchestrator(&req, tt.worker, tt.parent)
			got := buildSystemPrompt(req, modes[ModeDispatch], "/tmp/wt", "agent/x/issue-1", true, nil, "")
			hasPointer := strings.Contains(got, "@809.%1151") || strings.Contains(got, "### Your orchestrator")
			hasSend := strings.Contains(got, "SendMessage to your orchestrator")
			if hasPointer != tt.wantOrch || hasSend != tt.wantOrch {
				t.Errorf("orchestrator pointer=%v send=%v, want %v", hasPointer, hasSend, tt.wantOrch)
			}
			if strings.Contains(got, "different Claude account") != tt.wantReport {
				t.Errorf("cross-account note present=%v, want %v", !tt.wantReport, tt.wantReport)
			}
			if req.CrossAccount != tt.wantReport {
				t.Errorf("CrossAccount = %v", req.CrossAccount)
			}
		})
	}
}

func TestRestoreAccount(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	personal := filepath.Join(home, ".claude-personal")
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	body := `{"accounts":{"ll":{"config_dir":"~/.claude"},"personal":{"config_dir":"~/.claude-personal"}},"default_account":"ll"}`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	config.SetConfigPathForTest(cfgPath)
	t.Cleanup(func() { config.SetConfigPathForTest("") })
	cfg := config.Load()

	r, err := restoreAccount(cfg, "personal", "")
	if err != nil || r.Name != "personal" || r.Dir != personal {
		t.Errorf("recorded account = %+v, %v", r, err)
	}
	if _, err := restoreAccount(cfg, "gone", ""); err == nil {
		t.Error("a removed account must fail the restore, not fall back")
	}
	r, err = restoreAccount(cfg, "", filepath.Join(personal, "projects", "-w", "s.jsonl"))
	if err != nil || r.Name != "personal" {
		t.Errorf("legacy window with a personal transcript = %+v, %v", r, err)
	}
	r, _ = restoreAccount(&config.Config{}, "personal", "")
	if r != (claudehome.Resolution{}) {
		t.Errorf("no accounts configured must keep the inherited env, got %+v", r)
	}
}

func TestPrepareRestoreCarriesAccount(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	cfg := &config.Config{
		Agents:   map[string]config.AgentConfig{},
		Accounts: map[string]config.AccountConfig{"personal": {ConfigDir: "/abs/.claude-personal"}},
	}
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{"accounts":{"personal":{"config_dir":"/abs/.claude-personal"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	config.SetConfigPathForTest(cfgPath)
	t.Cleanup(func() { config.SetConfigPathForTest("") })
	_, env, _, err := prepareRestore(cfg, harness.For(""), RestoreRequest{AgentName: "a", WorkDir: "/w", Account: "personal"})
	if err != nil {
		t.Fatal(err)
	}
	if env["CLAUDE_CONFIG_DIR"] != "/abs/.claude-personal" || env["AGENTCTL_ACCOUNT"] != "personal" {
		t.Errorf("restore env = %v", env)
	}
}
