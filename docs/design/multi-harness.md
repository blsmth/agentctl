# Multi-harness support: running agents other than Claude Code

Status: research / proposal, 2026-11-17. Nothing here is approved yet.

## Question

agentctl is built around Claude Code. What would it take to run another terminal coding-agent harness (pi, Codex CLI, opencode, …) in an agentctl window, with the same state glyphs, done detection, approval popups, cost, and restore?

## TL;DR

- **The tmux half of agentctl is already harness-neutral.** Windows, `windowstate`, `winname`, the chrome bar, hub, `status`, mailbox storage, `signal`, `reconcile`, worktrees and cleanup do not care what is running in the pane.
- **The Claude half is two things:** a hard-wired `claude …` command builder, and a hook layer that is the *only* writer of working / blocked / idle / done. Transcripts (cost, recap, restore validation) and pane scraping (prompt injection, mobile dialog answering) are secondary couplings.
- **A de facto hook standard now exists, and it is Claude Code's shape.** Codex CLI, Factory Droid, Goose (via Open Plugins) and Cursor all accept command hooks with `session_id` / `tool_name` / `tool_input` on stdin, `hookSpecificOutput.permissionDecision` on stdout, and exit 2 to block. One normalizer plus a per-harness event-name table covers most of them.
- **Recommendation:** introduce a `Harness` interface (pure refactor, Claude only), then ship **Codex CLI** first (closest hook contract, biggest user base after Claude) and **pi** second (cleanest launch interface, needs a small TS bridge extension). Defer opencode until its v2 plugin API settles. Do not use ACP for interactive windows.

## Where agentctl is coupled to Claude Code

Ranked by how much breaks without it.

| Area | Where | Claude feature used | Without it |
|---|---|---|---|
| Launch | `dispatch.ClaudeInvocation` / `BuildBaseFlags` (all four launch paths: `Launch`, `buildRestoreCmd`, `improve.launchImprovement`, `cmd.LaunchWithResume`); `tmux.buildLaunchEnv` | `--agent`, `-n`, `--session-id`, `--resume`, `--fork-session`, `--model`, `--effort`, `--permission-mode`, `--settings`, `--append-system-prompt-file`; `CLAUDE_CODE_*` env | Nothing launches |
| Hooks → state | `internal/hooks` (`BuildHooksConfig`, `Dispatch`), installed into `.claude/settings.local.json` and the `--settings` file | SessionStart (only session-ID ↔ window link), Pre/PostToolUse (working), Notification `permission_prompt` (only *blocked* source), Stop (only *done* source, plus exit-2 force-continue), SessionEnd (reclaim), StopFailure | No state machine, no done/PR auto-open, no blocked alerts |
| Approval gate | `hooks/validate.go`, `dispatch_preview.go`, `preview_gate.go` | Blocking PreToolUse with allow/deny JSON | No `blocked_commands`, no dispatch popup. Deny-list logic itself is pure Go |
| Context injection | `hooks/mailbox_deliver.go` | `additionalContext` on PostToolUse / UserPromptSubmit; `CLAUDE_ENV_FILE` | Mailbox notices not delivered (keystroke injection exists as fallback) |
| Transcripts | `internal/claude/paths.go`, `internal/sessions`, `internal/logstream`, `fleet/loginfo.go` | `~/.claude/projects/<slug>/<sid>.jsonl`, `message.usage`, `message.id` dedup, `away_summary` / `ai-title` / `pr-link` records | No tokens/cost/ctx%, recap, title, PR link, work journal, restore validation. State is unaffected |
| Pane scraping | `internal/panestate`, `fleet.ClassifyPane`, `tmux/inject.go` | Dialog text, `esc to interrupt`, spinner regex, `❯` prompt glyph | No `status --verify`, prompt injection verification, mobile dialog answering |
| Statusline | `internal/statusline` | Pushed JSON (ctx%, rate limits, verified session name) | Degrades to JSONL-derived ctx% |
| Agent defs | `internal/agent`, `--agent <name>` | Claude loads the `.md`; agentctl expands `@shared` and appends via system-prompt file | Persona must be injected entirely by agentctl |
| Messaging | `dispatch/preamble.go` | Native `SendMessage` / `ListAgents` | Workers cannot report to orchestrators |
| Misc | `reaper` (`claude` process match), `tokens` (`claude-*` billing), `toolinput.Summarize` (Claude tool names), `backends` (Anthropic-protocol env swap, not a harness abstraction) | | Cosmetic to moderate |

