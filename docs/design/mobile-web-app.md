# Mobile web app: `agentctl serve`

Status: approved 2026-11-16. Supersedes the native-iOS draft (`ios-companion-app.md`). Epic: see the GitHub issue titled "Epic: mobile web app (agentctl serve)".

## Goal

From a phone, away from the desk, over the home network or the UDM Pro VPN:

1. **See the fleet**: every agentctl session on the laptop and every agent in it (working, blocked, done, idle), with what each is doing.
2. **Get pushed** when something needs attention: an agent blocked on a dialog, an agent finished with a PR, an agent blocked for more than N minutes.
3. **Unblock agents**: answer a permission dialog, reply to an idle agent, interrupt a runaway one.
4. **Start work**: dispatch an ad-hoc prompt to any agent in any project.
5. **Look closer**: read the pane, and use a live interactive terminal.

Non-goals: code review / merging (deep-link to GitHub), editing config, exposure to the public internet.

## Decisions (made, do not re-litigate)

- **Web app (PWA), not native iOS.** Served by the Go binary from `go:embed`, installed to the Home Screen. iOS 16.4+ supports Web Push for Home Screen web apps. No Apple developer account, no Xcode, no Swift.
- **No frontend build step.** Plain HTML + CSS + vanilla JavaScript ES modules. Third-party JS is vendored (minified file + LICENSE) under `internal/serve/web/vendor/`. No npm, no bundler, no node in CI.
- **Events use Server-Sent Events** (`EventSource`), not WebSocket. Only the live terminal (PR6) uses WebSocket.
- **One process per machine**: `agentctl serve`, launchd-managed. It does not depend on per-project monitors: fleet data comes from `fleet` (tmux + windowstate), exactly like `agentctl status --json`. It is the natural home for the per-machine process in the monitor-removal plan (PR9 `metrics serve` can later fold in; not in scope here).
- **Write actions are in scope from the start** (answer dialogs, reply, interrupt, dispatch), behind `serve.allow_write` and strong guards.
- **Keep-awake**: on by default. While any agent anywhere is `working` or `blocked`, `serve` holds a `caffeinate -s` child (prevents system sleep on AC power only; macOS ignores `-s` on battery). Released when the fleet is all idle/done. `serve.keep_awake: false` disables.
- **Push transports**: Web Push (VAPID) is primary. ntfy is an optional fallback (`serve.ntfy_topic`) for when the PWA isn't installed or Web Push misbehaves. Push payloads never contain pane text or prompt text.

## Architecture

```
Phone (Safari / Home Screen PWA)
   │  HTTPS (local CA, trusted on the phone) over home LAN or UDM VPN
   ▼
agentctl serve  (internal/serve; launchd LaunchAgent; selfexec hot reload)
   ├─ reads: fleet (tmux list-sessions/list-windows + windowstate), tmux capture-pane, agent discovery
   ├─ writes: tmux send-keys (guarded), in-process ad-hoc dispatch
   ├─ events: fsnotify on every session runtime dir windows/ → diff → SSE + push triggers
   └─ push: Web Push (VAPID) → Apple push service → phone; optional ntfy
```

## Configuration (global config only)

New `serve` block in `~/.config/agentctl/config.json`. Every key is **global-only**: stripped from project-local config with the existing `warn(..., globalOnly)` mechanism in `internal/config`.

```json
"serve": {
  "listen": ":8443",                 // default ":8443"; binding all interfaces survives DHCP/network changes, allow_cidrs + device auth are the gate
  "hostnames": ["agentctl.home"],    // extra DNS SANs for the cert; <hostname>.local and all non-loopback IPs are always added
  "allow_cidrs": ["192.168.0.0/16", "10.0.0.0/8"],  // default: RFC1918 + 100.64.0.0/10 + loopback; source IP must match or the connection gets 403 before auth
  "allow_write": false,              // write endpoints return 403 unless true
  "keep_awake": true,
  "push": {
    "blocked": true,
    "done": true,
    "blocked_reminder_minutes": 10,  // 0 disables reminders; one reminder per blocked episode
    "approvals": true,               // push when an approval waits (see Remote approvals); ignores quiet hours
    "quiet_hours": "23:00-07:00",    // local time; empty = none
    "muted_projects": []
  },
  "ntfy_topic": "",                  // optional; posts to https://ntfy.sh/<topic>. ntfy.sh topics are public: use a long random name
  "ntfy_details": false              // off: ntfy posts name only the kind of event (no agent, project, window or Click URL)
}
```

## Files on disk

All under `config.StateDir()` (`~/.local/state/agentctl`) in `serve/`, dirs 0700, files 0600:

