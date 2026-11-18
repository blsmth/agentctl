package dispatch

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/tmux/launchtest"
)

const (
	finishingMarker = "When you are finished — follow these steps IN ORDER"
	execMarker      = "Execute this ticket autonomously"
	mergeLine       = "Do not merge to main."
	orchMarker      = "### Delegate"
	taskHeading     = "## Task"
)

// longPrompt is over longPromptThreshold, so a worker adhoc launch puts it in
// the system prompt under "## Task".
var longPrompt = "Audit the repo.\n\n" + strings.Repeat("Check every package carefully. ", 30)

func TestApplyAgentRole(t *testing.T) {
	yes := true
	tests := []struct {
		name          string
		req           Request
		role          string
		wantMode      Mode
		wantRole      Role
		wantFinishing bool
		wantWorktree  *bool // nil: not set
		agentIssued   bool  // dispatched from a tracked agent session
	}{
		{"worker adhoc unchanged", Request{Mode: ModeAdhoc}, "", ModeAdhoc, RoleWorker, true, nil, false},
		{"explicit worker unchanged", Request{Mode: ModeAdhoc}, agent.RoleWorker, ModeAdhoc, RoleWorker, true, nil, false},
		{"worker dispatch unchanged", Request{Mode: ModeDispatch, Ticket: 3}, "", ModeDispatch, RoleWorker, true, nil, false},
		{"orchestrator human adhoc becomes bare", Request{Mode: ModeAdhoc, UseWorktree: &yes}, agent.RoleOrchestrator, ModeBare, RoleOrchestrator, false, &yes, false},
		{"orchestrator human adhoc keeps adhoc worktree default", Request{Mode: ModeAdhoc}, agent.RoleOrchestrator, ModeBare, RoleOrchestrator, false, &yes, false},
		{"orchestrator adhoc from an agent session stays adhoc", Request{Mode: ModeAdhoc}, agent.RoleOrchestrator, ModeAdhoc, RoleOrchestrator, false, nil, true},
		{"orchestrator scheduled adhoc stays adhoc", Request{Mode: ModeAdhoc, ScheduledBy: "nightly"}, agent.RoleOrchestrator, ModeAdhoc, RoleOrchestrator, false, nil, false},
		{"orchestrator dispatch stays dispatch", Request{Mode: ModeDispatch, Ticket: 3}, agent.RoleOrchestrator, ModeDispatch, RoleOrchestrator, false, nil, false},
		{"orchestrator bare stays bare", Request{Mode: ModeBare}, agent.RoleOrchestrator, ModeBare, RoleOrchestrator, false, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, spec := applyAgentRole(tt.req, modes[tt.req.Mode], tt.role, !tt.agentIssued)
			if req.Mode != tt.wantMode {
				t.Errorf("Mode = %s, want %s", req.Mode, tt.wantMode)
			}
			if spec.Role != tt.wantRole {
				t.Errorf("Role = %s, want %s", spec.Role, tt.wantRole)
			}
			if spec.IncludeFinishingSteps != tt.wantFinishing {
				t.Errorf("IncludeFinishingSteps = %v, want %v", spec.IncludeFinishingSteps, tt.wantFinishing)
			}
			switch {
			case tt.wantWorktree == nil && req.UseWorktree != tt.req.UseWorktree:
				t.Errorf("UseWorktree changed to %v", *req.UseWorktree)
			case tt.wantWorktree != nil && (req.UseWorktree == nil || *req.UseWorktree != *tt.wantWorktree):
				t.Errorf("UseWorktree = %v, want %v", req.UseWorktree, *tt.wantWorktree)
			}
		})
	}
	// The shared modes table must never be mutated.
	if !modes[ModeAdhoc].IncludeFinishingSteps || modes[ModeDispatch].Role != RoleWorker {
		t.Fatal("applyAgentRole mutated the modes table")
	}
}

// TestOrchestratorSystemPrompt checks the prompt an orchestrator gets in a
// worktree: never the worker tail, and execInstructions only when unattended.
func TestOrchestratorSystemPrompt(t *testing.T) {
	tests := []struct {
		name      string
		req       Request
		role      string
		finishing bool
		exec      bool
		orch      bool
		task      bool
	}{
		{"worker adhoc", Request{Mode: ModeAdhoc, CustomPrompt: longPrompt}, "", true, true, false, true},
		{"orchestrator human adhoc", Request{Mode: ModeAdhoc, CustomPrompt: longPrompt}, agent.RoleOrchestrator, false, false, true, false},
		{"orchestrator scheduled adhoc", Request{Mode: ModeAdhoc, CustomPrompt: longPrompt, ScheduledBy: "nightly-triage"}, agent.RoleOrchestrator, false, true, true, true},
		{"orchestrator dispatch", Request{Mode: ModeDispatch, Ticket: 4, TicketTitle: "t", TicketBody: "b"}, agent.RoleOrchestrator, false, true, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.req.AgentName = "lead"
			req, spec := applyAgentRole(tt.req, modes[tt.req.Mode], tt.role, true)
			req.Orchestrator = Orchestrator{WindowID: "@5", Pane: "%6"}
			if spec.Role == RoleOrchestrator {
				req.Orchestrator = Orchestrator{} // launchOrchestrator drops it
			}
			got := buildSystemPrompt(req, spec, "/repo/.worktrees/x", "agent/lead/adhoc-1", true, nil, "")
			for _, c := range []struct {
				what   string
				marker string
				want   bool
			}{
				{"finishing steps", finishingMarker, tt.finishing},
				{"worktree-branch line", mergeLine, tt.finishing},
				{"execInstructions", execMarker, tt.exec},
				{"orchestrator preamble", orchMarker, tt.orch},
				{"## Task section", taskHeading + "\n", tt.task},
				{"orchestrator section", "### Your orchestrator", !tt.orch},
			} {
				if strings.Contains(got, c.marker) != c.want {
					t.Errorf("%s present = %v, want %v", c.what, !c.want, c.want)
				}
			}
		})
	}
}

