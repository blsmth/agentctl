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
| State | `$XDG_STATE_HOME/agentctl` (default `~/.local/state/agentctl`) | `registry/` (live sessions), `ratelimits.json`, `hub/` (hub session working directory), `serve/` (`agentctl serve` certificates, devices, log), `cron/` (`agentctl cron` tick lock, state, run history, log), `titles/` (hourly request cap for short row titles) |

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

`backends`, `titles`, `accounts`, `default_account`, `account_rules`, `agents.<name>.account` and `agents.<name>.harness` are global-only for the same reason: they decide where prompts go, which login pays for them, and (for `harness`) which approval layer an agent runs under.

Rationale: a project-local file can arrive with a cloned repository. Executing commands named by an untrusted repo would be remote code execution. See [SECURITY.md](../SECURITY.md).

## Top-level keys

| Key | Type | Default | Description |
|---|---|---|---|
| `notify_cmd` | string | platform default | Desktop-notification command template. See [Notifications](#notifications). |
| `open_pr_in_browser` | bool | `true` | Open a dispatched agent's PR in the browser (`gh pr view --web`) when it finishes. Each PR opens once per monitor process. |
| `monitor` | bool | `true` | Kill switch for the per-project monitor. When `false`, session start and the hook watchdog do not start or replace a monitor and `agentctl health` says "monitor disabled". A running monitor is not killed; the next relaunch just does not start one. Everything except the macapp's `state.json` feed works without it. Global-only in the enabling direction: a project-local `"monitor": false` switches it off for that project, but a project can never switch it back on. |
| `close_children` | bool | `true` | When an agent window closes (any way: kill popup, `agentctl kill`, the hub or phone close, `/exit`, sweep, closing the tmux window by hand), also close the live windows it dispatched, and theirs recursively, in any project session. Children are matched on both `spawned_by` and `spawned_by_session`; cron, phone-launched and other detached windows are never closed this way. Nothing cascades when the whole session goes away (session kill, relaunch, restart, tmux server death) or the window was saved for a restore. Closing a window never deletes its worktree. `false` leaves dispatched windows running. Global config only. |
| `hub_chrome` | bool | `false` | Show each agent's chrome bar inside the hub's live pane. When `false` the hub zooms the viewed window's main pane, which hides the bar there (the hub list already shows every agent); the zoom is undone when the hub moves to another row, closes, or you switch to the project session. Project sessions attached directly always show the bar. The hub key `c` toggles it for the running hub. Global config only. |
| `bash_preview` | bool | `false` | Show the human approval popup before **every** Bash tool call, not just `agentctl dispatch` and `agentctl cron add/resume/run --force`. See [bash_preview](#bash_preview). |
| `dispatch_approval` | string | `non-review` | Which `agentctl dispatch` commands need the approval popup: `all`, `non-review` (all except PR review dispatches), or `off`. Global-only in the loosening direction. See [dispatch_approval](#dispatch_approval). |
| `statusline_mobile_format` | string | `mobile` | Statusline format used while the phone web app has a live terminal or the window detail screen open on the window and no laptop tmux client is looking at it. Same values as `statusline_format`; unknown values fall back to `mobile`. Set it equal to `statusline_format` to disable the switch. See [statusline_format and statusline_align](#statusline_format-and-statusline_align). |
| `statusline_format` | string | `rich` | What `agentctl statusline` prints: `rich`, `complementary`, `full`, `mobile`, or `minimal`. Unknown values fall back to `rich`. |
| `statusline_align` | string | `left` | `left` or `right`. Right-aligns the `rich` format's lines. Unknown values fall back to `left`. |
| `work_journal` | bool | `false` | Journal a session summary on session end for all agents (needs `llm.provider`). Per-agent values can also enable it. |
| `llm` | object | disabled | Local model provider. See [llm](#llm). |
| `titles` | object | on with a `nous` backend | Short AI row titles in the hub and the phone app, generated through a Nous Portal backend. Global-only. See [titles](#titles). |
| `default_agent` | string | first discovered agent | Agent launched by the quick-launch keybinding. A project-local value overrides the global one only when it passes the safety checks in [permissions.md](permissions.md) (otherwise the global value is used, with a warning); ignored with a warning if it is not a discovered agent. |
| `theme` | string | `catppuccin-mocha` | Color theme name. |
| `auto_poll` | bool | `false` | Ticket-browser autopilot mode: every `auto_poll_interval` the open ticket browser dispatches open issues labelled `ready` (skipping `in-progress`). Global config only. Toggle it with `P` or the header pill in the browser, which asks before turning it on. |
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

By default agentctl only gates `agentctl dispatch` (per [dispatch_approval](#dispatch_approval)) and the `agentctl cron` subcommands that authorize scheduled runs (`cron add`, `cron resume`, `cron run --force`; see [cron](#cron)) behind the approval popup. With `bash_preview: true` every Bash tool call is previewed. The popup only appears when a human is attached to the tmux session **and** is looking at the exact window that issued the command. Otherwise the command falls through to Claude Code's normal permission flow, so an unattended dispatched agent can never deadlock on an invisible popup.

```json
{
  "bash_preview": true,
  "agents": { "go-engineer": { "bash_preview": false } }
}
```

A per-agent value overrides the global one.

### dispatch_approval

Controls which `agentctl dispatch` Bash commands an agent must get approved in the popup (and, with `agentctl serve` running, on the phone). It is independent of `bash_preview`.

| Value | Effect |
|---|---|
| `all` | Every dispatch is gated. |
| `non-review` (default) | Every dispatch is gated except PR reviews run by `code-reviewer`: a literal positive `--review-pr N` (or `--review-pr=N`; the last one wins) with no `--agent`/`-a` (the `--review-pr` default is `code-reviewer`) or with a last `--agent code-reviewer`, `--agent=code-reviewer` or `-a code-reviewer`. The exempt form may also carry only `--author`, `--prompt`/`--prompt-key` (review focus), `--repo`, `--model`, `--wait` and `--timeout`. Any other flag (`--issue`, `--dir`, `--backend`, `--account`, `--harness`, `--no-worktree`), a review with any other agent, a shell-expanded agent or flag, or an unquoted expanding value is gated. Quoting a flag does not hide it. When one Bash command holds several dispatches, the popup is skipped only if every one of them is exempt; one gated dispatch keeps the gate for the whole command. The exemption holds only for the global `code-reviewer`; see [Only the global reviewer is exempt](#only-the-global-reviewer-is-exempt). |
| `off` | No dispatch is gated. |

An unrecognized value is treated as `all` and logged as a config warning. A dispatch that is not gated is handled like any other Bash command: with `bash_preview` on it still gets the generic Bash popup, otherwise it runs through Claude Code's normal permission flow with no blocked state or approval marker. The global and project deny lists still run first, and a single Bash command that mixes `agentctl dispatch` with a gated `agentctl cron` call is still denied. The `agentctl cron` gate does not depend on this setting.

A project-local `.agentctl/config.json` may tighten the setting (strictness `all` > `non-review` > `off`) but never loosen it: a looser project value is dropped with a warning, so a project `"off"` over an unset global stays `non-review`, and a project `"all"` over a global `"off"` gives `all`.

The global setting is human-only. `hook-pre-tool` computes the result of an agent's `Write`/`Edit`/`MultiEdit` on the global config file (`<XDG_CONFIG_HOME or ~/.config>/agentctl/config.json`) and denies it when the resulting `dispatch_approval` is less strict than the current file's (unset counts as `non-review`, an unknown value as `all`); edits to other keys, and edits that keep or tighten the setting, pass. A result that does not parse is denied if it mentions `dispatch_approval`, and otherwise judged as unset. In Bash the file is protected like the cron files: redirects, `tee`, in-place `sed`/`perl`, `cp`/`mv` and interpreters naming it are denied, as is any command that mentions `dispatch_approval` together with the file or its directory; reads (`cat`, `grep`, `jq` without a redirect onto it) pass. Arguments of `agentctl` commands (a `context set` value, a dispatch prompt) are text and are not inspected, unless the command uses shell syntax the scanner does not model (a command substitution, a `#` comment, `$'...'`, `${...}`, an unterminated quote); then every word is checked. `Write`/`Edit` match the file through symlinks and, on macOS, case. The project file needs no guard, since it can only tighten. Like the cron rules this is a speed bump, not a sandbox.

#### Only the global reviewer is exempt

The exemption trusts the name `code-reviewer`, but agent discovery lets a project-local `.claude/agents/code-reviewer.md` shadow the global one. So a review skips the popup only when `code-reviewer` resolves to the global definition: a regular file directly in `~/.claude/agents/` (that directory may itself be a symlink, as each account's `agents` dir is to it; files are compared with `os.SameFile`). A project-local file, a registry entry that is a symlink (what `agentctl sync` creates), or a global `code-reviewer` that `extends` a project-local base all count as local.

Two places enforce this:

- **The hook** (`hook-validate-bash`) gates an otherwise exempt review when `code-reviewer` does not resolve to the global file from the Bash tool's current directory, or when anything other than an `agentctl` command runs before the dispatch in the same line (a `cd`, `env`, `VAR=value`, a keyword such as `!`, `time` or `if`, a wrapper such as `nohup` or `nice`, a subshell or group, a shell-expanded command word), since the hook cannot see what those do to the directory or environment. Commands after the dispatch (`| tail`, `&& echo`) do not matter. The popup then shows as usual.
- **The CLI** (`dispatch --review-pr`) is the backstop, because only it knows the directory it really runs in. When `dispatch_approval` is not `off`, the caller is an agent session (a tracked Claude session, the same test that gives a worker an orchestrator) and `code-reviewer` resolves to a local definition (from `--dir` or the agent dir, the current directory, and again from the reviewer's worktree once it is created), the launch needs a recorded human approval. Without one it is refused, and the message tells the agent to rerun the command as its own Bash call from the directory it should run in, which shows the popup. A person running `agentctl dispatch` in a plain shell is never refused, and the global reviewer is unaffected.

When a human approves a dispatch popup (on the laptop or the phone), the hook records one approval per dispatch in the command under `<XDG_STATE_HOME or ~/.local/state>/agentctl/dispatch-approvals/` (directory 0700, files 0600). Each is keyed by the issuing window and a SHA-256 of the dispatch's arguments, is consumed by the first matching `--review-pr` launch from an agent session (whether or not its reviewer turned out to need it) and expires after 2 minutes; a dispatch whose arguments the shell expands (`$(...)`, `$VAR`, globs) gets no record. Agents cannot write that directory: the Bash validator denies redirects, write commands, in-place edits and interpreters naming `dispatch-approvals`, and `hook-pre-tool` denies `Write`/`Edit`/`MultiEdit`/`NotebookEdit` inside it. Like the cron rules this is a speed bump, not a sandbox.

### statusline_format and statusline_align

`agentctl statusline` is the data source for Claude Code's status line (`agentctl statusline install` wires it up).

| Format | Output |
|---|---|
| `rich` (default) | Colored, up to three lines: account rate limits; session metrics (model, context %, tokens, cache hit rate, turns, cost, duration); workspace (directory, branch, dirty count, worktree, PR). |
| `complementary` | Account-level figures only, e.g. `5h 12% · 7d 9% · $13.43`. Absent components are omitted; if all are absent it prints `-`. |
| `full` | Model, context %, and agent name. |
| `mobile` | Phone-width, up to two lines: `model · <battery> N% ctx · session length` and `5h N% · 7d N% · $cost · account`. Same numbers, colors and trust rules as `rich` (context bar, session length from the payload with a state-snapshot fallback; cost hidden for non-`claude-*` models and when zero). Absent values are omitted; when narrow, line 1 drops session length first and line 2 drops account, then 7d, then cost (5h is kept longest). No git branch, no subprocess. |
| `minimal` | A bare `-`. |

`statusline_align: "right"` pads the `rich` and `mobile` lines flush with the terminal's right edge.

When accounts are configured (see [Accounts](#accounts-multiple-claude-logins)), every format except `minimal` shows the name of the account the session runs under (`$CLAUDE_CONFIG_DIR`, else `~/.claude`), the same one whose rate limits are shown: first on the `rich` rate-limit line (on the workspace line when there are no rate limits), dropped before the 5h figure when the line is narrow. With no accounts, or a dir that matches none, nothing is added.

#### Phone format

A window has one statusline, so the laptop and the phone see the same output. While `agentctl serve` has a live terminal open on a window, or the phone's window detail screen is open on it (that screen polls the pane; its marker expires 10 seconds after the last poll, so a phone that drops without closing stops counting on its own), **and** no laptop tmux client (any session, including the hub; not the phone's own viewer) is actively showing that window, the statusline renders `statusline_mobile_format` (default `mobile`: two short lines with cost, rate limits and branch) instead of `statusline_format`. A client counts as active when it had input within the last 2 minutes (the hub's nested viewer is judged by its own input; the hub's own window switching does not refresh it), so a laptop that is asleep or locked with tmux still attached does not block the phone format. It switches back within about a second of the phone disconnecting or the laptop looking at the window again (Claude Code refreshes the statusline every second). Set `statusline_mobile_format` to the same value as `statusline_format` to turn this off. If tmux cannot be queried, the normal format is used.

Serve records open terminals as marker files under `<state dir>/phone-viewed/<window id>/`, one per connection; the statusline only runs `tmux list-clients` when such a marker exists.

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

### titles

A row's recap (Claude Code's away summary, or an ad-hoc task prompt) is often a paragraph. With `titles` on, the hub list row and the phone fleet row show a 2-5 word title instead ("Organizing your Photos", "Fixing Hub Sort Order"). The full recap stays where it was: the hub's selected-row detail lines and the phone's window view, and the serve JSON keeps it in `recap` next to the new `title`. A row whose recap is still its ticket (`#N title`) or Claude Code's AI title is short already and keeps it; once an away_summary takes over the recap, the generated title is shown instead.

| Key | Default | Description |
|---|---|---|
| `titles.enabled` | `true` when a `nous`-kind backend exists | `false` turns the feature off entirely (no requests, no writes). |
| `titles.backend` | first `nous`-kind backend, by name | Backend to call. Must be `kind: "nous"`; anything else disables titles. |
| `titles.model` | the backend's `models.haiku`, else its `model` | Model id sent to `/v1/chat/completions`. |

Titles are made by `agentctl hook-title`, registered as an **async** hook (Claude Code never waits on it) next to the existing handlers on `Stop`, `UserPromptSubmit` and `SessionStart`. It builds its input from what exists at that moment: the ad-hoc task line (or the ticket title), for a bare window with neither, its first prompt (below), and, for the one refinement, the first Stop's last assistant message (the tail, about 1500 characters). The result goes into the window's windowstate record (`short_title`), which the hub, `agentctl status` and `agentctl serve` already read, so readers make no network call and read no extra files.

A bare (interactive) window has no task, so it is titled from its first prompt: on its first `UserPromptSubmit` the hook makes the request in that same run. The prompt is stored (about 500 characters) in windowstate `first_prompt`, once, and stays an input for later retitles; it is restored with the window and never replaces `task`, which drives the recap row. Slash commands, blank prompts and prompts made only of `agentctl notice:` lines or `<task-notification>`/`<system-reminder>` blocks are not requests and are ignored; dispatched windows keep using their task or ticket. Prompt text is never logged. The title shows on the next refresh of the hub (1s poll), the chrome bar (fsnotify, 100ms debounce) and the phone (200ms debounce) after the request returns, so a second or two after the prompt. Rows with no recap yet, or only an activity line, show it.

A window gets at most two titles, then the title is frozen. The first comes from its task or ticket (dispatched window) or its first real prompt (bare window). The second is one refinement at the first `Stop` that carries a last assistant message, made from the task or first prompt plus that response; it sets `final` in `short_title`. After that nothing retitles the window: not later turns, not elapsed time, not a new away_summary (which is no longer read). A request that fails (network, HTTP error, empty or unusable reply) keeps the current title and does not freeze anything: it backs off for 10m, 30m, 2h, then 12h, and a refinement that failed is retried at a later `Stop` once the backoff has passed (from that Stop's response). Identical input is never sent twice. `/clear` (a `SessionStart` with source `clear`) resets the title and the stored first prompt so the next prompt titles the new work; resume and compact keep them. A machine-wide cap of 30 requests per rolling hour applies (`<StateDir>/titles/budget.json`). Each request times out after 15s. Before a request, the window is claimed inside its windowstate transaction, so a hook that is registered twice (the project `settings.local.json` plus a per-agent `--settings` file) still sends one request. When titles are off, or no API key is available, the hook makes no request and writes nothing. Each title also carries a **kind** tag, from the same single request: the model replies on one line as `<kind> | <title>`, with the kind one of a closed set (`review`, `go`, `python`, `swift`, `web`, `infra`, `docs`, `tests`, `migration`, `research`, `debug`, `chat`). An unknown kind, or a reply with no `|`, keeps the whole reply as the title and records no kind; a bad kind never fails the title. The window's agent name and dispatch mode are sent as hints (they are not part of the input hash, so existing windows are not retitled). A `--review-pr` window is always kind `review`. The kind is stored as `short_title.kind`, set with the title (the refinement may change it, a failed attempt keeps the old one), and shown as a small pill on the phone fleet row (the hub list does not draw it), and served as `kind` next to `title`. The tag shows on dispatched windows only (any mode but bare): a window you opened yourself, including an orchestrator launched from the phone, has its kind stored but displayed and served empty. Windows frozen before this change have no kind and are not retitled. A row with no title yet shows the first sentence of its recap (or task), cut at a word boundary with `…` when it does not fit, never mid-word; the full text stays in the detail views. pi sessions do not run `hook-title`.

**Backfill.** A window that sat idle since before titles shipped never fires the hooks above, so housekeeping titles it: every 5 minutes (`<runtimeDir>/backfill-titles.stamp`), the detached `agentctl reconcile --housekeeping` (the janitor) looks at this session's live windows that have a started Claude session and no frozen title, are not under a retry backoff, and have something to title from (task, ticket, first prompt, or, once the window has finished a turn, its last response and the log's away_summary). It sends at most 3 requests per run, each with an 8s timeout, through the same claim, backoff and hourly cap as the hook, but stops while fewer than 10 of the 30 hourly requests remain so the hooks always have room. Untitled windows go first. A window that has finished a turn gets a **final** title (it describes the session so far, no later refinement); one that has not gets a normal first title, refined at its first `Stop`. Shell, `improve~`, cron and other sessions' windows are skipped, and nothing runs when titles are off, no key is available or the backend is out of credits. The backfill never spawns a process.

The key comes from the same place as for agent launches (`api_key_cmd` or `agentctl backend login`). It is never written to windowstate, the log or an error message. The `claude` CLI is never used for titles.

```json
{
  "titles": { "model": "meta-llama/llama-3.1-8b-instruct" }
}
```

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
| `serve.push.approvals` | `true` | Push when a dispatch, cron or Bash approval popup is waiting and can be answered from the phone (see [Remote approvals](design/mobile-web-app.md#remote-approvals)). Ignores quiet hours, since an unanswered approval blocks work; respects `muted_projects`. |
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

**Approval.** When an agent runs `agentctl cron add`, `cron resume <name>` or `cron run <name> --force` through Bash, the approval popup opens (always, regardless of `dispatch_approval`; `--help` lookups pass): job name, the schedule with its words and next fire (`30 7 * * 1-5 (weekdays 07:30), next: Wed 18 Nov 07:31`), agent, dir, account, harness, mode, worktree, timeout and the prompt. `resume` and `run --force` show the stored job. Plain `cron run` is not gated: it honors overlap, `max_concurrent` and the usage gate, so it is an early fire of an already approved job. The gate fails closed: a `--prompt`, `--dir`, name or other shown value using `$`, backticks or backslashes, an unset `--prompt-key`, an unreadable `--prompt-file`, an invalid schedule, or more than one gated cron call in one Bash command is denied with the reason. A `--prompt-key` job shows the key's current content, which can change after approval; prefer `--prompt-file` for a fixed prompt. The phone gets the same approval (kind `cron`). Agents cannot write `schedules.json` or `<state dir>/cron/` directly: the Bash validator denies redirects, write commands and interpreters that name them, and `hook-pre-tool` denies `Write`/`Edit`/`MultiEdit`/`NotebookEdit` on them. Reading them (`cat`, `jq`) is allowed; `agentctl cron list` is the intended read path. Arguments of `agentctl` commands (say, a `context set` value that mentions `schedules.json` and `tee`) are not inspected; only real shell write targets are. Like the serve rules, this is a speed bump, not a sandbox.

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
| `alert_config.blocked` | bool | `true` | Notify on Working to Blocked (needs approval, including a pending dispatch/cron/bash approval popup). |
| `alert_config.idle` | bool | `true` | Notify on Working to Idle (done). |
| `alert_config.gone` | bool | `false` | Reserved; no notification uses it yet. |
| `auto_improve` | bool | `false` | Launch a self-improvement session when a session ends. Global-only (a project may only switch it off). |
| `cross_context` | bool | `true` | Inject a cross-agent summary at launch. Global-only (a project may only switch it off). |
| `context_keys` | string[] | none | Context-store keys always injected at launch for this agent. Global-only. |
| `dir` | string | empty (portable) | Fixed working directory. Global-only. |
| `model` | string | inherit | `sonnet`, `opus`, `haiku`, or a full model name. |
| `effort_level` | string | unset | `low`, `medium`, `high`, `max`, or `auto`. |
| `permission_mode` | string | `auto` for worker dispatches; unset otherwise | Value passed to `claude --permission-mode`. Worker dispatches (`--issue`, `--prompt`, `--prompt-key`, `--review-pr`) default to `auto`; interactive modes (bare, discuss, triage, review) get no flag unless set. One of `acceptEdits`, `auto`, `bypassPermissions`, `manual`, `dontAsk`, `plan`; an unknown value logs a warning and falls back to the default. `agentctl launch --permission-mode <mode>` overrides it for a single launch (unknown values are rejected); the hub's "new" action launches with `--permission-mode auto`. |
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
| `harness` | string | `claude` | Agent program to run this agent on: `claude` (Claude Code) or `pi`. Wins over a `harness:` line in the agent definition's frontmatter; `agentctl dispatch --harness` and `agentctl launch --harness` win over both. An unknown name fails the launch. Global-only. See [Harnesses](#harnesses-claude-code-and-pi). |
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

### Agent frontmatter `role`

An agent definition can declare `role: orchestrator` (or `role: worker`, the same as leaving it out) in its frontmatter. There is no config key. An `extends` child inherits the base's role unless it sets its own; an unknown value is logged and ignored.

```markdown
---
name: brandon
role: orchestrator
---
```

An orchestrator gets the orchestrator preamble in every mode and never gets worker finishing steps (commit, push, open a PR). The phone's new-session button, or `dispatch --prompt` from a shell with no tracked Claude session, launches it as an interactive `bare` session with the prompt as its first message (no autonomous instructions, but the worker permission mode: `auto` unless overridden); cron jobs, `--prompt` dispatches from another agent session and `dispatch --issue` keep autonomous execution. See [dispatch.md](dispatch.md#orchestrator-agents).

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
- **Out of credits.** An HTTP 402 (or the `insufficient_credits_for_paid_model` error) from Nous marks the backend out of credits in `<stateDir>/backends/<name>.json` (`no_credits_since`, `last_checked`), and any later successful request clears it. While the marker is under an hour old, short titles make no requests (no budget spent, no backoff) and dispatches on that backend are refused before launch with the fix, with no paid probe per dispatch; after an hour one request probes again. The first time the marker is set you get one desktop notification (through `notify_cmd`). A `billing_error` stop inside a session on a nous backend sets the marker too and the alert reads "Nous Portal out of credits". `agentctl health` lists marked backends; `agentctl backend status --verify` rechecks (one paid one-token request) and clears the marker if the account works again.
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
- **Resolution order** at launch: `--account` flag > `agents.<name>.account` > `account_rules` > the dispatching window's account > `default_account` > the launching process's own dir (`$CLAUDE_CONFIG_DIR`, else `~/.claude`). `default_account` only picks where a new launch goes: a running window (its hooks, statusline, transcripts and rate limits) is always attributed to the dir it actually uses, `$CLAUDE_CONFIG_DIR` when set, else `~/.claude` and whichever account is configured there. Windows launched without `--permission-mode auto` run with `CLAUDE_CODE_SUBPROCESS_ENV_SCRUB=1`, which makes Claude Code drop `CLAUDE_CONFIG_DIR` from its hooks and statusline; under the scrub agentctl attributes those processes to the account in `AGENTCTL_ACCOUNT` (set by the launch, and not scrubbed) instead. A name that is not configured fails the launch; there is never a fallback to another account, and agentctl never switches accounts on its own (for example when one hits its usage limit).
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

`account add` creates the dir (0700), symlinks the shared allowlist from `~/.claude` (`agents/`, `shared/`, `skills/`, `commands/`, `CLAUDE.md`, `settings.json`, `hooks/`, `plugins/`; entries that do not exist are skipped, and an existing non-symlink file is never overwritten), registers the account, then prints `CLAUDE_CONFIG_DIR=<dir> claude auth login` (plain `claude auth login` for `~/.claude`, which is also never linked into itself) and, inside tmux, opens it in a popup. Credentials, `projects/`, `sessions/`, history and org policy files stay private to each dir. Agent memory is per account, because Claude Code only trusts its memory dir inside its own config dir.

## Harnesses (Claude Code and pi)

A harness is the terminal coding-agent program that runs in an agent's window. The default is Claude Code; [pi](https://github.com/earendil-works/pi) (`pi` CLI, 1.0+) is the second. The harness is chosen per launch, highest first:

1. `agentctl dispatch --harness <name>` or `agentctl launch --harness <name>`
2. `agents.<name>.harness` in the global config
3. `harness: <name>` in the agent definition's frontmatter
4. `claude`

Config wins over frontmatter for the same reason it does for `model`: your own config outranks a definition file that may have come with a cloned repo. The harness is recorded in windowstate and `restore.json`, so restore relaunches the window on the same harness (records without it mean `claude`).

```yaml
---
name: pi-reviewer
description: Reviews diffs on pi
harness: pi
---
```

What changes on pi:

- **Launch.** `pi --session-id <uuid> -n <name> [--model <m>] [--thinking <level>] -e <bridge> --append-system-prompt <file> [-- <first message>]`. `effort_level` maps to `--thinking` (`auto` leaves pi's default). `model` is passed as is (pi accepts `provider/id` and fuzzy patterns); the mode default model (`sonnet` for issue dispatches) is a Claude alias and is not applied. pi has no `--agent`, so the agent's full definition body, with its `@shared` includes expanded, goes into the system-prompt file with the usual preamble. Restore reopens the session with `--session-id`.
- **Hooks.** pi has no shell-hook config. agentctl writes a small dependency-free bridge extension (`<runtime dir>/launch/agentctl-pi.ts`) at every launch and loads it with `-e`. It forwards pi's `session_start`, `agent_start`, `tool_call`, `tool_result`, `agent_settled` and `session_shutdown` events to `agentctl hook-* --harness pi`, so working / idle / done, dispatch completion, the done notice to the dispatcher's mailbox, and worktree reclaim on quit behave as on Claude. `agent_settled` is the turn end; when the completion checks refuse the stop (uncommitted changes, failing build) the bridge sends the reason back as a user message, as Claude Code's Stop hook does.
- **Approvals.** pi has no permission system. The bash gate (`blocked_commands`, `blocked_patterns`, the global deny list, `bash_preview` and the dispatch and cron approval popups) runs on every pi `bash` tool call and blocks it on deny. A window is only blocked while an agentctl approval popup is open; `permission_mode` and `allowed_permissions` do not apply.
- **Backends.** `backends` work on pi too (`dispatch --backend`, `agents.<name>.backend`, restore). agentctl does not touch pi's own config: the bridge registers the backend with `pi.registerProvider` as provider `agentctl-<name>` (so it never merges with a provider of yours in `~/.pi/agent/models.json`) and pi launches with `--provider agentctl-<name> --model <model>`. Both kinds go through pi's OpenAI Chat Completions client at `<base_url>/v1`. A Nous key is fetched per request by `agentctl backend-token <name>` (the same helper Claude's `apiKeyHelper` runs), so it is never in env or a file; Ollama gets its `auth_token`. Preflight is the same as on Claude (model in the Nous catalog, a key available, Ollama reachable with the model pulled), minus the Claude settings-layer credential check, which does not apply to pi. `max_context_tokens` (or the Nous catalog's context length) sets the registered context window.
- **Not available on pi:** accounts (an explicit `--account` is an error), `SendMessage` / `ListAgents` (workers are not taught them and report through the PR; agentctl's done notice still reaches the dispatcher), the statusline, and pane classification (`status --verify` reports `unknown`). Mailbox notices addressed to a pi window are delivered through the bridge.
- **Transcripts.** pi session files (`~/.pi/agent/sessions/`, or `$PI_CODING_AGENT_SESSION_DIR`) are read in place, so the chrome bar's tokens and context %, the work journal and `agentctl cost` work. Tokens are priced through the same table as Claude sessions; a Claude model run through pi is billed `metered` (pi calls the API with its own credentials).
- **tmux.** agentctl enables `extended-keys` (and `extended-keys-format csi-u` on tmux 3.5+) on the tmux server so pi can tell Shift+Enter from Enter. Both are server options; `on` only affects programs that ask for extended keys.

## Environment variables

| Variable | Effect |
|---|---|
| `XDG_CONFIG_HOME` | Moves the global config directory (default `~/.config`). |
| `CLAUDE_CONFIG_DIR` | The Claude Code config dir. With no `accounts` configured it is the default dir agentctl reads transcripts from (else `~/.claude`); with accounts, launches set it per window. |
| `AGENTCTL_ACCOUNT` | Set in a launched window to the account it runs under (recorded in windowstate). |
| `AGENTCTL_HARNESS` | Set in a launched window to its harness when that is not `claude` (recorded in windowstate). |
| `AGENTCTL_REPO` | Set by dispatch in a dispatched agent's environment, for its information only. It is never read to resolve the repo; use `agentctl dispatch --repo owner/name` for that. |
| `AGENTCTL_RUNTIME_DIR` | Override the `.agentctl/` runtime directory. |
