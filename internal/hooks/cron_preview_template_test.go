package hooks

import (
	"fmt"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/cron"
	"github.com/blsmth/agentctl/internal/prompttemplate"
)

// withCronTemplates makes templateLoad resolve tmpls, each only from the
// dirs listed for it (any dir when none), and records the dirs asked.
func withCronTemplates(t *testing.T, tmpls map[string]prompttemplate.Template, dirs map[string]string) *[]string {
	t.Helper()
	orig := templateLoad
	t.Cleanup(func() { templateLoad = orig })
	var asked []string
	templateLoad = func(name, dir string) (prompttemplate.Template, error) {
		asked = append(asked, dir)
		tm, ok := tmpls[name]
		if ok && (dirs[name] == "" || dirs[name] == dir) {
			return tm, nil
		}
		return prompttemplate.Template{}, fmt.Errorf("%w: %q", prompttemplate.ErrNotFound, name)
	}
	return &asked
}

func TestBuildCronPreview_Templates(t *testing.T) {
	isolateContextStore(t)
	withCronNow(t)
	withCronJobs(t,
		cron.Job{Name: "tmpl", Schedule: "0 9 * * *", Dir: "/srv", Template: "triage"},
		cron.Job{Name: "broken", Schedule: "0 9 * * *", Agent: "ops", Dir: "/srv", Template: "deleted"},
		cron.Job{Name: "plain", Schedule: "0 9 * * *", Agent: "ops", Dir: "/srv", Prompt: "stored prompt text"},
	)
	triage := prompttemplate.Template{Name: "triage", Agent: "go-engineer", Model: "sonnet", Prompt: "Triage the backlog.",
		Scope: prompttemplate.ScopeProject, Path: "/srv/.agentctl/templates/triage.md"}
	review := prompttemplate.Template{Name: "review", Agent: "code-reviewer", Prompt: "Review open PRs.",
		Scope: prompttemplate.ScopeGlobal, Path: "/g/templates/review.md"}
	asked := withCronTemplates(t, map[string]prompttemplate.Template{"triage": triage, "review": review},
		map[string]string{"triage": "/srv"})

	tests := []struct {
		name      string
		command   string
		cwd       string
		wantDeny  string
		wantAgent string
		wantBody  []string
		wantDir   string
	}{
		{
			name:      "add shows the template prompt and defaults",
			command:   `agentctl cron add nightly --schedule @daily --dir /srv --template triage`,
			wantAgent: "go-engineer",
			wantBody: []string{"Triage the backlog.", "**Template:** triage", "**Agent:** go-engineer (from template)",
				"**Model:** sonnet (from template)", "template triage (project, /srv/.agentctl/templates/triage.md; read at every fire"},
			wantDir: "/srv",
		},
		{
			name:      "job agent wins",
			command:   `agentctl cron add nightly --schedule @daily -a ops --dir /srv --template=triage`,
			wantAgent: "ops",
			wantBody:  []string{"**Agent:** ops\n"},
		},
		{
			name:     "relative dir resolves against the payload cwd",
			command:  `agentctl cron add nightly --schedule @daily --dir . --template triage`,
			cwd:      "/srv",
			wantBody: []string{"Triage the backlog."},
			wantDir:  "/srv",
		},
		{name: "template not found from the job dir", command: `agentctl cron add n --schedule @daily --dir /other --template triage`,
			wantDeny: `cron add: template "triage" not found`},
		{name: "unknown template", command: `agentctl cron add n --schedule @daily --dir /srv --template nope`,
			wantDeny: `template "nope" not found`},
		{name: "expanding template", command: `agentctl cron add n --schedule @daily --dir /srv --template "$T"`,
			wantDeny: "--template uses shell expansion"},
		{name: "template and prompt", command: `agentctl cron add n --schedule @daily --dir /srv --template triage -p hi`,
			wantDeny: "do not combine"},
		{name: "cd first", command: `cd /srv && agentctl cron add n --schedule @daily --dir . --template triage`,
			wantDeny: "changes directory or environment"},
		{
			name:      "edit switches a prompt job to a template",
			command:   `agentctl cron edit plain --template review`,
			wantAgent: "ops",
			wantBody:  []string{"Edit changes: prompt, template.", "Review open PRs.", "template review (global"},
		},
		{
			name:     "edit switches a template job to a prompt",
			command:  `agentctl cron edit tmpl -a ops --prompt "Inline now."`,
			wantBody: []string{"Edit changes: agent, prompt, template.", "Inline now."},
		},
		{
			name:      "edit of another field shows the stored template",
			command:   `agentctl cron edit tmpl --timeout 1h`,
			wantAgent: "go-engineer",
			wantBody:  []string{"Triage the backlog.", "read at every fire; unchanged)"},
		},
		{name: "edit to a missing template", command: `agentctl cron edit plain --template nope`, wantDeny: `cron edit: template "nope" not found`},
		{name: "edit template and prompt", command: `agentctl cron edit plain --template review -p x`, wantDeny: "do not combine"},
		{name: "edit dir away from the template", command: `agentctl cron edit tmpl --dir /other --template triage`,
			wantDeny: `template "triage" not found`},
		{name: "edit relative dir of a stored template job after cd", command: `cd /other && agentctl cron edit tmpl --dir .`,
			cwd: "/srv", wantDeny: "changes directory or environment"},
		{name: "edit relative dir of a template job switched in after cd", command: `cd /other && agentctl cron edit plain --dir . --template triage`,
			cwd: "/srv", wantDeny: "changes directory or environment"},
		{
			name:     "edit absolute dir of a stored template job after cd still previews",
			command:  `cd /other && agentctl cron edit tmpl --dir /srv --timeout 1h`,
			wantBody: []string{"Triage the backlog."},
		},
		{
			name:     "edit relative dir of a stored template job resolves against the cwd",
			command:  `agentctl cron edit tmpl --dir .`,
			cwd:      "/srv",
			wantBody: []string{"Triage the backlog."},
			wantDir:  "/srv",
		},
		{
			name:      "run --force on a template job",
			command:   `agentctl cron run tmpl --force`,
			wantAgent: "go-engineer",
			wantBody:  []string{"Force run", "Triage the backlog."},
		},
		{
			name:     "resume of a job whose template was deleted previews the breakage",
			command:  `agentctl cron resume broken`,
			wantBody: []string{`template "deleted" not found; fires will fail until it exists`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			*asked = nil
			p, deny := buildCronPreviewIn(tt.command, tt.cwd)
			if tt.wantDeny != "" {
				if !strings.Contains(deny, tt.wantDeny) {
					t.Fatalf("deny = %q, want containing %q", deny, tt.wantDeny)
				}
				return
			}
			if deny != "" {
				t.Fatalf("unexpected deny: %s", deny)
			}
			if tt.wantAgent != "" && p.agent != tt.wantAgent {
				t.Errorf("agent = %q, want %q", p.agent, tt.wantAgent)
			}
			for _, want := range tt.wantBody {
				if !strings.Contains(p.body, want) {
					t.Errorf("body missing %q:\n%s", want, p.body)
				}
			}
			if tt.wantDir != "" && (len(*asked) == 0 || (*asked)[0] != tt.wantDir) {
				t.Errorf("template looked up from %v, want %s", *asked, tt.wantDir)
			}
		})
	}
}

func TestCronValueFlagsIncludeTemplate(t *testing.T) {
	// A --template value is never a positional or a help flag.
	if !isCronValueFlag("--template") {
		t.Error("--template is not a value flag")
	}
	if !isAgentctlCronGated(`agentctl cron add x --template --help`) {
		t.Error("a --template value of --help was taken as a help lookup")
	}
}
