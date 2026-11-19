package dispatchplan

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/config"
)

func TestPermissionMode(t *testing.T) {
	tests := []struct {
		name                       string
		worker                     bool
		configured, override, want string
	}{
		{"worker default", true, "", "", DefaultWorkerPermissionMode},
		{"interactive default", false, "", "", ""},
		{"configured wins", true, "plan", "", "plan"},
		{"override wins", true, "plan", "acceptEdits", "acceptEdits"},
		{"invalid configured skipped", true, "yolo", "", DefaultWorkerPermissionMode},
		{"invalid override skipped", false, "plan", "yolo", "plan"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PermissionMode(tt.worker, tt.configured, tt.override); got != tt.want {
				t.Errorf("PermissionMode = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestModel(t *testing.T) {
	tests := []struct{ override, configured, def, want string }{
		{"", "", "", ""},
		{"", "", "sonnet", "sonnet"},
		{"", "opus", "sonnet", "opus"},
		{"haiku", "opus", "sonnet", "haiku"},
		{"inherit", "opus", "sonnet", ""},
	}
	for _, tt := range tests {
		if got := Model(tt.override, tt.configured, tt.def); got != tt.want {
			t.Errorf("Model(%q, %q, %q) = %q, want %q", tt.override, tt.configured, tt.def, got, tt.want)
		}
	}
}

func TestBackendName(t *testing.T) {
	tests := []struct{ override, configured, want string }{
		{"", "", ""},
		{"", "local", "local"},
		{"nous", "local", "nous"},
		{"none", "local", ""},
	}
	for _, tt := range tests {
		if got := BackendName(tt.override, tt.configured); got != tt.want {
			t.Errorf("BackendName(%q, %q) = %q, want %q", tt.override, tt.configured, got, tt.want)
		}
	}
}

func TestAgentDir(t *testing.T) {
	agents := []agent.Agent{{Name: "blogger", Dir: "/code/blog"}, {Name: "portable"}}
	if d, ok := AgentDir(agents, "blogger", "/cwd"); d != "/code/blog" || !ok {
		t.Errorf("configured = (%q, %v)", d, ok)
	}
	if d, ok := AgentDir(agents, "portable", "/cwd"); d != "/cwd" || ok {
		t.Errorf("portable = (%q, %v)", d, ok)
	}
	if d, _ := AgentDir(nil, "x", ""); d == "" {
		t.Error("no cwd: want $HOME, got empty")
	}
}

// writeAgent writes an agent definition and returns its path.
func writeAgent(t *testing.T, dir, name, frontmatter string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name+".md")
	if err := os.WriteFile(p, []byte("---\nname: "+name+"\n"+frontmatter+"---\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func stubDiscover(t *testing.T, agents []agent.Agent) *[][]string {
	t.Helper()
	var calls [][]string
	orig := Discover
	Discover = func(_ *config.Config, dirs []string) ([]agent.Agent, error) {
		calls = append(calls, dirs)
		return agents, nil
	}
	t.Cleanup(func() { Discover = orig })
	return &calls
}

func TestResolve(t *testing.T) {
	tmp := t.TempDir()
	withModel := writeAgent(t, tmp, "writer", "model: opus\n")
	plain := writeAgent(t, tmp, "go-engineer", "")
	stubDiscover(t, []agent.Agent{
		{Name: "writer", FilePath: withModel, Scope: agent.ScopeGlobal},
		{Name: "go-engineer", FilePath: plain, Scope: agent.ScopeGlobal},
		{Name: "blogger", FilePath: plain, Dir: "/code/blog", Scope: agent.ScopeLocal, ProjectName: "blog"},
		{Name: "pi-agent", FilePath: plain, Harness: "pi", Scope: agent.ScopeGlobal},
		{Name: "code-reviewer", FilePath: plain, Scope: agent.ScopeGlobal},
	})
	cfg := &config.Config{
		Agents: map[string]config.AgentConfig{
			"blogger":     {Dir: "/code/blog", Model: "haiku", PermissionMode: "acceptEdits"},
			"local-model": {Backend: "local"},
			"broken":      {Backend: "ghost"},
			"pi-cfg":      {Harness: "pi"},
		},
		Backends: map[string]config.BackendConfig{"local": {Kind: "ollama", Model: "qwen3"}},
	}
	tests := []struct {
		name  string
		in    Input
		check func(t *testing.T, p Plan)
	}{
		{"adhoc with flag dir", Input{Agent: "general-purpose", Dir: "~/code/blsmth.com", Cwd: "/code/agentctl"}, func(t *testing.T, p Plan) {
			home, _ := os.UserHomeDir()
			want(t, "WorkDir", p.WorkDir, filepath.Join(home, "code/blsmth.com"))
			want(t, "DirSource", p.DirSource, SourceFlag)
			want(t, "Mode", p.Mode, ModeAdhoc)
			want(t, "AgentSource", p.AgentSource, SourceBuiltin)
			want(t, "Model", p.Model, "")
			want(t, "Harness", p.Harness, "claude")
			want(t, "PermissionMode", p.PermissionMode, DefaultWorkerPermissionMode)
			if !p.Worktree || !p.Accounts {
				t.Errorf("Worktree=%v Accounts=%v, want both", p.Worktree, p.Accounts)
			}
		}},
		{"relative dir", Input{Agent: "writer", Dir: "../site", Cwd: "/code/agentctl"}, func(t *testing.T, p Plan) {
			want(t, "WorkDir", p.WorkDir, "/code/site")
		}},
		{"agent config dir and model", Input{Agent: "blogger", Cwd: "/code/agentctl", NoWorktree: true}, func(t *testing.T, p Plan) {
			want(t, "WorkDir", p.WorkDir, "/code/blog")
			want(t, "DirSource", p.DirSource, SourceConfig)
			want(t, "AgentSource", p.AgentSource, "project blog")
			want(t, "Model", p.Model, "haiku")
			want(t, "ModelSource", p.ModelSource, SourceConfig)
			want(t, "PermissionMode", p.PermissionMode, "acceptEdits")
			if p.Worktree {
				t.Error("Worktree = true with --no-worktree")
			}
		}},
		{"cwd fallback and frontmatter model", Input{Agent: "writer", Cwd: "/code/agentctl"}, func(t *testing.T, p Plan) {
			want(t, "WorkDir", p.WorkDir, "/code/agentctl")
			want(t, "DirSource", p.DirSource, SourceCwd)
			want(t, "Model", p.Model, "opus")
			want(t, "ModelSource", p.ModelSource, SourceAgentFile)
			want(t, "AgentSource", p.AgentSource, SourceGlobal)
		}},
		{"model flag wins", Input{Agent: "writer", Model: "haiku", Cwd: "/c"}, func(t *testing.T, p Plan) {
			want(t, "Model", p.Model, "haiku")
			want(t, "ModelSource", p.ModelSource, SourceFlag)
		}},
		{"issue default agent and mode model", Input{Issue: 12, Cwd: "/c"}, func(t *testing.T, p Plan) {
			want(t, "Mode", p.Mode, ModeIssue)
			want(t, "Agent", p.Agent, "go-engineer")
			want(t, "AgentSource", p.AgentSource, SourceLabels)
			want(t, "Model", p.Model, IssueDefaultModel)
			want(t, "ModelSource", p.ModelSource, SourceMode)
		}},
		{"review default agent", Input{ReviewPR: 7, Cwd: "/c"}, func(t *testing.T, p Plan) {
			want(t, "Mode", p.Mode, ModeReviewPR)
			want(t, "Agent", p.Agent, config.DefaultReviewAgent)
			want(t, "Model", p.Model, "")
		}},
		{"configured backend model", Input{Agent: "local-model", Cwd: "/c"}, func(t *testing.T, p Plan) {
			want(t, "Backend", p.Backend, "local")
			want(t, "Model", p.Model, "qwen3")
			want(t, "ModelSource", p.ModelSource, SourceBackend)
		}},
		{"backend none", Input{Agent: "local-model", Backend: "none", Cwd: "/c"}, func(t *testing.T, p Plan) {
			want(t, "Backend", p.Backend, "")
		}},
		{"unknown backend", Input{Agent: "broken", Cwd: "/c"}, func(t *testing.T, p Plan) {
			want(t, "Backend", p.Backend, "ghost")
			if p.BackendErr == "" {
				t.Error("BackendErr empty for an unconfigured backend")
			}
		}},
		{"frontmatter harness", Input{Agent: "pi-agent", Cwd: "/c"}, func(t *testing.T, p Plan) {
			want(t, "Harness", p.Harness, "pi")
			if p.Accounts {
				t.Error("Accounts = true on pi")
			}
		}},
		{"harness flag over config", Input{Agent: "pi-cfg", Harness: "claude", Cwd: "/c"}, func(t *testing.T, p Plan) {
			want(t, "Harness", p.Harness, "claude")
		}},
		{"unknown harness", Input{Agent: "writer", Harness: "emacs", Cwd: "/c"}, func(t *testing.T, p Plan) {
			want(t, "Harness", p.Harness, "emacs")
			if p.HarnessErr == "" {
				t.Error("HarnessErr empty")
			}
		}},
		{"unknown agent", Input{Agent: "nobody", Cwd: "/c"}, func(t *testing.T, p Plan) {
			want(t, "AgentSource", p.AgentSource, SourceNotFound)
		}},
		{"plugin agent", Input{Agent: "tools:helper", Cwd: "/c"}, func(t *testing.T, p Plan) {
			want(t, "AgentSource", p.AgentSource, SourcePlugin)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { tt.check(t, Resolve(cfg, tt.in)) })
	}
}

func want(t *testing.T, field, got, w string) {
	t.Helper()
	if got != w {
		t.Errorf("%s = %q, want %q", field, got, w)
	}
}

func TestProjectRoot(t *testing.T) {
	tmp := t.TempDir()
	repo := filepath.Join(tmp, "agentctl")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(repo, "internal", "hooks")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	tests := []struct{ dir, want string }{
		{"", ""},
		{repo, repo},
		{sub, repo},
		{filepath.Join(repo, ".worktrees", "agent-x-issue-1"), repo},
		{filepath.Join(repo, ".worktrees", "agent-x-issue-1", "internal"), repo},
		{filepath.Join(tmp, "plain"), filepath.Join(tmp, "plain")},
	}
	for _, tt := range tests {
		if got := ProjectRoot(tt.dir); got != tt.want {
			t.Errorf("ProjectRoot(%q) = %q, want %q", tt.dir, got, tt.want)
		}
	}
}

func TestAbbrevHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	tests := []struct{ in, want string }{
		{home, "~"},
		{filepath.Join(home, "code", "x"), "~/code/x"},
		{"/srv/x", "/srv/x"},
		{home + "other", home + "other"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := AbbrevHome(tt.in); got != tt.want {
			t.Errorf("AbbrevHome(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
