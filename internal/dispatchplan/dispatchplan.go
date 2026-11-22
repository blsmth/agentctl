// Package dispatchplan resolves where and how an `agentctl dispatch` will
// run (project dir, agent, model, backend, harness, permission mode,
// worktree) without launching anything, and holds the precedence rules the
// launch itself uses, so the approval popup and the phone show what the
// launch will do.
//
// It is a leaf below internal/dispatch (which imports internal/hooks, so the
// hooks cannot import dispatch): internal/dispatch and internal/cmd call the
// precedence helpers here, and the approval hook calls Resolve. Resolve never
// touches the network, spawns nothing and never fails: anything it cannot
// work out is left empty.
package dispatchplan

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/agentlabel"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/harness"
	"github.com/blsmth/agentctl/internal/launchopts"
	"github.com/blsmth/agentctl/internal/paths"
)

// DefaultWorkerPermissionMode is the claude --permission-mode used for
// worker dispatches (issue, ad-hoc, PR review) when the agent config does
// not set one.
const DefaultWorkerPermissionMode = "auto"

// IssueDefaultModel is the model an issue dispatch (dispatch.ModeDispatch)
// prefers on Claude when neither --model nor the agent config names one.
const IssueDefaultModel = "sonnet"

// ValidPermissionModes is the set of values `claude --permission-mode`
// accepts.
var ValidPermissionModes = map[string]bool{
	"acceptEdits":       true,
	"auto":              true,
	"bypassPermissions": true,
	"manual":            true,
	"dontAsk":           true,
	"plan":              true,
}

// BuiltinAgents are the agents Claude Code ships with. They have no .md file
// for agentctl's discovery to find.
var BuiltinAgents = []string{
	"general-purpose", "Explore", "Plan", "claude-code-guide", "statusline-setup",
}

// PermissionMode returns the permission mode a launch passes, or "" for
// none: a valid override, else a valid configured value, else
// DefaultWorkerPermissionMode for worker launches. Invalid values are
// skipped (the caller logs them).
func PermissionMode(worker bool, configured, override string) string {
	if override != "" && ValidPermissionModes[override] {
		return override
	}
	if configured != "" && ValidPermissionModes[configured] {
		return configured
	}
	if worker {
		return DefaultWorkerPermissionMode
	}
	return ""
}

// Model returns the model a launch passes before any backend applies:
// override > configured > modeDefault, with launchopts.Inherit clearing it.
func Model(override, configured, modeDefault string) string {
	switch {
	case override == launchopts.Inherit:
		return ""
	case override != "":
		return override
	case configured == "":
		return modeDefault
	}
	return configured
}

// BackendName returns the backend a launch uses (override, else the agent's
// configured backend), or "" when none applies (also for
// launchopts.BackendNone).
func BackendName(override, configured string) string {
	name := override
	if name == "" {
		name = configured
	}
	if name == launchopts.BackendNone {
		return ""
	}
	return name
}

// AgentDir returns the working directory a dispatch of name uses when no
// --dir is given: the configured dir of the discovered agent, else cwd, else
// $HOME. ok is false for the cwd/$HOME fallback.
func AgentDir(agents []agent.Agent, name, cwd string) (dir string, ok bool) {
	for _, a := range agents {
		if a.Name == name && a.Dir != "" {
			return a.Dir, true
		}
	}
	if cwd == "" {
		cwd, _ = os.UserHomeDir()
	}
	return cwd, false
}

// Input is a dispatch command's flags plus where it runs.
type Input struct {
	Agent      string
	Issue      int
	ReviewPR   int
	Dir        string // raw --dir; "~/" is expanded, relative is against Cwd
	Model      string
	Backend    string
	Harness    string
	NoWorktree bool
	// Cwd is the directory the dispatch command runs in.
	Cwd string
	// TemplateAgent, TemplateModel and TemplateDir are the defaults of a
	// `dispatch --template` prompt template (internal/prompttemplate). Each
	// applies only when the matching flag is unset, as in the CLI.
	TemplateAgent string
	TemplateModel string
	TemplateDir   string
}

