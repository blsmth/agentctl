# Fleet self-improvement loop (epic)

Goal: agentctl improves its own fleet (agent definitions, shared includes, preamble text, config, permissions, settings) from real evidence, with full history and undo, through a daily Opus 5.5 cron session on the agentctl project. Brendan's rules (2026-11-23): an improvement session may change anything that improves the experience, the agents' efficiency, or security. The fleet review runs daily as an `agentctl cron` job in the agentctl session on Opus 5.5. auto_improve on Gone was removed in #287 (merged).

## PR A: hidden history store for ~/.claude/agents and ~/.claude/shared (new package `internal/agenthistory`)
- A bare git repo owned by agentctl at `<stateDir>/agent-history.git`, with work tree `claudehome.HomeDir()`, tracking only `agents/` and `shared/` (use an info/exclude allowlist or explicit pathspecs; never touch other files under ~/.claude). Run every git call with `--git-dir`/`--work-tree` and `--no-optional-locks`, through a seam so tests use a temp HOME. Never create a `.git` inside ~/.claude.
- Also track project-local agent dirs (`<repo>/.claude/agents`, gitignored) for projects agentctl knows about, as separate pathspecs or separate stores keyed by project. Keep it simple: one store per tracked root.
- `Snapshot(reason, author)` commits any drift. Call it from `reconcile.Housekeep` (rate-limited, e.g. once per 5 min with a stamp file like the title backfill), and before and after any agentctl-driven change (improve apply, revert). Commit messages say what triggered the snapshot (housekeeping "edit", improve proposal id, revert), and the author is "agentctl".
- CLI `agentctl agents history [name]` (log), `agents diff [name] [rev]`, `agents revert <name|path> [rev]` (revert creates a new snapshot, never rewrites history). Check `agentctl agents` already exists as a list command and nest the new ones as subcommands without breaking it. `revert` from an agent session goes through the approval popup, the same pattern as templatePreviewGate.
- First run initialises the store with the current contents as the baseline commit.
- Edge cases: git missing (no-op plus one warning), symlinked files inside agents/ (track the link, do not follow it out of the tree), huge or binary files (skip anything over 1 MB), concurrent housekeeping runs (flock).
- Tests need a testenv TestMain. Paths resolve lazily, never at package init.

## PR B: outcome ledger (verdicts and approvals)
- Audit currently labels dispatch and cron approval timeouts as `denied_by: "rule"`. Add `denied_by: "timeout"`, and record approved outcomes (new `tool_approved` event, or the existing type if there is one) with source (`laptop` | `phone`) and elapsed ms. Code lives in internal/hooks popup_deadline.go / popup_remote.go and internal/audit.
- Verdicts: when a session sends a SendMessage whose body matches `REVIEW PR #<N> — VERDICT: <MERGE|MERGE-WITH-FOLLOWUPS|BLOCKED|NEEDS-BRENDAN>` (PostToolUse on the SendMessage tool), append one NDJSON line to `<stateDir>/verdicts/verdicts-<YYYY-MM>.ndjson`: pr, repo, verdict, rounds if stated, reviewer window and session_id, author window, branch and session_id when resolvable (the same author lookup `dispatch --review-pr` uses), ts. Prune at 90 days in housekeeping.
- `agentctl audit verdicts [--since 30d] [--json]` lists them, with a BLOCKED-rate per author agent.

## PR C: improve evidence pack and CLI targeting
- Replace the top-3 first-message summaries in internal/improve with `improve.Evidence(agent, since, sessionIDs)`: per session, outcome (closedlog: done, PR, final state, turns, model, backend; note closedlog only goes back to 2026-11-22, so fall back to transcripts via internal/sessions), audit tool_denied and tool_failure counts with the top failing commands, cost (sessions.CostOfSummary), verdicts (PR B ledger, when present), and sessionaudit findings when #260 is merged (behind a nil-able seam so C does not depend on it). Cap the rendered pack (e.g. 30 KB) and say what was cut.
- Fleet mode: `agentctl improve --fleet --since 24h` builds the pack across every agent.
- CLI: `agentctl improve [<agent>] [--fleet] [--session <id>] [--since 7d] [--theme T] [--template name] [--model M] [--backend B] [--account A] [--dry-run]`. `--dry-run` prints the evidence and the prompt. `--model` must override the agent's frontmatter model.
- Improve windows: record windowstate Mode "improve" plus spawned_by, stop skipping them in closedlog (tag reason/kind improve), show them in the hub as a role row, let sweep close idle ones, launch unfocused unless a human started it from the popup or hub.
- Split improve spend out of `cost --by agent` (an `improve` mode/kind column, or attribute it to `improve:<agent>`).
- Collapse the now no-op Full/Quick popup choice into theme-only (#287 follow-up).

## PR D: Nous narrative pre-pass (after #260 merges; it is #249's PR2 `--narrate`)
- `agentctl audit report --narrate` through `llm.NousClient` with llmusage feature `audit`, cached per session_id under `<stateDir>/audit/narratives/`, clean sessions skipped. The fleet review reads narratives instead of raw transcripts.

## PR E: improve applies through the history store
- An improve session may edit anything in scope: agents, shared, project .claude/agents, the preamble and docs in the repo (as a normal PR), global config and settings. For non-repo files, the improve run snapshots before (PR A), edits, then snapshots after with the proposal id and rationale in the commit message. Every change is revertable with `agentctl agents revert`. Repo changes (Go, preamble, docs) go through the normal PR + code-reviewer flow.
- Config and permission edits still go through the existing guards (dispatch_approval tightening rules, config tamper), so a loosening needs a human in the popup. Those guards stay.
- Daily summary: one phone push and one desktop notification listing what changed, with the history revisions.

## PR F: daily fleet review cron
- A prompt template `fleet-improve` (global templates dir), run by a dedicated orchestrator-role agent definition (e.g. `fleet-improver`) or by brandon. The prompt: build the fleet evidence pack for the last 24h, find recurring problems (2+ sessions or 1 security issue), apply fixes per PR E, dispatch engineers for repo changes, and report.
- `agentctl cron add` in the agentctl project, daily, `--model opus` (Opus 5.5). It goes through the cron approval popup, so Brendan approves it once.

## Order
A, B and C in parallel now. D after #260. E after A and C. F after E.

## Brendan's answers (2026-11-24)
- Apply mode: apply + daily summary. The review applies fixes itself (history store, one-command undo) and sends one phone push plus desktop notification per run listing the changes. Loosening safety settings (dispatch_approval etc.) still needs the human popup via the existing guards.
- Schedule: 22:00 daily, reviewing that day (last 24h).
- Sync: this laptop only. No remote for the history store.
- Runner: a new dedicated `fleet-improver` agent (role: orchestrator, Opus 5.5), whose definition only covers improvement work. It may dispatch engineers for repo changes.