**Existing seams.** `tmux.LaunchAgent` already takes an opaque `shellCmd`. `signal.Apply` / `agentctl signal` is a harness-neutral state writer. `logstream.Entry` + `Subscriber` is a normalized log-entry seam. `panestate.Classify` is a single predicate that can become per-harness. User lifecycle hooks (`runUserHooks`: `session_start`, `pre_tool`, `post_tool`, `stop`, …) already define a harness-neutral event vocabulary. `windowstate.Backend` / `AGENTCTL_BACKEND` shows the pattern a `harness` field would follow.

## The landscape (checked 2026-11-17)

| Harness | Stars | License | Hooks | Transcript | Resume by ID | Custom backends | Fit |
|---|---|---|---|---|---|---|---|
| **Codex CLI** (openai/codex, Rust) | 136k | Apache-2.0 | **Command hooks, Claude-shaped** (14 events incl. PreToolUse/PermissionRequest/Stop/SessionEnd; deny via `permissionDecision` or exit 2; no `ask`) | JSONL rollouts `~/.codex/sessions/YYYY/MM/DD/`, cumulative `token_count` events | `codex resume <id>` | `[model_providers]` + `auth.command` helper; Responses API only | **Best** |
| **pi** (earendil-works/pi, TS) | 123k | MIT | In-process TS extensions (~40 events; `tool_call` can block; `pi.exec()` to shell out). No shell-hook config | JSONL tree, per-message usage **and cost** | `--session-id <id>` (also *pre-assigns* it) | `models.json`, `apiKey: "!command"` | **Strong**, needs a bridge extension |
| opencode (anomalyco/opencode, TS) | 228k | MIT | In-process TS plugins, V1→V2 migration in progress | SQLite | `-s <id>` | Any | Good, but moving target |
| Gemini CLI | 109k | Apache-2.0 | Command hooks, renamed events | JSON chats | `--resume` | Google only | Retired for individuals (→ closed Antigravity CLI); Qwen Code is the better fork |
| Goose | 57k | Apache-2.0 | Claude-format hooks (no Notification, no transcript path) | SQLite | `--session-id` | Many | Nearly free once the generic adapter exists |
| Factory Droid | closed | — | Claude-identical, incl. Notification and `ask` | JSONL | `-s <id>` | BYOK | Nearly free, small user base |
| Amp, Cursor CLI | closed | — | TS plugins / partial hook firing in CLI | Server-side / none | partial | Vendor-hosted only | Skip |
| Aider, Crush | 50k / 29k | Apache / FSL | none / "preliminary" | — | partial | Any | Skip (Aider has stalled) |

Notable gotchas found along the way:

- **Codex starts a shared background daemon** (`codex app-server --managed-daemon`, since 0.157) that inherits the environment of whichever window launched first. That would leak one window's backend/env into every other Codex window. Launch with `--no-daemon` (or `features.daemon_auto_start=false`).
- **Codex SessionEnd/Interrupt hooks time out after 1s (3s max).** agentctl already detaches heavy SessionEnd work; keep it that way.
- **Codex ignores provider config in project config**; it has to go in `$CODEX_HOME/config.toml` or `-c` flags.
- **Codex 0.167 added `codex agents`**, an "agent command center". It overlaps with agentctl's hub; worth watching, not a blocker.
- **pi has no permission system at all** (docs recommend a container). agentctl's PreToolUse gate would be pi's approval layer, which is a selling point.
- **pi in tmux needs `extended-keys on`** (plus `extended-keys-format csi-u` on tmux 3.5+) or Shift+Enter breaks. agentctl owns the session, so it can set this.
- **No harness has native inter-session messaging** like `SendMessage`/`ListAgents`. The agentctl mailbox becomes the portable substitute.

### What about ACP?

The Agent Client Protocol (Zed + JetBrains; v1 stable; ~60 agents, native in opencode, Gemini, Goose, Qwen, Droid, Cursor; adapters for Codex, Claude, pi) would in theory let one client drive every harness. **It is the wrong fit for agentctl's interactive windows:** ACP makes the client the chat UI, so the harness's own TUI disappears from the tmux pane and the human can no longer just type into the window, which is the whole point of [tmux as the runtime](tmux-as-agent-runtime.md). Hooks do not reliably fire in ACP mode and ACP carries no token usage. Revisit it only for a future `dispatch --headless` worker mode.

## Proposed design

### 1. A `Harness` interface (new package `internal/harness`)

