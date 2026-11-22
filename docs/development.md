## agentctl Development Guide

- Install for development: `just install` (builds to `~/.local/bin/agentctl`; override with `BINDIR`)
- Build only: `just build` (local `./agentctl`)
- Full restart: `just restart` (install + kill tmux session + relaunch)
- Debug logs: `just logs` (tails `.agentctl/logs/agentctl.log` for the current project)
- Go 1.26+, tmux 3.4+, Bubble Tea for TUI popups

### Working Directory

- **You are already in the repository (or your worktree). Never `cd` anywhere.**
- **Never chain `cd` into Bash commands.** Write `go build ./...`, not `cd <repo> && go build ./...`.
- Use paths relative to the repo root or absolute paths in all tool calls.

### Code Style

- Subcommand routing uses [Kong](https://github.com/alecthomas/kong): add a field to the `CLI` struct in `cli.go` with a `Run()` method (`hidden:""` for internal-only commands)
- Wrap timing-sensitive calls with `logging.Timing("label")` / `done()`
- Log significant actions with `logging.Log(...)` for debug traceability
- Return errors from library code, wrapped with context (`fmt.Errorf("doing X: %w", err)`); command `Run()` methods return the error to Kong
- Tmux interactions go in `internal/tmux/`, never in root-level files
- There is no plugin registry. Reactive logic is a plain function called from `detectAndFireTransitions()` in `monitor.go`

### Bubble Tea TUI Pattern

All popups follow the same pattern in `internal/tui/`:
1. Define a `FooResult` struct for output and a `FooModel` with Bubble Tea's `Init/Update/View`
2. `NewFooModel(...)` constructor, `Result()` accessor
3. The calling command runs `tea.NewProgram(model)`, then reads the result after `p.Run()`
4. Table-based viewers share `tableStyles` (`internal/tui/tablestyles.go`); reuse it rather than redefining column, rule, dim, detail, subtitle, and hint-bar styles. Popup-only styles such as `selectedStyle` are defined locally in the tui package (e.g. `improve.go`)
5. Popups are launched via `tmux display-popup -E` with the running agentctl binary (`os.Executable()`)

### Dispatch command

`agentctl dispatch` is the non-interactive entry point for launching an agent on a GitHub issue.

```bash
agentctl dispatch --issue 42 --agent go-engineer       # explicit agent
agentctl dispatch --issue 42                            # auto-detect from labels
agentctl dispatch --issue 42 --dir ~/code/other/repo    # explicit working directory
agentctl dispatch --issue 42 --repo owner/name          # explicit GitHub repo
```

The implementation is `dispatchIssue()` in `internal/cmd/dispatch.go`, reusable from both CLI and TUI flows.

**When to use dispatch vs ticket-launch:**
- `dispatch` is non-interactive, for scripts and automation. You already know the issue number.
- `ticket-launch` is the interactive TUI, for humans browsing and picking issues.

### State is hook-driven

Claude Code hooks write each window's state to `.agentctl/windows/<windowID>.json` (`internal/windowstate`). The monitor reads it every tick. JSONL logs are parsed only for metrics. Config reference: `docs/configuration.md`.

### tmux patterns

Tmux interactions all live in `internal/tmux/`. Read the existing popup, menu, and keybinding code there before adding new ones.

### Phone web app: browser tests

The phone app (`internal/serve/web`) has three layers of checks:

- `go test ./internal/serve/` includes `TestWebReferencesServed` and `TestServiceWorkerPrecachesShell` (`webjs_imports_test.go`): every URL loaded by a static `import`, `index.html`, the manifest or the `sw.js` precache list must be in `staticAssets` (`assets.go`) and serve 200, and every served shell asset must be precached. No node needed. A new module that is imported but missing from the asset table fails here.
- `node internal/serve/web/logic.test.mjs`: the pure logic in `logic.js`.
- `just web-test`: Playwright on WebKit (the engine iOS Safari uses) with an iPhone 15 profile (`hasTouch`, `isMobile`, iPhone viewport and UA). It boots the app, checks the service worker, the fleet, swipe-to-close, the window view and live updates. The CI `web` job runs it on every PR and uploads the HTML report and traces when it fails.

`just web-test` installs the npm deps and WebKit on first run; extra arguments go to `playwright test` (`just web-test -g swipe`, `just web-test --headed`, `just web-test --debug`). The report is in `internal/serve/e2e/playwright-report/` (`npx playwright show-report` from that directory).

**How it runs.** `playwright.config.ts` starts `internal/serve/servetest/devserver`, which runs the real serve handler (Router, middleware, embedded assets) over fakes from `internal/serve/servetest`: a fixture fleet (`DefaultFixture`: idle, done, working and blocked windows, a three-level dispatch tree, a second project and the hub), canned pane captures, and backends that record every close, key, reply, dispatch and approval decision instead of touching tmux. A device is paired for real in a temp state dir and the browser gets its cookie, so requests go through device auth and CSRF as on a phone. It listens on plain HTTP on loopback only (loopback is a secure context, so the service worker registers) and is never linked into the agentctl binary.

The dev server's control API lives under `/__test/` (see `internal/serve/servetest/control.go`): `POST reset`, `GET actions`, `GET`/`PUT fleet` (swap fixtures, or `{"fixture":"empty"}`), `POST window` (patch one window's state, activity or pane) and `POST approvals` (open a remote approval as a hook would). Fixture changes reach the page as real SSE events, because the fleet bus re-reads the fixture every 50ms.

