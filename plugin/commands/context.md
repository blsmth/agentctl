---
description: Read and write the agentctl context store (set, get, list, push, clear)
---

Parse $ARGUMENTS to extract the context subcommand and run it:

```bash
agentctl context <subcommand> [args] [flags]
```

Available subcommands:

- `list` — print all entries in the context store as a table
- `set <key> <value> [--agent <name>] [--tag <tag>]...` — write a key/value entry. `--agent <name>` scopes the entry to that agent so it is auto-injected into that agent's prompt; without `--agent` the entry is NOT injected anywhere (it is only readable via `get`/`push`, or if an agent lists the key in its `context_keys`). `--tag` may be repeated.
- `get <key>` — print the value for a key
- `push <key> --agent <name>` — inject a context entry into a running agent's prompt via tmux (needs an agentctl tmux session)
- `clear <key>` — delete the single entry `<key>`. A key is required; there is no way to clear everything at once.

If no subcommand is given in $ARGUMENTS, default to `list`. If `clear` is given without a key, ask the user which key to delete rather than running it.

Examples:
- `/agentctl:context list` → `agentctl context list`
- `/agentctl:context set repo_url https://github.com/org/repo` → `agentctl context set repo_url https://github.com/org/repo`
- `/agentctl:context set api_base_url https://api.example.com --agent go-engineer --tag discovery` → same command, scoped to `go-engineer`
- `/agentctl:context get repo_url` → `agentctl context get repo_url`
- `/agentctl:context push repo_url --agent go-engineer` → `agentctl context push repo_url --agent go-engineer`
- `/agentctl:context clear repo_url` → `agentctl context clear repo_url`
