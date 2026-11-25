# Prompt Architecture for Agent Dispatch

How a launch splits what it tells the agent between the system prompt file and the first user message, and why.

## The rule: the system file is the same every launch

For the same agent, mode, repo, harness, account and config, the file agentctl appends with `--append-system-prompt-file` is byte-identical across launches. Everything that differs per launch (the task, the worktree, the window, the orchestrator, the start time) goes in the first user message. `TestSystemFileStableAcrossLaunches` in `internal/dispatch/prompt_split_test.go` pins this.

### Why: the prompt cache

Claude Code sends each request in this order:

1. tools
2. system prompt: Claude Code's base prompt, the agent body (`--agent`), then our appended file
3. messages: CLAUDE.md and memory as reminders, then the first user message

The prompt cache reuses the longest unchanged prefix. Before this layout the appended file started with a frontmatter block carrying a `started:` timestamp and held the issue body, spec, worktree path, branch and the orchestrator's `@N.%P`, so every launch changed the system block. Everything after it, including a repo CLAUDE.md of about 14K tokens, was written to the cache fresh (at 1.25x the input price) on every launch. Over about 1,000 launches, first-turn context was 23-60K tokens but only 0-13K was read from the cache.

With the per-launch content moved into the first message, only the first message (and whatever follows it) is new per launch; tools, the system prompt and the CLAUDE.md reminders are read from the cache.

### Why the task still works as a user message

Claude follows instructions in user messages more reliably than in the system prompt, and long reference material placed above the question improves answers. The first message keeps that shape: the task material first, then the launch facts, then the directive and finishing steps last.

## Dispatch layout

| Layer | Content | Delivery |
|---|---|---|
| Agent definition | Role, persona, coding patterns, repo conventions | `--agent <name>` (reads `~/.claude/agents/<name>.md`; on pi, inlined into the system prompt file) |
| System prompt file | Stable rules: preamble, shared conventions, mode instructions, worktree note, session block, the agent's context entries | `--append-system-prompt-file <path>` (on pi `--append-system-prompt`), a file under `<project>/.agentctl/launch/`, pruned after 7 days by `reconcile --housekeeping` |
| First user message | The task, launch facts, directive and finishing steps | One argv word of the launch script's `claude` (or `pi -- <msg>`) command, single-quoted with `shellquote.Quote` |

`buildLaunchPrompt` in `internal/dispatch/prompt.go` builds both.

### System prompt file, most stable first

```
1. Preamble (agentctl runtime: context store, messaging, identity, waiting;
   worker, orchestrator or reviewer flavour; the review lines follow
   `pr_review`; orchestrators also get a "Models" section built from the
   global config, see dispatch.md#choosing-a-model, and a "Schedule" section
   on `agentctl cron`; workers get one line saying never to create jobs). A worker with an
   orchestrator gets the "Your orchestrator" section, which says how to
   find and treat it but not where it is: the location is a launch fact.
   Harnesses without native messaging (pi) get a mailbox-reporting section
   instead, and a worker on a different Claude account than its dispatcher
   gets a cross-account section
1b. Agent definition body (pi only: it has no --agent flag)
2. Shared Conventions (the agent's @shared/*.md includes, expanded, since
   Claude Code does not expand them inside agent files)
3. Mode instructions (autonomy boilerplate for dispatch/adhoc; a block per
   mode for discuss/triage/review; the review rules and verdict format for
   review-pr, with the PR as "N"; empty for bare and custom)
4. Worktree note: that the launch has a worktree, not its path or branch
   (finishing modes: "Do not merge to main"; the others get the longer
   orientation). None for review-pr, whose checkout steps are launch facts
5. Session (agent, repo, mode; no timestamp)
6. Context store entries for the agent and pinned `context_keys`, each cut
   at 8KB; never the ticket's `ticket-<N>-spec` or the `--prompt-key` entry
```

The context entries are last because they change whenever an agent writes to the store.

### First user message

