package dispatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/claudehome"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/harness"
)

func TestApplyAccountEnv(t *testing.T) {
	env := map[string]string{}
	ApplyAccountEnv(env, claudehome.Resolution{})
	if len(env) != 0 {
		t.Errorf("zero resolution changed env: %v", env)
	}
	ApplyAccountEnv(env, claudehome.Resolution{Name: "personal", Dir: "/h/.claude-personal"})
	if env["CLAUDE_CONFIG_DIR"] != "/h/.claude-personal" || env["AGENTCTL_ACCOUNT"] != "personal" {
		t.Errorf("env = %v", env)
	}
	if !filepath.IsAbs(env["CLAUDE_CONFIG_DIR"]) {
		t.Error("CLAUDE_CONFIG_DIR must be absolute")
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