```go
type Harness interface {
    Name() string                 // "claude", "codex", "pi"
    Capabilities() Caps           // what degrades gracefully (see below)

    // Launch
    Command(inv Invocation, ac config.AgentConfig) (shellCmd string, env map[string]string, err error)
    ResumeCommand(inv Invocation, sessionID string) (string, error)
    PrepareWorkspace(workDir string, ac config.AgentConfig) error // hooks, permissions, extension files

    // Hooks
    DecodeHook(event string, stdin []byte) (hooks.Event, error)   // payload → neutral event
    EncodeDecision(d hooks.Decision) (stdout []byte, exit int)    // allow/deny/context → harness contract

    // Transcripts
    TranscriptPath(workDir, sessionID string) string
    ParseTranscript(r io.Reader) ([]logstream.Entry, error)

    // Pane
    PaneClassifier() panestate.Classifier
}
```

`Invocation` is today's `ClaudeInvocation` minus Claude vocabulary: agent name, session name, session ID, resume/fork, model, effort, permission mode (mapped per harness), system-prompt file, first message.

`Caps` flags what a harness can and cannot do (`PreassignSessionID`, `BlockingPreTool`, `BlockedSignal`, `ContextInjection`, `NativeMessaging`, `Statusline`, `ResumeByID`). Callers check caps instead of harness names, and the UI shows a degraded indicator rather than lying (e.g. no blocked state → chrome bar never shows ⏸ for that window, and `status --verify` falls back to pane classification).

### 2. Neutral hook events

Hook handlers already do "decode payload → update windowstate". Split them:

- Harness-installed hooks call `agentctl hook-<event> --harness <name>` (Claude keeps the current argv for compatibility).
- `DecodeHook` produces one of: `SessionStarted{sid, cwd, transcript}`, `PromptSubmitted`, `ToolStarted{name, input}`, `ToolFinished`, `NeedsPermission`, `TurnEnded{lastText}`, `ApiError`, `Interrupted`, `SessionEnded{terminal bool}`.
- Existing state logic (`windowstate`, `winname`, `ClaimComplete`, mailbox, reconcile, user lifecycle hooks) runs on neutral events unchanged.
- `EncodeDecision` handles the output contract: Claude/Codex/Droid/Goose `permissionDecision` JSON or exit 2; pi's bridge reads a simple JSON reply.

For the Claude-shaped family (Claude, Codex, Droid, Goose) the decoder is one function with an event-name table. Codex mapping: `PermissionRequest` → blocked, `Interrupt` → idle, no Notification.

### 3. Identity

`windowstate` and `restore.json` gain `harness`. Agent frontmatter and `agents.<name>` config gain `harness` (default `claude`), and `dispatch --harness` overrides. `reaper`, `ClassifyBilling` and `toolinput.Summarize` key off the harness instead of `claude` string matching.

### 4. Persona and messaging for non-Claude harnesses

- No `--agent` equivalent: agentctl injects the full expanded agent body plus preamble through the harness's system-prompt mechanism (pi `--append-system-prompt <file>`; Codex `-c developer_instructions=…`). This path already carries most of the content today.
- The worker preamble must not teach `SendMessage`/`ListAgents` to harnesses without them. Instead: workers report via `agentctl signal done` plus the PR, and orchestrator → worker messages go through the mailbox, delivered by hook context injection where supported, otherwise by `tmux/inject.go` keystrokes.

## Harness sketches

### Codex CLI

- Launch: `codex --no-daemon -m <model> -a <approval> -s <sandbox> -c developer_instructions=@<file> "<first message>"`. Map `permission_mode` → `-a`/`-s`.
- Session ID is **not** pre-assignable: link window ↔ session in the SessionStart hook (as Claude does today), not at launch.
- Hooks: `agentctl hooks-install --harness codex` writes `$CODEX_HOME/hooks.json` (project `.codex/hooks.json` only applies in trusted projects).
- Approval popup: PreToolUse has no `ask`, so the popup must resolve to allow/deny inside the hook, which agentctl already does.
- Transcript: rollout JSONL, possibly `.jsonl.zst`; `token_count` is cumulative so the parser diffs consecutive totals. Rollout schema comes from third-party parsers, so build golden tests from real files first.
- Backends: `-c model_providers.<name>.base_url=… auth.command="agentctl backend-token <name>"`. Ollama works (serves `/v1/responses`); Nous `/v1/responses` support is unverified.

### pi