- `ca.key`, `ca.crt` — local CA (ECDSA P-256, 10 years, name "agentctl local CA (<hostname>)").
- The CA is name-constrained (critical): DNS `local`, `lan`, `home`, `home.arpa`, `internal` plus any `serve.hostnames` entry outside those at creation time; IPs RFC 1918, `100.64.0.0/10`, loopback and `fc00::/7`. A stolen `ca.key` cannot impersonate public sites. Leaf SANs outside the constraints are dropped with a warning; `agentctl serve ca rotate` replaces the CA (devices must reinstall the profile and re-pair).
- `server.key`, `server.crt` — leaf signed by the CA (≤ 825 days so iOS accepts it), SANs = hostnames + `<os.Hostname>.local` + all non-loopback interface IPs at generation time. Regenerated automatically on start if SANs changed or < 30 days left. The CA is never regenerated automatically.
- `devices.json` — paired devices: `{id, name, created, last_seen, token_sha256}`. Tokens are only stored hashed.
- `vapid.json` — VAPID keypair (PR3). `subscriptions.json` — Web Push subscriptions keyed by device id (PR3).
- `audit.jsonl` — one line per write action (PR4).
- `serve.pid`, `serve.log` (log via lumberjack like the monitor).

## Pairing and auth

1. `agentctl serve pair [--name iphone]` (talks to the running server over a 0600 unix socket at `<StateDir>/serve/control.sock`; if no server is running it says so).
2. Server opens a **5-minute pairing window**:
   - Starts a plain-HTTP bootstrap listener on `listen` port + 1 that serves only `GET /ca.mobileconfig` (an iOS configuration profile containing `ca.crt`) and `GET /` (a page linking to it plus instructions: install profile, then Settings → General → About → Certificate Trust Settings → enable full trust). The bootstrap listener closes when the window ends.
   - Creates a one-time pairing code (128-bit random, base32).
3. The terminal prints: the CA SHA-256 fingerprint, a QR code for `http://<ip>:<port+1>/` (step 1, install CA), and a QR code for `https://<host>:<port>/pair?code=<code>` (step 2). QR rendering: `github.com/mdp/qrterminal/v3`.
4. `GET /pair?code=…` (HTTPS) validates the code (one-time, constant-time compare), creates a device with a fresh 256-bit token, sets cookie `agentctl_device=<token>; Secure; HttpOnly; SameSite=Strict; Path=/; Max-Age=31536000`, and redirects to `/`.
5. Every other request (except `/healthz`, `/manifest.webmanifest`, static assets needed to render the "not paired" page) requires a valid device cookie; otherwise 401 (API) or a "not paired — run `agentctl serve pair`" page (HTML).
6. CSRF: write endpoints additionally require header `X-Agentctl-CSRF: 1` (cookies are SameSite=Strict too).
7. `agentctl serve devices` lists devices; `agentctl serve devices rm <id|name>` revokes.

Source-IP check against `allow_cidrs` runs before everything, including `/healthz`.

## HTTP API (contract shared by all PRs)

All JSON. Errors: `{"error": "message", "code": "snake_case"}` with an appropriate status. Window IDs in paths are tmux IDs without `@` url-escaping issues: use `/v1/sessions/{session}/windows/{window}` where `{window}` is like `@523` (url-encoded `%40523`; the server must accept both `@523` and `523`).

### Read (PR2)

| Method + path | Response |
|---|---|
| `GET /healthz` | `{"ok":true,"version":"…","started":"RFC3339"}` (no auth, CIDR-checked) |
| `GET /v1/fleet` | `{"generated":"RFC3339","groups":[Group]}` |
| `GET /v1/sessions/{s}/windows/{w}` | `Window` plus `"detail"` (summary, last activity, pending tool, blocked since, cost if cheap to compute) |
| `GET /v1/sessions/{s}/windows/{w}/pane?lines=200` | `{"text":"…ANSI…","verdict":"awaiting_input|working|idle|unknown","dialog":{"text":"…","hash":"sha256hex","options":[{"key":"1","label":"Yes"},…]}|null}` |
| `GET /v1/agents` | `{"agents":[{"name":"go-engineer","description":"…","scope":"global|project"}]}` |
| `GET /v1/launch-options?agent=&project_dir=` | `{"backends":[{"name","kind","default"}],"models":{<backend>:{"models":[{"value","note","default"}],"model_list_error"?}},"efforts":[…],"defaults":{"backend","model","effort"}}`; same choices as the tmux launch popup (`internal/launchopts`). Ollama lists are fetched live (3s timeout, cached 60s); on failure only the default model is listed with `model_list_error`. Feature `launch-options`. |
| `GET /v1/projects` | `{"projects":[{"name":"agentctl","dir":"/Users/…","session":"agentctl-agentctl"}]}` (live sessions + registry; dispatch targets) |
| `GET /v1/events` | SSE stream (below) |

