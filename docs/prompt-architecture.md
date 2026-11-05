# Prompt Architecture for Agent Dispatch

How we structure system prompts vs user messages when launching agents, based on Anthropic's published guidance.

## Key Principles

1. **User messages are stronger for instructions.** Claude follows directives in human messages better than system prompt ones. Put the actual task in the user message.

2. **Long-form data goes in the system prompt, at the top.** Reference context (issue specs, docs, code snippets) should be above the instruction. Queries at the end improve response quality by up to 30%.

3. **System prompt is for scene-setting.** Role, persona, reference material, tool definitions, execution constraints. Not the task itself.

4. **Keep the user message short and direct.** One sentence that says exactly what to do. Not a wall of context.

## Dispatch Layout

| Layer | Content | Delivery |
|---|---|---|
| Agent definition | Role, persona, coding patterns, repo conventions | `--agent <name>` (reads `~/.claude/agents/<name>.md`) |
| System prompt file | Issue body, worktree note, context store entries, execution boilerplate | `--append-system-prompt-file <path>` (temp file, cleaned up after launch) |
| User message | Short directive: "Implement issue #N: \<title\>" | Inline CLI argument |

### System prompt file ordering

`internal/dispatch/prompt.go`'s `buildSystemPrompt` composes the file in this
order:

```
1. Frontmatter (ticket/title/agent/repo/started/mode)
2. Task content (issue body, or a long custom prompt) — the bulk of the content
3. "---" separator
4. Preamble (agentctl capabilities: context store, messaging, etc.)
5. Worktree orientation note (branch name only, if applicable)
6. Mode instructions (autonomy/behavior boilerplate for dispatch/adhoc; a
   different block per mode for discuss/triage/review/custom; empty for bare)
7. Context store entries (auto-injected ticket spec + pinned context_keys)
8. Finishing steps — verify, commit, push, open a PR (worktree-aware; only
   for the fully autonomous dispatch/adhoc modes, since triage/review forbid
   implementing and discuss/custom/bare leave "done" to the human)
```

Finishing steps are deliberately last — ordered instructions closest to the
end of the prompt are followed most reliably, and getting the agent to
actually push and open a PR (rather than stopping after a local commit) is
the step most worth reinforcing. The workflow itself (verify → commit → push
→ PR, and whether a worktree/ticket exists) lives in `internal/dispatch`,
which is shared by every agent and every repo; it never hardcodes a
language-specific build tool or a repo-specific deploy/restart step (e.g.
`go build`, `just restart`) — those belong to the agent's own `.md` file,
which is the only thing that knows the toolchain.

### User message

```
Implement issue #42: logstream: persistent JSONL reader with subscriber fan-out
```

That's it. ~80 chars. Safe for any shell argument.

## Every mode obeys the short-user-message rule

The user message is an inline CLI argument for **every** mode, not just
dispatch/adhoc — so no mode's `DefaultUserMessage` may embed the ticket body.
`ModeDiscuss` and `ModeReview` used to violate this (`IncludeTicketBody:
false` paired with a `DefaultUserMessage` that interpolated the full body),
which is the exact "shell argument too long" failure below, just scoped to
two modes instead of all of them. Both now set `IncludeTicketBody: true` and
have a short directive as their `DefaultUserMessage`, matching every other
ticket-bearing mode (dispatch, triage, custom).

## Why This Ordering

**Before (broken for large issues):**
- System prompt file: short boilerplate (~500 bytes)
- User message (inline arg): full issue body (~10KB)
- Result: shell argument too long, `claude` never launches

**After:**
- System prompt file: issue body + boilerplate (~10KB, in a file)
- User message (inline arg): one-line directive (~80 bytes)
- Result: always works, better instruction following per Anthropic's guidance

## Sources

- [Anthropic Prompting Best Practices](https://platform.claude.com/docs/en/build-with-claude/prompt-engineering/claude-prompting-best-practices) — long context placement, system vs user roles
- [Anthropic System Prompts](https://docs.anthropic.com/en/release-notes/system-prompts) — system prompt design philosophy
