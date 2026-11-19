// Package claudehome resolves Claude Code config dirs (CLAUDE_CONFIG_DIR),
// one per configured account (see docs/design/accounts.md).
//
// Each account is a directory. Claude Code owns the login and token storage
// for it (a separate keychain entry per dir); agentctl only picks the dir and
// never reads, copies or passes credentials.
//
// With no accounts configured everything resolves to the default dir:
// $CLAUDE_CONFIG_DIR when set, else ~/.claude.
package claudehome

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/blsmth/agentctl/internal/config"
)

// EnvConfigDir is the variable Claude Code reads its config dir from.
const EnvConfigDir = "CLAUDE_CONFIG_DIR"

// EnvAccount carries the resolved account name into a launched window (and
// from there into windowstate).
const EnvAccount = "AGENTCTL_ACCOUNT"

// Account is one configured Claude login.
type Account struct {
	Name  string // "" for the unnamed default dir (no matching account)
	Dir   string // absolute config dir
	Label string
}

// Seams for tests.
var (
	loadConfig  = config.Load
	userHomeDir = os.UserHomeDir
)

// HomeDir returns ~/.claude, the home of the content shared across accounts
// (agents/, shared/, settings.json, …). `account add` symlinks that content
// from here into each account dir. Returns "" when the home dir is unknown.
func HomeDir() string {
	home, err := userHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".claude")
}

// DefaultDir returns Claude Code's own config dir for this process:
// $CLAUDE_CONFIG_DIR when set (made absolute), else, in a process Claude
// Code's subprocess env scrub ran on, the dir of the account named by
// $AGENTCTL_ACCOUNT (see scrubbedAccountDir), else ~/.claude. It is what
// Current returns, and what a launch falls back to when no account applies
// and default_account is unset. It never reflects default_account.
func DefaultDir() string {
	return defaultDirIn(nil)
}

// defaultDirIn is DefaultDir for a caller that may already hold the loaded
// config. cfg may be nil: the config is then loaded only when the
// scrubbed-env fallback is reached.
func defaultDirIn(cfg *config.Config) string {
	if env := os.Getenv(EnvConfigDir); env != "" {
		if d, err := ExpandDir(env); err == nil {
			return d
		}
	}
	if d := scrubbedAccountDir(cfg); d != "" {
		return d
	}
	return HomeDir()
}

// envSubprocessScrub is set on a Claude Code process (see
// harness.subprocessEnvScrub) to make it strip sensitive variables from the
// env of everything it spawns: hooks, the statusline command, Bash tools.
const envSubprocessScrub = "CLAUDE_CODE_SUBPROCESS_ENV_SCRUB"

// scrubbedAccountDir recovers the config dir that Claude Code's subprocess
// env scrub hid from this process: the dir of the configured account named
// by $AGENTCTL_ACCOUNT. It returns "" unless $CLAUDE_CODE_SUBPROCESS_ENV_SCRUB
// is on, or when AGENTCTL_ACCOUNT is unset or names no configured account.
//
// Windows launched without --permission-mode auto run with the scrub on, and
// Claude Code then drops CLAUDE_CONFIG_DIR from its subprocesses while
// AGENTCTL_ACCOUNT and the scrub variable itself survive (checked against
// Claude Code 2.1.327). Every launch sets AGENTCTL_ACCOUNT together with the
// account's CLAUDE_CONFIG_DIR (dispatch.ApplyAccountEnv), so under the scrub
// it names the dir the claude process really uses. Outside the scrub
// CLAUDE_CONFIG_DIR is visible when set, and an absent one means the process
// physically uses ~/.claude whatever AGENTCTL_ACCOUNT says, so the account
// name is not consulted there. With no accounts configured no name matches.
func scrubbedAccountDir(cfg *config.Config) string {
	if !scrubOn() {
		return ""
	}
	name := os.Getenv(EnvAccount)
	if name == "" {
		return ""
	}
	if cfg == nil {
		cfg = loadConfig()
	}
	if cfg == nil {
		return ""
	}
	a, ok := cfg.Accounts[name]
	if !ok {
		return ""
	}
	d, err := ExpandDir(a.ConfigDir)
	if err != nil {
		return ""
	}
	return d
}

// scrubOn reports whether $CLAUDE_CODE_SUBPROCESS_ENV_SCRUB is enabled.
func scrubOn() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(envSubprocessScrub))) {
	case "", "0", "false", "no", "off":
		return false
	}
	return true
}

// ExpandDir expands a leading "~" and returns the cleaned absolute path.
func ExpandDir(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", errors.New("empty path")
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := userHomeDir()
		if err != nil || home == "" {
			return "", fmt.Errorf("resolving ~ in %q: home directory unknown", p)
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("resolving %q: %w", p, err)
	}
	return filepath.Clean(abs), nil
}