const leadAgentMD = "---\nname: lead\ndescription: lead\nrole: orchestrator\n---\nYou coordinate.\n"

var sysFileRe = regexp.MustCompile(`--append-system-prompt-file '([^']+)'`)

// launchedSystemPrompt reads the system prompt file a captured launch names.
func launchedSystemPrompt(t *testing.T, l launchtest.Launch) string {
	t.Helper()
	m := sysFileRe.FindStringSubmatch(l.Command)
	if m == nil {
		t.Fatalf("no system prompt file in %q", l.Command)
	}
	data, err := os.ReadFile(m[1])
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestLaunchGoldenOrchestrator pins the command line, env and window record
// of an orchestrator launched by a human (interactive, prompt as the first
// message) and by cron (autonomous), and checks where the prompt lands.
func TestLaunchGoldenOrchestrator(t *testing.T) {
	no := false
	tests := []struct {
		name       string
		req        Request
		inMessage  bool // the whole prompt is the first user message
		inSystem   bool // the prompt is under ## Task in the system prompt
		exec       bool
		permission bool // --permission-mode auto
	}{
		{"orchestrator-human-adhoc", Request{Mode: ModeAdhoc, CustomPrompt: longPrompt, Detached: true}, true, false, false, false},
		{"orchestrator-scheduled-adhoc", Request{Mode: ModeAdhoc, CustomPrompt: longPrompt, Detached: true, ScheduledBy: "nightly-triage"}, false, true, true, true},
		// Dispatched by another agent session (the stub resolves a tracked
		// dispatcher, Detached is false): unattended, so done and --wait work.
		{"orchestrator-agent-dispatched-adhoc", Request{Mode: ModeAdhoc, CustomPrompt: longPrompt}, false, true, true, true},
		{"orchestrator-dispatch", Request{Mode: ModeDispatch, Ticket: 7, TicketTitle: "Fix it", TicketBody: "body"}, false, false, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := launchtest.Setup(t, goldenConfig, map[string]string{"lead": leadAgentMD})
			prevOrch, prevParent, prevExec := resolveOrchestratorFn, parentAccountFn, execPath
			resolveOrchestratorFn = func() Orchestrator { return Orchestrator{WindowID: "@5", Pane: "%6"} }
			parentAccountFn = func() string { return "" }
			execPath = func() (string, error) { return "/opt/bin/agentctl", nil }
			t.Cleanup(func() { resolveOrchestratorFn, parentAccountFn, execPath = prevOrch, prevParent, prevExec })

			rec := launchtest.Capture(t)
			req := tt.req
			req.AgentName, req.WorkDir, req.RepoSlug, req.UseWorktree, req.Background = "lead", env.Project, "o/r", &no, true
			if _, err := Launch(req); err != nil {
				t.Fatalf("Launch: %v", err)
			}
			l := rec.Only()
			sys := launchedSystemPrompt(t, l)
			if strings.Contains(sys, finishingMarker) {
				t.Error("orchestrator got finishing steps")
			}
			if !strings.Contains(sys, orchMarker) {
				t.Error("orchestrator preamble missing")
			}
			if strings.Contains(sys, "### Your orchestrator") {
				t.Error("orchestrator was given an orchestrator to report to")
			}
			if got := strings.Contains(sys, execMarker); got != tt.exec {
				t.Errorf("execInstructions present = %v, want %v", got, tt.exec)
			}
			if got := strings.Contains(sys, taskHeading+"\n\nAudit the repo."); got != tt.inSystem {
				t.Errorf("prompt in system prompt = %v, want %v", got, tt.inSystem)
			}
			if got := strings.Contains(l.Command, "Check every package carefully."); got != tt.inMessage {
				t.Errorf("prompt in first message = %v, want %v", got, tt.inMessage)
			}
			if got := strings.Contains(l.Command, "--permission-mode 'auto'"); got != tt.permission {
				t.Errorf("--permission-mode auto = %v, want %v", got, tt.permission)
			}
			got := l.Render() + launchtest.WindowRecord("@9001")
			got = env.Normalize(got, launchtest.BinPair(l)...)
			launchtest.CheckGolden(t, filepath.Join("testdata", "golden", "launch-"+tt.name+".txt"), got)
		})
	}
}

func TestBareOversizedPromptGoesToSystemPrompt(t *testing.T) {
	huge := "Huge task.\n" + strings.Repeat("x", maxInlineMessage)
	req := Request{AgentName: "lead", Mode: ModeBare, CustomPrompt: huge}
	if msg := bareUserMessage(req); strings.Contains(msg, "xxxx") {
		t.Error("oversized prompt passed inline")
	}
	if got := buildSystemPrompt(req, modes[ModeBare], "/w", "", false, nil, ""); !strings.Contains(got, taskHeading+"\n\nHuge task.") {
		t.Error("oversized prompt missing from the system prompt")
	}
	small := Request{Mode: ModeBare, CustomPrompt: "hi"}
	if bareUserMessage(small) != "hi" || strings.Contains(buildSystemPrompt(small, modes[ModeBare], "/w", "", false, nil, ""), taskHeading) {
		t.Error("short bare prompt not delivered as the message")
	}
}
