package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/blsmth/agentctl/internal/claudehome"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/ratelimit"
	"github.com/blsmth/agentctl/internal/shellquote"
)

// credentialEnvVars outrank a config dir's own login. agentctl only checks
// whether they are set; it never reads or prints their values.
var credentialEnvVars = []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"}

// authInfo is the identity `claude auth status --json` reports for a dir.
type authInfo struct {
	LoggedIn         bool
	Email            string
	OrgName          string
	SubscriptionType string
}

// Seams for tests: no test may exec a real claude or tmux.
var (
	accountAuthStatus = claudeAuthStatus
	openLoginPopup    = tmuxLoginPopup
	inTmux            = func() bool { return os.Getenv("TMUX") != "" }
	tmuxGlobalEnvSet  = tmuxGlobalHasEnv
)

// LoginCommand is the command that logs a config dir in. It runs Claude
// Code's own OAuth flow; agentctl never sees the token. Claude Code's
// default dir is logged in with CLAUDE_CONFIG_DIR unset (see
// claudehome.EnvFor), so its command is plain `claude auth login`.
func LoginCommand(dir string) string {
	value, unset := claudehome.EnvFor(dir)
	if unset || value == "" {
		return "claude auth login"
	}
	return "CLAUDE_CONFIG_DIR=" + shellquote.Quote(value) + " claude auth login"
}

// claudeEnv returns the environment for a claude subprocess pointed at dir,
// without inherited credential variables (they would outrank the dir's login).
// Claude Code's default dir is selected by leaving CLAUDE_CONFIG_DIR out.
func claudeEnv(dir string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k == claudehome.EnvConfigDir || containsStr(credentialEnvVars, k) {
			continue
		}
		env = append(env, kv)
	}
	if value, unset := claudehome.EnvFor(dir); !unset && value != "" {
		env = append(env, claudehome.EnvConfigDir+"="+value)
	}
	return env
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// claudeAuthStatus runs `claude auth status --json` against dir.
func claudeAuthStatus(dir string) (authInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, "claude", "auth", "status", "--json")
	c.Env = claudeEnv(dir)
	out, err := c.Output()
	if len(out) == 0 && err != nil {
		return authInfo{}, fmt.Errorf("claude auth status: %w", err)
	}
	return parseAuthStatus(out)
}

// parseAuthStatus reads the identity fields from `claude auth status --json`
// output, at the top level or one object deep.
func parseAuthStatus(data []byte) (authInfo, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return authInfo{}, fmt.Errorf("parsing claude auth status: %w", err)
	}
	str := func(key string) string {
		if v, ok := raw[key].(string); ok {
			return v
		}
		for _, v := range raw {
			if m, ok := v.(map[string]any); ok {
				if s, ok := m[key].(string); ok {
					return s
				}
			}
		}
		return ""
	}
	info := authInfo{Email: str("email"), OrgName: str("orgName"), SubscriptionType: str("subscriptionType")}
	if v, ok := raw["loggedIn"].(bool); ok {
		info.LoggedIn = v
	} else {
		info.LoggedIn = info.Email != ""
	}
	return info, nil
}

// tmuxLoginPopup opens the login command in a tmux popup.
func tmuxLoginPopup(loginCmd string) error {
	return exec.Command("tmux", "display-popup", "-E", "-w", "90%", "-h", "70%",
		"-T", " claude auth login ", loginCmd).Run()
}

// tmuxGlobalHasEnv reports whether the tmux server's global environment sets
// name (launched windows inherit it).
func tmuxGlobalHasEnv(name string) bool {
	out, err := exec.Command("tmux", "show-environment", "-g", name).Output()
	if err != nil {
		return false
	}
	line := strings.TrimSpace(string(out))
	return strings.HasPrefix(line, name+"=") && line != name+"="
}

// linkAction is one planned symlink in an account dir.
type linkAction struct {
	src, dst string
	replace  bool // dst is a symlink to somewhere else
	ok       bool // dst already links to src
}

