# Dispatch: Ticket-to-PR Orchestration

Dispatch is the primary way to assign GitHub issues to worker agents. It fetches the issue, creates an isolated worktree, and launches the agent autonomously.

## Usage

```bash
agentctl dispatch --issue 42                        # auto-detect agent from labels
agentctl dispatch --issue 42 --agent go-engineer    # explicit agent
agentctl dispatch --issue 42 --dir ~/code/other     # explicit working directory
agentctl dispatch --issue 42 --no-worktree          # skip worktree, use shared repo
agentctl dispatch --issue 42 --repo owner/name      # explicit GitHub repo (else resolved from the working dir)
agentctl dispatch --agent go-engineer --prompt '...' # ad-hoc prompt (or --prompt-key <key>)
agentctl dispatch --review-pr 57                    # review an open PR (code-reviewer by default)
agentctl dispatch --issue 42 --model opus           # model override (sonnet, opus, haiku, or a full id)
agentctl dispatch --issue 42 --backend fenrir       # run against a configured backend ('none' forces Anthropic)
agentctl dispatch --issue 42 --account personal     # launch under a configured Claude account
agentctl dispatch --issue 42 --harness pi           # run the agent on pi instead of Claude Code
agentctl dispatch --issue 42 --wait --timeout 1h    # block until the agent completes
```

