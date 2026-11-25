# Artifacts: agent-published reports, mockups and displays served by `agentctl serve`

## Goal

Agents often want to *show* Brendan something rather than describe it: a report, a comparison table, a chart, a clickable mock of a web page, a diagram. Like Claude Desktop's artifacts, an agent should publish that with one command and get back a URL; Brendan opens it on the phone (PWA) or the paired laptop browser. One entry point (`agentctl artifact ...`) for adding and managing; one place (`/v1/artifacts` + an Artifacts tab) for viewing.

## Decisions (Brendan, 2026-11-23; do not re-litigate)

- **Same server, sandboxed.** Artifacts are served by `agentctl serve` on its existing listener under `/a/...`. Every artifact response carries `Content-Security-Policy: sandbox allow-scripts allow-forms allow-popups allow-modals allow-downloads` (never `allow-same-origin`), so the document runs in an opaque origin: no device cookie on its subrequests, no reading the `/v1` API, no CSRF bypass. The PWA shows it in an `<iframe sandbox="allow-scripts allow-forms allow-popups allow-modals allow-downloads">`. The top-level `/a/` request itself is device-authenticated (cookie sent on the navigation/iframe load from the PWA origin).
- **Laptop = pair the laptop browser** once with `serve pair`; `agentctl artifact open` opens the same https URL. No file:// fallback. (Superseded: links no longer need pairing, see "Security model: the id is the capability" at the end.)
- **Feedback loop later.** v1 is publish + view. Comments from the viewer delivered to the author's window come in the last PR.
- **Push on publish, on by default**: new `serve.push.artifacts` (global-only, default true), honours quiet hours and muted projects; tap opens the artifact.

## Data model and storage (`internal/artifacts`, leaf package)

Machine-wide, under `config.StateDir()/artifacts/` (dirs 0700, files 0600), resolved lazily (never at package init; testenv TestMain).

```
artifacts/<id>/meta.json
artifacts/<id>/rev/<n>/...        # content files of revision n (n = 1, 2, ...)
```

