# Artifact kit: block spec

A kit page is one HTML file with `kit.css` and `kit.js` inlined and a JSON spec in `<script type="application/json" id="spec">`. `kit.js` validates the spec and renders it into `<main>`. This document is the contract; `kit.js` (`SCHEMA`) and the later `agentctl artifact render` validator implement exactly this.

Rules for the whole spec:

- The spec is closed: an unknown field or block type is an error, not ignored.
- Fields are flat scalars, arrays of strings/numbers, or arrays of flat objects. `null` is never allowed except where stated (`kpis` value, chart/doughnut values, table cells), meaning "unknown". Omit optional fields instead of setting them to `null`.
- All text is escaped. The only place raw HTML is allowed is an `html` block.
- A bad block renders as a red error card naming `blocks[i].field` and the problem; the other blocks still render.
- Numbers shown to the reader go through shared formatters (below), so write raw numbers, not pre-formatted strings, wherever a field accepts a number.

## Top level

| Field | Type | Required | Notes |
|---|---|---|---|
| `kind` | `report` \| `dashboard` \| `comparison` \| `diagram` | yes | `report` and `dashboard` must contain a `sources` block, or the page shows a "No sources" warning |
| `title` | string | yes | Page `<h1>` and document title |
| `summary` | string | no | One sentence that gives the answer |
| `date` | string | no | ISO `YYYY-MM-DD`; shown as "Nov 24, 2026" |
| `author` | string | no | |
| `scope` | string | no | Meta line is `date · author · scope`, empty parts skipped |
| `blocks` | array of block | yes | Rendered in order |

Every block has `type` (required) and `title` (optional string, shown as the section heading; `sources` defaults to "Sources").

## Block types

### `summary`

Callouts. Fields: `items` (required, non-empty array of `{tone?, title?, text}`). `tone` is `info` (default) \| `good` \| `warn` \| `bad`; `text` required, `title` optional.

```json
{"type": "summary", "items": [{"tone": "bad", "title": "Regression", "text": "p95 is over the SLO."}]}
```

### `kpis`

Tiles. `items` (required, non-empty) of:

| Field | Type | Required | Notes |
|---|---|---|---|
| `label` | string | yes | |
| `value` | string \| number \| null | yes | `null` renders "unknown"; a string is shown as given |
| `format` | `number` \| `percent` \| `currency` \| `duration` \| `text` | no | default `number`; applies to numeric `value` and `delta` |
| `currency` | string | no | 3-letter ISO code, default `USD` |
| `delta` | string \| number | no | a number is formatted with a sign; a string is shown as given. A leading `+`/`-` adds an arrow |
| `trend` | `good` \| `bad` \| `neutral` | no | colours the delta by meaning, never by sign |
| `note` | string | no | small caption |

```json
{"type": "kpis", "items": [{"label": "p95", "value": 0.48, "format": "duration", "delta": 0.17, "trend": "bad"}]}
```

### `chart`

Chart.js via CDN, colours from the CSS tokens, redrawn when the colour scheme changes. If the CDN is unreachable the data is shown as a table.

| Field | Type | Required | Notes |
|---|---|---|---|
| `chart` | `line` \| `bar` \| `stacked_bar` \| `doughnut` | yes | |
| `labels` | array of string | yes | one per data point |
| `series` | array of `{name, values}` | yes | `name` string; `values` array of number or `null` (gap), same length as `labels`. A doughnut takes exactly one series and no `null` |
| `unit` | string | no | shown in the y-axis title "(unit)" and in tooltips |
| `x_label` | string | no | x-axis title |
| `y_label` | string | no | y-axis title, before the unit |
| `note` | string | no | caption under the chart |

```json
{"type": "chart", "title": "Requests per day", "chart": "line", "unit": "requests", "labels": ["Mon", "Tue"], "series": [{"name": "This week", "values": [120, null]}]}
```

### `table`

Own scroll container, sticky header, client-side sort (tap a header), numeric columns right-aligned.

