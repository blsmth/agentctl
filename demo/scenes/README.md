# Demo scenes

Each `*.yaml` file here describes one demo world and the keys a generated vhs tape sends to it. `just demo <scene>` seeds it for poking around; `just demo-shot <scene> [out]` seeds it and renders it to `demo/out/<scene>.png` (or `.gif`). A scene can be a bare name (resolved here, with or without `.yaml`) or a path to any file; anything containing a `/` is a path, e.g. `just demo-shot /tmp/bug-123.yaml` or `./hub.yaml`. Relative paths are resolved from the repo root. `just demo` prints the exact `tmux -L <label> attach` command; the label is `agentctl-demo` for the default root and `agentctl-demo-<hash>` for a custom `AGENTCTL_DEMO_ROOT`. `just demo-check <scene>` validates a file without starting anything.

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
  padding: 20                         # px, default 20

config:                               # optional; network, notifications, monitor, titles, metrics and cron are always off
  theme: catppuccin-mocha             # agentctl theme (see `agentctl` theme picker); default: agentctl's default
  dispatch_approval: all              # all (default) | non-review | off
  statusline_format: rich             # rich | complementary | full | mobile | minimal; default: agentctl's default

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

steps:                                # one action per step
  - attach: orch                      # attach the vhs client to a window (later attaches switch-client)
  - prefix: g                         # tmux prefix (C-b) then this key
  - type: some text                   # typed as-is (no quotes or backticks)
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