**Adding a test.** Put a `*.spec.ts` in `internal/serve/e2e/tests/` and import `test`, `expect`, `openFleet` and `row` from `./fixtures`. Each test starts on a reset server with the device cookie set; the `control` fixture drives the server and reads recorded actions (`control.waitForActions(...)`). An automatic guard fails any test whose page logged a console error or had a same-origin request fail or return >= 400; call `guard.allow(/.../)` for an error the test provokes on purpose. Touch drags use `drag`/`move`/`lift` from `./touch`, which dispatch `PointerEvent`s with `pointerType: "touch"` (Playwright's touchscreen can only tap, and WebKit has no protocol-level touch moves); taps use `locator.tap()`. Tests run serially against one server.

**Poking at it by hand.** `just web-dev` serves the app at `http://127.0.0.1:8787`. Open the printed `/__login` URL to get the device cookie, then use the browser's device emulation (Chrome DevTools device toolbar, or Safari's Responsive Design Mode) to get a phone viewport and touch events. `curl` the control API to change the fleet while you watch.

The iOS Simulator (macOS with Xcode only) is still useful as an occasional manual smoke check of real Safari behaviour (installing to the home screen, push, safe areas), but it is not part of CI.

### Demo harness and screenshots

`just demo` builds a fake, fully isolated agentctl world for screenshots and bug reports: fake projects, fake agent windows that print canned Claude Code output (no `claude`, no tokens), and real agentctl chrome bars, hub, HUD, popups and statusline on top. Scenes are data files in `demo/scenes/` (schema in [demo/scenes/README.md](../demo/scenes/README.md)); the code is `internal/demo` and its CLI `internal/demo/cmd/seed`.

