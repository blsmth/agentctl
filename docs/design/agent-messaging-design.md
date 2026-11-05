# Agent Messaging Design

Status: **accepted**, 2026-08-13
Supersedes: the voluntary `agentctl signal done` convention as the primary completion mechanism.

## Problem

Agents have no way to tell each other anything. The only mechanism today is
`agentctl signal done`, which has three failures:

1. **It's voluntary.** The dispatch preamble asks the agent to run it as its last
   action. An agent that forgets, crashes, or ends its turn early never sends it.
   Reliability depends on model goodwill.
2. **Nothing consumes it.** `dispatch.HandleComplete` is the only reader, and it
   drops everything that isn't a ticketed dispatch. For ad-hoc dispatch — the
   default workflow — the signal has literally no effect beyond a window rename.
   (Fixed separately as Phase 0.)
3. **It's one-way and one-bit.** There is no reply address, no payload, and no
   way to say anything other than "done".

## Key insight: the reliable signal already exists

`hooks.CheckDone` is wired to Claude Code's **Stop hook** and fires
deterministically at the end of every turn. It cannot be forgotten by the agent.
It already sets `state=idle`, and its input struct already carries
`last_assistant_message` — which we currently decode and discard.

Likewise `hooks.Notify` is wired to the **Notification hook** and already sets
`state="blocked"` on `permission_prompt`.

