package hooks

import (
	"encoding/json"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/dispatchapproval"
)

func TestValidateBash_ReviewGatedWhenReviewerNotGlobal(t *testing.T) {
	tests := []struct {
		name      string
		global    bool
		mode      string
		wantGated bool
	}{
		{"global reviewer keeps the exemption", true, "", false},
		{"local reviewer is gated", false, "", true},
		{"off still gates nothing", false, config.DispatchApprovalOff, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dirs := stubReviewerGlobal(t, tt.global)
			gated, _ := runValidateBashPayload(t, &config.Config{DispatchApproval: tt.mode}, "agentctl dispatch --review-pr 5", "/work/tree")
			if gated != tt.wantGated {
				t.Errorf("gated = %v, want %v", gated, tt.wantGated)
			}
			if tt.mode == "" && !slices.Equal(*dirs, []string{"/work/tree"}) {
				t.Errorf("reviewer resolved from %v, want the payload cwd", *dirs)
			}
		})
	}
}

func TestValidateBash_ReviewGatedWhenLineChangesContext(t *testing.T) {
	tests := []struct {
		command   string
		wantGated bool
	}{
		{"agentctl dispatch --review-pr 5", false},
		{"agentctl dispatch --review-pr 5 2>&1 | tail -3", false},
		{"agentctl context set k 'focus' && agentctl dispatch --review-pr 5 -k k", false},
		{"cd /tmp/evil && agentctl dispatch --review-pr 5", true},
		{"cd /tmp/evil; agentctl dispatch --review-pr 5", true},
		{"cd /tmp/evil&&agentctl dispatch --review-pr 5", true},
		{"pushd /tmp/evil >/dev/null && agentctl dispatch --review-pr 5", true},
		{"(cd /tmp/evil && agentctl dispatch --review-pr 5)", true},
		{"env -u AGENTCTL_SESSION_ID agentctl dispatch --review-pr 5", true},
		{"AGENTCTL_SESSION_ID= agentctl dispatch --review-pr 5", true},
		{"unset TMUX_PANE; agentctl dispatch --review-pr 5", true},
		{"$DIR/agentctl dispatch --review-pr 5", true},
		{"time cd evil; time env -u AGENTCTL_SESSION_ID -u TMUX_PANE -u AGENTCTL_WINDOW_ID agentctl dispatch --review-pr 5", true},
		{"! cd evil && nohup env -u AGENTCTL_SESSION_ID -u TMUX_PANE -u AGENTCTL_WINDOW_ID agentctl dispatch --review-pr 5", true},
		{"nice -n 0 env -u AGENTCTL_SESSION_ID agentctl dispatch --review-pr 5", true},
		{"agentctl dispatch --review-pr 5 > out.txt", false},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			gated, _ := runValidateBashWith(t, &config.Config{}, tt.command)
			if gated != tt.wantGated {
				t.Errorf("gated = %v, want %v", gated, tt.wantGated)
			}
		})
	}
}

func TestLineChangesContext(t *testing.T) {
	tests := []struct {
		command string
		want    bool
	}{
		{`agentctl dispatch --review-pr 5 --prompt "cd somewhere first"`, false},
		{`agentctl dispatch --review-pr 5 --prompt "$(cat focus.md)"`, false},
		{`agentctl dispatch --review-pr 5 --model sonnet`, false},
		{`go test ./... && agentctl dispatch --review-pr 5`, true},
		{`echo cd | agentctl dispatch --review-pr 5`, true},
		{`agentctl dispatch --review-pr 5 | tail -3 && echo done`, false},
		{`agentctl context set k v; /usr/local/bin/agentctl dispatch --review-pr 5 2>&1`, false},
		{`agentctl dispatch --review-pr 5 > out.txt`, false},
		{`time cd evil; time env -u AGENTCTL_SESSION_ID -u TMUX_PANE -u AGENTCTL_WINDOW_ID agentctl dispatch --review-pr 5`, true},
		{`! cd evil && nohup env -u AGENTCTL_SESSION_ID -u TMUX_PANE -u AGENTCTL_WINDOW_ID agentctl dispatch --review-pr 5`, true},
		{`nice -n 0 env -u AGENTCTL_SESSION_ID agentctl dispatch --review-pr 5`, true},
		{`if true; then cd evil; fi; agentctl dispatch --review-pr 5`, true},
		{`nohup agentctl dispatch --review-pr 5`, true},
		{`agentctl dispatch --review-pr 1; cd evil; agentctl dispatch --review-pr 2`, true},
		{`cd x && agentctl dispatch --review-pr 5`, true},
		{`X=1 agentctl dispatch --review-pr 5`, true},
		{`true || cd x; agentctl dispatch --review-pr 5`, true},
		{`{ cd x; agentctl dispatch --review-pr 5; }`, true},
		{`source env.sh && agentctl dispatch --review-pr 5`, true},
		{`. ./env.sh && agentctl dispatch --review-pr 5`, true},
		{`/usr/bin/env agentctl dispatch --review-pr 5`, true},
		{`"cd" x && agentctl dispatch --review-pr 5`, true},
		{"(agentctl dispatch --review-pr 5)", true},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			if got := lineChangesContext(tt.command); got != tt.want {
				t.Errorf("lineChangesContext = %v, want %v", got, tt.want)
			}
		})
	}
}

