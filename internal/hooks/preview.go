package hooks

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/selfpath"
	"github.com/blsmth/agentctl/internal/shellquote"
	"github.com/blsmth/agentctl/internal/tui"
)

// filePreviewPopupCmd returns the shell command line tmux display-popup runs
// for the file-write preview. display-popup hands its command to the user's
// shell, so every part -- including the binary path, which can contain spaces
// -- is quoted individually.
func filePreviewPopupCmd(agentctlBin, tmpPath, filePath, agentName string, contentWidth, vpHeight int) string {
	return shellquote.Join(
		agentctlBin, "file-preview",
		"--path", tmpPath,
		"--name", filePath,
		"--agent", agentName,
		"--content-width", strconv.Itoa(contentWidth),
		"--vp-height", strconv.Itoa(vpHeight),
	)
}

// permissionRequestInput is the JSON payload from Claude Code's PermissionRequest hook.
type permissionRequestInput struct {
	ToolName  string              `json:"tool_name"`
	ToolInput permissionToolInput `json:"tool_input"`
}

type permissionToolInput struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
	Command  string `json:"command"` // populated for Bash tool permission requests
}

// permissionDecision is the allow/deny JSON output expected by Claude Code.
type permissionDecision struct {
	HookSpecificOutput struct {
		HookEventName string `json:"hookEventName"`
		Decision      struct {
			Behavior string `json:"behavior"`
			Message  string `json:"message,omitempty"`
		} `json:"decision"`
	} `json:"hookSpecificOutput"`
}

// PermissionRequest is the handler for Claude Code's PermissionRequest hook.
//
// This hook exists to show a human a rich tmux popup preview for the two
// cases worth previewing: a Write's diff, and an `agentctl dispatch`'s
// resolved prompt. It is NOT an approval mechanism. Anything it does not
// preview -- and every failure path within the cases it does -- must PASS
// THROUGH, meaning emit no decision at all so Claude Code falls back to its
// normal permission flow (match permissions.allow, otherwise prompt the
// operator).
//
// Passing through is expressed as "exit 0, write nothing to stdout", which
// is the only correct spelling for this event. Claude Code's consumer is
// `case "PermissionRequest": if (hookSpecificOutput.decision) {...}` -- an
// absent decision leaves permissionRequestResult unset and the normal flow
// runs. Note there is deliberately no "defer" behavior used here: the
// PermissionRequest schema is a discriminated union of exactly
// {behavior:"allow"} and {behavior:"deny"}, so emitting "defer" would fail
// schema validation. ("defer" belongs to PreToolUse, where it is also
// print-mode only and ignored in interactive sessions.)
//
// This previously returned an explicit "allow" on every non-preview path,
// which silently auto-approved every non-dispatch Bash command and turned
// each of the error paths below into a permission bypass -- defeating the
// per-agent allowed_permissions lists entirely. Fail open here means
// "approve arbitrary commands with no human in the loop", so every one of
// these paths must fail SAFE instead.
func PermissionRequest() {
	var input permissionRequestInput
	if !decodeInput(&input) {
		// Undecodable payload: no output, normal flow decides.
		return
	}

	switch input.ToolName {
	case "Write":
		// NOTE: this almost certainly has the same non-blocking defect as the
		// old Bash/dispatch path below -- Claude Code does not wait for the
		// PermissionRequest hook, so a slow-to-decide popup here can race the
		// Write the same way the dispatch popup raced the Bash tool call (see
		// ValidateBash's dispatch gate and its comment for the full story).
		// That is a real, tracked bug, but it is NOT fixed in this change --
		// only the dispatch/Bash path was moved to PreToolUse here. Leave
		// this branch as-is.
		PermissionWriteWithInput(input.ToolInput.FilePath, input.ToolInput.Content)
	case "Bash":
		// PreToolUse's hook-validate-bash now owns the dispatch approval gate
		// (see ValidateBash / dispatchPreviewGate in validate.go and
		// dispatch_preview.go) because it's the only hook Claude Code actually
		// blocks on. Emitting an explicit "allow" here would bypass the
		// user's own permission settings for the command -- dispatch or not
		// -- so just pass through and let those settings (and the PreToolUse
		// gate, for dispatch commands) decide.
		passThroughPermission("Bash command, deferring to normal permission flow")
	default:
		passThroughPermission("tool %q not previewed, deferring to normal permission flow", input.ToolName)
	}
}

// passThroughPermission declines to decide, logging why. It deliberately
// writes NOTHING to stdout -- see PermissionRequest's doc comment for why
// silence (not an "allow", and not a "defer") is the correct way to hand
// the decision back to Claude Code's normal permission flow.
func passThroughPermission(format string, args ...interface{}) {
	logging.Log("hook-permission-request: "+format, args...)
}

