package hooks

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/dispatchplan"
	"github.com/blsmth/agentctl/internal/harness"
	"github.com/blsmth/agentctl/internal/remoteapproval"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// dispatchFlags are the flags of a gated dispatch that decide where and how
// it runs.
type dispatchFlags struct {
	plan    dispatchplan.Input
	account string
}

// parseDispatchPlanFlags reads the where/how flags of the gated dispatch in
// command. The last occurrence of a flag wins, as in the CLI; the values of
// other value flags (a prompt mentioning --dir) are skipped.
func parseDispatchPlanFlags(command string) dispatchFlags {
	var f dispatchFlags
	tokens := dispatchTokens(command)
	str := map[string]*string{
		"--agent": &f.plan.Agent, "-a": &f.plan.Agent,
		"--dir": &f.plan.Dir, "--model": &f.plan.Model, "-m": &f.plan.Model,
		"--backend": &f.plan.Backend, "--harness": &f.plan.Harness, "--account": &f.account,
	}
	num := map[string]*int{"--issue": &f.plan.Issue, "-i": &f.plan.Issue, "--review-pr": &f.plan.ReviewPR}
	set := func(name, value string) {
		if p, ok := str[name]; ok {
			*p = value
		} else if p, ok := num[name]; ok {
			*p, _ = strconv.Atoi(value)
		}
	}
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		if tok == "--no-worktree" {
			f.plan.NoWorktree = true
			continue
		}
		if name, value, ok := strings.Cut(tok, "="); ok && strings.HasPrefix(name, "--") {
			set(name, value)
			continue
		}
		if isDispatchValueFlag(tok) && i+1 < len(tokens) {
			set(tok, tokens[i+1])
			i++
		}
	}
	return f
}

// askingCwd is the directory the dispatch command runs in: the Bash tool's
// cwd from the payload, else the asking window's recorded work dir (resolved
// from the pane, not from inherited AGENTCTL_* env), else this process's.
func askingCwd(payloadCwd string) string {
	if payloadCwd != "" {
		return payloadCwd
	}
	if id := resolveHookWindowID(); id != "" {
		if ws, err := windowstate.Read(id); err == nil && ws.WorkDir != "" {
			return ws.WorkDir
		}
	}
	cwd, _ := os.Getwd()
	return cwd
}

// resolveDispatchDetails works out where and how the dispatch in command will
// run, for the approval popup and the phone. Best effort: it never fails, and
// anything it cannot resolve is left empty.
func resolveDispatchDetails(cfg *config.Config, command, cwd string) remoteapproval.Details {
	f := parseDispatchPlanFlags(command)
	f.plan.Cwd = cwd
	p := dispatchplan.Resolve(cfg, f.plan)

	d := remoteapproval.Details{
		Agent:          p.Agent,
		AgentSource:    p.AgentSource,
		Model:          p.Model,
		ModelSource:    p.ModelSource,
		Backend:        p.Backend,
		PermissionMode: p.PermissionMode,
		Mode:           planModeText(p, f.plan),
		Worktree:       "worktree",
	}
	if !p.Worktree {
		d.Worktree = "no worktree"
	}
	if d.Model == "" {
		d.Model = "default"
	}
	if p.BackendErr != "" {
		d.Backend += " (not configured)"
	}
	switch {
	case p.HarnessErr != "":
		d.Harness = p.Harness + " (unknown)"
	case p.Harness != harness.Claude:
		d.Harness = p.Harness
	}
	if root := dispatchplan.ProjectRoot(p.WorkDir); root != "" {
		d.Project = filepath.Base(root)
		d.Dir = dispatchplan.AbbrevHome(p.WorkDir)
		d.DirSource = p.DirSource
		if asking := dispatchplan.ProjectRoot(cwd); asking != "" {
			d.AskingProject = filepath.Base(asking)
			d.OtherProject = asking != root
		}
	}
	if p.Accounts && cfg.HasAccounts() {
		d.Account = previewAccount(cfg, f.account, p.Agent, p.WorkDir)
	}
	return d
}

// planModeText describes the dispatch mode for the approval.
func planModeText(p dispatchplan.Plan, in dispatchplan.Input) string {
	switch p.Mode {
	case dispatchplan.ModeIssue:
		return fmt.Sprintf("issue #%d", in.Issue)
	case dispatchplan.ModeReviewPR:
		return fmt.Sprintf("review PR #%d", in.ReviewPR)
	}
	return "ad-hoc"
}
