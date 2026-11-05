package hooks

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

// capturePermissionOut swaps the permissionOut seam for a buffer and
// restores it when the test ends.
func capturePermissionOut(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := permissionOut
	permissionOut = &buf
	t.Cleanup(func() { permissionOut = prev })
	return &buf
}

// withHookStdin replaces os.Stdin with a pipe seeded with payload for the
// duration of the test, so decodeInput (which reads os.Stdin directly) sees
// it. Restores the original os.Stdin on cleanup.
func withHookStdin(t *testing.T, payload string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdin pipe: %v", err)
	}
	if _, err := io.WriteString(w, payload); err != nil {
		t.Fatalf("write stdin payload: %v", err)
	}
	_ = w.Close()

	prev := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = prev
		_ = r.Close()
	})
}

// decodedBehavior parses captured hook output and returns the decision
// behavior. ok is false when nothing was emitted (the pass-through case).
func decodedBehavior(t *testing.T, raw string) (behavior string, ok bool) {
	t.Helper()
	if strings.TrimSpace(raw) == "" {
		return "", false
	}
	var dec permissionDecision
	if err := json.Unmarshal([]byte(raw), &dec); err != nil {
		t.Fatalf("emitted output is not valid decision JSON: %v (raw: %q)", err, raw)
	}
	return dec.HookSpecificOutput.Decision.Behavior, true
}

// TestPassThroughPermission_EmitsNothing pins the core security property:
// declining to decide must produce zero bytes on stdout. Emitting an
// "allow" here is what previously auto-approved every non-dispatch Bash
// command, so this asserts absence, not a return value.
func TestPassThroughPermission_EmitsNothing(t *testing.T) {
	buf := capturePermissionOut(t)

	passThroughPermission("test reason %d", 1)

	if got := buf.String(); got != "" {
		t.Errorf("passThroughPermission wrote %q, want no output at all", got)
	}
}

// TestPermissionWrite_NotInTmux_PassesThrough covers the fail-safe path:
// with no tmux there is no popup, so there is no operator verdict, so the
// hook must stay silent rather than approving the write.
func TestPermissionWrite_NotInTmux_PassesThrough(t *testing.T) {
	t.Setenv("TMUX", "")
	buf := capturePermissionOut(t)

	PermissionWriteWithInput("/tmp/some-file.go", "package main")

	if behavior, ok := decodedBehavior(t, buf.String()); ok {
		t.Errorf("emitted decision %q with no popup available, want pass-through (no output)", behavior)
	}
}

// TestDispatchPreviewGate_NotInTmux_PassesThrough is the same fail-safe for
// the dispatch popup gate, which now lives on PreToolUse (see
// dispatchPreviewGate in dispatch_preview.go and its caller in
// validate.go). An unreviewed dispatch spawns an agent, so this path must
// never report approved=true, and decided=false must mean "no verdict was
// reached", not "assume approval".
func TestDispatchPreviewGate_NotInTmux_PassesThrough(t *testing.T) {
	t.Setenv("TMUX", "")

	approved, decided := dispatchPreviewGate("lead", "agentctl dispatch --agent go-engineer --prompt hi")

	if approved || decided {
		t.Errorf("dispatchPreviewGate() = (approved=%v, decided=%v) with no popup available, want (false, false)", approved, decided)
	}
}

// TestPermissionRequest_Bash_PassesThrough pins the actual bug this change
// fixes: PermissionRequest previously emitted an explicit "allow" for every
// Bash tool call, silently auto-approving arbitrary commands with no human
// in the loop and bypassing each agent's allowed_permissions list. The
// dispatch approval gate now lives entirely on PreToolUse (see
// dispatchPreviewGate / ValidateBash), so PermissionRequest itself must
// produce no output at all for Bash, dispatch command or not.
func TestPermissionRequest_Bash_PassesThrough(t *testing.T) {
	buf := capturePermissionOut(t)
	withHookStdin(t, `{"tool_name":"Bash","tool_input":{"command":"ls -la"}}`)

	PermissionRequest()

	if behavior, ok := decodedBehavior(t, buf.String()); ok {
		t.Errorf("PermissionRequest emitted decision %q for a non-dispatch Bash command, want pass-through (no output)", behavior)
	}
}

// TestPermissionRequest_UnknownTool_PassesThrough covers the default branch:
// any tool this hook doesn't explicitly preview must also stay silent
// rather than synthesizing an allow.
func TestPermissionRequest_UnknownTool_PassesThrough(t *testing.T) {
	buf := capturePermissionOut(t)
	withHookStdin(t, `{"tool_name":"Read","tool_input":{}}`)

	PermissionRequest()

	if behavior, ok := decodedBehavior(t, buf.String()); ok {
		t.Errorf("PermissionRequest emitted decision %q for an unknown tool, want pass-through (no output)", behavior)
	}
}

// TestWritePermissionDecision_Shape guards the wire format against a
// well-meaning "simplification" of decision to a bare string. Claude Code
// validates PermissionRequest output as a union of {behavior:"allow",...}
// and {behavior:"deny",...}; a decision that fails validation is silently
// treated as no decision, which would turn an explicit deny into a
// pass-through without any visible error.
func TestWritePermissionDecision_Shape(t *testing.T) {
	for _, tc := range []struct {
		name     string
		behavior string
		message  string
	}{
		{name: "allow", behavior: "allow"},
		{name: "deny with message", behavior: "deny", message: "User denied file write"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf := capturePermissionOut(t)

			writePermissionDecision(tc.behavior, tc.message)

			var probe struct {
				HookSpecificOutput struct {
					HookEventName string `json:"hookEventName"`
					Decision      struct {
						Behavior string `json:"behavior"`
						Message  string `json:"message"`
					} `json:"decision"`
				} `json:"hookSpecificOutput"`
			}
			if err := json.Unmarshal(buf.Bytes(), &probe); err != nil {
				t.Fatalf("output is not valid JSON: %v (raw: %q)", err, buf.String())
			}
			if got := probe.HookSpecificOutput.HookEventName; got != "PermissionRequest" {
				t.Errorf("hookEventName = %q, want %q", got, "PermissionRequest")
			}
			if got := probe.HookSpecificOutput.Decision.Behavior; got != tc.behavior {
				t.Errorf("decision.behavior = %q, want %q", got, tc.behavior)
			}
			if got := probe.HookSpecificOutput.Decision.Message; got != tc.message {
				t.Errorf("decision.message = %q, want %q", got, tc.message)
			}
		})
	}
}

// TestWritePermissionDecision_OmitsEmptyMessage keeps an empty message out
// of the payload entirely rather than sending "message":"".
func TestWritePermissionDecision_OmitsEmptyMessage(t *testing.T) {
	buf := capturePermissionOut(t)

	writePermissionDecision("allow", "")

	if strings.Contains(buf.String(), "message") {
		t.Errorf("output %q should omit an empty message field", buf.String())
	}
}

func TestAgentctlBinaryPath(t *testing.T) {
	if path := agentctlBinaryPath(); path == "" {
		t.Error("agentctlBinaryPath returned empty string")
	}
}
