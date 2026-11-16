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
| `default_agent` | string | first discovered agent | Agent launched by the quick-launch keybinding. A project-local value overrides the global one only when it passes the safety checks in [permissions.md](permissions.md) (otherwise the global value is used, with a warning); ignored with a warning if it is not a discovered agent. |
| `theme` | string | `catppuccin-mocha` | Color theme name. |
| `auto_poll` | bool | `false` | Ticket-browser autopilot mode. |
| `auto_poll_interval` | string | `5m` | Autopilot poll interval (Go duration). |
| `restore_max_age` | string | `168h` | Oldest saved `state.json` that `agentctl restore` will restore agents from after a relaunch (Go duration). `0` or negative disables the gate. Global config only. Skipped agents are named in the log. |
| `new_window_position` | string | `left` | Where new agent windows open in the tmux window list: `left` (first position) or `right` (appended). Unknown values fall back to `left`. Global config only. Utility windows (tickets, sessions, agents, shell) are unaffected. |
| `session_retention_days` | int | `30` | Days to keep session metadata before purging. |
| `dispatch.on_complete` | string | empty | Deprecated; use `hooks.dispatch_complete`. Still honored as a fallback. Global-only. |
| `hooks` | object | none | Event name to list of shell commands. Global-only. See [hooks](#hooks). |
| `rate_limit.token_cap` | int | `88000` | **Deprecated, unused.** Formerly estimated usage when no rate-limit percentage had been pushed; the monitor no longer derives rate limits. Still accepted so existing config files keep loading. |
| `metrics` | object | disabled | Prometheus endpoint and remote write. See [metrics](#metrics). |
| `models` | object | built-in registry | Model registry overrides and context thresholds. See [models](#models). |
| `backends` | object | none | Named self-hosted endpoints. Global-only. See [Backends](#backends-ollama-and-nous-portal). |
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
| `backend` | string | none | Name of a `backends` entry to launch this agent against instead of Anthropic. Global-only. |
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

## Backends (Ollama and Nous Portal)

A backend points Claude Code at an Anthropic-compatible endpoint instead of Anthropic. Two kinds exist, chosen by `kind`: `"ollama"` (the default when `kind` is empty; a self-hosted server) and `"nous"` (the [Nous Research Portal](#nous-portal-kind-nous)). Everything up to the Nous section describes the Ollama kind.

Ollama speaks the Anthropic Messages API, so Claude Code can run against a self-hosted server with only env vars and a model flag. Define named backends globally and point agents at them:

```json
{
  "backends": {
    "fenrir": {
      "base_url": "http://fenrir:11434",
      "model": "qwen3.6:35b-a3b",
      "auth_token": "ollama",
      "max_context_tokens": 131072
    }
  },
  "agents": { "go-engineer-local": { "backend": "fenrir" } }
}
```

| Key | Description |
|---|---|
| `kind` | Empty or `ollama` (this section), or `nous`. |
| `base_url` | Required. An `http`/`https` URL. |
| `model` | Required. Matched exactly against the server's model list. |
| `auth_token` | Dummy token; defaults to `ollama` (Ollama ignores it). |
| `max_context_tokens` | Sets `CLAUDE_CODE_MAX_CONTEXT_TOKENS` so Claude Code knows the context size of a model it does not recognize. `0` leaves it unset. |

For an Ollama backend, a launch sets `ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN`, an explicitly **empty** `ANTHROPIC_API_KEY` (so a real key cannot leak to the endpoint), `CLAUDE_CODE_MAX_CONTEXT_TOKENS`, and points `ANTHROPIC_DEFAULT_OPUS_MODEL`, `ANTHROPIC_DEFAULT_SONNET_MODEL`, `ANTHROPIC_DEFAULT_HAIKU_MODEL`, and `CLAUDE_CODE_SUBAGENT_MODEL` at the backend model (background tasks and subagents use aliases that do not exist on Ollama). The backend model wins over the agent's `model`; an explicit `dispatch --model` wins over both.

`agentctl dispatch --backend <name>` overrides the agent's backend; `--backend none` forces Anthropic. Dispatch, improve, resume, and `agentctl restore` all apply the backend. Restore re-resolves the backend name saved with the window; if it no longer exists in config the window is skipped (logged), never relaunched on Anthropic. `agentctl status <window>` and `--json` show the backend.

**Global-only.** `backends` and `agents.<name>.backend` are ignored, with a warning, in a project-local `.agentctl/config.json`. A project that could set `base_url` could silently redirect every prompt and every file the agent reads to an attacker's host. An agent that names an unknown backend logs a warning at load and fails at launch; it never falls back to Anthropic.

**Preflight.** Before launching, agentctl does `GET <base_url>/api/tags` (3s timeout). An unreachable server is a hard error naming the URL. If the response is an Ollama tag list and the model is not in it, the launch fails and lists what is available. Names match exactly: `qwen3.6:35b` and `qwen3.6:35b-a3b` are the same digest but different names. A non-200 or non-Ollama response skips the model check, since it may be another Anthropic-compatible server.

Gotchas:

- **Context length is a server setting.** Ollama defaults to 4096 tokens of context regardless of the model, and the Anthropic-compatible API does not let the client set `num_ctx`. The Claude Code system prompt alone exceeds that, so run the server with `OLLAMA_CONTEXT_LENGTH` set (e.g. `131072`) and set `max_context_tokens` to match.
- **Per-model context without a server restart.** Instead of `OLLAMA_CONTEXT_LENGTH` (which needs a server restart, and on macOS the Ollama app sometimes refuses to quit), bake the context into a derived model. This affects only that model (verified on Ollama 0.40):

  ```sh
  printf "FROM gemma4\nPARAMETER num_ctx 65536\n" > Modelfile
  ollama create gemma4-64k -f Modelfile
  ```

  Then set the backend `model` to `gemma4-64k` and `max_context_tokens` to `65536`. Confirm the context the model actually loaded with via `ollama ps`, or `GET /api/ps` (field `context_length`).
- **Context size floor.** Claude Code's fixed overhead (system prompt plus tool definitions) uses roughly 30k tokens, so 32k of context is too small to be useful; 64k is a practical minimum.
- **Pick a tool-calling model.** qwen3.6 MoE works; deepseek-r1 cannot call tools.
- Cost metrics skip unknown models; that is expected.

### Nous Portal (`kind: "nous"`)

The Nous inference host (`https://inference-api.nousresearch.com`) exposes an Anthropic-style `POST /v1/messages` (undocumented by Nous), so Claude Code can talk to it directly with no proxy.

```json
{
  "backends": {
    "nous": {
      "kind": "nous",
      "model": "qwen/qwen3-coder-next",
      "models": { "haiku": "openai/gpt-oss-120b" }
    }
  },
  "agents": { "go-engineer-nous": { "backend": "nous" } }
}
```

| Key | Description |
|---|---|
| `kind` | `"nous"`. |
| `base_url` | Optional; defaults to the Nous host. A trailing `/v1` is stripped (Claude Code adds it). Must be `https`. |
| `model` | Required. A catalog slug such as `qwen/qwen3-coder-next` or `~anthropic/claude-sonnet-latest`. Slugs may contain `A-Za-z0-9._:/~-`. |
| `models` | Optional per-alias overrides `{opus, sonnet, haiku, subagent}`; each falls back to `model`. Useful for sending Claude Code's background haiku work to a cheaper model. |
| `auth` | `"api_key"` (the default). `"oauth"` is reserved and rejected as not supported. |
| `api_key_cmd` | Optional shell command whose first stdout line is the key (run with `sh -c`, 10s timeout), e.g. `"op read op://Private/nous/credential"`. Takes precedence over the stored key. |
| `max_context_tokens` | Optional. When `0`, the catalog's `context_length` for `model` is used. |
| `auth_token` | **Rejected** for nous. Keys never go in `config.json`. |

**Authentication.** Run `agentctl backend login nous` to enter the key (no-echo prompt; `--stdin` reads it from a pipe for scripting). The key is verified with one paid request (`POST /v1/messages`, `max_tokens: 1`); pass `--no-verify` to skip that. A 401 reports "key invalid or out of credits" and a 404 "model not found". `agentctl backend logout <name>` deletes the stored key, and `agentctl backend status [--verify]` lists each backend's kind, auth mode, and whether a credential exists and where it comes from (never the value).

**Where keys are stored.** On macOS, in the login keychain (service `agentctl`, account `backend:<name>`), written through `security -i` on stdin so the key is never in argv. On other systems, in a `0600` file under `$XDG_STATE_HOME/agentctl/credentials/` (default `~/.local/state/...`, directory `0700`); a file with broader permissions is refused. `api_key_cmd` bypasses the store.

**How the key reaches Claude Code.** The key never appears in tmux argv or env, launch scripts, logs, windowstate, `state.json`, or config. Claude Code gets it by running its `apiKeyHelper`, which agentctl points at the hidden `agentctl backend-token <name>` command (it prints exactly the key and a newline). The helper and `ANTHROPIC_BASE_URL` always travel together in the `--settings` file (named `<agent>@<backend>.json`) and, for worktree launches, the worktree's `.claude/settings.local.json`; the file also blanks `ANTHROPIC_API_KEY` and `ANTHROPIC_AUTH_TOKEN` so a lower settings layer cannot outrank the helper. The launch script unsets `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN` and `CLAUDE_CODE_OAUTH_TOKEN`. A restore, resume, or `--backend none` launch rewrites the worktree file, so Nous routing never lingers.

The `ANTHROPIC_AUTH_TOKEN` sentence above applies to the Ollama kind only; a nous launch sets no `ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN` or `ANTHROPIC_API_KEY` in the window env. It does set the alias model variables, `CLAUDE_CODE_SUBAGENT_MODEL`, `CLAUDE_CODE_MAX_CONTEXT_TOKENS` and `CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS=1`.

**Launch checks.** A nous launch fails, with the fix named, when:

- `--model` is given and is not a slug (no `/`): otherwise a Claude model name would silently run on your Anthropic subscription.
- `GET <base_url>/v1/models` (3s timeout, cached in the runtime dir for 10 minutes) is unreachable, or `model` or any `models.*` entry is missing. Up to 10 ids with the same prefix are suggested. A model whose `endpoints` do not list `messages` only logs a warning, since that field is unreliable.
- No key can be resolved (`agentctl backend login <name>`). No request is made with the key at launch.
- Any settings layer (managed settings, `~/.claude/settings.json`, `.claude/settings{,.local}.json` in the working directory and its parents) sets a non-empty `ANTHROPIC_API_KEY` or `ANTHROPIC_AUTH_TOKEN` in `env`; the error names the file.

Caveats:

- **Billing.** API keys bill pay-as-you-go credits, not any subscription's included usage. Rate limits depend on your Nous tier; check the Portal for current limits.
- **Models.** Only models verified to do tool use over `/v1/messages` are supported; the catalog's `endpoints` field is copied from OpenRouter and may be wrong. `anthropic/*` models are the safest bet. Prompt caching behaviour under concurrent agents is unverified, so cost may be higher than the token counts suggest.
- **The agent can read its own key.** It runs as your user, so it can run `agentctl backend-token`, `security find-generic-password`, or read the credential file. The `Bash(*backend-token*)` and `Edit(**/.claude/settings*.json)` deny rule that nous launches add are defence in depth only. Set a spend limit on the Nous key.
- **Cost display is wrong** for slug models: the statusline and metrics have no pricing for them.

## Environment variables

| Variable | Effect |
|---|---|
| `XDG_CONFIG_HOME` | Moves the global config directory (default `~/.config`). |
| `AGENTCTL_REPO` | Set by dispatch in a dispatched agent's environment, for its information only. It is never read to resolve the repo; use `agentctl dispatch --repo owner/name` for that. |
| `AGENTCTL_RUNTIME_DIR` | Override the `.agentctl/` runtime directory. |
