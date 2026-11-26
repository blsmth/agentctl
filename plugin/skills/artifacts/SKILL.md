---
name: artifacts
description: Publish a report, summary, table, chart, dashboard, comparison, mock website or UI, diagram, or any other visual feedback as an agentctl artifact and send the human a URL they can open on their phone or laptop. Use whenever you want to show the human something that is clearer rendered than described in chat.
---

# Artifacts

`agentctl artifact add` publishes a file and prints a direct link (`https://<host>:<port>/a/<id>/latest/`) plus the phone app link. The direct link opens in any browser on the LAN or VPN with no pairing: the id is the credential, so anyone with the link can view it. It is served by `agentctl serve` at `/a/<id>/<rev>/` inside a sandbox: scripts run, but there are no cookies and no access to any API. `https:` CDNs work (Chart.js, Mermaid, Tailwind CDN); the phone then needs internet.

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

**report, dashboard, comparison, diagram: write a spec, never HTML.**

1. Copy `${CLAUDE_PLUGIN_ROOT}/skills/artifacts/kit/examples/<kind>.json` to `/tmp/<slug>/spec.json` and edit it. Block reference: `${CLAUDE_PLUGIN_ROOT}/skills/artifacts/kit/SPEC.md`.
2. `agentctl artifact render /tmp/<slug>/spec.json` validates it, builds the page and publishes it. Every error names its JSON path (`blocks[3].series[0].values`); fix them and rerun, nothing is published until the spec is valid. It takes `--title`, `--description`, `--pin`, `--no-notify`, `--json`, and `--out page.html` to only write the file.
3. Send the human the direct link (and the one-line conclusion). Keep the id. Treat the link like a password: do not post it anywhere public (issues, PRs, commits). If serve is not running it says so; the URL is still valid once it starts.
4. To change it, edit the spec and run `agentctl artifact render spec.json --update <id> --note "what changed"`. Never publish a second artifact for the same thing.

**Client-facing reports are branded.** When a report is for a client or names one, set top-level `"brand"` (a profile from `agentctl artifact brand list`) and `"client"` (shown as "Prepared for <client>") in the spec, and set `author` to a person. Branded pages show the firm's logo, colours, font and footer, use the brand's palette (light, or dark when the brand says so), and never name agentctl, so keep tool names and internal jargon out of the content too. Hand it over as a PDF: `agentctl artifact pdf <id> --out /tmp/<slug>/<slug>.pdf` (cover page, footer with page numbers), and check the pages before sending. Never create or edit brand files; if the brand is missing or `agentctl artifact brand check <name>` fails, say so.

**mockups, markdown, images, anything else:** write the file yourself.

- `${CLAUDE_PLUGIN_ROOT}/skills/artifacts/templates/mockup.html`: free-form clickable multi-screen UI (hash routing, `sendChoice`); shares the kit tokens. `report.md`: markdown report (fenced `mermaid` blocks allowed).
- `agentctl artifact add <file> --title "Short title"`, then `agentctl artifact update <id> <file> --note "what changed"`. `add` takes `--pin`, `--no-notify`, `--json`; with stdin (`-`) pass `--entry report.html`.

Other commands: `artifact list`, `show <id>`, `url <id>`, `open <id>` (laptop browser), `pin <id>` (exempt from the 30-day cleanup), `rm <id>`.

Limits: 25 MB and 500 files per revision, 20 revisions per artifact.

## Style guide

- Lead with the answer: a title that states it and a one-sentence `summary`.
- Colour only for meaning (good/warn/bad); never decorate.
- Every number traces to a `sources` entry. Reports and dashboards need one; the page warns if it is missing.
- Unknown values are `null` (shown as "unknown"), never 0 or a guess.
- Raw numbers plus `unit`/`format`, so units and formatting stay consistent; the kit formats them.
- Screenshots, GIFs and diagram images go in an `image` block (`src` is a local file, inlined by `render`; png/jpeg/gif/webp only), not a hand-written HTML page.
- No new CSS or `html` blocks unless the kit lacks what you need; if so, say so in your report so the kit can grow.

## Hard rules

- Prefer one self-contained file: inline CSS and JS, no build step. If you need several files, use a directory with `index.html` and **relative paths only** (`./app.js`, never `/app.js`; the page lives under `/a/<id>/<rev>/`).
- **No secrets**: no tokens, API keys, env values, credentials, private URLs or personal data in the content, the data, or comments. Check before publishing; anyone with the link, and every paired device, can read it.
- **Mobile first**: design for 390 px wide, include `<meta name="viewport" content="width=device-width, initial-scale=1">`, and make it work at desktop widths too (cap content width, wrap grids). Wide tables scroll inside their own container; nothing may scroll the whole page sideways. Tap targets at least 40 px.
- Respect `prefers-color-scheme` (light and dark). Use the kit's CSS variables, not hard-coded colours (the kit already does).
- No external fonts. Use the system font stack.
- Keep it small, a few MB at most. Downscale images or inline small ones as data URIs; never embed big datasets, summarise them.
- Do not rely on `localStorage`, cookies or `fetch` to agentctl; the sandbox has no origin. Keep state in the URL hash or in memory.
- Links to other sites need `target="_blank" rel="noopener"`.
- Pin CDN libraries to an exact version (the templates do, with SRI). Show a sensible fallback when the CDN is unreachable.
- Write plain facts and label assumptions. A chart needs a title, axis labels or units.

## Feedback from the human

The viewer has a comment button. What Brendan writes there arrives in your session as a message: `Feedback on artifact "<title>" (<id>, rev N) from Brendan: <text>`, followed by `Update it with: agentctl artifact update <id> <file>`. Treat it as his instruction: change the artifact and update it (`render --update <id>` for a spec, `update` for a file; do not publish a new one). If your window was busy the message is queued; if it was closed, the comment is only stored and `agentctl artifact comments <id>` lists it (`artifact show <id>` lists them too).

An interactive mockup can offer "send my choice" buttons. Call `parent.postMessage({type: "agentctl:feedback", text: "Option B, blue header"}, "*")` from the page's script (the `mockup.html` template has a `sendChoice(text)` helper). The viewer does not send it: it opens the feedback sheet prefilled with that text and Brendan taps Send. Only `type` and a plain-text `text` (up to 4000 characters) are read; put the whole choice in the text, in words you can act on. Nothing happens when the page is opened outside the viewer.

## Check that it renders

If the `playwright-cli` skill is available, open the file with it and look at it at 390x844 and 1280x800, in light and dark (`--color-scheme` emulation). Look for sideways page scroll, clipped text, unreadable contrast and console errors. If playwright is not available, skip this step; do not install anything for it.

## Example

```bash
mkdir -p /tmp/db-options && cp "${CLAUDE_PLUGIN_ROOT}/skills/artifacts/kit/examples/comparison.json" /tmp/db-options/spec.json
# edit spec.json, then:
agentctl artifact render /tmp/db-options/spec.json
# later, after feedback:
agentctl artifact render /tmp/db-options/spec.json --update <id> --note "added cost column"
```
