---
description: Read and write the agentctl context store (set, get, list, prune, clear)
---

Parse $ARGUMENTS to extract the context subcommand and run it:

```bash
agentctl context <subcommand> [args] [flags]
```

Available subcommands:

- `list` — print all entries in the context store as a table (the USED column shows when a `dispatch --prompt-key` last used the entry, `-` if never)
- `set <key> <value> [--tag <tag>]...` — write a key/value entry, read back by `get`, `dispatch --prompt-key <key>` and cron. Entries are never injected into prompts, except a `ticket-<N>-spec` entry, which goes into the `dispatch --issue N` launch. `--agent <name>` is accepted for old scripts but ignored. `--tag` may be repeated.
- `get <key>` — print the value for a key
- `prune --used [--older-than <duration>] [--dry-run]` — delete entries a dispatch has used at least that long ago (default `1d`). Entries never used are never deleted. Prefer `--dry-run` first.
- `clear <key>` — delete the single entry `<key>`. A key is required; there is no way to clear everything at once.

If no subcommand is given in $ARGUMENTS, default to `list`. If `clear` is given without a key, ask the user which key to delete rather than running it.

Examples:
- `/agentctl:context list` → `agentctl context list`
- `/agentctl:context set repo_url https://github.com/org/repo` → `agentctl context set repo_url https://github.com/org/repo`
- `/agentctl:context set api_base_url https://api.example.com --tag discovery` → `agentctl context set api_base_url https://api.example.com --tag discovery`
- `/agentctl:context get repo_url` → `agentctl context get repo_url`
- `/agentctl:context prune --used --dry-run` → `agentctl context prune --used --dry-run`
- `/agentctl:context clear repo_url` → `agentctl context clear repo_url`
