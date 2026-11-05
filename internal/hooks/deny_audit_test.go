package hooks

// deny_audit_test.go — tests for the EventToolDenied audit trail.
//
// hook-validate-bash denies at PreToolUse by exiting 2, so PostToolUse never
// fires and (before this change) no audit record was ever written for a
// refused command -- the highest-signal event in a security audit log was
// systematically dropped. These tests cover both deny emitters (denyCommand,
// denyBashPopup) writing exactly one EventToolDenied record each, and that a
// failure to write that record never blocks the deny itself.

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/audit"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/paths"
)

// TestDenyEmitters_WriteExactlyOneDenialRecord is the core regression test:
// both PreToolUse deny emitters must append exactly one EventToolDenied
// audit record per denied command, with DeniedBy distinguishing a
// rule-based policy block (denyCommand) from a human popup refusal
// (denyBashPopup), OK=false, and the exact command preserved verbatim in
// Input.
func TestDenyEmitters_WriteExactlyOneDenialRecord(t *testing.T) {
	tests := []struct {
		name         string
		command      string
		emit         func(actx denyAuditContext)
		wantDeniedBy string
	}{
		{
			name:    "denyCommand rule-based deny",
			command: "rm -rf /",
			emit: func(actx denyAuditContext) {
				denyCommand("go-engineer", "rm -rf /", "blocked: recursive delete of root", actx)
			},
			wantDeniedBy: deniedByRule,
		},
		{
			name:    "denyBashPopup human refusal (plain command)",
			command: "kubectl delete pod mypod",
			emit: func(actx denyAuditContext) {
				denyBashPopup("go-engineer", "kubectl delete pod mypod", false, actx)
			},
			wantDeniedBy: deniedByHuman,
		},
		{
			name:    "denyBashPopup human refusal (dispatch)",
			command: "agentctl dispatch --issue 1 --agent go-engineer",
			emit: func(actx denyAuditContext) {
				denyBashPopup("go-engineer", "agentctl dispatch --issue 1 --agent go-engineer", true, actx)
			},
			wantDeniedBy: deniedByHuman,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupAuditRecordTest(t)

			const sessionID = "deny-sess"
			rawInput := json.RawMessage(`{"command":` + strconv.Quote(tt.command) + `}`)
			actx := denyAuditContext{
				WindowID:       "@7",
				Session:        sessionID,
				PermissionMode: "default",
				RawInput:       rawInput,
			}

			_, _, exitCodes := captureDenyEmitter(t, func() { tt.emit(actx) })
			if len(exitCodes) != 1 || exitCodes[0] != 2 {
				t.Fatalf("exitCodes = %v, want exactly one call with code 2", exitCodes)
			}

			records, err := audit.Query(audit.Filter{Session: sessionID})
			if err != nil {
				t.Fatalf("audit.Query: %v", err)
			}
			if len(records) != 1 {
				t.Fatalf("audit.Query returned %d records, want exactly 1: %+v", len(records), records)
			}
			r := records[0]

			if r.Event != audit.EventToolDenied {
				t.Errorf("Event = %q, want %q", r.Event, audit.EventToolDenied)
			}
			if r.DeniedBy != tt.wantDeniedBy {
				t.Errorf("DeniedBy = %q, want %q", r.DeniedBy, tt.wantDeniedBy)
			}
			if r.OK {
				t.Error("OK = true, want false")
			}
			if r.Tool != "Bash" {
				t.Errorf("Tool = %q, want %q", r.Tool, "Bash")
			}
			if string(r.Input) != string(rawInput) {
				t.Errorf("Input = %s, want verbatim tool_input %s", r.Input, rawInput)
			}
			if !strings.Contains(string(r.Input), tt.command) {
				t.Errorf("Input %s does not preserve command %q", r.Input, tt.command)
			}
			if r.Window != actx.WindowID {
				t.Errorf("Window = %q, want %q", r.Window, actx.WindowID)
			}
			if r.PermissionMode != actx.PermissionMode {
				t.Errorf("PermissionMode = %q, want %q", r.PermissionMode, actx.PermissionMode)
			}
			if r.Agent != "go-engineer" {
				t.Errorf("Agent = %q, want %q", r.Agent, "go-engineer")
			}
			if r.Summary == "" {
				t.Error("Summary is empty, want a human-scannable summary")
			}
			if r.Error == "" {
				t.Error("Error is empty, want the deny reason")
			}
			if r.DurationMS != 0 {
				t.Errorf("DurationMS = %d, want 0 -- PreToolUse never ran the tool", r.DurationMS)
			}
			if r.Result != nil {
				t.Errorf("Result = %s, want nil -- PreToolUse never produced a result", r.Result)
			}
		})
	}
}