// Mode names.
const (
	ModeIssue    = "issue"
	ModeReviewPR = "review-pr"
	ModeAdhoc    = "adhoc"
)

// Sources of a resolved value.
const (
	SourceFlag      = "flag"
	SourceConfig    = "agent config"
	SourceCwd       = "current dir"
	SourceDefault   = "default"
	SourceLabels    = "issue labels"
	SourceBackend   = "backend"
	SourceMode      = "mode default"
	SourceAgentFile = "agent file"
	SourceGlobal    = "global"
	SourceProject   = "project"
	SourceBuiltin   = "built-in"
	SourcePlugin    = "plugin"
	SourceNotFound  = "not found"
	SourceTemplate  = "template"
)

// Plan is what a dispatch will run. Empty fields are unknown.
type Plan struct {
	Mode string
	// Agent is the agent that launches; AgentSource how it was chosen or
	// where its definition lives (SourceGlobal, SourceProject + " " +
	// project name, SourceBuiltin, SourcePlugin, SourceNotFound, or
	// SourceLabels for an issue dispatch without --agent).
	Agent       string
	AgentSource string
	WorkDir     string
	DirSource   string
	// Model is the model the agent runs on ("" = the harness default).
	Model       string
	ModelSource string
	Backend     string
	// BackendErr is set when Backend names no configured backend (the
	// dispatch will fail).
	BackendErr string
	// Harness is the resolved harness name, or the unknown name asked for
	// with HarnessErr set.
	Harness    string
	HarnessErr string
	// Accounts reports whether accounts apply on the harness.
	Accounts       bool
	PermissionMode string
	Worktree       bool
}

// Discover is the agent discovery Resolve uses. A var for tests.
var Discover = agent.DiscoverAll

// Resolve works out the plan for in under cfg with the rules the launch
// uses: --dir > agents.<name>.dir > the cwd; --harness >
// agents.<name>.harness > frontmatter > claude; --model > backend model >
// agents.<name>.model > mode default > agent frontmatter; --backend >
// agents.<name>.backend.
func Resolve(cfg *config.Config, in Input) Plan {
	p := Plan{Mode: ModeAdhoc, Agent: in.Agent, Worktree: !in.NoWorktree}
	if in.Cwd == "" {
		in.Cwd, _ = os.Getwd()
	}
	// Template defaults fill unset flags; dir and model keep their source.
	dirFromTemplate, modelFromTemplate := false, false
	if p.Agent == "" {
		p.Agent = in.TemplateAgent
	}
	if in.Dir == "" && in.TemplateDir != "" {
		in.Dir, dirFromTemplate = in.TemplateDir, true
	}
	if in.Model == "" && in.TemplateModel != "" {
		in.Model, modelFromTemplate = in.TemplateModel, true
	}
	switch {
	case in.ReviewPR > 0:
		p.Mode = ModeReviewPR
		if p.Agent == "" {
			p.Agent = config.DefaultReviewAgent
		}
	case in.Issue > 0:
		p.Mode = ModeIssue
		if p.Agent == "" {
			p.Agent, p.AgentSource = agentlabel.Default, SourceLabels
		}
	}

	p.WorkDir, p.DirSource = resolveDir(cfg, in, p.Agent)
	if dirFromTemplate {
		p.DirSource = SourceTemplate
	}

	agents, _ := Discover(cfg, agent.ProjectDirs(cfg, p.WorkDir, in.Cwd))
	def, found := findAgent(agents, p.Agent)
	if p.AgentSource == "" {
		p.AgentSource = agentSource(p.Agent, def, found)
	}

	ac := cfg.ForAgent(p.Agent)
	fm := ""
	if in.Harness == "" && ac.Harness == "" && found {
		fm = def.Harness
	}
	h, err := harness.Resolve(in.Harness, ac.Harness, fm)
	if err != nil {
		p.Harness, p.HarnessErr = firstNonEmpty(in.Harness, ac.Harness, fm), err.Error()
	} else {
		caps := h.Caps()
		p.Harness, p.Accounts = h.Name(), caps.Accounts
		if caps.PermissionModes {
			p.PermissionMode = PermissionMode(true, ac.PermissionMode, "")
		}
		if caps.Backends {
			p.Backend = BackendName(in.Backend, ac.Backend)
		}
	}

	modeDefault := ""
	if p.Mode == ModeIssue && p.Harness == harness.Claude {
		modeDefault = IssueDefaultModel
	}
	p.Model = Model(in.Model, ac.Model, modeDefault)
	switch {
	case modelFromTemplate && in.Model != launchopts.Inherit:
		p.ModelSource = SourceTemplate
	case in.Model != "" && in.Model != launchopts.Inherit:
		p.ModelSource = SourceFlag
	case in.Model == launchopts.Inherit:
	case ac.Model != "":
		p.ModelSource = SourceConfig
	case modeDefault != "":
		p.ModelSource = SourceMode
	}
	if p.Backend != "" {
		b, err := cfg.ResolveBackend(p.Backend)
		switch {
		case err != nil:
			p.BackendErr = err.Error()
		case in.Model == "" || in.Model == launchopts.Inherit:
			p.Model, p.ModelSource = b.Model, SourceBackend
		}
	}
	if p.Model == "" && found && p.Harness == harness.Claude {
		if m := frontmatterModel(def.FilePath); m != "" {
			p.Model, p.ModelSource = m, SourceAgentFile
		}
	}
	return p
}

