# Demo scenes

Each `*.yaml` file here describes one demo world and the keys a generated vhs tape sends to it. `just demo <scene>` seeds it for poking around; `just demo-shot <scene> [out]` seeds it and renders it to `demo/out/<scene>.png` (or `.gif`). A scene can be a bare name (resolved here, with or without `.yaml`) or a path to any file; anything containing a `/` is a path, e.g. `just demo-shot /tmp/bug-123.yaml` or `./hub.yaml`. Relative paths are resolved from the repo root. `just demo` prints the exact `tmux -L <label> attach` command; the label is `agentctl-demo` for the default root and `agentctl-demo-<hash>` for a custom `AGENTCTL_DEMO_ROOT`. `just demo-phone-shot <scene> [out]` renders the phone side (see Phone). `just demo-check <scene>` validates a file without starting anything.

Decoding is strict: an unknown key, a bad state or mode, a `spawned_by` that names no window, or a step with two actions fails with an error naming the field.

## Schema

```yaml
description: free text                # optional
output: png                           # png (default) | gif. gif records every visible step; screenshot steps still write PNGs

terminal:                             # all optional
  cols: 160                           # 40-400, default 160
  rows: 45                            # 8-200, default 45
  theme: Catppuccin Mocha             # any `vhs themes` name
  font_size: 14                       # px, default 14
  hub_left: 95                        # hub list width in columns (the live hub caps it at 56 otherwise); default: unset
  padding: 20                         # px, default 20
  framerate: 15                       # gif framerate, 5-60 (vhs default 50); lower makes smaller gifs

config:                               # optional; network, notifications, monitor, titles and metrics are always off
  theme: catppuccin-mocha             # agentctl theme (see `agentctl` theme picker); default: agentctl's default
  dispatch_approval: all              # all (default) | non-review | off
  serve: true                         # fake a running `agentctl serve` so the hub shows "📱 on" (a stub on a socket in the demo's state dir); default false
  statusline_format: rich             # rich | complementary | full | mobile | minimal; default: agentctl's default
  default_agent: go-engineer          # default_agent: what a hub launch (`n` then `a`) uses; a fake ~/.claude/agents/<agent>.md is written for it. Launched windows run a live fake `claude` (start screen, idle row) that never reaches the network; see hub-launch.yaml
  new_window_position: right          # left (agentctl's default) | right; see hub-orchestrate.yaml
  cron: true                          # set cron.enabled (default false). No ticker is installed and claude is stubbed, so nothing fires either way; true only drops the hub's "cron is off" warning

projects:                             # at least one; each becomes ~/code/<name> and session agentctl-<name>
  - name: acme-api                    # lowercase letters, digits, dashes
    branch: main                      # default main
    commits: [Initial commit, Add X]  # commit subjects, oldest first; default [Initial commit]
    dirty: [README.md]                # files left modified in the checkout

windows:                              # in tmux order per project
  - key: orch                         # unique; steps and spawned_by refer to it
    project: acme-api                 # must be in projects
    agent: orchestrator               # agent name; a fake ~/.claude/agents/<agent>.md is written
    instance: orchestrator            # default: agent, then agent:2, agent:3 within the project
    mode: bare                        # bare (default) | dispatched | adhoc | review-pr
    state: idle                       # working | blocked | idle (default) | done
    seen: false                       # default false for done (shows ✓), true otherwise
    since: 4m                         # how long ago it entered its state (Go duration)
    select: true                      # make it the session's current window (one per project)

    title: Rate limiter rollout       # short title shown in the hub
    kind: go                          # short-title kind (dispatched windows only); review-pr defaults to review
    ticket: 142                       # dispatched only
    ticket_title: Add rate limiting
    task: Free-form task line         # ad-hoc task; review-pr defaults to "Review PR #N"
    review_pr: 138                    # required with mode review-pr
    pr: 64                            # PR the window opened
    branch: agent/x/issue-142         # default: agentctl's branch for the mode; bare uses the project branch
    worktree: true                    # default true for every mode but bare (bare cannot have one)
    dirty: [internal/x.go]            # files left modified in the window's worktree
    spawned_by: orch                  # key of the dispatching window: same project, listed earlier, not bare

    model: claude-opus-5-5            # default claude-opus-5-5
    cost_usd: 1.87
    ctx_pct: 22                       # 0-100
    turns: 9
    last_tool: go test ./...          # default: the transcript's last tool call

    pending_tool: Bash                # blocked only (required): the tool awaiting permission
    pending_detail: psql -f m.sql     # blocked only: its command or path
    permission:                       # blocked only: the dialog drawn in the pane
      title: Bash command             # default "Bash command"
      command: psql -f m.sql
      description: Apply the migration
      option2: Yes, and don't ask again for psql commands

    spinner: "Testing… (41s · ↓ 1.2k tokens · esc to interrupt)"   # working only
    transcript: rate-limiter          # fixture demo/transcripts/<name>.yaml, or an inline transcript (below)
    approval: agentctl dispatch ...   # Enter in this pane runs the real hook-validate-bash with this command
    reply:                            # idle only, not with approval: a scripted turn (see Scripted turns)
      - say: I'll split this in two.
        delay: 1500ms                 # pause before the entry, up to 10s
      - tool: Bash
        input: {command: 'agentctl dispatch --agent go-engineer --prompt "..."'}
    scheduled_by: nightly-deps        # a schedule's name: the window was launched by that cron job (⏲ tag, the job's running window)

schedules:                            # optional `agentctl cron` jobs for the hub's Schedules tabs (T)
  - name: nightly-deps                # cron job name rules
    project: billing-svc              # must be in projects; the job's dir is its checkout
    agent: go-engineer                # a fake agent file is written for it
    schedule: "30 2 * * *"            # 5-field cron or @daily etc.
    prompt: Bump the dependencies.    # required
    paused: false                     # default false
    worktree: true                    # default true
    timeout: 45m                      # optional Go duration
    runs:                             # runs.ndjson entries, oldest first
      - {ago: 4h, result: "failed:launch", note: "worktree exists"}   # fired | failed:<reason> | skipped:<kind> | timeout

steps:                                # one action per step
  - attach: orch                      # attach the vhs client to a window (later attaches switch-client)
  - prefix: g                         # tmux prefix (C-b) then this key
  - type: some text                   # typed as-is (no quotes or backticks)
    speed: 45ms                       # only with type: per-character delay, up to 1s (default 30ms)
  - key: Enter                        # Enter Escape Tab Space Backspace Up Down Left Right PageUp PageDown
    repeat: 3                         # only with key
  - ctrl: c                           # Ctrl+<letter>
  - sleep: 1500ms                     # up to 1m
  - wait: agentctl hub                # regexp the screen must match (20s timeout), no slashes
  - screenshot                        # writes <out>.png; later ones <out>-2.png ...
  - screenshot: popup                 # writes <out>-popup.png
  - hide                              # stop recording (gif)
  - show
```

