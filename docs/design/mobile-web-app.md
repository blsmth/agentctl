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
- **One process per machine**: `agentctl serve`, launchd-managed. It does not depend on per-project monitors: fleet data comes from `herd` (tmux + windowstate), exactly like `agentctl status --json`. It is the natural home for the per-machine process in the monitor-removal plan (PR9 `metrics serve` can later fold in; not in scope here).
- **Write actions are in scope from the start** (answer dialogs, reply, interrupt, dispatch), behind `serve.allow_write` and strong guards.
- **Keep-awake**: on by default. While any agent anywhere is `working` or `blocked`, `serve` holds a `caffeinate -s` child (prevents system sleep on AC power only; macOS ignores `-s` on battery). Released when the fleet is all idle/done. `serve.keep_awake: false` disables.
- **Push transports**: Web Push (VAPID) is primary. ntfy is an optional fallback (`serve.ntfy_topic`) for when the PWA isn't installed or Web Push misbehaves. Push payloads never contain pane text or prompt text.

## Architecture

```
Phone (Safari / Home Screen PWA)
   │  HTTPS (local CA, trusted on the phone) over home LAN or UDM VPN
   ▼
agentctl serve  (internal/serve; launchd LaunchAgent; selfexec hot reload)
   ├─ reads: herd (tmux list-sessions/list-windows + windowstate), tmux capture-pane, agent discovery
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
| `GET /v1/projects` | `{"projects":[{"name":"agentctl","dir":"/Users/…","session":"agentctl-agentctl"}]}` (live sessions + registry; dispatch targets) |
| `GET /v1/events` | SSE stream (below) |

`Group` = `{"session","project","dir","rollup":"blocked|working|done|idle","windows":[Window]}`, ordered with `herd.OrderGroups`.
`Window` = the same field names as `agentctl status --json` rows (`session, project, window_id, window_index, agent, state, done, mode, context_pct, branch, worktree_path, parent, activity`) plus `pr_url`, `issue`, `blocked_since`, `state_since`, `pending_tool` where available. Implement one shared conversion from `herd.Row` so `status --json` and `serve` cannot drift (refactor `status --json` to use it).

### Events (PR2)

`GET /v1/events` — SSE. On connect, one `event: fleet` with the full `/v1/fleet` body. Then:
- `event: window` `data: Window` — a window appeared or a **rendered** field changed (state, done, activity, context_pct, pr_url…). Hooks rewrite windowstate on every tool call, so diff the rendered `Window`, never raw file events.
- `event: gone` `data: {"session","window_id"}`.
- `: ping` comment every 20s.
Source: an internal event bus (`internal/serve/fleetwatch`) that fsnotify-watches every live session runtime dir `windows/` (and re-discovers sessions every 5s, since sessions come and go), debounces 200ms, rebuilds rows with `herd`, and diffs. PR3 (push) and keep-awake subscribe to the same bus, receiving typed transitions `{window, from, to}`.

### Write (PR4) — all require `allow_write: true`, CSRF header, and are audit-logged

| Method + path | Body | Behavior and guards |
|---|---|---|
| `POST …/windows/{w}/answer` | `{"key":"1","dialog_hash":"…"}` | Re-capture and re-classify the pane **immediately before sending**. Must still be awaiting input and the dialog hash must equal `dialog_hash`, and `key` must be one of the dialog's option keys or `"esc"`. Otherwise 409 `dialog_changed`. Then `send-keys` the key. The window lock does not cover Claude Code's redraw, so the answered hash is remembered per window for 5s and a repeat (double tap, second phone) gets 409 `already_answered`; the answer also waits up to 1s for the dialog to leave the pane before releasing the lock. |
| `POST …/windows/{w}/reply` | `{"text":"…"}` | Only when the pane classifies idle (prompt ready) and windowstate is not `working`. Sends literal text (`send-keys -l`) then Enter. Text > 4000 chars rejected (413). 409 `not_idle` otherwise. Text whose first non-space character is `!` (Claude Code bash mode: runs a shell command with no permission dialog), `/` (slash commands such as `/permissions`, `/hooks`, `/add-dir`) or `#` (memory) is rejected with 400 `reply_is_command`: a reply is text for the agent, never a command. |
| `POST …/windows/{w}/interrupt` | `{}` | Sends Escape. Allowed in any state. |
| `POST /v1/dispatch` | `{"agent","project_dir","prompt","model"?}` | Ad-hoc dispatch, same code path as `agentctl dispatch --agent X --prompt Y` with `--dir project_dir`. Export the needed function from `internal/cmd` (or move it to `internal/dispatch`) instead of shelling out. `project_dir` must be one of `/v1/projects` dirs. Returns `{"window_id","session","branch"}`. |
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
- **Discovery**: `tmux.IsAnyViewerSession` (hub and phone viewers) is excluded from session listing, herd discovery, hub leave, and home-session resolution; pane/window → session lookups map a viewer to its home session; approval popups never target a phone client.
- **Frontend** (`terminal.js`, route `#/t/<session>/<window>`): full-screen xterm.js sized to the server's cols/rows with the font scaled to the screen width (never `fit()`), pinch-zoom and pan as a fallback, a key bar (Esc, Tab, ^C, arrows, 1, 2, Enter) above the soft keyboard in write mode, a `read-only` badge otherwise, reconnect with backoff.

