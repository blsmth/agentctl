package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/blsmth/agentctl/internal/audit"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/harness"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/textutil"
)

// exitFunc is the seam production deny paths use to terminate the process
// with exit code 2 -- the only thing that actually blocks a tool call (see
// denyCommand and denyBashPopup). It defaults to os.Exit; tests that
// invoke a deny emitter (directly, or transitively via ValidateBash) must
// swap it for a recorder via t.Cleanup so the test binary survives the call.
var exitFunc = os.Exit

// dispatchGate is the seam ValidateBash uses to show the dispatch approval
// popup; tests swap it to assert whether a dispatch was gated.
var dispatchGate = dispatchPreviewGate

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
	// "agentctl dispatch ..." commands are gated per the global
	// dispatch_approval setting (default "non-review": every dispatch except
	// PR review dispatches; see dispatchNeedsApproval), independent of
	// bash_preview. Every other Bash command is gated only when the operator
	// has opted into bash_preview for this agent (default: off, since an
	// unattended dispatched agent must never block on a popup nobody will
	// ever see -- see bashPreviewGate / hasAttachedClient). A dispatch that
	// dispatch_approval exempts is treated exactly like any other Bash
	// command: with bash_preview on it gets the generic Bash popup, otherwise
	// it passes through silently like the !decided path below (no explicit
	// allow, no approval-pending marker, no blocked state).
	//
	// Gated "agentctl cron" subcommands (add, resume, run --force; see
	// cron_preview.go) are always gated, for the same reason as dispatch:
	// one approval authorizes unattended runs.
	isDispatch := isAgentctlDispatch(command)
	isCron := isAgentctlCronGated(command)
	if isDispatch && isCron {
		// Each popup previews one invocation; mixing them would leave one
		// unseen. Denied regardless of dispatch_approval.
		denyCommand(agentName, command, "Run agentctl dispatch and agentctl cron in separate Bash calls, "+
			"so each can be shown for approval.", actx)
		return
	}
	gateDispatch := isDispatch && dispatchNeedsApproval(command, cfg.EffectiveDispatchApproval())
	if gateDispatch || isCron || cfg.EffectiveBashPreview(agentName) {
		var approved, decided bool
		var denyReason string
		gate := PreviewKindBash
		switch {
		case isCron:
			gate = PreviewKindCron
			approved, decided, denyReason = cronPreviewGate(agentName, command)
		case gateDispatch:
			gate = PreviewKindDispatch
			approved, decided, denyReason = dispatchGate(agentName, command)
		default:
			approved, decided, denyReason = bashPreviewGate(agentName, command)
		}
		if denyReason != "" {
			// The prompt cannot be shown or went unanswered: fail closed.
			denyCommand(agentName, command, denyReason, actx)
			return
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
				denyBashPopup(agentName, command, gate, actx)
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

// serveTamperReason is the deny reason for commands that could subvert
// remote approvals.
const serveTamperReason = "\U0001F6D1 BLOCKED: this touches agentctl serve's state, control socket or process. " +
	"Remote approvals depend on it; ask the user to do this themselves. " +
	"To read serve's logs use `agentctl serve logs` (add --audit for audit.jsonl)."

// serveTamperPatterns protect remote approvals (docs/design/mobile-web-app.md,
// "Remote approvals"). A same-uid agent that could stop agentctl serve and
// bind its own control socket, or pair a device, could answer its own
// approval popups. They deny: references to the serve state dir, its control
// socket or pidfile; stopping or reinstalling the serve LaunchAgent or
// systemd unit (launchctl and the label in either order, across lines);
// killing agentctl processes; and the serve subcommands that pair devices,
// start, stop, restart or remove the server (`serve start|stop|toggle` are
// for the human: the prefix+W binding and the hub's S key and indicator run
// outside Claude Code and are unaffected). This is a speed bump against
// accidents and naive attempts, not a sandbox: a program written to disk can
// still do any of this. Ordinary commands that mention "serve" (go test
// ./internal/serve/..., agentctl serve status|logs, npm run serve) pass;
// `agentctl serve logs` is the read-only way to read serve.log and
// audit.jsonl.
var serveTamperPatterns = []*regexp.Regexp{
	regexp.MustCompile(`control\.sock\b`),
	regexp.MustCompile(`\bserve\.pid\b`),
	regexp.MustCompile(`(?:state/agentctl|XDG_STATE_HOME\}?/agentctl)/serve\b`),
	regexp.MustCompile(`(?s)\blaunchctl\b.*agentctl\.serve\b`),
	regexp.MustCompile(`(?s)agentctl\.serve\b.*\blaunchctl\b`),
	regexp.MustCompile(`\bsystemctl\b[^\n]*agentctl`),
	regexp.MustCompile(`\b(?:pkill|killall)\b[^\n|;&]*agentctl`),
	regexp.MustCompile(`\bkill\b[^\n]*\bagentctl\b[^\n]*\bserve\b`),
	regexp.MustCompile(`\bagentctl\b["')]*\s+["']?serve["']?\s+["']?(?:install|uninstall|pair|ca|start|stop|toggle)\b`),
	// The same from source (agents in this repo run the CLI with go run).
	regexp.MustCompile(`\bgo\s+run\b[^\n;&|]*\s["']?serve["']?\s+["']?(?:install|uninstall|pair|ca|start|stop|toggle)\b`),
}

// serveStateDir is the resolved serve state dir, matched literally (absolute
// and ~-relative) on top of serveTamperPatterns. A var so tests can pin it.
var serveStateDir = func() string {
	dir, err := config.StateDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "serve")
}

// checkServeTamper reports whether command could subvert remote approvals.
func checkServeTamper(command string) string {
	for _, re := range serveTamperPatterns {
		if re.MatchString(command) {
			return serveTamperReason
		}
	}
	if dir := serveStateDir(); dir != "" {
		if strings.Contains(command, dir) {
			return serveTamperReason
		}
		if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(dir, home+"/") {
			if strings.Contains(command, "~/"+strings.TrimPrefix(dir, home+"/")) ||
				strings.Contains(command, "$HOME/"+strings.TrimPrefix(dir, home+"/")) {
				return serveTamperReason
			}
		}
	}
	return ""
}

// cronTamperReason is the deny reason for commands that write the cron jobs
// file or cron state directly.
const cronTamperReason = "\U0001F6D1 BLOCKED: this writes agentctl's cron schedules or state directly. " +
	"Scheduled jobs need human approval: use `agentctl cron add` (shown for approval), " +
	"`agentctl cron pause|rm` to stop one, and `agentctl cron list` to read them."

// cronTargetPattern matches a reference to the cron jobs file or the cron
// state dir (<StateDir>/cron: state.json, runs.ndjson, tick.lock). The
// resolved state dir is matched literally on top, see cronStateDir.
const cronTargetPattern = `(?:schedules\.json|(?:state/agentctl|XDG_STATE_HOME\}?/agentctl)/cron\b)`

// cronWriteVerbs are commands that create, replace, move, delete or change a
// file named among their arguments.
const cronWriteVerbs = `cp|mv|rm|ln|install|rsync|dd|truncate|chmod|chown|touch|unlink|shred|sponge|tee`

// cronTamperPatterns close the side doors around `cron add` approval
// (docs/design/cron.md, "Safety and approval"): an agent that cannot get a
// job approved could write ~/.config/agentctl/schedules.json, or re-enable or
// reset a job in <StateDir>/cron/state.json, itself. They deny a redirect
// into either, a write-ish command (cp, mv, rm, tee, sponge, ...) or an
// in-place sed/perl naming either in the same simple command, and any
// script interpreter whose command line mentions either. Reading is fine
// (cat, jq, head, grep without a redirect into the file), and `agentctl cron
// list` is the intended read path. Like serveTamperPatterns this is a speed
// bump against accidents and naive attempts, not a sandbox: a program
// written to disk can still write the file. Copying the file elsewhere with
// cp is denied too; that is the price of matching the verb, not the
// direction.
//
// %[1]s is the target alternation (cronTargetPattern, plus the resolved
// state dir).
var cronTamperTemplates = []string{
	// > file, >> file, >| file, &> file, with optional quotes and a path prefix.
	`>\|?\s*["']?[^\s;|&<>]*%[1]s`,
	`\b(?:` + cronWriteVerbs + `)\b[^\n|;&]*%[1]s`,
	`\b(?:sed|gsed|perl)\b[^\n|;&]*\s(?:-[A-Za-z]*i|--in-place)[^\n|;&]*%[1]s`,
	`\b(?:python[0-9.]*|node|ruby|deno|bun|osascript)\b[^\n]*%[1]s`,
}

// cronStateDir is the resolved <StateDir>/cron, matched literally (absolute,
// ~- and $HOME-relative) on top of cronTargetPattern. A var so tests can pin
// it.
var cronStateDir = func() string {
	dir, err := config.StateDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "cron")
}

