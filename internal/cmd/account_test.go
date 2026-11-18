package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
)

// accountEnv isolates HOME, the global config and every exec seam. No test
// here may run a real claude or tmux.
func accountEnv(t *testing.T) (home string, cfgPath string) {
	t.Helper()
	home = t.TempDir()
	if r, err := filepath.EvalSymlinks(home); err == nil {
		home = r
	}
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	for _, k := range credentialEnvVars {
		t.Setenv(k, "")
	}
	cfgPath = filepath.Join(t.TempDir(), "config.json")
	config.SetConfigPathForTest(cfgPath)
	t.Cleanup(func() { config.SetConfigPathForTest("") })

	oldAuth, oldPopup, oldTmux, oldEnv := accountAuthStatus, openLoginPopup, inTmux, tmuxGlobalEnvSet
	accountAuthStatus = func(string) (authInfo, error) {
		return authInfo{LoggedIn: true, Email: "me@example.com", OrgName: "Me", SubscriptionType: "max"}, nil
	}
	openLoginPopup = func(string) error { t.Error("login popup opened outside tmux"); return nil }
	inTmux = func() bool { return false }
	tmuxGlobalEnvSet = func(string) bool { return false }
	t.Cleanup(func() {
		accountAuthStatus, openLoginPopup, inTmux, tmuxGlobalEnvSet = oldAuth, oldPopup, oldTmux, oldEnv
	})
	return home, cfgPath
}

