package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/blsmth/agentctl/internal/audit"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/textutil"
)

// exitFunc is the seam production deny paths use to terminate the process
// with exit code 2 -- the only thing that actually blocks a tool call (see
// denyCommand and denyBashPopup). It defaults to os.Exit; tests that
// invoke a deny emitter (directly, or transitively via ValidateBash) must
// swap it for a recorder via t.Cleanup so the test binary survives the call.
var exitFunc = os.Exit

// ValidateBash is called by Claude Code's PreToolUse hook (matcher: Bash).
// It checks the command against per-agent blocked commands/patterns and a global
// deny list. Returns a deny decision if the command is blocked.
func ValidateBash() {
	if !guardAgentctl() {
		return
	}

	var input struct {
		ToolName       string          `json:"tool_name"`
		ToolInput      json.RawMessage `json:"tool_input"`
		SessionID      string          `json:"session_id"`
		PermissionMode string          `json:"permission_mode"`
	}
	if !decodeInput(&input) {
		return
	}

	var toolInput struct {
		Command string `json:"command"`
	}
	if len(input.ToolInput) > 0 {
		if err := json.Unmarshal(input.ToolInput, &toolInput); err != nil {
			logging.Log("hook-validate-bash: decode tool_input: %v", err)
		}
	}

	if toolInput.Command == "" {
		return
	}

	doneTimer := logging.Timing("hook/validate-bash")
	defer doneTimer()

	agentName := os.Getenv("AGENTCTL_AGENT_NAME")
	command := toolInput.Command
	windowID := resolveHookWindowID()

	// actx carries the PreToolUse context both deny emitters need to write an
	// EventToolDenied audit record (see denyAuditContext) -- resolved once
	// here rather than re-derived inside each emitter.
	actx := denyAuditContext{
		WindowID:       windowID,
		Session:        input.SessionID,
		PermissionMode: input.PermissionMode,
		RawInput:       input.ToolInput,
	}

	// Check global deny list first.
	if reason := checkGlobalDenyList(command); reason != "" {
		// denyCommand exits the process (exit 2) in production, so this
		// `return` is unreachable there. It stays in place because tests
		// stub exitFunc to a no-op recorder so the test binary survives the
		// call -- without the `return`, a stubbed exitFunc would let
		// execution fall through and evaluate the deny checks below against a
		// command that has already been denied.
		denyCommand(agentName, command, reason, actx)
		return
	}

	cfg := deps.LoadConfig()

	// Check per-agent blocked commands.
	if agentName != "" {
		ac := cfg.ForAgent(agentName)
		if reason := checkAgentDenyList(command, ac); reason != "" {
			// See the comment on the global-deny-list `return` above -- same
			// reasoning applies here.
			denyCommand(agentName, command, fmt.Sprintf("%s is %s", agentName, reason), actx)
			return
		}
	}

	// Check project-level deny list.
	cwd, _ := os.Getwd()
	projectCfg := deps.LoadProjectConfig(cwd)
	if projectCfg != nil {
		if reason := checkProjectDenyList(command, agentName, projectCfg); reason != "" {
			// See the comment on the global-deny-list `return` above -- same
			// reasoning applies here.
			denyCommand(agentName, command, reason, actx)
			return
		}
	}

	// Decide whether this command is gated behind a human-in-the-loop tmux
	// popup preview. This lives here, on PreToolUse, rather than on
	// PermissionRequest (see preview.go's Bash case) because PreToolUse is
	// the only hook Claude Code actually blocks on before running the tool --
	// PermissionRequest's popup was racing the tool call and losing every
	// time.
	//
	// "agentctl dispatch ..." commands are ALWAYS gated, independent of the
	// bash_preview setting -- this is unchanged from before bash_preview
	// existed. Every other Bash command is gated only when the operator has
	// opted into bash_preview for this agent (default: off, since an
	// unattended dispatched agent must never block on a popup nobody will
	// ever see -- see bashPreviewGate / hasAttachedClient).
	isDispatch := isAgentctlDispatch(command)
	if isDispatch || cfg.EffectiveBashPreview(agentName) {
		var approved, decided bool
		if isDispatch {
			approved, decided = dispatchPreviewGate(agentName, command)
		} else {
			approved, decided = bashPreviewGate(agentName, command)
		}

		if decided {
			if approved {
				// The ONLY place in ValidateBash that may emit an explicit
				// PreToolUse allow: a human gave an affirmative verdict for
				// THIS EXACT command in the popup. See allowCommand's doc
				// comment for the safety invariant this enforces.
				allowCommand(agentName, command)
			} else {
				// See the comment on the global-deny-list `return` above --
				// same reasoning applies here.
				denyBashPopup(agentName, command, isDispatch, actx)
			}
			return
		}
		// !decided means no popup was shown (not in tmux, no attached
		// client, launch failure, or another invocation already owns the
		// gate) -- stay silent and let Claude Code's normal permission flow
		// decide, same as every other pass-through path.
	}

	// Allow -- exit 0 with no output.
	logging.Log("hook/validate-bash: approved window=%s agent=%s cmd=%s", windowID, agentName, textutil.Truncate(command, 100, "..."))
}

