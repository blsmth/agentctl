package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/dispatchplan"
	"github.com/blsmth/agentctl/internal/prompttemplate"
	"github.com/blsmth/agentctl/internal/remoteapproval"
	"github.com/blsmth/agentctl/internal/tui"
)

// templateWorld isolates the global config dir and returns a project
// checkout with a project template "proj" and a global "glob"; "both"
// exists in both (the project's wins).
func templateWorld(t *testing.T) (project string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	project = t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	save := func(tm prompttemplate.Template, scope prompttemplate.Scope) {
		t.Helper()
		if _, err := prompttemplate.Save(tm, scope, project); err != nil {
			t.Fatal(err)
		}
	}
	save(prompttemplate.Template{Name: "glob", Agent: "go-engineer", Model: "opus", Prompt: "Global prompt."}, prompttemplate.ScopeGlobal)
	save(prompttemplate.Template{Name: "both", Agent: "go-engineer", Prompt: "Global both."}, prompttemplate.ScopeGlobal)
	save(prompttemplate.Template{Name: "both", Agent: "writer", Model: "haiku", Dir: "/code/site", Prompt: "Project both."}, prompttemplate.ScopeProject)
	save(prompttemplate.Template{Name: "noagent", Prompt: "No agent here."}, prompttemplate.ScopeGlobal)
	return project
}

func TestResolveDispatchTemplate(t *testing.T) {
	project := templateWorld(t)
	tests := []struct {
		name       string
		command    string
		wantPrompt string // "" with wantDeny "" means no template
		wantDeny   string
	}{
		{name: "no template", command: "agentctl dispatch --agent a --prompt hi"},
		{name: "global", command: "agentctl dispatch --template glob", wantPrompt: "Global prompt."},
		{name: "project shadows global", command: "agentctl dispatch --template both", wantPrompt: "Project both."},
		{name: "equals form", command: "agentctl dispatch --template=glob --prompt x", wantPrompt: "Global prompt."},
		{name: "last wins", command: "agentctl dispatch --template both --template glob", wantPrompt: "Global prompt."},
		{name: "prompt mentioning the flag", command: `agentctl dispatch --agent a --prompt "--template both"`},
		{name: "missing denies", command: "agentctl dispatch --template nope", wantDeny: "not found"},
		{name: "invalid name denies", command: "agentctl dispatch --template ../x", wantDeny: "invalid template name"},
		{name: "expanding name denies", command: `agentctl dispatch --template "$T"`, wantDeny: "shell expansion"},
		{name: "cd first denies", command: "cd /tmp && agentctl dispatch --template glob", wantDeny: "changes directory"},
		{name: "env first denies", command: "XDG_CONFIG_HOME=/tmp/evil agentctl dispatch --template glob", wantDeny: "changes directory"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, deny := resolveDispatchTemplate(tt.command, project)
			if tt.wantDeny != "" {
				if got != nil || !strings.Contains(deny, tt.wantDeny) {
					t.Fatalf("got %+v, deny %q; want deny containing %q", got, deny, tt.wantDeny)
				}
				return
			}
			if deny != "" {
				t.Fatalf("unexpected deny %q", deny)
			}
			if tt.wantPrompt == "" {
				if got != nil {
					t.Fatalf("got template %+v, want none", got)
				}
				return
			}
			if got == nil || got.Prompt != tt.wantPrompt {
				t.Fatalf("got %+v, want prompt %q", got, tt.wantPrompt)
			}
		})
	}
}

// The dispatch popup shows the template's full prompt (plus --prompt as
// additional instructions), its name and the template's agent.
func TestDispatchPreviewGate_Template(t *testing.T) {
	project := templateWorld(t)
	t.Setenv("TMUX", "/tmp/fake-tmux-socket")
	t.Setenv("AGENTCTL_SESSION_ID", "sess-template-1")
	command := `agentctl dispatch --template both --prompt "Only the docs."`
	removeDispatchLockForTest(t, command)

	var args DispatchPreviewArgs
	called := withFakeDispatchPopupRunner(t, func(_, _ int, path string) error {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &args); err != nil {
			t.Fatal(err)
		}
		return nil
	})
	approved, decided, deny := dispatchPreviewGate("lead", command, project)
	if !*called || !approved || !decided || deny != "" {
		t.Fatalf("approved=%v decided=%v deny=%q called=%v", approved, decided, deny, *called)
	}
	want := prompttemplate.Compose("Project both.", "Only the docs.")
	if args.Prompt != want {
		t.Errorf("Prompt = %q, want %q", args.Prompt, want)
	}
	if args.Template != "both" || args.TargetAgent != "writer" {
		t.Errorf("Template=%q TargetAgent=%q", args.Template, args.TargetAgent)
	}
}