`Group` = `{"session","project","dir","rollup":"blocked|working|done|idle","windows":[Window]}`, ordered with `fleet.OrderGroups`.
`Window` = the same field names as `agentctl status --json` rows (`session, project, window_id, window_index, agent, state, done, mode, context_pct, branch, worktree_path, parent, activity`) plus `pr_url`, `issue`, `blocked_since`, `state_since`, `pending_tool` where available. Implement one shared conversion from `fleet.Row` so `status --json` and `serve` cannot drift (refactor `status --json` to use it).

### Events (PR2)

`GET /v1/events` — SSE. On connect, one `event: fleet` with the full `/v1/fleet` body. Then:
- `event: window` `data: Window` — a window appeared or a **rendered** field changed (state, done, activity, context_pct, pr_url…). Hooks rewrite windowstate on every tool call, so diff the rendered `Window`, never raw file events.
- `event: gone` `data: {"session","window_id"}`.
- `: ping` comment every 20s.
Source: an internal event bus (`internal/serve/fleetwatch`) that fsnotify-watches every live session runtime dir `windows/` (and re-discovers sessions every 5s, since sessions come and go), debounces 200ms, rebuilds rows with `fleet`, and diffs. PR3 (push) and keep-awake subscribe to the same bus, receiving typed transitions `{window, from, to}`.

### Write (PR4) — all require `allow_write: true`, CSRF header, and are audit-logged

| Method + path | Body | Behavior and guards |
|---|---|---|
| `POST …/windows/{w}/answer` | `{"key":"1","dialog_hash":"…"}` | Re-capture and re-classify the pane **immediately before sending**. Must still be awaiting input and the dialog hash must equal `dialog_hash`, and `key` must be one of the dialog's option keys or `"esc"`. Otherwise 409 `dialog_changed`. Then `send-keys` the key. The window lock does not cover Claude Code's redraw, so the answered hash is remembered per window for 5s and a repeat (double tap, second phone) gets 409 `already_answered`; the answer also waits up to 1s for the dialog to leave the pane before releasing the lock. |
| `POST …/windows/{w}/reply` | `{"text":"…"}` | Only when the pane classifies idle (prompt ready) and windowstate is not `working`. Sends literal text (`send-keys -l`) then Enter. Text > 4000 chars rejected (413). 409 `not_idle` otherwise. Text whose first non-space character is `!` (Claude Code bash mode: runs a shell command with no permission dialog), `/` (slash commands such as `/permissions`, `/hooks`, `/add-dir`) or `#` (memory) is rejected with 400 `reply_is_command`: a reply is text for the agent, never a command. |
| `POST …/windows/{w}/queue` | `{"text":"…"}` | The explicit opt-in for messaging a **working** agent (feature `queue`, advertised with `write`): Claude Code queues typed input and delivers it after the current step. Accepts a `working` or `idle` record with a pane that classifies busy or idle (the turn may end between tap and send); a `blocked` record, a dialog or an unrecognised pane is 409 `not_idle`. Same length limit, `reply_is_command` prefix guard and guarded inject as `reply`, audited as action `queue`. |
| `POST …/windows/{w}/upload` | multipart, up to 4 `image` parts | Feature `upload` (with `write`). Stores each image (png, jpeg, gif or webp, sniffed from the bytes; the client name and declared type are ignored; ≤10 MB each) as `<runtimeDir>/uploads/<window>/<random>.<ext>`, 0600 in 0700 dirs, and returns `{"images":["<id>",…]}`. Audited as `upload` (hash of the image bytes). Uploads older than 7 days are pruned on every upload and by `reconcile --housekeeping`. |
| `reply` / `queue` `"images"` | `{"text":"…","images":["<id>"]}` | Optional IDs from `upload` for the same window (≤4). Each is appended to the typed text as `[Screenshot: /abs/path]` on its own line after a blank line, so the agent opens it with its file-read tool; unknown IDs are 400. Text may be empty when images are present. |
| `POST …/windows/{w}/interrupt` | `{}` | Sends Escape. Allowed in any state. |
| `POST /v1/dispatch` | `{"agent","project_dir","prompt","model"?,"backend"?,"effort"?}` | Ad-hoc dispatch, same code path as `agentctl dispatch --agent X --prompt Y` with `--dir project_dir`. Export the needed function from `internal/cmd` (or move it to `internal/dispatch`) instead of shelling out. `project_dir` must be one of `/v1/projects` dirs. Returns `{"window_id","session","branch"}`. |
| `POST /v1/group-order` | `{"keys":["/path/proj",…]}` | Saves the project group order shared with the hub (`group-order.json`, see `fleet.GroupOrderPath`). `keys` are the visible groups' `key` values (`fleet.GroupKey`: project dir, else session) in the new order: at most 500, each a non-empty string of at most 4096 bytes with no control characters (else 400); duplicates and keys of groups not currently in the fleet are ignored. Merged with the saved order by `fleet.MergeGroupOrder`, so projects that are not running keep their slots; written atomically (0600). Triggers a fleet refresh so every client gets the new order as a `fleet` event. Returns `{"keys":[…]}`, the saved order. Audited as `group-order`. |
| `POST …/windows/{w}/close` | `{}` | Kill the window, only if done or idle and it is not the orchestrator's own window class (`shell`, `improve~*` excluded), mirroring `sweep` safety rules. |

