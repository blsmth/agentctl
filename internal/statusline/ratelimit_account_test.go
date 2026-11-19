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

// A window launched without --permission-mode auto runs claude with
// CLAUDE_CODE_SUBPROCESS_ENV_SCRUB=1, which drops CLAUDE_CONFIG_DIR from the
// statusline's env but keeps AGENTCTL_ACCOUNT. The label and the rate-limit
// file must still name the window's account, not the ~/.claude one.
func TestRunWithIO_ScrubbedEnvUsesAgentctlAccount(t *testing.T) {
	const cfg = `{"statusline_format":"rich","accounts":{"ll":{"config_dir":"~/.claude"},"personal":{"config_dir":"~/.claude-personal"}},"default_account":"ll"}`
	tests := []struct {
		name      string
		cfg       string
		scrub     string
		configDir string // relative to HOME; "" = unset
		account   string
		want      string // account in the label and file name; "" = none
	}{
		{"scrubbed personal window", cfg, "1", "", "personal", "personal"},
		{"scrubbed ~/.claude window", cfg, "1", "", "ll", "ll"},
		{"config dir set wins", cfg, "1", ".claude", "personal", "ll"},
		{"unscrubbed personal window", cfg, "0", ".claude-personal", "personal", "personal"},
		{"no accounts configured", `{"statusline_format":"rich"}`, "1", "", "personal", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := setupAccounts(t, tt.cfg)
			if tt.configDir != "" {
				t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, tt.configDir))
			} else {
				os.Unsetenv("CLAUDE_CONFIG_DIR")
			}
			t.Setenv("CLAUDE_CODE_SUBPROCESS_ENV_SCRUB", tt.scrub)
			t.Setenv("AGENTCTL_ACCOUNT", tt.account)

			var out bytes.Buffer
			RunWithIO(strings.NewReader(withLimits), &out, false)
			got := out.String()
			for _, name := range []string{"ll", "personal"} {
				if has := strings.Contains(got, name); has != (tt.want == name) {
					t.Errorf("label %q present = %v, want %v in %q", name, has, tt.want == name, got)
				}
			}

			pr, err := ratelimit.LoadPushedFor(tt.want, false)
			if err != nil || pr == nil || pr.FiveHour == nil || pr.FiveHour.UsedPercentage != 26 {
				t.Errorf("ratelimits for %q = %+v, %v; want five_hour 26", tt.want, pr, err)
			}
			for _, other := range []string{"ll", "personal"} {
				if other == tt.want {
					continue
				}
				if pr, _ := ratelimit.LoadPushedFor(other, false); pr != nil {
					t.Errorf("ratelimits leaked into account %q: %+v", other, pr)
				}
			}
		})
	}
}
