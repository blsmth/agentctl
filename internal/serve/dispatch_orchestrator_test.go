package serve

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/dispatch"
	"github.com/blsmth/agentctl/internal/tmux/launchtest"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// TestPhoneDispatchOrchestratorIsInteractive drives the phone's new-session
// path (launchDispatcher -> dispatch.LaunchAdhoc -> dispatch.Launch, tmux
// captured) for an agent with role: orchestrator: it must start as an
// interactive session on the prompt, never as an autonomous worker that
// commits, pushes and opens a PR.
func TestPhoneDispatchOrchestratorIsInteractive(t *testing.T) {
	tests := []struct {
		name        string
		md          string
		wantMode    string
		wantAuto    bool
		wantMessage string
	}{
		{"orchestrator", "---\nname: lead\nrole: orchestrator\n---\nYou coordinate.\n", "bare", false, "'audit the docs'"},
		{"worker", "---\nname: lead\n---\nYou implement.\n", "adhoc", true, "'audit the docs'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := launchtest.Setup(t, `{}`, map[string]string{"lead": tt.md})
			rec := launchtest.Capture(t)
			d := launchDispatcher{
				repoSlug:        func(string) (string, error) { return "o/r", nil },
				checkRuntimeDir: func(string) error { return nil },
				validateAgent:   func(string, string) error { return nil },
				launch:          dispatch.LaunchAdhoc,
			}
			res, err := d.Dispatch(DispatchRequest{
				Agent: "lead", Prompt: "audit the docs", Dir: env.Project,
				Session: "agentctl-launchtest", RuntimeDir: filepath.Join(env.Project, ".agentctl"),
			})
			if err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			l := rec.Only()
			if got := strings.Contains(l.Command, "--permission-mode 'auto'"); got != tt.wantAuto {
				t.Errorf("--permission-mode auto = %v, want %v\n%s", got, tt.wantAuto, l.Command)
			}
			if !strings.HasSuffix(strings.TrimSpace(l.Command), tt.wantMessage) {
				t.Errorf("first message is not the prompt: %s", l.Command)
			}
			if l.Env["AGENTCTL_MODE"] != tt.wantMode {
				t.Errorf("AGENTCTL_MODE = %q, want %q", l.Env["AGENTCTL_MODE"], tt.wantMode)
			}
			m := regexp.MustCompile(`--append-system-prompt-file '([^']+)'`).FindStringSubmatch(l.Command)
			if m == nil {
				t.Fatalf("no system prompt file: %s", l.Command)
			}
			sys, err := os.ReadFile(m[1])
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(string(sys), "Open a pull request"); got != tt.wantAuto {
				t.Errorf("finishing steps present = %v, want %v", got, tt.wantAuto)
			}
			ws, err := windowstate.Read(res.WindowID)
			if err != nil || ws.Mode != tt.wantMode || ws.Task != "audit the docs" {
				t.Errorf("windowstate = mode %q task %q (%v)", ws.Mode, ws.Task, err)
			}
		})
	}
}