// Dir returns the absolute config dir for account. "" is the default account:
// default_account when configured, else DefaultDir(). A name that is not a
// configured account is an error (never a silent fallback).
func Dir(account string) (string, error) {
	return dirIn(loadConfig(), account)
}

func dirIn(cfg *config.Config, account string) (string, error) {
	if account == "" {
		account = cfg.DefaultAccount
		if account == "" {
			if d := defaultDirIn(cfg); d != "" {
				return d, nil
			}
			return "", errors.New("cannot resolve the Claude config dir: home directory unknown")
		}
	}
	a, ok := cfg.Accounts[account]
	if !ok {
		return "", fmt.Errorf("account %q is not configured (see `agentctl account list`)", account)
	}
	return ExpandDir(a.ConfigDir)
}

// Current returns the config dir the running process itself uses: an
// explicit $CLAUDE_CONFIG_DIR (hooks and the statusline inherit it from the
// claude process that spawned them), else, when Claude Code's subprocess env
// scrub dropped that variable, the $AGENTCTL_ACCOUNT account's dir (see
// scrubbedAccountDir), else ~/.claude (HomeDir). Returns "" when nothing
// resolves.
//
// default_account is never consulted: it picks where a new launch goes
// (Resolve/ForLaunch), not where a running process is. A claude process with
// CLAUDE_CONFIG_DIR unset is using Claude Code's default dir, ~/.claude,
// whatever default_account says: windows on a ~/.claude account are launched
// with the variable unset (see EnvFor), and so is every window launched
// before accounts were configured.
func Current() string {
	return DefaultDir()
}

// CurrentAccount returns the account name of the running process (see
// Current), or "" when its dir matches no configured account.
func CurrentAccount() string {
	return AccountForDir(Current())
}

// CurrentAccountIn is CurrentAccount for a caller that already holds the
// loaded config, saving a second config load on hot paths.
func CurrentAccountIn(cfg *config.Config) string {
	return accountForDirIn(cfg, defaultDirIn(cfg))
}

// HomeAccount returns the configured account whose dir is ~/.claude
// (HomeDir), or "" when none is. Processes that ran before accounts were
// configured used that dir, so it owns pre-accounts data such as the single
// ratelimits.json.
func HomeAccount() string {
	return AccountForDir(HomeDir())
}

// All returns every configured account, sorted by name, plus an unnamed entry
// for DefaultDir() when no account uses it (that dir still holds history
// from before accounts were configured). With no accounts it returns the
// default dir alone. Code that scans transcripts across accounts uses this.
func All() []Account {
	return allIn(loadConfig())
}

func allIn(cfg *config.Config) []Account {
	var out []Account
	seen := map[string]bool{}
	names := make([]string, 0, len(cfg.Accounts))
	for name := range cfg.Accounts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		d, err := ExpandDir(cfg.Accounts[name].ConfigDir)
		if err != nil || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, Account{Name: name, Dir: d, Label: cfg.Accounts[name].Label})
	}
	if d := defaultDirIn(cfg); d != "" && !seen[d] {
		out = append(out, Account{Dir: d})
	}
	return out
}

// ProjectsDirs returns the projects/ dir of every account (see All).
func ProjectsDirs() []string {
	var out []string
	for _, a := range All() {
		out = append(out, filepath.Join(a.Dir, "projects"))
	}
	return out
}

// AccountForDir returns the name of the configured account whose config dir
// is dir (compared after cleaning and resolving symlinks), or "".
func AccountForDir(dir string) string {
	return accountForDirIn(loadConfig(), dir)
}

func accountForDirIn(cfg *config.Config, dir string) string {
	if dir == "" {
		return ""
	}
	want := canonical(dir)
	names := make([]string, 0, len(cfg.Accounts))
	for name := range cfg.Accounts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		d, err := ExpandDir(cfg.Accounts[name].ConfigDir)
		if err == nil && canonical(d) == want {
			return name
		}
	}
	return ""
}

// AccountForPath returns the account whose config dir contains path (for
// example a transcript under <dir>/projects/), or "" when none does.
func AccountForPath(path string) string {
	cfg := loadConfig()
	p := canonical(path)
	best, bestLen := "", 0
	for _, a := range allIn(cfg) {
		d := canonical(a.Dir)
		if a.Name != "" && (p == d || strings.HasPrefix(p, d+string(filepath.Separator))) && len(d) > bestLen {
			best, bestLen = a.Name, len(d)
		}
	}
	return best
}

// canonical cleans p and resolves symlinks when it exists.
func canonical(p string) string {
	if abs, err := ExpandDir(p); err == nil {
		p = abs
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// IsDefaultAccount reports whether name is the default account:
// default_account, or with none set, the account whose dir is DefaultDir().
func IsDefaultAccount(name string) bool {
	if name == "" {
		return false
	}
	cfg := loadConfig()
	if cfg.DefaultAccount != "" {
		return cfg.DefaultAccount == name
	}
	return accountForDirIn(cfg, defaultDirIn(cfg)) == name
}
