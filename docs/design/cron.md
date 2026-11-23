# Scheduled agent runs (`agentctl cron`)

Status: implemented (PR #98, then editing in #239, the phone Schedules screen in #240 and the hub's Schedules section in #243). This is the design record; where it differs from what shipped, [configuration.md](../configuration.md#cron) is the reference. Companion to [multi-harness.md](multi-harness.md) (PR #88).

## Goal

Run agents on a schedule ("every weekday at 7:30, triage new issues"; "nightly, bump deps and open a PR"; "hourly, babysit open PRs") without a human at the keyboard, with the result landing where every other agentctl run lands: a tmux window with state glyphs, the hub, done detection, PR auto-open, phone push.

## Is this allowed? (Terms of Service check, 2026-11-17)

Short answer: **yes, as long as agentctl keeps launching the unmodified `claude` binary under the user's own login, for the user's own work, at a human-ish volume.** It must never touch Claude credentials or route Claude subscription usage through another harness.

What applies:

| Plan | Terms | Automation clause |
|---|---|---|
| Free / Pro / Max / Ultra | [Consumer Terms](https://www.anthropic.com/legal/consumer-terms) (eff. 2025-10-08) | §3.7 bans access "through automated or non-human means, whether through a bot, script, or otherwise", **"except when you are accessing our Services via an Anthropic API Key or where we otherwise explicitly permit it."** |
| Team / Enterprise / API | [Commercial Terms](https://www.anthropic.com/legal/commercial-terms) (eff. 2025-06-17) | No automation ban. Usage Policy applies. |

The "explicitly permit" exception covers us. Anthropic's own Claude Code docs ship and recommend unattended, scheduled use of the CLI on a subscription login:

- [`claude -p`](https://code.claude.com/docs/en/headless.md) is documented for "scripts and CI/CD". Only `--bare` mode requires an API key, which implies a normal `-p` run uses the subscription login. `--permission-prompts none` is documented for when nobody can answer prompts, "for example in a scheduled job".
- [`/loop` and `CronCreate`](https://code.claude.com/docs/en/scheduled-tasks.md) are recurring, unattended runs inside a CLI session, down to one-minute intervals.
- [Desktop scheduled tasks](https://code.claude.com/docs/en/desktop-scheduled-tasks.md) start fresh local sessions on a cron schedule. [Cloud scheduled tasks](https://code.claude.com/docs/en/routines.md) require a subscription login.

What is **not** allowed, per [Legal and compliance → Authentication and credential use](https://code.claude.com/docs/en/legal-and-compliance):

- Routing requests through Free/Pro/Max/Ultra credentials "on behalf of" other users, or offering Claude.ai login in a third-party app.
- Collecting, storing or intermediating Claude.ai credentials or session tokens. **agentctl must never read `~/.claude/.credentials.json` or the keychain entry, and must never hand a Claude OAuth token to Codex, pi, opencode or anything else.** A non-Claude harness uses its own vendor's auth, or an API key through `apiKeyHelper` as the Nous backend already does.
- Modifying the Claude Code binary (relevant only if agentctl were ever distributed with Claude Code preinstalled).

Soft limit: "Advertised usage limits for Pro, Max, and Ultra plans assume ordinary, individual usage of Claude Code and the Agent SDK", and "Anthropic reserves the right to take measures to enforce these restrictions … without prior notice." A cron that fans out dozens of Opus agents around the clock is the profile most likely to draw enforcement, even if the letter of the terms allows it. The design below therefore throttles by default (minimum interval, concurrency cap, and a skip when the 5h/7d windows run hot) instead of leaving it to the user.

The Team seat is Lincoln Loop's; Brendan also has a personal Max plan. Personal-project jobs must run on the personal account, resolved by the rules in [accounts.md](accounts.md). Each job has an `account` field, and the usage gate reads that account's own 5h/7d windows.

**Invariants the code must hold** (each gets a test):

1. Claude jobs exec the user's installed `claude` (same resolution as `dispatch`). No credential files are read, copied or passed in env.
2. `--bare` is never combined with subscription auth (bare ignores OAuth, so the run would silently fail or bill an API key).
3. Schedules live only in global config. A project-local `.agentctl/` can never create a recurring autonomous job, the same rule as `notify_cmd` and `hooks.*`.
4. Default throttles are on and visible in `cron list`.

## Why not just use Claude Code's own schedulers?

| | `/loop` / `CronCreate` | Desktop tasks | Cloud tasks | `agentctl cron` |
|---|---|---|---|---|
| Survives restarts | 7-day expiry, session-bound | yes | yes | yes |
| Local files and tools | yes | yes | no (fresh clone) | yes |
| Visible in tmux / hub / phone | only that session | no (Desktop app) | no | **yes** |
| Worktree per run, done detection, PR auto-open | no | worktree checkbox | n/a | **yes, via dispatch** |
| Other harnesses | no | no | no | **yes, via `Harness`** |

The gap is durable local scheduling that feeds agentctl's runtime. `/loop` remains the right tool for "babysit this PR for the next hour" inside a session, and the design doesn't replace it.

## Design

### 1. Jobs: `~/.config/agentctl/schedules.json` (global only, 0600)

A separate file rather than a `config.json` key, so `cron add/rm` rewrite a small file atomically instead of round-tripping the whole config.

```json
{
  "jobs": [
    {
      "name": "issue-triage",
      "schedule": "30 7 * * 1-5",
      "dir": "~/code/personal/agentctl",
      "agent": "brandon",
      "prompt": "Triage issues opened since the last run ...",
      "harness": "claude",
      "mode": "window",
      "worktree": false,
      "model": "", "effort": "", "backend": "", "account": "",
      "overlap": "skip",
      "catchup": "latest",
      "timeout": "45m",
      "skip_if_usage_above": {"five_hour": 70, "seven_day": 85},
      "enabled": true
    }
  ]
}
```

- `schedule`: 5-field cron, local time. Parse with `robfig/cron/v3` (standard parser, no seconds field). Also accept `@hourly`/`@daily`/`@weekly`.
- `prompt` or `prompt_key` (context store). Exactly one is required.
- `mode`: `window` (default) or `headless`. See §3.
- `harness`: `claude` (default). Other values are validated against the `internal/harness` registry from multi-harness.md. Until that refactor lands, `claude` is the only accepted value, but the field ships in v1 so the schema doesn't change later.
- `overlap`: `skip` (default) skips a run if the previous one is still alive and not Done; `allow` runs anyway. No `queue`, which is one more state machine for little gain.
- `catchup`: `latest` (default) runs once for the most recent missed fire if it is within `catchup_window` (default 6h), the same as Desktop tasks, and otherwise records a skip. `none` never catches up.
- `timeout`: after this, a `window` run gets `C-c` plus a `blocked`/timeout notice. It is not killed, so a human can look. A `headless` run gets SIGINT, then SIGTERM after 30s (SIGINT first so the turn ends cleanly; see the headless docs on exit code 143).

Global defaults under `cron` in `config.json`: `min_interval` (default `15m`; `cron add` rejects schedules that fire more often), `max_concurrent` (default 2), `skip_if_usage_above` (default 5h ≥ 80%, 7d ≥ 90%), `catchup_window`.

### 2. The ticker: one launchd job, `agentctl cron tick` every 60s

- `agentctl cron install` writes `~/Library/LaunchAgents/com.blsmth.agentctl.cron.plist` with `StartInterval 60` and `RunAtLoad`. Reuse `internal/serve/launchd.go`'s plist writer: extract it to a small `internal/launchd` package and keep serve on it. Set `PATH` (so `claude`, `tmux` and `gh` resolve) and a UTF-8 `LANG` (the #69 gotcha). On Linux, `cron install` prints a crontab line or systemd user timer. That's v2; personal use is macOS.
- One plist for all jobs, rather than one per job: there's a single place to install, upgrade and inspect, and missed-run logic lives in Go where it is testable, not in launchd's `StartCalendarInterval` coalescing.
- **Not** in the monitor (being removed) and **not** in `agentctl serve` (a crash in the HTTPS server shouldn't stop schedules, and serve may be off).
- `cron tick` is short-lived and flock-guarded (`<stateDir>/cron/tick.lock`, the same pattern as `reconcile`). For each enabled job it computes the fires due since `last_fire` (persisted per job in `<stateDir>/cron/state.json`), applies `catchup`, `overlap`, `max_concurrent` and the usage gate, then launches. It updates `last_fire` *before* launching, so a crash can't double-fire.
- Deterministic jitter: 0–90s per job, from a hash of the job name, so jobs at `0 * * * *` don't all hit the API on the same second. Claude Code and Desktop stagger their scheduled tasks for the same reason. Ours is smaller, because a 60s tick already spreads load.

### 3. Launching: reuse dispatch, don't fork it

`mode: window` (default) calls the same code path as `agentctl dispatch --prompt` (`cmd.RunDispatch`, refactored to take an options struct) with the job's agent, dir, model, effort, backend and worktree settings. That reuse buys:

- An interactive `claude` (not `-p`) in a tmux window under the user's own login: the ToS-cleanest shape, the same thing a human dispatch does.
- `--permission-mode auto` (worker default). An unattended run that hits a real prompt shows `blocked` and fires the existing push notification. Phone approvals (pending `phone-approvals-prompt`) make this answerable from anywhere.
- Worktree per run (slug `cron-<job>-<YYYYMMDD-HHMM>`), done detection, PR auto-open, SessionEnd reclaim of empty/landed worktrees. Jobs that only report should set `worktree: false`.
- **Session missing:** if the project's tmux session isn't running, start it detached (`tmux.StartSession` without attach), then dispatch. If tmux itself isn't available, record a `failed` run and notify.
- Windowstate gets `scheduled_by: <job>` and `spawned_by_session: cron` so the hub and chrome bar can show a ⏲ marker and `sweep` can treat cron windows as closable once Done.

`mode: headless` is for small report-style jobs that don't need a window: `claude -p --permission-mode auto --permission-prompts none --output-format json` (plus `--max-budget-usd` when a metered backend is set). No `--bare` (invariant 2). Output goes to `<stateDir>/cron/runs/<job>/<ts>.json`. The `result` text is the push-notification body and is viewable with `agentctl cron runs <job>`. The headless run still fires agentctl hooks, since it isn't bare, so cost lands in `agentctl cost` as usual.

### 4. Multi-harness

The cron layer only knows "launch job J with harness H in mode M". It goes through the `Harness` interface from multi-harness.md, with one addition to that proposal:

```go
// in Caps
Headless bool
// on Harness
HeadlessCommand(inv Invocation) (shellCmd string, env map[string]string, err error)
```

| Harness | `window` | `headless` | Auth |
|---|---|---|---|
| claude | `claude --agent …` (today) | `claude -p … --permission-prompts none` | user's own login or API key; never extracted |
| codex | `codex --no-daemon …` | `codex exec …` | OpenAI login or provider `auth.command` |
| pi | `pi --session-id … -e agentctl-pi.ts` | `pi -p …` | `models.json` `apiKey: "!agentctl backend-token …"` |

`cron add --harness X` fails if X isn't registered, or if `mode: headless` is used and `!Caps.Headless`. The usage gate in §2 uses Claude's statusline-pushed 5h/7d numbers. Other harnesses have no equivalent yet, so for them the gate is a no-op plus `max_concurrent`, and `cron list` says so.

### 5. Safety and approval

- **Creating a schedule is the approval moment.** One `cron add` authorizes unlimited future autonomous runs, so when an agent (not a human shell) runs `agentctl cron add/edit/enable`, the PreToolUse gate shows a preview popup like the dispatch preview: schedule in words ("weekdays 07:30, next: Tue 18 Nov 07:30"), agent, dir, harness, mode, rendered prompt. Extend `dispatch_preview.go`'s parser rather than adding a second popup. A human typing `cron add` in a shell isn't gated.
- `--prompt "$(cat …)"` is rejected for `cron add` for the same reason as dispatch. Use `--prompt-key` or `--prompt-file` (copied into the job at add time, so later edits to the file don't silently change what runs).
- Kill switch: global `cron.enabled: false` makes `tick` a no-op. `agentctl cron pause <job>` / `resume <job>`.
- Every decision (fired, skipped:overlap, skipped:usage, skipped:catchup, failed:…) is appended to `<stateDir>/cron/runs.ndjson`. `agentctl health` reports: plist loaded? last tick age? jobs failing N times in a row?

### 6. CLI

```
agentctl cron add <name> --schedule '30 7 * * 1-5' --agent brandon --dir ~/code/x \
                         (--prompt-key K | --prompt-file F | --prompt '...') \
                         [--harness claude] [--mode window|headless] [--no-worktree] \
                         [--model M] [--effort E] [--backend B] [--timeout 45m]
agentctl cron list                 # next fire, last result, throttle state
agentctl cron runs [<name>]        # history from runs.ndjson
agentctl cron run <name>           # fire now (ignores schedule, honors overlap + usage gate unless --force)
agentctl cron pause|resume|rm <name>
agentctl cron install|uninstall    # launchd plist
agentctl cron tick                 # hidden; what launchd runs
```

The phone web app and the hub get a Schedules view to list, create, edit, pause/resume, run now, duplicate and delete jobs (#237).

### 7. Opening a run's session

A `fired` entry in `runs.ndjson` records `session_id`, the agent session the launch pre-assigned (`dispatch.Result.SessionID`, plumbed through `cron.LaunchResult`). Window IDs are reused after a tmux restart, so the window ID alone never identifies a run. Entries recorded before this have no `session_id` and stay valid.

`cronedit.Resolver.ResolveRun` maps an entry to a `RunTarget` (`live`, `closed` or `missing`), behind injectable seams for live windows, the closed ledger and the transcript finder:

- **live**: a live window whose windowstate `session_id` equals the entry's. A legacy entry (no session ID) needs the same tmux session and window ID, `scheduled_by` equal to the job, and `scheduled_at` within two minutes of the entry; a window with no session yet (still launching) matches on the same terms.
- **closed**: a `closedlog` record with the same session ID (legacy: same session and window, `scheduled_by` equal to the job, closed after the entry, closest wins). The transcript comes from the record's `log_path`, else a search of every account's projects dir. `Resumable` reuses `closedlog.Record.ResumeCheck`, the same check the hub's Closed section uses.
- **missing**: nothing matched or the transcript is gone (closed-ledger pruning at 90 days, `runs.ndjson` truncation, another machine). `Reason` says why; this is a normal state.

Only `fired` entries resolve; skipped, timeout and failed entries never do. Viewing a transcript never starts an agent.

## Rollout

1. **`cron` core, Claude only, `window` mode:** schedules.json, `robfig/cron/v3`, tick with flock and state, catch-up, overlap, `max_concurrent`, usage gate, launchd install (extract `internal/launchd`), `RunDispatch` options struct, `scheduled_by` in windowstate, the CLI above, `health` lines. Can ship before the Harness refactor.
2. **Approval popup for agent-run `cron add`** plus `--prompt-file`. Done: `cron add`, `cron resume` and `cron run --force` are gated by the dispatch popup (`PreviewKindCron`, `internal/hooks/cron_preview.go`) and phone approvals (`remoteapproval.KindCron`); plain `cron run` honors every throttle and is not gated. The Bash validator and the `PreToolUse` `hook-pre-tool` deny direct writes to `schedules.json` and `<stateDir>/cron/`. `cron edit` (added with the Schedules UI, step 6) is gated the same way; `resume` is the enable.
3. **`headless` mode** for Claude, with run output and push body.
4. **Harness plumbing** once multi-harness phase 1 lands: route launches through `Harness`, add `Headless`/`HeadlessCommand`, accept `harness: codex|pi`.
5. Hub ⏲ marker done: cron windows show `⏲ <job>` in the hub recap column and on phone fleet rows, sit under a `scheduled (N)` / "Scheduled jobs (N)" bucket, the phone window view has a "Scheduled run" line, and `agentctl status` shows MODE `cron` (`scheduled_by`/`scheduled_at` on the window JSON). 
6. **Schedules UI** ([#237](https://github.com/blsmth/agentctl/issues/237)). PR 1 done: the shared editing core `internal/cronedit` (per-field validation, a jobs-file flock around every read-modify-write, fire state kept on edit and re-seeded only when the schedule changes or a job resumes, name immutable, preview with words and next 3 fires, the `SimpleSchedule`/`ParseSimple` picker round trip, list status), `agentctl cron edit` behind the cron approval popup, `cron add/pause/resume/rm` on the shared path, and the `/v1/schedules` serve API (see [configuration.md](../configuration.md#cron)). PR 2 done: the phone Schedules screen (`internal/serve/web/schedules.js`, `#/schedules`). PR 3 done: `T` in the hub lists the jobs (phone glyphs and sort), shows a job card as a drawer sliding in over the list pane (the live viewer pane is left alone), and creates, edits, duplicates, pauses, resumes, runs and deletes jobs through `internal/cronedit` in process, with the picker, live preview and review step; the job card in `cronedit.Detail` (and `GET /v1/schedules/{name}`) carries the next 3 fires.
7. Later: Linux timer install, event triggers (a GitHub webhook via `serve`) if wanted.

## Tests worth writing

- Due-fire calculation: table over (schedule, last_fire, now) including DST transitions, sleep gaps longer than `catchup_window`, and a first run with no state.
- Overlap and concurrency: job still running vs. Done vs. window gone.
- Usage gate: stale rate-limit file (older than 6h → treat as unknown → run, but log), missing file, over threshold.
- `min_interval` rejection for `* * * * *`.
- Invariants 1–3: the claude launch command never contains `--bare` without `ANTHROPIC_API_KEY`/`apiKeyHelper`. A project-local config containing `schedules`/`cron` is ignored. No code path reads Claude credential files (grep test, as with the existing deny-list tests).
- Tick double-fire: two concurrent `tick`s → one launch (flock), and `last_fire` written before launch.
- Every exec/detach in tick stubbed in tests (the 11-17 fork-bomb rule).

## Open questions

1. (Answered: the Team seat is LL's, so personal jobs use the personal Max account; see [accounts.md](accounts.md).)
2. Default `min_interval` of 15m: too tight or too loose? Anthropic's own cloud and Desktop forms use 1h.
3. Should a cron `window` run close its window after Done-and-reclaimed, or leave it ✓ for review (current dispatch behavior)? This proposal leaves it, and `sweep` cleans up.