// PermissionWriteWithInput handles the Write permission request with the given
// file path and content. Exported for testing.
func PermissionWriteWithInput(filePath, content string) {
	// Check if we're in a tmux session — can't show popup otherwise.
	if os.Getenv("TMUX") == "" {
		passThroughPermission("not in tmux session, cannot preview write to %s", filePath)
		return
	}

	// Write content to temp file.
	tmpFile, err := os.CreateTemp("", "agentctl-preview-*")
	if err != nil {
		passThroughPermission("failed to create temp file, cannot preview: %v", err)
		return
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := tmpFile.WriteString(content); err != nil {
		tmpFile.Close()
		passThroughPermission("failed to write temp file, cannot preview: %v", err)
		return
	}
	tmpFile.Close()

	// Resolve the popup's sizing inputs from the attached client BEFORE
	// building the tmux popup, so the same values can be passed to the
	// "file-preview" subprocess below via --content-width/--vp-height.
	contentWidth, vpHeight := filePreviewFit()

	// Build popup dimensions from the worst-case size the model itself
	// computes (tui.FilePreviewMaxPopupSize), fed by contentWidth/vpHeight
	// resolved above, so this hook and the model's chrome arithmetic can
	// never drift apart -- see the doc comment on FilePreviewMaxPopupSize
	// for why the hook can't just measure the real content: tmux fixes the
	// popup's size before the model is built. clampPopupToClient is still
	// applied as a final defensive shrink: the readability floors in
	// filePreviewFit can still legitimately exceed a genuinely tiny client.
	sz := clampPopupToClient(tui.FilePreviewMaxPopupSize(
		tui.WithFileContentWidth(contentWidth),
		tui.WithFileMaxViewportHeight(vpHeight),
	))
	popupW := sz.Width
	popupH := sz.Height

	// Read agent name from environment (set by agentctl dispatch).
	agentName := os.Getenv("AGENTCTL_AGENT_NAME")

	// Build the agentctl binary path.
	agentctlBin := agentctlBinaryPath()

	// Launch tmux popup.
	cmd := exec.Command("tmux", "display-popup",
		"-E",
		"-w", strconv.Itoa(popupW),
		"-h", strconv.Itoa(popupH),
		filePreviewPopupCmd(agentctlBin, tmpPath, filePath, agentName, contentWidth, vpHeight),
	)
	logging.Log("hook-permission-request: launching popup for file=%s", filePath)

	if err := cmd.Run(); err != nil {
		// Non-zero exit from tmux display-popup: could be deny (exit 1) or
		// popup launch failure (other non-zero). Check exit code.
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode := exitErr.ExitCode()
			logging.Log("hook-permission-request: popup exit code=%d file=%s", exitCode, filePath)
			if exitCode == 1 {
				// User explicitly denied.
				writePermissionDecision("deny", "User denied file write")
				return
			}
		}
		// Popup failed to launch, or exited with an unexpected code. We have
		// no operator verdict, so we must not synthesize one.
		passThroughPermission("popup error, no operator verdict: %v", err)
		return
	}

	// Exit code 0 = user explicitly allowed in the popup.
	writePermissionDecision("allow", "")
}

// permissionOut is where decision JSON is written. A package-level var so
// tests can capture the exact bytes emitted -- in particular, that the
// pass-through paths emit NOTHING at all, which is the whole security
// property being asserted and is invisible to a test that only inspects
// return values.
var permissionOut io.Writer = os.Stdout

// writePermissionDecision writes the hook decision JSON to stdout.
//
// The shape below -- decision as a nested object carrying "behavior" -- is
// what Claude Code actually validates against: the PermissionRequest branch
// of its hook output schema is a union of {behavior:"allow", updatedInput?,
// updatedPermissions?} and {behavior:"deny", message?, interrupt?}. Do not
// "simplify" decision to a bare string; it would fail validation, and a
// failed decision is silently treated as no decision.
//
// Only ever call this with an actual verdict. For "I have no verdict", call
// passThroughPermission instead -- writing "allow" as a stand-in for
// uncertainty is the bug this seam and its tests exist to prevent.
func writePermissionDecision(behavior, message string) {
	var dec permissionDecision
	dec.HookSpecificOutput.HookEventName = "PermissionRequest"
	dec.HookSpecificOutput.Decision.Behavior = behavior
	if message != "" {
		dec.HookSpecificOutput.Decision.Message = message
	}

	out, err := json.Marshal(dec)
	if err != nil {
		logging.Log("hook-permission-request: marshal error: %v", err)
		return
	}
	fmt.Fprintln(permissionOut, string(out))
}

// agentctlBinaryPath returns the path to the running agentctl binary
// (symlinks resolved), falling back to "agentctl" on PATH. It deliberately
// never prefers a conventional install location over the running executable,
// so a stale ~/.local/bin copy is never launched.
func agentctlBinaryPath() string {
	return selfpath.Path()
}
