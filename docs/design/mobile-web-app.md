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
    "quiet_when_laptop_active": true, // no pushes while the laptop is in use (HID idle / tmux client input); see Laptop suppression
    "laptop_idle_seconds": 120,      // laptop input this recent counts as in use
    "quiet_hours": "23:00-07:00",    // local time; empty = none
    "muted_projects": []
  },
  "ntfy_topic": "",                  // optional; posts to https://ntfy.sh/<topic>. ntfy.sh topics are public: use a long random name
  "ntfy_details": false,             // off: ntfy posts name only the kind of event (no agent, project, window or Click URL)
  "public_hostname": "",             // optional; e.g. "mac.agentctl.example.com": Let's Encrypt wildcard via Cloudflare DNS-01 (see Public certificate)
  "acme": {"email": "", "staging": false}
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
- With `serve.public_hostname` only: `cloudflare.token` (0600, created by the human) and `acme/` (0700, certmagic storage). See [Public certificate](#public-certificate-cloudflare-dns-01).

## Public certificate (Cloudflare DNS-01)

Optional. With `serve.public_hostname` set, serve also holds a Let's Encrypt certificate so artifact links open in any browser on the LAN or VPN without installing the local CA profile. Nothing is exposed to the internet: the name resolves to a LAN address, the challenge is DNS-01 (no inbound connection from Let's Encrypt), and `allow_cidrs`, device auth, CSRF and the artifact sandbox are unchanged.