// seedHome creates a ~/.claude with shared and private content.
func seedHome(t *testing.T, home string) string {
	t.Helper()
	ch := filepath.Join(home, ".claude")
	for _, d := range []string{"agents", "shared", "commands", "projects", "sessions", "hooks", "agent-memory"} {
		if err := os.MkdirAll(filepath.Join(ch, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"CLAUDE.md", "settings.json", ".credentials.json", "history.jsonl", "policy-limits.json", ".claude.json"} {
		if err := os.WriteFile(filepath.Join(ch, f), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return ch
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestAccountAddLinksExactlyTheAllowlist(t *testing.T) {
	home, _ := accountEnv(t)
	ch := seedHome(t, home)
	var out bytes.Buffer
	if err := RunAccountAdd(AccountAddOptions{Name: "personal", ConfigDir: "~/.claude-personal", Label: "Personal"}, &out); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".claude-personal")
	// Golden listing: only allowlisted entries that exist in ~/.claude.
	want := []string{"CLAUDE.md", "agents", "commands", "hooks", "settings.json", "shared"}
	if got := listDir(t, dir); !slices.Equal(got, want) {
		t.Errorf("account dir = %v, want %v", got, want)
	}
	for _, name := range want {
		target, err := os.Readlink(filepath.Join(dir, name))
		if err != nil || target != filepath.Join(ch, name) {
			t.Errorf("%s -> %q (%v), want link to %s", name, target, err, filepath.Join(ch, name))
		}
	}
	for _, private := range []string{"agent-memory", ".credentials.json", "sessions", "projects", "history.jsonl", "policy-limits.json", ".claude.json"} {
		if _, err := os.Lstat(filepath.Join(dir, private)); !os.IsNotExist(err) {
			t.Errorf("%s must not be created or linked (err=%v)", private, err)
		}
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v, %v; want 0700", fi.Mode().Perm(), err)
	}
	cfg := config.Load()
	if a := cfg.Accounts["personal"]; a.ConfigDir != "~/.claude-personal" || a.Label != "Personal" {
		t.Errorf("registered %+v", a)
	}
	if !strings.Contains(out.String(), LoginCommand(dir)) || !strings.Contains(LoginCommand(dir), "claude auth login") {
		t.Errorf("login command not printed:\n%s", out.String())
	}

	// Re-running is idempotent.
	if err := RunAccountAdd(AccountAddOptions{Name: "personal", ConfigDir: dir}, &bytes.Buffer{}); err != nil {
		t.Errorf("re-run: %v", err)
	}
}

func TestAccountAddRefusesToOverwrite(t *testing.T) {
	home, _ := accountEnv(t)
	seedHome(t, home)
	dir := filepath.Join(home, ".claude-personal")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := RunAccountAdd(AccountAddOptions{Name: "personal", ConfigDir: dir}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "settings.json") {
		t.Fatalf("err = %v, want refusal naming settings.json", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "settings.json")); string(got) != "mine" {
		t.Errorf("existing file modified: %q", got)
	}
	if _, err := os.Lstat(filepath.Join(dir, "agents")); !os.IsNotExist(err) {
		t.Error("partial links created despite the refusal")
	}
	if config.Load().HasAccounts() {
		t.Error("account registered despite the refusal")
	}
}

func TestAccountAddOpensLoginPopupInTmux(t *testing.T) {
	home, _ := accountEnv(t)
	seedHome(t, home)
	inTmux = func() bool { return true }
	var popped string
	openLoginPopup = func(c string) error { popped = c; return nil }
	if err := RunAccountAdd(AccountAddOptions{Name: "p", ConfigDir: "~/.p"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if popped != LoginCommand(filepath.Join(home, ".p")) {
		t.Errorf("popup ran %q", popped)
	}
}

func TestAccountRmKeepsDir(t *testing.T) {
	home, _ := accountEnv(t)
	seedHome(t, home)
	if err := RunAccountAdd(AccountAddOptions{Name: "p", ConfigDir: "~/.p"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunAccountRm("p", &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".p")); err != nil {
		t.Errorf("rm deleted the dir: %v", err)
	}
	if config.Load().HasAccounts() {
		t.Error("account still registered")
	}
	if err := RunAccountRm("p", &bytes.Buffer{}); err == nil {
		t.Error("removing an unknown account must fail")
	}
}

func TestAccountList(t *testing.T) {
	home, cfgPath := accountEnv(t)
	var out bytes.Buffer
	if err := RunAccountList(&out); err != nil || !strings.Contains(out.String(), "No accounts configured") {
		t.Fatalf("empty list = %q, %v", out.String(), err)
	}
	body := `{"accounts":{"ll":{"config_dir":"~/.claude","label":"LL"},"personal":{"config_dir":"~/.claude-personal"}},"default_account":"ll"}`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	accountAuthStatus = func(dir string) (authInfo, error) {
		if dir == filepath.Join(home, ".claude") {
			return authInfo{LoggedIn: true, Email: "b@ll.com", OrgName: "Lincoln Loop", SubscriptionType: "team"}, nil
		}
		return authInfo{}, nil
	}
	out.Reset()
	if err := RunAccountList(&out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ll *", "b@ll.com", "Lincoln Loop", "team", "personal", "(not logged in)", "~/.claude-personal"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("list missing %q:\n%s", want, out.String())
		}
	}
}

func TestAccountDoctor(t *testing.T) {
	home, _ := accountEnv(t)
	seedHome(t, home)
	if err := RunAccountAdd(AccountAddOptions{Name: "p", ConfigDir: "~/.p"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunAccountDoctor(&out); err != nil {
		t.Fatalf("healthy setup: %v\n%s", err, out.String())
	}

	dir := filepath.Join(home, ".p")
	// Claude Code replaced the settings link with a file; sessions/ got shared;
	// a credential variable leaks in; the account is logged out.
	if err := os.Remove(filepath.Join(dir, "settings.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, ".claude", "sessions"), filepath.Join(dir, "sessions")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, ".claude", "agent-memory"), filepath.Join(dir, "agent-memory")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-not-real")
	accountAuthStatus = func(string) (authInfo, error) { return authInfo{}, nil }
	out.Reset()
	err := RunAccountDoctor(&out)
	if err == nil {
		t.Fatalf("doctor passed a broken setup:\n%s", out.String())
	}
	for _, want := range []string{"settings.json is a regular file", "sessions/ is a symlink", "agent-memory/ is a symlink", "move the memory", "ANTHROPIC_API_KEY is set", "not logged in"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("doctor output missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "sk-test-not-real") {
		t.Error("doctor printed a credential value")
	}
}

func TestParseAuthStatus(t *testing.T) {
	info, err := parseAuthStatus([]byte(`{"loggedIn":true,"email":"a@b.c","orgName":"Org","subscriptionType":"max"}`))
	if err != nil || info != (authInfo{LoggedIn: true, Email: "a@b.c", OrgName: "Org", SubscriptionType: "max"}) {
		t.Errorf("flat = %+v, %v", info, err)
	}
	info, err = parseAuthStatus([]byte(`{"account":{"email":"a@b.c","orgName":"Org"}}`))
	if err != nil || !info.LoggedIn || info.Email != "a@b.c" || info.OrgName != "Org" {
		t.Errorf("nested = %+v, %v", info, err)
	}
	info, _ = parseAuthStatus([]byte(`{"loggedIn":false}`))
	if info.LoggedIn {
		t.Error("loggedIn false ignored")
	}
}

func TestClaudeEnvStripsCredentials(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "x")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "y")
	t.Setenv("CLAUDE_CONFIG_DIR", "/old")
	env := claudeEnv("/new")
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if slices.Contains(credentialEnvVars, k) {
			t.Errorf("%s passed to claude", k)
		}
		if k == "CLAUDE_CONFIG_DIR" && kv != "CLAUDE_CONFIG_DIR=/new" {
			t.Errorf("stale %s", kv)
		}
	}
}

// Registering Claude Code's default dir as an account must not link the
// shared allowlist into itself, and its login hint must not set
// CLAUDE_CONFIG_DIR (an explicit default dir is a separate, empty login).
func TestAccountAddDefaultDirSkipsSelfLink(t *testing.T) {
	for _, configDir := range []string{"~/.claude", "~/.claude/"} {
		t.Run(configDir, func(t *testing.T) {
			home, _ := accountEnv(t)
			ch := seedHome(t, home)
			before := listDir(t, ch)
			var out bytes.Buffer
			if err := RunAccountAdd(AccountAddOptions{Name: "ll", ConfigDir: configDir}, &out); err != nil {
				t.Fatal(err)
			}
			if got := listDir(t, ch); !slices.Equal(got, before) {
				t.Errorf("~/.claude changed: %v, want %v", got, before)
			}
			for _, name := range before {
				if fi, err := os.Lstat(filepath.Join(ch, name)); err != nil || fi.Mode()&os.ModeSymlink != 0 {
					t.Errorf("%s replaced by a link (err=%v)", name, err)
				}
			}
			if strings.Contains(out.String(), "linked") {
				t.Errorf("self-links reported:\n%s", out.String())
			}
			if strings.Contains(out.String(), "CLAUDE_CONFIG_DIR") || !strings.Contains(out.String(), "  claude auth login\n") {
				t.Errorf("default-dir login hint must be plain `claude auth login`:\n%s", out.String())
			}
		})
	}
}

func TestLoginCommand(t *testing.T) {
	home, _ := accountEnv(t)
	tests := []struct {
		dir  string
		want string
	}{
		{filepath.Join(home, ".claude"), "claude auth login"},
		{filepath.Join(home, ".claude") + "/", "claude auth login"},
		{"/abs/.claude-personal", "CLAUDE_CONFIG_DIR='/abs/.claude-personal' claude auth login"},
	}
	for _, tt := range tests {
		if got := LoginCommand(tt.dir); got != tt.want {
			t.Errorf("LoginCommand(%q) = %q, want %q", tt.dir, got, tt.want)
		}
	}
}

// The `account list` / `account doctor` probe for the default dir must run
// claude with CLAUDE_CONFIG_DIR unset, or it reports a logged-in ll as
// logged out.
func TestClaudeEnvDefaultDirUnsetsConfigDir(t *testing.T) {
	home, _ := accountEnv(t)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude-personal"))
	t.Setenv("ANTHROPIC_API_KEY", "x")
	for _, kv := range claudeEnv(filepath.Join(home, ".claude")) {
		k, _, _ := strings.Cut(kv, "=")
		if k == "CLAUDE_CONFIG_DIR" || slices.Contains(credentialEnvVars, k) {
			t.Errorf("%s passed to the default-dir probe", kv)
		}
	}
}
