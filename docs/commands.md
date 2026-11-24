# Commands

Every user-facing `agentctl` subcommand, grouped by job. Run `agentctl <command> --help` for the full flag list; this page says what each one is for and where it is documented. Hidden internal commands (hook handlers, popup helpers, `reconcile`, `dispatch-complete`, …) are not listed.

## Session

| Command | What it does |
|---|---|
| `agentctl` | Start (or attach to) the tmux session for the current project, `agentctl-<dir>`. |
| `agentctl quit` | Quit the agentctl session (saves `restore.json` first). |
| `agentctl restart` | Kill the session so the next `agentctl` relaunches it; inside tmux, prefix+`R` does the same with a confirm. |
| `agentctl apply-config` | Re-apply tmux hooks, options and keybindings to the running session (no restart). Also switches existing windows when `chrome_bar` changes. |
| `agentctl health` | Health checks: hooks, monitor (or "monitor disabled"), metrics serve, cron ticker, backends out of credits, agent `tools:` lists that contradict their role. |
| `agentctl version` | Print the version. |

## Agents and windows

| Command | What it does |
|---|---|
| `agentctl launch [<name>]` | Launch an agent in a new window. Flags: `--no-worktree`, `--model`, `--effort`, `--backend`, `--account`, `--harness`, `--permission-mode`, `--background`. prefix+`a` is the interactive launcher. |
| `agentctl dispatch` | Launch an agent on a GitHub issue (`--issue N`), an ad-hoc prompt (`--prompt`, `--prompt-key`) or a PR review (`--review-pr N`). See [dispatch.md](dispatch.md). |
| `agentctl kill <name>` | Kill an agent window (and, with `close_children`, the windows it dispatched). |
| `agentctl logs <name>` | Formatted session log for an agent, in `less` (prefix+`o`). |
| `agentctl improve` | Launch a self-improvement session (no editor step) that rewrites an agent's instructions from short summaries of its recent sessions. Runs in the agent's configured dir, else the directory you launched it from. Only runs when you start it. |
| `agentctl cleanup` | Reclaim finished worktrees under `.worktrees/` (`--dry-run`, `--yes`, `--include-open-prs`, `--json`). See [dispatch.md](dispatch.md#cleanup). |
| `agentctl sweep` | Close finished windows in the current tmux session and list the rest (`--dry-run`, `--include-bare`, `--session`, `--json`). See [dispatch.md](dispatch.md#cleanup). |
| `agentctl agents` | List agents (`--tui` browser, `--json`). |
| `agentctl agents history [name]` | Change history of `~/.claude/agents` and `~/.claude/shared` (a hidden git store under the state dir, snapshotted by housekeeping every 5 minutes). |
| `agentctl agents diff [name] [rev]` | Compare the current files with a revision (default: last snapshot). |
| `agentctl agents revert <name\|path> [rev]` | Restore a definition; recorded as a new snapshot, never rewrites history. From an agent session it goes through the approval popup. |
| `agentctl create [<name>]` | Create a new agent definition (wizard). |
| `agentctl new <name>` | Create a new agent in the current project (`--extends <base>`). |
| `agentctl sync` | Symlink project agents into the global registry (`--dry-run`, `--clean`, `--unlink`). |
| `agentctl skills` / `agentctl memory` | List slash-command skills / memory files (CLAUDE.md); `--tui`, `--json`. |

## Watching the fleet

| Command | What it does |
|---|---|
| `agentctl hub` | Open the fleet hub, a cross-session dashboard with a live agent pane (prefix+`g`). See [keybindings.md](keybindings.md#fleet-hub). |
| `agentctl status [<window>]` | Agents across every live session, or one window's detail (`--json`, `--session`, `--state`, `--blocked`, `--verify`). `--closed [--since 7d]` lists recently closed windows instead. |
| `agentctl status peek <window>` | A window's pane contents and what they imply (`--lines`, `--json`). |
| `agentctl status whoami` | Which tmux window this process is in (`--json`). |
| `agentctl audit` (`audit log`) | Query the hook-sourced audit trail of every tool call (filters by session, agent, window, tool, project, event; `--json`). See [permissions.md](permissions.md#automatic-tool-call-audit-logging). |
| `agentctl audit report [target]` | Grade a past session (or `--all`) for safety and efficiency: scores, the findings behind them, metrics; `--json`. See [permissions.md](permissions.md#session-audit-reports). |
| `agentctl race [<target>]` | Replay a session as a race against a human typing and reading the same work. See [Race](#race). |

## Tickets and sessions

| Command | What it does |
|---|---|
| `agentctl ticket [<number>]` | List issues, or show one (`--tui`, `--json`); prefix+`t` opens the browser. Alias `tickets`. |
| `agentctl ticket create` | Create an issue (`--title`, `--body`, `--body-file`, `--labels`); prefix+`Q`. |
| `agentctl sessions [<id>]` | List Claude Code sessions, or show one (`--tui`, `--json`); prefix+`S`. |
| `agentctl sessions search <query>` | Search transcripts (`--tool`, `--include-output`, `--agent`, `--project`, `--since`, `--regex`, `--context`, `--json`). |
| `agentctl sessions summary` | Summary of a day's sessions (`--date`, `--dir`). |
| `agentctl sessions sync` | Rebuild session metadata sidecars. |
| `agentctl context` | Cross-agent context store: `set <key> <value> [--agent A]`, `get`, `list`, `clear`, `push <key> --agent A`, `--tui` (prefix+`X`). Entries stored for an agent are injected into its dispatch prompt. |

## Cost and models

| Command | What it does |
|---|---|
| `agentctl cost` | Per-session cost (`--since`, `--by agent\|model\|day\|project\|account`, `--account <name>`, `--sort cost\|date\|turns\|tokens`, `--top N`, `--metered`, `--json`), plus agentctl's own background LLM calls. Same as `cost report`. See [Pricing and cost](configuration.md#pricing-and-cost). |
| `agentctl cost prices [<filter>]` | Model prices and where each comes from (`--refresh`, `--json`). |
| `agentctl usage` | Token usage stats. |
| `agentctl models` | The model registry (context window, pricing); `--resolve <name>` shows which step matched. |

## Accounts, backends and harnesses

| Command | What it does |
|---|---|
| `agentctl account add <name> --config-dir DIR` | Create and register an account dir, then log it in (`--label`, `--no-login`). |
| `agentctl account list` / `doctor` / `rm <name>` | List accounts with login and 5h/7d usage / check shared links and stray credential env vars / unregister (never deletes the dir). |
| `agentctl backend login <name>` | Store and verify a Nous Portal API key (`--stdin`, `--no-verify`). |
| `agentctl backend logout <name>` / `status` | Remove a stored key / list backends and their credential source (`--verify`). |

See [Accounts](configuration.md#accounts-multiple-claude-logins), [Backends](configuration.md#backends-ollama-and-nous-portal) and [Harnesses](configuration.md#harnesses-claude-code-and-pi). The harness is picked per launch with `--harness` on `launch` and `dispatch`.

## Scheduled runs

| Command | What it does |
|---|---|
| `agentctl cron add <name>` | Add a job: `--schedule`, `--agent`, `--dir`, one of `--prompt`/`--prompt-key`/`--prompt-file`/`--template` (`--agent` optional with a template), plus `--no-worktree`, `--model`, `--effort`, `--backend`, `--account`, `--timeout`, `--overlap`, `--catchup`. |
| `agentctl cron edit <name>` | Change only the given fields. |
| `agentctl cron list` | Jobs with next fire, last result and throttles. |
| `agentctl cron runs [<name>]` | Run history (`-n`). |
| `agentctl cron run <name>` | Fire a job now; `--force` ignores overlap, `max_concurrent` and the usage gate. |
| `agentctl cron pause` / `resume` / `rm <name>` | Pause, resume (missed fires are not caught up) or remove a job (history kept). |
| `agentctl cron install` / `uninstall` | Install or remove the launchd ticker (prints a crontab line on Linux). |

See [cron](configuration.md#cron) for the config keys, approval rules and the hub's Schedules tabs, and [design/cron.md](design/cron.md) for the design.

## Artifacts

`agentctl artifact` publishes a report, table, chart or mockup an agent wants to show the human, and prints a direct link to open it, `https://<host>:<port>/a/<id>/latest/` (built from `serve.listen` and the first `serve.hostnames` entry, else `<hostname>.local`), then the phone app's viewer link `app: https://<host>:<port>/#/artifacts/<id>`. The direct link needs no pairing: the 26-character random id is the credential, so it opens in any browser that can reach serve (the `allow_cidrs` networks, e.g. the LAN or VPN), and anyone who has the link can view the artifact until it is removed with `artifact rm`. Artifacts published before this change have 10-character ids; their direct link still needs a paired browser. `--json` has both, as `url` and `app_url`. The store is `<state dir>/artifacts/` (0700/0600); this command is its only writer, and direct Bash, Write and Edit access to it is denied to agents. Serve being down never fails a command: it says so on stderr and prints the URL it will have.

| Command | What it does |
|---|---|
| `agentctl artifact add <path\|dir\|->` | Publish a file, a directory (opens `index.html`, or `--entry`) or stdin (`-`, needs `--entry name.ext` or `--kind`). `--title`, `--description`, `--kind html\|markdown\|image\|text`, `--pin`, `--no-notify`, `--json`. Kind comes from the entry's extension; an unknown extension is text only if it is valid UTF-8. The title defaults to the HTML `<title>`, the first markdown `# heading`, else the file name. |
| `agentctl artifact update <id> <path\|dir\|->` | Add revision n+1 (`--note`, `--title`, `--json`). The latest is the default view; at most 20 revisions are kept, oldest dropped. |
| `agentctl artifact list` | The current project's artifacts (`--all` for every project, `--project`, `--agent`, `--since 7d`, `--json`). |
| `agentctl artifact show <id>` | Metadata, revisions and URLs (`--json`). |
| `agentctl artifact comments <id>` | Feedback sent from the viewer (`--json`): revision, time, device, delivery status and text. `show` lists them too. |
| `agentctl artifact url <id>` / `open <id>` | Print the direct link, or open it in the laptop browser (`--rev N` for `/a/<id>/<N>/`). No pairing needed, except for a legacy 10-character id. |
| `agentctl artifact pin` / `unpin <id>` | Pinned artifacts are never pruned. |
| `agentctl artifact rm <id>` | Delete it (`--yes` to skip the prompt; required when not on a terminal). |
| `agentctl artifact prune` | Delete unpinned artifacts not updated for `--older-than` (default 30d; `--dry-run` lists them). `reconcile --housekeeping` runs the same 30-day prune. |

Limits: 25 MB per revision, 500 files per directory, 1 GB for the store (`add` then fails, naming `artifact rm` and `prune`). Content is copied, never symlinked; a symlink inside a directory, a `..` entry, or a non-UTF-8 file with an unknown extension is refused. Use relative paths inside an HTML artifact (it is served under `/a/<id>/<rev>/`). Origin (agent, window, session, branch) comes from the calling window through `TMUX_PANE`; a plain shell records only the project, from the cwd's worktree-aware root. `agentctl serve` serves them under `/a/<id>/<rev>/` in a sandbox and pushes a notification for a new one (not for updates; `--no-notify` skips it), which opens the phone app's viewer. See [design/artifacts.md](design/artifacts.md).

## Phone web app and metrics

| Command | What it does |
|---|---|
| `agentctl serve` | Run the HTTPS server for the mobile web app in the foreground (what launchd runs). |
| `agentctl serve install` / `uninstall` | Install or remove the LaunchAgent (prints a systemd unit on Linux). |
| `agentctl serve start` / `stop` / `toggle` | Turn phone access on or off (push notifications, phone approvals and the phone app); serve keeps running for artifact links. `start` also starts the installed LaunchAgent when serve is down. prefix+`W` and the hub's `S` toggle it. |
| `agentctl serve stop --all` | Stop the server itself (artifact links too) until the next login; the LaunchAgent stays installed. |
| `agentctl serve status` | Whether it runs, its URL, devices, cert expiry and CA fingerprint. |
| `agentctl serve pair` | Open a 5-minute pairing window and print QR codes (`--name`). |
| `agentctl serve devices [rm <device>]` | List or revoke paired devices. |
| `agentctl serve logs` | Tail `serve.log` or, with `--audit`, `audit.jsonl` (`-n`, `--follow`). |
| `agentctl serve ca rotate` | Replace the CA and server certificate; every device pairs again. |
| `agentctl metrics serve` | The per-machine Prometheus process (started automatically when `metrics.enabled`). |
| `agentctl dashboards list` / `export` / `install` | Bundled Grafana dashboards. |

Starting, stopping and pairing serve are for humans: agents are denied them. See [serve](configuration.md#serve) and [design/mobile-web-app.md](design/mobile-web-app.md).

## Setup

| Command | What it does |
|---|---|
| `agentctl init` | Initialize agentctl in the current project. |
| `agentctl hooks-install` | Install Claude Code hooks into `.claude/settings.local.json` (`--global` for `~/.claude/settings.json`). See [claude-code-hooks.md](claude-code-hooks.md#agentctl-hook-configuration). |
| `agentctl statusline install` | Make agentctl Claude Code's status line (`--force`, `--uninstall`). See [statusline_format](configuration.md#statusline_format-and-statusline_align). |
| `agentctl install-templates` | Install starter agents: architect, devops-engineer, go-engineer, python-engineer, terraform-engineer (`--force`). |
| `agentctl ctl <restart\|quit>` | Let an agent surface the restart or quit confirm popup on the operator's window (`--async` to not wait). |

## Race

`agentctl race` replays a session's transcript as a race between the agent and a person doing the same typing and reading by hand.

```bash
agentctl race                       # this window's session (or: @N, a session ID, a .jsonl path)
agentctl race @12 --wpm 80 --think 5s
agentctl race @12 --real-life --focus-hours 6 --days-per-week 5   # calendar time too
agentctl race <session-id> --json   # totals only: steps, tool calls, chars typed/read, agent vs human time
```

The agent's time comes from the transcript's timestamps, with any gap over `--max-gap` (default 60s) capped and time spent waiting on your next prompt not counted. The human's time is the characters they would type (commands, new code, prompts, messages; only the pattern or path for Read/Grep/Glob) at `--wpm` (60) plus the tool output they would read at `--read-wpm` (250), plus `--think` per step (0s by default). It is not a bound either way: the person never pauses or makes a typo, but they also read every tool result in full and type the agent's own messages. The gap cap also clips long tool runs and Task sub-agents, which makes the agent look faster. The replay is compressed to `--duration` (45s), or runs at `--speed N` times real time; both racers share the clock. Keys: space pause, `+`/`-` speed, `r` restart, `e` end card, `q` quit.

`--real-life` (off by default) adds what the nonstop number means on a calendar: a person starting Monday 09:00 works `--focus-hours` (default 6, max 16; the real keyboard time left after meetings, Slack and context switches) a day on `--days-per-week` (default 5) days, with a 1h lunch and two 15m coffee breaks, then sleeps 8h. The end card gains a "Real life" line (finish time, nights asleep, weekends, lunch and coffee) and a calendar-time multiplier, and `--json` gains a `real_life` object. The big multiplier stays the nonstop one. `--think` is added to the effort before the conversion.
