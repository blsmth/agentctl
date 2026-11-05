---
description: Read and write the agentctl context store (set, get, list, push, clear)
---

Parse $ARGUMENTS to extract the context subcommand and run it:

```bash
agentctl context <subcommand> [args] [flags]
```

Available subcommands:

- `list` — print all entries in the context store as a table
- `set <key> <value>` — write a key/value entry; supports `--agent <name>` and `--tag <tag>` flags
- `get <key>` — print the value for a key
- `push <key> --agent <name>` — inject a context entry into a running agent's prompt via tmux
- `clear` — remove all entries from the context store

If no subcommand is given in $ARGUMENTS, default to `list`.

Examples:
- `/agentctl:context list` → `agentctl context list`
- `/agentctl:context set repo_url https://github.com/org/repo` → `agentctl context set repo_url https://github.com/org/repo`
- `/agentctl:context set api_base_url https://api.example.com --tag discovery` → `agentctl context set api_base_url https://api.example.com --tag discovery`
- `/agentctl:context get repo_url` → `agentctl context get repo_url`
- `/agentctl:context push repo_url --agent go-engineer` → `agentctl context push repo_url --agent go-engineer`
- `/agentctl:context clear` → `agentctl context clear`

Common agent use-case — sharing a discovery with another agent:
1. Store the discovery: `/agentctl:context set db_schema_path /tmp/schema.sql --tag discovery`
2. Push it to a peer agent: `/agentctl:context push db_schema_path --agent python-engineer`