`--issue`, `--prompt` and `--prompt-key` are mutually exclusive, and `--agent` is required with `--prompt`/`--prompt-key`. Write a long prompt to the context store first (`agentctl context set <key> ...` in its own Bash call) and pass `--prompt-key <key>`, so the approval popup can show it. See [Backends](configuration.md#backends-ollama-and-nous-portal), [Accounts](configuration.md#accounts-multiple-claude-logins) and [Harnesses](configuration.md#harnesses-claude-code-and-pi).

When an agent runs `agentctl dispatch` through Bash, the approval popup (and the phone, with `agentctl serve` running) asks a human first, per [`dispatch_approval`](configuration.md#dispatch_approval) (default: everything except `code-reviewer` PR reviews).

Or use the TUI: `ctrl+b T` opens the ticket launcher, where you can browse issues and dispatch interactively.

## What happens

1. **Fetch** — Fetches the issue title, body, and labels via `gh issue view`
2. **Infer agent** — Matches issue labels to agents (`go` → `go-engineer`, `python` → `python-engineer`, etc.) or defaults to `go-engineer`. Override with `--agent`.
3. **Create worktree** — Creates a git worktree at `.worktrees/agent-<name>-issue-<N>/` on branch `agent/<name>/issue-<N>`. The agent gets a clean, isolated copy of the repo.
4. **Build prompt** — Builds the stable system prompt file (preamble, shared conventions, mode rules, the agent's context entries) and the first user message (the issue body, worktree path, branch, orchestrator location and finishing steps); see [prompt-architecture.md](prompt-architecture.md).
5. **Launch** — Opens a new tmux window running `claude --agent <name>` (or the agent's [harness](configuration.md#harnesses-claude-code-and-pi)) with the assembled prompt, under the resolved account and backend. Worker dispatches use `--permission-mode auto` unless the agent's `permission_mode` says otherwise. The agent starts working immediately.
6. **Autonomous execution** — The agent reads the codebase, implements the change, writes tests, commits with `Closes #N`, pushes, and opens a PR.
7. **Completion** — Done is derived automatically by the Stop hook: once the agent's turn ends cleanly (git clean, build passes) in an autonomous mode, the hook marks the window done (the window name gets a ✓) and spawns a detached `agentctl dispatch-complete`, which finds the PR, swaps labels, opens it in the browser ([`open_pr_in_browser`](configuration.md#top-level-keys)) and runs `dispatch_complete` hooks. The dispatching window gets an `agentctl notice:` line. `agentctl signal done` still exists as an optional *early* signal an agent can call if it wants to flag completion before its final turn ends.

## Git worktrees

Dispatched agents work in isolated git worktrees by default — no conflicts with your working tree or other agents.

Each dispatch creates:

- A worktree at `.worktrees/agent-<name>-issue-<N>/` (gitignored)
- A branch named `agent/<agent-name>/issue-<N>`

The agent is told its worktree path and branch in the prompt, and instructed to push (not merge to main).

### Opting out

Use `--no-worktree` to skip worktree creation and use the shared repo directory instead. Useful for quick fixes where isolation isn't needed, but be aware of potential conflicts with other agents. The launch facts say what that directory is: the repo's main checkout, an existing linked worktree (named by its branch, when dispatching from a session that runs in one) that other sessions may also use, or a directory outside git.

### Cleanup

Finished worktrees are reclaimed automatically in the common case:

- **On session end.** When a dispatched agent's session ends for good (a terminal `SessionEnd`), agentctl classifies that window's own worktree and removes it, along with its window, only if the verdict is `empty` (no work was done) or `landed` (the branch is merged). Anything dirty, unpushed, or with an open PR is left alone. `git worktree prune` always runs. Opt out per agent with `auto_reclaim_worktree: false` (see [configuration.md](configuration.md#per-agent-settings)).
- **On demand.** `agentctl cleanup` classifies every worktree under `.worktrees/` and reclaims the safe ones. Unknown directories are reported as `skip` and never deleted.

```bash
agentctl cleanup --dry-run                 # print the plan and exit
agentctl cleanup --yes                     # act without the confirmation prompt
agentctl cleanup --include-open-prs        # also reclaim worktrees whose branch has an open PR (branch is kept)
agentctl cleanup --json                    # machine-readable output
agentctl sweep --dry-run                   # window-centric, current session only
```

To clean up by *window* instead of by worktree, run `agentctl sweep` from inside tmux. It only touches the current session, closes finished windows (with or without a worktree), and prints a table of what needs you. Flags: `--dry-run`, `--include-bare` (also close finished bare/interactive windows: idle after at least one completed turn, no pending mail, no live child sessions it dispatched (in any state, since closing it would close them), then the usual worktree/branch/open-PR safety; without it bare windows are only listed), `--session <name>` (required outside tmux; a session that does not exist is an error), `--json`. The hub (`w`, or a group header's `sweep` button) and the phone (a group header's 🧹, with a "Full sweep" toggle that adds `--include-bare`; the hub never does) run the same sweep for one project with a preview and a confirm; see [keybindings](keybindings.md) and the [mobile web app design](design/mobile-web-app.md).

```sh
git worktree list                          # see all worktrees
```

If worktree creation fails (e.g. the branch already exists), dispatch falls back to the shared repo directory automatically.

## Dispatch modes

A dispatch is launched in one of several modes (`internal/dispatch/modes.go`). Each mode picks the system-prompt instructions, whether the agent runs autonomously, and a default model.

| Mode | What it does | Autonomous | Default model |
|------|--------------|------------|---------------|
| `dispatch` | Implement the ticket end to end: code, tests, commit, push, open a PR. | yes | sonnet |
| `adhoc` | Same finishing steps, but the task is a free-form prompt instead of a ticket (`--prompt` / `--prompt-key`). | yes | agent config |
| `triage` | Do **not** implement. Reads the referenced code, fleshes out the issue (context, deliverables, files to change, tests, acceptance criteria), updates the issue body with `gh issue edit`, and applies existing labels. | yes | sonnet |
| `review` | Do **not** implement. Reviews related PRs or commits for correctness, edge cases, style, test coverage and security, and reports what must change. | no (interactive) | opus |
| `discuss` | Do **not** implement yet. Acts as a technical collaborator to scope and refine the ticket; implements only once you say to proceed. | no (interactive) | sonnet |
| `custom` | Uses your own prompt as the system instructions, optionally with a ticket attached. | no (interactive) | agent config |
| `review-pr` | Review one open PR (`--review-pr N`) and send a verdict to the orchestrator. Never commits, pushes, opens a PR or writes to GitHub. | yes | agent config |

`agentctl dispatch --issue N` runs `dispatch` mode, `--prompt` / `--prompt-key` run `adhoc`, and `--review-pr N` runs `review-pr`. The other modes are reached from the **ticket launcher** (`ctrl+b T`): pick an agent and an issue, then choose a prompt mode:

| Launcher choice | Key | Mode |
|-----------------|-----|------|
| Work on ticket | `w` / `enter` | `dispatch` |
| Code review | `r` | `review` |
| Triage ticket | `t` | `triage` |
| No prompt (conversational) | `n` | `discuss` |
| Custom | `c` | `custom` |

## Orchestrator agents

An agent with `role: orchestrator` in its frontmatter (see [configuration.md](configuration.md#agent-frontmatter-role)) coordinates others, so it never gets the worker tail: no finishing steps (commit, push, open a PR) and no "do not merge to main" worktree line, and its preamble is the orchestrator one.

- **Human-initiated `--prompt`** (the phone's new-session button, or the CLI from a shell with no tracked Claude session): launched as a `bare` interactive session. The prompt is the first user message, there are no autonomous instructions, and the permission mode resolves like a worker dispatch (per-launch override, then the agent's `permission_mode`, then `auto`).
- **Unattended** (a cron job, or `--prompt` dispatched from another agent session, so done notices and `--wait` work) **and `--issue`**: the mode is kept, with its autonomous instructions, and the task is in the first message.

## Reporter agents

An agent with `role: reporter` in its frontmatter (see [configuration.md](configuration.md#agent-frontmatter-role)) never changes code: code-reviewer, researcher, designer. It is a worker in every other respect (worker preamble, autonomous instructions, worktree), but in ad-hoc and `--issue` launches the finishing steps are replaced by a short report step: make no commits and open no PR unless the task asks for one, then send the result to the orchestrator with SendMessage, or put it in the final message when there is no orchestrator. Its orchestrator section has no "After you open the PR, stay responsive until it merges" paragraph, and an `--issue` launch's directive is "Work on issue #N and report the result." instead of "open a PR when done". `--review-pr` launches are unchanged, and a `bare` launch keeps the orchestrator preamble (a role never downgrades it).

## Choosing a model

Every orchestrator agentctl launches gets a "Models" section in its preamble with this guidance:

- Pick the cheapest model that can do the job. Plan usage is shared across every session on an account, and long Opus sessions use it up fastest.
- Sonnet is the default for implementation, tests, docs, refactors and reviews.
- Opus (`--model opus`) only for genuinely hard work (concurrency, hooks, auth, security-relevant code, design) and reviews of those areas.
- `agentctl account list` shows 5H/7D plan usage; `agentctl cost report --since 5h --sort cost --top 10` shows what used the most.

When the global config defines [backends](configuration.md#backends-ollama-and-nous-portal), the section also gets one line per backend, built from config at launch (no network calls, no credential checks). A Nous Portal backend is billed per token in real money outside the Claude plan, so the orchestrator is told to use it only for a second opinion from a non-Anthropic model (for example a second review of a PR that already has a Claude review), and not for primary work or as a fallback when plan usage is high. An Ollama backend is described as free and local, for small mechanical tasks. With no backends there are no backend lines, and harnesses that cannot use backends never get them. Workers do not get this section.

## PR review (`--review-pr`)

```bash
agentctl dispatch --review-pr 57                          # author detected from the PR branch
agentctl dispatch --review-pr 57 --author @12             # name the author window
agentctl dispatch --review-pr 57 --model sonnet --prompt 'Focus on the tmux calls.'
```

- Fetches the PR with `gh pr view` (title, URL, base/head branch, head sha, changed files, body). A closed or merged PR is an error. Exclusive with `--issue`; `--prompt` / `--prompt-key` add text under "Additional review instructions".
- The worktree is `.worktrees/agent-<agent>-review-pr-<N>-<id>/` on `agent/<agent>/review-pr-<N>-<id>` (a fresh id per review, so two reviews never share a worktree), cut from the default branch. The reviewer checks out the PR head detached and returns to that branch when done, so SessionEnd can reclaim it. It is never pushed.
- **Author**: the live window whose worktree branch equals the PR's head branch, across every agentctl session (`--author @N` overrides). When it runs under the same Claude account, the prompt names it by `@N.%P` and agentctl queues an `agentctl notice:` in the author's window introducing the reviewer. Otherwise the reviewer reports to its orchestrator only.
- Under [`pr_review`](configuration.md#pr_review) `optional` or `off`, orchestrators are told they may skip, or should not dispatch, reviewers; `--review-pr` itself works under every mode.
- With the default `dispatch_approval` (`non-review`) a review dispatch run by `code-reviewer` (no `--agent`, or `--agent code-reviewer`) does not need approval in the popup; a review by any other agent is gated. Set `dispatch_approval: "all"` to gate every review too (see [configuration](configuration.md#dispatch_approval)).
- The deliverable is one `REVIEW PR #<N> — VERDICT: MERGE | MERGE-WITH-FOLLOWUPS | BLOCKED | NEEDS-BRENDAN` message to the orchestrator, after at most 3 fix rounds with the author.

## Context injection

The dispatched agent receives a carefully scoped context window:

- **Issue body** — the problem statement from GitHub
- **Agent system prompt** — the agent's conventions and tool preferences from `~/.claude/agents/<name>.md`
- **Project CLAUDE.md** — codebase conventions
- **Context entries** — every context store entry stored with `--agent <name>`, within a size budget, each cut at 8KB with a pointer to `agentctl context get <key>`. A `ticket-<N>-spec` entry is the exception: it is injected only into the `--issue N` launch for that same N (or when the key is in the agent's `context_keys`), so a stale spec does not ride along on unrelated launches

And nothing else. No conversation history about unrelated issues. No stale file reads. Just the signal the agent needs.

## Agent inference from labels

Dispatch maps GitHub issue labels to agent names. A label that is itself one of the agent names below (`python-engineer`) wins; otherwise the short label applies. Labels match exactly, not as substrings:

| Label | Agent |
|----------------|-------|
| `go` | `go-engineer` |
| `python` | `python-engineer` |
| `terraform` | `terraform-engineer` |

Override with `--agent <name>` for any issue. Default when no label matches: `go-engineer`.