// checkAgentDenyList checks a command against the per-agent blocked commands
// and patterns from ac. Returns a non-empty reason string if the command
// should be denied, or "" if it is allowed. It is a pure function with no
// side-effects, making it easy to unit-test.
func checkAgentDenyList(command string, ac config.AgentConfig) string {
	for _, blocked := range ac.BlockedCommands {
		if strings.Contains(command, blocked) {
			return fmt.Sprintf("not allowed to run commands containing %q", blocked)
		}
	}
	for _, pattern := range ac.BlockedPatterns {
		matched, err := regexp.MatchString(pattern, command)
		if err != nil {
			// Skip invalid patterns -- don't block on misconfiguration.
			logging.Log("hook-validate-bash: bad pattern %q: %v", pattern, err)
			continue
		}
		if matched {
			return fmt.Sprintf("not allowed to run commands matching pattern %q", pattern)
		}
	}
	return ""
}

// checkProjectDenyList checks a command against the project-level blocked
// commands, patterns, protected branches, and per-agent overrides from
// projectCfg. Returns a non-empty reason string if the command should be
// denied, or "" if it is allowed. Project config is additive -- it can add
// restrictions but never weaken user-level or global blocks.
func checkProjectDenyList(command, agentName string, projectCfg *config.ProjectConfig) string {
	// Check project-level blocked commands (substring match).
	for _, blocked := range projectCfg.BlockedCommands {
		if strings.Contains(command, blocked) {
			return fmt.Sprintf("blocked by project config: command contains %q", blocked)
		}
	}

	// Check project-level blocked patterns (regex match).
	for _, pattern := range projectCfg.BlockedPatterns {
		matched, err := regexp.MatchString(pattern, command)
		if err != nil {
			logging.Log("checkProjectDenyList: bad pattern %q: %v", pattern, err)
			continue
		}
		if matched {
			return fmt.Sprintf("blocked by project config: command matches pattern %q", pattern)
		}
	}

	// Check expanded protected branches.
	for _, pattern := range projectCfg.ExpandProtectedBranches() {
		matched, err := regexp.MatchString(pattern, command)
		if err != nil {
			logging.Log("checkProjectDenyList: bad branch pattern %q: %v", pattern, err)
			continue
		}
		if matched {
			return "blocked by project config: push to protected branch"
		}
	}

	// Check per-agent overrides from project config.
	if agentName != "" {
		if agentCfg, ok := projectCfg.Agents[agentName]; ok {
			if reason := checkAgentDenyList(command, agentCfg); reason != "" {
				return fmt.Sprintf("blocked by project config (agent %s): %s", agentName, reason)
			}
		}
	}

	return ""
}

// globalDenyEntry holds a pre-compiled regex and its associated deny reason.
type globalDenyEntry struct {
	re     *regexp.Regexp
	reason string
}

// globalDenyPatterns are blocked for ALL agents regardless of config.
// Regexes are compiled once at package init to avoid per-call allocation.
var globalDenyPatterns = []globalDenyEntry{
	{regexp.MustCompile(`rm\s+(-[a-zA-Z]*f[a-zA-Z]*\s+)?/\s*$`), "\U0001F6D1 BLOCKED: recursive delete of root -- are you trying to nuke the machine?"},
	{regexp.MustCompile(`rm\s+(-[a-zA-Z]*f[a-zA-Z]*\s+)?~/?\s*$`), "\U0001F6D1 BLOCKED: recursive delete of home directory -- absolutely not"},
	{regexp.MustCompile(`git\s+push\s+.*origin\s+main\b`), "\U0001F6D1 BLOCKED: direct push to main -- open a PR instead"},
	{regexp.MustCompile(`curl\s.*\|\s*(ba)?sh`), "\U0001F6D1 BLOCKED: piping curl to shell -- download first, inspect, then run"},
	// Catches attempts to invoke agentctl via "go run" (e.g. "cd ~/src/agentctl && go run .").
	// agentctl is always on PATH -- call it directly.
	{regexp.MustCompile(`go\s+run\s+.*agentctl|agentctl.*go\s+run\s+\.`), "\U0001F6D1 BLOCKED: never invoke agentctl via 'go run' -- call 'agentctl' directly from PATH instead"},
}