type recordedApproval struct {
	window string
	args   []string
}

func stubRecordApproval(t *testing.T) *[]recordedApproval {
	t.Helper()
	var got []recordedApproval
	orig := recordApproval
	recordApproval = func(window string, args []string) error {
		got = append(got, recordedApproval{window, args})
		return nil
	}
	t.Cleanup(func() { recordApproval = orig })
	return &got
}

// runValidateBashGate runs ValidateBash under dispatch_approval "all" with a
// dispatch gate that answers approved.
func runValidateBashGate(t *testing.T, command string, approved bool) string {
	t.Helper()
	d := DefaultDeps()
	d.LoadConfig = func() *config.Config { return &config.Config{DispatchApproval: config.DispatchApprovalAll} }
	d.LoadProjectConfig = func(string) *config.ProjectConfig { return nil }
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })
	orig := dispatchGate
	dispatchGate = func(_, _, _ string) (bool, bool, string) { return approved, true, "" }
	t.Cleanup(func() { dispatchGate = orig })
	stdin := `{"tool_name":"Bash","tool_input":{"command":` + strconv.Quote(command) + `}}`
	out, _ := runHookCaptured(t, map[string]string{
		"AGENTCTL": "1", "AGENTCTL_AGENT_NAME": "lead", "AGENTCTL_WINDOW_ID": "w99",
		"AGENTCTL_SESSION_ID": "", "TMUX_PANE": "",
	}, stdin, ValidateBash)
	return out
}

func TestValidateBash_ApprovedDispatchRecordsApproval(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		approved bool
		want     []recordedApproval
	}{
		{
			name:     "approved review",
			command:  `agentctl dispatch --review-pr 5 --prompt "focus on locking" 2>&1`,
			approved: true,
			want:     []recordedApproval{{"w99", []string{"--review-pr", "5", "--prompt", "focus on locking"}}},
		},
		{
			name:     "one per dispatch",
			command:  `agentctl dispatch --review-pr 1; agentctl dispatch --review-pr 2 --agent code-reviewer`,
			approved: true,
			want: []recordedApproval{
				{"w99", []string{"--review-pr", "1"}},
				{"w99", []string{"--review-pr", "2", "--agent", "code-reviewer"}},
			},
		},
		{
			name:     "separate redirection target is not an argument",
			command:  `agentctl dispatch --review-pr 5 > out.txt`,
			approved: true,
			want:     []recordedApproval{{"w99", []string{"--review-pr", "5"}}},
		},
		{
			name:     "expanding words are not recorded",
			command:  `agentctl dispatch --review-pr 5 --prompt "$(cat x)"`,
			approved: true,
		},
		{
			name:    "denied records nothing",
			command: `agentctl dispatch --review-pr 5`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stubRecordApproval(t)
			out := runValidateBashGate(t, tt.command, tt.approved)
			decision, _ := decisionOf(t, out)
			if want := map[bool]string{true: "allow", false: "deny"}[tt.approved]; decision != want {
				t.Errorf("decision = %q, want %q", decision, want)
			}
			if len(*got) != len(tt.want) {
				t.Fatalf("recorded %+v, want %+v", *got, tt.want)
			}
			for i := range tt.want {
				if (*got)[i].window != tt.want[i].window || !slices.Equal((*got)[i].args, tt.want[i].args) {
					t.Errorf("recorded[%d] = %+v, want %+v", i, (*got)[i], tt.want[i])
				}
			}
		})
	}
}

// pinApprovalDir points the approval tamper and edit guards at a fixed dir.
func pinApprovalDir(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", "/Users/u")
	orig := approvalStateDir
	approvalStateDir = func() string { return "/Users/u/.local/state/agentctl/dispatch-approvals" }
	t.Cleanup(func() { approvalStateDir = orig })
}