- `id`: URL-safe, random, lowercase base32 (`a-z2-7`); plus a human `slug` from the title for display only. Since the public-link change it is 26 characters (130 bits from `crypto/rand`) and is the viewing capability (see "Security model: the id is the capability" below). Artifacts published before it have 10-character ids (35 bits), which `ValidID` still accepts but which keep needing a paired device.
- `meta.json`: `id, title, description, kind, entry (relative path of the file to open, e.g. index.html / report.md), created, updated, revs [{n, created, size, note}], pinned, project_dir, project (display name), agent, window_id, session, session_id, branch`. Origin fields are filled from the calling window (TMUX_PANE → windowstate, same trusted source `status whoami` uses); a plain shell gets project from cwd and blank agent/window.
- **Kinds** (inferred from the entry's extension, overridable with `--kind`): `html` (single file or a directory with index.html, may reference relative assets), `markdown` (.md, rendered server-side to a styled HTML page with goldmark, already an indirect dep; fenced ```mermaid blocks rendered client-side if feasible, else left as code), `image` (.png/.jpg/.gif/.webp/.svg; SVG served under the same sandbox CSP), `text` (.txt/.log/.json/.csv shown preformatted; CSV may render as a table). Unknown extension → `text` if valid UTF-8, else refuse.
- **Revisions**: `artifact update <id> <path>` adds revision n+1 (content copied, never symlinked); the latest is the default view; older ones stay reachable. Keep at most 20 revisions per artifact (oldest dropped).
- **Limits**: 25 MB per revision, 500 files per directory artifact, no symlinks followed out of the source dir, paths cleaned (no `..`). Total store cap 1 GB: over it, `add` fails with a message naming `artifact rm`/`prune`.
- **Retention**: unpinned artifacts not updated in 30 days are pruned by `reconcile --housekeeping` (same pattern as the other pruners). `pinned` ones never.
- Writes are atomic (temp dir + rename; meta via temp file + rename) and safe against concurrent `add`/`update` (flock on `artifacts/.lock`).

## CLI: `agentctl artifact` (the one entry point)

```
agentctl artifact add <path|dir|-> [--title T] [--description D] [--kind K] [--entry F] [--pin] [--no-notify] [--json]
agentctl artifact update <id> <path|dir|-> [--note N] [--title T] [--json]
agentctl artifact list [--project P|--all] [--agent A] [--since 7d] [--json]
agentctl artifact show <id> [--json]            # metadata + revisions + URL
agentctl artifact url <id> [--rev N]            # just the URL
agentctl artifact pdf <id> [--rev N] [--out path|-] [--force]   # PDF export, see "PDF export"
agentctl artifact open <id> [--rev N]           # opens the URL in the laptop browser (`open` / xdg-open), seam-stubbed in tests
agentctl artifact pin|unpin <id>
agentctl artifact rm <id> [--yes]
agentctl artifact prune [--older-than 30d] [--dry-run]
```

- `-` reads stdin; requires `--kind` or `--title` with an extension hint (`--entry report.html`).
- `add`/`update` print the id and the URL (originally `https://<serve host>:<port>/#/artifacts/<id>`; now the direct link `/a/<id>/latest/` first and the app link on the next line, see below; from serve's control socket / status when running; otherwise print the id and "serve is not running; the artifact will be at <url> once it starts" using config listen + hostname). Never fail just because serve is down.
- Title default: the HTML `<title>`, else the first markdown `# heading`, else the file name.
- Default list scope: the current project; `--all` for everything.
- Read commands are ungated. `add`/`update`/`rm` need no approval popup (the content is sandboxed and harmless to the host). Direct Bash/Write/Edit into `StateDir()/artifacts/` should be denied by a tamper rule like `checkCronTamper` so the CLI stays the only writer (metadata integrity, limits) — speed bump only.

## Serve (routes, `internal/serve/routes_artifacts.go`)

- `rt.Feature("artifacts")`.
- `GET /v1/artifacts?project=&since=` (Device) → list, newest first, with `url`, `raw_url`, kind, title, origin, rev count, updated.
- `GET /v1/artifacts/{id}` (Device) → metadata + revisions.
- `GET /a/{id}/{rev}/{path...}` (Device) → raw content of that revision (`rev` = number or `latest`). Responses: correct Content-Type (sniff-safe, `X-Content-Type-Options: nosniff`), the sandbox CSP above, `Cache-Control: private, no-cache` for `latest`, immutable for numbered revs, `Referrer-Policy: no-referrer`, `Cross-Origin-Resource-Policy: same-origin`. Markdown/text kinds are served as rendered HTML pages (agentctl's own template + CSS, light/dark) under the same sandbox CSP. Directory traversal impossible (resolve within the rev dir, reject `..`, no symlinks).
- Inside the sandbox the artifact may load scripts/styles/fonts/images from `https:` CDNs (agents will use Chart.js, Tailwind CDN, Mermaid, etc.) and `'self'` relative assets; set the CSP as `sandbox ...; default-src 'self' https: data: blob: 'unsafe-inline' 'unsafe-eval'; connect-src 'self' https:` — the protection is the opaque origin, not the source list. Note in the doc: the phone needs internet for CDN-using artifacts.
- `DELETE /v1/artifacts/{id}` and `POST /v1/artifacts/{id}/pin` as Write routes (audited).
- Fleetwatch/SSE: emit an `artifact` event when the store changes (fsnotify on `artifacts/`), so the list updates live.
- Push: on a new artifact (not on update), when `serve.push.artifacts` is true: title = artifact title, body = `agent · project`, URL = `#/artifacts/<id>`. Quiet hours and muted projects apply. Payload never contains artifact content.

### As built (PR 2)

- **Headers.** `sandboxArtifacts` (`internal/serve/artifact_content.go`) is the outermost middleware for every path under `/a/`: whatever writes the response (CIDR filter, CSRF check, device check, the mux's 404, the handler) leaves with the sandbox CSP, `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer` (keeps the URL token out of `Referer`), `Cross-Origin-Resource-Policy: cross-origin` (the document's opaque origin is cross-origin to everything, so `same-origin` makes WebKit drop its own stylesheets and images) and `X-Frame-Options: SAMEORIGIN`, plus `Access-Control-Allow-Origin: *` (no credentials) on capability paths so ES modules and `fetch()` of the artifact's own files work (the app's other pages keep `DENY` and `cspHTML`, whose `frame-ancestors 'none'` stops an artifact framing them). They are applied when the status line is written, so `writeHTML`'s `cspHTML` (the "not paired" page) cannot replace them. The CSP never contains `allow-same-origin`; tests assert it on every response kind.
- **Auth: a capability URL, not the cookie.** Measured in Chromium and WebKit: the `SameSite=Strict` device cookie reaches the entry request (a navigation or the PWA's iframe load) but never the subresource requests of the artifact document, whose initiator is the opaque origin (`Sec-Fetch-Site: cross-site`), whether the page is top-level or framed. So `GET /a/{id}/{rev}/{path...}` (Device, `rev` = number or `latest`) only resolves the revision and redirects (302, `no-store`) to `/a/t/{token}/{id}/{n}/{path...}`, a Public route where the token is the credential and the cookie is neither needed nor read. The token (`artifact_token.go`) is `<expiry base36>.<device id>.<HMAC-SHA256, 18 bytes, base64url>` over id, resolved revision number (never `latest`), expiry and device id, with a 32-byte key in `<serve dir>/artifact-url.key` (0600). It lasts 24-25h (the expiry is rounded up to the hour so URLs minted within an hour are identical), and it dies at once when its device is revoked (the device must still be paired). Because it is in the base path, every relative URL the document uses carries it. `/v1/artifacts` mints the `raw_url`s for the requesting device. A token for one artifact and revision is refused (403) for any other, when expired, tampered or extended. The document still cannot read any `/v1` response (CORS) or send the CSRF header (failed preflight).
- **Paths.** The middleware refuses with 400 any `/a/` URL with a `.` or `..` segment (also `%2e%2e` and double-encoded `%252e%252e`), an empty segment (`//`, absolute), `%2f`, `%5c`, a backslash or NUL, before the mux could clean and redirect it. The handler then reads through `artifacts.Store.Open(id, rev, rel)`: `os.Root` on the revision dir, `Lstat` of every component (any symlink is 400), `O_NOFOLLOW` on the file, a same-file check after open. A directory resolves to its `index.html`; an empty path is the entry (a redirect to it when the entry is in a subdirectory, so its relative URLs resolve). `/a/<id>/<rev>` redirects to `/a/<id>/<rev>/`.
- **Types and rendering.** Content-Type comes from a fixed extension table (`artifactTypes`); unknown extensions are `application/octet-stream`, never HTML. A markdown entry is rendered with goldmark (GFM, heading IDs, raw HTML passed through) into an agentctl page with light/dark CSS; fenced `mermaid` blocks load Mermaid 11 from jsDelivr inside the sandbox (the phone needs internet for that, as for any CDN-using artifact). A text entry is an escaped `<pre>`; a `.csv` entry is a table (first row as header, at most 5000 rows). `?raw=1` serves the entry's bytes instead (`text/plain` for markdown). Errors are small HTML pages (they show in the iframe).
- **Caching.** Capability URLs name a numbered revision, so content is `private, max-age=31536000, immutable`; the cookie redirect (where `latest` resolves) and errors are `no-store`.
- **Live updates.** Every store write (`Add`, `Update`, `Pin`, `Remove`, `Prune`) rewrites `<store>/.changes/stamp`, so serve needs one fsnotify watch however many artifacts exist (kqueue opens a descriptor per entry of a watched dir). `artifactWatcher` (`internal/serve/artifact_watch.go`) creates that dir when the store does not exist yet, debounces 200ms, rescans the metadata and diffs it (latest rev, updated, pinned, title) into `add`/`update`/`rm` changes, published on the fleetwatch bus as `KindArtifact`. It also rescans every 30s and right after the server's own pin/delete. The baseline is taken when serve starts, so a restart re-announces nothing. SSE clients get `event: artifact` `{"id","action"}`; a client that falls behind gets a `fleet` event instead and should refetch `/v1/artifacts`.
- **Push.** The notifier pushes only `add` changes: title = artifact title, body = `agent · project`, URL `/#/artifacts/<id>`, tag `artifact:<id>`, kind `artifact` (ntfy without details: "agentctl: new artifact"). Skipped when `serve.push.artifacts` is false, the artifact was published with `--no-notify` (stored as `no_notify` in meta.json), its project is in `muted_projects`, or it is quiet hours.
- **Writes.** `DELETE /v1/artifacts/{id}` (body `{}`) and `POST /v1/artifacts/{id}/pin` (`{"pinned":bool}`) are Write routes: `allow_write`, CSRF header, rate limit, audit lines with `artifact` (the id), plus `project` and `agent`.
- **Order.** `POST /v1/artifacts/order` (`{project, ids}`, Write, audited with `project`) saves one project's display order in `<artifacts>/order.json` (written atomically under the store flock). `GET /v1/artifacts` applies it (`artifacts.ApplyOrder`): artifacts not in the saved list come first, newest first, then the saved order; vanished ids are dropped. The phone groups the list by project (collapse state in localStorage `agentctl.artifacts.collapsed`), ordered like the fleet; dragging a group header saves the shared fleet group order.

## PWA

- New **Artifacts** tab (`#/artifacts`, shown when the server advertises `artifacts`): list grouped by project (same order as fleet), each row: kind icon, title, agent chip, relative time, rev count, pin. Pull/SSE live.
- **Viewer** (`#/artifacts/<id>[/<rev>]`): header (back, title, origin chips: agent, project, window link → `#/w/...`, rev picker, "Open in new tab" (raw URL), pin, delete with confirm), body = full-height sandboxed iframe. Works rotated and on the laptop browser at desktop widths.
- **Window detail**: a chip "◆ N artifacts" linking to the list filtered by that window (artifacts with that window_id).
- New `web/artifacts.js` registered in `assets.go`; `just web-test`.

## Hub (small, optional in the PWA PR or its own)

- Row chip `◆N` when the window has artifacts; `A` on a row with artifacts opens the newest in the browser (`artifact open`, seam-stubbed).

## Agent adoption

- Preamble (`internal/dispatch/preamble.go`, both worker and orchestrator): a short `### Artifacts` section: "To show the human a report, table, chart, mockup or diagram, write a self-contained HTML (or Markdown) file and run `agentctl artifact add <file> --title '...'`; it prints a URL the human can open on their phone. Send them the URL. Update with `agentctl artifact update <id> <file>` rather than adding a new one." Only when serve is installed/configured? No: always, the URL is valid once serve runs.
- Shared include `~/.claude/shared/artifacts.md` (Brendan's dotfiles; I write it, not in the repo): style guidance (single self-contained file preferred, mobile-first, dark-mode aware, CDN libs OK, no secrets in artifacts, prefer updating over re-adding).
- Prompt template `report` in `.agentctl/templates` later.

## Feedback (last PR)

- Viewer gets a comment box (Write route `POST /v1/artifacts/{id}/comments`); comments stored in meta (`comments [{rev, at, device, text}]`) and delivered to the author window as an `agentctl notice:`-style mailbox message through the existing reply path when the window is live and idle (queued otherwise). Optionally a tiny `window.agentctl.feedback(obj)` postMessage bridge so interactive mockups can send structured choices back (the parent PWA validates `event.source === iframe.contentWindow`, then posts as a comment).

## PR plan

1. **Store + CLI**: `internal/artifacts` (store, kinds, revisions, limits, lock, prune), `agentctl artifact` subcommands (add/update/list/show/url/open/pin/unpin/rm/prune), housekeeping prune hook, tamper deny rule for direct writes, docs (`docs/commands.md`, `docs/configuration.md`, new `docs/design/artifacts.md` archiving this spec). URL printed from config even with serve down.
2. **Serve routes + push**: `routes_artifacts.go`, sandbox CSP headers, markdown/text rendering, SSE event, `serve.push.artifacts` key (global-only), push trigger. Security tests mandatory: no auth → 401; CSP sandbox present on every `/a/` response incl. errors-as-HTML and markdown renders; no `allow-same-origin`; traversal (`..`, encoded, symlink) refused; nosniff; write routes need allow_write + CSRF.
3. **PWA**: Artifacts tab, viewer, window-detail chip, `artifacts.js` in assets.go, web tests.
4. **Agent adoption + hub**: preamble section (golden/prompt tests updated), hub chip + `A`.
5. **Feedback**: comments → author window, postMessage bridge.

Order: 1 → 2 → (3 ∥ 4) → 5.

## Hard parts / edge cases to handle

- No tmux / plain shell caller: origin fields blank, project from cwd (worktree-aware root, so artifacts from a worktree group under the main project).
- Serve not installed or not running: add still works, URL printed with a note.
- Concurrent adds from many agents; partial copy on crash (temp dir + rename).
- Huge dirs / node_modules accidentally passed: file-count and size limits with a clear error.
- Binary files with a .txt extension; non-UTF-8.
- An HTML artifact with absolute paths (`/style.css`) will break under `/a/<id>/<rev>/`; document "use relative paths". Consider a `<base href>` injection for single-file HTML? No: leave content byte-for-byte.
- Deleting an artifact while the phone views it: 404 page in the iframe, list updates via SSE.
- Tests never touch the real state dir, never bind fixed ports, `artifact open` browser launch is stubbed.

## Agent adoption and the hub (PR 4)

- Every runtime preamble (worker and orchestrator, with and without native messaging) carries a short `### Artifacts` section after the context store: publish with `agentctl artifact add <file> --title '...'`, send the human the URL, revise with `artifact update <id> <file>`, use relative paths, and see the `agentctl:artifacts` skill for templates and style rules (`artifactsSection` in `internal/dispatch/preamble.go`).
- The hub shows a dim `◆N` chip before the title of a row whose window (artifact `window_id` + `session`) has N artifacts (`internal/tui/fleet_artifacts.go`). The listing is read on the poll tick, off the UI goroutine, and skipped while the stamp is unchanged, so rendering never touches disk. The stamp is the newest of the store directory's mtime and every `meta.json` mtime, so a new revision (which rewrites only `meta.json`) refreshes the chip too.
- `A` on such a row (also in the row menu, the footer hints and the `?` reference) runs `agentctl artifact open` on the newest one; it does nothing on other rows. `o` stays the sort toggle everywhere.
- `agentctl artifact list` pads columns by display width, with the pin in its own `PIN` column, so wide characters no longer shift a row.

## Feedback (PR 5)

- **Store.** `meta.json` gains `comments [{id, rev, at, device, text, delivered_at, status}]` (`internal/artifacts/comments.go`, under the store flock, atomic like every meta write). At most 200 per artifact (the oldest are dropped), text at most 4000 characters. A comment does not change `updated`, so it does not extend retention. `status` is `delivered`, `queued` or `undelivered`. `meta.json` also records the author window's `account`.
- **Route.** `POST /v1/artifacts/{id}/comments` `{text, rev}` (Write: `allow_write`, CSRF, the shared write rate limit, audited as `artifact-comment`; `rev` 0 = latest; feature `artifact-comments`). The comment is stored first. Delivery then goes to the artifact's `session` + `window_id` through the same guards as `reply` and `queue`: a live idle window gets a reply, a working one a queued message, and anything else (window gone, another account now owns the ID, no window recorded, blocked on a dialog, pane not at a prompt) leaves the comment `undelivered`. The response is `{comment, delivery: delivered|queued|saved, reason}`; delivery never fails the request. `GET /v1/artifacts/{id}` includes `comments`; the list carries only `comment_count`.
- **Text typed into the agent:** `Feedback on artifact "<title>" (<id>, rev N) from Brendan: <text>` then `Update it with: agentctl artifact update <id> <file>`.
- **Viewer.** A comment button in the header row opens a bottom-sheet composer that names the revision; the outcome is a toast. Past comments are listed in the ⋯ sheet.
- **postMessage bridge.** An artifact may call `parent.postMessage({type: "agentctl:feedback", text}, "*")`. The viewer accepts it only when `event.source` is the iframe's own `contentWindow` and the type matches (`artifactFeedbackFrom`, `logic.js`), caps the text, and opens the composer prefilled. It never sends: agent-authored content cannot message an agent by itself, a person reviews the text and taps Send.
- **CLI.** `agentctl artifact comments <id> [--json]`; `artifact show` lists them too.

## Security model: the id is the capability (public links)

Decision (Brendan, 2026-11-23): viewing an artifact must not require pairing. Opening a link on a laptop browser that was never paired used to show the "not paired" page. The artifact id itself is now the credential.

- **Ids.** `artifacts.NewID` returns 26 characters of lowercase base32 (`a-z2-7`, unpadded) from 17 bytes of `crypto/rand`: 130 bits, unguessable. `ValidID` accepts that form and the legacy 10-character one, so every store path, the hooks and the routes keep working for old artifacts; `CapabilityID` is true only for the 26-character form. Comment ids come from the same generator.
- **Public viewing.** `GET /a/{id}/{rev}/{path...}`, `GET /a/{id}/{rev}` and `GET /a/{id}` (to `latest/`) are Public routes. For a capability id the content is served right there, no cookie, no redirect to a token URL: the sandboxed document's subresources resolve under `/a/<id>/<rev>/` and authenticate by the id in their path. `latest` is `private, no-cache` with an ETag naming the revision (a new revision's file may carry an older mtime); numbered revisions stay immutable. Every response, errors included, carries the same sandbox CSP, nosniff, `Referrer-Policy: no-referrer` (keeps the id out of `Referer` to CDNs), CORP and `X-Frame-Options: SAMEORIGIN` as the token route, plus `Access-Control-Allow-Origin: *` (no credentials), and traversal is refused the same way (`sandboxArtifacts`, `Store.Open`).
- **Legacy ids** keep the old flow: the handler reads the device cookie itself (`artifactAccess`), then redirects to the token URL. A legacy id without a valid cookie gets the same 404 page as an unknown id (and `/a`, `/a/`), so a miss reveals nothing about which artifacts exist.
- **Who can view.** Anyone who has the link and whose source address passes `serve.allow_cidrs` (LAN/VPN only; the filter runs before everything and is unchanged). There is no per-viewer check and no expiry: revoke by deleting the artifact with `agentctl artifact rm <id>` (or let retention prune it). Do not paste links anywhere public.
- **Still paired.** `/v1/artifacts` (list and detail: they enumerate ids), pin, delete and comments (Write routes), and the PWA's Artifacts tab. `/v1/artifacts` returns the direct numbered URL `/a/<id>/<n>/` as `raw_url` for a capability id, and a token URL for a legacy one, so the PWA frames the direct URL for new artifacts.
- **CLI.** `add`/`update` print the direct link `https://<host>:<port>/a/<id>/latest/` and then `app: https://<host>:<port>/#/artifacts/<id>`; `show` prints both (`url`, `app`) and a numbered direct link per revision; `url` (`--rev N` → `/a/<id>/<N>/`) and `open` use the direct link. JSON: `url` is the direct link, `app_url` the app link. The push notification still opens the PWA viewer. With `serve.public_hostname` set and a valid public certificate held by the running server, the direct `/a/<id>/` links use that name, so they open in any browser without the local CA profile, while the `app:` link stays on the local host where the device cookie lives ([Public certificate](mobile-web-app.md#public-certificate-cloudflare-dns-01)); the security model above is unchanged.
- **Phone access off.** `agentctl serve stop` (and toggle, prefix+`W`, the hub's `S`) switches phone access off instead of stopping serve, so artifact links survive muting the phone: the Public `/a/` routes above (capability ids, token URLs, and the legacy entry with its cookie check) never consult the switch, while the app routes (`/v1/artifacts`, the Artifacts tab and viewer, the new-artifact push) are off with the rest of the phone app. `artifact add/url/open` count a phone-off serve as running (`servectl.State.Serving`). Only `agentctl serve stop --all` takes the links down. See "Phone access off" in [mobile-web-app.md](mobile-web-app.md#phone-access-off).
- **Logs.** Ids are secrets now: the server log never gets a full capability id. Push diagnostics, warnings and store errors go through `redactArtifactIDs` / `artifacts.ShortID` (a 6-character prefix), and an ntfy post with `serve.ntfy_details` links to `/#/artifacts`, not to the artifact. The write audit log (`audit.jsonl`, 0600 in the serve dir) still records the full id of pin/delete/comment requests, which come from paired devices.

## Rendering from a spec (`artifact render`)

Agents never edit artifact HTML. A kit page is described by a JSON spec (`plugin/skills/artifacts/kit/SPEC.md`), and `agentctl artifact render <spec.json|-> [--title T] [--description D] [--update <id> --note N] [--out file.html] [--no-notify] [--pin] [--json]` builds the page and publishes it through the same `add`/`update` code as the other commands (`internal/cmd/artifact_render.go`).

- **Why.** On the first real use an agent deleted the `</script>` that closed the spec block in a copied template; the kit JS was swallowed into the JSON and the phone showed a blank page with no error card. With render the spec is its own `.json` file and the HTML is never hand-edited.
- **Validation** (`internal/artifactkit`) follows SPEC.md: the closed set of block types, required fields, enums, unknown fields, and the cross-field rules `kit.js` enforces (series length equals labels, a doughnut has one series without gaps, unique column keys, rows only use column keys, at most one recommended option and at least two options, `date`/`datetime` values are ISO/RFC 3339). Reports and dashboards must contain a `sources` block (the browser only warns; render refuses). Every error is printed with its JSON path (`blocks[3].series[0].values`), the command exits non-zero and nothing is published.
- **Page.** The same shell as the templates: `kit.css` and `kit.js` inlined, the spec in `<script type="application/json" id="spec">`. The kit is embedded from `plugin/skills/artifacts/kit/embed.go` (`go:embed` cannot reach outside a package directory, so the Go package lives next to the files and they stay the one copy). The spec is embedded with `<`, `>` and `&` as unicode escapes, so `</script` or `<!--` inside a spec string can never break the page.
- **Update and `--out`.** `--update <id>` adds a revision (the artifact must be an html one); `--out` only writes the file and publishes nothing. `--title` defaults to the spec's title.
- **Shared fixtures.** `kit/testdata/valid/*.json` must pass and `kit/testdata/invalid/*.json` must fail at the path named in `kit/testdata/expect.json`. The Go validator is tested against all of them; when `node` is on the PATH (CI's runners have it) `testdata/check.js` runs `kit.js` over the same files, otherwise that half is skipped. Starting points per kind are `kit/examples/<kind>.json`, extracted from the templates; templates stay for mockups.
- **Kit additions from first use.** A `datetime` table column (sortable, shown in local time as `YYYY-MM-DD HH:MM`), a KPI `breakdown` list, chart `excluded` notes under the chart, and text table cells that break only at word boundaries with a minimum width instead of mid-word on a 390px phone.

## PDF export

`agentctl artifact pdf <id> [--rev N] [--out path|-] [--force]` writes a paged US Letter PDF (0.5in margins, light palette, no header or footer). It is read-only on the store, so ungated, and it works whether or not `agentctl serve` runs. The package is `internal/artifactpdf` (`Render(ctx, store, id, rev, w)`, rev 0 = latest), built to be called by the CLI and by a serve route behind a viewer button.

- **How.** `Render` starts an HTTP server on `127.0.0.1:0` that serves that one revision, and execs a headless browser (`--headless=new --print-to-pdf`) at its URL. No Go dependency; the exec goes through the `runBrowser` seam that tests stub. The server answers only the loopback `Host` and a random path prefix, reads files with `Store.Open` (so the `..`/symlink protections apply) and serves each kind as `agentctl serve` does: html and every asset as-is (relative URLs work for directory artifacts), markdown and text through `internal/artifactpage` (the same page code serve uses), an image wrapped in a minimal page (`?raw=1` is the file). Each export has its own port and temp dir, so exports can run side by side.
- **Browser.** `artifacts.pdf_browser` (global only, see [configuration](../configuration.md#artifacts)), else Chrome, Chromium, Edge in `/Applications`, else `google-chrome`, `google-chrome-stable`, `chromium`, `chromium-browser` on `PATH`. A fresh `--user-data-dir` per run means it never collides with a browser you have open. It runs in its own process group; the 60s context deadline (or a cancel) kills the group. A run with no PDF fails with the browser's stderr tail.
- **Light.** `--blink-settings=preferredColorScheme=1` (in Blink `1` is light and `0` is dark, checked with `matchMedia`) keeps a dark system from darkening html artifacts; kit pages and the markdown/text page also pin the light palette under `@media print`.
- **CDN scripts and timing.** `--virtual-time-budget=15000` lets Chart.js and Mermaid load and draw; `--timeout=30000` stops a page whose script host never answers and prints what rendered (without it Chrome waits for the dead request until the 60s kill). The URL carries `?print=1`: a headless print lays the page out as screen media and prints before animations finish, so the kit reads `?print=1` (or the print media type) and draws charts without animation.
- **Print styles.** `kit.css` has `@page { size: letter; margin: .5in }` and a print block: light tokens, no shadows or sticky headers, `break-inside: avoid` on cards, KPI tiles, chart boxes, diagrams and table rows, repeated `thead`, tables wrapped to the page width instead of scrolling, `pre` wrapped. A `<button>` in a repeated header row prints empty in Chrome, so the sort buttons print as plain text (`display: contents`). The agentctl markdown/text page has the same rules.
- **CLI.** The default output is `./<slug>.pdf` (the first six characters of the id when there is no slug); `--out` is a file, an existing directory, or `-` for stdout. An existing file is refused unless `--force`, before the browser starts. The path is printed. An unknown id or revision gives the same errors as `artifact url`.
- **Viewer.** The ⋯ sheet has "Save as PDF (vN)" for the revision on screen, backed by `GET /v1/artifacts/{id}/pdf?rev=N` (a Device route; limits, cache and errors in [mobile-web-app.md](mobile-web-app.md#artifacts)). The button shows "Rendering PDF…" while serve renders, then hands the file to `navigator.share` when the browser can share files (an iOS home-screen app cannot download reliably and does not share Safari's cookies, so it never opens the Device URL elsewhere), else to a blob `<a download>`. The share carries `{files: [file]}` and nothing else: on iOS a `title` or `text` next to the files makes the share sheet treat the share as text and drop the file actions (Save to Files, Print). Before sharing or downloading, the viewer checks the answer really is a PDF (the response's `application/pdf` type, a `.pdf` name, non-empty, first bytes `%PDF-`) and otherwise shows an error instead of sharing it. When the render outlasted the tap's user activation, the button turns into "Share PDF" for a second tap.
- **Open PDF.** Next to it (with `allow_write`, feature `artifact-pdf-link`), "Open PDF" does not depend on the share sheet: it POSTs `/v1/artifacts/{id}/pdf-link?rev=N` for a five-minute token URL `/pdf/<token>/<name>.pdf` and `window.open`s it. That URL needs no cookie, so the window a home-screen app opens (which has Safari's cookie jar, not the app's) can load it, and iOS shows its native PDF viewer, whose share button has Save to Files and Print. The token is checked before any store read or render; the PDF goes through the same exporter, limits and cache as the Device route; it is served `inline`, `private, no-store`. As with "Share PDF", when the request used up the tap's activation (or the window was blocked) the button turns into "Open PDF ↗" for a second tap; a link within 30s of expiry is re-minted. Routes in [mobile-web-app.md](mobile-web-app.md#artifacts). Serve finds Chrome under launchd as the CLI does: the app bundle paths are absolute, so launchd's PATH does not matter.
