# Artifacts: agent-published reports, mockups and displays served by `agentctl serve`

## Goal

Agents often want to *show* Brendan something rather than describe it: a report, a comparison table, a chart, a clickable mock of a web page, a diagram. Like Claude Desktop's artifacts, an agent should publish that with one command and get back a URL; Brendan opens it on the phone (PWA) or the paired laptop browser. One entry point (`agentctl artifact ...`) for adding and managing; one place (`/v1/artifacts` + an Artifacts tab) for viewing.

## Decisions (Brendan, 2026-11-23; do not re-litigate)

- **Same server, sandboxed.** Artifacts are served by `agentctl serve` on its existing listener under `/a/...`. Every artifact response carries `Content-Security-Policy: sandbox allow-scripts allow-forms allow-popups allow-modals allow-downloads` (never `allow-same-origin`), so the document runs in an opaque origin: no device cookie on its subrequests, no reading the `/v1` API, no CSRF bypass. The PWA shows it in an `<iframe sandbox="allow-scripts allow-forms allow-popups allow-modals allow-downloads">`. The top-level `/a/` request itself is device-authenticated (cookie sent on the navigation/iframe load from the PWA origin).
- **Laptop = pair the laptop browser** once with `serve pair`; `agentctl artifact open` opens the same https URL. No file:// fallback.
- **Feedback loop later.** v1 is publish + view. Comments from the viewer delivered to the author's window come in the last PR.
- **Push on publish, on by default**: new `serve.push.artifacts` (global-only, default true), honours quiet hours and muted projects; tap opens the artifact.

## Data model and storage (`internal/artifacts`, leaf package)

Machine-wide, under `config.StateDir()/artifacts/` (dirs 0700, files 0600), resolved lazily (never at package init; testenv TestMain).

```
artifacts/<id>/meta.json
artifacts/<id>/rev/<n>/...        # content files of revision n (n = 1, 2, ...)
```

- `id`: short, URL-safe, random (e.g. 10 chars base32 lowercase); plus a human `slug` from the title for display only. IDs are unguessable but auth still applies.
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
agentctl artifact open <id> [--rev N]           # opens the URL in the laptop browser (`open` / xdg-open), seam-stubbed in tests
agentctl artifact pin|unpin <id>
agentctl artifact rm <id> [--yes]
agentctl artifact prune [--older-than 30d] [--dry-run]
```

- `-` reads stdin; requires `--kind` or `--title` with an extension hint (`--entry report.html`).
- `add`/`update` print the id and the URL (`https://<serve host>:<port>/#/artifacts/<id>`, from serve's control socket / status when running; otherwise print the id and "serve is not running; the artifact will be at <url> once it starts" using config listen + hostname). Never fail just because serve is down.
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

## PWA

- New **Artifacts** tab (`#/artifacts`, shown when the server advertises `artifacts`): list grouped by project (same order as fleet), each row: kind icon, title, agent chip, relative time, rev count, pin. Pull/SSE live.
- **Viewer** (`#/artifacts/<id>[/<rev>]`): header (back, title, origin chips: agent, project, window link → `#/w/...`, rev picker, "Open in new tab" (raw URL), pin, delete with confirm), body = full-height sandboxed iframe. Works rotated and on the laptop browser at desktop widths.
- **Window detail**: a chip "◆ N artifacts" linking to the list filtered by that window (artifacts with that window_id).
- New `web/artifacts.js` registered in `assets.go`; `just web-test`.

## Hub (small, optional in the PWA PR or its own)

- Row chip `◆N` when the window has artifacts; `o` on a row with artifacts opens the newest in the browser (`artifact open`, seam-stubbed).

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
4. **Agent adoption + hub**: preamble section (golden/prompt tests updated), hub chip + `o`.
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
- The hub shows a dim `◆N` chip before the title of a row whose window (artifact `window_id` + `session`) has N artifacts (`internal/tui/fleet_artifacts.go`). The listing is read on the poll tick, off the UI goroutine, and skipped while the store directory's mtime is unchanged, so rendering never touches disk. An in-place `update` does not change that mtime; the count is unaffected and `o` re-resolves the newest artifact when pressed.
- `o` on such a row (also in the row menu and the `?` reference) runs `agentctl artifact open` on the newest one. On a row without artifacts `o` keeps its sort-toggle meaning.
- `agentctl artifact list` pads columns by display width, with the pin in its own `PIN` column, so wide characters no longer shift a row.
