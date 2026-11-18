package statusline

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// setupAccounts points HOME/XDG at temp dirs and writes the given config body
// (empty = no config file). It returns the temp HOME.
func setupAccounts(t *testing.T, body string) string {
	t.Helper()
	blockTmuxDeps(t)
	windowstate.SetDirForTest(t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("COLUMNS", "200")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	if body != "" {
		if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	config.SetConfigPathForTest(cfgPath)
	t.Cleanup(func() { config.SetConfigPathForTest("") })
	return home
}

const accountsCfg = `{"accounts":{"ll":{"config_dir":"~/.claude"},"personal":{"config_dir":"~/.claude-personal"}},"default_account":"personal"}`

func runLine(t *testing.T, payload string) string {
	t.Helper()
	var out bytes.Buffer
	RunWithIO(strings.NewReader(payload), &out, true)
	return out.String()
}

const withLimits = `{"cwd":"/tmp/x","rate_limits":{"five_hour":{"used_percentage":26,"resets_at":1738425600},"seven_day":{"used_percentage":9}}}`
const noLimits = `{"cwd":"/tmp/x"}`

func TestStatuslineAccount(t *testing.T) {
	tests := []struct {
		name    string
		cfg     string
		envDir  string // relative to HOME; "" = unset
		payload string
		format  string
		want    string // "" = no account expected
	}{
		{"unset env shows ~/.claude account", accountsCfg, "", withLimits, "rich", "ll"},
		{"personal dir", accountsCfg, ".claude-personal", withLimits, "rich", "personal"},
		{"rich without rate limits uses line 3", accountsCfg, ".claude-personal", noLimits, "rich", "personal"},
		{"complementary", accountsCfg, ".claude-personal", withLimits, "complementary", "personal"},
		{"full", accountsCfg, "", `{"model":{"display_name":"Opus"}}`, "full", "ll"},
		{"minimal unchanged", accountsCfg, "", withLimits, "minimal", ""},
		{"unmatched dir", accountsCfg, ".other", withLimits, "rich", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := setupAccounts(t, `{"statusline_format":"`+tt.format+`",`+tt.cfg[1:])
			if tt.envDir != "" {
				t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, tt.envDir))
			} else {
				os.Unsetenv("CLAUDE_CONFIG_DIR")
			}
			got := runLine(t, tt.payload)
			for _, name := range []string{"ll", "personal"} {
				has := strings.Contains(got, name)
				if (tt.want == name) != has {
					t.Errorf("account %q present = %v, want %v in %q", name, has, tt.want == name, got)
				}
			}
		})
	}
}

func TestStatuslineNoAccountsUnchanged(t *testing.T) {
	for _, format := range []string{"rich", "complementary", "full", "minimal"} {
		t.Run(format, func(t *testing.T) {
			setupAccounts(t, `{"statusline_format":"`+format+`"}`)
			got := runLine(t, withLimits)
			p := mustDecode(t, withLimits)
			var want string
			switch format {
			case "rich":
				want = strings.Join(renderRich(p, 200, nil), "\n")
			default:
				want = renderLine(p, format)
			}
			if strings.TrimRight(got, "\n") != want {
				t.Errorf("got %q, want %q", got, want)
			}
		})
	}
}

func TestRichLine1AccountLeadsAndDropsBeforeFloor(t *testing.T) {
	p := mustDecode(t, withLimits)
	base, _ := renderRichLine1(p, 999)
	p.account = "personal"
	full, _ := renderRichLine1(p, 999)
	if !strings.HasPrefix(full, "\x1b") && !strings.Contains(full, "personal") {
		t.Fatalf("account missing: %q", full)
	}
	if strings.Index(full, "personal") > strings.Index(full, "5h") {
		t.Errorf("account must lead: %q", full)
	}
	// Drop order: 7d goes first, then the account, never the 5h floor.
	w := lipgloss.Width(full)
	noSeven, _ := renderRichLine1(p, w-1)
	if strings.Contains(noSeven, "7d") || !strings.Contains(noSeven, "personal") || !strings.Contains(noSeven, "5h") {
		t.Errorf("want 7d dropped first: %q", noSeven)
	}
	floor, _ := renderRichLine1(p, lipgloss.Width(noSeven)-1)
	if strings.Contains(floor, "personal") || !strings.Contains(floor, "5h") {
		t.Errorf("want account dropped before 5h: %q", floor)
	}
	p.account = ""
	if again, _ := renderRichLine1(p, 999); again != base {
		t.Errorf("empty account changed output")
	}
}

func TestRightAlignWithAccount(t *testing.T) {
	p := mustDecode(t, withLimits)
	p.account = "personal"
	for i, l := range renderRichAligned(p, 100, nil, "right") {
		if w := lipgloss.Width(l); w != 100-rightAlignReserve {
			t.Errorf("line %d width %d, want %d", i, w, 100-rightAlignReserve)
		}
	}
}

func mustDecode(t *testing.T, s string) *Payload {
	t.Helper()
	p, err := Decode([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return p
}
