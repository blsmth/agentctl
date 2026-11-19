package hooks

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/dispatchplan"
	"github.com/blsmth/agentctl/internal/remoteapproval"
)

func TestParseDispatchPlanFlags(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    dispatchFlags
	}{
		{"none", "agentctl dispatch --issue 1", dispatchFlags{}},
		{
			"all flags",
			"agentctl dispatch --agent general-purpose --dir ~/code/blsmth.com --model opus --backend local --harness pi --account ll --no-worktree --prompt-key k",
			dispatchFlags{account: "ll"},
		},
		{"equals form", "agentctl dispatch --agent=a --dir=/srv/y --review-pr=7 -m haiku", dispatchFlags{}},
		{"prompt mentioning flags", `agentctl dispatch -a a --prompt "use --dir /x and --no-worktree" --issue 3`, dispatchFlags{}},
		{"last wins", "agentctl dispatch --agent a --agent b --prompt x", dispatchFlags{}},
	}
	tests[0].want.plan.Issue = 1
	tests[1].want.plan = planInput("general-purpose", "~/code/blsmth.com", "opus", "local", "pi", true)
	tests[2].want.plan = planInput("a", "/srv/y", "haiku", "", "", false)
	tests[2].want.plan.ReviewPR = 7
	tests[3].want.plan = planInput("a", "", "", "", "", false)
	tests[3].want.plan.Issue = 3
	tests[4].want.plan = planInput("b", "", "", "", "", false)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseDispatchPlanFlags(tt.command); got != tt.want {
				t.Errorf("parseDispatchPlanFlags = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func planInput(agentName, dir, model, backend, harnessName string, noWorktree bool) dispatchplan.Input {
	return dispatchplan.Input{Agent: agentName, Dir: dir, Model: model, Backend: backend, Harness: harnessName, NoWorktree: noWorktree}
}

// detailsFixture is a global config, an agent registry and two git projects:
// the asking project (agentctl) and the agent's configured one (blsmth.com).
type detailsFixture struct {
	asking, blog string
}

func newDetailsFixture(t *testing.T, configJSON func(blog string) string) detailsFixture {
	t.Helper()
	tmp := t.TempDir()
	f := detailsFixture{asking: filepath.Join(tmp, "agentctl"), blog: filepath.Join(tmp, "blsmth.com")}
	for _, d := range []string{f.asking, f.blog} {
		if err := os.MkdirAll(filepath.Join(d, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	reg := filepath.Join(tmp, "agents")
	if err := os.MkdirAll(reg, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, fm := range map[string]string{"blogger": "model: opus\n", "go-engineer": ""} {
		body := "---\nname: " + name + "\n" + fm + "---\nbody\n"
		if err := os.WriteFile(filepath.Join(reg, name+".md"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	origReg := agent.RegistryDir
	agent.RegistryDir = reg
	t.Cleanup(func() { agent.RegistryDir = origReg })
	cfgPath := filepath.Join(tmp, "config.json")
	if err := os.WriteFile(cfgPath, []byte(configJSON(f.blog)), 0o600); err != nil {
		t.Fatal(err)
	}
	config.SetConfigPathForTest(cfgPath)
	t.Cleanup(func() { config.SetConfigPathForTest("") })
	return f
}

func TestResolveDispatchDetails(t *testing.T) {
	f := newDetailsFixture(t, func(blog string) string {
		return `{"agents":{"blogger":{"dir":"` + blog + `","permission_mode":"acceptEdits"}},` +
			`"backends":{"local":{"kind":"ollama","base_url":"http://127.0.0.1:11434","model":"qwen3"}}}`
	})
	tests := []struct {
		name    string
		command string
		want    remoteapproval.Details
	}{
		{
			"agent config dir is another project",
			"agentctl dispatch --agent blogger --prompt-key blog-post",
			remoteapproval.Details{
				Project: "blsmth.com", Dir: dispatchplan.AbbrevHome(f.blog), DirSource: "agent config",
				AskingProject: "agentctl", OtherProject: true,
				Agent: "blogger", AgentSource: "global", Model: "opus", ModelSource: "agent file",
				Mode: "ad-hoc", Worktree: "worktree", PermissionMode: "acceptEdits",
			},
		},
		{
			"flag dir, model and backend, no worktree",
			"agentctl dispatch --agent go-engineer --dir " + f.blog + " --backend local --model qwen3:32b --no-worktree --prompt x",
			remoteapproval.Details{
				Project: "blsmth.com", Dir: dispatchplan.AbbrevHome(f.blog), DirSource: "flag",
				AskingProject: "agentctl", OtherProject: true,
				Agent: "go-engineer", AgentSource: "global", Model: "qwen3:32b", ModelSource: "flag", Backend: "local",
				Mode: "ad-hoc", Worktree: "no worktree", PermissionMode: "auto",
			},
		},
		{
			"issue in the asking project",
			"agentctl dispatch --issue 12",
			remoteapproval.Details{
				Project: "agentctl", Dir: dispatchplan.AbbrevHome(f.asking), DirSource: "current dir", AskingProject: "agentctl",
				Agent: "go-engineer", AgentSource: "issue labels", Model: "sonnet", ModelSource: "mode default",
				Mode: "issue #12", Worktree: "worktree", PermissionMode: "auto",
			},
		},
		{
			"review defaults to the reviewer, unknown backend and harness flagged",
			"agentctl dispatch --review-pr 7 --backend ghost --harness emacs",
			remoteapproval.Details{
				Project: "agentctl", Dir: dispatchplan.AbbrevHome(f.asking), DirSource: "current dir", AskingProject: "agentctl",
				Agent: "code-reviewer", AgentSource: "not found", Model: "default", Harness: "emacs (unknown)",
				Mode: "review PR #7", Worktree: "worktree",
			},
		},
		{
			"pi harness drops the permission mode",
			"agentctl dispatch --agent go-engineer --harness pi --prompt x",
			remoteapproval.Details{
				Project: "agentctl", Dir: dispatchplan.AbbrevHome(f.asking), DirSource: "current dir", AskingProject: "agentctl",
				Agent: "go-engineer", AgentSource: "global", Model: "default", Harness: "pi",
				Mode: "ad-hoc", Worktree: "worktree",
			},
		},
		{
			"unconfigured backend",
			"agentctl dispatch --agent go-engineer --backend ghost --prompt x",
			remoteapproval.Details{
				Project: "agentctl", Dir: dispatchplan.AbbrevHome(f.asking), DirSource: "current dir", AskingProject: "agentctl",
				Agent: "go-engineer", AgentSource: "global", Model: "default", Backend: "ghost (not configured)",
				Mode: "ad-hoc", Worktree: "worktree", PermissionMode: "auto",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveDispatchDetails(config.Load(), tt.command, f.asking)
			if got != tt.want {
				t.Errorf("details =\n %+v\nwant\n %+v", got, tt.want)
			}
		})
	}
}

// The phone receives the resolved details with the approval, so they are
// covered by the payload hash it answers with.
func TestDispatchGateSendsDetailsToPhone(t *testing.T) {
	f := newDetailsFixture(t, func(blog string) string {
		return `{"agents":{"blogger":{"dir":"` + blog + `"}}}`
	})
	isolateContextStore(t)("blog-post", "Write the turnaround post.")
	t.Setenv("TMUX", "/tmp/fake-tmux-socket")
	t.Setenv("AGENTCTL_SESSION_ID", "sess-details")
	command := "agentctl dispatch --agent blogger --prompt-key blog-post"
	removeDispatchLockForTest(t, command)
	stubPopupSeams(t, 5*time.Second)
	fs := withFakeServe(t)
	installBlockingRunner(t)

	out := make(chan gateOut, 1)
	go func() {
		a, d, r := dispatchPreviewGate("brandon", command, f.asking)
		out <- gateOut{a, d, r}
	}()
	req := fs.waitRequest()
	want := remoteapproval.Details{
		Project: "blsmth.com", Dir: dispatchplan.AbbrevHome(f.blog), DirSource: "agent config", AskingProject: "agentctl", OtherProject: true,
		Agent: "blogger", AgentSource: "global", Model: "opus", ModelSource: "agent file",
		Mode: "ad-hoc", Worktree: "worktree", PermissionMode: "auto",
	}
	if req.Details != want {
		t.Errorf("request details =\n %+v\nwant\n %+v", req.Details, want)
	}
	if req.Prompt != "Write the turnaround post." {
		t.Errorf("prompt = %q", req.Prompt)
	}
	fs.answer(remoteapproval.Allow)
	if g := waitGate(t, out); !g.approved || !g.decided {
		t.Fatalf("got %+v, want phone approval", g)
	}
}