```
1. Task
   - the issue (`## Issue #N: title` and body) for ticket modes, then its
     `ticket-<N>-spec` context entry
   - `## Task` with the ad-hoc prompt (and its `Source: context key` /
     `Source: template` line), or `## Instructions` for custom
   - review-pr: the PR block (metadata, diffstat, CI, changed files,
     description), the author section with its `@N.%P`, and the additional
     review instructions
   - `## Additional instructions from the user` (the phone form's extra text)
2. ## Launch facts: started time, window name, working directory, branch
   (or "No worktree"), the orchestrator's window and pane (and its session
   name as a hint). A cron fire (`ScheduledBy` set) adds a line saying it is
   a scheduled run of that job, nobody is watching, do not wait on a human.
   For review-pr, the commands to check out the PR head
   detached and return the worktree to its base
3. The mode's directive ("Implement issue #42 (title) and open a PR when
   done.", "Review PR #N ... REVIEW PR #N — VERDICT: ..."), then the
   finishing steps for dispatch/adhoc: verify, commit, push, open a PR,
   report to the orchestrator
```

Finishing steps are deliberately the last thing the agent reads: ordered instructions closest to the end of the prompt are followed most reliably, and getting the agent to push and open a PR (rather than stopping after a local commit) is the step most worth reinforcing. They run only for the fully autonomous dispatch/adhoc modes (triage/review/review-pr forbid committing; discuss/custom/bare leave "done" to the human) and never for a `role: orchestrator` agent. The workflow lives in `internal/dispatch` and never hardcodes a language-specific build tool or a repo-specific deploy step; those belong to the agent's own `.md`.

The user's additional instructions used to be the very last section of the system prompt. They are per launch, so they now sit at the end of the task block, above the launch facts and finishing steps.

### Size cap

The first message reaches `claude` as one argv word, so it is bound by the per-argument limit (128KB on Linux, `MAX_ARG_STRLEN`) and the total argv plus environment limit (1MB on macOS, `ARG_MAX`). A message over `maxInlineMessage` (64KB) keeps its launch facts, directive and finishing steps, and its task block moves to the end of the system prompt file under `## Task material (too large for the first message)`, which the message points to. That launch writes the cache from the task onwards, but it launches; the stable part of the file is unchanged in front of it.

## Bare and orchestrator launches

A bare launch's first message is the human's own text, unchanged: the phone's new-session button, `dispatch --prompt` from a plain shell for a `role: orchestrator` agent, or a menu launch with no prompt (no message at all). Bare launches have no orchestrator and no finishing steps, so their only per-launch fact is the worktree, when there is one (a human ad-hoc launch of an orchestrator keeps the ad-hoc worktree default). It goes in a one-line prefix, `[agentctl launch facts: git worktree <path>, branch <branch>]`, above the human text. A bare launch with a worktree and no prompt has no message to carry it, so its path and branch stay in the system file. A bare prompt over 64KB moves to `## Task` in the system file, as before.

The preamble role is the mode's, upgraded to orchestrator when the agent's frontmatter says `role: orchestrator` (never downgraded). Such an agent never gets finishing steps or the "do not merge to main" line (`dispatch.applyAgentRole`). Cron launches, ad-hoc dispatches from another agent session, and `--issue` keep their mode and autonomous instructions.

## Paths that do not build a prompt

- `dispatch.Restore` and `cmd.LaunchWithResume` resume or recreate a session and pass neither a system prompt file nor a first message, so the task is never sent twice.
- The approval popup, `--prompt-key` resolution and `dispatchplan.Resolve` show the prompt from the dispatch arguments (or the context store), not the built message. Windowstate records the ad-hoc prompt (`prompt`, `task`) at launch, so the hub and phone show the real task, never the launch boilerplate.
- `agentctl improve` builds its own prompt file and does not go through `buildLaunchPrompt`.

## Sources

- [Prompt caching](https://docs.anthropic.com/en/docs/build-with-claude/prompt-caching): prefix matching and cache write pricing
- [Anthropic Prompting Best Practices](https://platform.claude.com/docs/en/build-with-claude/prompt-engineering/claude-prompting-best-practices): long context placement, system vs user roles