- Launch: `pi --session-id <uuid> -n <name> --append-system-prompt <file> --model <provider/id> -e <agentctl-pi.ts>`. Pre-assigned session ID means the transcript path is known before launch, same as Claude today.
- Ship `agentctl-pi.ts` (~200 lines, embedded in the binary and written to the runtime dir), bridging `session_start`, `input`, `tool_call` (gate: `pi.exec("agentctl", ["hook-validate-bash", "--harness", "pi"])` → `{block, reason}`), `tool_result`, `agent_settled` (not `agent_end`), and `session_shutdown` to `agentctl hook-*`.
- Mailbox delivery via `pi.sendMessage` inside the extension.
- Transcript: JSONL tree (`id`/`parentId`, v3) with usage and cost per message; still price through `internal/tokens` and treat pi's cost as a cross-check.
- Backends: `models.json` provider with `apiKey: "!agentctl backend-token <name>"`.
- Blocked state: pi has no permission prompts, so "blocked" exists only while an agentctl approval popup is open, which agentctl already marks itself.

## Rollout plan

1. **Refactor, Claude only, no behavior change.** `internal/harness` + `claude` implementation; route `BuildBaseFlags`, `buildLaunchEnv`'s `CLAUDE_CODE_*` vars, `permissionModeFromCmd`, settings writing, `claude.LogPath`, and hook decoding through it. Add `harness` to windowstate/config. This is the bulk of the work and is worth doing even if no second harness ships.
2. **Codex harness** behind `harness: codex`. Golden-file tests from real hook payloads and rollouts. Mark capabilities honestly.
3. **pi harness** with the bridge extension; `extended-keys` set on the agentctl session.
4. **Spike opencode** once its V2 plugin docs stabilize; its SSE `/event` stream could feed every session at once.
5. Later, maybe: ACP client for headless workers; Goose/Droid via the Claude-shaped decoder.

## Open questions for Brendan

- Is the goal "use a different model cheaply" (already partly served by `backends` on the Claude harness) or "use a different harness's UX/tools"? If it is the former, Codex/pi buy less than they cost.
- Should orchestrators (brandon) stay Claude-only? Native `SendMessage`/`ListAgents` is the hardest thing to replace, and limiting non-Claude harnesses to *worker* roles avoids it.
- Acceptable to ship a TypeScript file inside a Go binary for pi?

## Unverified

Codex session-name flag; the Codex rollout schema (third-party parsers) and its new SQLite metadata DB; Nous `/v1/responses`; current opencode V1/V2 event names; Gemini hook payload fields; Amp thread resume; Cursor transcript location; Crush storage format.

## Sources

Codex: [hooks](https://learn.chatgpt.com/docs/hooks), [CLI reference](https://learn.chatgpt.com/docs/cli/reference), [app-server](https://learn.chatgpt.com/docs/app-server), [rust-v0.167.0](https://github.com/openai/codex/releases/tag/rust-v0.167.0), [daemon](https://www.developersdigest.tech/blog/codex-cli-background-daemon-auto-start-disable-2026), [custom providers](https://codex.danielvaughan.com/2026/04/23/codex-cli-custom-model-providers-configuration-guide/), [statusline request #17827](https://github.com/openai/codex/issues/17827).
pi: [cli.md](https://raw.githubusercontent.com/earendil-works/pi/main/packages/coding-agent/docs/cli.md), [extensions.md](https://raw.githubusercontent.com/earendil-works/pi/main/packages/coding-agent/docs/extensions.md), [rpc.md](https://raw.githubusercontent.com/earendil-works/pi/main/packages/coding-agent/docs/rpc.md), [session-format.md](https://raw.githubusercontent.com/earendil-works/pi/main/packages/coding-agent/docs/session-format.md), [security.md](https://raw.githubusercontent.com/earendil-works/pi/main/packages/coding-agent/docs/security.md), [models-configuration.md](https://raw.githubusercontent.com/earendil-works/pi/main/packages/coding-agent/docs/models-configuration.md), [tmux.md](https://raw.githubusercontent.com/earendil-works/pi/main/packages/coding-agent/docs/tmux.md).
opencode: [server](https://opencode.ai/docs/server/), [plugins](https://opencode.ai/docs/build/plugins/server), [agents](https://opencode.ai/docs/agents/), [permissions](https://opencode.ai/docs/permissions/).
Others: [Gemini hooks](https://geminicli.com/docs/hooks/), [Antigravity transition](https://www.theregister.com/ai-ml/2026/05/20/bye-bye-gemini-cli-google-nudges-devs-toward-antigravity/5243605), [Goose hooks](https://goose-docs.ai/docs/guides/context-engineering/hooks/), [Factory hooks](https://docs.factory.com/reference/hooks-reference), [Amp plugins](https://ampcode.com/docs/customize/plugins), [Cursor hooks](https://cursor.com/docs/agent/hooks), [ACP](https://agentclientprotocol.com/protocol/overview), [ACP agents](https://agentclientprotocol.com/overview/agents), [Open Plugins](https://open-plugins.com/plugin-builders/specification).