So both signals we care about — *done* and *blocked* — are already available from
Claude Code itself, with no agent cooperation required. `agentctl signal done`
adds exactly one bit on top of Stop: `Done=true`, a **semantic claim** ("I
consider the task finished") rather than a liveness fact.

**For a dispatched agent, turn-end is task-end.** A dispatch receives one kickoff
prompt and nobody types after it. When Stop fires, it is done. The voluntary
signal is redundant for precisely the case that matters; it only carries
information for interactive windows where a human keeps typing.

### Consequence

We **derive** done and blocked from hooks. `agentctl signal done` degrades to an
optional *early* completion signal and is never required. This is the change that
eliminates the reliability problem.

## Why not ntfy (or any pub/sub bus)

ntfy is a good fit for notifications and a poor fit for this:

- **Agents are ephemeral; pub/sub is fire-and-forget.** An agent that isn't
  running when a message publishes never receives it. Agents start and stop
  constantly, so this is the common case, not the edge case. Making it durable
  means reimplementing per-subscriber cursors on top of `since=` polling — i.e.
  building a mailbox anyway.
- **It doesn't solve the hard part.** Delivery still has to get text into a
  running Claude Code loop somehow. A bus only relocates the queue. (This
  document originally assumed that meant tmux `send-keys`; §3 below explains
  why that assumption was wrong and what actually works.)
- **It breaks the architectural thesis.** windowstate, state.json, the session
  registry, and the context store are all atomic-write JSON on local disk, read on
  a tick. Everything in agentctl is local-filesystem IPC with tmux as the runtime.
  A message bus should match that, not add a network dependency and a daemon.

**Where ntfy does belong:** egress to a phone. A `blocked` message when the
operator is away from the laptop is something `alerter` cannot deliver. That is a
`notify_cmd` target (Phase 4), not the inter-agent transport.

## Design

### Transport

At the time this was written, `tmux.InjectPromptToAgent(agentName, content)`
(`send-keys -l <content>` + `Enter` into an agent's main pane, already used in
production by `agentctl context push`) looked like the obvious delivery
primitive to reuse. **It turned out not to be** — see §3 for why, and what
replaced it (hook `additionalContext`, not tmux injection at all).
`tmux.InjectPromptToAgent` and `tmux.InjectPromptToWindow` still exist and are
still used by `agentctl context push`; they are just no longer part of the
mailbox delivery path.

### 1. Addressing: windows, not names

Agent names are ambiguous — two `go-engineer` windows are common, and
`InjectPromptToAgent` resolves to whichever matches first, arbitrarily. Messages
address a **tmux window ID** (`@36`), paired with the window's session UUID.

Add to `windowstate.WindowState`:

```go
SpawnedBy        string `json:"spawned_by,omitempty"`         // window ID of the dispatcher
SpawnedBySession string `json:"spawned_by_session,omitempty"` // dispatcher's session UUID
```

Set at dispatch time from the dispatching process's own window. This is the
**single most important missing piece**: without a reply-to address there is no
supervision tree and "chat" cannot be more than a broadcast.

The session UUID is not redundant. **tmux reuses `@N` window IDs after a window
closes**, so a bare window ID can silently resolve to an unrelated new agent.
Every address is therefore a `(windowID, sessionID)` pair, verified against the
target's current `windowstate` before delivery. This must be in the data model
from Phase 1 — it cannot be bolted on later without rewriting every stored
message.

### 2. Mailbox: durable, on disk

```
.agentctl/inbox/<windowID>/<ulid>.json        # pending
.agentctl/inbox/<windowID>/read/<ulid>.json   # delivered
```

Atomic tmp+rename, mirroring `internal/registry`. Durability is the property that
pub/sub does not give us: a message survives the target not being run yet.

```json
{
  "id":         "01J...",
  "from":       "@36",
  "to":         "@41",
  "to_session": "4b20d406-...",
  "kind":       "done|blocked|fyi|question",
  "subject":    "PR #123 opened",
  "body":       "...",
  "reply_to":   "01J...",
  "hops":       0,
  "created":    "2026-08-13T19:00:00Z"
}
```

### 3. Delivery: three paths, one per agent state

There are exactly three moments mail can reach an agent, and each maps to a
distinct agent state. Which path fires is not a choice made per-message —
it falls out of whatever the target window happens to be doing when the
mail is enqueued or later drained:

| Agent state | Delivery path | Mechanism |
|---|---|---|
| **working** | `PostToolUse` `additionalContext` | in-band hook output, next tool call |
| **idle** | `agentctl wake` | explicit send-keys, run by a human or another agent |
| *(about to prompt)* | `UserPromptSubmit` `additionalContext` | in-band hook output, next prompt |

The first and third are automatic and were the subject of the original
in-band delivery fix (below). The second — **idle** — has no hook that fires
on its own, by definition: idle means nothing is happening. `agentctl wake
<addr>` is the deliberate, explicit escape hatch for that state: it resolves
`<addr>` the same way `agentctl send` does, refuses outright if the window
is `working` (streaming output makes send-keys unsafe — see below), checks
for a live Claude process under the pane before typing anything into it,
then drains and injects the window's pending mail as a fresh prompt via
`tmux send-keys`. It is documented in `internal/cmd/wake.go`.

**Automatic monitor-driven wake was considered and rejected on purpose.**
The monitor already ticks every window every 500ms and could, in principle,
notice a window has gone idle with mail waiting and inject it itself. This
was deliberately not built, for two reasons:

1. It reintroduces exactly the state-guessing that caused the original
   tmux `send-keys` bug (see below): "is this window really idle, or is it
   about to start streaming a response the injection would land inside of"
   is precisely the kind of inference `#{pane_current_command}` got wrong
   for months. A human (or another agent) calling `agentctl wake`
   explicitly, at a moment they've chosen, does not have that race.
2. An interactive `bare`-mode window can be idle because a human is mid-
   conversation, not because the agent finished. Typing a mailbox message
   into that window's prompt out from under a human who is about to type
   their own next message is a much worse failure mode than mail sitting
   pending a little longer. The monitor has no way to distinguish "idle,
   nobody's here" from "idle, human is thinking" — a human calling
   `agentctl wake` does.

So the monitor's role stays strictly observational: it counts pending
mailbox messages per window (`mailbox.PendingCount`, cache-refreshed on a
throttled tick) and surfaces the count in the chrome bar (`✉ N`) purely so
a human knows `agentctl wake` is available and worth running — it never
drains, delivers, or injects anything itself.

#### The working/about-to-prompt paths: in-band via hooks, not tmux `send-keys`

**This subsection originally specified tmux `send-keys` delivery from the monitor.
That design shipped, and it never delivered a single message.** The monitor's
`shouldDeliver` gate required a live tmux pane whose `#{pane_current_command}`
looked like Claude (`claude`, `claude-*`, or `node`) — but every agentctl pane
reports `zsh`, because Claude runs as a *child* of the pane's shell, not as the
pane's own foreground process. `shouldDeliver` therefore returned `(false,
"pane")` for every single window, forever, and the skip was logged at
`logging.Debug` with debug off by default — so it failed completely silently.
135 messages piled up as pending over several days before this was noticed.

The actual mechanism that works, discovered while investigating that failure:
**a hook can inject text into its own running session by writing
`{"hookSpecificOutput":{"hookEventName":"<Event>","additionalContext":"..."}}`
to stdout and exiting 0.** No tmux pane inspection, no `send-keys`, no keystroke
simulation — the hook that already fires on every tool call or prompt submission
just tells Claude Code to add the text as context for that turn.

**Delivery now happens from two hooks, in-band, not from the monitor:**

- **`PostToolUse`** (`hooks.PostTool`) — the primary path. It fires on every
  tool call, so it reaches a *working* agent within one tool call of a message
  arriving, without waiting for the agent to go idle.
- **`UserPromptSubmit`** (`hooks.UserPrompt`) — covers interactive/`bare`-mode
  windows. The old design's mode gate (`dispatch.IsAutonomous`) excluded these
  entirely; hooking `UserPromptSubmit` means a human typing into an interactive
  window still gets their mail delivered as context on their next prompt.

Both call `mailbox.DrainFor(windowID, sessionID, mailbox.DefaultTTL)`
(`internal/mailbox/deliver.go`), which:

- **Verifies identity.** Compares each message's `to_session` against the
  window's live `windowstate.SessionID`. On mismatch — tmux recycles `@N`
  window IDs after a window closes — the message is **discarded** (moved to
  `read/`, never delivered): retrying on a later tick can never fix a mismatch
  caused by window-ID reuse.
- **Enforces a TTL (`DefaultTTL` = 1 hour).** A message older than the TTL is
  discarded and logged at `Warn`, not silently dropped. This is load-bearing,
  not cosmetic: the 135 messages that piled up while delivery was broken (some
  4 days old) would otherwise all land on whichever window now owns each
  window ID the moment delivery started working.
- **Coalesces** every surviving pending message for the window into one body,
  capped at 8000 characters (with an explicit `[truncated]` marker on
  overflow) so a backlog can't blow past whatever limit Claude Code applies to
  hook output strings.
- **Claims before returning.** Every delivered message is moved to `read/` as
  part of `DrainFor` itself, before the caller ever writes to stdout.
  `PostToolUse` fires on every tool call, so claiming has to happen atomically
  with the drain — otherwise the same message would be re-delivered on every
  subsequent tool call until the agent happened to stop.

There is no state gate at all now — no `idle`/`blocked`/`working` check, no
mode check, no pane-command check. `PostToolUse` and `UserPromptSubmit` are
themselves the safe boundaries: a hook only fires between turns (`PostToolUse`
after a tool result lands, `UserPromptSubmit` before a new turn starts), so
there is no "mid-stream" moment to corrupt the way raw `send-keys` injection
into a live TUI would risk.

**Formerly a known gap, now closed by `agentctl wake`:** a window that goes
fully idle with no further tool calls and no human typing (e.g. a dispatched
agent that already called Stop and is just sitting there) never fires either
hook again, so mail queued for it would otherwise sit pending until
something else happened to wake the agent up. See the top of §3 for why the
fix is an explicit `agentctl wake <addr>` command rather than a Stop-hook
backstop or monitor-driven auto-injection — both were considered and
rejected for reintroducing state-guessing.

A **Stop-hook backstop** (having `hooks.CheckDone` itself drain and inject
mail before the turn ends) remains a distinct, separate follow-up — see
`internal/hooks/control.go`'s note on `stop_hook_active`. It is out of scope
here: `agentctl wake` addresses the "already idle, nothing will ever fire
again" case; a Stop-hook backstop would instead address "about to go idle,
catch it in the same turn," which has its own hook-interaction subtleties
that deserve their own design pass.

### 4. Auto-emit from hooks

- **Stop hook** (`hooks.CheckDone`): when the window's mode is dispatch-like and
  `SpawnedBy` is set, enqueue a `done` message to the parent, using the already-
  decoded `last_assistant_message` as the body.
- **Notification hook** (`hooks.Notify`): on `permission_prompt`, enqueue a
  `blocked` message to the parent.

Neither requires the agent to do anything.

### 5. CLI

```
agentctl send <addr> "message"   # enqueue, fire-and-forget
agentctl inbox                   # list my pending/read messages
agentctl wake <addr>             # explicit idle-path delivery via send-keys (see §3)
```

`<addr>` accepts a window ID (`@41`), or an agent name resolved to a window when
unambiguous (erroring when not) — all three commands resolve it the same way.

## Explicitly deferred: `agentctl ask`

Blocking request/reply across two independently-scheduled LLM loops is where most
agent-messaging designs fail: the asker must block a turn while the answerer is
scheduled independently, with no shared clock and no guarantee the answerer is
ever run. Fire-and-forget plus wake-on-idle covers the real use cases (completion
notification, blocked escalation, delegation handoff) without that machinery.
Revisit only if a concrete need appears.

## Phasing

| Phase | Scope | Status |
|---|---|---|
| **0** | Fix `HandleComplete` to handle ad-hoc dispatch (3 bugs: mode check, ticket check, Branch/WorkDir derivation) | dispatched |
| **1** | `SpawnedBy` + `SpawnedBySession` on `WindowState`, recorded at dispatch. Pure plumbing, no behavior change. | done |
| **2** | `internal/mailbox` + `agentctl send` / `agentctl inbox` | done |
| **2b** | tmux `send-keys` monitor drain — **shipped, never delivered a single message** (see §3), removed | removed |
| **2c** | In-band delivery via `PostToolUse` / `UserPromptSubmit` hooks (see §3) | done |
| **3** | Auto-emit `done`/`blocked` from Stop and Notification hooks | done |
| **4** | Optional: ntfy as phone egress for `blocked` | |
| **5** | Explicit `agentctl wake <addr>` for fully-idle windows, plus a chrome-bar unread indicator so it's discoverable (see §3) | done |
| **6** | Stop-hook backstop that catches a window right before it goes idle (distinct from Phase 5 — see §3) | pending follow-up |

Phase 2c is what actually solves the stated problem. Phase 3's auto-emitted
messages had nowhere to go until 2c landed — they enqueued correctly but sat
undelivered exactly like everything else pending mailbox delivery. Phase 5
closes the remaining gap for windows that go idle before either hook ever
fires again.

## Risks

- **Reply loops.** A acks B's done, B acks A's ack, forever. Mitigation: `hops`
  counter with a hard ceiling, and auto-emitted messages must never auto-reply.
- **Context burn.** Every injected message costs the receiver a full turn.
  Mitigation: coalescing (above); revisit if it proves noisy in practice.
- **Window ID reuse.** Addressed by the `(windowID, sessionID)` pairing above,
  now enforced in `mailbox.DrainFor`. Called out separately because it is easy
  to drop during implementation and fails silently and confusingly when it
  does.
- **Stale backlog on first real delivery.** Addressed by `mailbox.DefaultTTL`
  (1 hour): a message older than the TTL is discarded and logged at `Warn`
  rather than delivered to whatever window now owns that ID. This was not a
  theoretical risk — it is exactly what happened when delivery went from
  "silently broken" to "working" (see §3).
- **A fully-idle window never gets its mail on its own.** Closed by
  `agentctl wake` (§3, Phase 5) rather than automatic monitor-driven
  injection: the monitor deliberately never drains or delivers mail itself,
  only counts it, so waking an idle window is always a deliberate human (or
  agent) action, never a background guess about whether a window is
  "really" idle. A Stop-hook backstop for the moment right before a window
  goes idle remains a distinct, still-deferred follow-up (Phase 6).
- **Stale pending mail on a closed or recycled window.** `mailbox.DrainFor`'s
  TTL expiry only runs when a hook fires for a still-*live* window, so a
  window that closes (or whose "@N" ID tmux later recycles onto an unrelated
  session) leaves its pending mail on disk indefinitely — and, because older
  messages may carry an empty `ToSession`, a recycled ID could otherwise
  deliver stale mail to a completely unrelated agent. `mailbox.PruneStale`,
  run from the monitor on a ~5-minute throttled tick, is the hard backstop:
  it deletes pending messages older than `mailbox.MaxPendingAge` (24h) —
  safe by construction, since anything that old already exceeds
  `DefaultTTL` and was never getting delivered anyway.
