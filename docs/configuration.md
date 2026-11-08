# Configuration

agentctl reads JSON config from two places and merges them on every monitor tick (there is no watcher; edits take effect within about 500ms):

| Layer | Path | Purpose |
|---|---|---|
| Global | `$XDG_CONFIG_HOME/agentctl/config.json` (default `~/.config/agentctl/config.json`) | Your personal defaults. The only layer allowed to define commands. |
| Project-local | `.agentctl/config.json` in the project root | Per-project overrides. `.agentctl/` writes its own `.gitignore`, so this file is not committed unless you move it. |

Both files are optional. Every key is optional.

## Where agentctl stores files

agentctl follows the XDG base directory spec and splits user-level files by role:

| Role | Location | Contents |
|---|---|---|
| Config | `$XDG_CONFIG_HOME/agentctl` (default `~/.config/agentctl`) | `config.json`, user-authored `themes/*.json` |
| Data | `$XDG_DATA_HOME/agentctl` (default `~/.local/share/agentctl`) | `context/` (context store), `journal/` (work journal) |
| State | `$XDG_STATE_HOME/agentctl` (default `~/.local/state/agentctl`) | `registry/` (live sessions), `ratelimits.json`, `hub/` (hub session working directory) |

A relative `XDG_*` value is invalid per the spec and is ignored. `AGENTCTL_REGISTRY_DIR` still overrides the registry location.

Earlier versions kept everything in the config directory. On first use, agentctl moves each of `context/`, `journal/`, `registry/`, `ratelimits.json`, and `hub/` to its new home automatically (only when the new path does not already exist). If a move fails, for example across filesystems, agentctl logs it and keeps using the old location. New directories are created with mode 0700.

## Layering

The project-local file is overlaid on the global one field by field: a non-empty/non-null project value wins, otherwise the global value is used. Exceptions:

- `agents` merges per agent name; a project entry for `go-engineer` replaces the whole global `go-engineer` entry (no deep merge inside one agent).
- `models.overrides` merges per model key.
- `hooks` replaces the global map entirely when the project sets any hooks.
- Booleans that default to `true` (`open_pr_in_browser`, `auto_reclaim_worktree`) are tri-state: an explicit `false` in the project file survives the merge.

### Command keys are global-only

Keys that make agentctl run a shell command are honored **only from the global config**. They are ignored if they appear in a project-local `.agentctl/config.json`:

- `notify_cmd`
- `hooks.*` (and per-agent `agents.<name>.hooks`)
- `dispatch.on_complete` (and per-agent `dispatch.on_complete`)

Rationale: a project-local file can arrive with a cloned repository. Executing commands named by an untrusted repo would be remote code execution. See [SECURITY.md](../SECURITY.md).

## Top-level keys