// planSharedLinks plans the allowlist symlinks from home into dir. Entries
// missing from home are skipped. An existing non-symlink at a destination is
// an error: nothing is overwritten.
func planSharedLinks(home, dir string) ([]linkAction, error) {
	var plan []linkAction
	var conflicts []string
	for _, name := range claudehome.SharedEntries {
		src := filepath.Join(home, name)
		if _, err := os.Lstat(src); err != nil {
			continue
		}
		dst := filepath.Join(dir, name)
		fi, err := os.Lstat(dst)
		switch {
		case errors.Is(err, os.ErrNotExist):
			plan = append(plan, linkAction{src: src, dst: dst})
		case err != nil:
			return nil, fmt.Errorf("checking %s: %w", dst, err)
		case fi.Mode()&os.ModeSymlink == 0:
			conflicts = append(conflicts, dst)
		default:
			target, _ := os.Readlink(dst)
			plan = append(plan, linkAction{src: src, dst: dst, ok: target == src, replace: target != src})
		}
	}
	if len(conflicts) > 0 {
		return nil, fmt.Errorf("refusing to replace existing files (move them away first): %s", strings.Join(conflicts, ", "))
	}
	return plan, nil
}

// applySharedLinks creates the planned symlinks.
func applySharedLinks(plan []linkAction, out io.Writer) error {
	for _, a := range plan {
		if a.ok {
			continue
		}
		if a.replace {
			if err := os.Remove(a.dst); err != nil {
				return fmt.Errorf("replacing symlink %s: %w", a.dst, err)
			}
		}
		if err := os.Symlink(a.src, a.dst); err != nil {
			return fmt.Errorf("linking %s: %w", a.dst, err)
		}
		fmt.Fprintf(out, "  linked %s -> %s\n", a.dst, a.src)
	}
	return nil
}

// AccountAddOptions are the flags of `agentctl account add`.
type AccountAddOptions struct {
	Name      string
	ConfigDir string
	Label     string
	NoLogin   bool // do not open the login popup
}

// RunAccountAdd creates an account dir, shares the allowlist into it from
// ~/.claude, registers it in the global config and points the user at
// Claude Code's own login flow.
func RunAccountAdd(opts AccountAddOptions, out io.Writer) error {
	if !config.ValidAccountName(opts.Name) {
		return fmt.Errorf("invalid account name %q (letters, digits, '.', '_', '-')", opts.Name)
	}
	if strings.TrimSpace(opts.ConfigDir) == "" {
		return errors.New("--config-dir is required")
	}
	dir, err := claudehome.ExpandDir(opts.ConfigDir)
	if err != nil {
		return err
	}
	home := claudehome.HomeDir()
	if home == "" {
		return errors.New("cannot resolve ~/.claude: home directory unknown")
	}
	var plan []linkAction
	if !claudehome.SameDir(dir, home) {
		if plan, err = planSharedLinks(home, dir); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	if err := applySharedLinks(plan, out); err != nil {
		return err
	}
	// Store the path as given when it starts with ~/ so the config stays
	// portable; ExpandDir already proved it resolves.
	stored := opts.ConfigDir
	if !strings.HasPrefix(stored, "~/") && stored != "~" {
		stored = dir
	}
	if err := config.SetAccount(opts.Name, config.AccountConfig{ConfigDir: stored, Label: opts.Label}); err != nil {
		return err
	}
	fmt.Fprintf(out, "Registered account %q (%s).\n", opts.Name, dir)
	login := LoginCommand(dir)
	fmt.Fprintf(out, "Log it in with Claude Code's own flow:\n\n  %s\n\n", login)
	if !opts.NoLogin && inTmux() {
		if err := openLoginPopup(login); err != nil {
			fmt.Fprintf(out, "Could not open the login popup (%v); run the command above.\n", err)
		}
	}
	return nil
}

// RunAccountRm unregisters an account. The dir and its login are left alone.
func RunAccountRm(name string, out io.Writer) error {
	cfg := config.Load()
	dir, _ := claudehome.Dir(name)
	found, err := config.RemoveAccount(name)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("account %q is not configured", name)
	}
	fmt.Fprintf(out, "Unregistered account %q. %s was left in place (delete it yourself if you no longer need its login or history).\n", name, dir)
	for _, ref := range accountRefs(cfg, name) {
		fmt.Fprintf(out, "warning: %s still names %q; launches it applies to will fail until you change it\n", ref, name)
	}
	return nil
}