func TestDispatchPreviewGate_UnresolvableTemplateDenies(t *testing.T) {
	project := templateWorld(t)
	t.Setenv("TMUX", "/tmp/fake-tmux-socket")
	t.Setenv("AGENTCTL_SESSION_ID", "sess-template-deny")
	command := "agentctl dispatch --template gone"
	removeDispatchLockForTest(t, command)
	failIfDispatchPopupRunnerCalled(t)

	approved, decided, deny := dispatchPreviewGate("lead", command, project)
	if approved || decided || !strings.Contains(deny, "gone") {
		t.Errorf("approved=%v decided=%v deny=%q", approved, decided, deny)
	}
}

// The where/how rows use the template's agent, model and dir when the flags
// leave them unset, and the flags otherwise.
func TestDispatchDetails_Template(t *testing.T) {
	project := templateWorld(t)
	tmpl, deny := resolveDispatchTemplate("agentctl dispatch --template both", project)
	if deny != "" {
		t.Fatal(deny)
	}
	orig := dispatchplan.Discover
	t.Cleanup(func() { dispatchplan.Discover = orig })
	dispatchplan.Discover = func(*config.Config, []string) ([]agent.Agent, error) { return nil, nil }

	d := dispatchDetails(&config.Config{}, "agentctl dispatch --template both", project, tmpl)
	if d.Agent != "writer" || d.Model != "haiku" || d.ModelSource != dispatchplan.SourceTemplate ||
		d.DirSource != dispatchplan.SourceTemplate || d.Dir != "/code/site" || d.Template != "both" {
		t.Errorf("details = %+v", d)
	}
	d = dispatchDetails(&config.Config{}, "agentctl dispatch --template both --agent a --model opus --dir /x", project, tmpl)
	if d.Agent != "a" || d.Model != "opus" || d.ModelSource != dispatchplan.SourceFlag || d.DirSource != dispatchplan.SourceFlag {
		t.Errorf("flags: details = %+v", d)
	}
}

func TestIsAgentctlTemplateGated(t *testing.T) {
	for cmd, want := range map[string]bool{
		"agentctl template add x --prompt hi":     true,
		"agentctl template edit x --model opus":   true,
		"agentctl template rm x":                  true,
		"/usr/local/bin/agentctl template rm x":   true,
		"agentctl template list":                  false,
		"agentctl template list --json":           false,
		"agentctl template show x":                false,
		"agentctl template add --help":            false,
		"agentctl template add x --prompt --help": true, // --help is the prompt
		`echo "agentctl template rm x"`:           false,
		"agentctl dispatch --template x":          false,
		"go test ./internal/prompttemplate/":      false,
	} {
		if got := isAgentctlTemplateGated(cmd); got != want {
			t.Errorf("isAgentctlTemplateGated(%q) = %v, want %v", cmd, got, want)
		}
	}
}

