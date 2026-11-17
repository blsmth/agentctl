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
| Config | `$XDG_CONFIG_HOME/agentctl` (default `~/.config/agentctl`) | `config.json`, `schedules.json` (`agentctl cron` jobs), user-authored `themes/*.json` |
| Data | `$XDG_DATA_HOME/agentctl` (default `~/.local/share/agentctl`) | `context/` (context store), `journal/` (work journal) |
| State | `$XDG_STATE_HOME/agentctl` (default `~/.local/state/agentctl`) | `registry/` (live sessions), `ratelimits.json`, `hub/` (hub session working directory), `serve/` (`agentctl serve` certificates, devices, log), `cron/` (`agentctl cron` tick lock, state, run history, log) |

A relative `XDG_*` value is invalid per the spec and is ignored. `AGENTCTL_REGISTRY_DIR` still overrides the registry location.

Earlier versions kept everything in the config directory. On first use, agentctl moves each of `context/`, `journal/`, `registry/`, `ratelimits.json`, and `hub/` to its new home automatically (only when the new path does not already exist). If a move fails, for example across filesystems, agentctl logs it and keeps using the old location. New directories are created with mode 0700.

## Layering

The project-local file is overlaid on the global one field by field: a non-empty/non-null project value wins, otherwise the global value is used. Exceptions:

- `agents` merges per agent name; a project entry for `go-engineer` replaces the whole global `go-engineer` entry (no deep merge inside one agent).
- `models.overrides` merges per model key.
- `hooks` replaces the global map entirely when the project sets any hooks.
- Booleans that default to `true` (`open_pr_in_browser`, `auto_reclaim_worktree`, `monitor`) are tri-state: an explicit `false` in the project file survives the merge.

### Command keys are global-only

Keys that make agentctl run a shell command are honored **only from the global config**. They are ignored if they appear in a project-local `.agentctl/config.json`:

- `notify_cmd`
- `hooks.*` (and per-agent `agents.<name>.hooks`)
- `dispatch.on_complete` (and per-agent `dispatch.on_complete`)

`backends`, `accounts`, `default_account`, `account_rules` and `agents.<name>.account` are global-only for the same reason: they decide where prompts go and which login pays for them.

Rationale: a project-local file can arrive with a cloned repository. Executing commands named by an untrusted repo would be remote code execution. See [SECURITY.md](../SECURITY.md).

## Top-level keys

