package claudehome

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
)

// setup points HOME at a temp dir, clears CLAUDE_CONFIG_DIR and makes
// loadConfig return cfg. It returns the home dir.
func setup(t *testing.T, cfg *config.Config) string {
	t.Helper()
	home := t.TempDir()
	if r, err := filepath.EvalSymlinks(home); err == nil {
		home = r
	}
	t.Setenv("HOME", home)
	t.Setenv(EnvConfigDir, "")
	t.Setenv(EnvAccount, "")
	t.Setenv(envSubprocessScrub, "")
	origLoad, origHome := loadConfig, userHomeDir
	loadConfig = func() *config.Config { return cfg }
	userHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { loadConfig, userHomeDir = origLoad, origHome })
	return home
}

func twoAccounts() *config.Config {
	return &config.Config{
		Accounts: map[string]config.AccountConfig{
			"ll":       {ConfigDir: "~/.claude", Label: "Lincoln Loop"},
			"personal": {ConfigDir: "~/.claude-personal", Label: "Personal"},
		},
		DefaultAccount: "ll",
		AccountRules: []config.AccountRule{
			{Path: "~/code/**", Account: "ll"},
			{Path: "~/code/personal/**", Account: "personal"},
			{Path: "~/code/personal/ll-fork", Account: "ll"},
		},
		Agents: map[string]config.AgentConfig{"brandon": {Account: "personal"}},
	}
}

func TestDirDefaults(t *testing.T) {
	home := setup(t, &config.Config{})
	if got, err := Dir(""); err != nil || got != filepath.Join(home, ".claude") {
		t.Errorf("Dir(\"\") = %q, %v; want ~/.claude", got, err)
	}
	t.Setenv(EnvConfigDir, filepath.Join(home, "alt"))
	if got, _ := Dir(""); got != filepath.Join(home, "alt") {
		t.Errorf("Dir(\"\") with CLAUDE_CONFIG_DIR = %q", got)
	}
	if _, err := Dir("nope"); err == nil {
		t.Error("unknown account must be an error")
	}
	if got := All(); len(got) != 1 || got[0].Dir != filepath.Join(home, "alt") || got[0].Name != "" {
		t.Errorf("All() = %+v", got)
	}
}

func TestDirAccounts(t *testing.T) {
	home := setup(t, twoAccounts())
	if got, _ := Dir("personal"); got != filepath.Join(home, ".claude-personal") {
		t.Errorf("Dir(personal) = %q", got)
	}
	if got, _ := Dir(""); got != filepath.Join(home, ".claude") {
		t.Errorf("Dir(\"\") = %q, want default_account dir", got)
	}
	all := All()
	if len(all) != 2 || all[0].Name != "ll" || all[1].Name != "personal" {
		t.Errorf("All() = %+v", all)
	}
	if got := AccountForDir(filepath.Join(home, ".claude-personal") + "/"); got != "personal" {
		t.Errorf("AccountForDir = %q", got)
	}
	if got := AccountForPath(filepath.Join(home, ".claude-personal", "projects", "x", "s.jsonl")); got != "personal" {
		t.Errorf("AccountForPath = %q", got)
	}
	t.Setenv(EnvConfigDir, "~/.claude-personal")
	if got := CurrentAccount(); got != "personal" {
		t.Errorf("CurrentAccount = %q, want personal from CLAUDE_CONFIG_DIR", got)
	}
}

func TestAllIncludesUnclaimedDefaultDir(t *testing.T) {
	home := setup(t, &config.Config{Accounts: map[string]config.AccountConfig{"p": {ConfigDir: "~/.p"}}})
	all := All()
	if len(all) != 2 || all[1].Dir != filepath.Join(home, ".claude") || all[1].Name != "" {
		t.Errorf("All() = %+v", all)
	}
}

func TestResolveOrder(t *testing.T) {
	home := setup(t, nil)
	cfg := twoAccounts()
	ll, personal := filepath.Join(home, ".claude"), filepath.Join(home, ".claude-personal")
	tests := []struct {
		name       string
		req        Request
		wantName   string
		wantDir    string
		wantSource string
	}{
		{"flag wins over everything", Request{Flag: "ll", Agent: "personal", RepoRoot: home + "/code/personal/x", Parent: "personal"}, "ll", ll, SourceFlag},
		{"agent over rule", Request{Agent: "ll", RepoRoot: home + "/code/personal/x", Parent: "personal"}, "ll", ll, SourceAgent},
		{"rule over parent", Request{RepoRoot: home + "/code/personal/x", Parent: "ll"}, "personal", personal, SourceRule},
		{"longest rule wins", Request{RepoRoot: home + "/code/personal/ll-fork"}, "ll", ll, SourceRule},
		{"plain dir rule matches below", Request{RepoRoot: home + "/code/personal/ll-fork/sub"}, "ll", ll, SourceRule},
		{"rule boundary is a path component", Request{RepoRoot: home + "/code/personalx", Parent: "personal"}, "ll", ll, SourceRule},
		{"parent when no rule", Request{RepoRoot: "/srv/other", Parent: "personal"}, "personal", personal, SourceParent},
		{"unknown parent ignored", Request{RepoRoot: "/srv/other", Parent: "gone"}, "ll", ll, SourceDefault},
		{"default_account", Request{RepoRoot: "/srv/other"}, "ll", ll, SourceDefault},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Resolve(cfg, tt.req)
			if err != nil {
				t.Fatal(err)
			}
			if got.Name != tt.wantName || got.Dir != tt.wantDir || got.Source != tt.wantSource {
				t.Errorf("Resolve = %+v, want %s %s %s", got, tt.wantName, tt.wantDir, tt.wantSource)
			}
		})
	}
}

