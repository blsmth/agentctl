# Per-Agent Permissions

agentctl gives you fine-grained control over what each agent is allowed to do. Permissions are enforced at two layers — settings injection at launch and runtime hook validation — so dangerous commands are blocked even in multi-line scripts.

## User-level config

Per-agent settings live in `~/.config/agentctl/config.json`:

```json
{
  "agents": {
    "go-engineer": {
      "blocked_commands": ["kubectl delete", "terraform destroy", "git push --force"],
      "blocked_patterns": ["rm\\s+-rf\\s+/(?:usr|etc|home)"],
      "allowed_permissions": ["Bash(git *)", "Bash(go *)", "Bash(just *)"],
      "auto_accept": false
    }
  }
}
```

### Fields

| Field | Type | Description |
|-------|------|-------------|
| `blocked_commands` | `[]string` | Substrings to block in Bash commands (e.g. `"rm -rf"`) |
| `blocked_patterns` | `[]string` | Go regex patterns to block in Bash commands |
| `allowed_permissions` | `[]string` | Claude Code permission globs to pre-approve (e.g. `"Bash(git *)"`) |
| `auto_accept` | `bool` | Bypass all Claude Code permission prompts for this agent |

The example config above for `go-engineer`:

- Blocks `kubectl delete`, `terraform destroy`, and force-pushes by substring
- Blocks destructive `rm -rf` targeting system directories via regex
- Pre-approves `git`, `go`, and `just` commands so Claude Code doesn't prompt for them
- Still prompts for any other Bash command (e.g. `curl`, `docker`)

> **Warning:** `auto_accept: true` bypasses all Claude Code permission prompts for that agent. Claude Code will not ask for approval before running Bash commands, writing files, or fetching URLs. This is a power-user escape hatch for fully autonomous agents — do not enable it unless you trust the agent's instructions completely.

## Project-level config

Put a `.agentctl/config.json` in your project to add project-wide restrictions for all agents working in that directory. agentctl walks up from the agent's working directory to find this file.

> **Note:** agentctl creates `.agentctl/` with a self-ignoring `.gitignore` (`*`), so the file is untracked by default. To share it with your team, force-add it: `git add -f .agentctl/config.json`.

```json
{
  "blocked_commands": ["kubectl delete", "terraform destroy"],
  "blocked_patterns": ["rm\\s+-rf\\s+/"],
  "protected_branches": ["main", "production"],
  "agents": {
    "go-engineer": {
      "blocked_commands": ["go generate"]
    }
  }
}
```

| Field | Description |
|-------|-------------|
| `blocked_commands` | Substring blocks applied to all agents in the project |
| `blocked_patterns` | Regex blocks applied to all agents in the project |
| `protected_branches` | Branch names that agents cannot `git push` to |
| `agents.<name>` | Per-agent overrides with the same block fields |

**Security: what the project layer may not set.** Because a project file can arrive with a cloned repo, agentctl ignores (and logs a warning for) keys that could run commands, widen permissions, or send data elsewhere. Ignored from the project layer; set these only in the global `~/.config/agentctl/config.json`:

- Command keys: `notify_cmd`, `hooks`, `dispatch.on_complete` (and the per-agent forms).
- Permission keys: `agents.<name>.auto_accept` and `agents.<name>.allowed_permissions`. A project may add to `blocked_commands` / `blocked_patterns` but never remove entries the global config set.
- `permission_mode`, `llm`, and the `metrics` endpoint and credentials.
- `default_agent` (what prefix+A launches) is honored from the project layer only when it is safe: a valid agent name, naming a built-in, a plugin agent, or an agent in `~/.claude/agents` that the repo's own `.claude/agents` does not define or shadow, and not an agent with `auto_accept` or an approval-skipping `permission_mode` (unless it is also the global default). Otherwise prefix+A warns and launches the global `default_agent`.
- `agents.<name>.dir` (would point an `auto`-mode worker at any directory), `agents.<name>.context_keys` and `agents.<name>.cross_context` (would pull your global context store into the prompt of an agent working on the repo's input), and `agents.<name>.auto_improve`. A project may still switch `cross_context` and `auto_improve` off.

Agent names themselves are also untrusted: an agent definition (`.claude/agents/*.md`) whose name is not `[A-Za-z0-9._-]+` (max 64 characters, no leading `.` or `-`) is skipped at discovery, and a project-local agent that shadows a global agent of the same name prints a warning naming the project file when it is launched.

See [configuration.md](configuration.md#command-keys-are-global-only) for the layering rules.

**Precedence:** Project config is additive — it can only restrict, never relax. User-level blocks apply first, then project-level blocks are checked on top.

## How enforcement works

When agentctl launches an agent, enforcement happens at two distinct points:

### Layer 1: Settings file injection

agentctl writes a per-session Claude Code settings file at launch (`.agentctl/settings/<name>.json`), passed via `--settings`. It contains `permissions.deny` globs (one per `blocked_commands` entry, e.g. `Bash(*rm -rf*)`) and `permissions.allow` globs (from `allowed_permissions` or `auto_accept`). Claude Code enforces these natively *before* a tool runs — the agent never sees a denied tool call.

### Layer 2: Runtime hook validation

agentctl registers a `PreToolUse` hook that runs on every Bash call. It performs substring checks against `blocked_commands` and regex checks against `blocked_patterns` — for both user-level and project-level config — and returns a deny decision if matched. This layer catches commands that slip past the glob-based layer (e.g. multi-line scripts) and enforces project config, which is loaded at hook time, not at launch.

Both layers must pass for a Bash command to run.

## Managing permissions in the UI

The **Permissions popup** (`ctrl+b P`) manages project-level allow lists interactively. These apply to all agents in a given project directory.

## Automatic tool-call audit logging

Every tool call made by agentctl-managed agents — not just Bash, but Read, Edit, Write, WebFetch, Agent, etc. — is automatically logged from the `PostToolUse`/`PostToolUseFailure` hooks (`internal/audit`) to:

```
<runtimeDir>/audit/<session-id>.jsonl
```

where `<runtimeDir>` is `<project>/.agentctl` (or the path in `AGENTCTL_RUNTIME_DIR` if set). Each line is a JSON object; see `internal/audit.Record` for the full field list, including `v` (schema version), `ts`, `event`, `tool`, `tool_use_id`, `session`, `agent`, `window`, `ok`, `summary`, `input`, and `result`. The file is written with mode `0600` since it can contain tool output.

Audit logging runs for every agent launched by agentctl — there is no opt-out today. Logs are scoped per session so you can audit exactly what each dispatch session did. Read them with `agentctl audit` (add `--json` for the raw records).
