package claudehome

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/windowstate"
	"github.com/blsmth/agentctl/internal/worktree"
)

// Resolution sources, in precedence order.
const (
	SourceFlag    = "flag"
	SourceAgent   = "agent"
	SourceRule    = "rule"
	SourceParent  = "parent"
	SourceDefault = "default"
)

// Request holds the inputs to Resolve.
type Request struct {
	// Flag is the --account value.
	Flag string
	// Agent is agents.<name>.account for the launched agent.
	Agent string
	// RepoRoot is the main repo root the agent works in (worktrees resolve to
	// their main repo root before matching account_rules).
	RepoRoot string
	// Parent is the dispatching window's account, "" when unknown.
	Parent string
}

// Resolution is the account a launch runs under.
type Resolution struct {
	Name   string // "" when no accounts are configured
	Dir    string // absolute config dir; "" when no accounts are configured
	Source string // one of the Source* constants; "" when no accounts are configured
}

// Resolve picks the account for a launch:
//
//	--account flag > agents.<name>.account > account_rules on the repo root
//	(longest match) > dispatching window's account > default_account
//
// With no accounts configured it returns the zero Resolution (the launch
// keeps its inherited environment); a flag is then an error. A name that is
// not configured is an error: there is no fallback to another account.
// When nothing matches and default_account is unset, the account whose dir is
// DefaultDir() is used (Name may be "" when no account uses it).
func Resolve(cfg *config.Config, req Request) (Resolution, error) {
	if !cfg.HasAccounts() {
		if req.Flag != "" {
			return Resolution{}, fmt.Errorf("--account %q: no accounts are configured (see `agentctl account add`)", req.Flag)
		}
		return Resolution{}, nil
	}
	name, source := pick(cfg, req)
	if name == "" {
		d := DefaultDir()
		if d == "" {
			return Resolution{}, fmt.Errorf("cannot resolve the default Claude config dir")
		}
		return Resolution{Name: accountForDirIn(cfg, d), Dir: d, Source: SourceDefault}, nil
	}
	d, err := dirIn(cfg, name)
	if err != nil {
		return Resolution{}, fmt.Errorf("%s account: %w", source, err)
	}
	return Resolution{Name: name, Dir: d, Source: source}, nil
}

// pick returns the winning account name and its source.
func pick(cfg *config.Config, req Request) (string, string) {
	if req.Flag != "" {
		return req.Flag, SourceFlag
	}
	if req.Agent != "" {
		return req.Agent, SourceAgent
	}
	if r := MatchRule(cfg.AccountRules, req.RepoRoot); r != "" {
		return r, SourceRule
	}
	if req.Parent != "" {
		if _, ok := cfg.Accounts[req.Parent]; ok {
			return req.Parent, SourceParent
		}
	}
	return cfg.DefaultAccount, SourceDefault
}

// MatchRule returns the account of the rule matching root with the longest
// (expanded) path, or "" when none matches.
func MatchRule(rules []config.AccountRule, root string) string {
	if root == "" {
		return ""
	}
	root = filepath.Clean(root)
	best, bestLen := "", -1
	for _, r := range rules {
		pat, ok := expandPattern(r.Path)
		if !ok || !matchPath(pat, root) {
			continue
		}
		if len(pat) > bestLen {
			best, bestLen = r.Account, len(pat)
		}
	}
	return best
}

func expandPattern(p string) (string, bool) {
	p = strings.TrimSpace(p)
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := userHomeDir()
		if err != nil || home == "" {
			return "", false
		}
		p = home + strings.TrimPrefix(p, "~")
	}
	if !filepath.IsAbs(p) {
		return "", false
	}
	return p, true
}

// matchPath matches root against pat: "<dir>/**" and a plain directory match
// the directory itself and everything below it; other patterns are
// filepath.Match globs on the whole root.
func matchPath(pat, root string) bool {
	if base, ok := strings.CutSuffix(pat, "/**"); ok {
		return underDir(filepath.Clean(base), root)
	}
	if strings.ContainsAny(pat, "*?[") {
		m, err := filepath.Match(filepath.Clean(pat), root)
		return err == nil && m
	}
	return underDir(filepath.Clean(pat), root)
}

func underDir(dir, p string) bool {
	return p == dir || strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/")
}

// ParentAccount returns the account of the dispatching window: its recorded
// windowstate account, else the dispatching process's own CLAUDE_CONFIG_DIR
// when that is explicitly set. "" when unknown.
func ParentAccount(windowID string) string {
	if windowID != "" {
		if ws, err := windowstate.Read(windowID); err == nil && ws.Account != "" {
			return ws.Account
		}
	}
	if env := os.Getenv(EnvConfigDir); env != "" {
		return AccountForDir(env)
	}
	return ""
}

// ParentDir returns the config dir the dispatching window runs under:
// parent's dir when parent names an account, else this process's Current().
func ParentDir(parent string) string {
	if parent != "" {
		if d, err := Dir(parent); err == nil {
			return d
		}
	}
	return Current()
}

// ForLaunch resolves the account for launching agentName in workDir.
// flag is --account; parent the dispatching window's account (ParentAccount).
func ForLaunch(cfg *config.Config, flag, agentName, workDir, parent string) (Resolution, error) {
	if !cfg.HasAccounts() {
		return Resolve(cfg, Request{Flag: flag})
	}
	root := ""
	if workDir != "" {
		root = worktree.MainRepoRoot(workDir)
	}
	return Resolve(cfg, Request{
		Flag:     flag,
		Agent:    cfg.ForAgent(agentName).Account,
		RepoRoot: root,
		Parent:   parent,
	})
}

// SameDir reports whether two config dirs are the same directory.
func SameDir(a, b string) bool {
	if a == "" || b == "" {
		return a == b
	}
	return canonical(a) == canonical(b)
}