| Key | Type | Default | Description |
|---|---|---|---|
| `notify_cmd` | string | platform default | Desktop-notification command template. See [Notifications](#notifications). |
| `open_pr_in_browser` | bool | `true` | Open a dispatched agent's PR in the browser (`gh pr view --web`) when it finishes. Each PR opens once per monitor process. |
| `monitor` | bool | `true` | Kill switch for the per-project monitor. When `false`, session start and the hook watchdog do not start or replace a monitor and `agentctl health` says "monitor disabled". A running monitor is not killed; the next relaunch just does not start one. Everything except the macapp's `state.json` feed works without it. Global-only in the enabling direction: a project-local `"monitor": false` switches it off for that project, but a project can never switch it back on. |
| `hub_chrome` | bool | `false` | Show each agent's chrome bar inside the hub's live pane. When `false` the hub zooms the viewed window's main pane, which hides the bar there (the hub list already shows every agent); the zoom is undone when the hub moves to another row, closes, or you switch to the project session. Project sessions attached directly always show the bar. The hub key `c` toggles it for the running hub. Global config only. |
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
| `accounts` | object | none | Named Claude logins, one config dir each. Global-only. See [Accounts](#accounts-multiple-claude-logins). |
| `default_account` | string | none | Account used when nothing else picks one. Global-only. |
| `account_rules` | array | none | Route launches to an account by repo root. Global-only. |
| `serve` | object | defaults | `agentctl serve` (mobile web app server). Global-only. See [serve](#serve). |
| `cron` | object | defaults | `agentctl cron` (scheduled agent runs): kill switch and throttles. Global-only. See [cron](#cron). |
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

When accounts are configured (see [Accounts](#accounts-multiple-claude-logins)), every format except `minimal` shows the name of the account the session runs under (`$CLAUDE_CONFIG_DIR`, else `~/.claude`), the same one whose rate limits are shown: first on the `rich` rate-limit line (on the workspace line when there are no rate limits), dropped before the 5h figure when the line is narrow. With no accounts, or a dir that matches none, nothing is added.

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

The whole `metrics` block is global-only: a `metrics` key in a project's `.agentctl/config.json` is ignored (with a log warning), because one per-machine serve is shared by every project and must not depend on which project started it.

| Key | Default | Description |
|---|---|---|
| `metrics.enabled` | `false` | Turn on the metrics system. |
| `metrics.endpoint` | none | Prometheus remote-write URL. |
| `metrics.push_interval` | `15s` | Push interval (Go duration). |
| `metrics.port` | `9101` | Local `/metrics` port. |
| `metrics.labels` | none | Static labels, e.g. `{"instance": "laptop"}`. |
| `metrics.basic_auth` | none | `{"username": "...", "password": "..."}` for the remote-write endpoint. |

Metrics are produced by one per-machine process, `agentctl metrics serve`, not by the per-project monitor, so they work with `monitor: false`. Session start and the hook janitor (at most once a minute) start it detached when `metrics.enabled` is set and none is alive; a machine-wide lock keeps it to one. It exits 0 at once if another is running, exits after 10 minutes with no agentctl tmux sessions, and re-execs itself when the binary is replaced. Its pidfile and log are `<state dir>/metrics-serve.pid` and `metrics-serve.log` (`agentctl health` shows its pid and port).

- **Transitions** (spawned, completed, state transitions, tool uses, failures, active gauge, durations) come from an event log the state writers append to: `<runtimeDir>/metrics/events-<YYYY-MM-DD>.ndjson`, one line per real state change (and per Gone), written only while metrics are enabled. The serve tails each project's files from the end at startup, so history is not replayed; counters reset when it restarts, which Prometheus handles. Event files older than 3 days are removed.
- **Usage** (`agentctl_tokens_total`, `_tool_use_total`, `_turns_total`, `_context_pct`, `_cost_estimate_dollars`) is read from each window's Claude log, for every project found through tmux.
- Series names and existing labels are unchanged. Every transition and usage series gains a `project` label (the project directory's basename), since one process serves every project. `agentctl_monitor_uptime_seconds` now reports the **serve process** uptime (name kept so dashboards keep working).
- If the port is already taken (for example by an old-binary monitor), the serve logs it and carries on with remote write only.

### serve

Settings for `agentctl serve`, the per-machine HTTPS server the mobile web app talks to (design: [design/mobile-web-app.md](design/mobile-web-app.md)). **Every key is global-only**: a `serve` block in a project-local config is ignored with a warning. The server reads it at start; restart it (`agentctl serve install` again, or `launchctl kickstart -k gui/$(id -u)/com.blsmth.agentctl.serve`) after editing; both are human-only (agents are blocked from running them).

| Key | Default | Description |
|---|---|---|
| `serve.listen` | `:8443` | HTTPS listen address. While pairing, a plain-HTTP listener on port + 1 serves only the CA profile and instructions. |
| `serve.hostnames` | none | Extra DNS names for the server certificate. `<hostname>.local` and every non-loopback interface IP are always included. |
| `serve.allow_cidrs` | RFC 1918, `100.64.0.0/10`, loopback | Source-address allowlist, checked before anything else (including `/healthz`). `X-Forwarded-For` is never trusted. |
| `serve.allow_write` | `false` | Enable write actions (answer, reply, interrupt, dispatch, close, and answering approval popups from the phone). |
| `serve.keep_awake` | `true` | Hold `caffeinate -s` while any agent is working or blocked. |
| `serve.push.blocked` / `serve.push.done` | `true` | Push on an agent becoming blocked / finishing. |
| `serve.push.blocked_reminder_minutes` | `10` | One reminder per blocked episode; `0` disables. |
| `serve.push.approvals` | `true` | Push when a dispatch or Bash approval popup is waiting and can be answered from the phone (see [Remote approvals](design/mobile-web-app.md#remote-approvals)). Ignores quiet hours, since an unanswered approval blocks work; respects `muted_projects`. |
| `serve.push.quiet_hours` | none | Local `HH:MM-HH:MM` range with no pushes. |
| `serve.push.muted_projects` | none | Project names that never push. |
| `serve.push.vapid_subscriber` | `https://github.com/blsmth/agentctl` | VAPID contact (the JWT `sub`) sent to push services: an `https:` URL or an email address, with or without `mailto:`. |
| `serve.ntfy_topic` | none | Also post notifications to `https://ntfy.sh/<topic>`. **ntfy.sh topics are public**: anyone who knows or guesses the name can read every post, so use a long random topic (for example `openssl rand -hex 16`). |
| `serve.ntfy_details` | `false` | Put agent and project names in ntfy posts, plus a `Click` link to the window (which publishes the server's LAN address). Off by default, a post says only what kind of event happened ("an agent is blocked"). |

State lives in `$XDG_STATE_HOME/agentctl/serve/` (0700): the local CA and server certificate, `devices.json` (token hashes only), `vapid.json` (Web Push keypair, 0600), `subscriptions.json` (per-device push subscriptions; dropped when a device is revoked), `serve.pid`, `serve.log`, and the 0600 control socket `control.sock`. Commands: `agentctl serve` (foreground), `serve install` / `uninstall` (launchd LaunchAgent; prints a systemd user unit on Linux), `serve status`, `serve pair [--name N]`, `serve devices [rm <id|name>]`, `serve logs [-n N] [--audit] [--follow]` (read-only tail of `serve.log` or `audit.jsonl`). Agents cannot touch the serve state dir, restart or reinstall serve, or pair devices (the Bash validator blocks it, since remote approvals depend on serve); `serve logs` is how they read its logs. The CA is name-constrained to local names (`.local`, `.lan`, `.home`, `.home.arpa`, `.internal`, the `serve.hostnames` present when it was created) and private/loopback/ULA IPs; after adding a hostname outside those, run `agentctl serve ca rotate` (every device must reinstall the profile and pair again).

Web Push subscriptions are accepted only for known push services (Apple, Firebase Cloud Messaging, Mozilla, Windows Notification Service), and push requests never connect to private, loopback or link-local addresses or follow redirects.

### cron

Global defaults for `agentctl cron`, scheduled agent runs (design: [design/cron.md](design/cron.md)). **Every key is global-only**: a `cron` block in a project-local config is ignored with a warning, and the jobs themselves live only in `~/.config/agentctl/schedules.json` (0600), so nothing a cloned repository ships can create or loosen a recurring autonomous job.

| Key | Default | Description |
|---|---|---|
| `cron.enabled` | `true` | Kill switch. `false` makes `agentctl cron tick` a no-op (it still stamps its last-tick time). |
| `cron.min_interval` | `15m` | `cron add` rejects a schedule whose fires, measured over the next 50, come closer together than this. |
| `cron.max_concurrent` | `2` | Live, not-done scheduled windows across all jobs; a fire past the cap is recorded as `skipped:concurrency`. |
| `cron.skip_if_usage_above` | `{"five_hour": 80, "seven_day": 90}` | Skip a fire when the statusline-pushed 5h or 7d used percentage of the account the job runs under is at or above this. The account resolves as for `dispatch` (the job's `account`, `agents.<name>.account`, `account_rules` on the repo root, `default_account`); a hot account is skipped, never swapped for another. Missing data, or data older than 6h, counts as unknown: the run goes ahead and the run log says so. A job's own `skip_if_usage_above` overrides per field. |
| `cron.catchup_window` | `6h` | With `catchup: latest` (the default), the most recent missed fire still runs if it is at most this late; older ones are recorded as `skipped:catchup`. |

Jobs (`agentctl cron add <name> --schedule '30 7 * * 1-5' --agent A --dir D (--prompt T | --prompt-key K | --prompt-file F)`) take `[--no-worktree] [--model] [--effort] [--backend] [--account] [--timeout 45m] [--overlap skip|allow] [--catchup latest|none]`. Schedules are 5-field cron in local time, or `@hourly`/`@daily`/`@weekly`. `harness` (`claude`) and `mode` (`window`) are in the schema but only those values are accepted for now. `--prompt-file` is copied into the job when it is added; `--prompt-key` is read from the context store at each fire.

`agentctl cron install` writes `~/Library/LaunchAgents/com.blsmth.agentctl.cron.plist`, which runs `agentctl cron tick` every 60 seconds with a PATH covering the current `claude`, `tmux` and `gh` and a UTF-8 locale (on Linux it prints a crontab line instead). Each tick takes a flock on `<state dir>/cron/tick.lock`, records `last_fire` per job in `cron/state.json` before launching (a crash cannot fire twice), waits a deterministic 0-90s per-job jitter, and launches through the same path as `agentctl dispatch --prompt`: a worktree per fire (`.worktrees/cron-<job>-<YYYYMMDD-HHMM>`, unless `worktree: false`), a detached project session if none is running, and `scheduled_by` in the window's record. `overlap: skip` (default) skips a fire while the previous run's window is alive and not done. A job `timeout` sends `C-c` to the window and marks it blocked with a notice; it never kills it. Every decision is appended to `cron/runs.ndjson` (`agentctl cron runs [<name>]`). `agentctl cron list` shows the next fire, last result and throttles; `agentctl health` reports whether the ticker is loaded, the last tick's age, and jobs whose last 3 launches failed.

### models

Teach agentctl about a new model without a release, and tune context-window warnings.

| Key | Default | Description |
|---|---|---|
| `models.default_context_window` | built-in | Window for models that match nothing in the registry. |
| `models.context_warn_pct` | `75` | Context % where the warning fires (clamped to 10-100). |
| `models.context_critical_pct` | `90` | Context % where the critical state fires. If warn >= critical both revert to defaults. |
| `models.reserve_output` | `true` | Subtract the model's max-output reserve from the usable window. |
| `models.overrides` | none | Map of model name (or prefix) to `context_window`, `max_output`, `short`, `input_per_mtok`, `output_per_mtok`, `cache_read_per_mtok`, `cache_create_per_mtok`. Zero fields fall through to the built-in value (a price field replaces the whole rate card, including any long-context tier). See [Pricing and cost](#pricing-and-cost). |

`agentctl models` prints the resolved registry.

### Pricing and cost

One table prices everything: `internal/tokens`. `tokens.Lookup(model)` resolves, in order, a `models.overrides` entry, the cached Nous catalog (exact id such as `openai/gpt-6-astra`), the built-in Anthropic table, then a family fallback (`opus`/`sonnet`/`haiku`/`fable` substring) at the **newest** generation's rates. Anything else is **unknown**: its cost is shown as `?` and excluded from totals (with a count of excluded sessions), never priced as Sonnet.

- **Anthropic built-ins** (per MTok, input / output / cache read / 5m cache write): Opus 5.5 `4 / 20 / 0.20 / 5`, Sonnet 5.5 `2 / 10 / 0.10 / 2.50`, Haiku 5.5 `0.10 / 0.50 / 0.01 / 0.125`, Fable 5.2 `8 / 40 / 0.20 / 10`, Fable 5.1 `10 / 50 / 0.25 / 12.50`, Fable 5 `10 / 50 / 1 / 12.50`, Opus 5 and 4.5-4.8 `5 / 25 / 0.50 / 6.25`, Sonnet 5 `2 / 10 / 0.20 / 2.50`. Refresh from <https://platform.claude.com/docs/en/about-claude/pricing>.
- **Long-context tier.** A model may carry a threshold and alternate rates, applied **per request** when its prompt (input + cache read + cache write) exceeds the threshold. Haiku 5.5 bills a request over 100,000 prompt tokens at `0.50 / 2.50 / 0.05 / 0.625`. Session totals are therefore summed per request (turn), never from aggregate token totals; aggregates are used only when a session has no per-turn data (the `.meta.json` sidecar), where tiers cannot apply. A `models.overrides` price field replaces the whole rate card, including the tier.
- **Nous Portal.** `agentctl dispatch` caches the catalog (`GET /v1/models`) as `nous-models-*.json` in the runtime dir, now including each model's `pricing` ($/token strings: `prompt`, `completion`, `input_cache_read`, `input_cache_write`). Pricing is converted to per MTok and read lazily, once per process, from that file; nothing on a hot path touches the network. A missing or zero `input_cache_write` bills cache writes as input; a missing or zero `input_cache_read` means the model does not cache, so cache reads bill at the input price. A `:free` variant not in the catalog is $0. `agentctl cost` refreshes the cache (3s timeout) when it meets an unpriced model and a nous backend is configured. A cache written by an older agentctl has no pricing and is refetched on next use.
- **Overrides always win**, so you can price a model the catalog lacks:

  ```json
  { "models": { "overrides": { "openai/gpt-6-astra": { "input_per_mtok": 10, "output_per_mtok": 50, "cache_read_per_mtok": 1, "cache_create_per_mtok": 12.5 } } } }
  ```

- **Billing class.** Each session is `metered` (Nous: real money), `sub` (Anthropic via the Claude subscription: the dollars are API-equivalent value, not a charge) or `free` (Ollama: always $0). The backend is recorded per session by the SessionStart hook (a small file under the session data dir, since the window state is deleted when the window closes) and classified with the global `backends` kind. Sessions recorded before this existed fall back to the model: `claude-*` is `sub`, a catalog model is `metered`, a bare non-Claude tag with an Ollama backend configured is `free`, anything else is unknown (`?`).
- **Message de-duplication.** Claude Code writes one JSONL line per content block, repeating the response's `message.id` and usage. Usage and turn counts are taken once per message id; counting every line overstates tokens by about 1.5x.

#### `agentctl cost`

```sh
agentctl cost                      # sessions from the last 7 days
agentctl cost --since 36h --metered
agentctl cost --by model           # or agent | day | project
agentctl cost <session-id>         # per-model breakdown for one session
agentctl cost --json
```

Columns: DATE, PROJECT, AGENT, MODEL, BILLING, TURNS, CACHE READ, OUTPUT, COST. The totals row has `metered` (the number that matters) and `sub` subtotals; sessions with unknown pricing show `?` and are excluded, with a count. `agentctl usage` stays token-only and `agentctl tables sessions` / the sessions browser now use the same pricing (they show `?` for unpriced models). With `--json`, an unknown cost is `null`.

**Statusline cost.** Claude Code's pushed `cost.total_cost_usd` comes from its own Claude price table, so for a non-`claude-*` model id (Nous, Ollama) it applies Claude rates to foreign token counts. agentctl hides it for those models and does not store it; use `agentctl cost`. (Not verified against a live non-Anthropic session here: JSONL logs carry no `total_cost_usd`, only the statusline payload does.)

### Agent-to-agent messaging

Agents talk to each other with Claude Code's native cross-session tools, `ListAgents` and `SendMessage` (Claude Code 2.1.224+), not through agentctl. Every agentctl launch passes `-n <name>` so sessions are findable: dispatched worktree agents use the worktree slug (for example `agent-go-engineer-issue-42`), bare launches use `<agent>-<project>-<N>`, where `N` is the agentctl instance number (`brandon:30` gives `brandon-agentctl-30`; the first window counts as 1), so concurrent windows never ask Claude Code for the same name (it silently renames duplicates). The name is stored in the window's `session_name` and reused on restore and resume, except that the legacy shared `<agent>-<project>` name, or a name another restored window already took, is regenerated from the window's instance number. The statusline push carries `session_name`, the `--name` or `/rename` value (an AI-generated title when none was set, so it is ignored for windows launched without `-n`); when it differs from the recorded one (a `/rename`) it replaces `session_name` and the launch-time name moves to `requested_session_name`. Names are only a hint: a worker is told its orchestrator by tmux window and pane ID (`@534.%777`, the pane being `main_pane`), matched against the `@N.%P` suffix of its ListAgents location, because the tmux session prefix of that location changes when the hub links windows. The name is included only when `session_name_verified` is set.

If an agent's `.md` frontmatter has a `tools:` list, Claude Code hides any tool not on it. Add `SendMessage` and `ListAgents` to that list; `agentctl dispatch` prints a warning naming the file when either is missing. Agents with no `tools:` line get every tool.

agentctl keeps one small notice path for the done/blocked backstop: when a dispatched worker goes idle or stops on a permission prompt, the dispatching window receives a short `agentctl notice: ...` line through its next prompt or tool result. It is factual, carries no worker output, and is not a substitute for the worker's own `SendMessage` report.

## Per-agent settings

Under `agents.<name>`. Defaults for a new agent: `alerts: true`, `alert_config.blocked: true`, `alert_config.idle: true`, `cross_context: true`.

| Key | Type | Default | Description |
|---|---|---|---|
| `alerts` | bool | `true` | Master switch for this agent's notifications. |
| `alert_config.blocked` | bool | `true` | Notify on Working to Blocked (needs approval, including a pending dispatch/bash approval popup). |
| `alert_config.idle` | bool | `true` | Notify on Working to Idle (done). |
| `alert_config.gone` | bool | `false` | Reserved; no notification uses it yet. |
| `auto_improve` | bool | `false` | Launch a self-improvement session when a session ends. Global-only (a project may only switch it off). |
| `cross_context` | bool | `true` | Inject a cross-agent summary at launch. Global-only (a project may only switch it off). |
| `context_keys` | string[] | none | Context-store keys always injected at launch for this agent. Global-only. |
| `dir` | string | empty (portable) | Fixed working directory. Global-only. |
| `model` | string | inherit | `sonnet`, `opus`, `haiku`, or a full model name. |
| `effort_level` | string | unset | `low`, `medium`, `high`, `max`, or `auto`. |
| `permission_mode` | string | `auto` for worker dispatches; unset otherwise | Value passed to `claude --permission-mode`. Worker dispatches (`--issue`, `--prompt`, `--prompt-key`) default to `auto`; interactive modes (bare, discuss, triage, review) get no flag unless set. One of `acceptEdits`, `auto`, `bypassPermissions`, `manual`, `dontAsk`, `plan`; an unknown value logs a warning and falls back to the default. `agentctl launch --permission-mode <mode>` overrides it for a single launch (unknown values are rejected); the hub's "new" action launches with `--permission-mode auto`. |
| `max_thinking_tokens` | int | `0` (default) | Thinking budget. |
| `blocked_commands` | string[] | none | Substrings that deny matching Bash commands. |
| `blocked_patterns` | string[] | none | Regexes that deny matching Bash commands. |
| `allowed_permissions` | string[] | none | Permission globs to pre-approve, e.g. `WebFetch(*)`. Passed to Claude Code via a generated per-agent `--settings` file. |
| `auto_accept` | bool | `false` | Auto-accept permission prompts through the PreToolUse hook. Use with care. |
| `bash_preview` | bool | inherit | Overrides the global `bash_preview`. |
| `auto_reclaim_worktree` | bool | `true` | On a terminal session end, remove this agent's worktree and window if it is empty or landed. Set `false` to opt out; `git worktree prune` and `agentctl cleanup` are unaffected. |
| `work_journal` | bool | inherit | Enable journaling for this agent (needs `llm.provider`). |
| `backend` | string | none | Name of a `backends` entry to launch this agent against instead of Anthropic. Global-only. |
| `account` | string | none | Name of an `accounts` entry this agent launches under. Global-only. |
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

`agentctl dispatch --backend <name>` overrides the agent's backend; `--backend none` forces Anthropic. `agentctl launch` takes the same `--backend`, plus `--model` and `--effort` (`inherit` clears the agent's configured value). The prefix+a popup is filter-first: just type to filter (name prefix ranks above name substring above description/model/backend), `↑↓`/`ctrl+p`/`ctrl+n` move, `enter` launches with the agent's defaults, `tab` opens an options sheet (backend, model, effort, dir, worktree; `ctrl+u` resets to the agent's defaults), `ctrl+r` resumes a recent session, `ctrl+g` jumps to the most recently active running instance, `ctrl+u` clears the filter and `esc` clears the filter, then closes. `space` on Model opens a fuzzy model picker (type any valid model id to use it as typed). On Anthropic it lists the aliases (`opus`, `sonnet`, `haiku`, `fable`) and then every full model id from the built-in registry (plus `claude-*` keys in `models.overrides`) with its price. Ollama model lists are fetched live from the backend's `/api/tags` (2s timeout) and Nous lists come from the cached catalog. Dispatch, improve, resume, and `agentctl restore` all apply the backend. Restore re-resolves the backend name saved with the window; if it no longer exists in config the window is skipped (logged), never relaunched on Anthropic. `agentctl status <window>` and `--json` show the backend.

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
- Ollama sessions are `free`: `agentctl cost` prices them at $0 (billing class `free`), and cost metrics skip unknown models.

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

**How the key reaches Claude Code.** The key never appears in tmux argv or env, launch scripts, logs, windowstate, `state.json`, or config. Claude Code gets it by running its `apiKeyHelper`, which agentctl points at the hidden `agentctl backend-token <name>` command (it prints exactly the key and a newline). The helper and `ANTHROPIC_BASE_URL` always travel together in the `--settings` file (named `<agent>@<backend>.json`) (the primary delivery, sufficient on its own, including outside a worktree) and, for worktree launches, as an additional copy in the worktree's `.claude/settings.local.json` (the main checkout's settings file is never written, so your own sessions are not rerouted); the file also blanks `ANTHROPIC_API_KEY` and `ANTHROPIC_AUTH_TOKEN` so a lower settings layer cannot outrank the helper. The launch script unsets `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN` and `CLAUDE_CODE_OAUTH_TOKEN`. A restore, resume, or `--backend none` launch updates only the agentctl-owned keys of the worktree file (hooks, `apiKeyHelper`, the three env keys, agentctl's deny rules) and keeps everything else, such as permission rules Claude Code saved there, so Nous routing never lingers.

The `ANTHROPIC_AUTH_TOKEN` sentence above applies to the Ollama kind only; a nous launch sets no `ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN` or `ANTHROPIC_API_KEY` in the window env. It does set the alias model variables, `CLAUDE_CODE_SUBAGENT_MODEL`, `CLAUDE_CODE_MAX_CONTEXT_TOKENS` and `CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS=1`.

**Launch checks.** A nous launch fails, with the fix named, when:

- an explicit `--model` is not a slug (no `/`): otherwise a Claude model name would silently run on your Anthropic subscription.
- `GET <base_url>/v1/models` (3s timeout, cached in the runtime dir for 10 minutes) is unreachable, or `model`, an explicit `--model`, or any `models.*` entry is missing. Up to 10 ids with the same prefix are suggested. A model whose `endpoints` do not list `messages` only logs a warning, since that field is unreliable.
- No key is available (`agentctl backend login <name>`, or `api_key_cmd` configured). Launch only checks availability: `api_key_cmd` is not run and no request is made with the key.
- Any settings layer (managed settings, `~/.claude/settings.json`, `.claude/settings{,.local}.json` in the working directory and its parents) sets a non-empty `ANTHROPIC_API_KEY` or `ANTHROPIC_AUTH_TOKEN` in `env`; the error names the file.

Caveats:

- **Billing.** API keys bill pay-as-you-go credits, not any subscription's included usage. Rate limits depend on your Nous tier; check the Portal for current limits.
- **Verified free models.** `meituan/longcat-2.5:free` works for tool use over `/v1/messages` (verified end to end with Claude Code doing Read, Bash and Write). `stepfun/step-3.7-flash:free` and `upstage/solar-mini4:free` return `400 missing tags` and are unusable.
- **Edge blocking.** Nous sits behind Cloudflare, which returns a 403 HTML page for Go's default User-Agent; agentctl sends `User-Agent: agentctl/<version>` on every request to Nous. `backend login` reports a non-JSON 403 as blocked by the provider edge (WAF), not an invalid key, and an `insufficient_credits_for_paid_model` error as a valid key on an account with no credits (add credits or use a `:free` model).
- **Models.** Only models verified to do tool use over `/v1/messages` are supported; the catalog's `endpoints` field is copied from OpenRouter and may be wrong. `anthropic/*` models are the safest bet. Prompt caching behaviour under concurrent agents is unverified, so cost may be higher than the token counts suggest.
- **The agent can read its own key.** It runs as your user, so it can run `agentctl backend-token`, `security find-generic-password`, or read the credential file. The `Bash(*backend-token*)` and `Edit(**/.claude/settings*.json)` deny rule that nous launches add are defence in depth only. Set a spend limit on the Nous key.
- **Cost.** Nous sessions are priced from the catalog (`agentctl cost`, billing class `metered`). The statusline hides Claude Code's pushed cost for non-Claude models because Claude Code prices them with Claude rates.

## Accounts (multiple Claude logins)

Each account is one Claude Code config dir (`CLAUDE_CONFIG_DIR`) with its own login. Claude Code runs the login (`claude auth login`) and stores and refreshes the token itself, keyed per dir; agentctl only chooses which dir a window gets and never reads, copies or passes credentials. Design: [design/accounts.md](design/accounts.md).

```json
{
  "accounts": {
    "ll":       {"config_dir": "~/.claude",          "label": "Lincoln Loop (Team)"},
    "personal": {"config_dir": "~/.claude-personal", "label": "Personal (Max)"}
  },
  "default_account": "ll",
  "account_rules": [
    {"path": "~/code/ll/**",       "account": "ll"},
    {"path": "~/code/personal/**", "account": "personal"}
  ],
  "agents": {"brandon": {"account": "personal"}}
}
```

- `accounts.<name>.config_dir` must be absolute or start with `~/`. Names use letters, digits, `.`, `_` and `-`.
- `account_rules[].path` is matched against the launch's **repo root** (a worktree resolves to its main repo root, so worktrees inherit). `<dir>/**` and a plain directory match that directory and everything below it; other patterns are `filepath.Match` globs on the whole root. The longest matching path wins.
- **Resolution order** at launch: `--account` flag > `agents.<name>.account` > `account_rules` > the dispatching window's account > `default_account` > the launching process's own dir (`$CLAUDE_CONFIG_DIR`, else `~/.claude`). `default_account` only picks where a new launch goes: a running window (its hooks, statusline, transcripts and rate limits) is always attributed to the dir it actually uses, `$CLAUDE_CONFIG_DIR` when set, else `~/.claude` and whichever account is configured there. A name that is not configured fails the launch; there is never a fallback to another account, and agentctl never switches accounts on its own (for example when one hits its usage limit).
- All four keys are **global-only**: a project-local `.agentctl/config.json` cannot route a repo onto another account.

With accounts configured, every launch (dispatch, bare launch, cron, restore, resume, improvement sessions) sets an absolute `CLAUDE_CONFIG_DIR` and unsets inherited `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN` and `CLAUDE_CODE_OAUTH_TOKEN` (they outrank the per-dir login) unless the launch's backend sets them. The account is recorded in windowstate and `restore.json`, so restore relaunches under the same login. With no accounts configured nothing changes: windows inherit the environment as before. An account whose dir is Claude Code's default `~/.claude` is the exception: it launches with `CLAUDE_CONFIG_DIR` unset (and inherited credentials still scrubbed), because Claude Code treats an explicit `CLAUDE_CONFIG_DIR=~/.claude` as a separate login with its own keychain entry and its own `.claude.json`, so setting it would start that window logged out; the account is still recorded via `AGENTCTL_ACCOUNT`.

Accounts are isolated on purpose. They share no `sessions/` registry, so sessions under different accounts cannot `SendMessage` each other. A worker dispatched under a different account than its dispatcher gets no "report to your orchestrator" step; the dispatcher still gets agentctl's own `agentctl notice:` done/blocked lines, and the approval popup shows the resolved account.

Rate limits (5h/7d) are per login: the statusline records them in `~/.local/state/agentctl/ratelimits-<account>.json` (the pre-accounts `ratelimits.json` is still read for the default account for one release).

Commands:

```
agentctl account add personal --config-dir ~/.claude-personal [--label "Personal (Max)"]
agentctl account list      # name, dir, label, login identity, 5h/7d usage
agentctl account doctor    # shared links intact, logged in, stray credential env vars
agentctl account rm personal   # unregister only; never deletes the dir
agentctl dispatch --issue 12 --account personal
```

`account add` creates the dir (0700), symlinks the shared allowlist from `~/.claude` (`agents/`, `shared/`, `skills/`, `commands/`, `agent-memory/`, `CLAUDE.md`, `settings.json`, `hooks/`, `plugins/`; entries that do not exist are skipped, and an existing non-symlink file is never overwritten), registers the account, then prints `CLAUDE_CONFIG_DIR=<dir> claude auth login` (plain `claude auth login` for `~/.claude`, which is also never linked into itself) and, inside tmux, opens it in a popup. Credentials, `projects/`, `sessions/`, history and org policy files stay private to each dir.

## Environment variables

| Variable | Effect |
|---|---|
| `XDG_CONFIG_HOME` | Moves the global config directory (default `~/.config`). |
| `CLAUDE_CONFIG_DIR` | The Claude Code config dir. With no `accounts` configured it is the default dir agentctl reads transcripts from (else `~/.claude`); with accounts, launches set it per window. |
| `AGENTCTL_ACCOUNT` | Set in a launched window to the account it runs under (recorded in windowstate). |
| `AGENTCTL_REPO` | Set by dispatch in a dispatched agent's environment, for its information only. It is never read to resolve the repo; use `agentctl dispatch --repo owner/name` for that. |
| `AGENTCTL_RUNTIME_DIR` | Override the `.agentctl/` runtime directory. |