func checkGlobalDenyList(command string) string {
	for _, deny := range globalDenyPatterns {
		if deny.re.MatchString(command) {
			return deny.reason
		}
	}
	return ""
}

// denyAuditContext carries the PreToolUse fields both deny emitters need to
// write an EventToolDenied audit record, but that neither emitter can derive
// on its own from its existing (agentName, command, ...) parameters --
// resolved once in ValidateBash and threaded through so denyCommand and
// denyBashPopup stay pure with respect to stdin/env.
type denyAuditContext struct {
	WindowID       string
	Session        string
	PermissionMode string
	RawInput       json.RawMessage // the tool_input JSON exactly as received, for Record.Input
}

// Values for audit.Record.DeniedBy -- see its doc comment.
const (
	deniedByRule  = "rule"
	deniedByHuman = "human"
)

// denyCommand denies a Bash command. The exit(2) below -- not the JSON -- is
// what actually blocks the tool call: per Claude Code's hook docs, an exit-0
// process whose stdout JSON fails schema validation is a non-blocking error
// (the action proceeds and the transcript just shows a "hook error" notice),
// while an exit-2 process blocks regardless of what, if anything, it printed.
// The hookSpecificOutput JSON on stdout is presentation only -- it renders a
// nicely formatted reason when Claude Code accepts the schema -- and must
// never be the sole mechanism a deny relies on to take effect. A deny must
// never be reachable via an exit-0 path.
//
// The audit.Append call below is best-effort and must never affect
// enforcement: it runs before exitFunc, but any error from it is logged and
// swallowed exactly like every other appendAuditRecord call site -- a
// failure to record the denial must never allow the denied command through.
// It lives inside this function (not at any of its three call sites in
// ValidateBash) so a denied command produces exactly one audit record no
// matter how many call sites exist -- see the `return` comments at those
// call sites for why more than one call could otherwise happen under a
// stubbed exitFunc.
func denyCommand(agentName, command, reason string, actx denyAuditContext) {
	logging.Log("hook-validate-bash: DENIED agent=%s reason=%q cmd=%q", agentName, reason, command)
	// Truncate the command in the reason for readability (long commands clutter the UI).
	displayCmd := command
	if len(displayCmd) > 80 {
		displayCmd = displayCmd[:80] + "\u2026"
	}
	fullReason := fmt.Sprintf("%s\nBlocked command: %s", reason, displayCmd)
	// Note: hookEventName is REQUIRED as of Claude Code 2.1.231. Omitting it
	// causes Claude Code to show a generic "hook error" instead of the deny
	// reason (Zod validation failure on hookSpecificOutput). Kept for the
	// presentation layer -- see the enforcement note below.
	output := map[string]interface{}{
		"hookSpecificOutput": map[string]string{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "deny",
			"permissionDecisionReason": fullReason,
		},
	}
	json.NewEncoder(os.Stdout).Encode(output) //nolint:errcheck

	appendAuditRecord(audit.Record{
		Event:          audit.EventToolDenied,
		Tool:           "Bash",
		Session:        actx.Session,
		Agent:          agentName,
		Instance:       os.Getenv("AGENTCTL_INSTANCE_NAME"),
		Window:         actx.WindowID,
		PermissionMode: actx.PermissionMode,
		OK:             false,
		Error:          reason,
		Summary:        truncateSummary(fmt.Sprintf("Bash denied by rule: %s", reason)),
		DeniedBy:       deniedByRule,
		Input:          actx.RawInput,
		// No DurationMS, no Result: PreToolUse never ran the tool.
	})

	// Enforcement: stderr carries the plain-text reason (never JSON -- stderr
	// is surfaced verbatim as the blocking reason, and a JSON blob there would
	// just be noise) and exit 2 is what actually stops the tool call. This
	// must happen unconditionally, independent of whether the stdout JSON
	// above validates against Claude Code's schema.
	fmt.Fprint(os.Stderr, fullReason)
	exitFunc(2)
}

