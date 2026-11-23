---
name: artifacts
description: Publish a report, summary, table, chart, dashboard, comparison, mock website or UI, diagram, or any other visual feedback as an agentctl artifact and send the human a URL they can open on their phone or laptop. Use whenever you want to show the human something that is clearer rendered than described in chat.
---

# Artifacts

`agentctl artifact add` publishes a file and prints a URL. The human opens it in the agentctl phone app (PWA) or a laptop browser. It is served by `agentctl serve` at `/a/<id>/<rev>/` inside a sandbox: scripts run, but there are no cookies and no access to any API. `https:` CDNs work (Chart.js, Mermaid, Tailwind CDN); the phone then needs internet.

## Artifact or text?

Publish when the output is:
- a table with more than ~5 rows or 3 columns, a comparison, a chart or a dashboard
- a report or summary longer than ~30 lines, or one the human will want to scroll, sort or revisit
- a diagram, a mock website or UI, a layout or design option
- anything the human is asked to judge visually

Answer in text when it is a short answer, a yes/no, a status line, a code snippet or a diff. Never publish just to look thorough, and still say the one-line conclusion in chat.

## Pick the kind

| Kind | Use for | Input |
|---|---|---|
| html | charts, dashboards, mockups, diagrams, comparisons, anything interactive | one `.html` file (preferred) or a directory with `index.html` |
| markdown | prose reports, notes, tables (rendered server-side, light/dark) | `.md` |
| image | screenshots, SVG graphics | `.png` `.jpg` `.gif` `.webp` `.svg` |
| text | logs, CSV, JSON | `.txt` `.log` `.csv` `.json` |

Use `--kind` only to override the extension.

## Workflow

1. Copy the closest template from `${CLAUDE_PLUGIN_ROOT}/skills/artifacts/templates/` into `/tmp/<slug>/` (or the worktree, if the file belongs with the work). Edit only the marked DATA / CONTENT region.
   - `report.html`: summary callouts, sections, sortable table
   - `dashboard.html`: KPI tiles and Chart.js charts, data in one JSON block
   - `mockup.html`: clickable multi-screen UI with hash routing and a "mock" badge
   - `diagram.html`: Mermaid diagrams, light/dark
   - `comparison.html`: side-by-side options, pros/cons, recommendation
   - `report.md`: markdown report (fenced `mermaid` blocks allowed)
2. Publish: `agentctl artifact add /tmp/<slug>/report.html --title "Short title" --description "One line"`. It prints the id and URL. If serve is not running it says so; the URL is still valid once it starts.
3. Send the human the URL (and the one-line conclusion) in your message. Keep the id.
4. To change it, run `agentctl artifact update <id> <file> --note "what changed"`. This adds a revision at the same URL. Never `add` a second artifact for the same thing.
5. Other commands: `artifact list`, `show <id>`, `url <id>`, `open <id>` (laptop browser), `pin <id>` (exempt from the 30-day cleanup), `rm <id>`. `add` takes `--pin`, `--no-notify` (skip the push), `--json`. With stdin (`-`) pass `--entry report.html` so the kind is known.

Limits: 25 MB and 500 files per revision, 20 revisions per artifact.

## Hard rules

- Prefer one self-contained file: inline CSS and JS, no build step. If you need several files, use a directory with `index.html` and **relative paths only** (`./app.js`, never `/app.js`; the page lives under `/a/<id>/<rev>/`).
- **No secrets**: no tokens, API keys, env values, credentials, private URLs or personal data in the content, the data, or comments. Check before publishing; the store is readable by every paired device.
- **Mobile first**: design for 390 px wide, include `<meta name="viewport" content="width=device-width, initial-scale=1">`, and make it work at desktop widths too (cap content width, wrap grids). Wide tables scroll inside their own container; nothing may scroll the whole page sideways. Tap targets at least 40 px.
- Respect `prefers-color-scheme` (light and dark). Use the template's CSS variables, not hard-coded colours.
- No external fonts. Use the system font stack.
- Keep it small, a few MB at most. Downscale images or inline small ones as data URIs; never embed big datasets, summarise them.
- Do not rely on `localStorage`, cookies or `fetch` to agentctl; the sandbox has no origin. Keep state in the URL hash or in memory.
- Links to other sites need `target="_blank" rel="noopener"`.
- Pin CDN libraries to an exact version (the templates do, with SRI). Show a sensible fallback when the CDN is unreachable.
- Write plain facts and label assumptions. A chart needs a title, axis labels or units.

## Check that it renders

If the `playwright-cli` skill is available, open the file with it and look at it at 390x844 and 1280x800, in light and dark (`--color-scheme` emulation). Look for sideways page scroll, clipped text, unreadable contrast and console errors. If playwright is not available, skip this step; do not install anything for it.

## Example

```bash
mkdir -p /tmp/db-options && cp "${CLAUDE_PLUGIN_ROOT}/skills/artifacts/templates/comparison.html" /tmp/db-options/index.html
# edit the CONTENT region, then:
agentctl artifact add /tmp/db-options/index.html --title "Database options"
# later, after feedback:
agentctl artifact update <id> /tmp/db-options/index.html --note "added cost column"
```