## Scripted turns

A window with `reply:` behaves like an idle Claude Code session waiting for a prompt. Keys sent to its pane echo into the `❯` input box (Backspace deletes, arrows are ignored); Enter submits: the prompt moves into the transcript as a `❯` line and the reply plays. Each entry appears after its `delay` (defaults: 1.5s before the first, 1s before a say, 0.8s before a tool), under a `✻ Thinking… (Ns · esc to interrupt)` spinner that counts up; the turn ends with `✻ Brewed for Ns`. Only one turn plays.

The pane fires the real hooks the way Claude Code does, so the window's row changes live: `hook-user-prompt` on Enter (working), `hook-pre-tool` and, for Bash, `hook-validate-bash` before each tool, `hook-post-tool` (or `hook-post-tool-failure`) after it, and `hook-check-done` at the end (idle). A Bash entry whose command is `agentctl dispatch ...` really runs, from the window's checkout with the demo PATH, once hook-validate-bash allows it: the real dispatch opens a real worker window (the fake `claude`, which shows the prompt and stays working) with `spawned_by` set, and the call's result line is the command's `Dispatched ...` line. A deny shows `Denied: <reason>` and nothing runs. Every other tool only shows its `result`. Agents named by `--agent`/`-a` in those commands get agent files. Use `--prompt` dispatches: `gh` is stubbed, so `--issue` and `--review-pr` fail. With `dispatch_approval` at its default (`all`) the dispatch waits for the approval popup; set `off` to skip it.