func TestBuildTemplatePreview(t *testing.T) {
	project := templateWorld(t)
	promptFile := filepath.Join(project, "p.md")
	if err := os.WriteFile(promptFile, []byte("From a file.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "empty.md"), []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(project, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	gdir, _ := prompttemplate.GlobalDir()
	tests := []struct {
		name     string
		command  string
		wantDeny string
		want     []string // substrings of the body
		wantPath string
	}{
		{name: "add global", command: `agentctl template add n --agent go-engineer --description "Daily" --prompt "Do the thing."`,
			want: []string{"New template.", "agent: go-engineer", "description: Daily", "Do the thing."}, wantPath: filepath.Join(gdir, "n.md")},
		{name: "add project from relative file", command: "agentctl template add n --project --prompt-file p.md",
			want: []string{"From a file."}, wantPath: filepath.Join(project, ".agentctl", "templates", "n.md")},
		{name: "add existing", command: "agentctl template add glob --prompt x", want: []string{"already exists"}},
		{name: "add with no prompt opens the editor", command: "agentctl template add n --agent a", wantDeny: "$EDITOR"},
		{name: "add expanding prompt", command: `agentctl template add n --prompt "$(cat spec.md)"`, wantDeny: "shell expansion"},
		{name: "add unreadable file", command: "agentctl template add n --prompt-file nope.md", wantDeny: "cannot read"},
		{name: "add from stdin redirect", command: "agentctl template add x --prompt-file /dev/stdin < p.md", wantDeny: "not a regular file"},
		{name: "add from fd here-string", command: "agentctl template add x --prompt-file /dev/fd/0 <<< 'evil'", wantDeny: "not a regular file"},
		{name: "edit from stdin heredoc", command: "agentctl template edit glob --prompt-file /dev/stdin <<EOF\nEVIL\nEOF", wantDeny: "not a regular file"},
		{name: "add from a fifo", command: "agentctl template add x --prompt-file fifo", wantDeny: "not a regular file"},
		{name: "add empty file", command: "agentctl template add x --prompt-file empty.md", wantDeny: "is empty"},
		{name: "add short flag", command: "agentctl template add n -p hi", wantDeny: `"-p"`},
		{name: "add no name", command: "agentctl template add --prompt hi", wantDeny: "exactly one template name"},
		{name: "add bad name", command: "agentctl template add ../x --prompt hi", wantDeny: "invalid template name"},
		{name: "edit patches", command: "agentctl template edit glob --model sonnet --prompt New.",
			want: []string{"Edit changes: model, prompt.", "model: sonnet", "New."}},
		{name: "edit project", command: "agentctl template edit both --project --description d",
			want: []string{"Edit changes: description.", "Project both."}},
		{name: "edit no flags opens the editor", command: "agentctl template edit glob", wantDeny: "$EDITOR"},
		{name: "edit missing", command: "agentctl template edit nope --model x", wantDeny: "not found"},
		{name: "rm shows content", command: "agentctl template rm glob", want: []string{"Removes this template.", "Global prompt."}},
		{name: "rm missing", command: "agentctl template rm nope", wantDeny: "not found"},
		{name: "two at once", command: "agentctl template rm glob && agentctl template rm both", wantDeny: "one template add/edit/rm per Bash call"},
		{name: "cd first", command: "cd /tmp && agentctl template rm glob", wantDeny: "changes directory"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, deny := buildTemplatePreview(tt.command, project)
			if tt.wantDeny != "" {
				if !strings.Contains(deny, tt.wantDeny) {
					t.Fatalf("deny = %q, want %q", deny, tt.wantDeny)
				}
				return
			}
			if deny != "" {
				t.Fatalf("unexpected deny %q", deny)
			}
			for _, w := range tt.want {
				if !strings.Contains(p.body, w) {
					t.Errorf("body missing %q:\n%s", w, p.body)
				}
			}
			if tt.wantPath != "" && p.path != tt.wantPath {
				t.Errorf("path = %q, want %q", p.path, tt.wantPath)
			}
		})
	}
}

func TestValidateBash_TemplateGate(t *testing.T) {
	templateWorld(t)
	add := `agentctl template add n --prompt "hello"`
	tests := []struct {
		name       string
		command    string
		verdict    func(t *testing.T) error // nil: popup must not open
		wantResult string
		wantReason string
	}{
		{name: "approved add", command: add, verdict: func(*testing.T) error { return nil }, wantResult: "allow"},
		{name: "denied add", command: add, verdict: exitError1, wantResult: "deny", wantReason: "Prompt template change denied"},
		{name: "editor add", command: "agentctl template add n", wantResult: "deny", wantReason: "$EDITOR"},
		{name: "template and dispatch together", command: add + " && agentctl dispatch --template n", wantResult: "deny", wantReason: "separate Bash calls"},
		{name: "list passes silently", command: "agentctl template list", wantResult: ""},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessionID := "sess-validate-template-" + string(rune('a'+i))
			t.Setenv("TMUX", "/tmp/fake-tmux-socket")
			t.Setenv("AGENTCTL_SESSION_ID", sessionID)
			removeDispatchLockForTest(t, tt.command)
			if tt.verdict == nil {
				failIfDispatchPopupRunnerCalled(t)
			} else {
				withFakeDispatchPopupRunner(t, func(_, _ int, _ string) error { return tt.verdict(t) })
			}
			out := runValidateBash(t, sessionID, tt.command)
			if tt.wantResult == "" {
				if out != "" {
					t.Fatalf("want no output, got %q", out)
				}
				return
			}
			decision, reason := decisionOf(t, out)
			if decision != tt.wantResult || !strings.Contains(reason, tt.wantReason) {
				t.Errorf("decision %q reason %q, want %q containing %q", decision, reason, tt.wantResult, tt.wantReason)
			}
		})
	}
}

func TestTemplatePreviewGate_NotInTmux_StaysSilent(t *testing.T) {
	t.Setenv("TMUX", "")
	failIfDispatchPopupRunnerCalled(t)
	if approved, decided, deny := templatePreviewGate("a", "agentctl template rm x", ""); approved || decided || deny != "" {
		t.Errorf("got %v %v %q", approved, decided, deny)
	}
}

