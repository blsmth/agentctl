package statusline

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/ratelimit"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// Two accounts pushing rate limits must not overwrite each other: each
// statusline process writes the file of the account its CLAUDE_CONFIG_DIR
// belongs to.
func TestRunWithIO_PersistsRateLimitsPerAccount(t *testing.T) {
	blockTmuxDeps(t)
	windowstate.SetDirForTest(t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	body := `{"accounts":{"ll":{"config_dir":"~/.claude"},"personal":{"config_dir":"~/.claude-personal"}},"default_account":"ll"}`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	config.SetConfigPathForTest(cfgPath)
	t.Cleanup(func() { config.SetConfigPathForTest("") })

	push := func(configDir, pct string) {
		t.Setenv("CLAUDE_CONFIG_DIR", configDir)
		payload := `{"rate_limits":{"five_hour":{"used_percentage":` + pct + `,"resets_at":1738425600}}}`
		var out bytes.Buffer
		RunWithIO(strings.NewReader(payload), &out, false)
	}
	push(filepath.Join(home, ".claude"), "10")
	push(filepath.Join(home, ".claude-personal"), "90")

	for name, want := range map[string]float64{"ll": 10, "personal": 90} {
		pr, err := ratelimit.LoadPushedFor(name, false)
		if err != nil || pr == nil || pr.FiveHour == nil || pr.FiveHour.UsedPercentage != want {
			t.Errorf("account %s = %+v, %v; want five_hour %v", name, pr, err, want)
		}
	}
}