Reach the pane through the hub's live viewer: open the hub (`prefix: g`), and `key: Enter` on the selected row focuses the viewer, so typed keys go to the agent. Set `new_window_position: right` in such a scene: with the default (`left`) every dispatched window is inserted before the others, and tmux re-syncs the viewer's grouped session by window index, so the viewer (and the hub selection) jumps to another window.

## Phone

An optional `phone:` block drives the phone web app over the same fleet. `just demo-phone-shot <scene-or-path> [out]` serves the scene on the dev server (`devserver -scene`: loopback, free port, no tmux or real state), plays the steps with Playwright (WebKit, the `just web-test` install) and writes `demo/out/<scene>-phone.png`. A lone unnamed screenshot is `<scene>-phone.png`; several are `<scene>-phone-<suffix>.png`, or `<scene>-phone-<n>.png` when unnamed. Strict like the rest of the schema.

```yaml
phone:
  viewport: {width: 393, height: 852, dpr: 3}   # default iPhone 15; width 200-1200, height 300-2400, dpr 1-4
  theme: light                                   # light (default) | dark
  steps:                                         # at least one, and at least one screenshot
    - open: fleet                                # fleet | window:<key> | terminal:<key> | approval:<key>
    - tap: Approve                               # visible text, or css:<selector>
    - wait: Do you want to proceed?              # visible text, or css:<selector> (10s)
    - screenshot                                 # or screenshot: <suffix>
```

`approval:<key>` needs a window with `approval:` set; only those windows have a remote approval open, and the phone shows them blocked with the pane drawn as it is while agentctl's hook holds the command (no Claude dialog yet). A plain `blocked` window shows Claude Code's permission dialog and has no remote approval. A window's pane text is drawn by the same renderer as the terminal demo (`internal/demo/pane.go`, from its transcript), so `just demo-pane-check` covers both. `terminal:<key>` opens the live terminal route, which the dev server cannot serve (no tmux), so it shows the app's unavailable state.

Mapped to the phone: `agent`, `mode` (dispatched shows as `dispatch`), `state` (done is idle plus done, or the unseen turn-done mark for a bare window), `title` and `kind` (review-pr defaults to `review`), `ticket`/`ticket_title`, `task`/`review_pr`, `pr`, `branch` (not bare), `spawned_by` (the parent/child tree), `model`, `cost_usd`, `ctx_pct`, `since`, `last_tool` (else the transcript's last tool, then its last text), `pending_tool`/`pending_detail`/`permission` (the pane dialog), `approval` (the remote approval and its pane), and from the transcript the recap (away summary, else ticket or task, else title), the prompt and the pane.

No phone equivalent: `schedules` and `scheduled_by` (the phone's `open: schedules` shows the dev server's own demo jobs), `seen`, `turns`, `instance` (rows show the agent), `worktree`/`dirty`, `spinner` (drawn in the pane only), `select`, `projects[].commits/dirty/branch`, `config.*`, `terminal.*`, the per-project `shell` window (the phone fleet has no shell rows) and the pane statusline (it needs the real `agentctl statusline` process).

## Transcripts

A transcript is what the fake pane draws as Claude Code output, and it is also written as Claude Code JSONL under the fake account's `projects/` dir, so the hub recap, HUD stream and cost columns read it like a real session. Fixtures live in `demo/transcripts/<name>.yaml`; a window can also inline one under `transcript:`.

```yaml
title: Orders rate limiter            # ai-title record
recap: One-line away summary          # system/away_summary record (the hub recap)
pr: 64                                # pr-link record
entries:                              # exactly one of user, say or tool per entry
  - user: The prompt
  - say: Assistant text
  - tool: Bash                        # any tool name; Edit shows as Update
    input: {command: go test ./...}   # the tool_use input; command/file_path/pattern is shown
    result: ok                        # tool_result text (first lines shown)
    diff: |2                          # Edit/Write diff lines: +added, -removed, " "context
       context line
      -old line
      +new line
    error: false                      # draw the call as failed
    running: true                     # last entry only: no result yet ("Running…")
```

Use `|2` for a diff block whose first line starts with a space (context line), so YAML does not take that space as indentation.