Write rate limit: 30/min per device (token bucket, `golang.org/x/time/rate`). Audit line: `{time, device_id, device_name, remote_ip, action, session, window, body_sha256, result}`, plus `agent` and `project` for dispatch. An action that acts writes a `result: "attempt"` line immediately before the side effect, then the result line; a refused request writes only the result line.

The window actions check and type into the window's main pane (the pane that is neither the chrome bar nor the HUD, `tmux.MainPaneOf`), never the active pane. Dispatch resolves the repo slug (`gh repo view`, with a timeout) and runs the runtime-dir git check before binding the session; only agent validation and the launch run under the session lock, so a slow `gh` never stalls fleet reads.

### Push (PR3)

| Method + path | Purpose |
|---|---|
| `GET /v1/push/vapid` | `{"public_key":"…"}` |
| `POST /v1/push/subscribe` | browser `PushSubscription` JSON; stored per device |
| `DELETE /v1/push/subscribe` | remove this device's subscription |
| `POST /v1/push/test` | send a test push to this device |

Triggers (from the fleetwatch bus): transition into `blocked`; transition to done with a PR (`pr_url` non-empty) or done without one; still blocked after `blocked_reminder_minutes` (once per blocked episode). De-dupe per window per episode; respect quiet hours and muted projects. Payload JSON: `{"title":"agentctl: go-engineer blocked","body":"agentctl · @523 · Bash","url":"/#/w/agentctl-agentctl/@523","tag":"<session>:<window>"}`. Library: `github.com/SherClockHolmes/webpush-go`. Expired subscriptions (404/410) are deleted. ntfy fallback: by default a post carries only the kind of event (no agent, project or window, no `Click:` header, since ntfy.sh topics are public and the app URL is a LAN address); with `ntfy_details` it posts the same title/body with `Click:` set to the app URL.

Push is a server-side request a paired device controls, so it is fenced: `POST /v1/push/subscribe` accepts only https endpoints on the default port on known push services (`*.push.apple.com`, `fcm.googleapis.com`, `*.push.services.mozilla.com`, `*.notify.windows.com`; `pushServiceHosts`); the push HTTP client refuses to dial loopback, private, link-local, CGNAT and unspecified addresses after DNS resolution (rebinding), refuses redirects and ignores proxy environment variables; and `/v1/push/test` returns a generic error, with the detail only in `serve.log`. The VAPID subject defaults to `https://github.com/blsmth/agentctl`; a configured `mailto:` value is passed to webpush-go without its scheme, which the library adds back.

### Live terminal (PR6)

`GET /v1/sessions/{s}/windows/{w}/terminal` → WebSocket (`github.com/coder/websocket`), a Device route advertised as feature `terminal`. The window is resolved through the fleet like the read routes; only fleet IDs reach tmux.

