# The agentctl agent team

agentctl is built by Claude Code sessions that agentctl itself dispatches. Each one runs in its own window and worktree, does one job, and ends with a PR or a report. Repo-wide rules are in [CLAUDE.md](../CLAUDE.md); each role's full instructions are in its agent definition. This page says who does what, in what order, and how to dispatch them.

## Roster

| Agent | Scope | Does | Changes code? | Ends with |
|---|---|---|---|---|
| `brandon` | project (`.claude/agents/`) | Lead: triage, specs, dispatch, merge on verdict, install. Never writes code or reads diffs | no | merged PR + `just install` |
| `go-engineer` | global | All Go, `web/` JS and tests. `--model opus` for hooks, dispatch, tmux, serve/auth, concurrency | yes | PR |
| `code-reviewer` | global | Reviews every PR, works fixes out directly with the author | no | `REVIEW PR #N — VERDICT: ...` |
| `ux-designer` | global | UI check for any hub, chrome bar, popup, HUD or phone change, from demo screenshots | no | prioritized change list |
| `qa` | project (`.claude/agents/`) | After merge + install, shows the change works for real (CLI, demo harness, phone shots) | no | `QA PR #N — WORKS / PARTIAL / BROKEN` |
| `tmux-man` | global | tmux debugging | rarely | diagnosis |
| `researcher` | global | Prior art, internals, evidence for a decision | no | report in `/tmp` |
| `artifact-engineer` | global | Reports, dashboards, mockups for Brendan | no | artifact URL |
| `fleet-improver` | global, cron `fleet-improve` nightly | Fixes recurring problems in agent definitions, config and agentctl | sometimes | PRs + run record |

`.claude/` and `.agentctl/` are gitignored here, so the project agents (`.claude/agents/`) and the project templates listed below (`.agentctl/templates/`) live only in the main checkout (`~/code/personal/agentctl`); `team-bootstrap` is a global template in `~/.config/agentctl/templates/`. None of them ship with the repo.

## Pipeline

Every change runs in this order. The lead does not skip steps; the only shortcut is the lead's own true one-liners.

1. **Triage** (`brandon`). Ticket only for multi-PR work Brendan must review first (spec in the context store as `ticket-<N>-spec`); everything else is an ad-hoc dispatch.
2. **Build** (`go-engineer`). The prompt names files, behavior, edge cases and tests. Engineers never write `docs/specs/`; the lead archives specs.
3. **Review** (`code-reviewer`). Every PR. The reviewer applies the load-bearing rules in CLAUDE.md (detach/`os.Executable` stubs, `testenv` `TestMain`, `assets.go`, gated-command guards) and runs `just web-test` when `web/` or JS fixtures change.
4. **UI check** (`ux-designer`). Only when the PR changes something a person sees in the hub, chrome bar, popups, HUD, statusline or phone. Feeds back through the reviewer before the verdict.
5. **Merge** (`brandon`). On `MERGE` / `MERGE-WITH-FOLLOWUPS` and green `gh pr checks` (read them; workers claim green early). Follow-ups go to the lead's notes or a new dispatch.
6. **Install** (`brandon`). `git pull origin main` in the main checkout, then `just install`. Never `just upgrade` / `just restart` without asking Brendan.
7. **QA** (`qa`). For any user-visible change. `BROKEN` → a fix dispatch to the original engineer; live-only checks it could not do go to Brendan as one-minute steps.

Human gates: Brendan approves every non-review dispatch in the popup, reviews plans for ticketed work, and answers `NEEDS-BRENDAN` verdicts.

## Dispatch lines

Run from the repo. Long prompts: `agentctl context set <key> '...'` in its own call, then `--prompt-key <key>`.

| Work | Command |
|---|---|
| Go feature or fix | `agentctl dispatch --agent go-engineer --prompt-key <key>` (add `--model opus` for hard Go) |
| Ticketed work | `agentctl dispatch --issue <N>` |
| Review a PR | `agentctl dispatch --review-pr <N> --agent code-reviewer` (`--model opus` for hooks/auth/concurrency) |
| UI check of a PR | `agentctl dispatch --agent ux-designer --prompt 'Review the UI change in PR #<N> from demo screenshots (just demo-shot / demo-phone-shot). Send the list to the reviewer in @<R>.'` |
| QA after install | `agentctl dispatch --agent qa --prompt 'QA PR #<N>: <what should now work>.'` |
| Research | `agentctl dispatch --agent researcher --no-worktree --prompt-key <key>` |
| Report or mockup | `agentctl dispatch --agent artifact-engineer --prompt-key <key>` |
| Common jobs | `agentctl dispatch --template <bugfix\|flaky-test\|docs-drift\|release-notes\|host-perf\|log-audit> --prompt '...'` |

## Setting up a team in another repo

The same three pieces work in any repo: role agents in `.claude/agents/`, a roster page like this one, and a pipeline section in the repo's CLAUDE.md that the lead follows. The `team-bootstrap` template drafts all three from a one-paragraph description:

```
agentctl dispatch --template team-bootstrap --dir <repo> --prompt '<what the repo is, who works on it, what must stay human>'
```

There is no team object in agentctl: a team is a convention built from project agents, `role:` frontmatter, `@shared` includes and templates.