// TestDenyEmitters_StillDenyWhenAuditAppendFails is the hard-constraint
// regression test: a denial audit write must NEVER change deny behavior.
// audit.Append is deliberately made to fail here by leaving paths
// uninitialized (paths.RuntimeDir() == ""), and both emitters must still
// invoke exitFunc(2) exactly once -- the audit failure must be logged and
// swallowed, never allowed to affect enforcement.
func TestDenyEmitters_StillDenyWhenAuditAppendFails(t *testing.T) {
	paths.ResetForTest()
	t.Cleanup(paths.ResetForTest)

	tests := []struct {
		name string
		emit func(actx denyAuditContext)
	}{
		{
			name: "denyCommand",
			emit: func(actx denyAuditContext) {
				denyCommand("go-engineer", "rm -rf /", "blocked", actx)
			},
		},
		{
			name: "denyBashPopup",
			emit: func(actx denyAuditContext) {
				denyBashPopup("go-engineer", "rm -rf /tmp/scratch", false, actx)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actx := denyAuditContext{
				WindowID: "@1",
				Session:  "sess-audit-fail",
				RawInput: json.RawMessage(`{"command":"x"}`),
			}

			_, _, exitCodes := captureDenyEmitter(t, func() { tt.emit(actx) })
			if len(exitCodes) != 1 || exitCodes[0] != 2 {
				t.Fatalf("exitCodes = %v, want exactly one call with code 2 even though audit.Append fails (paths uninitialized)", exitCodes)
			}
		})
	}
}

// TestValidateBash_GlobalDeny_WritesDenialRecord is an end-to-end check
// (real stdin decode, real resolveHookWindowID, real appendAuditRecord) that
// the global deny list path (checkGlobalDenyList -> denyCommand) writes a
// rule-based EventToolDenied record with Session/PermissionMode/Window
// threaded from the real PreToolUse payload, not just from a hand-built
// denyAuditContext as in the unit test above.
func TestValidateBash_GlobalDeny_WritesDenialRecord(t *testing.T) {
	setupAuditRecordTest(t)
	d := stubDeps(t)
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	const sessionID = "sess-global-deny-e2e"
	envVars := map[string]string{
		"AGENTCTL":            "1",
		"AGENTCTL_AGENT_NAME": "go-engineer",
		"AGENTCTL_WINDOW_ID":  "@11",
		"TMUX_PANE":           "",
		"AGENTCTL_SESSION_ID": "",
	}
	stdin := `{"tool_name":"Bash","tool_input":{"command":"git push origin main"},"session_id":"` + sessionID + `","permission_mode":"default"}`

	runHookCaptured(t, envVars, stdin, ValidateBash)

	records, err := audit.Query(audit.Filter{Session: sessionID})
	if err != nil {
		t.Fatalf("audit.Query: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("audit.Query returned %d records, want exactly 1: %+v", len(records), records)
	}
	r := records[0]
	if r.Event != audit.EventToolDenied {
		t.Errorf("Event = %q, want %q", r.Event, audit.EventToolDenied)
	}
	if r.DeniedBy != deniedByRule {
		t.Errorf("DeniedBy = %q, want %q", r.DeniedBy, deniedByRule)
	}
	if r.Window != "@11" {
		t.Errorf("Window = %q, want %q", r.Window, "@11")
	}
	if r.PermissionMode != "default" {
		t.Errorf("PermissionMode = %q, want %q", r.PermissionMode, "default")
	}
	if !strings.Contains(string(r.Input), "git push origin main") {
		t.Errorf("Input %s does not preserve the denied command", r.Input)
	}
}

// TestValidateBash_PopupDeny_WritesDenialRecord is the end-to-end
// counterpart for the human-refusal path (bashPreviewGate -> denyBashPopup).
func TestValidateBash_PopupDeny_WritesDenialRecord(t *testing.T) {
	setupAuditRecordTest(t)

	const agentName = "go-engineer"
	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		agentName: {BashPreview: boolPtr(true)},
	}}
	withBashPreviewDeps(t, cfg)

	const sessionID = "sess-popup-deny-e2e"
	const command = "rm -rf /tmp/some-scratch-dir"
	withFakeAttachedClient(t, true)
	t.Setenv("AGENTCTL_SESSION_ID", sessionID)
	removeDispatchLockForTest(t, command)
	withFakeDispatchPopupRunner(t, func(popupW, popupH int, argsFilePath string) error {
		return exitError1(t)
	})

	envVars := map[string]string{
		"AGENTCTL":            "1",
		"AGENTCTL_AGENT_NAME": agentName,
		"AGENTCTL_WINDOW_ID":  "@22",
		"AGENTCTL_SESSION_ID": sessionID,
		"TMUX_PANE":           "",
		"TMUX":                "/tmp/fake-tmux-socket",
	}
	stdin := `{"tool_name":"Bash","tool_input":{"command":"` + command + `"},"session_id":"` + sessionID + `","permission_mode":"default"}`

	runHookCaptured(t, envVars, stdin, ValidateBash)

	records, err := audit.Query(audit.Filter{Session: sessionID})
	if err != nil {
		t.Fatalf("audit.Query: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("audit.Query returned %d records, want exactly 1: %+v", len(records), records)
	}
	r := records[0]
	if r.Event != audit.EventToolDenied {
		t.Errorf("Event = %q, want %q", r.Event, audit.EventToolDenied)
	}
	if r.DeniedBy != deniedByHuman {
		t.Errorf("DeniedBy = %q, want %q", r.DeniedBy, deniedByHuman)
	}
	if !strings.Contains(string(r.Input), command) {
		t.Errorf("Input %s does not preserve the denied command", r.Input)
	}
}