- **Origin**: the upgrade is refused (403 `forbidden_origin`) unless `Origin` is exactly this server's scheme and host. CORS does not cover WebSockets, so this is the CSRF defence for the route.
- **Mechanism** (`internal/tmux/mobileview.go`): a grouped viewer session `agentctl-mobileview-<random>` (`new-session -d -t =<home>`) with the target window selected, then `tmux -u attach-session -t =<viewer> -f ignore-size [-r]` inside a PTY (`github.com/creack/pty`), piped both ways. Never `join-pane`. The viewer has `prefix`/`prefix2` None and an empty `key-table`, so every key the phone sends reaches the pane and none reaches tmux (no command prompt, no new-window).
- **Size**: `ignore-size` keeps the phone client from resizing the shared window under the laptop. The phone renders at the window's real size: the first message is `{type:"size", cols, rows, read_only}` from `#{window_width} #{window_height}`, resent when a 2s poll sees a change (the PTY is resized to match). Two failed polls close the socket (1001 "window closed").
- **Protocol**: binary frames are raw terminal bytes both ways; text frames are JSON control messages (`size` from the server, `ping` either way every 20s).
- **Write gate**: input is accepted only with `serve.allow_write`. Read-only mode drops input frames and attaches with `-r`. In write mode each connection writes two audit lines (`action: "terminal"`, `connected`/`disconnected` with `bytes_in`/`bytes_out`, never content).
- **Cleanup**: on socket close, PTY EOF, `Server.Close` (shutdown and selfexec re-exec) the PTY process is killed and the viewer killed; `destroy-unattached` is also turned on once attached, so a viewer dies with its client even if serve crashes. Serve startup sweeps stale `agentctl-mobileview-*` sessions. At most 4 terminals (429 `too_many_terminals`).
- **Discovery**: `tmux.IsAnyViewerSession` (hub and phone viewers) is excluded from session listing, fleet discovery, hub leave, and home-session resolution; pane/window → session lookups map a viewer to its home session; approval popups never target a phone client.
- **Frontend** (`terminal.js`, route `#/t/<session>/<window>`): full-screen xterm.js sized to the server's cols/rows with the font scaled to the screen width (never `fit()`), pinch-zoom and pan as a fallback, a key bar (Esc, Tab, ^C, arrows, 1, 2, Enter) above the soft keyboard in write mode, a `read-only` badge otherwise, reconnect with backoff.

## Remote approvals

Away mode: when only the phone is at hand, the approval popups that gate `agentctl dispatch` and `agentctl cron add/resume/run --force` (always) and Bash commands (`bash_preview`) can be answered from the web app. Without this, a popup nobody sees blocks the agent until its 9-minute deadline and is then denied.

### Hook side (`internal/hooks`, `internal/remoteapproval`)

`runPopupWithDeadline` (both popup kinds) opens a remote approval in parallel with the tmux popup when `agentctl serve` is running, and otherwise behaves exactly as before.