func TestResolveErrors(t *testing.T) {
	setup(t, nil)
	cfg := twoAccounts()
	if _, err := Resolve(cfg, Request{Flag: "ghost"}); err == nil {
		t.Error("unknown flag account must be an error")
	}
	cfg.AccountRules = []config.AccountRule{{Path: "/srv/**", Account: "ghost"}}
	if _, err := Resolve(cfg, Request{RepoRoot: "/srv/x"}); err == nil {
		t.Error("rule naming an unknown account must be an error, not a fallback")
	}
	if _, err := Resolve(&config.Config{}, Request{Flag: "ll"}); err == nil {
		t.Error("--account with no accounts configured must be an error")
	}
	if got, err := Resolve(&config.Config{}, Request{RepoRoot: "/x"}); err != nil || got != (Resolution{}) {
		t.Errorf("no accounts = %+v, %v; want zero", got, err)
	}
}

func TestResolveNoDefaultAccount(t *testing.T) {
	home := setup(t, nil)
	cfg := twoAccounts()
	cfg.DefaultAccount = ""
	got, err := Resolve(cfg, Request{RepoRoot: "/srv/x"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Dir != filepath.Join(home, ".claude") || got.Name != "ll" || got.Source != SourceDefault {
		t.Errorf("Resolve = %+v, want the ~/.claude account", got)
	}
}

func TestMatchRuleGlob(t *testing.T) {
	setup(t, nil)
	rules := []config.AccountRule{{Path: "/srv/*/client", Account: "c"}}
	if got := MatchRule(rules, "/srv/a/client"); got != "c" {
		t.Errorf("glob match = %q", got)
	}
	if got := MatchRule(rules, "/srv/a/b/client"); got != "" {
		t.Errorf("glob must not cross components, got %q", got)
	}
	if got := MatchRule([]config.AccountRule{{Path: "relative/**", Account: "x"}}, "/relative/a"); got != "" {
		t.Errorf("relative rule matched: %q", got)
	}
}

func TestSameDir(t *testing.T) {
	home := setup(t, nil)
	d := filepath.Join(home, "d")
	if err := os.Mkdir(d, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(d, filepath.Join(home, "link")); err != nil {
		t.Fatal(err)
	}
	if !SameDir(d, filepath.Join(home, "link")) || !SameDir(d+"/", d) || SameDir(d, home) {
		t.Error("SameDir mismatch")
	}
}

func TestEnvFor(t *testing.T) {
	home := setup(t, twoAccounts())
	def := filepath.Join(home, ".claude")
	personal := filepath.Join(home, ".claude-personal")
	for _, d := range []string{def, personal} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(home, "claude-link")
	if err := os.Symlink(def, link); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		dir       string
		wantValue string
		wantUnset bool
	}{
		{"default dir", def, "", true},
		{"default dir trailing slash", def + "/", "", true},
		{"default dir via symlink", link, "", true},
		{"default dir with tilde", "~/.claude", "", true},
		{"other dir", personal, personal, false},
		{"empty inherits", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, unset := EnvFor(tt.dir)
			if value != tt.wantValue || unset != tt.wantUnset {
				t.Errorf("EnvFor(%q) = %q, %v; want %q, %v", tt.dir, value, unset, tt.wantValue, tt.wantUnset)
			}
		})
	}
}

// TestCurrent pins Current to the dir the process physically uses:
// $CLAUDE_CONFIG_DIR, else ~/.claude. default_account and AGENTCTL_ACCOUNT
// never change it (regression: with default_account pointing at a
// non-~/.claude account, unset-variable windows were filed under it).
func TestCurrent(t *testing.T) {
	personalDefault := func() *config.Config {
		cfg := twoAccounts()
		cfg.DefaultAccount = "personal"
		return cfg
	}
	tests := []struct {
		name        string
		cfg         *config.Config
		envDir      string // relative to HOME; "" leaves CLAUDE_CONFIG_DIR unset
		envAccount  string
		wantDir     string // relative to HOME
		wantAccount string
	}{
		{name: "var set to other dir", cfg: personalDefault(), envDir: ".claude-personal", wantDir: ".claude-personal", wantAccount: "personal"},
		{name: "var set, default_account is home", cfg: twoAccounts(), envDir: ".claude-personal", wantDir: ".claude-personal", wantAccount: "personal"},
		{name: "var unset, default_account non-home", cfg: personalDefault(), wantDir: ".claude", wantAccount: "ll"},
		{name: "var unset, stale AGENTCTL_ACCOUNT", cfg: personalDefault(), envAccount: "personal", wantDir: ".claude", wantAccount: "ll"},
		{name: "var unset, no accounts", cfg: &config.Config{}, wantDir: ".claude", wantAccount: ""},
		{name: "var set, no accounts", cfg: &config.Config{}, envDir: "alt", wantDir: "alt", wantAccount: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := setup(t, tt.cfg)
			if tt.envDir != "" {
				t.Setenv(EnvConfigDir, filepath.Join(home, tt.envDir))
			}
			t.Setenv(EnvAccount, tt.envAccount)
			if got, want := Current(), filepath.Join(home, tt.wantDir); got != want {
				t.Errorf("Current() = %q, want %q", got, want)
			}
			if got := CurrentAccount(); got != tt.wantAccount {
				t.Errorf("CurrentAccount() = %q, want %q", got, tt.wantAccount)
			}
		})
	}
}