// denyBashPopup emits the flat deny decision for a Bash command the human
// rejected in a preview popup -- either the always-on "agentctl dispatch"
// gate or the opt-in bash_preview gate for every other command; isDispatch
// only changes the wording of the reason shown to Claude Code. Uses the same
// flat hookSpecificOutput schema as denyCommand (proven to work) rather than
// the nested decision.behavior form writePermissionDecision emits. Like
// denyCommand, it includes hookEventName -- required as of Claude Code
// 2.1.231; see the comment above. The exit(2) at the end is the actual
// enforcement mechanism (see denyCommand's doc comment for the full
// rationale) -- the JSON is presentation only, and this deny must never be
// reachable via an exit-0 path.
//
// Like denyCommand, the audit.Append call below is best-effort (see its
// comment for the full rationale) and lives inside this function rather than
// at its single call site in ValidateBash, for the same one-record-per-denial
// reasoning.
func denyBashPopup(agentName, command string, isDispatch bool, actx denyAuditContext) {
	reason := "Command denied in preview popup"
	if isDispatch {
		reason = "Dispatch denied in preview popup"
	}
	logging.Log("hook-validate-bash: DENIED via popup agent=%s dispatch=%v cmd=%q", agentName, isDispatch, textutil.Truncate(command, 100, "..."))
	output := map[string]interface{}{
		"hookSpecificOutput": map[string]string{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "deny",
			"permissionDecisionReason": reason,
		},
	}
	json.NewEncoder(os.Stdout).Encode(output) //nolint:errcheck

	appendAuditRecord(audit.Record{
		Event:          audit.EventToolDenied,
		Tool:           "Bash",
		Session:        actx.Session,
		Agent:          agentName,
		Instance:       os.Getenv("AGENTCTL_INSTANCE_NAME"),
		Window:         actx.WindowID,
		PermissionMode: actx.PermissionMode,
		OK:             false,
		Error:          reason,
		Summary:        truncateSummary(fmt.Sprintf("Bash denied by human in preview popup: %s", reason)),
		DeniedBy:       deniedByHuman,
		Input:          actx.RawInput,
		// No DurationMS, no Result: PreToolUse never ran the tool.
	})

	// Enforcement: plain-text reason on stderr (never JSON) plus exit 2.
	fmt.Fprint(os.Stderr, reason)
	exitFunc(2)
}

// allowCommand emits the flat PreToolUse allow decision for a Bash command a
// human explicitly approved in a preview popup.
//
// This is the ONLY place ValidateBash may emit an explicit "allow" for a
// Bash command. Doing so BYPASSES the operator's own permissions.allow list
// and every deny check that ran upstream in this function -- that is only
// safe because a human already gave an affirmative, per-command verdict in
// the popup dispatchPreviewGate/bashPreviewGate just showed them. It must be
// called ONLY from the single `decided && approved` branch in ValidateBash --
// never from a default branch, a fallthrough, or any of the `decided ==
// false` pass-through paths (not in tmux, no attached client, popup launch
// failure, lost the gate lock, unexpected popup exit code). Those must keep
// emitting nothing at all so Claude Code's normal permission flow decides.
//
// This mirrors, one layer down, a fail-open bug already fixed once in
// PermissionRequest (see its doc comment): emitting "allow" as a stand-in
// for "no verdict" turns every uncertain path into a bypass. Do not
// reintroduce that here.
func allowCommand(agentName, command string) {
	logging.Log("hook-validate-bash: ALLOWED via popup agent=%s cmd=%q", agentName, textutil.Truncate(command, 100, "..."))
	output := map[string]interface{}{
		"hookSpecificOutput": map[string]string{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "allow",
			"permissionDecisionReason": "Approved in agentctl preview popup",
		},
	}
	// Written directly to os.Stdout (not cached in a package var) -- same
	// pattern denyCommand/denyBashPopup use, and the pattern every existing
	// test capture helper (captureStdout, captureDenyEmitter, runHookCaptured
	// in hooks_test.go) already relies on: swapping the os.Stdout package
	// variable before the call is a fully sufficient seam as long as the
	// write reads os.Stdout at call time rather than caching it at package
	// init, so no extra indirection is introduced here.
	json.NewEncoder(os.Stdout).Encode(output) //nolint:errcheck
}