- **Detection**: the control socket `<StateDir>/serve/control.sock` must accept within 200ms. Nothing accepting means "not running", and only the popup is used.
- **Guard** (`remoteapproval.Guard`), before anything is sent: `serve.pid` must be a regular 0600 file owned by the user and name a live process whose command line is `agentctl … serve`; where the OS reports the socket peer (macOS `LOCAL_PEERPID`, Linux `SO_PEERCRED`) it must be that pid, on both the probe and the approval connection; a `ping` op must report that pid and the same executable build (dev, inode, size, mtime) as the hook's own binary. Any failure logs and falls back to the popup alone. During the ~2s after `just install` before serve re-execs, the builds differ and the phone is not offered the approval.
- **Protocol**: one control-socket connection per approval. The hook writes `{op:"approval_request", id (128-bit random hex), kind ("dispatch"|"bash"|"cron"), asking_agent, session, window_id, target_agent, issue, prompt (fully resolved, including `--prompt-key` content), command, deadline}` and then reads a single line `{decision:"allow"|"deny", device, payload_sha256}` from that same connection. The decision is never read from anywhere else. Anything other than a well-formed allow or deny whose `payload_sha256` equals the hook's own hash of what it sent is an error.
- **First answer wins**: the laptop popup answering sends `{op:"approval_cancel", id, reason:"answered_on_laptop"}` and closes the connection. The phone answering cancels the popup's tmux client and closes the popup with `display-popup -C` on its client, and its verdict is returned. At the deadline or on a signal both are closed and the command is denied.
- **Errors never allow**: an error on the remote path drops it, and the popup alone decides. If the popup could not be shown (it would have passed through), the hook keeps waiting for the phone until the deadline.
- **No laptop client** (`selectPopupClient` finds none, so `popupTarget.Client` is empty): the popup is skipped and only the phone can answer, until the same deadline. If the remote path then fails, the popup is started as it would have been without serve.
- **Conflicting verdicts deny**: when the laptop and the phone both answer before the hook acts on either (the phone's answer arrives while the popup is being stopped, or both are pending at the deadline), the result is allow only if both said allow, and deny otherwise.
- **Tightening**: with serve running and no laptop tmux client, a dispatch whose popup previously could not be shown (and passed through silently, leaving Claude Code's permission flow to decide) now waits for the phone and is denied at the deadline if nobody answers. The same holds when the popup fails to launch while the phone path is open.
- `bashPreviewGate` still passes through when no human is attached; only popups that are shown (or, for dispatch, would be) go to the phone. Blocked state, desktop notice and the approval-pending marker from `beginApproval` are unchanged. Every outcome is logged with its source (`laptop`, `phone`, `timeout`).

### Serve side

- **Control socket**: `approval_request` keeps the connection open; the approval is held in memory, bound to that connection, and disappears when the connection drops (the hook gave up or exited), on `approval_cancel`, at deadline plus 10s, or when serve stops. Deadlines are clamped to 15 minutes and at most 32 approvals may be pending. `ping` returns `{pid, build}`. `approval_cancel` on any other connection is refused, and no op decides anything.
- **API**: `GET /v1/approvals` (Device) returns `{"approvals":[{id, kind, asking_agent, session, project, window_id, target_agent, issue, prompt, command, opened, deadline, seconds_remaining, payload_sha256}]}`. `payload_sha256` is the SHA-256 of the canonical JSON of the shown fields (`remoteapproval.PayloadHash`: id, kind, asking agent, session, window, target agent, issue, prompt, command). `POST /v1/approvals/{id}` (Write: CSRF, `allow_write`, rate limit, audit) takes `{decision, payload_sha256}`; 409 `approval_changed` when the hash differs, 409 `already_answered` when it was answered or resolved (resolved IDs are remembered 15 minutes), 409 `approval_gone` when the hook hung up before the answer reached it, 404 for unknown IDs. Feature `approvals` is always advertised (the list is readable without `allow_write`).
- **SSE**: `event: approval` (the list entry) when one opens, and once per pending approval on connect; `event: approval_resolved` `{id, session, window_id, outcome, decision?}` when it closes. Outcomes: `answered`, `answered_on_laptop`, `timeout`, `cancelled`, `gone`, `expired`, `server_stopping`.
- **Push**: on open, to every subscribed device: title "Approval needed", body "<asking agent> wants to dispatch <target agent>", "<asking agent> wants to schedule <target agent>" (cron; `target_agent` is the job's agent and `prompt` the job summary plus prompt) or "<asking agent> wants to run a command", url `/#/approvals/<id>`, urgency high, TTL = time to the deadline. Prompt and command text never go into a push (or into ntfy, which says only "approval needed"). Muted projects are respected; quiet hours are ignored, since an unanswered approval blocks work and is denied at the deadline. `serve.push.approvals` (default true, global-only) turns these pushes off.
- **Audit** (`audit.jsonl`): `approval_requested` when one opens; for an answer the usual write lines (`approval` with `attempt`, then the result) carrying device, `decision` and the `payload_sha256` the phone sent; `approval_resolved` with the outcome when it ended any other way. Lines never contain prompt or command text.

### Web app

`#/approvals` lists pending approvals and `#/approvals/<id>` shows one (the push target). Each shows the asking agent and window (linked), target agent, issue, a countdown on the phone's clock, the full prompt and the command (text only, monospace, scrollable), and Approve / Deny. Both go through a confirm sheet that repeats everything that will run, and send the payload hash of the version shown; a 409 or 404 toasts "Already answered or changed" and refreshes. The Fleet view shows a red "N approval(s) waiting" banner, the asking window's detail view shows its approval inline, and the bottom nav has an Approvals tab with a count badge (only when the feature is advertised). The list is refetched on every SSE (re)connect and every 15s as a backstop.

### Security model and residual risk

- Decisions come only from `POST /v1/approvals/{id}` by a paired device with `allow_write`, CSRF header and rate limit, and are bound to the payload hash of what the phone displayed. The device token is stored only hashed, so a same-uid agent cannot read one from disk.
- The hook only trusts its own connection, verified as described above, and requires the decision to carry the hash of its own payload.
- The Bash validator (`checkServeTamper` in `internal/hooks/validate.go`, part of the global deny list) blocks commands that reference the serve state dir, `control.sock` or `serve.pid`; stop, reload or reinstall the serve LaunchAgent or systemd unit; `pkill`/`killall` agentctl, or `kill` an `agentctl serve` found some other way; `launchctl` with the serve label in either order, across lines; and `agentctl serve install|uninstall|pair|ca|start|stop|toggle` (also by path or `"$(which agentctl)"`).
- These rules make some operations human-only, including `launchctl kickstart … com.blsmth.agentctl.serve` (restarting serve) and `agentctl serve install|uninstall|pair|ca|start|stop|toggle`. The human's prefix+`W` and the hub's `S` key and indicator run outside Claude Code. An agent that injects keystrokes with `tmux send-keys` (into the hub pane, or `send-keys -K -c <client> C-b W` to fire the prefix binding) is outside what a string-based rule can see, like the program-on-disk case below. Agents read serve's logs with the read-only `agentctl serve logs [-n N] [--audit] [--follow]`, which prints the tail of `serve.log`, or of `audit.jsonl` with `--audit`; the deny reason points to it.
- **Residual risk**: every agent runs as the same uid as serve. A determined agent can write a program to disk and run it, which the validator cannot see into: it could stop serve, rewrite `serve.pid`, and bind an impostor socket named like the real one that answers the ping with a copied build identity and the right pid; or ask the real control socket to open a pairing window and pair itself as a device over loopback. These are bypasses of the same-uid boundary, not of this protocol, and they are equally available without away mode (such an agent can already run anything it likes). The checks above make an accidental or naive bypass fail closed: a fake or broken socket can at worst cause a deny or a timeout.

## Web app (PR5)

Embedded under `internal/serve/web/` (`go:embed`), served at `/`. Hash-based routing. Mobile-first, dark mode by default (follows `prefers-color-scheme`), safe-area insets, large touch targets.

- `manifest.webmanifest` (`display: standalone`, icons 192/512 PNG generated once and committed), `sw.js` service worker: handles `push` (showNotification with tag) and `notificationclick` (focus/open the payload URL). Cache only the app shell; never cache API responses.
- **Fleet row title**: the headline comes from `fleet.Recap` (exposed as `recap`/`recap_source` on each window): the ticket title for a ticket dispatch, else the first sentence of Claude Code's away summary or the AI title, else the agent name. The agent name moves to a muted chip, and the full recap (two lines max) replaces the raw activity line; activity shows only when no recap exists.
- **Scheduled runs**: windows launched by `agentctl cron` carry `scheduled_by` (job name) and `scheduled_at` (RFC 3339) on each window; both are omitted for other windows. Their fleet rows get a muted `⏲ <job>` chip and, with no usable parent, sit in a "Scheduled jobs (N)" bucket instead of "Tasks from other sessions"; the window view shows "Scheduled run: <job>, fired Wed 18 Nov 07:00" under the task line.
- **Fleet** (`#/`): groups by project in the saved order shared with the hub (`group-order.json`; projects not in it follow alphabetically), not by urgency: each group's rollup colour and the counts on a folded header carry the attention state. Each group in `/v1/fleet` carries its `key`. Long-press (~400ms) a group header and drag to reorder (touch and mouse, Pointer Events; a tap still folds, a scroll still scrolls). While dragging, groups render at header height so every target stays on screen, the page autoscrolls near the top and bottom edges, and renders are held until the drop. The drop is shown at once and saved with `POST /v1/group-order`; a refused save puts the old order back and toasts. Alt+ArrowUp / Alt+ArrowDown on a focused header is the keyboard path. Windows inside a group stay in attention order. Row: state glyph/colour, agent, window id, mode, activity line, time in state, context %. Blocked rows red, pinned. Live via SSE; reconnect with backoff; banner "laptop unreachable since HH:MM" when SSE is down > 15s.
- **Window detail** (`#/w/{session}/{window}`): a column that fills the screen above the tab bar. Header: back link, agent, a state pill ("working for 3m"), project and branch, a task line (task title, else issue, else activity) and chips for what is known (PR link, context %, cost, short model, window id and mode). The pane (refresh every 2s while visible and on SSE window events) takes the remaining height; its bar has a liveness chip ("updated 12s ago", or "offline since HH:MM" in the blocked colour while fetches fail or the live feed has been down > 15s) and a Reflow / Exact / Live segment (Live opens the live terminal; feature-detected). Reflow wraps lines with a hanging indent, leaves out an empty prompt box and folds Claude Code's footer into a "Claude Code status" toggle; a "New output" pill appears when output changes while scrolled up. Exact drops the footer, sizes the terminal rows to what fits, and pans sideways once the font reaches its 8px floor. State-aware actions:
  - awaiting input: above the pane, the dialog's question and one full-width row per option (the focused, recommended one primary) plus "Dismiss (Esc)" (uses `answer` with the hash it was shown)
  - idle/done: Reply box with a full-width Reply; a small Close window text button below
  - working: a Queue message composer (`queue`, when the server advertises it) beside a small Interrupt (confirm sheet); after an interrupt, the reply box that follows within 10s gets focus
  - closed (404): actions go, "This window was closed" and a way back to the Fleet
- **Dispatch** (`#/dispatch`): agent picker, project picker, multi-line prompt, confirm sheet showing exactly what will be sent.
- **Settings** (`#/settings`): server version, device name, enable notifications (requests permission, subscribes, test push button), write mode status, an explanation when not installed to the Home Screen ("Add to Home Screen to enable notifications").
- All writes show a confirm sheet; errors (409 dialog changed / not idle) show a toast and refresh the view.

## CLI

```
agentctl serve                 # run in the foreground (what launchd runs)
agentctl serve install         # write ~/Library/LaunchAgents/com.blsmth.agentctl.serve.plist (KeepAlive, RunAtLoad), bootstrap it
agentctl serve uninstall
agentctl serve start           # bootstrap the installed LaunchAgent (kickstart if loaded); human-only
agentctl serve stop            # bootout the LaunchAgent, plist kept: back at next login; human-only
agentctl serve toggle          # stop if a serve process is alive, else start; human-only
agentctl serve status          # running?, pid, listen URL, version, devices count, cert expiry, CA fingerprint
agentctl serve pair [--name N]
agentctl serve devices [rm <id|name>]
agentctl serve logs [-n N] [--audit] [--follow]   # read-only tail of serve.log / audit.jsonl
```

Linux: `install` prints a systemd user unit instead of writing launchd (no need to fully support Linux, but do not break the build). `start`/`stop`/`toggle` print the matching `systemctl --user start|stop agentctl-serve`.

### Start, stop and the toggle (`internal/servectl`)

`internal/servectl` is the leaf package behind `serve start|stop|toggle`, prefix+`W` and the hub indicator (internal/serve imports the TUI, so the TUI cannot import it; servectl owns the label, `serve.pid` and `control.sock` names and serve uses its constants). Every `launchctl` call goes through `launchd.Agent`'s runner (`Bootstrap`, `Bootout`, `Kickstart`, `Loaded`, `Installed`) and every signal through `Controller.Terminate`; inside a test binary `servectl.New` refuses both.

- **stop** boots the job out and leaves the plist on disk, so serve comes back at the next login or with `start`. It does not `launchctl disable` the job: a stop that silently outlived a reboot would leave serve installed but never running, and remote approvals quietly off; `uninstall` is the permanent stop. A serve process launchd does not manage (run in a terminal) gets SIGTERM, but only after `pidfile.Running` confirmed it is `agentctl … serve`.
- **start** kickstarts a loaded job (`-k` when a process is alive but its control socket is dead) or bootstraps the installed plist, then waits up to 10s for the control socket and reports the URL. With no plist it fails with "run `agentctl serve install`" rather than spawning a detached server: that process would have no supervisor, no log capture and would not come back at login, and it would be one more self-spawn path.
- **toggle** stops when `serve.pid` names a live serve process (answering or not), else starts.
- **prefix+`W`** runs `agentctl serve toggle --message-client '#{client_name}'` in a background `run-shell` and shows the result as a tmux message ("serve stopped (…)", "serve started at https://…", or the error).
- **Hub indicator**: the hub list's title row shows `📱 serve` (green, running), `📱 off` (dim) or `📱 stuck` (yellow: the pid is alive but the control socket refuses connections, so phone approvals are not offered). It is fed by a probe on the hub's 1s poll tick (`servectl.Probe`: one local socket connect and, only when that fails, the pidfile check that tells "stuck" from "off"; no launchctl, nothing per render, independent of the monitor). Clicking it or pressing `S` in the list toggles serve and flashes the outcome in the footer.

These paths run outside Claude Code. For agents, `serve start|stop|toggle` and `launchctl` against the serve label are denied by the serve-tamper rules (see "Security model and residual risk").

Hot reload: use `internal/selfexec` exactly like the hub/monitor, so `just install` upgrades the running server (same PID, same listener is fine to re-open). Pidfile-guarded: a second `serve` exits with a clear message.

## PR plan

1. **Foundation** — config block (global-only), `internal/serve` server skeleton, CIDR filter, CA + leaf certs, control socket, pairing (bootstrap listener, QR, mobileconfig), devices + cookie auth + CSRF middleware, `/healthz`, placeholder page at `/`, CLI (`serve`, `install`, `uninstall`, `status`, `pair`, `devices`), selfexec hot reload, pidfile, logging. Commits this design doc.
2. **Read API + events + keep-awake** — shared `fleet.Row` → `Window` conversion (and `status --json` uses it), `/v1/fleet|windows|pane|agents|projects`, fleetwatch bus, SSE, caffeinate management.
3. **Push** — VAPID, subscriptions, triggers off the bus, quiet hours/mute, reminders, ntfy fallback.
4. **Write API** — answer/reply/interrupt/dispatch/close with guards, rate limit, audit log.
5. **Web app** — the PWA described above.
6. **Live terminal** — WebSocket + PTY + grouped viewer session + xterm.js.

Order: 1 → (2 ∥ 4) → (3 ∥ 5) → 6.

## Testing expectations (all PRs)

- Table-driven unit tests per package; `httptest` for handlers with a fake fleet source (define small interfaces: `FleetSource`, `PaneSource`, `KeySender`, `Dispatcher` so handlers are testable without tmux).
- Security tests are mandatory: CIDR rejection, missing/invalid/revoked cookie, CSRF header missing, pairing code reuse and expiry, `allow_write:false` → 403, `answer` with stale hash → 409, `reply` to a working pane → 409.
- No test may bind a fixed port, touch the real state dir, or require tmux unless it is skipped when tmux is absent.
