# Dispatch: Ticket-to-PR Orchestration

Dispatch is the primary way to assign GitHub issues to worker agents. It fetches the issue, creates an isolated worktree, and launches the agent autonomously.

## Usage

```bash
agentctl dispatch --issue 42                        # auto-detect agent from labels
agentctl dispatch --issue 42 --agent go-engineer    # explicit agent
agentctl dispatch --issue 42 --dir ~/code/other     # explicit working directory
agentctl dispatch --issue 42 --no-worktree          # skip worktree, use shared repo
agentctl dispatch --issue 42 --repo owner/name      # explicit GitHub repo (else resolved from the working dir)
```

Or use the TUI: `ctrl+b T` opens the ticket launcher, where you can browse issues and dispatch interactively.

## What happens

1. **Fetch** — Fetches the issue title, body, and labels via `gh issue view`
2. **Infer agent** — Matches issue labels to agents (`go` → `go-engineer`, `python` → `python-engineer`, etc.) or defaults to `go-engineer`. Override with `--agent`.
3. **Create worktree** — Creates a git worktree at `.worktrees/agent-<name>-issue-<N>/` on branch `agent/<name>/issue-<N>`. The agent gets a clean, isolated copy of the repo.
4. **Build prompt** — Assembles the agent's system prompt with the issue body, worktree path, branch name, and autonomous execution instructions.
5. **Launch** — Opens a new tmux window running `claude --agent <name>` with the assembled prompt. The agent starts working immediately.
6. **Autonomous execution** — The agent reads the codebase, implements the change, writes tests, commits with `Closes #N`, pushes, and opens a PR.
7. **Completion** — Done is derived automatically by the Stop hook: once the agent's turn ends cleanly (git clean, build passes) in an autonomous mode, the hook marks the window done. The monitor updates the tab bar state from that fact. `agentctl signal done` still exists as an optional *early* signal an agent can call if it wants to flag completion before its final turn ends.

## Git worktrees

Dispatched agents work in isolated git worktrees by default — no conflicts with your working tree or other agents.

Each dispatch creates:

- A worktree at `.worktrees/agent-<name>-issue-<N>/` (gitignored)
- A branch named `agent/<agent-name>/issue-<N>`

The agent is told its worktree path and branch in the prompt, and instructed to push (not merge to main).

### Opting out

Use `--no-worktree` to skip worktree creation and use the shared repo directory instead. Useful for quick fixes where isolation isn't needed, but be aware of potential conflicts with other agents.

### Cleanup

Finished worktrees are reclaimed automatically in the common case:

- **On session end.** When a dispatched agent's session ends for good (a terminal `SessionEnd`), agentctl classifies that window's own worktree and removes it, along with its window, only if the verdict is `empty` (no work was done) or `landed` (the branch is merged). Anything dirty, unpushed, or with an open PR is left alone. `git worktree prune` always runs. Opt out per agent with `auto_reclaim_worktree: false` (see [configuration.md](configuration.md#per-agent-settings)).
- **On demand.** `agentctl cleanup` classifies every worktree under `.worktrees/` and reclaims the safe ones. Unknown directories are reported as `skip` and never deleted.

```bash
agentctl cleanup --dry-run                 # print the plan and exit
agentctl cleanup --yes                     # act without the confirmation prompt
agentctl cleanup --include-open-prs        # also reclaim worktrees whose branch has an open PR (branch is kept)
agentctl cleanup --json                    # machine-readable output
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

`agentctl dispatch --issue N` runs `dispatch` mode and `--prompt` / `--prompt-key` run `adhoc`. The other modes are reached from the **ticket launcher** (`ctrl+b T`): pick an agent and an issue, then choose a prompt mode:

| Launcher choice | Key | Mode |
|-----------------|-----|------|
| Work on ticket | `w` / `enter` | `dispatch` |
| Code review | `r` | `review` |
| Triage ticket | `t` | `triage` |
| No prompt (conversational) | `n` | `discuss` |
| Custom | `c` | `custom` |

## Context injection

The dispatched agent receives a carefully scoped context window:

- **Issue body** — the problem statement from GitHub
- **Agent system prompt** — the agent's conventions and tool preferences from `~/.claude/agents/<name>.md`
- **Project CLAUDE.md** — codebase conventions
- **Implementation spec** (if available) — from the context store, keyed to the ticket number

And nothing else. No conversation history about unrelated issues. No stale file reads. Just the signal the agent needs.

## Agent inference from labels

Dispatch maps GitHub issue labels to agent names:

| Label contains | Agent |
|----------------|-------|
| `go` | `go-engineer` |
| `python` | `python-engineer` |
| `terraform` | `terraform-engineer` |

Override with `--agent <name>` for any issue. Default when no label matches: `go-engineer`.