// checkCronTamper reports whether command writes the cron jobs file or state.
func checkCronTamper(command string) string {
	targets := []string{cronTargetPattern}
	if dir := cronStateDir(); dir != "" {
		targets = append(targets, regexp.QuoteMeta(dir)+`\b`)
		if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(dir, home+"/") {
			rel := regexp.QuoteMeta(strings.TrimPrefix(dir, home+"/"))
			targets = append(targets, `(?:~|\$HOME|\$\{HOME\})/`+rel+`\b`)
		}
	}
	// Cheap prefilter: no mention of either target, nothing to match.
	if !strings.Contains(command, "schedules.json") && !strings.Contains(command, "cron") {
		return ""
	}
	alt := "(?:" + strings.Join(targets, "|") + ")"
	for _, tmpl := range cronTamperTemplates {
		re, err := regexp.Compile(fmt.Sprintf(tmpl, alt))
		if err != nil {
			logging.Log("hook-validate-bash: bad cron tamper pattern: %v", err)
			continue
		}
		if re.MatchString(command) {
			return cronTamperReason
		}
	}
	return ""
}

// checkGlobalDenyList matches globalDenyPatterns and the serve- and
// cron-tamper rules against command with data-only heredoc bodies removed
// (see stripDataHeredocs). Quoted arguments are still matched literally.
func checkGlobalDenyList(command string) string {
	command = stripDataHeredocs(command)
	for _, deny := range globalDenyPatterns {
		if deny.re.MatchString(command) {
			return deny.reason
		}
	}
	if reason := checkServeTamper(command); reason != "" {
		return reason
	}
	return checkCronTamper(command)
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
	emitDecision(harness.Decision{Kind: harness.Deny, HookEvent: "PreToolUse", Reason: fullReason})

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
// rejected in a preview popup -- the always-on "agentctl dispatch" and
// "agentctl cron" gates or the opt-in bash_preview gate for every other
// command; gate only changes the wording of the reason shown to Claude Code. Uses the same
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
func denyBashPopup(agentName, command string, gate PreviewKind, actx denyAuditContext) {
	reason := "Command denied in preview popup"
	switch gate {
	case PreviewKindDispatch:
		reason = "Dispatch denied in preview popup"
	case PreviewKindCron:
		reason = "Cron job denied in preview popup"
	}
	logging.Log("hook-validate-bash: DENIED via popup agent=%s gate=%s cmd=%q", agentName, gate, textutil.Truncate(command, 100, "..."))
	emitDecision(harness.Decision{Kind: harness.Deny, HookEvent: "PreToolUse", Reason: reason})

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
	decision := harness.Decision{Kind: harness.Allow, HookEvent: "PreToolUse", Reason: "Approved in agentctl preview popup"}
	// Written directly to os.Stdout (not cached in a package var) -- same
	// pattern denyCommand/denyBashPopup use, and the pattern every existing
	// test capture helper (captureStdout, captureDenyEmitter, runHookCaptured
	// in hooks_test.go) already relies on: swapping the os.Stdout package
	// variable before the call is a fully sufficient seam as long as the
	// write reads os.Stdout at call time rather than caching it at package
	// init, so no extra indirection is introduced here.
	emitDecision(decision)
}