How it works (`internal/serve/publiccert.go`, `acme.go`; [certmagic](https://github.com/caddyserver/certmagic) with [libdns/cloudflare](https://github.com/libdns/cloudflare)):

- **Wildcard.** The certificate is for the parent of the hostname: `macbookpro.agentctl.blsmth.com` gets `*.agentctl.blsmth.com`. The exact name is not on it, so the machine name never appears in the public Certificate Transparency logs. `public_hostname` therefore needs at least three labels (never `*.com` or `*.blsmth.com`).
- **SNI routing.** `GetCertificate` gives the public certificate only to a TLS SNI equal to `public_hostname`. Every other name under the wildcard, IPs, `.local`, `serve.hostnames` and no SNI get the local-CA leaf, so paired phones keep working with no re-pair. With the key unset the listener's TLS config is exactly the old one.
- **Never blocks startup.** serve listens on the local leaf at once; certmagic obtains the certificate in the background, stores it in `acme/`, retries failures with backoff and renews before expiry. Failures (no token, a loose token, a Cloudflare API error, a rate limit) are logged to `serve.log` and shown by `serve status`. The token is read from disk on every DNS call, so fixing it needs no restart; it is never logged or put in config.
- **Advertising.** Only artifact content links move. While a valid, publicly trusted certificate is held (by the Mac's clock), `agentctl artifact add|update|show|url|open` and the hub print links on `public_hostname` (the CLI asks the running server over the control socket, only when the key is set), and `serve status` adds a `public url` row (`public_url` in JSON). Before that, and with the key unset, links use the old host. A staging certificate (`serve.acme.staging`) is served for the name but never advertised.
- **The app stays on the LAN address.** The device cookie is host-only and set at pairing on the LAN address, so a phone has no cookie on the public name. App links therefore keep the LAN address: the `serve status` `url` row, the `app:` link that `artifact add` prints, ntfy `Click` links, pairing URLs and the CA bootstrap listener. Artifact content links need no cookie (the id is the credential), which is the point of the public name. Moving the app to the public origin would mean pairing there too, and is out of scope.
- **`serve status`** shows `public host`, `public cert name` (the wildcard), and `public cert`: `none`, `obtaining`, `valid, expires <date>` or `error`, plus `public cert error` with the last failure (a valid certificate with an error means a renewal attempt failed; certmagic keeps retrying).

Setup (human only: agents cannot touch the serve state dir or restart serve):

1. **DHCP reservation.** Reserve the Mac's LAN IP on the router (UDM: Settings → Client Devices → the Mac → Fixed IP), so the DNS record stays right.
2. **DNS record.** In Cloudflare, add an `A` record for `macbookpro.agentctl` on `blsmth.com` pointing at that LAN IP, **DNS only (grey cloud), not proxied**. Only that one name needs a record; the wildcard certificate needs none.
3. **API token.** Cloudflare → My Profile → API Tokens → Create Token → "Edit zone DNS" template: permission Zone · DNS · Edit, zone resources Include · Specific zone · `blsmth.com` only.
4. **Token file.** Put the token alone on one line in `~/.local/state/agentctl/serve/cloudflare.token` and `chmod 600` it. A group- or world-readable file is refused.
5. **Config.** In the global `~/.config/agentctl/config.json`: `"serve": {"public_hostname": "macbookpro.agentctl.blsmth.com", "acme": {"email": "you@example.com"}}`. Try `"staging": true` first if you are iterating on the setup.
6. **Restart serve:** `launchctl kickstart -k gui/$(id -u)/com.blsmth.agentctl.serve` (or stop and rerun `agentctl serve` in the foreground). Then `agentctl serve status` until `public cert: valid`, and `agentctl serve logs` for progress.
7. **DNS rebinding on the UDM.** Some resolvers drop public answers that point at private addresses. If `macbookpro.agentctl.blsmth.com` does not resolve on the LAN or VPN, add a local DNS record on the UDM (Settings → Routing → DNS → Create Entry: `macbookpro.agentctl.blsmth.com` → the LAN IP), or allow the domain in whatever rebind protection is on.

Edge cases:

- **Hostname changed in config.** Takes effect on restart: the new wildcard is obtained and the old one is no longer loaded, served or advertised (its files stay in `acme/`, harmless).
- **Mac IP changed.** A DNS-only problem: the certificate does not name the IP. Update the A record (and the UDM override), or keep the DHCP reservation.
- **Clock skew.** A certificate outside its validity window by the Mac's clock is reported as `error` ("check the clock") and not advertised.
- **Rate limits.** Let's Encrypt limits duplicate certificates per week; use `acme.staging` while testing. certmagic backs off on failure.

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
| `GET /v1/templates?dir=<project>` | `{"templates":[{"name","description"?,"scope":"global"\|"project","agent"?,"model"?,"dir"?,"effort"?,"prompt"}]}`; what `agentctl template list` shows from that project (`prompttemplate.List`, project shadows global), `dir` with `~/` expanded. `dir` must be a `/v1/projects` dir; without it only global templates are listed. No templates is `[]`. |
| `GET /v1/launch-options?agent=&project_dir=` | `{"backends":[{"name","kind","default"}],"models":{<backend>:{"models":[{"value","note","default"}],"model_list_error"?}},"efforts":[…],"defaults":{"backend","model","effort"}}`; same choices as the tmux launch popup (`internal/launchopts`). Ollama lists are fetched live (3s timeout, cached 60s); on failure only the default model is listed with `model_list_error`. Feature `launch-options`. |
| `GET /v1/projects` | `{"projects":[{"name":"agentctl","dir":"/Users/…","session":"agentctl-agentctl"}]}` (live sessions + registry; dispatch targets) |
| `GET /v1/events` | SSE stream (below) |

`Group` = `{"session","project","dir","rollup":"blocked|working|done|idle","windows":[Window]}`, ordered with `fleet.OrderGroups`.
`Window` = the same field names as `agentctl status --json` rows (`session, project, window_id, window_index, agent, state, done, mode, context_pct, branch, worktree_path, parent, activity`) plus `pr_url`, `issue`, `blocked_since`, `state_since`, `pending_tool` where available. Implement one shared conversion from `fleet.Row` so `status --json` and `serve` cannot drift (refactor `status --json` to use it).

### Artifacts

Feature `artifacts`. See [artifacts.md](artifacts.md) for the store and the sandbox.

| Method + path | Access | Response |
|---|---|---|
| `GET /v1/artifacts?project=&since=` | Device | `{"artifacts":[Artifact]}`, most recently updated first. `project` matches the display name or the project dir; `since` is RFC 3339 or a duration back from now (`24h`, `7d`; anything else is 400). |
| `GET /v1/artifacts/{id}` | Device | `Artifact`; 404 `not_found` for an unknown or malformed id. |
| `DELETE /v1/artifacts/{id}` | Write | Body `{}`. Deletes it and all revisions; `{"ok":true}`. Audited as `artifact-delete`. |
| `POST /v1/artifacts/{id}/pin` | Write | `{"pinned":bool}` (required). Returns the `Artifact`. Audited as `artifact-pin`. |
| `POST /v1/artifacts/{id}/comments` | Write | `{"text":string,"rev":int}` (`rev` 0 = latest; text at most 4000 characters). Stores the comment and types it into the author window like reply (idle) or queue (working). Returns `{"comment":…,"delivery":"delivered"\|"queued"\|"saved","reason"?}`; `saved` means stored only (window closed, another account, busy). Feature `artifact-comments`. Audited as `artifact-comment`. |
| `GET /a/{id}/{rev}/{path...}` | Public (id) | For a 26-character capability id: the content itself, no cookie (`latest` is `private, no-cache` with an ETag; a number is `immutable`), CORS like the token route. For a legacy 10-character id: needs the device cookie, resolves `rev` (a number or `latest`) and redirects (302, `no-store`) to the token URL of the same file for this device, query kept. A legacy id without a valid cookie is the same 404 page as an unknown id. `/a/{id}/{rev}` and `/a/{id}` redirect to the `/` and `latest/` forms under the same rule. |
| `GET /a/t/{token}/{id}/{rev}/{path...}` | Public (token) | Content of revision `rev` (a number; the token must grant exactly this artifact and revision, be unexpired and belong to a still-paired device, else 403). An empty path is the entry. Markdown and text entries are rendered pages (`?raw=1` for the bytes). `immutable`. `Access-Control-Allow-Origin: *` without credentials. |

Every response under `/a/`, errors included, carries `Content-Security-Policy: sandbox allow-scripts allow-forms allow-popups allow-modals allow-downloads; default-src 'self' https: data: blob: 'unsafe-inline' 'unsafe-eval'; connect-src 'self' https:` (never `allow-same-origin`), `nosniff`, `Referrer-Policy: no-referrer`, `Cross-Origin-Resource-Policy: cross-origin` and `X-Frame-Options: SAMEORIGIN`. Traversal (`..`, encoded or not, `%2f`, `//`, symlinks) is 400. The PWA frames `raw_url` (or a revision's `raw_url`): the direct `/a/<id>/<n>/` for a capability id, a token URL for a legacy one. See "Security model" in [artifacts.md](artifacts.md#security-model-the-id-is-the-capability-public-links).

`Artifact` = the `meta.json` fields (`id, slug, title, description, kind, entry, created, updated, pinned, no_notify, project_dir, project, agent, window_id, session, session_id, branch`) plus `latest`, `rev_count`, `url` (the app route `/#/artifacts/<id>`), `raw_url` (the capability URL of the latest revision's entry, minted for the requesting device, valid about 24h; refetch for a fresh one) and `revs` `[{n, created, size, note, raw_url}]`.

### Host status

`GET /v1/host` (Device auth, feature `host`) backs the phone's Host screen (Settings → Host status). It returns `server_time` (RFC 3339, for client-side clock skew; the client measures round trip itself), `host` (hostname, os/arch, uptime, cores, load 1/5/15, `cpu_percent`, memory total/used/available, swap total/used, and on macOS `memory_pressure` of normal/warn/critical via sysctl), `agentctl` (serve version and uptime, `tmux_windows`) and `processes`: `groups` (count, summed CPU% of one core and RSS for `claude` and `agentctl chrome-bar|serve|hub-view|hud|metrics|other`) plus the top 5 processes by CPU. Fields a platform cannot provide are omitted, never errors; Linux builds and reports what gopsutil gives it.

Stats come from gopsutil (no cgo, no `ps`/`top`). A snapshot is cached for 3 seconds and shared by all callers, so several open phones cost one collection per interval; CPU percentages (system and per process) are deltas between consecutive collections, so the first request after an idle period omits `cpu_percent` and reports 0 per process. The tmux window count is cached on the same interval. The screen polls every 4 seconds and stops when it is left or the page is hidden.

### Events (PR2)

`GET /v1/events` — SSE. On connect, one `event: fleet` with the full `/v1/fleet` body. Then:
- `event: window` `data: Window` — a window appeared or a **rendered** field changed (state, done, activity, context_pct, pr_url…). Hooks rewrite windowstate on every tool call, so diff the rendered `Window`, never raw file events.
- `event: gone` `data: {"session","window_id"}`.
- `event: artifact` `data: {"id","action":"add|update|rm"}` — the artifact store changed (pin and title changes are `update`). Refetch `/v1/artifacts` on it, and on any `fleet` event (sent when a client fell behind and dropped events).
- `: ping` comment every 20s.
Source: an internal event bus (`internal/serve/fleetwatch`) that fsnotify-watches every live session runtime dir `windows/` (and re-discovers sessions every 5s, since sessions come and go), debounces 200ms, rebuilds rows with `fleet`, and diffs. PR3 (push) and keep-awake subscribe to the same bus, receiving typed transitions `{window, from, to}`.

### Write (PR4) — all require `allow_write: true`, CSRF header, and are audit-logged

| Method + path | Body | Behavior and guards |
|---|---|---|
| `POST …/windows/{w}/answer` | `{"key":"1","dialog_hash":"…"}` | Re-capture and re-classify the pane **immediately before sending**. Must still be awaiting input and the dialog hash must equal `dialog_hash`, and `key` must be one of the dialog's option keys or `"esc"`. Otherwise 409 `dialog_changed`. Then `send-keys` the key. The window lock does not cover Claude Code's redraw, so the answered hash is remembered per window for 5s and a repeat (double tap, second phone) gets 409 `already_answered`; the answer also waits up to 1s for the dialog to leave the pane before releasing the lock. A multi-select AskUserQuestion is submitted with `{"key":"submit","select":["1","3"],"dialog_hash":"…"}`: the server toggles the boxes that differ, arrows to the Next/Submit row and presses Enter, sending one key at a time and re-parsing the pane after each; if a toggle did not flip its box or an arrow did not move the cursor one row (or the question changed), it stops with 409 `dialog_changed`. The review page ("Ready to submit your answers?" then "1. Submit answers") is matched structurally, not by its phrases anywhere in the pane. Claude Code's folder-trust dialog comes back as `dialog.kind: "trust"` with `folder` and two options keyed `trust` and `exit` in screen order (`panestate.ParseTrustDialog`, current unnumbered and older numbered layouts; a pane where it lingers above Claude Code's exit message has no dialog). It is answered with `{"key":"trust"|"exit","dialog_hash":"…"}` only (`esc`, numbers and `submit` get 409, since Esc on it exits): the server reads the ❯ row, sends one arrow at a time and re-captures until the same hash shows the cursor one row on, and sends Enter only with the cursor on the chosen row (Claude Code wraps the arrows, so the move count never relies on wrapping). Same lock, hash, TTL, settle and audit as any answer. |
| `POST …/windows/{w}/reply` | `{"text":"…"}` | Only when the pane classifies idle (prompt ready) and windowstate is not `working`. Sends literal text (`send-keys -l`) then Enter. Text > 4000 chars rejected (413). 409 `not_idle` otherwise. Text whose first non-space character is `!` (Claude Code bash mode: runs a shell command with no permission dialog), `/` (slash commands such as `/permissions`, `/hooks`, `/add-dir`) or `#` (memory) is rejected with 400 `reply_is_command`: a reply is text for the agent, never a command. The message points at the `command` action for slash commands. |
| `POST …/windows/{w}/queue` | `{"text":"…"}` | The explicit opt-in for messaging a **working** agent (feature `queue`, advertised with `write`): Claude Code queues typed input and delivers it after the current step. Accepts a `working` or `idle` record with a pane that classifies busy or idle (the turn may end between tap and send); a `blocked` record, a dialog or an unrecognised pane is 409 `not_idle`. Same length limit, `reply_is_command` prefix guard and guarded inject as `reply`, audited as action `queue`. |
| `POST …/windows/{w}/command` | `{"text":"/compact focus on the tests"}` | The explicit way to run a Claude Code slash command (feature `command`, advertised with `write`), audited as action `command`. After trimming, the text must start with `/`, be one line, pass the control-character check, be at most 2000 characters (413) and have a command name matching `^[A-Za-z0-9][A-Za-z0-9:_-]*$` (plugin commands such as `/plugin:skill`); anything else is 400. `!` (bash mode) and `#` (memory) stay refused everywhere. Claude harness only (a harness without a pane classifier, pi, is 409 `unsupported_harness`). Idle only, through the same lock, recorded-state and pane guards and guarded inject as `reply` (409 `not_idle` otherwise). Interactive commands (`/model`, `/config`, `/resume`) open a menu in the pane; the phone shows the pane and the terminal is the fallback. The window row carries `harness` (omitted for Claude). The phone's reply box switches to "Run command" when the text starts with `/` (a first word with a second `/`, a path, stays a reply), suggests common built-ins, and confirms `/clear`. |
| `POST …/windows/{w}/upload` | multipart, up to 4 `image` parts | Feature `upload` (with `write`). Stores each image (png, jpeg, gif or webp, sniffed from the bytes; the client name and declared type are ignored; ≤10 MB each) as `<runtimeDir>/uploads/<window>/<random>.<ext>`, 0600 in 0700 dirs, and returns `{"images":["<id>",…]}`. Audited as `upload` (hash of the image bytes). Uploads older than 7 days are pruned on every upload and by `reconcile --housekeeping`. |
| `reply` / `queue` `"images"` | `{"text":"…","images":["<id>"]}` | Optional IDs from `upload` for the same window (≤4). Each is appended to the typed text as `[Screenshot: /abs/path]` on its own line after a blank line, so the agent opens it with its file-read tool; unknown IDs are 400. Text may be empty when images are present. |
| `POST …/windows/{w}/interrupt` | `{}` | Sends Escape. Allowed in any state. |
| `POST /v1/shell` | `{project_dir}` | Feature `shell` (with `write`). Opens a plain `shell` window (`tmux.NewShellWindow`, the hub's shell selector) in the running session of the project `project_dir` names, resolved exactly like a dispatch. Returns `{session, window_id}`. `409 session_not_running` when the project's session is down (it never creates one); the hub session is refused with 400. Shell windows are not fleet rows, so the live terminal route also accepts a window the session's tmux reports as `shell`. Audited as `shell`. |
| `POST /v1/dispatch/upload` | multipart: a `project_dir` text field and up to 4 `image` parts | Feature `dispatch-upload` (with `write`). A dispatch has no window yet, so these uploads are keyed by the reserved name `dispatch` instead of a window ID: `<runtimeDir>/uploads/dispatch/<random>.<ext>` in the runtime dir of the project `project_dir` names (validated exactly like a dispatch: it must be one of `/v1/projects` dirs). `uploads.DispatchKey` cannot collide with a window key (window keys are digits). Same image checks, modes, 7-day pruning, guards and rate limit as `upload`; audited as `dispatch-upload`. The Dispatch screen uploads only after the confirm sheet (so a cancelled sheet stores nothing), to the project actually dispatched to, and does not dispatch if the upload fails. The worker reads the screenshot by absolute path in the main project runtime dir, outside its worktree. |
| `POST /v1/dispatch` | `{"agent","project_dir","prompt","model"?,"backend"?,"effort"?,"images"?,"instructions"?,"skip_review"?,"template"?}` | Ad-hoc dispatch, same code path as `agentctl dispatch --agent X --prompt Y` with `--dir project_dir`. Export the needed function from `internal/cmd` (or move it to `internal/dispatch`) instead of shelling out. `project_dir` must be one of `/v1/projects` dirs. Returns `{"window_id","session","branch"}`. Optional `images` (≤4 IDs from `dispatch/upload` for the same project): each is appended to the prompt as `[Screenshot: /abs/path]` after a blank line, exactly like a reply; unknown IDs (including IDs of another project or of a window upload) are 400, and the prompt text stays required (no image-only dispatch). The length and control-character checks run on the final prompt. The lines survive the orchestrator path (first message of the bare session) because the prompt is passed as one quoted argument. Optional `instructions` (feature `dispatch-instructions`, ≤4000 characters, 413 beyond, same control-character rules, trimmed, dropped when blank) is standing guidance appended at the very end of the launched agent's system prompt under `## Additional instructions from the user`; it never goes into the task prompt and is not logged or audited. Optional `template` (feature `dispatch-templates`, a valid template name, else 400) is display-only metadata, like `dispatch --template`'s record: the prompt sent is the text the phone edited, and the sheet's Template picker (which fills agent, model, effort, project and prompt, all editable) is hidden when `/v1/templates` is empty. A restored window resumes its Claude session without rebuilding the system prompt, so nothing is persisted for restore. Optional `skip_review` (feature `dispatch-skip-review`; the dispatch sheet's "Skip code review" toggle) launches as if [`pr_review`](../configuration.md#pr_review) were `off` for this dispatch only. |
| `POST /v1/group-order` | `{"keys":["/path/proj",…]}` | Saves the project group order shared with the hub (`group-order.json`, see `fleet.GroupOrderPath`). `keys` are the visible groups' `key` values (`fleet.GroupKey`: project dir, else session) in the new order: at most 500, each a non-empty string of at most 4096 bytes with no control characters (else 400); duplicates and keys of groups not currently in the fleet are ignored. Merged with the saved order by `fleet.MergeGroupOrder`, so projects that are not running keep their slots; written atomically (0600). Triggers a fleet refresh so every client gets the new order as a `fleet` event. Returns `{"keys":[…]}`, the saved order. Audited as `group-order`. |
| `POST /v1/sweep/preview` | `{"session":"agentctl-proj","include_bare":false}` | Runs `agentctl sweep --session <s> --dry-run --json [--include-bare]` (`include_bare` optional, default false; true also closes finished bare/interactive sessions) as a subprocess (never in process: `cmd.RunSweep` sets the global `tmux.SessionName`) with `AGENTCTL_RUNTIME_DIR` pinned to the group's runtime dir, via `internal/sweeprun`, which the hub shares. `session` must be the `session` of a group in the current fleet (404 `unknown_session` otherwise; 400 `unknown_session` for the hub session or a group without a `.agentctl` runtime dir); it is never passed through as given. Returns the sweep report plus `project`: `{"session","project","dry_run":true,"closed":[…],"attention":[…],"in_progress":[…],"skipped":[…]}`, each row `{"window_id","name","agent","status","detail","errors"}`. A write route although it only reads: it shells out to git and gh per finished window, and is only useful with the apply. One sweep per session at a time (409 `busy`). A failed sweep is 502 `action_failed` with its error. Audited as `sweep-preview`. |
| `POST /v1/sweep` | `{"session":"agentctl-proj","include_bare":false}` | Applies the sweep (`agentctl sweep --session <s> --json [--include-bare]`, same `include_bare` as the preview the user saw), same resolution and guards. Sweep re-classifies every window at apply time, so `closed` is what actually closed, which can differ from the preview. Triggers a fleet refresh. Audited as `sweep`. |
| `POST …/windows/{w}/close` | `{}` | Kill the window, only if done or idle and it is not the orchestrator's own window class (`shell`, `improve~*` excluded), mirroring `sweep` safety rules. |

Write rate limit: 30/min per device (token bucket, `golang.org/x/time/rate`). Audit line: `{time, device_id, device_name, remote_ip, action, session, window, body_sha256, result}`, plus `agent` and `project` for dispatch. An action that acts writes a `result: "attempt"` line immediately before the side effect, then the result line; a refused request writes only the result line.

The window actions check and type into the window's main pane (the pane that is neither the chrome bar nor the HUD, `tmux.MainPaneOf`), never the active pane. Dispatch resolves the repo slug (`gh repo view`, with a timeout) and runs the runtime-dir git check before binding the session; only agent validation and the launch run under the session lock, so a slow `gh` never stalls fleet reads.

### Push (PR3)

| Method + path | Purpose |
|---|---|
| `GET /v1/push/vapid` | `{"public_key":"…"}` |
| `POST /v1/push/subscribe` | browser `PushSubscription` JSON; stored per device |
| `DELETE /v1/push/subscribe` | remove this device's subscription |
| `POST /v1/push/test` | send a test push to this device (never suppressed) |
| `POST /v1/presence` | `{"visible": bool}`: the app reports it is in the foreground (CSRF; a Device route, so it works with `allow_write` off) |

Triggers (from the fleetwatch bus): transition into `blocked`; transition to done with a PR (`pr_url` non-empty) or done without one; still blocked after `blocked_reminder_minutes` (once per blocked episode); a newly published artifact (`serve.push.artifacts`, not updates, not `--no-notify`; `{"title":"<artifact title>","body":"<agent> · <project>","url":"/#/artifacts/<id>","tag":"artifact:<id>"}`). De-dupe per window per episode; respect quiet hours and muted projects. Payload JSON: `{"title":"Fix hub viewer jump","body":"Needs permission · go-engineer · agentctl · Bash: go test ./...","url":"/#/w/agentctl-agentctl/@523","tag":"<session>:<window>"}`. Library: `github.com/SherClockHolmes/webpush-go`. Expired subscriptions (404/410) are deleted. ntfy fallback: by default a post carries only the kind of event (no agent, project or window, no `Click:` header, since ntfy.sh topics are public and the app URL is a LAN address); with `ntfy_details` it posts the same title/body with `Click:` set to the app URL.

Foreground suppression (`serve.push.suppress_while_open`, default true): `Sender.Broadcast` skips a device whose app is open, for every kind. The web app posts `/v1/presence` `visible:true` on load and when it becomes visible, every 20s while visible, and `visible:false` on hidden and `pagehide` (`keepalive`). The server keeps an in-memory device → last-visible map; a report older than 45s counts as background (iOS freezes a backgrounded page, so the hidden report may never arrive), and a restart means everyone is background. Suppression is server-side because iOS revokes a subscription whose push does not show a notification, so `sw.js` always calls `showNotification`. The result is logged and recorded ("sent to 1/2, 1 in app"). The notifier's episode logic is unchanged: a suppressed first blocked push still marks the episode, so the reminder fires if the user has backgrounded the app and the window is still blocked. ntfy is a topic, not a device, and is not suppressed.

Laptop suppression (`serve.push.quiet_when_laptop_active`, default true; threshold `serve.push.laptop_idle_seconds`, default 120): while someone is at the laptop, the phone stays quiet. The signal is a `LaptopIdleFunc` (`internal/serve/laptop.go`, `Options.LaptopIdle`; tests inject a fake and never run ioreg or tmux): on macOS `HIDIdleTime` from `ioreg -c IOHIDSystem` (input in any app), which is authoritative when it works; otherwise, and on Linux, the latest `client_activity` of a `tmux list-clients` client in an `agentctl-*` session (hub, hub viewer and project sessions; phone viewer and control-mode clients excluded). tmux is not consulted when ioreg works, because a client attached over ssh is not the person at the laptop. No signal (no ioreg, no tmux server, no client: headless serve) means not active, so pushes go out. One `laptopActivity` cache (5s) is shared by the notifier and `Sender.Broadcast`, so events never shell out one by one, and the notifier's tick probes only while it holds something. The decisions: done and artifact pushes while active are dropped (logged `suppressed, laptop active`); a blocked push or reminder marks the episode owed; an approval push (`Notifier.Approval`, from `pushApproval`) is held with its deadline and stays answerable in the app. When the laptop goes idle the tick sends each held approval still pending (TTL cut to the time left) and one push per window still blocked ("Still blocked N min" once it has been a minute), once, with quiet hours and muted projects applied; windows that unblocked, approvals answered on the laptop or expired, and everything else are not replayed. `Broadcast` also skips every device while active (result "sent to 0, laptop active") as a backstop; `SendTo` (the test push) never is. Because the notifier gates before enqueueing, ntfy is quiet too.

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

Away mode: when only the phone is at hand, the approval popups that gate `agentctl dispatch` (per `dispatch_approval`; review dispatches skip it by default) and `agentctl cron add/resume/run --force` (always) and Bash commands (`bash_preview`) can be answered from the web app. Without this, a popup nobody sees blocks the agent until its 9-minute deadline and is then denied.

### Hook side (`internal/hooks`, `internal/remoteapproval`)

`runPopupWithDeadline` (both popup kinds) opens a remote approval in parallel with the tmux popup when `agentctl serve` is running, and otherwise behaves exactly as before.

- **Detection**: with phone access off (`<StateDir>/serve/phone-off` exists, see "Phone access off") the guard returns `ErrPhoneOff` before dialing and only the popup is used, exactly as with serve down. Otherwise the control socket `<StateDir>/serve/control.sock` must accept within 200ms. Nothing accepting means "not running", and only the popup is used.
- **Guard** (`remoteapproval.Guard`), before anything is sent: `serve.pid` must be a regular 0600 file owned by the user and name a live process whose command line is `agentctl … serve`; where the OS reports the socket peer (macOS `LOCAL_PEERPID`, Linux `SO_PEERCRED`) it must be that pid, on both the probe and the approval connection; a `ping` op must report that pid and the same executable build (dev, inode, size, mtime) as the hook's own binary. Any failure logs and falls back to the popup alone. During the ~2s after `just install` before serve re-execs, the builds differ and the phone is not offered the approval.
- **Protocol**: one control-socket connection per approval. The hook writes `{op:"approval_request", id (128-bit random hex), kind ("dispatch"|"bash"|"cron"|"template"), asking_agent, session, window_id, target_agent, issue, prompt (fully resolved, including `--prompt-key` content), command, details, deadline}` and then reads a single line `{decision:"allow"|"deny", device, payload_sha256}` from that same connection. The decision is never read from anywhere else. Anything other than a well-formed allow or deny whose `payload_sha256` equals the hook's own hash of what it sent is an error.
- **Dispatch details** (`details`, `remoteapproval.Details`; zero-valued for bash and cron; `template` is the `dispatch --template` name, and for kind `template`, an `agentctl template add|edit|rm` approval, it holds the template name with `mode` the action and `prompt` the file): where and how the dispatched agent will run, resolved by the hook with the rules dispatch itself uses (`internal/dispatchplan`, shared with `internal/dispatch` and `internal/cmd`): `project` (basename of the target's project root, a `.worktrees/<slug>` worktree counting as its checkout), `dir` (`~`-abbreviated) and `dir_source` (`--dir`, else `agents.<name>.dir` of a discovered agent, else the Bash tool's cwd from the payload; when that dir depends on the cwd and something before the dispatch may change it (`lineChangesContext`: a `cd`, subshell, ...), `project`/`dir` are left empty and `dir_source` is "command changes dir first", which the phone shows as Project "unknown" with that flag), `asking_project` and `other_project` (the target is a different project from the asking window's cwd), `agent` and `agent_source` (`--agent`; `code-reviewer` for `--review-pr`; `go-engineer` "issue labels" for an issue without `--agent`, since labels are not fetched; source `global`, `project <name>`, `built-in`, `plugin` or `not found`), `model` and `model_source` (`--model` > backend model > `agents.<name>.model` > mode default (issue: `sonnet`) > agent frontmatter, else `default`), `backend` (`--backend` > `agents.<name>.backend`; empty for plain Claude, "(not configured)" when unknown), `harness` (only when not claude), `account` (`claudehome.ForLaunch` with the parent account resolved from the pane; empty without accounts or on a harness without them; "unknown (command changes dir first)" when the dir is unknown and neither `--account` nor `agents.<name>.account` decides it, since `account_rules` match the dir), `mode` (`issue #N`, `review PR #N`, `ad-hoc`), `worktree` (`worktree`/`no worktree`) and `permission_mode`. Resolution only reads config and agent files: no network, `gh` or spawns, and it never fails or denies the gate (a panic is recovered and gives empty details); anything unresolved is left empty and the phone omits the row.
- **First answer wins**: the laptop popup answering sends `{op:"approval_cancel", id, reason:"answered_on_laptop"}` and closes the connection. The phone answering cancels the popup's tmux client and closes the popup with `display-popup -C` on its client, and its verdict is returned. At the deadline or on a signal both are closed and the command is denied.
- **Errors never allow**: an error on the remote path drops it, and the popup alone decides. If the popup could not be shown (it would have passed through), the hook keeps waiting for the phone until the deadline.
- **No laptop client** (`selectPopupClient` finds none, so `popupTarget.Client` is empty): the popup is skipped and only the phone can answer, until the same deadline. If the remote path then fails, the popup is started as it would have been without serve.
- **Conflicting verdicts deny**: when the laptop and the phone both answer before the hook acts on either (the phone's answer arrives while the popup is being stopped, or both are pending at the deadline), the result is allow only if both said allow, and deny otherwise.
- **Tightening**: with serve running and no laptop tmux client, a dispatch whose popup previously could not be shown (and passed through silently, leaving Claude Code's permission flow to decide) now waits for the phone and is denied at the deadline if nobody answers. The same holds when the popup fails to launch while the phone path is open.
- `bashPreviewGate` still passes through when no human is attached; only popups that are shown (or, for dispatch, would be) go to the phone. Blocked state, desktop notice and the approval-pending marker from `beginApproval` are unchanged. Every outcome is logged with its source (`laptop`, `phone`, `timeout`).

### Serve side

- **Control socket**: `approval_request` keeps the connection open; the approval is held in memory, bound to that connection, and disappears when the connection drops (the hook gave up or exited), on `approval_cancel`, at deadline plus 10s, or when serve stops. Deadlines are clamped to 15 minutes and at most 32 approvals may be pending. `ping` returns `{pid, build}`. `approval_cancel` on any other connection is refused, and no op decides anything.
- **API**: `GET /v1/approvals` (Device) returns `{"approvals":[{id, kind, asking_agent, session, project, window_id, target_agent, issue, prompt, command, details, opened, deadline, seconds_remaining, payload_sha256}]}`. `payload_sha256` is the SHA-256 of the canonical JSON of the shown fields (`remoteapproval.PayloadHash`: id, kind, asking agent, session, window, target agent, issue, prompt, command, and the whole `details` object with every key present, also when empty). Serve and the hook are always the same build (the guard's build check), so the hash shape never differs between them; a skewed pair is refused before a request is sent. Resolved history entries carry `target_project` (no dir, prompt or command). `POST /v1/approvals/{id}` (Write: CSRF, `allow_write`, rate limit, audit) takes `{decision, payload_sha256}`; 409 `approval_changed` when the hash differs, 409 `already_answered` when it was answered or resolved (resolved IDs are remembered 15 minutes), 409 `approval_gone` when the hook hung up before the answer reached it, 404 for unknown IDs. Feature `approvals` is always advertised (the list is readable without `allow_write`).
- **SSE**: `event: approval` (the list entry) when one opens, and once per pending approval on connect; `event: approval_resolved` `{id, session, window_id, outcome, decision?}` when it closes. Outcomes: `answered`, `answered_on_laptop`, `timeout`, `cancelled`, `gone`, `expired`, `server_stopping`, `phone_off`.
- **Push**: on open, to every subscribed device: title "Approval needed", body "<asking agent> wants to dispatch <target agent>" (plus " to <project>" when `details.project` is known), "<asking agent> wants to schedule <target agent>" (cron; `target_agent` is the job's agent and `prompt` the job summary plus prompt) or "<asking agent> wants to run a command", url `/#/approvals/<id>`, urgency high, TTL = time to the deadline. Prompt and command text never go into a push (or into ntfy, which says only "approval needed"). Muted projects are respected; quiet hours are ignored, since an unanswered approval blocks work and is denied at the deadline. `serve.push.approvals` (default true, global-only) turns these pushes off.
- **Audit** (`audit.jsonl`): `approval_requested` when one opens; for an answer the usual write lines (`approval` with `attempt`, then the result) carrying device, `decision` and the `payload_sha256` the phone sent; `approval_resolved` with the outcome when it ended any other way. Lines never contain prompt or command text.

### Web app

`#/approvals` lists pending approvals and `#/approvals/<id>` shows one (the push target). Each shows the asking agent and window (linked; its project is the asking session's), a countdown on the phone's clock, the full prompt and the command (text only, monospace), and Approve / Deny. A dispatch is titled "<asking> wants to dispatch <agent> to <project>" and shows the `details` as compact rows under it (`approvalRows` in `logic.js`): Project (name, full dir muted, a red "not <asking project>" badge when `other_project`), Agent (and its source), Model (and its source), Backend, Harness, Account, Mode (worktree and permission mode muted); empty values are left out, and an approval without details (bash, cron) shows the target agent and issue as before. A prompt or command over 12 lines (or about 960 characters) is folded to 12 lines with a fade and a "Show all N lines" / "Show less" toggle; unfolded it grows with the page rather than scrolling inside a box. Both go through a confirm sheet that repeats everything that will run (the details rows as plain text), and send the payload hash of the version shown; a 409 or 404 toasts "Already answered or changed" and refreshes. The Fleet view shows a red "N approval(s) waiting" banner, the asking window's detail view shows its approval inline, and the bottom nav has an Approvals tab with a count badge (only when the feature is advertised). The list is refetched on every SSE (re)connect and every 15s as a backstop.

### Security model and residual risk

- Decisions come only from `POST /v1/approvals/{id}` by a paired device with `allow_write`, CSRF header and rate limit, and are bound to the payload hash of what the phone displayed. The device token is stored only hashed, so a same-uid agent cannot read one from disk.
- The hook only trusts its own connection, verified as described above, and requires the decision to carry the hash of its own payload.
- The Bash validator (`checkServeTamper` in `internal/hooks/validate.go`, part of the global deny list) blocks commands that reference the serve state dir, `control.sock` or `serve.pid`; stop, reload or reinstall the serve LaunchAgent or systemd unit; `pkill`/`killall` agentctl, or `kill` an `agentctl serve` found some other way; `launchctl` with the serve label in either order, across lines; and `agentctl serve install|uninstall|pair|ca|start|stop|toggle` (`stop --all` too; also by path or `"$(which agentctl)"`, and through a `go run` of the agentctl root package such as `go run . serve stop`, `goRunServeTamper`). File-editing tools (Write, Edit, MultiEdit, NotebookEdit) are denied any path in the serve state dir by `serveEditTarget` (`internal/hooks/serve_edit_guard.go`, in `hook-pre-tool`), which covers the `phone-off` switch.
- These rules make some operations human-only, including `launchctl kickstart … com.blsmth.agentctl.serve` (restarting serve) and `agentctl serve install|uninstall|pair|ca|start|stop|toggle`. The human's prefix+`W` and the hub's `S` key and indicator run outside Claude Code. An agent that injects keystrokes with `tmux send-keys` (into the hub pane, or `send-keys -K -c <client> C-b W` to fire the prefix binding) is outside what a string-based rule can see, like the program-on-disk case below. Agents read serve's logs with the read-only `agentctl serve logs [-n N] [--audit] [--follow]`, which prints the tail of `serve.log`, or of `audit.jsonl` with `--audit`; the deny reason points to it.
- **Residual risk**: every agent runs as the same uid as serve. A determined agent can write a program to disk and run it, which the validator cannot see into: it could stop serve, rewrite `serve.pid`, and bind an impostor socket named like the real one that answers the ping with a copied build identity and the right pid; or ask the real control socket to open a pairing window and pair itself as a device over loopback. These are bypasses of the same-uid boundary, not of this protocol, and they are equally available without away mode (such an agent can already run anything it likes). The checks above make an accidental or naive bypass fail closed: a fake or broken socket can at worst cause a deny or a timeout.

## Web app (PR5)

Embedded under `internal/serve/web/` (`go:embed`), served at `/`. Hash-based routing. Mobile-first, dark mode by default (follows `prefers-color-scheme`), safe-area insets, large touch targets.

- `manifest.webmanifest` (`display: standalone`, icons 192/512 PNG generated once and committed), `sw.js` service worker: handles `push` (showNotification with tag) and `notificationclick` (focus/open the payload URL). Cache only the app shell; never cache API responses.
- **Fleet row title**: the headline comes from `fleet.Recap` (exposed as `recap`/`recap_source` on each window): the ticket title for a ticket dispatch, else the first sentence of Claude Code's away summary or the AI title, else the agent name. The agent name moves to a muted chip, and the full recap (two lines max) replaces the raw activity line; activity shows only when no recap exists.
- **Scheduled runs**: windows launched by `agentctl cron` carry `scheduled_by` (job name) and `scheduled_at` (RFC 3339) on each window; both are omitted for other windows. Their fleet rows get a muted `⏲ <job>` chip and, with no usable parent, sit in a "Scheduled jobs (N)" bucket instead of "Tasks from other sessions"; the window view shows "Scheduled run: <job>, fired Wed 18 Nov 07:00" under the task line.
- **Fleet** (`#/`): groups by project in the saved order shared with the hub (`group-order.json`; projects not in it follow alphabetically), not by urgency: each group's rollup colour and the counts on a folded header carry the attention state. Each group in `/v1/fleet` carries its `key`. Long-press (~400ms) a group header and drag to reorder (touch and mouse, Pointer Events; a tap still folds, a scroll still scrolls). While dragging, groups render at header height so every target stays on screen, the page autoscrolls near the top and bottom edges, and renders are held until the drop. The drop is shown at once and saved with `POST /v1/group-order`; a refused save puts the old order back and toasts. Alt+ArrowUp / Alt+ArrowDown on a focused header is the keyboard path. With `write`, a project group's header (one with a project dir, never the hub's) has a 🧹 sweep button: a tap previews `POST /v1/sweep/preview`, then a confirm sheet lists the windows that would close and how many need attention or are in progress. The sheet has a "Full sweep" checkbox (also close finished interactive sessions; default on) that re-runs the preview when flipped, and Confirm applies with the value of the preview shown; Confirm applies with `POST /v1/sweep` and toasts `Swept N` (what actually closed); nothing to close toasts `Nothing to sweep` with no sheet. The button is disabled while its sweep runs, so a double tap starts one sweep. Windows inside a group stay in attention order. Row: state glyph/colour, agent, window id, mode, activity line, time in state, context %. Blocked rows red, pinned. Live via SSE; reconnect with backoff; banner "laptop unreachable since HH:MM" when SSE is down > 15s.
- **Window detail** (`#/w/{session}/{window}`): a column that fills the screen above the tab bar. Header: back link, agent, a state pill ("working for 3m"), project and branch, a task line (task title, else issue, else activity) and chips for what is known (PR link, context %, cost, short model, window id and mode). The pane (refresh every 2s while visible and on SSE window events) takes the remaining height; its bar has a liveness chip ("updated 12s ago", or "offline since HH:MM" in the blocked colour while fetches fail or the live feed has been down > 15s) and a Reflow / Exact / Live segment (Live opens the live terminal; feature-detected). Reflow wraps lines with a hanging indent, leaves out an empty prompt box and folds Claude Code's footer into a "Claude Code status" toggle; a "New output" pill appears when output changes while scrolled up. Exact drops the footer, sizes the terminal rows to what fits, and pans sideways once the font reaches its 8px floor. State-aware actions:
  - awaiting input: above the pane, the dialog's question and one full-width row per option (the focused, recommended one primary) plus "Dismiss (Esc)" (uses `answer` with the hash it was shown)
  - folder trust (`dialog.kind == "trust"`, `trust.js`): a card "Trust this folder?" with the full folder path (wrapped, never cut), one line on what trusting means, "Trust folder" (primary) and "Exit session" (destructive, behind a confirm sheet); no Esc. A fleet row blocked on folder trust (`pending_tool: "trust"`) shows the same card under the row; its buttons fetch the pane first and answer with that hash, refusing when the dialog is gone or names another folder. The blocked push for it already links to the window, which opens on the card.
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
agentctl serve start           # phone access on; bootstrap the installed LaunchAgent if serve is down (kickstart if loaded); human-only
agentctl serve stop            # phone access off; serve keeps running for artifact links; human-only
agentctl serve stop --all      # bootout the LaunchAgent, plist kept: back at next login; human-only
agentctl serve toggle          # phone off if a serve process is alive with phone on, else start; human-only
agentctl serve status          # running?, pid, listen URL, version, phone access, devices count, cert expiry, CA fingerprint
agentctl serve pair [--name N]
agentctl serve devices [rm <id|name>]
agentctl serve logs [-n N] [--audit] [--follow]   # read-only tail of serve.log / audit.jsonl
```

Linux: `install` prints a systemd user unit instead of writing launchd (no need to fully support Linux, but do not break the build). The phone switch works the same everywhere; `start` with serve down and `stop --all` print the matching `systemctl --user start|stop agentctl-serve`.

### Start, stop and the toggle (`internal/servectl`)

`internal/servectl` is the leaf package behind `serve start|stop|toggle`, prefix+`W` and the hub indicator (internal/serve imports the TUI, so the TUI cannot import it; servectl owns the label, `serve.pid`, `control.sock` and `phone-off` names and serve uses its constants). Every `launchctl` call goes through `launchd.Agent`'s runner (`Bootstrap`, `Bootout`, `Kickstart`, `Loaded`, `Installed`) and every signal through `Controller.Terminate`; inside a test binary `servectl.New` refuses both.

The everyday switch is **phone access**, not the process, so muting the phone never takes artifact links down. Its state is one file, `<stateDir>/serve/phone-off` (0600; present = off), written and removed only by `servectl.SetPhone`. See "Phone access off" below for what serve does while it exists.

- **stop** writes `phone-off`. No launchctl, no restart: serve reads the file per request. Works the same on Linux.
- **stop --all** (`Controller.StopAll`) is the full stop: it boots the job out and leaves the plist on disk, so serve comes back at the next login or with `start`. It does not `launchctl disable` the job: a stop that silently outlived a reboot would leave serve installed but never running, and remote approvals quietly off; `uninstall` is the permanent stop. A serve process launchd does not manage (run in a terminal) gets SIGTERM, but only after `pidfile.Running` confirmed it is `agentctl … serve`. It leaves the phone switch as it is.
- **start** removes `phone-off`; if serve answers, that is all ("phone access on (serve at https://…)"). Otherwise it kickstarts a loaded job (`-k` when a process is alive but its control socket is dead) or bootstraps the installed plist, then waits up to 10s for the control socket and reports the URL. With no plist it fails with "run `agentctl serve install`" rather than spawning a detached server: that process would have no supervisor, no log capture and would not come back at login, and it would be one more self-spawn path.
- **toggle** turns phone access off when serve answers on its control socket and phone access is on, else runs `start`: so one press on a stuck server (pid alive, socket dead) kickstarts the launchd job, or reports the `stop --all` hint for an unmanaged one.
- **prefix+`W`** runs `agentctl serve toggle --message-client '#{client_name}'` in a background `run-shell` and shows the result as a tmux message ("phone access off: …", "phone access on (…)", "serve started at https://…", or the error).
- **Hub indicator**: the hub list's title row shows `📱 on` (green: serve answers, phone access on), `📱 off` (dim: phone access off, artifacts still served), `📱 down` (dim, struck through: no serve process, artifact links are down too) or `📱 stuck` (yellow: the pid is alive but the control socket refuses connections, so phone approvals are not offered). It is fed by a probe on the hub's 1s poll tick (`servectl.Probe`: one local socket connect plus a stat of `phone-off` and, only when the connect fails, the pidfile check that tells "stuck" from "down"; no launchctl, nothing per render, independent of the monitor). Clicking it or pressing `S` in the list toggles phone access and flashes the outcome in the footer.

These paths run outside Claude Code. For agents, `serve start|stop|toggle` (with or without `--all`) and `launchctl` against the serve label are denied by the serve-tamper rules, and `phone-off` is in the serve state dir, which their Bash and Write/Edit/MultiEdit/NotebookEdit calls are denied (`checkServeTamper`, `serveEditTarget`; see "Security model and residual risk").

### Phone access off

While `phone-off` exists the serve process keeps running (same port, certificate and listener) and (`internal/serve/phone.go`):

- **No push**: `Sender.Broadcast` and `Sender.SendTo` send nothing (`serve status` shows the last push as "phone access off"), and the ntfy relay is skipped.
- **No remote approvals**: `remoteapproval.Guard.Open` checks the file before it dials and returns `ErrPhoneOff` (which wraps `ErrNotRunning`), so the hook shows the laptop popup alone, exactly as with serve down. As a backstop, `ServeApproval` refuses a request that raced the toggle, and an approval already open when the phone turns off ends with an error (`phone_off` outcome in the audit log), so the hook drops the phone and the popup decides (it is shown then if there was no laptop client).
- **Phone app off**: every `Device` and `Write` route answers `503` with code `phone_off` (`Router.phoneOff`, checked after the device cookie, so an unpaired browser still only sees "not paired"). A browser navigation to the app gets the self-contained "Phone access is off" page (`web/phoneoff.html`, no scripts, reloads every 30s, so the app comes back by itself). In an open app, `api.js` reloads into that page on a `phone_off` error, and `live.js` checks `/healthz` (which reports `"phone_off": true`) when its event stream drops. Open SSE streams and live terminals end within a second (a 1s poll of the file in `Server.Start`), pairing is refused (`/pair` and the control-socket `pair` op), and presence reports stop.
- **Artifacts keep working**: `/a/<capability-id>/...`, the token routes `/a/t/...` and the legacy 10-character entry (which checks the device cookie itself and redirects to its token URL) are `Public` routes and never consult the switch. The app's artifact viewer and `/v1/artifacts` are app routes and are off.

Each request stats the file (no caching), so a toggle takes effect on the next request with no restart.

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