func TestPreviewKindTemplate_MatchesTUI(t *testing.T) {
	if string(PreviewKindTemplate) != tui.PreviewKindTemplate {
		t.Errorf("PreviewKindTemplate = %q, tui = %q", PreviewKindTemplate, tui.PreviewKindTemplate)
	}
}

// pinTemplateDir points the template tamper rules at a fixed global dir
// under a fake home.
func pinTemplateDir(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", "/Users/u")
	orig := templateGlobalDir
	t.Cleanup(func() { templateGlobalDir = orig })
	templateGlobalDir = func() string { return "/Users/u/.config/agentctl/templates" }
}

func TestCheckTemplateTamper(t *testing.T) {
	pinTemplateDir(t)
	tests := []struct {
		command  string
		wantDeny bool
	}{
		{`echo hi > ~/.config/agentctl/templates/x.md`, true},
		{`echo hi >> /Users/u/.config/agentctl/templates/x.md`, true},
		{`printf x > "$HOME/.config/agentctl/templates/x.md"`, true},
		{`echo x > .agentctl/templates/x.md`, true},
		{`cat > /code/p/.agentctl/templates/x.md <<'EOF'` + "\nbody\nEOF", true},
		{`sed -i '' 's/a/b/' .agentctl/templates/x.md`, true},
		{`cp /tmp/x.md ~/.config/agentctl/templates/`, true},
		{`rm -rf .agentctl/templates`, true},
		{`tee $XDG_CONFIG_HOME/agentctl/templates/x.md < /tmp/x`, true},
		{`python3 -c "open('.agentctl/templates/x.md','w')"`, true},
		// Reading and agentctl's own commands are fine.
		{`cat ~/.config/agentctl/templates/x.md`, false},
		{`ls .agentctl/templates`, false},
		{`agentctl template list`, false},
		{`agentctl template add x --prompt 'echo y > ~/.config/agentctl/templates/x.md and cp a .agentctl/templates/b'`, false},
		{`agentctl context set k "tee .agentctl/templates/x.md"`, false},
		{`go test ./internal/prompttemplate/`, false},
		{`cp a.md docs/templates/b.md`, false},
		// An agentctl command with a substitution is scanned whole.
		{`agentctl template add x --prompt "$(cat a)" > .agentctl/templates/x.md`, true},
	}
	for _, tt := range tests {
		got := checkTemplateTamper(tt.command)
		if (got != "") != tt.wantDeny {
			t.Errorf("checkTemplateTamper(%q) = %q, wantDeny %v", tt.command, got, tt.wantDeny)
		}
	}
	// Wired into the global deny list.
	if checkGlobalDenyList(`echo x > .agentctl/templates/x.md`) == "" {
		t.Error("checkGlobalDenyList does not apply the template rule")
	}
}

func TestTemplateEditTarget(t *testing.T) {
	pinTemplateDir(t)
	link := filepath.Join(t.TempDir(), "link")
	real := filepath.Join(t.TempDir(), "p", ".agentctl", "templates")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		tool, path string
		want       bool
	}{
		{"Write", "/Users/u/.config/agentctl/templates/x.md", true},
		{"Edit", "~/.config/agentctl/templates/x.md", true},
		{"MultiEdit", "/code/proj/.agentctl/templates/x.md", true},
		{"Write", "/code/proj/.worktrees/w/.agentctl/templates/x.md", true},
		{"Write", filepath.Join(link, "x.md"), true},
		{"Read", "/code/proj/.agentctl/templates/x.md", false},
		{"Write", "/code/proj/internal/prompttemplate/x.go", false},
		{"Write", "/code/proj/.agentctl/config.json", false},
		{"Write", "/code/proj/templates/x.md", false},
	}
	for _, tt := range tests {
		in, _ := json.Marshal(map[string]string{"file_path": tt.path})
		if got := templateEditTarget(tt.tool, in) != ""; got != tt.want {
			t.Errorf("templateEditTarget(%s, %s) = %v, want %v", tt.tool, tt.path, got, tt.want)
		}
	}
}

// The phone sees add/edit/rm as a template approval naming the template.
func TestTemplatePopupNotice_PhoneKind(t *testing.T) {
	n := templatePopupNotice("lead", "agentctl template edit triage", templatePreview{action: "edit", name: "triage", agent: "writer", body: "x"})
	r := n.Remote
	if r == nil || r.Kind != remoteapproval.KindTemplate || r.Details.Template != "triage" || r.Details.Mode != "edit template" || r.Details.Agent != "writer" {
		t.Errorf("remote = %+v", r)
	}
}
