package config

import (
	"fmt"
	"regexp"
	"strings"
)

// AccountConfig is one Claude login: a Claude Code config dir
// (CLAUDE_CONFIG_DIR). Claude Code owns the login and token storage for the
// dir; agentctl only chooses which dir a window gets.
type AccountConfig struct {
	// ConfigDir is the Claude Code config dir. "~/" is expanded; relative
	// paths are rejected at load.
	ConfigDir string `json:"config_dir"`
	// Label is a human-readable description (e.g. "Personal (Max)").
	Label string `json:"label,omitempty"`
}

// AccountRule routes launches whose repo root matches Path to Account.
type AccountRule struct {
	// Path is a directory or glob on the repo root. "~/" is expanded. A
	// trailing "/**" (or a plain directory) matches the directory and
	// everything below it; other patterns use filepath.Match.
	Path    string `json:"path"`
	Account string `json:"account"`
}

// accountNameRe is the charset for account names. Names become file name
// components (the per-account rate-limit file) and env values.
var accountNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidAccountName reports whether name is usable as an account name.
func ValidAccountName(name string) bool { return accountNameRe.MatchString(name) }

// validate checks one account entry.
func (a AccountConfig) validate() error {
	dir := strings.TrimSpace(a.ConfigDir)
	if dir == "" {
		return fmt.Errorf("config_dir is required")
	}
	if dir != "~" && !strings.HasPrefix(dir, "~/") && !strings.HasPrefix(dir, "/") {
		return fmt.Errorf("config_dir %q must be absolute or start with ~/", a.ConfigDir)
	}
	return nil
}

// HasAccounts reports whether any account is configured. With none, launches
// keep the inherited environment (no CLAUDE_CONFIG_DIR is set).
func (c Config) HasAccounts() bool { return len(c.Accounts) > 0 }

// validateAccounts drops invalid accounts and rules (logging why) and warns
// about references to accounts that do not exist. References are kept, not
// dropped: resolving them fails the launch rather than silently falling back
// to another account.
func validateAccounts(cfg *Config) {
	for name, a := range cfg.Accounts {
		err := a.validate()
		if err == nil && !ValidAccountName(name) {
			err = fmt.Errorf("account name must match %s", accountNameRe)
		}
		if err != nil {
			logOnce(fmt.Sprintf("config: ignoring account %q: %v", name, err))
			delete(cfg.Accounts, name)
		}
	}
	known := func(name string) bool { _, ok := cfg.Accounts[name]; return ok }
	if cfg.DefaultAccount != "" && !known(cfg.DefaultAccount) {
		logOnce(fmt.Sprintf("config: default_account %q is not a configured account; launches that fall back to it will fail", cfg.DefaultAccount))
	}
	rules := cfg.AccountRules[:0:0]
	for _, r := range cfg.AccountRules {
		if strings.TrimSpace(r.Path) == "" || r.Account == "" {
			logOnce(fmt.Sprintf("config: ignoring account_rules entry %+v: path and account are required", r))
			continue
		}
		if !known(r.Account) {
			logOnce(fmt.Sprintf("config: account_rules path %q references unknown account %q; launches it matches will fail", r.Path, r.Account))
		}
		rules = append(rules, r)
	}
	cfg.AccountRules = rules
	for name, ac := range cfg.Agents {
		if ac.Account != "" && !known(ac.Account) {
			logOnce(fmt.Sprintf("config: agent %q references unknown account %q; launching it will fail", name, ac.Account))
		}
	}
}

// sanitizeProjectAccounts strips the global-only account keys from a
// project-local layer (agents.*.account is handled per agent in
// sanitizeProjectConfig).
func sanitizeProjectAccounts(project *Config, warn func(key, why string)) {
	const globalOnly = "only honored in the global config"
	if len(project.Accounts) > 0 {
		warn("accounts", globalOnly)
	}
	if project.DefaultAccount != "" {
		warn("default_account", globalOnly)
	}
	if len(project.AccountRules) > 0 {
		warn("account_rules", globalOnly)
	}
	project.Accounts, project.DefaultAccount, project.AccountRules = nil, "", nil
}

// SetAccount registers (or replaces) an account in the global config.
func SetAccount(name string, a AccountConfig) error {
	if !ValidAccountName(name) {
		return fmt.Errorf("invalid account name %q (want %s)", name, accountNameRe)
	}
	if err := a.validate(); err != nil {
		return fmt.Errorf("account %q: %w", name, err)
	}
	return updateLayer(globalConfigPath, func(layer *Config) {
		if layer.Accounts == nil {
			layer.Accounts = map[string]AccountConfig{}
		}
		layer.Accounts[name] = a
	})
}

// RemoveAccount unregisters an account from the global config. It reports
// whether the account existed. A default_account naming it is cleared; rules
// and agents that reference it are left alone (launches they match fail
// loudly instead of moving to another account).
func RemoveAccount(name string) (bool, error) {
	var found bool
	err := updateLayer(globalConfigPath, func(layer *Config) {
		if _, ok := layer.Accounts[name]; !ok {
			return
		}
		found = true
		delete(layer.Accounts, name)
		if layer.DefaultAccount == name {
			layer.DefaultAccount = ""
		}
	})
	return found, err
}
