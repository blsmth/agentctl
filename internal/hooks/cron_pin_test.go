package hooks

import (
	"encoding/json"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/cron"
	"github.com/blsmth/agentctl/internal/dispatchapproval"
)

func TestBuildCronPreview_PinArgsAndText(t *testing.T) {
	withCronNow(t)
	set := isolateContextStore(t)
	set("spec-5", "Stored key text.")
	withCronJobs(t, cron.Job{Name: "keyed", Schedule: "0 9 * * *", Agent: "ops", Dir: "/srv", PromptKey: "spec-5"})
	tests := []struct {
		name     string
		command  string
		wantArgs []string
		wantText string
	}{
		{
			name:     "add",
			command:  `agentctl cron add j --schedule @daily -a ops --dir /x --prompt-key spec-5 2>&1`,
			wantArgs: []string{"cron", "add", "j", "--schedule", "@daily", "-a", "ops", "--dir", "/x", "--prompt-key", "spec-5"},
			wantText: "Stored key text.",
		},
		{
			name:     "edit",
			command:  `agentctl cron edit keyed --prompt-key=spec-5`,
			wantArgs: []string{"cron", "edit", "keyed", "--prompt-key=spec-5"},
			wantText: "Stored key text.",
		},
		{
			name:     "inline context set is the text",
			command:  `agentctl context set spec-5 'Inline.' && agentctl cron add j --schedule @daily -a ops --dir /x -k spec-5`,
			wantArgs: []string{"cron", "add", "j", "--schedule", "@daily", "-a", "ops", "--dir", "/x", "-k", "spec-5"},
			wantText: "Inline.",
		},
		{name: "inline prompt has no pin", command: cronAddCmd},
		{name: "edit without a key has no pin", command: `agentctl cron edit keyed --timeout 1h`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, deny := buildCronPreview(tt.command)
			if deny != "" {
				t.Fatalf("deny: %s", deny)
			}
			if tt.wantArgs == nil {
				if p.pin != nil {
					t.Fatalf("pin = %+v, want none", p.pin)
				}
				return
			}
			if p.pin == nil || !slices.Equal(p.pin.args, tt.wantArgs) || p.pin.text != tt.wantText || p.pin.key != "spec-5" {
				t.Fatalf("pin = %+v, want args %q text %q", p.pin, tt.wantArgs, tt.wantText)
			}
			if !strings.Contains(p.body, tt.wantText) {
				t.Errorf("body does not show the pinned text:\n%s", p.body)
			}
		})
	}
}

// TestCronPreviewGate_PinIsThePopupRead changes the key while the popup is
// up: the pin is still the text the popup showed.
func TestCronPreviewGate_PinIsThePopupRead(t *testing.T) {
	withCronNow(t)
	set := isolateContextStore(t)
	set("spec-7", "Approved text.")
	command := `agentctl cron add j --schedule @daily -a ops --dir /x -k spec-7`
	t.Setenv("TMUX", "/tmp/fake-tmux-socket")
	t.Setenv("AGENTCTL_SESSION_ID", "sess-cron-pin")
	removeDispatchLockForTest(t, command)
	var shown string
	withFakeDispatchPopupRunner(t, func(_, _ int, argsFile string) error {
		var args DispatchPreviewArgs
		data, err := os.ReadFile(argsFile)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &args); err != nil {
			t.Fatal(err)
		}
		shown = args.Prompt
		set("spec-7", "Swapped after the popup.")
		return nil
	})
	approved, decided, deny, pin := cronPreviewGatePin("orch", command, "")
	if !approved || !decided || deny != "" {
		t.Fatalf("got (%v, %v, %q)", approved, decided, deny)
	}
	if !strings.Contains(shown, "Approved text.") {
		t.Errorf("popup showed:\n%s", shown)
	}
	if pin == nil || pin.text != "Approved text." {
		t.Errorf("pin = %+v, want the text the popup showed", pin)
	}
}

func TestRecordCronPin(t *testing.T) {
	type rec struct {
		window string
		args   []string
		text   string
	}
	var got []rec
	orig := recordPinApproval
	recordPinApproval = func(w string, args []string, text string) error {
		got = append(got, rec{w, args, text})
		return nil
	}
	t.Cleanup(func() { recordPinApproval = orig })

	recordCronPin("@1", nil)
	recordCronPin("", &cronPin{args: []string{"cron", "add"}, key: "k", text: "t"})
	recordCronPin("@1", &cronPin{key: "k", text: "t"}) // expanding words: no args
	if len(got) != 0 {
		t.Fatalf("recorded %+v, want nothing", got)
	}
	recordCronPin("@1", &cronPin{args: []string{"cron", "add", "j"}, key: "k", text: "t"})
	if len(got) != 1 || got[0].window != "@1" || !slices.Equal(got[0].args, []string{"cron", "add", "j"}) || got[0].text != "t" {
		t.Errorf("recorded %+v", got)
	}
}

// TestValidateBash_ApprovedCronKeyRecordsPin runs the whole hook: the
// approved popup's text is what the CLI can consume, keyed by the argv the
// shell passes from "cron" on.
func TestValidateBash_ApprovedCronKeyRecordsPin(t *testing.T) {
	withCronNow(t)
	set := isolateContextStore(t)
	set("spec-8", "Hook-approved text.")
	dir, err := dispatchapproval.Dir()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	command := `agentctl cron add j --schedule @daily -a ops --dir /x -k spec-8`
	removeDispatchLockForTest(t, command)
	withFakeDispatchPopupRunner(t, func(_, _ int, _ string) error { return nil })
	d := DefaultDeps()
	d.LoadConfig = func() *config.Config { return &config.Config{} }
	d.LoadProjectConfig = func(string) *config.ProjectConfig { return nil }
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })
	stdin := `{"tool_name":"Bash","tool_input":{"command":` + strconv.Quote(command) + `}}`
	out, _ := runHookCaptured(t, map[string]string{
		"AGENTCTL": "1", "AGENTCTL_AGENT_NAME": "lead", "AGENTCTL_WINDOW_ID": "w99",
		"AGENTCTL_SESSION_ID": "sess-cron-pin-e2e", "TMUX_PANE": "", "TMUX": "/tmp/fake-tmux-socket",
	}, stdin, ValidateBash)
	if decision, _ := decisionOf(t, out); decision != "allow" {
		t.Fatalf("decision = %q (%s)", decision, out)
	}
	argv := []string{"cron", "add", "j", "--schedule", "@daily", "-a", "ops", "--dir", "/x", "-k", "spec-8"}
	text, err := dispatchapproval.ConsumeCronPrompt("w99", argv)
	if err != nil || text != "Hook-approved text." {
		t.Fatalf("ConsumeCronPrompt = %q, %v", text, err)
	}
	if err := dispatchapproval.Consume("w99", argv); err == nil {
		t.Error("a cron pin satisfied a dispatch approval")
	}
}