| Field | Type | Required | Notes |
|---|---|---|---|
| `columns` | array of `{key, label, type?, currency?}` | yes | `key` unique; `type` is `text` (default) \| `number` \| `percent` \| `currency` \| `duration` \| `date` \| `status` |
| `rows` | array of objects | yes | keys must be column keys; values string, number, boolean or `null` (unknown) |
| `sortable` | boolean | no | default `true` |
| `sort_by` | string | no | initial sort, a column key |
| `sort_dir` | `asc` \| `desc` | no | default `asc` |
| `note` | string | no | caption |

Column types: `percent` values are already percent (`34.5` shows `34.5%`); `duration` values are seconds; `date` values are ISO dates; `status` shows a pill coloured by meaning: good (`ok pass passed passing healthy good done success succeeded green up`), warn (`warn warning degraded pending slow partial yellow running`), bad (`fail failed failing error down blocked bad red critical`), anything else neutral. Unknown cells sort last.

```json
{"type": "table", "columns": [{"key": "svc", "label": "Service"}, {"key": "p95", "label": "p95", "type": "duration"}], "rows": [{"svc": "api", "p95": 0.21}, {"svc": "web", "p95": null}]}
```

### `text`

Paragraphs and bullet lists only. `text` (required): blank line separates paragraphs; lines starting `- ` or `* ` are bullets. No other markup; everything is escaped.

```json
{"type": "text", "title": "Findings", "text": "One paragraph.\n\n- first\n- second"}
```

### `mermaid`

`source` (required): Mermaid source. `caption` (optional). Themed from the tokens for light and dark; the source is shown if the CDN is unreachable or the diagram does not parse.

```json
{"type": "mermaid", "title": "Flow", "source": "flowchart LR\n  A --> B"}
```

### `comparison`

`options` (required, 2 or more) of `{name, description?, pros?, cons?, effort?, recommended?}`: `name` required string, `pros`/`cons` arrays of string, `recommended` boolean (at most one option). `recommendation` (optional string) renders the closing recommendation card.

```json
{"type": "comparison", "options": [{"name": "A", "recommended": true, "pros": ["Fast"]}, {"name": "B", "cons": ["Slow"]}], "recommendation": "A, because it is fast."}
```

### `timeline`

`items` (required, non-empty) of `{date, title, text?, tone?}`. `date` is a string (ISO dates are formatted, anything else shown as given); `tone` is `info` (default) \| `good` \| `warn` \| `bad`.

```json
{"type": "timeline", "items": [{"date": "2026-11-14", "title": "Deploy", "tone": "bad", "text": "Latency rose."}]}
```

### `html`

Escape hatch, discouraged: use it only when no other block fits, and say so in your report. `html` (required): raw HTML inserted as-is, styled by the kit's classes and tokens. No scripts, no inline colours.

```json
{"type": "html", "html": "<p class=\"muted\">Custom <b>markup</b>.</p>"}
```

### `sources`

Footer list. `items` (required, non-empty) of `{label, url?, note?}`; `url` must be `http(s)`. Required on `report` and `dashboard` kinds; every number on the page should trace to an entry.

```json
{"type": "sources", "items": [{"label": "Grafana, checkout dashboard", "url": "https://grafana.example.com/d/x", "note": "1-23 Nov"}]}
```

## Formatters

Fixed `en-US` so every page reads the same: numbers group thousands and show up to 2 decimals (`12,400`, `0.48`); percent up to 1 decimal (`34.5%`); currency 2 decimals under 1,000 and none from 1,000 up (`$48.00`, `$1,480`); duration from seconds (`210 ms`, `3.2 s`, `4m 05s`, `2h 05m`, `3d 4h`); dates `Nov 24, 2026` (UTC, no timezone shift). Missing or `null` values show `unknown`.

## Inlining and the drift guard

Templates carry the kit between marker lines, and `plugin_manifest_test.go` fails if a copy differs from `kit/kit.css` or `kit/kit.js`:

```
/* kit:css:begin */ ... /* kit:css:end */      (inside <style>)
// kit:js:begin ... // kit:js:end               (inside <script>)
```

After editing a kit file, refresh every template with `UPDATE_ARTIFACT_KIT=1 go test -run ArtifactKit .`, then run `go test -run Plugin .`. The test also checks that each template's spec is valid JSON with only known block types. Never edit inside the marker regions of a template by hand.