| Key | Type | Default | Description |
|---|---|---|---|
| `notify_cmd` | string | platform default | Desktop-notification command template. See [Notifications](#notifications). |
| `open_pr_in_browser` | bool | `true` | Open a dispatched agent's PR in the browser (`gh pr view --web`) when it finishes. Each PR opens once per monitor process. |
| `bash_preview` | bool | `false` | Show the human approval popup before **every** Bash tool call, not just `agentctl dispatch`. See [bash_preview](#bash_preview). |
| `statusline_format` | string | `rich` | What `agentctl statusline` prints: `rich`, `complementary`, `full`, or `minimal`. Unknown values fall back to `rich`. |
| `statusline_align` | string | `left` | `left` or `right`. Right-aligns the `rich` format's lines. Unknown values fall back to `left`. |
| `work_journal` | bool | `false` | Journal a session summary on session end for all agents (needs `llm.provider`). Per-agent values can also enable it. |
| `llm` | object | disabled | Local model provider. See [llm](#llm). |
| `default_agent` | string | first discovered agent | Agent launched by the quick-launch keybinding. Global-only; ignored with a warning if it is not a discovered agent. |
| `theme` | string | `catppuccin-mocha` | Color theme name. |
| `auto_poll` | bool | `false` | Ticket-browser autopilot mode. |
| `auto_poll_interval` | string | `5m` | Autopilot poll interval (Go duration). |
| `restore_max_age` | string | `168h` | Oldest saved `state.json` that `agentctl restore` will restore agents from after a relaunch (Go duration). `0` or negative disables the gate. Global config only. Skipped agents are named in the log. |
| `session_retention_days` | int | `30` | Days to keep session metadata before purging. |
| `dispatch.on_complete` | string | empty | Deprecated; use `hooks.dispatch_complete`. Still honored as a fallback. Global-only. |
| `hooks` | object | none | Event name to list of shell commands. Global-only. See [hooks](#hooks). |
| `rate_limit.token_cap` | int | `88000` | Token budget per 5-hour window, used only to *estimate* usage when Claude Code has not pushed a real rate-limit percentage. |
| `metrics` | object | disabled | Prometheus endpoint and remote write. See [metrics](#metrics). |
| `models` | object | built-in registry | Model registry overrides and context thresholds. See [models](#models). |
| `agents` | object | none | Per-agent settings. See [Per-agent settings](#per-agent-settings). |

### Notifications

Notifications are hook-driven. agentctl runs `notify_cmd` with `{{title}}` and `{{message}}` substituted and a 30 second timeout.

| `notify_cmd` | Behavior |
|---|---|
| unset | Platform default: `alerter` on macOS, `notify-send` on Linux if it is installed, otherwise nothing. |
| `""` (empty string) | Notifications disabled. |
| any other string | That command. |

```json
{
  "notify_cmd": "terminal-notifier -title {{title}} -message {{message}}"
}
```

`notify_cmd` is global-only (see [Command keys](#command-keys-are-global-only)).

### bash_preview

By default agentctl only gates `agentctl dispatch` behind the approval popup. With `bash_preview: true` every Bash tool call is previewed. The popup only appears when a human is attached to the tmux session **and** is looking at the exact window that issued the command. Otherwise the command falls through to Claude Code's normal permission flow, so an unattended dispatched agent can never deadlock on an invisible popup.

```json
{
  "bash_preview": true,
  "agents": { "go-engineer": { "bash_preview": false } }
}
```

A per-agent value overrides the global one.

### statusline_format and statusline_align

`agentctl statusline` is the data source for Claude Code's status line (`agentctl statusline install` wires it up).

| Format | Output |
|---|---|
| `rich` (default) | Colored, up to three lines: account rate limits; session metrics (model, context %, tokens, cache hit rate, turns, cost, duration); workspace (directory, branch, dirty count, worktree, PR). |
| `complementary` | Account-level figures only, e.g. `5h 12% · 7d 9% · $13.43`. Absent components are omitted; if all are absent it prints `-`. |
| `full` | Model, context %, and agent name. |
| `minimal` | A bare `-`. |

`statusline_align: "right"` pads the `rich` lines flush with the terminal's right edge.

### hooks

Lifecycle hooks run shell commands (via `sh -c`) after agentctl's built-in handling. Supported event keys: `session_start`, `session_end`, `notification`, `stop`, `stop_failure`, `pre_tool`, `post_tool`, `dispatch_complete`. Per-agent hooks run after global hooks for the same event. Global-only.

```json
{
  "hooks": {
    "dispatch_complete": ["echo \"$AGENTCTL_AGENT opened PR $AGENTCTL_PR\" >> ~/agentctl-done.log"]
  }
}
```

`dispatch_complete` commands receive `AGENTCTL_ISSUE`, `AGENTCTL_AGENT`, `AGENTCTL_BRANCH`, `AGENTCTL_WORKDIR`, `AGENTCTL_PR`, and `AGENTCTL_WINDOW_ID`.

### llm

A small single-turn completion provider. Its only consumer today is the work journal. Every failure (host down, timeout, invalid response) is non-fatal: the caller logs and moves on.

| Key | Default | Description |
|---|---|---|
| `llm.provider` | `""` (disabled) | `ollama`, `claude` (shells out to the `claude` CLI; explicit opt-in only), or empty. |
| `llm.ollama.host` | `http://localhost:11434` | Ollama base URL. |
| `llm.ollama.model` | none, required | Model name. There is deliberately no default: a guessed model that is not pulled gives a confusing 404. |
| `llm.ollama.num_ctx` | `8192` | Context window sent on every request. Ollama's own default is 4096 regardless of model. |
| `llm.ollama.num_predict` | `512` | Max tokens to generate. |
| `llm.ollama.keep_alive` | `30m` | How long Ollama keeps the model loaded. |
| `llm.ollama.timeout` | `30s` | Per-request timeout (Go duration). Journaling runs before the window is killed on `/exit`, so this is how long the window can linger. |

```json
{
  "work_journal": true,
  "llm": {
    "provider": "ollama",
    "ollama": { "model": "qwen3:8b" }
  }
}
```

Journal output goes to `~/.local/share/agentctl/journal/<agent>.md` with a machine-readable line per entry in `~/.local/share/agentctl/journal/entries.jsonl`.

### metrics

| Key | Default | Description |
|---|---|---|
| `metrics.enabled` | `false` | Turn on the metrics system. |
| `metrics.endpoint` | none | Prometheus remote-write URL. |
| `metrics.push_interval` | `15s` | Push interval (Go duration). |
| `metrics.port` | `9101` | Local `/metrics` port. |
| `metrics.labels` | none | Static labels, e.g. `{"instance": "laptop"}`. |
| `metrics.basic_auth` | none | `{"username": "...", "password": "..."}` for the remote-write endpoint. |

### models

Teach agentctl about a new model without a release, and tune context-window warnings.

| Key | Default | Description |
|---|---|---|
| `models.default_context_window` | built-in | Window for models that match nothing in the registry. |
| `models.context_warn_pct` | `75` | Context % where the warning fires (clamped to 10-100). |
| `models.context_critical_pct` | `90` | Context % where the critical state fires. If warn >= critical both revert to defaults. |
| `models.reserve_output` | `true` | Subtract the model's max-output reserve from the usable window. |
| `models.overrides` | none | Map of model name (or prefix) to `context_window`, `max_output`, `short`, `input_per_mtok`, `output_per_mtok`, `cache_read_per_mtok`, `cache_create_per_mtok`. Zero fields fall through to the built-in value. |

`agentctl models` prints the resolved registry.

## Per-agent settings

Under `agents.<name>`. Defaults for a new agent: `alerts: true`, `alert_config.blocked: true`, `alert_config.idle: true`, `cross_context: true`.

| Key | Type | Default | Description |
|---|---|---|---|
| `alerts` | bool | `true` | Master switch for this agent's notifications. |
| `alert_config.blocked` | bool | `true` | Notify on Working to Blocked (needs approval). |
| `alert_config.idle` | bool | `true` | Notify on Working to Idle (done). |
| `alert_config.gone` | bool | `false` | Reserved; no notification uses it yet. |
| `auto_improve` | bool | `false` | Launch a self-improvement session when a session ends. Global-only (a project may only switch it off). |
| `cross_context` | bool | `true` | Inject a cross-agent summary at launch. Global-only (a project may only switch it off). |
| `context_keys` | string[] | none | Context-store keys always injected at launch for this agent. Global-only. |
| `dir` | string | empty (portable) | Fixed working directory. Global-only. |
| `model` | string | inherit | `sonnet`, `opus`, `haiku`, or a full model name. |
| `effort_level` | string | unset | `low`, `medium`, `high`, `max`, or `auto`. |
| `permission_mode` | string | `auto` for worker dispatches; unset otherwise | Value passed to `claude --permission-mode`. Worker dispatches (`--issue`, `--prompt`, `--prompt-key`) default to `auto`; interactive modes (bare, discuss, triage, review) get no flag unless set. One of `acceptEdits`, `auto`, `bypassPermissions`, `manual`, `dontAsk`, `plan`; an unknown value logs a warning and falls back to the default. |
| `max_thinking_tokens` | int | `0` (default) | Thinking budget. |
| `blocked_commands` | string[] | none | Substrings that deny matching Bash commands. |
| `blocked_patterns` | string[] | none | Regexes that deny matching Bash commands. |
| `allowed_permissions` | string[] | none | Permission globs to pre-approve, e.g. `WebFetch(*)`. Passed to Claude Code via a generated per-agent `--settings` file. |
| `auto_accept` | bool | `false` | Auto-accept permission prompts through the PreToolUse hook. Use with care. |
| `bash_preview` | bool | inherit | Overrides the global `bash_preview`. |
| `auto_reclaim_worktree` | bool | `true` | On a terminal session end, remove this agent's worktree and window if it is empty or landed. Set `false` to opt out; `git worktree prune` and `agentctl cleanup` are unaffected. |
| `work_journal` | bool | inherit | Enable journaling for this agent (needs `llm.provider`). |
| `dispatch.on_complete` | string | inherit | Per-agent completion command. Global-only. |
| `hooks` | object | none | Per-agent lifecycle hooks. Global-only. |

```json
{
  "agents": {
    "go-engineer": {
      "model": "sonnet",
      "auto_reclaim_worktree": false,
      "allowed_permissions": ["WebFetch(*)"]
    }
  }
}
```

## Environment variables

| Variable | Effect |
|---|---|
| `XDG_CONFIG_HOME` | Moves the global config directory (default `~/.config`). |
| `AGENTCTL_REPO` | Set by dispatch in a dispatched agent's environment, for its information only. It is never read to resolve the repo; use `agentctl dispatch --repo owner/name` for that. |
| `AGENTCTL_RUNTIME_DIR` | Override the `.agentctl/` runtime directory. |