func TestHomeAccount(t *testing.T) {
	cfg := twoAccounts()
	cfg.DefaultAccount = "personal"
	setup(t, cfg)
	t.Setenv(EnvConfigDir, "~/.claude-personal")
	if got := HomeAccount(); got != "ll" {
		t.Errorf("HomeAccount() = %q, want ll", got)
	}
	setup(t, &config.Config{Accounts: map[string]config.AccountConfig{"p": {ConfigDir: "~/.claude-p"}}})
	if got := HomeAccount(); got != "" {
		t.Errorf("HomeAccount() with no ~/.claude account = %q, want \"\"", got)
	}
}

// Claude Code's subprocess env scrub (CLAUDE_CODE_SUBPROCESS_ENV_SCRUB=1)
// drops CLAUDE_CONFIG_DIR from hooks and the statusline but keeps
// AGENTCTL_ACCOUNT; the process must still resolve its own account.
// Outside the scrub AGENTCTL_ACCOUNT is ignored (see TestCurrent).
func TestCurrentUnderSubprocessScrub(t *testing.T) {
	tests := []struct {
		name        string
		cfg         *config.Config
		scrub       string
		configDir   string // relative to HOME; "" = unset
		account     string // AGENTCTL_ACCOUNT
		wantDir     string // relative to HOME
		wantAccount string
	}{
		{"scrubbed: account env restores dir", twoAccounts(), "1", "", "personal", ".claude-personal", "personal"},
		{"scrubbed, true", twoAccounts(), "true", "", "personal", ".claude-personal", "personal"},
		{"scrubbed ~/.claude account", twoAccounts(), "1", "", "ll", ".claude", "ll"},
		{"scrubbed, CLAUDE_CONFIG_DIR still wins", twoAccounts(), "1", ".claude", "personal", ".claude", "ll"},
		{"scrubbed, no account env", twoAccounts(), "1", "", "", ".claude", "ll"},
		{"scrubbed, unknown account ignored", twoAccounts(), "1", "", "gone", ".claude", "ll"},
		{"scrubbed, no accounts configured", &config.Config{}, "1", "", "personal", ".claude", ""},
		{"scrub off", twoAccounts(), "0", "", "personal", ".claude", "ll"},
		{"scrub off, CLAUDE_CONFIG_DIR set", twoAccounts(), "0", ".claude-personal", "personal", ".claude-personal", "personal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := setup(t, tt.cfg)
			if tt.configDir != "" {
				t.Setenv(EnvConfigDir, filepath.Join(home, tt.configDir))
			}
			t.Setenv(EnvAccount, tt.account)
			t.Setenv(envSubprocessScrub, tt.scrub)
			want := filepath.Join(home, tt.wantDir)
			if got := Current(); got != want {
				t.Errorf("Current() = %q, want %q", got, want)
			}
			if got := CurrentAccount(); got != tt.wantAccount {
				t.Errorf("CurrentAccount() = %q, want %q", got, tt.wantAccount)
			}
			if got := CurrentAccountIn(tt.cfg); got != tt.wantAccount {
				t.Errorf("CurrentAccountIn() = %q, want %q", got, tt.wantAccount)
			}
		})
	}
}

func TestParentAccountUnderSubprocessScrub(t *testing.T) {
	setup(t, twoAccounts())
	t.Setenv(EnvAccount, "personal")
	t.Setenv(envSubprocessScrub, "")
	if got := ParentAccount(""); got != "" {
		t.Errorf("ParentAccount without scrub = %q, want \"\"", got)
	}
	t.Setenv(envSubprocessScrub, "1")
	if got := ParentAccount(""); got != "personal" {
		t.Errorf("ParentAccount under scrub = %q, want personal from AGENTCTL_ACCOUNT", got)
	}
}