// resolveDir is the dispatch working directory: --dir, else AgentDir over
// the agents discovered from the cwd (what `dispatch` itself scans).
func resolveDir(cfg *config.Config, in Input, agentName string) (string, string) {
	if in.Dir != "" {
		d := paths.ExpandHome(in.Dir)
		if !filepath.IsAbs(d) && in.Cwd != "" {
			d = filepath.Join(in.Cwd, d)
		}
		return filepath.Clean(d), SourceFlag
	}
	agents, _ := Discover(cfg, agent.ProjectDirs(cfg, in.Cwd))
	dir, ok := AgentDir(agents, agentName, in.Cwd)
	if ok {
		return dir, SourceConfig
	}
	return dir, SourceCwd
}

func findAgent(agents []agent.Agent, name string) (agent.Agent, bool) {
	if name == "" {
		return agent.Agent{}, false
	}
	for _, a := range agents {
		if a.Name == name {
			return a, true
		}
	}
	return agent.Agent{}, false
}

func agentSource(name string, a agent.Agent, found bool) string {
	switch {
	case name == "":
		return ""
	case found && a.Scope == agent.ScopeGlobal:
		return SourceGlobal
	case found && a.ProjectName != "":
		return SourceProject + " " + a.ProjectName
	case found:
		return SourceProject
	case slices.Contains(BuiltinAgents, name):
		return SourceBuiltin
	case agent.ValidPluginName(name):
		return SourcePlugin
	}
	return SourceNotFound
}

// frontmatterModel is the model: of an agent definition file, which claude
// applies itself when no --model is passed.
func frontmatterModel(path string) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	fm, err := agent.ParseFrontmatter(data)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(fm.Model)
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// ProjectRoot returns the project a directory belongs to: the checkout a
// `.worktrees/<slug>` worktree was cut from, else the nearest ancestor with
// a .git entry, else dir itself. It only stats files.
func ProjectRoot(dir string) string {
	if dir == "" {
		return ""
	}
	dir = filepath.Clean(dir)
	sep := string(filepath.Separator)
	if i := strings.Index(dir+sep, sep+".worktrees"+sep); i > 0 {
		return dir[:i]
	}
	for d := dir; ; {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return dir
		}
		d = parent
	}
}

// AbbrevHome shortens a path under $HOME to "~/...".
func AbbrevHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || path == "" {
		return path
	}
	home = filepath.Clean(home)
	switch {
	case path == home:
		return "~"
	case strings.HasPrefix(path, home+string(filepath.Separator)):
		return "~" + path[len(home):]
	}
	return path
}
