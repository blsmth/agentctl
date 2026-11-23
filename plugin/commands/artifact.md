---
description: Publish and manage artifacts (reports, charts, mockups) with agentctl artifact
---

Parse $ARGUMENTS and run `agentctl artifact` as a Bash tool call. Artifacts are files published by agentctl and viewed through `agentctl serve` (phone app or laptop browser); see the `artifacts` skill for how to build a good one.

Mapping:
- No arguments → `agentctl artifact list`
- `list` → `agentctl artifact list`; `list all` → `agentctl artifact list --all`
- `add <path|dir|-> [--title T]` → `agentctl artifact add <path> [flags]`
- `update <id> <path>` → `agentctl artifact update <id> <path>` (optionally `--note N`, `--title T`)
- `show <id>` → `agentctl artifact show <id>`
- `url <id>` → `agentctl artifact url <id>`
- `open <id>` → `agentctl artifact open <id>` (opens the laptop browser)
- `pin <id>` / `unpin <id>` → `agentctl artifact pin|unpin <id>`
- `rm <id>` → confirm with the user first (it deletes every revision), then `agentctl artifact rm <id> --yes` (the prompt cannot be answered from Bash)
- `prune` → `agentctl artifact prune --dry-run` first, then without `--dry-run` only if the user confirms
- A bare id → `agentctl artifact show <id>`

Flags the user typed (`--title`, `--description`, `--kind`, `--entry`, `--pin`, `--no-notify`, `--note`, `--all`, `--json`, `--dry-run`, `--yes`) pass through unchanged. Do not invent other flags.

Notes:
- `add` and `update` print the artifact id and URL; send the URL to the user. Update an existing artifact rather than adding a duplicate.
- Never publish secrets, tokens or env values.
- If serve is not running the URL is still printed and works once serve starts.
