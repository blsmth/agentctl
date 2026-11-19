package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/claudehome"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/windowstate"
)

func TestParseDispatchAccountFlags(t *testing.T) {
	tests := []struct {
		cmd, account, dir string
	}{
		{"agentctl dispatch --issue 1", "", ""},
		{"agentctl dispatch --issue 1 --account personal --dir ~/code/x", "personal", "~/code/x"},
		{"agentctl dispatch --account=ll --dir=/srv/y --prompt 'hi --account x'", "ll", "/srv/y"},
	}
	for _, tt := range tests {
		a, d := parseDispatchAccountFlags(tt.cmd)
		if a != tt.account || d != tt.dir {
			t.Errorf("%q = (%q, %q), want (%q, %q)", tt.cmd, a, d, tt.account, tt.dir)
		}
	}
}

func TestDispatchPreviewAccount(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	config.SetConfigPathForTest(cfgPath)
	t.Cleanup(func() { config.SetConfigPathForTest("") })
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("AGENTCTL_WINDOW_ID", "")

	if err := os.WriteFile(cfgPath, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := dispatchPreviewAccount("go-engineer", "agentctl dispatch --issue 1"); got != "" {
		t.Errorf("no accounts = %q, want empty", got)
	}

	body := `{"accounts":{"ll":{"config_dir":"/abs/ll","label":"Lincoln Loop"},"personal":{"config_dir":"/abs/p"}},"default_account":"ll"}`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var gotFlag, gotDir string
	old := resolvePreviewAccount
	t.Cleanup(func() { resolvePreviewAccount = old })
	resolvePreviewAccount = func(cfg *config.Config, flag, agent, workDir, parent string) (claudehome.Resolution, error) {
		gotFlag, gotDir = flag, workDir
		return claudehome.Resolve(cfg, claudehome.Request{Flag: flag, Parent: parent})
	}
	if got := dispatchPreviewAccount("go-engineer", "agentctl dispatch --issue 1 --dir /srv/x"); got != "ll (Lincoln Loop)" {
		t.Errorf("default = %q", got)
	}
	if gotDir != "/srv/x" {
		t.Errorf("workDir = %q, want --dir", gotDir)
	}
	if got := dispatchPreviewAccount("go-engineer", "agentctl dispatch --issue 1 --account personal"); got != "personal" || gotFlag != "personal" {
		t.Errorf("--account personal = %q (flag %q)", got, gotFlag)
	}
	if got := dispatchPreviewAccount("go-engineer", "agentctl dispatch --issue 1 --account ghost"); !strings.HasPrefix(got, "error: ") {
		t.Errorf("unknown account = %q, want an error", got)
	}
}

func TestSessionLogPath(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/cfg")
	tests := []struct {
		name, sid, cwd, transcript, want string
	}{
		{"transcript wins", "s1", "/w", "/acct/projects/-w/s1.jsonl", "/acct/projects/-w/s1.jsonl"},
		{"mismatched transcript ignored", "s1", "/w", "/acct/projects/-w/other.jsonl", "/cfg/projects/-w/s1.jsonl"},
		{"relative transcript ignored", "s1", "/w", "s1.jsonl", "/cfg/projects/-w/s1.jsonl"},
		{"derived from this config dir", "s1", "/w", "", "/cfg/projects/-w/s1.jsonl"},
		{"nothing usable", "s1", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sessionLogPath(tt.sid, tt.cwd, tt.transcript); got != tt.want {
				t.Errorf("sessionLogPath = %q, want %q", got, tt.want)
			}
		})
	}
}

// The preview hook runs with Claude Code's env, whose AGENTCTL_WINDOW_ID can
// name another window (inherited at launch). The parent account must come
// from this window, resolved by session ID or pane, not from that value.
func TestDispatchPreviewAccountIgnoresInheritedWindowID(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	config.SetConfigPathForTest(cfgPath)
	t.Cleanup(func() { config.SetConfigPathForTest("") })
	body := `{"accounts":{"ll":{"config_dir":"/abs/ll"},"personal":{"config_dir":"/abs/p"}},"default_account":"ll"}`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	windowstate.SetDirForTest(t.TempDir())
	t.Cleanup(func() { windowstate.SetDirForTest("") })
	for id, ws := range map[string]windowstate.WindowState{
		"@943":  {Account: "ll", SessionID: "other-session"},
		"@1125": {Account: "personal", SessionID: "own-session"},
	} {
		if err := windowstate.Write(id, ws); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_WINDOW_ID", "@943")
	t.Setenv("AGENTCTL_SESSION_ID", "own-session")

	old := resolvePreviewAccount
	t.Cleanup(func() { resolvePreviewAccount = old })
	var gotParent string
	resolvePreviewAccount = func(cfg *config.Config, flag, agent, workDir, parent string) (claudehome.Resolution, error) {
		gotParent = parent
		return claudehome.Resolve(cfg, claudehome.Request{Flag: flag, Parent: parent})
	}
	dispatchPreviewAccount("go-engineer", "agentctl dispatch --issue 1 --dir /srv/x")
	if gotParent != "personal" {
		t.Errorf("parent account = %q, want personal (this window's, not @943's)", gotParent)
	}
}