## Web app (PR5)

Embedded under `internal/serve/web/` (`go:embed`), served at `/`. Hash-based routing. Mobile-first, dark mode by default (follows `prefers-color-scheme`), safe-area insets, large touch targets.

- `manifest.webmanifest` (`display: standalone`, icons 192/512 PNG generated once and committed), `sw.js` service worker: handles `push` (showNotification with tag) and `notificationclick` (focus/open the payload URL). Cache only the app shell; never cache API responses.
- **Fleet** (`#/`): groups by project, ordered by attention (blocked > working > done > idle). Row: state glyph/colour, agent, window id, mode, activity line, time in state, context %. Blocked rows red, pinned. Live via SSE; reconnect with backoff; banner "laptop unreachable since HH:MM" when SSE is down > 15s.
- **Window detail** (`#/w/{session}/{window}`): header (agent, branch, PR link opening GitHub, context %), pane view rendered with xterm.js in read-only mode (refresh every 2s while visible and on SSE window events), and a state-aware action bar:
  - awaiting input: dialog text + one button per option + Esc (uses `answer` with the hash it was shown)
  - idle/done: Reply box; Close window
  - working: Interrupt (confirm sheet)
  - always: "Live terminal" (PR6; hidden until the endpoint exists — feature-detect via `/healthz` `features` list)
- **Dispatch** (`#/dispatch`): agent picker, project picker, multi-line prompt, confirm sheet showing exactly what will be sent.
- **Settings** (`#/settings`): server version, device name, enable notifications (requests permission, subscribes, test push button), write mode status, an explanation when not installed to the Home Screen ("Add to Home Screen to enable notifications").
- All writes show a confirm sheet; errors (409 dialog changed / not idle) show a toast and refresh the view.

## CLI

```
agentctl serve                 # run in the foreground (what launchd runs)
agentctl serve install         # write ~/Library/LaunchAgents/com.blsmth.agentctl.serve.plist (KeepAlive, RunAtLoad), bootstrap it
agentctl serve uninstall
agentctl serve status          # running?, pid, listen URL, version, devices count, cert expiry, CA fingerprint
agentctl serve pair [--name N]
agentctl serve devices [rm <id|name>]
```

Linux: `install` prints a systemd user unit instead of writing launchd (no need to fully support Linux, but do not break the build).

Hot reload: use `internal/selfexec` exactly like the hub/monitor, so `just install` upgrades the running server (same PID, same listener is fine to re-open). Pidfile-guarded: a second `serve` exits with a clear message.

## PR plan

1. **Foundation** — config block (global-only), `internal/serve` server skeleton, CIDR filter, CA + leaf certs, control socket, pairing (bootstrap listener, QR, mobileconfig), devices + cookie auth + CSRF middleware, `/healthz`, placeholder page at `/`, CLI (`serve`, `install`, `uninstall`, `status`, `pair`, `devices`), selfexec hot reload, pidfile, logging. Commits this design doc.
2. **Read API + events + keep-awake** — shared `herd.Row` → `Window` conversion (and `status --json` uses it), `/v1/fleet|windows|pane|agents|projects`, fleetwatch bus, SSE, caffeinate management.
3. **Push** — VAPID, subscriptions, triggers off the bus, quiet hours/mute, reminders, ntfy fallback.
4. **Write API** — answer/reply/interrupt/dispatch/close with guards, rate limit, audit log.
5. **Web app** — the PWA described above.
6. **Live terminal** — WebSocket + PTY + grouped viewer session + xterm.js.

Order: 1 → (2 ∥ 4) → (3 ∥ 5) → 6.

## Testing expectations (all PRs)

- Table-driven unit tests per package; `httptest` for handlers with a fake fleet source (define small interfaces: `FleetSource`, `PaneSource`, `KeySender`, `Dispatcher` so handlers are testable without tmux).
- Security tests are mandatory: CIDR rejection, missing/invalid/revoked cookie, CSRF header missing, pairing code reuse and expiry, `allow_write:false` → 403, `answer` with stale hash → 409, `reply` to a working pane → 409.
- No test may bind a fixed port, touch the real state dir, or require tmux unless it is skipped when tmux is absent.
