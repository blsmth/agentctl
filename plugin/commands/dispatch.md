---
description: Dispatch a GitHub issue to an agent in an isolated worktree
---

Parse $ARGUMENTS to extract the issue number and optional agent name, then run the fully-constructed command immediately as a Bash tool call.

Parsing rules:
- Strip a leading literal word `issue` if present (e.g. `issue 42` → number is `42`)
- The first integer token is the issue number
- The next token (if any) after the number is the agent name

Build and immediately execute the command:
- If only a number was given:  `agentctl dispatch --issue <N>`
- If a number and agent were given: `agentctl dispatch --issue <N> --agent <name>`

No `<number>` or `<name>` placeholders may appear in the command you run — substitute real values only. If no issue number is present, ask the user for one instead of running anything.

Run this from inside an agentctl tmux session (the agent launches in a new tmux window there) with `gh` authenticated for the repo. The agent defaults to one inferred from issue labels, then `go-engineer`.

Examples:
- `/agentctl:dispatch 42` → `agentctl dispatch --issue 42`
- `/agentctl:dispatch issue 42` → `agentctl dispatch --issue 42`
- `/agentctl:dispatch 42 go-engineer` → `agentctl dispatch --issue 42 --agent go-engineer`