func TestCheckApprovalTamper(t *testing.T) {
	pinApprovalDir(t)
	tests := []struct {
		name     string
		command  string
		wantDeny bool
	}{
		{"redirect", `echo '{}' > ~/.local/state/agentctl/dispatch-approvals/abc`, true},
		{"absolute redirect", `printf x > /Users/u/.local/state/agentctl/dispatch-approvals/abc`, true},
		{"touch", `touch "$HOME/.local/state/agentctl/dispatch-approvals/abc"`, true},
		{"cp", `cp /tmp/m ~/.local/state/agentctl/dispatch-approvals/abc`, true},
		{"relative after cd", `cd ~/.local/state/agentctl && tee dispatch-approvals/abc < /tmp/m`, true},
		{"mv", `mv /tmp/m dispatch-approvals/abc`, true},
		{"python", `python3 -c "open('/Users/u/.local/state/agentctl/dispatch-approvals/x','w')"`, true},
		{"rm", `rm -rf ~/.local/state/agentctl/dispatch-approvals`, true},
		{"chained", `go build ./... && echo x > ~/.local/state/agentctl/dispatch-approvals/x`, true},

		{"list", `ls ~/.local/state/agentctl/dispatch-approvals`, false},
		{"cat", `cat ~/.local/state/agentctl/dispatch-approvals/x`, false},
		{"unrelated", `go test ./internal/dispatchapproval/`, false},
		{"agentctl argument", `agentctl context set note 'never > dispatch-approvals'`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkApprovalTamper(tt.command)
			if (got != "") != tt.wantDeny {
				t.Errorf("checkApprovalTamper(%q) = %q, want deny=%v", tt.command, got, tt.wantDeny)
			}
			if got != "" && checkGlobalDenyList(tt.command) == "" {
				t.Error("checkGlobalDenyList does not apply the rule")
			}
		})
	}
}

func TestApprovalEditTarget(t *testing.T) {
	pinApprovalDir(t)
	input := func(field, path string) json.RawMessage {
		b, _ := json.Marshal(map[string]string{field: path})
		return b
	}
	tests := []struct {
		name  string
		tool  string
		input json.RawMessage
		want  string
	}{
		{"write marker", "Write", input("file_path", "/Users/u/.local/state/agentctl/dispatch-approvals/abc"), "/Users/u/.local/state/agentctl/dispatch-approvals/abc"},
		{"tilde", "Write", input("file_path", "~/.local/state/agentctl/dispatch-approvals/abc"), "/Users/u/.local/state/agentctl/dispatch-approvals/abc"},
		{"elsewhere by name", "Edit", input("file_path", "/tmp/x/dispatch-approvals/abc"), "/tmp/x/dispatch-approvals/abc"},
		{"read marker", "Read", input("file_path", "/Users/u/.local/state/agentctl/dispatch-approvals/abc"), ""},
		{"source file", "Write", input("file_path", "/src/internal/dispatchapproval/dispatchapproval.go"), ""},
		{"bad input", "Write", json.RawMessage(`nope`), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := approvalEditTarget(tt.tool, tt.input); got != tt.want {
				t.Errorf("approvalEditTarget = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPreTool_DeniesApprovalMarkerWrite(t *testing.T) {
	pinApprovalDir(t)
	stdin := `{"tool_name":"Write","tool_input":{"file_path":"/Users/u/.local/state/agentctl/dispatch-approvals/abc","content":"{}"}}`
	var exits []int
	origExit := exitFunc
	t.Cleanup(func() { exitFunc = origExit })
	out, _ := runHookCaptured(t, map[string]string{"AGENTCTL": "1", "AGENTCTL_WINDOW_ID": "", "TMUX_PANE": ""}, stdin, func() {
		exitFunc = func(code int) { exits = append(exits, code) }
		PreTool()
	})
	decision, reason := decisionOf(t, out)
	if decision != "deny" || !strings.Contains(reason, "dispatch-approvals") {
		t.Errorf("decision %q reason %q, want a dispatch-approvals deny", decision, reason)
	}
	if len(exits) != 1 || exits[0] != 2 {
		t.Errorf("exit codes = %v, want [2]", exits)
	}
}

// The marker the hook records is the one the CLI looks up with the argv the
// shell passes it.
func TestValidateBash_RecordedApprovalMatchesCLIArgs(t *testing.T) {
	dir, err := dispatchapproval.Dir()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	runValidateBashGate(t, `agentctl dispatch --review-pr 5 --prompt 'focus on "it"' -k=x 2>/dev/null`, true)
	argv := []string{"--review-pr", "5", "--prompt", `focus on "it"`, "-k=x"}
	if err := dispatchapproval.Consume("w99", argv); err != nil {
		t.Fatalf("Consume with the shell's argv: %v", err)
	}
	if err := dispatchapproval.Consume("w99", argv); err == nil {
		t.Fatal("approval consumed twice")
	}
}