- `just demo [scene]`: start (or re-seed) a scene, default `hub` (a bit of everything), and print the windows and the exact attach command. For the default root that is `tmux -L agentctl-demo attach`; for any other `AGENTCTL_DEMO_ROOT` the server label is `agentctl-demo-<8 hex of sha256(root)>`, so concurrent demos with different roots (two worktrees) never share a server. A demo refuses to start or stop when the server on its label records a different root (`DEMO_ROOT_OWNER` in the server's global environment). `just demo-stop` only ever kills its own label.
- `just demo-stop`: kill the demo tmux server and remove the temp root.
- `just demo-shot <scene-or-path> [out]` (a bare name, with or without `.yaml`, is looked up in `demo/scenes/`; anything containing a `/` is a path): seed the scene, generate a vhs tape from its steps and render `demo/out/<scene>.png` (or `.gif`, or `out` + extension). `just demo-shots` renders every scene. Needs `vhs` (`brew install vhs`). `demo/out/` is gitignored. In vhs's default font `⏺` and `⎿` may look like `●` and `L`, and `📱` like a notebook; those are font fallbacks, not different characters.
- `just demo-phone-shot <scene-or-path> [out]`: render the scene's `phone:` block, the phone web app over the same fleet, to `demo/out/<scene>-phone.png` (numbered or suffixed PNGs for several screenshots). It builds `internal/serve/servetest/devserver`, starts it with `-scene` on a free loopback port (the same fake backends as `just web-dev`: no tmux, no real state dir), plays the steps with Playwright WebKit (`internal/serve/e2e/shot.mjs`, installed like `just web-test`) and always stops the server. The scene-to-fixture conversion is `internal/demo/phonefix`, and pane text comes from `demo.Render`, so `just demo-pane-check` covers the phone too. `devserver -scene <name-or-path>` also works by hand. The devserver and `internal/demo` are not linked into the agentctl binary (check with `go list -deps .`). Schema and field mapping: [demo/scenes/README.md](../demo/scenes/README.md#phone).
- `just demo-race [out] [duration]`: record `agentctl race` over the canned transcript `internal/race/testdata/demo.jsonl` (a rate-limiter change: reads, greps, a Task sub-agent, edits, a failing then passing `go test`, a PR) to `demo/out/race.gif` at 120x35, replay compressed to `duration` (default 30s). It needs no demo world or tmux: it builds agentctl into the demo root, runs it with a temp `HOME` (default theme, no real config) and writes a vhs tape there. Needs `vhs`; the recipe says so when it is missing. Regenerate the fixture by hand if you change it: `TestFixtureSteps` (`internal/race`) pins its step and tool counts.
- `just demo-check <scene-or-path>`: validate a scene without starting anything.
- `just demo-pane-check <tmux-target>`: drift check for the fake Claude Code pane. It runs `tmux capture-pane -p -t <target>` on the default tmux server (your real session, not the demo's), reduces the capture to which structural markers it contains (banner glyphs, `❯ ` prompt, `⏺ ` bullets, `⎿` result marker, `────` input rules, `⏵⏵` mode line, `✻` footer), compares them with a freshly drawn fake pane, prints which markers only the real pane has and which only the fake has, and exits non-zero on any difference. It never prints or stores the real pane's text, only marker names. Run it against a real pane that has completed a turn with a tool call, in auto mode, after a Claude Code upgrade; when it fails, update `internal/demo/pane.go` (and `ccVersion`). It is not wired into CI, which has no real Claude Code pane.

**Isolation.** Everything lives under `$TMPDIR/agentctl-demo` (override with `AGENTCTL_DEMO_ROOT`; the last path element must contain `agentctl-demo`): the fake `HOME`, `XDG_*` dirs, the fake Claude account (`CLAUDE_CONFIG_DIR=<home>/.claude`, agents in `agents/`, transcripts in `projects/`), the repos under `~/code/`, and `bin/` with the agentctl built from the checkout plus stub `claude`, `gh`, `alerter`, `notify-send` and `terminal-notifier` that fail at once. The tmux server is `-L agentctl-demo` (default root; see above for other roots) with its own config and is started with an environment built from scratch, so it inherits no `TMUX`, `CLAUDE_*` or `AGENTCTL_*` from the session you run it in; every pane, hook and run-shell inherits the demo environment, and agentctl's plain `tmux` calls reach the demo server through the panes' `TMUX`. The seeder replaces its own environment the same way before it touches `internal/paths` or `internal/windowstate`. The global config turns off the monitor, titles, notifications (`notify_cmd: ""`), metrics, cron, PR auto-open and `bash_preview`. Each project's session is started by running the real `agentctl` in the fake repo, so session hooks, keybindings and chrome bars are the real ones; window records are written through `windowstate.Update` (and renamed through `winname.SyncName`) from the scene.

**Adding a scene.** Copy the closest file in `demo/scenes/`, edit the windows and steps, and run `just demo-check` then `just demo-shot <name>`. Steps attach to a window by key, send prefix keys (`prefix: g` opens the hub, `prefix: h` the HUD), wait for screen text and take screenshots; see the existing scenes. New canned output goes in `demo/transcripts/<name>.yaml` or inline under the window's `transcript:`. `TestCheckedInScenes` keeps every checked-in scene and fixture valid.

**Reproducing a bug.** Write a scene anywhere (`/tmp/bug-123.yaml`) with the state that shows the bug (a window `blocked` with a `pending_tool`, a `done` window left unseen, a child whose `spawned_by` names its parent, a narrow `terminal.cols`), run `just demo /tmp/bug-123.yaml` to poke at it, then `just demo-shot /tmp/bug-123.yaml /tmp/bug-123` for the image.

**The approval scene** is a real round trip: Enter in a window with `approval:` makes its fake pane feed a PreToolUse payload to `agentctl hook-validate-bash`, which marks the window blocked and opens the dispatch approval popup, exactly as under Claude Code.