// accountRefs lists config settings that name account (other than the
// account entry itself and default_account, which RemoveAccount clears).
func accountRefs(cfg *config.Config, account string) []string {
	var refs []string
	for _, r := range cfg.AccountRules {
		if r.Account == account {
			refs = append(refs, fmt.Sprintf("account_rules path %q", r.Path))
		}
	}
	names := cfg.AgentNames()
	sort.Strings(names)
	for _, n := range names {
		if cfg.Agents[n].Account == account {
			refs = append(refs, "agents."+n+".account")
		}
	}
	return refs
}

// RunAccountList prints every account with its login identity and its
// latest pushed 5h/7d usage.
func RunAccountList(out io.Writer) error {
	cfg := config.Load()
	if !cfg.HasAccounts() {
		fmt.Fprintf(out, "No accounts configured; every window uses %s.\n", claudehome.DefaultDir())
		fmt.Fprintln(out, "Add one with: agentctl account add <name> --config-dir <dir>")
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tDIR\tLABEL\tEMAIL\tORG\tPLAN\t5H\t7D")
	for _, a := range claudehome.All() {
		if a.Name == "" {
			continue
		}
		name := a.Name
		if claudehome.IsDefaultAccount(a.Name) {
			name += " *"
		}
		var email, org, plan string
		info, err := accountAuthStatus(a.Dir)
		switch {
		case err != nil:
			email, org, plan = "(unknown)", "-", "-"
		case !info.LoggedIn:
			email, org, plan = "(not logged in)", "-", "-"
		default:
			email, org, plan = orDash(info.Email), orDash(info.OrgName), orDash(info.SubscriptionType)
		}
		fiveH, sevenD := usageCells(a.Name)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			name, abbreviateHome(a.Dir), orDash(a.Label), email, org, plan, fiveH, sevenD)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(out, "* default account")
	return nil
}

// usageNow is the clock for usageCells; tests replace it.
var usageNow = time.Now

// usageCells renders an account's latest pushed 5h and 7d usage. "~" marks
// data older than the freshness window.
//
// Claude Code omits a rate-limit window from the statusLine payload once it
// has expired, so a missing five_hour (or one whose resets_at has passed) means
// no window is open, not "unknown". Showing "-" for that read as a bug. So:
//   - a window whose resets_at is in the past renders "reset" (its stored
//     percentage belongs to the previous window and would be a stale number);
//   - a missing window in a fresh push renders "0%" (Claude only reports a
//     window once one is open; nothing has been used in a new window);
//   - a missing window in stale data, or no data at all, renders "-" (we
//     cannot tell whether a window opened since).
func usageCells(account string) (string, string) {
	pr, err := ratelimit.LoadPushedFor(account, account == claudehome.HomeAccount())
	if err != nil || pr == nil {
		return "-", "-"
	}
	now := usageNow()
	fresh := pr.Recent(now)
	stale := ""
	if !fresh {
		stale = "~"
	}
	cell := func(w *ratelimit.PushedWindow) string {
		if w == nil {
			if fresh {
				return "0%"
			}
			return "-"
		}
		if w.ResetsAt > 0 && w.ResetsAt <= now.Unix() {
			return "reset"
		}
		return fmt.Sprintf("%s%.0f%%", stale, w.UsedPercentage)
	}
	return cell(pr.FiveHour), cell(pr.SevenDay)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func abbreviateHome(p string) string {
	home, err := os.UserHomeDir()
	if err == nil && home != "" && (p == home || strings.HasPrefix(p, home+"/")) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

// RunAccountDoctor checks every account: shared symlinks intact and pointing
// at ~/.claude, sessions/ and projects/ not shared, logged in, no references
// to unknown accounts, and no inherited credential variables that would
// outrank the per-dir logins. It returns an error when it found problems.
func RunAccountDoctor(out io.Writer) error {
	cfg := config.Load()
	problems := 0
	report := func(format string, args ...any) {
		problems++
		fmt.Fprintf(out, "  ✗ "+format+"\n", args...)
	}
	if !cfg.HasAccounts() {
		fmt.Fprintln(out, "No accounts configured.")
	}
	home := claudehome.HomeDir()
	for _, a := range claudehome.All() {
		if a.Name == "" {
			continue
		}
		fmt.Fprintf(out, "%s (%s)\n", a.Name, a.Dir)
		before := problems
		if fi, err := os.Stat(a.Dir); err != nil || !fi.IsDir() {
			report("config dir missing; run: agentctl account add %s --config-dir %s", a.Name, a.Dir)
			continue
		}
		if !claudehome.SameDir(a.Dir, home) {
			checkSharedLinks(home, a.Dir, report)
		}
		for _, name := range claudehome.NeverShared {
			if fi, err := os.Lstat(filepath.Join(a.Dir, name)); err == nil && fi.Mode()&os.ModeSymlink != 0 {
				if name == "agent-memory" {
					report("agent-memory/ is a symlink; Claude Code prompts on every memory write through it. Replace it with a real dir and move the memory of the agents that run on this account into it")
					continue
				}
				report("%s/ is a symlink; accounts must not share it", name)
			}
		}
		info, err := accountAuthStatus(a.Dir)
		switch {
		case err != nil:
			report("login status unknown: %v", err)
		case !info.LoggedIn:
			report("not logged in; run: %s", LoginCommand(a.Dir))
		}
		if problems == before {
			fmt.Fprintln(out, "  ✓ ok")
		}
	}
	for _, ref := range unknownAccountRefs(cfg) {
		report("%s", ref)
	}
	for _, name := range credentialEnvVars {
		if os.Getenv(name) != "" {
			report("%s is set in this environment; it outranks the per-account login (unset it)", name)
		}
		if inTmux() && tmuxGlobalEnvSet(name) {
			report("%s is set in the tmux global environment; windows launched without an account inherit it (tmux set-environment -gu %s)", name, name)
		}
	}
	if problems > 0 {
		return fmt.Errorf("%d problem(s) found", problems)
	}
	fmt.Fprintln(out, "No problems found.")
	return nil
}

// checkSharedLinks reports allowlist entries in dir that are missing, not a
// symlink, or not pointing at the same entry in home.
func checkSharedLinks(home, dir string, report func(string, ...any)) {
	for _, name := range claudehome.SharedEntries {
		src := filepath.Join(home, name)
		if _, err := os.Lstat(src); err != nil {
			continue
		}
		dst := filepath.Join(dir, name)
		fi, err := os.Lstat(dst)
		switch {
		case err != nil:
			report("%s is not linked (re-run `agentctl account add` to repair)", name)
		case fi.Mode()&os.ModeSymlink == 0:
			report("%s is a regular file or dir, not a link to %s (Claude Code may have replaced the link)", name, src)
		default:
			if target, _ := os.Readlink(dst); target != src {
				report("%s links to %s, want %s", name, target, src)
			}
		}
	}
}

// unknownAccountRefs lists settings that name an account that is not
// configured.
func unknownAccountRefs(cfg *config.Config) []string {
	known := func(n string) bool { _, ok := cfg.Accounts[n]; return ok }
	var refs []string
	if cfg.DefaultAccount != "" && !known(cfg.DefaultAccount) {
		refs = append(refs, fmt.Sprintf("default_account names unknown account %q", cfg.DefaultAccount))
	}
	for _, r := range cfg.AccountRules {
		if !known(r.Account) {
			refs = append(refs, fmt.Sprintf("account_rules path %q names unknown account %q", r.Path, r.Account))
		}
	}
	names := cfg.AgentNames()
	sort.Strings(names)
	for _, n := range names {
		if a := cfg.Agents[n].Account; a != "" && !known(a) {
			refs = append(refs, fmt.Sprintf("agents.%s.account names unknown account %q", n, a))
		}
	}
	return refs
}
