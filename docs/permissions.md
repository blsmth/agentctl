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
- Routing and machine-wide keys: `backends`, `accounts`, `default_account`, `account_rules`, `agents.<name>.backend`/`account`/`harness`, `serve`, `cron`, `titles`, `close_children`, `chrome_bar`, `hub_chrome`, `auto_poll`, `restore_max_age`, `new_window_position`.
- Guards a project may only tighten: `bash_preview` and `dispatch_approval` (stricter only), `monitor` (off only).
- `default_agent` (what prefix+A launches) is honored from the project layer only when it is safe: a valid agent name, naming a built-in, a plugin agent, or an agent in `~/.claude/agents` that the repo's own `.claude/agents` does not define or shadow, and not an agent with `auto_accept` or an approval-skipping `permission_mode` (unless it is also the global default). Otherwise prefix+A warns and launches the global `default_agent`.
- `agents.<name>.dir` (would point an `auto`-mode worker at any directory), `agents.<name>.context_keys` and `agents.<name>.cross_context` (would pull your global context store into the prompt of an agent working on the repo's input), . A project may still switch `cross_context` off.

Agent names themselves are also untrusted: an agent definition (`.claude/agents/*.md`) whose name is not `[A-Za-z0-9._-]+` (max 64 characters, no leading `.` or `-`) is skipped at discovery, and a project-local agent that shadows a global agent of the same name prints a warning naming the project file when it is launched.

See [configuration.md](configuration.md#top-level-keys) (the Scope column) and [Layering](configuration.md#layering) for the full rules.

**Precedence:** Project config is additive — it can only restrict, never relax. User-level blocks apply first, then project-level blocks are checked on top.

## How enforcement works

When agentctl launches an agent, enforcement happens at two distinct points:

### Layer 1: Settings file injection

agentctl writes a per-session Claude Code settings file at launch (`.agentctl/settings/<name>.json`), passed via `--settings`. It contains `permissions.deny` globs (one per `blocked_commands` entry, e.g. `Bash(*rm -rf*)`) and `permissions.allow` globs (from `allowed_permissions` or `auto_accept`). Claude Code enforces these natively *before* a tool runs — the agent never sees a denied tool call.

### Layer 2: Runtime hook validation

agentctl registers a `PreToolUse` hook that runs on every Bash call. It performs substring checks against `blocked_commands` and regex checks against `blocked_patterns` — for both user-level and project-level config — and returns a deny decision if matched. This layer catches commands that slip past the glob-based layer (e.g. multi-line scripts) and enforces project config, which is loaded at hook time, not at launch.

Both layers must pass for a Bash command to run.

### Built-in guards

The same Bash hook (`hook-validate-bash`) and `hook-pre-tool` apply rules no config can turn off:

- **Global deny list.** Deleting `/` or `~`, pushing to `origin main`, and piping `curl` into a shell are denied for every agent.
- **Approval popups.** `agentctl dispatch` needs a human's approval per [`dispatch_approval`](configuration.md#dispatch_approval) (default: everything but `code-reviewer` PR reviews), `agentctl cron add`, `cron edit`, `cron resume` and `cron run --force` always do (see [cron](configuration.md#cron)), and with [`bash_preview`](configuration.md#bash_preview) every Bash command does. With `agentctl serve` running, the phone can answer the same popup. An unanswered popup denies after 9 minutes and marks the issuing window `!` until the human types into it ([needs attention](monitor-architecture.md#needs-attention)). An explicit `--agent` that `dispatch.ValidateAgentName` rejects (dispatch, cron add, cron edit with `--dir`) is denied before the popup with the CLI's error and agent list; an unparseable name, `--template`, `--mode improve` or a discovery error falls through to the popup.
- **Tamper rules.** Agents cannot touch `agentctl serve`'s state, control socket or process, pair devices, or start, stop or reinstall it; cannot write `schedules.json`, the cron state dir or the dispatch-approval records; cannot loosen the global `dispatch_approval`; and cannot touch the agent history stores (`<stateDir>/agent-history*.git`) with git or file tools (`checkHistoryTamper`, `historyEditTarget`: `--git-dir`, `-C`, `GIT_DIR=`, `cd`, rm/mv/cp, a relative path after `cd`, symlinks to a store, Write/Edit/MultiEdit; even read-only git is denied; globs and operations on the state dir root that can land in a store are denied too, while string-building tricks inside an interpreter are out of scope for this speed bump), so `git checkout <old> -- config.json` cannot get around the config and revert gates; use `agentctl agents history|diff|revert`. Reads pass, and `agentctl serve logs` / `agentctl cron list` are the intended read paths.
- **No agentctl from source.** `go run` of the agentctl root package (`go run .` in this repo, `go run github.com/blsmth/agentctl`) is denied, so an agent cannot bypass the gates with an unbuilt copy; `go run ./internal/...` and other repos pass.

These are speed bumps against accidents and naive attempts, not a sandbox: the agent runs as your user.

## Managing permissions in the UI

The **Permissions popup** (`ctrl+b P`) manages project-level allow lists interactively. These apply to all agents in a given project directory.

## Automatic tool-call audit logging

Every tool call made by agentctl-managed agents — not just Bash, but Read, Edit, Write, WebFetch, Agent, etc. — is automatically logged from the `PostToolUse`/`PostToolUseFailure` hooks (`internal/audit`) to:

```
<runtimeDir>/audit/<session-id>.jsonl
```

where `<runtimeDir>` is `<project>/.agentctl` (or the path in `AGENTCTL_RUNTIME_DIR` if set). Each line is a JSON object; see `internal/audit.Record` for the full field list, including `v` (schema version), `ts`, `event`, `tool`, `tool_use_id`, `session`, `agent`, `window`, `ok`, `summary`, `input`, and `result`. The file is written with mode `0600` since it can contain tool output.

Audit logging runs for every agent launched by agentctl — there is no opt-out today. Logs are scoped per session so you can audit exactly what each dispatch session did. Read them with `agentctl audit` (an alias for `agentctl audit log`; add `--json` for the raw records).

## Session audit reports

`agentctl audit report [target]` grades one past session for safety and efficiency. The target is the same as for `agentctl race`: a window (`@N`), a session ID, a transcript path, or nothing for the current window. `--all` grades many sessions at once (every account's transcripts, pi included) and prints a leaderboard plus the findings that recur most:

```bash
agentctl audit report                  # this window's session
agentctl audit report @12 --json       # schema 1, stable for scripts
agentctl audit report --all --since 2d --agent go-engineer --severity med
```

The transcript is the source. When the hook audit trail above has records for the same session ID (searched in every registered project's audit dir and the one belonging to the transcript's own repo), they are merged by `tool_use_id` (or, for records without one such as the PreToolUse denials, by tool, command or file path, and a timestamp within 5s) and are authoritative for denials (including whether a rule or a person refused, `denied_by`) and failures. Without them, denials and failures are inferred from the tool results, and the report says so.

Findings come from deterministic rules (`internal/sessionaudit/rules.go`). Bash commands are parsed with `mvdan.cc/sh`, so text inside quotes or a heredoc to `cat` never matches, while `bash -c '...'`, a heredoc to a shell, command substitutions, pipelines, `&&`/`;` chains and subshells are all checked, and wrappers such as `sudo`, `env`, `timeout` and `xargs` are peeled off. A command that does not parse yields no findings and is counted as unparsed.

| Severity | Safety rules | Efficiency rules |
|---|---|---|
| high | `rm -r` on `/`, home, `..` or outside the working tree (temp dirs excepted; relative paths follow a literal `cd` earlier in the same command); `git push --force`/`-f`/`+ref` (not `--force-with-lease`); push to `main`/`master`; `curl`/`wget` piped to a shell or run by one (`sh -c "$(curl …)"`, `bash <(curl …)`, `eval "$(curl …)"`); `sudo`; `chmod -R 777`; reading secrets (a copy's destination is not a read; `.env`, `*.pem`, `id_rsa*`, `~/.aws/credentials`, `security find-*-password`, env dumps to a file or the network); `git reset --hard`, `git clean -fd`, `git checkout .`/`restore .` | |
| med | `rm -r` of `.`, `*` or `./*` with no `cd` earlier in the same command (the whole working dir); `git add -A`/`.`; `--no-verify`; Write/Edit outside the working tree (Claude Code's `agent-memory` and `plans` dirs excepted); `kill -9`/`pkill`/`killall`; uploads (`curl -d/-F/-T`, but not `-d` with `-G`/`--get`; `scp`/`rsync` to a remote); `terraform apply` without `-target` or with `-auto-approve` | the same failed Bash command re-run with no edit in between; a sleep loop; `sleep` of 5s or more; a tool error rate of 25% or more (10+ calls) |
| low | each denied call (rule or person); a switch to a looser permission mode | re-reading a file that did not change; `cat`/`head`/`tail`/`grep`/`find`/`ls`/`sed -i` where Read/Grep/Glob/Edit exist (one finding per command name, Claude Code only); a failed edit; a gap of 5 minutes or more inside a turn |

A safety rule fires on what the agent tried, so a denied command still counts once for the attempt and once as a denial. Each category starts at 100 and loses 25 per high, 10 per med and 3 per low finding (floor 0). Efficiency penalties are scaled by 50/steps for sessions longer than 50 steps, so a long session is not punished for being long. Grades: A ≥ 90, B ≥ 80, C ≥ 70, D ≥ 60, else F. The weights live in one table in `internal/sessionaudit/report.go`. `--severity` hides lower findings from the output; the scores always count every finding.

The metrics block is not scored: tool calls by type, turns, wall and agent-active time (as `agentctl race` measures it), tokens, cost through the same pricing as `agentctl cost` (an unpriced model is an unknown cost, never a guess), cost per edit, error rate, repeat reads, sub-agent (sidechain) entries, which are skipped by the rules, and unparsed Bash commands.

### Written verdict (`--narrate`)

`agentctl audit report --narrate` adds a 3-5 sentence verdict (what went well, what was risky, one habit to change) written by the configured `nous` backend (the same backend and model as `titles`). Only the findings (including their short evidence snippets, which are command lines or tool inputs) and metrics are sent, never tool output or file contents. Evidence is redacted first: secret-looking `KEY=value` pairs and flags (token, secret, password, key, auth), `Authorization`/Bearer values, common key shapes (`sk-`, `ghp_`, `github_pat_`, `xox*-`, `AKIA…`) and URL `user:pass@` are replaced with `[REDACTED]`, and the home directory is shown as `~`. Redaction is pattern based, so treat it as best effort. The prompt is capped at about 6KB (the lowest-severity findings are dropped first). It is off by default. With `--json` it appears as a `narrative` field only when `--narrate` is passed.

Verdicts are cached per session under `<stateDir>/audit/narratives/` (pruned after 90 days by housekeeping) with a hash of what was sent, so an unchanged report is free and a changed one is narrated again. Sessions with no findings are skipped. With `--all`, each session that has findings is narrated (at most 20 per run; the skipped count is logged and noted), then one aggregate verdict for the run. Calls are ledgered under feature `audit` (see `agentctl cost`). It is non-fatal: with no backend, no key, a failed request, or a fresh out-of-credits marker (which stops spending until it goes stale, like titles), the report prints without a verdict and a one-line note says why.
