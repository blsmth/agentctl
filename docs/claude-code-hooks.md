# Claude Code Hooks — Complete Reference

Reference for all Claude Code hook events and how agentctl handles each one.
This document is the single source of truth for agents working on hook integration.

## How Hooks Work

Claude Code fires hook events at key lifecycle points. Hooks are configured in
`~/.claude/settings.json` under the `hooks` key. Each hook entry specifies:

- **event**: The lifecycle event name
- **matcher**: Optional filter (tool name, notification type, etc.)
- **type**: `command` (shell), `http`, `prompt`, or `agent`
- **async**: Whether to fire-and-forget (default: false)
- **timeout**: Max execution time in seconds (default: 600)

agentctl hooks are all `type: command` — they invoke `agentctl hook-<event>` which
reads JSON from stdin and optionally writes JSON to stdout. `agentctl hooks-install`
writes the full set (see [agentctl Hook Configuration](#agentctl-hook-configuration)).
Handlers guard on `AGENTCTL=1` (`guardAgentctl` in `internal/hooks/helpers.go`) and skip their work in Claude Code sessions that agentctl did not launch.

### JSON Payload (Common Fields)

Every hook receives these fields on stdin:

```json
{
  "session_id": "uuid",
  "transcript_path": "/path/to/session.jsonl",
  "cwd": "/current/working/directory",
  "permission_mode": "default|plan|acceptEdits|auto|dontAsk|bypassPermissions",
  "hook_event_name": "PreToolUse"
}
```

Plus event-specific fields documented below.

### Response Format

Blocking hooks can return JSON on stdout:

```json
{
  "decision": "allow|deny|block",
  "reason": "string (required when blocking/denying)",
  "continue": true|false,
  "stopReason": "string (shown to user when continue=false)",
  "hookSpecificOutput": {
    "hookEventName": "string",
    "additionalContext": "string (injected into Claude's context)",
    "permissionDecision": "allow|deny|ask",
    "updatedInput": {"field": "new_value"}
  }
}
```

### Exit Codes

- **0**: Success. JSON on stdout is parsed.
- **2**: Blocking error. stderr text is fed back to Claude as a continuation prompt.
- **Other**: Non-blocking error. stderr shown in verbose mode only.

---

## All Hook Events

### Session Lifecycle

#### SessionStart
- **When**: Session begins, resumes, after /clear, after compaction
- **Matcher values**: `startup`, `resume`, `clear`, `compact`
- **Blocking**: No
- **Async**: No
- **Extra payload**: `session_id`, `cwd`
- **Special**: Can write to `$CLAUDE_ENV_FILE` to persist env vars across Bash tool calls
- **agentctl status**: ✅ IMPLEMENTED (`hook-session-start`)
  - Records `session_id` and `log_path` in the window's windowstate file (also after `/clear`)
  - Writes `AGENTCTL_WINDOW_ID`, `AGENTCTL_WINDOW_INDEX` and dispatch metadata (ticket, mode, spawner) to `$CLAUDE_ENV_FILE`
  - Emits `additionalContext` with window identity info
  - Fires the user `session_start` lifecycle hooks

#### SessionEnd
- **When**: Session terminates
- **Matcher values**: `clear`, `resume`, `logout`, `prompt_input_exit`, `bypass_permissions_disabled`, `other`
- **Blocking**: No
- **Async**: Yes (recommended)
- **Extra payload**: `reason`, `session_id`
- **agentctl status**: ✅ IMPLEMENTED (`hook-session-end`)
  - Sets windowstate `state` to `idle` as a safety net
  - Fires the user `session_end` lifecycle hooks and, if enabled for the agent, writes a work-journal entry
  - On a terminal reason (`prompt_input_exit`, `logout`, `bypass_permissions_disabled`) kills the window and, when the worktree classifies as `empty` or `landed`, reclaims it (opt out with `auto_reclaim_worktree: false`; see [dispatch.md](dispatch.md#cleanup))

---

### Tool Lifecycle

#### PreToolUse
- **When**: Before tool execution (after permission granted)
- **Matcher values**: Tool name — `Bash`, `Edit`, `Read`, `Write`, `Glob`, `Grep`, `WebFetch`, `WebSearch`, `Agent`, `mcp__*`
- **Blocking**: Yes — can deny/allow/ask
- **Async**: No
- **Extra payload**: `tool_name`, `tool_input` (full tool arguments)
- **Response**: Can return `permissionDecision: "deny"` with reason
- **agentctl status**: ✅ IMPLEMENTED (two handlers)
  - `hook-pre-tool` (matcher: `*`): Sets windowstate `state` to `working`
  - `hook-validate-bash` (matcher: `Bash`): Checks global deny list and per-agent blocked commands/patterns

#### PostToolUse
- **When**: After tool execution succeeds
- **Matcher values**: Tool name (same as PreToolUse)
- **Blocking**: No
- **Async**: No
- **Extra payload**: `tool_name`, `tool_input`, `tool_response`, `tool_use_id`
- **agentctl status**: ✅ IMPLEMENTED (`hook-post-tool`)
  - Sets windowstate `state` to `working` and refreshes `last_tool_time` (fixes the stuck-spinner/stuck-blocked bugs where `idle_prompt` doesn't fire)
  - Delivers pending mailbox messages as `additionalContext` (see [design/agent-messaging-design.md](design/agent-messaging-design.md))
  - Appends a record to the hook-sourced audit trail (`internal/audit`; see `agentctl audit`) — this is the primary audit source, since it fires for every tool, not just Bash

#### PostToolUseFailure
- **When**: After tool execution fails
- **Matcher values**: Tool name
- **Blocking**: No
- **Async**: No
- **Extra payload**: `tool_name`, `tool_input`, `error`, `is_interrupt`
- **agentctl status**: ✅ IMPLEMENTED (`hook-post-tool-failure`)
  - Records `LastError`; sets state to `idle` on user interrupt
  - Appends a `tool_failure` record (`ok: false`) to the audit trail (`internal/audit`)

---

### Notification & Control

#### Notification
- **When**: Claude Code sends a notification to the user
- **Matcher values**: `permission_prompt`, `idle_prompt`, `auth_success`, `elicitation_dialog`
- **Blocking**: No
- **Async**: Yes (recommended — alerter blocks)
- **Extra payload**: `notification_type`, `message`
- **agentctl status**: ✅ IMPLEMENTED (`hook-notify`)
  - `permission_prompt`: Sets windowstate `state` to `blocked`, fires alert
  - `idle_prompt`: Sets windowstate `state` to `idle`, fires alert
  - Ignores `auth_success` and `elicitation_dialog`

#### Stop
- **When**: Main agent finishes responding (wants to stop)
- **Blocking**: Yes — exit 2 forces continuation
- **Async**: No
- **Extra payload**: `last_assistant_message`, `stop_hook_active`
- **agentctl status**: ✅ IMPLEMENTED (`hook-check-done`)
  - In autonomous modes, checks the worktree for uncommitted changes and build errors; exit 2 with a reason forces Claude to keep working
  - Once the turn ends cleanly, marks the window `done` (see `shouldMarkDone` in `internal/hooks/control.go`)

#### StopFailure
- **When**: Turn ends due to API error (not a clean stop)
- **Matcher values**: `rate_limit`, `authentication_failed`, `billing_error`, `invalid_request`, `server_error`, `max_output_tokens`, `unknown`
- **Blocking**: No
- **Async**: No
- **Extra payload**: Error type, error message
- **agentctl status**: ✅ IMPLEMENTED (`hook-stop-failure`)
  - Sets windowstate `state` to `idle`, records the error, and alerts the user

#### UserPromptSubmit
- **When**: User submits a prompt (before any processing)
- **Blocking**: Yes
- **Async**: No
- **Extra payload**: User prompt text
- **agentctl status**: ✅ IMPLEMENTED (`hook-user-prompt`)
  - Sets windowstate `state` to `working` immediately, before the first tool fires

---

### Subagent Lifecycle

#### SubagentStart
- **When**: A subagent is spawned (Agent tool use)
- **Matcher values**: Agent type name (`Explore`, `Plan`, custom names)
- **Blocking**: No
- **Async**: Yes
- **Extra payload**: `agent_id`, `agent_type`
- **agentctl status**: ✅ IMPLEMENTED (`hook-subagent-start`)
  - Increments `subagent_count` in windowstate

#### SubagentStop
- **When**: A subagent finishes
- **Matcher values**: None
- **Blocking**: No
- **Async**: No
- **Extra payload**: `agent_id`, `agent_type`, `agent_transcript_path`, `last_assistant_message`
- **agentctl status**: ✅ IMPLEMENTED (`hook-subagent-stop`)
  - Decrements `subagent_count` in windowstate (floor 0)

---

### Context & Configuration

#### PreCompact
- **When**: Before context window compaction
- **Matcher values**: `manual`, `auto`
- **Blocking**: No
- **Async**: No
- **Extra payload**: Compaction trigger reason
- **agentctl status**: ⚪ NO-OP (`hook-pre-compact`) — registered, drains stdin and logs only

#### PostCompact
- **When**: After compaction completes
- **Matcher values**: `manual`, `auto`
- **Blocking**: No
- **Async**: No
- **Extra payload**: Compaction stats
- **agentctl status**: ✅ IMPLEMENTED (`hook-post-compact`)
  - Logs the compaction and fires an alert; the monitor recomputes ctx% on its next tick

#### InstructionsLoaded
- **When**: CLAUDE.md or rules/*.md files are loaded
- **Matcher values**: `session_start`, `nested_traversal`, `path_glob_match`, `include`, `compact`
- **Blocking**: No
- **Async**: Yes
- **Extra payload**: File paths loaded
- **agentctl status**: ⚪ NO-OP (`hook-instructions-loaded`) — registered, drains stdin and logs only

#### ConfigChange
- **When**: Settings files change on disk
- **Matcher values**: `user_settings`, `project_settings`, `local_settings`, `policy_settings`, `skills`
- **Blocking**: Yes
- **Async**: No
- **Extra payload**: Changed config type
- **agentctl status**: ⚪ NO-OP (`hook-config-change`) — registered, drains stdin and logs only

#### CwdChanged
- **When**: Agent changes working directory
- **Blocking**: No
- **Async**: Yes
- **Extra payload**: New cwd path
- **Special**: Can return `watchPaths` to register file watchers
- **agentctl status**: ⚪ NO-OP (`hook-cwd-changed`) — registered, drains stdin and logs only

#### FileChanged
- **When**: A watched file changes on disk
- **Matcher values**: Filename basename (e.g., `.envrc`, `.env`)
- **Blocking**: No
- **Async**: Yes
- **Extra payload**: Changed file path
- **agentctl status**: ⚪ NO-OP (`hook-file-changed`) — registered, drains stdin and logs only

---

### Task & Team (Agent Teams Feature)

#### TaskCreated
- **When**: Task created via TaskCreate tool
- **Blocking**: Yes
- **Async**: No
- **agentctl status**: ⚪ NO-OP (`hook-task-created`) — registered, drains stdin and logs only

#### TaskCompleted
- **When**: Task marked as completed
- **Blocking**: Yes
- **Async**: No
- **agentctl status**: ⚪ NO-OP (`hook-task-completed`) — registered, drains stdin and logs only

#### TeammateIdle
- **When**: A team agent goes idle
- **Blocking**: Yes
- **Async**: No
- **agentctl status**: ⚪ NO-OP (`hook-teammate-idle`) — registered, drains stdin and logs only

---

### Worktree Management

#### WorktreeCreate
- **When**: Git worktree is created (`--worktree`, `isolation: "worktree"`, background sessions)
- **Blocking**: Yes
- **Async**: No
- **agentctl status**: ✅ IMPLEMENTED — `internal/hooks/worktree.go`
- **Verified stdin payload** (2026-08-20, against a real failing run): `session_id`,
  `transcript_path`, `cwd`, `prompt_id`, `agent_type`, `hook_event_name`, `name` — there is
  **no** `branch` field of any spelling. `name` (e.g. `agent-a6a3ff01c3c89551d`) is the only
  field that is unique per agent; `session_id` is shared by every concurrent agent dispatched
  within one session, so it must never be used alone to derive a branch name (doing so
  silently hands every concurrent agent the same worktree — a shared session ID collided). The permissive
  `branch`/`branchName` decode is kept in case a future Claude Code version adds one, but
  neither has been observed.
- **Output contract**: only a **bare absolute path** on stdout is accepted. A
  `{"hookSpecificOutput": ...}` JSON envelope is **rejected** — the consumer treats the whole
  JSON string as a relative path, joins it to the repo root, and aborts the launch (verified
  empirically 2026-08-20; see `worktree_test.go`). This is the second time an output-format guess has
  broken this hook (the first was a Noop wiring that wrote nothing), so `internal/hooks/worktree_test.go` pins the bare
  path with a negative assertion (no `{`, `}`, `"`, or `hookSpecificOutput` in stdout).

#### WorktreeRemove
- **When**: Git worktree is removed
- **Blocking**: No
- **Async**: No
- **agentctl status**: ✅ IMPLEMENTED — `internal/hooks/worktree.go`. No structured
  output, cannot block; failures are debug-logged only and the handler always exits 0.

---

### Permission & Setup

#### PermissionRequest
- **Matcher values**: Tool name (agentctl registers `Write` and `Bash`)
- **Blocking**: Yes
- **agentctl status**: ✅ IMPLEMENTED (`hook-permission-request`) — `internal/hooks/preview.go`
  - Backs `bash_preview` and the dispatch approval popup. Every uncertain path emits no decision, so the normal Claude Code permission flow decides (it never auto-approves).

#### Setup
- **agentctl status**: ⚪ NO-OP (`hook-setup`) — registered, drains stdin and logs only

---

### MCP Elicitation

#### Elicitation
- **When**: MCP server requests user input
- **Matcher values**: MCP server name
- **Blocking**: Yes
- **Async**: No
- **agentctl status**: ⚪ NO-OP (`hook-elicitation`) — registered, drains stdin and logs only

#### ElicitationResult
- **When**: User responds to MCP elicitation
- **Matcher values**: MCP server name
- **Blocking**: Yes
- **Async**: No
- **agentctl status**: ⚪ NO-OP (`hook-elicitation-result`) — registered, drains stdin and logs only

---

## agentctl Hook Configuration

`agentctl hooks-install` writes the hooks to `.claude/settings.local.json` in the current directory; add `--global` to write `~/.claude/settings.json` instead. It is idempotent: every agentctl-owned event is rewritten to its canonical value, and events agentctl does not own are preserved.

The command prefix is bare `agentctl` when the running binary is the one on `PATH`; otherwise it is the shell-quoted absolute path of the running binary (e.g. `'/opt/my tools/agentctl'`). It refuses to write a path under a temp or go-build directory. The abridged result looks like this (the full map has an entry for every event listed above):

```json
{
  "hooks": {
    "SessionStart": [{
      "hooks": [{ "type": "command", "command": "agentctl hook-session-start" }]
    }],
    "SessionEnd": [{
      "hooks": [{ "type": "command", "command": "agentctl hook-session-end", "async": true }]
    }],
    "Notification": [{
      "matcher": "idle_prompt|permission_prompt",
      "hooks": [{ "type": "command", "command": "agentctl hook-notify", "async": true }]
    }],
    "PreToolUse": [
      { "matcher": "*",    "hooks": [{ "type": "command", "command": "agentctl hook-pre-tool" }] },
      { "matcher": "Bash", "hooks": [{ "type": "command", "command": "agentctl hook-validate-bash" }] }
    ],
    "PostToolUse": [
      { "matcher": "*", "hooks": [{ "type": "command", "command": "agentctl hook-post-tool" }] }
    ],
    "Stop": [{
      "hooks": [{ "type": "command", "command": "agentctl hook-check-done" }]
    }]
  }
}
```

The canonical event map is `BuildHooksConfig` in `internal/hooks/hooks.go`; `agentctl init`, dispatch's per-agent settings and worktree settings all render from it, so the set cannot drift between them.

### Adding New Hooks

1. Add a handler function in `internal/hooks/` (start with `guardAgentctl()`, then decode stdin with `decodeInput`).
2. Add a `case "hook-<event>"` for it in `hooks.Dispatch` (`internal/hooks/hooks.go`). `main.go` routes every `hook-*` subcommand there on its fast path, before Kong.
3. Add the event to the map in `BuildHooksConfig` (same file), with a matcher and `async` flag. Re-run `agentctl hooks-install` to pick it up.
4. Add the event to `TestHooksConfigComplete` in `internal/hooks/hooks_test.go` and test the handler.

The pattern is always: read stdin JSON → resolve window ID → update the window's windowstate file → optionally emit stdout JSON.

---

## Key Architecture Notes

- **Hooks are separate processes.** Each hook invocation is a fresh `agentctl` process — no shared memory with the monitor.
- **Windowstate files are the state bus.** Hooks write per-window JSON files at `.agentctl/windows/<windowID>.json` (`internal/windowstate`), using atomic tmp+rename writes under an flock. The monitor reads them on each tick, resolves an `AgentStatus`, and owns all visual output (window renames, `state.json`). Hooks stay fast: update a file and exit.
- **The monitor reads, hooks write.** Hooks never touch the chrome bar or window names directly.
- **Window ID resolution** goes through `resolveHookWindowID()` (`internal/hooks/resolve.go`), which delegates to `signal.ResolveWindowID`: a 3-tier fallback of `AGENTCTL_SESSION_ID` windowstate lookup → `TMUX_PANE` → `AGENTCTL_WINDOW_ID` env. `SessionEnd` instead finds its window by scanning windowstate files for the payload's `session_id`, which is immune to stale `AGENTCTL_WINDOW_ID` values.
- **Async hooks** should be used for anything that might block (alerter, network calls). The `async: true` flag tells Claude Code to fire-and-forget.

## CLAUDE_CODE_SHELL_PREFIX and the bash 3.2 heredoc trap

`CLAUDE_CODE_SHELL_PREFIX` is wired to `.agentctl/shell-prefix.sh` for every launched
agent (see `internal/hooks/shell-prefix.sh`). Claude Code passes each Bash tool call's
entire command as one string, and the prefix script runs it via `eval "$@"` so that
sourced `env.d`/`shell-hooks.d` state (env overrides, pre-exec hooks) actually applies
to the executed command — a plain subprocess exec would not see that state.

macOS's system `/bin/bash` is **3.2.57 (2007)**, whose parser mis-handles a
quoted-delimiter heredoc nested inside command substitution when the heredoc body
contains an apostrophe — e.g. `git commit -m "$(cat <<'EOF' ... EOF )"` where the
message contains a word like `don't`. The failure surfaces as `unexpected EOF while
looking for matching '`, and the reported line number refers to the **evaluated
string**, not to `shell-prefix.sh` itself — easy to misdiagnose as an agentctl bug in
`eval "$@"`.

It is not: `bash -c "<same command>"` and the same text saved to a plain script file
both fail identically, so this is a bash-version parser bug, not an `eval` artifact.
`zsh -c "<same command>"` handles it fine.

`shell-prefix.sh`'s shebang is **resolved at write time**, not trusted to `env bash` /
`PATH` order (see `internal/hooks/bash_resolve.go`). `EnsureShellPrefixScript` picks the
first of `/opt/homebrew/bin/bash`, `/usr/local/bin/bash`, whatever `bash` resolves to on
`PATH`, or `/bin/bash`, whose `--version` reports major version `>= 4`, and bakes that
absolute path in as the shebang. This matters because PATH order is not something
agentctl controls — it differs between login shells, non-interactive shells, and
tmux-launched processes, so `#!/usr/bin/env bash` is not reliable: on a machine with
`/bin` ahead of `/opt/homebrew/bin` on `PATH` (common), `env bash` silently resolves to
the broken 3.2 interpreter even with a modern bash installed. The resolution is cached
for the process lifetime (`sync.Once`) and logged once at startup — `shell-prefix: using
bash <version> at <path>` at INFO, or a WARN if it had to fall back to a bash < 4.
