// Pure logic for the web app. No DOM, no fetch, no timers: everything here is
// a function of its arguments so it can be read and reasoned about on its own.

/** Attention order: lower sorts first. */
const RANK = { blocked: 0, working: 1, done: 2, idle: 3 };

/** effectiveState folds the done flag into a single display state. */
export function effectiveState(win) {
  if (win.state === "blocked") return "blocked";
  if (win.state === "working") return "working";
  if (win.done) return "done";
  return "idle";
}

export function stateRank(state) {
  return state in RANK ? RANK[state] : RANK.idle;
}

/** sortWindows orders windows by attention, then by window index. */
export function sortWindows(windows) {
  return [...windows].sort(
    (a, b) =>
      stateRank(effectiveState(a)) - stateRank(effectiveState(b)) ||
      (a.window_index ?? 0) - (b.window_index ?? 0),
  );
}

/** rollup is the most urgent state among windows (idle when empty). */
export function rollup(windows) {
  let best = "idle";
  for (const w of windows) {
    const s = effectiveState(w);
    if (stateRank(s) < stateRank(best)) best = s;
  }
  return best;
}

/**
 * sortGroups keeps the server's group order (the saved order shared with the
 * hub) and sorts the windows inside each group. Urgency shows through each
 * group's rollup styling and counts, not through its position.
 */
export function sortGroups(groups) {
  return groups.map((g) => {
    const windows = sortWindows(g.windows ?? []);
    return { ...g, windows, rollup: rollup(windows) };
  });
}

/** groupKeys lists the reorderable (keyed) groups' keys in display order. */
export function groupKeys(groups) {
  return groups.map((g) => g.key).filter((k) => typeof k === "string" && k !== "");
}

/** moveKey returns keys with key moved to index to (clamped); the input is untouched. */
export function moveKey(keys, key, to) {
  const from = keys.indexOf(key);
  if (from < 0) return [...keys];
  const out = keys.filter((k) => k !== key);
  out.splice(Math.max(0, Math.min(to, out.length)), 0, key);
  return out;
}

/**
 * applyGroupOrder reorders groups to follow order (keys); groups whose key is
 * not in it keep their relative order after the ordered ones.
 */
export function applyGroupOrder(groups, order) {
  if (!order || order.length === 0) return groups;
  const pos = new Map(order.map((k, i) => [k, i]));
  const rank = (g) => (pos.has(g.key) ? pos.get(g.key) : order.length);
  return groups.map((g, i) => ({ g, i })).sort((a, b) => rank(a.g) - rank(b.g) || a.i - b.i).map((d) => d.g);
}

/** sameOrder reports whether a and b list the keys they share in the same order. */
export function sameOrder(a, b) {
  const inB = new Set(b);
  const inA = new Set(a);
  const x = a.filter((k) => inB.has(k));
  const y = b.filter((k) => inA.has(k));
  return x.length === y.length && x.every((k, i) => k === y[i]);
}

/**
 * dropIndex is the slot a dragged group lands in: how many of the other
 * groups lie wholly above y. mids are the other groups' vertical midpoints in
 * display order.
 */
export function dropIndex(mids, y) {
  let n = 0;
  for (const m of mids) if (m < y) n++;
  return n;
}

/**
 * autoscrollDelta is the px to scroll per frame while dragging at pointer
 * height y in a viewport of height vh: negative near the top edge, positive
 * near the bottom, scaled up to max at the edge itself.
 */
export function autoscrollDelta(y, vh, edge = 80, max = 18) {
  if (y < edge) return -Math.ceil((max * (edge - Math.max(y, 0))) / edge);
  if (y > vh - edge) return Math.ceil((max * (Math.min(y, vh) - (vh - edge))) / edge);
  return 0;
}

/** GROUP_ORDER_PATH is the write route that saves the group order. */
export const GROUP_ORDER_PATH = "/v1/group-order";

/** buildGroupOrderRequest saves keys (the visible project groups, in order). */
export function buildGroupOrderRequest(keys) {
  return { method: "POST", path: GROUP_ORDER_PATH, body: { keys } };
}

export const SWEEP_PREVIEW_PATH = "/v1/sweep/preview";
export const SWEEP_PATH = "/v1/sweep";

/** buildSweepRequest previews (preview=true) or applies a sweep of a group's tmux session. */
export function buildSweepRequest(session, preview) {
  return { method: "POST", path: preview ? SWEEP_PREVIEW_PATH : SWEEP_PATH, body: { session } };
}

/**
 * canSweep reports whether a group header offers sweep: the write feature is
 * on and the group is a project's tmux session (it has a project dir and is
 * not the hub). The server resolves the session against the fleet again.
 */
export function canSweep(group, features) {
  if (!group || !hasFeature(features, "write")) return false;
  return Boolean(group.session) && Boolean(group.dir) && group.session !== HUB_SESSION;
}

/** sweepCounts summarises the parts of a sweep report besides the closes. */
export function sweepCounts(report) {
  const parts = [];
  const attention = report?.attention?.length ?? 0;
  const progress = report?.in_progress?.length ?? 0;
  if (attention > 0) parts.push(`${attention} ${attention === 1 ? "needs" : "need"} attention`);
  if (progress > 0) parts.push(`${progress} in progress`);
  return parts.join(", ");
}

/**
 * sweepConfirm builds the confirm sheet for a sweep preview, or returns null
 * when there is nothing to close: { title, lines, confirmLabel }.
 */
export function sweepConfirm(report, project) {
  const closed = report?.closed ?? [];
  if (closed.length === 0) return null;
  const list = closed.map((r) => (r.detail ? `${r.name}  (${r.detail})` : r.name)).join("\n");
  const lines = [`Close ${closed.length} ${closed.length === 1 ? "window" : "windows"}:\n${list}`];
  const rest = sweepCounts(report);
  if (rest) lines.push(rest);
  return { title: `Sweep ${project || report.session}?`, lines, confirmLabel: `Close ${closed.length}` };
}

/** nothingToSweep is the toast for a preview with no windows to close. */
export function nothingToSweep(report) {
  const rest = sweepCounts(report);
  return rest ? `Nothing to sweep (${rest})` : "Nothing to sweep";
}

/**
 * sweptMessage is the toast after an apply, from what actually closed (sweep
 * re-checks every window, so it can differ from the preview): { text, kind }.
 */
export function sweptMessage(report) {
  const closed = report?.closed ?? [];
  const failed = closed.filter((r) => (r.errors ?? []).length > 0);
  const ok = closed.length - failed.length;
  if (failed.length === 0) return { text: `Swept ${ok}`, kind: "info" };
  return { text: `Swept ${ok}; ${failed.length} failed: ${failed[0].name}: ${failed[0].errors.join("; ")}`, kind: "error" };
}

/**
 * runSweep is the sweep confirm flow, with its effects injected: send(req)
 * performs a request, confirm(sheet) resolves true to apply, toast(text,
 * kind) reports. Returns "applied", "cancelled", "nothing" or "failed".
 */
export async function runSweep(group, { send, confirm, toast }) {
  const project = group.project || group.session;
  let preview;
  try {
    preview = await send(buildSweepRequest(group.session, true));
  } catch (err) {
    toast(err?.message || "Sweep preview failed", "error");
    return "failed";
  }
  const sheet = sweepConfirm(preview, project);
  if (!sheet) {
    toast(nothingToSweep(preview), "info");
    return "nothing";
  }
  if (!(await confirm(sheet))) return "cancelled";
  let applied;
  try {
    applied = await send(buildSweepRequest(group.session, false));
  } catch (err) {
    toast(err?.message || "Sweep failed", "error");
    return "failed";
  }
  const msg = sweptMessage(applied);
  toast(msg.text, msg.kind);
  return "applied";
}

/** LONG_PRESS_MS is how long a group header is held before a drag starts. */
export const LONG_PRESS_MS = 400;
/** LONG_PRESS_SLOP is the movement in px that turns a pending press into a scroll. */
export const LONG_PRESS_SLOP = 10;

/** isDispatched mirrors fleet.Meta.Dispatched: launched by another agent. */
export function isDispatched(win) {
  return Boolean(win.mode) && win.mode !== "bare";
}

/** isDetached mirrors fleet.Meta.Detached: dispatched with no recorded dispatcher (phone, cron). */
export function isDetached(win) {
  return Boolean(win.detached);
}

/** rowTitle names a fleet row: the agent, or "Claude" for a session started without one. */
export function rowTitle(win) {
  return win.agent || "Claude";
}

const HEADLINE_MAX = 80;
const RECAP_SOURCES = new Set(["summary", "task", "title"]);

/** hasRecap is true when the window carries a recap worth showing in place of raw activity. */
function hasRecap(win) {
  return Boolean(win.recap) && RECAP_SOURCES.has(win.recap_source);
}

/** firstClause is the first sentence of text, trimmed and capped at max characters. */
function firstClause(text, max) {
  const t = text.replace(/\s+/g, " ").trim();
  const m = /^(.+?[.!?])(\s|$)/.exec(t);
  let out = m ? m[1] : t;
  out = out.replace(/[.!?]+$/, "");
  if (out.length > max) out = `${out.slice(0, max - 1).trimEnd()}…`;
  return out;
}

/**
 * rowHeadline is the short human title of a fleet row: the ticket title for a
 * task dispatch, else the first sentence of the recap, else the agent name.
 */
export function rowHeadline(win) {
  if (hasRecap(win)) {
    const text = win.recap_source === "task" ? win.recap.replace(/^#\d+\s+/, "") : win.recap;
    const line = firstClause(text, HEADLINE_MAX);
    if (line) return line;
  }
  return rowTitle(win);
}

/** agentChip is the agent name (no :N suffix) shown as a muted chip, or "" when it is the headline already. */
export function agentChip(win) {
  const name = (win.agent ?? "").replace(/[:~]\d+$/, "");
  if (!name || rowHeadline(win) === rowTitle(win)) return "";
  return name;
}

/**
 * rowDetailLine is the second line: the full recap, else the activity line,
 * else "". A task recap is already the headline and #issue chip, so it falls
 * through to the activity line.
 */
export function rowDetailLine(win) {
  if (hasRecap(win) && win.recap_source !== "task") return win.recap;
  return win.activity ?? "";
}

/**
 * rowTags are the identifiers worth showing on a fleet row: the issue being
 * worked and the PR it produced. Window IDs, launch modes and generated branch
 * names are internal and stay in the detail view.
 */
export function rowTags(win) {
  const tags = [];
  if (win.issue > 0) tags.push(`#${win.issue}`);
  const pr = win.pr_number > 0 ? win.pr_number : Number(/\/pull\/(\d+)/.exec(win.pr_url ?? "")?.[1] ?? 0);
  if (pr > 0) tags.push(`PR #${pr}`);
  else if (win.pr_url) tags.push("PR");
  return tags;
}

/** kindLabel is "task" for a window another agent started, "session" for one a person (or the phone/cron) started. */
export function kindLabel(win) {
  return isDispatched(win) && !isDetached(win) ? "task" : "session";
}

/**
 * bucketLabel heads the tasks whose recorded dispatcher is not in this group:
 * it was closed, or lives in another project. Detached windows (no recorded
 * dispatcher) are never bucketed there. The "scheduled" bucket heads the
 * windows `agentctl cron` launched that have no usable parent.
 */
export function bucketLabel(count, kind = "dispatched") {
  if (kind === "scheduled") return `Scheduled jobs (${count})`;
  return `Tasks from other sessions (${count})`;
}

/** isScheduled reports whether `agentctl cron` launched the window. */
export function isScheduled(win) {
  return Boolean(win.scheduled_by);
}

/** scheduledChip is the "⏲ job" chip of a cron-launched row, or "". */
export function scheduledChip(win) {
  return isScheduled(win) ? `⏲ ${win.scheduled_by}` : "";
}

const WEEKDAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

/**
 * scheduledLine is the window view's provenance line for a cron run:
 * "Scheduled run: nightly-triage, fired Wed 18 Nov 07:00", in the device's
 * time zone; the time is left off when unknown. "" for other windows.
 */
export function scheduledLine(win) {
  if (!isScheduled(win)) return "";
  const at = win.scheduled_at ? new Date(win.scheduled_at) : null;
  if (!at || Number.isNaN(at.getTime())) return `Scheduled run: ${win.scheduled_by}`;
  const pad = (n) => String(n).padStart(2, "0");
  const when = `${WEEKDAYS[at.getDay()]} ${at.getDate()} ${MONTHS[at.getMonth()]} ${pad(at.getHours())}:${pad(at.getMinutes())}`;
  return `Scheduled run: ${win.scheduled_by}, fired ${when}`;
}

/**
 * treeLines describes the connector lines for a tree entry as data: `through`
 * has one boolean per ancestor level (true = that ancestor has later siblings,
 * so its vertical continues through this row) and `last` says whether the
 * row's own elbow ends here (└) or continues (├).
 */
export function treeLines(entry) {
  if (entry.depth === 0) return { through: [], last: false };
  return { through: entry.ancestorsLast.map((l) => !l), last: Boolean(entry.last) };
}

/**
 * treeRows flattens one group's windows into display order. It ports
 * internal/tui/fleet_tree.go treeRows (shared cases: internal/tui/testdata/
 * tree_cases.json). A window nests under win.parent when that is another
 * window of the group (and not itself). Windows that are not dispatched or are
 * detached (no recorded dispatcher) and have no such parent are roots.
 * Dispatched windows whose recorded parent is unusable, plus windows
 * stuck in a parent cycle, go under a trailing {label, bucket:true,
 * kind:"dispatched"} entry; cron windows with no usable parent go under a
 * {kind:"scheduled"} bucket after it.
 * Roots and children are in attention order; every window is emitted once.
 * Entries are {win, depth, last, ancestorsLast[]} or {label, bucket:true, kind, count, depth:0}.
 */
export function treeRows(windows) {
  const rows = sortWindows(windows);
  const idx = new Map();
  rows.forEach((r, i) => {
    if (r.window_id && !idx.has(r.window_id)) idx.set(r.window_id, i);
  });
  const children = rows.map(() => []);
  const hasParent = rows.map(() => false);
  rows.forEach((r, i) => {
    if (!r.parent || !idx.has(r.parent)) return;
    const p = idx.get(r.parent);
    if (p === i) return;
    hasParent[i] = true;
    children[p].push(i);
  });

  const visited = rows.map(() => false);
  const out = [];
  const walk = (i, depth, ancestorsLast, last) => {
    visited[i] = true;
    out.push({ win: rows[i], depth, last, ancestorsLast });
    const kids = children[i].filter((c) => !visited[c]);
    const childAnc = depth > 0 ? [...ancestorsLast, last] : ancestorsLast;
    for (let n = 0; n < kids.length; n++) {
      if (visited[kids[n]]) continue; // reached via an earlier sibling's subtree
      walk(kids[n], depth + 1, childAnc, n === kids.length - 1);
    }
  };

  // Windows `agentctl cron` launched have no starting session; those without
  // a usable parent get their own "scheduled" bucket.
  const schedRoot = (i) => !hasParent[i] && isScheduled(rows[i]);
  // schedTree marks scheduled roots and everything below them, so the
  // dispatched bucket's cycle pass leaves their subtrees alone.
  const schedTree = rows.map(() => false);
  const mark = (i) => {
    if (schedTree[i]) return;
    schedTree[i] = true;
    children[i].forEach(mark);
  };
  rows.forEach((_, i) => schedRoot(i) && mark(i));

  rows.forEach((r, i) => {
    if ((!isDispatched(r) || isDetached(r)) && !hasParent[i] && !schedRoot(i)) walk(i, 0, [], true);
  });

  // bucket appends a header plus every unvisited window pick selects (two
  // passes): walk every top-level entry as non-last, then fix up the final one.
  const bucket = (kind, pick) => {
    const head = out.length;
    out.push({ bucket: true, kind, depth: 0 });
    let lastTop = -1;
    for (let pass = 0; pass < 2; pass++) {
      rows.forEach((r, i) => {
        if (visited[i] || !pick(i, pass)) return;
        lastTop = out.length;
        walk(i, 1, [], false);
      });
    }
    if (lastTop < 0) {
      out.length = head;
      return;
    }
    out[lastTop].last = true;
    for (let j = lastTop + 1; j < out.length; j++) out[j].ancestorsLast[0] = true;
    out[head].count = out.length - head - 1;
    out[head].label = `${kind} (${out[head].count})`;
  };
  // Dispatched windows with no usable parent, then cycles; scheduled
  // subtrees are kept for their own bucket.
  bucket("dispatched", (i, pass) => !schedTree[i] && (pass === 1 || (!hasParent[i] && isDispatched(rows[i]) && !isDetached(rows[i]))));
  bucket("scheduled", (i, pass) => pass === 0 && schedRoot(i));
  return out;
}

/** descendants returns the entries nested under entries[i] (consecutive deeper entries). */
export function descendants(entries, i) {
  const d = entries[i].depth;
  const out = [];
  for (let j = i + 1; j < entries.length && entries[j].depth > d; j++) out.push(entries[j]);
  return out;
}

/** collapseKey identifies a collapsible entry: window, or the group's bucket. */
export function collapseKey(session, entry) {
  if (!entry.bucket) return `${session}\u0000${entry.win.window_id}`;
  return `${session}\u0000${entry.kind === "scheduled" ? "bucket:scheduled" : "bucket"}`;
}

/**
 * groupKey is the collapse key of a whole project group. Window IDs start
 * with "@", so it never collides with a collapseKey.
 */
export function groupKey(session) {
  return `${session}\u0000group`;
}

/**
 * groupCounts tallies a group's windows by display state in attention order,
 * omitting empty states: what a folded project header shows (as on the hub).
 */
export function groupCounts(windows) {
  const n = { blocked: 0, working: 0, done: 0, idle: 0 };
  for (const w of windows) n[effectiveState(w)]++;
  return Object.keys(RANK)
    .filter((s) => n[s] > 0)
    .map((state) => ({ state, n: n[state] }));
}

/** visibleTree drops the entries hidden under a collapsed entry. */
export function visibleTree(entries, session, collapsed) {
  const out = [];
  let skipBelow = Infinity;
  for (const e of entries) {
    if (e.depth > skipBelow) continue;
    skipBelow = Infinity;
    out.push(e);
    if (collapsed.has(collapseKey(session, e)) && descendantsOf(entries, e).length > 0) skipBelow = e.depth;
  }
  return out;
}

function descendantsOf(entries, e) {
  return descendants(entries, entries.indexOf(e));
}

/** formatSince renders "time in state": "12s", "5m", "2h 05m", "3d". */
export function formatSince(iso, nowMs) {
  if (!iso) return "";
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return "";
  const s = Math.max(0, Math.floor((nowMs - t) / 1000));
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ${String(m % 60).padStart(2, "0")}m`;
  return `${Math.floor(h / 24)}d`;
}

/** formatClock renders a timestamp (ms) as local HH:MM. */
export function formatClock(ms) {
  const d = new Date(ms);
  return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
}

/** windowKey identifies a window across sessions. */
export function windowKey(win) {
  return `${win.session}\u0000${win.window_id}`;
}

/** windowApiPath is the API path for a window; tmux ids like "@523" are escaped. */
export function windowApiPath(session, windowId) {
  return `/v1/sessions/${encodeURIComponent(session)}/windows/${encodeURIComponent(windowId)}`;
}

/** windowHash is the app route for a window. */
export function windowHash(session, windowId) {
  return `#/w/${encodeURIComponent(session)}/${encodeURIComponent(windowId)}`;
}

/** dispatchHash is the Dispatch route with a project dir preselected. */
export function dispatchHash(dir) {
  return `#/dispatch/${encodeURIComponent(dir)}`;
}

/** terminalHash is the app route for a window's live terminal. */
export function terminalHash(session, windowId) {
  return `#/t/${encodeURIComponent(session)}/${encodeURIComponent(windowId)}`;
}

/** terminalSocketUrl is the WebSocket URL of a window's live terminal on this origin. */
export function terminalSocketUrl(loc, session, windowId) {
  const scheme = loc.protocol === "https:" ? "wss:" : "ws:";
  return `${scheme}//${loc.host}${windowApiPath(session, windowId)}/terminal`;
}

/** parseRoute turns location.hash into {name, ...params}. */
export function parseRoute(hash) {
  const parts = (hash || "").replace(/^#\/?/, "").split("/").filter(Boolean);
  try {
    if (parts[0] === "w" && parts.length >= 3) {
      return { name: "window", session: decodeURIComponent(parts[1]), windowId: decodeURIComponent(parts[2]) };
    }
    if (parts[0] === "t" && parts.length >= 3) {
      return { name: "terminal", session: decodeURIComponent(parts[1]), windowId: decodeURIComponent(parts[2]) };
    }
  } catch {
    return { name: "fleet" };
  }
  if (parts[0] === "approvals") {
    let id = "";
    try {
      id = parts[1] ? decodeURIComponent(parts[1]) : "";
    } catch {
      id = "";
    }
    return { name: "approvals", id };
  }
  if (parts[0] === "dispatch") {
    if (parts.length >= 2) {
      try {
        return { name: "dispatch", dir: decodeURIComponent(parts[1]) };
      } catch {
        return { name: "dispatch" };
      }
    }
    return { name: "dispatch" };
  }
  if (parts[0] === "settings") return { name: "settings" };
  return { name: "fleet" };
}

export function hasFeature(features, name) {
  return Array.isArray(features) && features.includes(name);
}

/**
 * buildAnswerRequest builds the request that answers a dialog. It always sends
 * the hash of the dialog the user was shown, so the server can refuse (409)
 * when the pane has moved on. key is an option key or "esc".
 */
export function buildAnswerRequest(session, windowId, dialog, key) {
  if (!dialog || !dialog.hash) throw new Error("no dialog to answer");
  const valid = key === "esc" || (dialog.options ?? []).some((o) => o.key === key);
  if (!valid) throw new Error(`key ${key} is not an option of this dialog`);
  return {
    method: "POST",
    path: `${windowApiPath(session, windowId)}/answer`,
    body: { key, dialog_hash: dialog.hash },
  };
}

/** MAX_IMAGES: the most images one message carries (the server enforces the same). */
export const MAX_IMAGES = 4;
/** MAX_IMAGE_BYTES: the largest image the server accepts. */
export const MAX_IMAGE_BYTES = 10 * 1024 * 1024;

/** messageBody is the JSON body of a reply or queue: text, plus uploaded image IDs when there are any. */
function messageBody(text, images) {
  return images && images.length ? { text, images } : { text };
}

/** buildReplyRequest builds POST …/reply. images are IDs returned by an upload. */
export function buildReplyRequest(session, windowId, text, images) {
  return { method: "POST", path: `${windowApiPath(session, windowId)}/reply`, body: messageBody(text, images) };
}

/** buildUploadPath is the POST …/upload path of a window. */
export function buildUploadPath(session, windowId) {
  return `${windowApiPath(session, windowId)}/upload`;
}

/**
 * checkAttachment reports why a picked file cannot be attached ("" when it can):
 * images only, within the size cap, and within the per-message count.
 */
export function checkAttachment(file, count) {
  if (count >= MAX_IMAGES) return `At most ${MAX_IMAGES} images per message.`;
  if (!/^image\/(png|jpe?g|gif|webp)$/i.test(file.type ?? "")) return `${file.name || "File"} is not a png, jpeg, gif or webp image.`;
  if (file.size > MAX_IMAGE_BYTES) return `${file.name || "Image"} is larger than ${MAX_IMAGE_BYTES >> 20} MB.`;
  return "";
}

/**
 * buildDispatchRequest builds POST /v1/dispatch. choice is the optional
 * {backend, model, effort} picked from launch options; empty fields are left
 * out so the server keeps the agent's own configuration.
 */
export function buildDispatchRequest(agent, projectDir, prompt, choice = {}) {
  const body = { agent, project_dir: projectDir, prompt };
  for (const k of ["backend", "model", "effort"]) if (choice[k]) body[k] = choice[k];
  return { method: "POST", path: "/v1/dispatch", body };
}

/** launchOptionsPath is GET /v1/launch-options for an agent in a project. */
export function launchOptionsPath(agent, projectDir) {
  return `/v1/launch-options?agent=${encodeURIComponent(agent)}&project_dir=${encodeURIComponent(projectDir)}`;
}

/**
 * modelPicker describes the model control for a backend of a launch-options
 * body: a select of choices, or a free-text field (with the default prefilled)
 * when the server could not list the backend's models.
 */
export function modelPicker(opts, backend) {
  const m = opts?.models?.[backend];
  const choices = m?.models ?? [];
  const def = choices.find((c) => c.default)?.value ?? choices[0]?.value ?? "";
  return { freeText: Boolean(m?.model_list_error), choices, def, error: m?.model_list_error ?? "" };
}

/** choiceLabel is the option text of a model choice: value, note, default mark. */
export function choiceLabel(c) {
  return `${c.value}${c.note ? ` (${c.note})` : ""}${c.default ? " \u2605 default" : ""}`;
}

/** confirmLines are the sheet lines for a dispatch: settings, then the prompt. */
export function confirmLines(body) {
  const rows = [`agent: ${body.agent}`, `project: ${body.project_dir}`];
  if (body.backend) rows.push(`backend: ${body.backend}`);
  if (body.model) rows.push(`model: ${body.model}`);
  if (body.effort) rows.push(`effort: ${body.effort}`);
  return [rows.join("\n"), body.prompt];
}

/** actionsFor decides which action-bar sections a window shows. */
export function actionsFor(win, pane) {
  if (pane && pane.verdict === "awaiting_input" && pane.dialog) return "answer";
  const s = effectiveState(win);
  if (s === "working") return "interrupt";
  if (s === "idle" || s === "done") return "reply";
  return "none";
}

/**
 * actionsWithQueue is actionsFor for a server that can queue a message to a
 * working agent: "queue" (composer plus a small Interrupt) replaces
 * "interrupt" when hasQueue is set.
 */
export function actionsWithQueue(win, pane, hasQueue) {
  const kind = actionsFor(win, pane);
  return kind === "interrupt" && hasQueue ? "queue" : kind;
}

export function buildQueueRequest(session, windowId, text, images) {
  return { method: "POST", path: `${windowApiPath(session, windowId)}/queue`, body: messageBody(text, images) };
}

/**
 * dialogQuestion is the line of a dialog that asks the question: the last
 * line ending in "?" before the first option, else the line just before the
 * options, else the first line.
 */
export function dialogQuestion(dialog) {
  const lines = (dialog?.text ?? "").split("\n").map((l) => l.trim()).filter(Boolean);
  const first = lines.findIndex((l) => /^\d+\.\s/.test(l));
  const head = first < 0 ? lines : lines.slice(0, first);
  for (let i = head.length - 1; i >= 0; i--) if (head[i].endsWith("?")) return head[i];
  return head[head.length - 1] ?? lines[0] ?? "";
}

/**
 * focusedOption is the key of the dialog option the pane's cursor (❯) is
 * on, which is the option Claude Code recommends by default; "" when the
 * pane shows none of the dialog's options focused.
 */
export function focusedOption(paneText, dialog) {
  const keys = new Set((dialog?.options ?? []).map((o) => o.key));
  const plain = (paneText ?? "").replace(ESC_RE, "");
  let found = "";
  for (const m of plain.matchAll(/❯\s*(\d{1,2})\.\s/g)) if (keys.has(m[1])) found = m[1];
  return found;
}

/**
 * shortModel trims a model id for a chip: "claude-" prefix and a trailing
 * date stamp go ("claude-sonnet-4-20250514" -> "sonnet-4").
 */
export function shortModel(model) {
  if (!model) return "";
  return model.replace(/^claude-/, "").replace(/-\d{8}$/, "").replace(/\[.*\]$/, "");
}

/** formatCost renders a USD cost: "$0.98", "$12.30", "<$0.01". */
export function formatCost(usd) {
  if (!(usd > 0)) return "";
  if (usd < 0.01) return "<$0.01";
  return `$${usd.toFixed(2)}`;
}

/** taskLine is the task the window was given: its ticket title, else its issue, else "". */
export function taskLine(win) {
  const title = win?.detail?.title?.trim();
  if (title) return title;
  if (win?.issue > 0) return `Issue #${win.issue}`;
  if (win?.ticket) return /^\d+$/.test(win.ticket) ? `Issue #${win.ticket}` : win.ticket;
  return "";
}

/**
 * headerLines are the window header's two text lines. task is the task the
 * window was given; recap is what it is doing (away summary or session title),
 * shown in place of the last tool call. A task recap repeats the task line, so
 * it stands in for the task only when there is none. With neither, the task
 * line falls back to the last tool call, keeping the header to one line.
 */
export function headerLines(win) {
  let task = taskLine(win);
  let recap = "";
  if (win && hasRecap(win)) {
    if (win.recap_source !== "task") recap = win.recap;
    else if (!task) task = win.recap.replace(/^#\d+\s+/, "");
  }
  if (!task && !recap) task = win?.activity ?? "";
  return { task, recap };
}

/** stateLabel is the header pill text: "working for 3m", "idle". */
export function stateLabel(win, nowMs) {
  const s = effectiveState(win);
  const since = formatSince(win.blocked_since || win.state_since, nowMs);
  return since ? `${s} for ${since}` : s;
}

/**
 * headerChips lists the window's chips, present fields only, as
 * {text, href?, kind}: PR (link), context, cost, model, then window id and
 * mode.
 */
export function headerChips(win, safeUrl) {
  const chips = [];
  const pr = safeUrl(win.pr_url ?? "");
  if (pr) chips.push({ kind: "pr", text: win.pr_number ? `PR #${win.pr_number}` : prLabel(pr), href: pr });
  if (win.context_pct) chips.push({ kind: "ctx", text: `${Math.round(win.context_pct)}% ctx` });
  const cost = formatCost(win.detail?.cost_usd);
  if (cost) chips.push({ kind: "cost", text: cost });
  const model = shortModel(win.detail?.model);
  if (model) chips.push({ kind: "model", text: model });
  const id = [win.window_id, win.mode].filter(Boolean).join(" · ");
  if (id) chips.push({ kind: "id", text: id });
  return chips;
}

/** prLabel is "PR #8" for a GitHub pull URL, else "PR". */
function prLabel(url) {
  const m = /\/pull\/(\d+)(?:[/?#]|$)/.exec(url);
  return m ? `PR #${m[1]}` : "PR";
}

/** FRESH_MS is how long after a pane change the liveness chip reads as fresh. */
export const FRESH_MS = 10000;

/**
 * liveness describes the pane bar's liveness chip. offlineSince (ms) is set
 * while the window cannot be fetched (or the live feed is down); otherwise
 * the chip says how long ago the pane last changed. Returns {text, kind}
 * with kind "offline", "fresh" or "stale"; text "" before the first fetch.
 */
export function liveness({ lastChangeAt, offlineSince }, nowMs) {
  if (offlineSince) return { text: `offline since ${formatClock(offlineSince)}`, kind: "offline" };
  if (!lastChangeAt) return { text: "", kind: "stale" };
  const s = Math.max(0, Math.floor((nowMs - lastChangeAt) / 1000));
  const ago = s < 60 ? `${s}s` : formatSince(new Date(lastChangeAt).toISOString(), nowMs);
  return { text: `updated ${ago} ago`, kind: nowMs - lastChangeAt < FRESH_MS ? "fresh" : "stale" };
}

/** backoffDelay is the reconnect delay in ms for the nth failed attempt (0-based), capped at 30s. */
export function backoffDelay(attempt) {
  return Math.min(30000, 1000 * 2 ** attempt);
}

/** applyWindow upserts a window into a fleet body, creating the group if needed. */
export function applyWindow(fleet, win) {
  const groups = (fleet.groups ?? []).map((g) => ({ ...g, windows: [...(g.windows ?? [])] }));
  let group = groups.find((g) => g.session === win.session);
  if (!group) {
    group = { session: win.session, project: win.project, dir: "", windows: [] };
    groups.push(group);
  }
  const i = group.windows.findIndex((w) => w.window_id === win.window_id);
  if (i >= 0) group.windows[i] = win;
  else group.windows.push(win);
  return { ...fleet, groups };
}

/** applyGone removes a window, and its group if that leaves it empty. */
export function applyGone(fleet, session, windowId) {
  const groups = (fleet.groups ?? [])
    .map((g) =>
      g.session === session ? { ...g, windows: (g.windows ?? []).filter((w) => w.window_id !== windowId) } : g,
    )
    .filter((g) => g.windows.length > 0);
  return { ...fleet, groups };
}

/** urlBase64ToBytes decodes a VAPID public key. */
export function urlBase64ToBytes(s) {
  const pad = "=".repeat((4 - (s.length % 4)) % 4);
  const raw = atob((s + pad).replace(/-/g, "+").replace(/_/g, "/"));
  return Uint8Array.from(raw, (c) => c.charCodeAt(0));
}

/** safeHttpsUrl returns the normalised URL when s parses as an https URL, else "". */
export function safeHttpsUrl(s) {
  try {
    const u = new URL(s);
    return u.protocol === "https:" ? u.href : "";
  } catch {
    return "";
  }
}

/**
 * parseControl parses a terminal control frame: {type:"size", cols, rows,
 * read_only} or {type:"ping"}. Anything else returns null.
 */
export function parseControl(text) {
  let m;
  try {
    m = JSON.parse(text);
  } catch {
    return null;
  }
  if (!m || typeof m !== "object") return null;
  if (m.type === "ping") return { type: "ping" };
  if (m.type === "size" && Number.isInteger(m.cols) && Number.isInteger(m.rows) && m.cols > 0 && m.rows > 0) {
    return { type: "size", cols: Math.min(m.cols, 1000), rows: Math.min(m.rows, 1000), readOnly: m.read_only === true };
  }
  return null;
}

/**
 * fitFontSize scales a font so cols columns fill width. measured is the
 * rendered width of those columns at font size base; xterm cell width is
 * linear in font size. The result is clamped to [min, max].
 */
export function fitFontSize(width, measured, base, min = 5, max = 16) {
  if (!(width > 0) || !(measured > 0) || !(base > 0)) return base;
  const f = Math.floor(((base * width) / measured) * 10) / 10;
  return Math.max(min, Math.min(max, f));
}

/**
 * KEY_BAR lists the extra keys a phone keyboard lacks. Arrow keys depend on
 * the terminal's cursor-key mode (see keySequence).
 */
export const KEY_BAR = [
  { label: "Esc", seq: "\x1b" },
  { label: "Tab", seq: "\t" },
  { label: "^C", seq: "\x03" },
  { label: "←", arrow: "D" },
  { label: "↑", arrow: "A" },
  { label: "↓", arrow: "B" },
  { label: "→", arrow: "C" },
  { label: "1", seq: "1" },
  { label: "2", seq: "2" },
  { label: "⏎", seq: "\r" },
];

/** keySequence returns the bytes a key-bar key sends. */
export function keySequence(key, applicationCursor) {
  if (key.arrow) return `\x1b${applicationCursor ? "O" : "["}${key.arrow}`;
  return key.seq;
}

// ---------------------------------------------------------------------------
// Pane rendering (Reflow mode): ANSI SGR parsing and line normalisation.

const ESC_RE = /\x1b(?:\[([0-9;:?<=> ]*)([@-~])|\][^\x07\x1b]*(?:\x07|\x1b\\)|[@-Z\\-_])/g;

const BASE16 = [
  "#000000", "#cd3131", "#0dbc79", "#e5e510", "#2472c8", "#bc3fbc", "#11a8cd", "#e5e5e5",
  "#666666", "#f14c4c", "#23d18b", "#f5f543", "#3b8eea", "#d670d6", "#29b8db", "#ffffff",
];

/** ansiColor maps a 0-255 palette index to a CSS hex colour. */
export function ansiColor(n) {
  if (n < 16) return BASE16[n];
  if (n >= 232) {
    const v = 8 + (n - 232) * 10;
    return `#${[v, v, v].map((x) => x.toString(16).padStart(2, "0")).join("")}`;
  }
  const i = n - 16;
  const lv = (c) => (c === 0 ? 0 : 55 + c * 40);
  const rgb = [Math.floor(i / 36), Math.floor(i / 6) % 6, i % 6].map(lv);
  return `#${rgb.map((x) => x.toString(16).padStart(2, "0")).join("")}`;
}

const hex2 = (n) => Math.max(0, Math.min(255, n | 0)).toString(16).padStart(2, "0");

/** applySgr folds one SGR parameter list into style (a fresh object is returned). */
function applySgr(style, paramStr) {
  const p = paramStr === "" ? [0] : paramStr.replace(/:/g, ";").split(";").map((x) => (x === "" ? 0 : Number(x)));
  let s = { ...style };
  for (let i = 0; i < p.length; i++) {
    const c = p[i];
    if (c === 0) s = {};
    else if (c === 1) s.bold = true;
    else if (c === 2) s.dim = true;
    else if (c === 3) s.italic = true;
    else if (c === 4) s.underline = true;
    else if (c === 7) s.inverse = true;
    else if (c === 22) { delete s.bold; delete s.dim; }
    else if (c === 23) delete s.italic;
    else if (c === 24) delete s.underline;
    else if (c === 27) delete s.inverse;
    else if (c >= 30 && c <= 37) s.fg = ansiColor(c - 30);
    else if (c >= 90 && c <= 97) s.fg = ansiColor(c - 90 + 8);
    else if (c >= 40 && c <= 47) s.bg = ansiColor(c - 40);
    else if (c >= 100 && c <= 107) s.bg = ansiColor(c - 100 + 8);
    else if (c === 39) delete s.fg;
    else if (c === 49) delete s.bg;
    else if (c === 38 || c === 48) {
      const key = c === 38 ? "fg" : "bg";
      if (p[i + 1] === 5 && Number.isFinite(p[i + 2])) {
        s[key] = ansiColor(Math.max(0, Math.min(255, p[i + 2])));
        i += 2;
      } else if (p[i + 1] === 2) {
        // Tolerate the colon form with an empty colour-space id (38:2::r:g:b).
        let j = i + 2;
        if (p.length - j >= 4) j++;
        s[key] = `#${hex2(p[j])}${hex2(p[j + 1])}${hex2(p[j + 2])}`;
        i = j + 2;
      }
    }
  }
  return s;
}

/**
 * parseAnsi splits text into lines of styled runs. Only SGR is interpreted;
 * every other escape sequence is dropped. A run is {t, fg?, bg?, bold?, dim?,
 * italic?, underline?, inverse?} with colours as CSS hex strings. Style
 * carries across lines, as it does in a terminal.
 */
export function parseAnsi(text) {
  const lines = [[]];
  let style = {};
  const push = (t) => {
    if (t) lines[lines.length - 1].push({ t, ...style });
  };
  const feed = (chunk) => {
    const parts = chunk.replace(/\r/g, "").split("\n");
    parts.forEach((part, i) => {
      if (i > 0) lines.push([]);
      push(part);
    });
  };
  let last = 0;
  for (const m of text.matchAll(ESC_RE)) {
    feed(text.slice(last, m.index));
    last = m.index + m[0].length;
    if (m[2] === "m") style = applySgr(style, m[1]);
  }
  feed(text.slice(last));
  return lines;
}

const RULE_CH = "─━═┄┈╌╍┅┉╴╶╸╺";
const CORNER_CH = "╭╮╰╯┌┐└┘┏┓┗┛╔╗╚╝├┤┬┴┼┠┨┯┷╠╣╦╩╬";
const RULE_ONLY = new RegExp(`^[${RULE_CH}${CORNER_CH}\\s]*[${RULE_CH}]{3}[${RULE_CH}${CORNER_CH}\\s]*$`);
const RULE_LABEL = new RegExp(`^[${CORNER_CH}]?[${RULE_CH}]{2,}\\s+(.*?[^${RULE_CH}${CORNER_CH}\\s].*?)\\s+[${RULE_CH}]{2,}[${CORNER_CH}]?$`);
const BLANKISH = /^[\s⠀]*$/;
const SIDE_CH = "│┃║";

/** ruleOf returns {label} when plain is a horizontal rule, else null. */
export function ruleOf(plain) {
  const s = plain.replace(/[⠀]/g, " ").trim();
  if (RULE_ONLY.test(s)) return { label: "" };
  const m = RULE_LABEL.exec(s);
  return m ? { label: m[1].trim() } : null;
}

const isSpace = (ch) => ch === " " || ch === "\t" || ch === "⠀";

/** normalizeCells cleans one line (an array of {ch, s} cells) for narrow reading. */
function normalizeCells(cells) {
  let c = cells.slice();
  const trimEnd = () => {
    while (c.length && isSpace(c[c.length - 1].ch)) c.pop();
  };
  const trimStart = () => {
    while (c.length && isSpace(c[0].ch)) c.shift();
  };
  trimEnd();
  // Right-aligned padding: braille blanks, or a deep run of plain spaces
  // before a short segment.
  let lead = 0;
  while (lead < c.length && isSpace(c[lead].ch)) lead++;
  const padded = c.slice(0, lead).some((x) => x.ch === "⠀") || (lead >= 16 && c.length - lead <= 60);
  if (padded) trimStart();
  // Box side borders (dialog and prompt boxes).
  let i = 0;
  while (i < c.length && c[i].ch === " ") i++;
  if (i < c.length && SIDE_CH.includes(c[i].ch)) {
    c.splice(0, i + 1);
    if (c.length && c[0].ch === " ") c.shift();
  }
  trimEnd();
  if (c.length && SIDE_CH.includes(c[c.length - 1].ch)) c.pop();
  trimEnd();
  // Interior alignment padding containing braille becomes a plain gap.
  const out = [];
  for (let k = 0; k < c.length; k++) {
    if (isSpace(c[k].ch)) {
      let e = k;
      let braille = false;
      while (e < c.length && isSpace(c[e].ch)) {
        if (c[e].ch === "⠀") braille = true;
        e++;
      }
      if (braille) out.push({ ch: " ", s: c[k].s }, { ch: " ", s: c[k].s });
      else out.push(...c.slice(k, e));
      k = e - 1;
    } else out.push(c[k]);
  }
  return out;
}

function cellsToRuns(cells) {
  const runs = [];
  for (const { ch, s } of cells) {
    const prev = runs[runs.length - 1];
    if (prev && prev.s === s) prev.t += ch;
    else runs.push({ s, t: ch });
  }
  return runs.map(({ s, t }) => ({ t, ...s }));
}

/**
 * normalizePane turns captured pane text (ANSI allowed) into render blocks:
 * {rule: label} for a horizontal rule, or {runs} for a text line (an empty
 * runs array is a blank line). See the numbered rules in the pane view:
 * trailing whitespace trimmed, braille/right-alignment padding collapsed,
 * rule-only lines become one rule, box side borders stripped, 3+ blank lines
 * collapse to one.
 */
export function normalizePane(text) {
  return collapseBlanks(lineBlocks(parseAnsi(text)));
}

/** lineBlocks maps parsed lines to one block each, before blank collapsing. */
function lineBlocks(lines) {
  return lines.map((runs) => {
    const plain = runs.map((r) => r.t).join("");
    const rule = ruleOf(plain);
    if (rule) return { rule: rule.label };
    const cells = runs.flatMap((r) => {
      const { t, ...s } = r;
      return Array.from(t, (ch) => ({ ch, s }));
    });
    const norm = normalizeCells(cells);
    if (norm.length === 0 || BLANKISH.test(norm.map((x) => x.ch).join(""))) return { runs: [] };
    return { runs: cellsToRuns(norm) };
  });
}

/** collapseBlanks collapses 3+ consecutive blank lines to one. */
function collapseBlanks(blocks) {
  const out = [];
  for (let i = 0; i < blocks.length; i++) {
    if (blocks[i].runs && blocks[i].runs.length === 0) {
      let j = i;
      while (j < blocks.length && blocks[j].runs && blocks[j].runs.length === 0) j++;
      const n = j - i;
      for (let k = 0; k < (n >= 3 ? 1 : n); k++) out.push({ runs: [] });
      i = j - 1;
    } else out.push(blocks[i]);
  }
  return out;
}

const blockText = (b) => (b.runs ? b.runs.map((r) => r.t).join("") : "");
const isPromptLine = (b) => /^[>❯]/.test(blockText(b));
const isBlank = (b) => b.runs !== undefined && b.runs.length === 0;

/**
 * promptOf describes the prompt box's input lines: text is what was typed
 * (the leading ">" or "❯" dropped, trimmed), and empty is true when nothing
 * was. Dim-only text after the glyph is Claude Code's placeholder hint and
 * counts as empty.
 */
function promptOf(blocks) {
  const typed = blocks.map((b, i) => {
    const runs = b.runs ?? [];
    const kept = runs.filter((r) => !r.dim);
    let t = kept.map((r) => r.t).join("");
    if (i === 0) t = t.replace(/^\s*[>❯]/, "");
    return t.trim();
  });
  const text = typed.filter(Boolean).join("\n");
  return { text, empty: text === "" };
}

/**
 * findPromptBox locates Claude Code's prompt input box in render blocks: a
 * rule, a line starting with ">" or "❯", optional continuation lines, then
 * the last rule. Returns {start, end} (the two rule indices) or null.
 */
function findPromptBox(blocks) {
  let end = -1;
  for (let i = blocks.length - 1; i >= 0; i--) {
    if (blocks[i].rule !== undefined) {
      end = i;
      break;
    }
  }
  let start = -1;
  for (let i = end - 1; i >= 0 && end >= 0; i--) {
    if (blocks[i].rule !== undefined) {
      start = i;
      break;
    }
  }
  if (start < 0 || end - start < 2 || !isPromptLine(blocks[start + 1])) return null;
  return { start, end };
}

/**
 * splitPane is normalizePane plus detection of Claude Code's trailing chrome:
 * the prompt input box and everything after the rule that closes it.
 * Returns {body, status, prompt}:
 *   - prompt is null when no prompt box is found, else {text, empty}.
 *   - status is null when there is no prompt box or nothing below it,
 *     otherwise {lines} with the raw plain lines (padding intact, trailing
 *     blank lines dropped).
 *   - body ends at the box's closing rule when something is typed in it;
 *     when the prompt is empty the box is left out (body ends at the last
 *     non-blank line above it), since an empty box says nothing.
 */
export function splitPane(text) {
  const lines = parseAnsi(text);
  const blocks = lineBlocks(lines);
  const box = findPromptBox(blocks);
  if (!box) return { body: collapseBlanks(blocks), status: null, prompt: null };
  const prompt = promptOf(blocks.slice(box.start + 1, box.end));
  const raw = lines.slice(box.end + 1).map((runs) => runs.map((r) => r.t).join("").replace(/\s+$/, ""));
  while (raw.length && raw[raw.length - 1].replace(/[\s⠀]/g, "") === "") raw.pop();
  let body = blocks.slice(0, prompt.empty ? box.start : box.end + 1);
  if (prompt.empty) {
    let n = body.length;
    while (n > 0 && isBlank(body[n - 1])) n--;
    body = body.slice(0, n);
  }
  return { body: collapseBlanks(body), status: raw.length ? { lines: raw } : null, prompt };
}

/**
 * paneLinesForExact returns the lines of pane text the Exact view writes to
 * its terminal: Claude Code's footer below the prompt box (statusline, mode
 * line) is dropped, as are trailing blank lines, so the terminal can be sized
 * to its content with no blank band. ANSI escapes are kept. Without a prompt
 * box only trailing blank lines are dropped.
 */
export function paneLinesForExact(text) {
  const raw = text.replace(/\r/g, "").split("\n");
  const blocks = lineBlocks(parseAnsi(text));
  const box = findPromptBox(blocks);
  // parseAnsi splits on the same newlines, so block i is raw line i.
  const lines = box ? raw.slice(0, box.end + 1) : raw;
  const plain = (l) => l.replace(ESC_RE, "").replace(/[\s⠀]/g, "");
  let n = lines.length;
  while (n > 0 && plain(lines[n - 1]) === "") n--;
  return lines.slice(0, n);
}

/**
 * exactRows is how many terminal rows the Exact view shows: as many as fit
 * availHeight at cellHeight, but never more than the content (contentLines)
 * or the pane itself (paneRows), and at least one.
 */
export function exactRows(contentLines, paneRows, availHeight, cellHeight) {
  const cap = Math.max(1, Math.min(contentLines || 1, paneRows || contentLines || 1));
  if (!(availHeight > 0) || !(cellHeight > 0)) return cap;
  return Math.max(1, Math.min(cap, Math.floor(availHeight / cellHeight)));
}

/** TOOL_GLYPHS start Claude Code's tool and result lines (●, ⏺, ⎿, └, …). */
const TOOL_GLYPHS = "●⏺⎿└✻✶✳✢◼☐☒✔✓✗·*";
const HANG_RE = new RegExp(`^([ \\t]*)([${TOOL_GLYPHS}][ \\t]+)?`);

/**
 * hangingIndent is the indent, in columns, for wrapped continuations of a
 * reflowed line: its leading whitespace, plus a leading tool glyph and the
 * space after it, so a wrapped "⎿ ok …" continues under "ok". At least 2
 * (a wrapped line is always set off) and at most 16.
 */
export function hangingIndent(plain) {
  const m = HANG_RE.exec(plain);
  const n = m ? m[0].length : 0;
  return Math.max(2, Math.min(16, n));
}

/** paneModeOrDefault validates a persisted pane mode. */
export function paneModeOrDefault(v) {
  return v === "exact" ? "exact" : "reflow";
}

/** INTERRUPT_FOLLOWUP_MS: an interrupt counts as the cause of the reply box appearing within this window. */
export const INTERRUPT_FOLLOWUP_MS = 10000;

// --- Remote approvals ("away mode") -----------------------------------------

/** approvalHash is the app route for one approval (or the list when id is empty). */
export function approvalHash(id) {
  return id ? `#/approvals/${encodeURIComponent(id)}` : "#/approvals";
}

/** approvalApiPath is the answer endpoint of an approval. */
export function approvalApiPath(id) {
  return `/v1/approvals/${encodeURIComponent(id)}`;
}

/**
 * buildApprovalRequest builds the answer for an approval as it was shown: the
 * body carries the payload hash of exactly that version, so the server
 * refuses (409) anything that changed or was already answered.
 */
export function buildApprovalRequest(approval, decision) {
  if (decision !== "allow" && decision !== "deny") throw new Error(`invalid decision ${decision}`);
  if (!approval || !approval.id || !approval.payload_sha256) throw new Error("no approval to answer");
  return {
    method: "POST",
    path: approvalApiPath(approval.id),
    body: { decision, payload_sha256: approval.payload_sha256 },
  };
}

/**
 * withReceived stamps an approval with the local time it arrived, so the
 * countdown runs on the phone's clock (the laptop's may differ).
 */
export function withReceived(approval, nowMs) {
  return { ...approval, received_ms: nowMs };
}

/** upsertApproval adds or replaces an approval, keeping the list oldest first. */
export function upsertApproval(list, approval) {
  const rest = (list ?? []).filter((a) => a.id !== approval.id);
  rest.push(approval);
  return rest.sort((a, b) => (a.opened < b.opened ? -1 : a.opened > b.opened ? 1 : a.id < b.id ? -1 : 1));
}

/** removeApproval drops an approval by id. */
export function removeApproval(list, id) {
  return (list ?? []).filter((a) => a.id !== id);
}

/** approvalsForWindow lists the pending approvals a window is asking for. */
export function approvalsForWindow(list, session, windowId) {
  return (list ?? []).filter((a) => a.session === session && a.window_id === windowId);
}

/** secondsLeft is the approval's remaining time on the phone's clock. */
export function secondsLeft(approval, nowMs) {
  const base = Number(approval.seconds_remaining) || 0;
  const elapsed = approval.received_ms ? Math.floor((nowMs - approval.received_ms) / 1000) : 0;
  return Math.max(0, base - elapsed);
}

/** formatCountdown renders seconds as m:ss. */
export function formatCountdown(secs) {
  const s = Math.max(0, Math.floor(secs));
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

/** approvalSummary is the one-line description of what is being asked. */
export function approvalSummary(a) {
  const who = a.asking_agent || "An agent";
  if (a.kind === "dispatch") return `${who} wants to dispatch ${a.target_agent || "an agent"}`;
  if (a.kind === "cron") return `${who} wants to schedule ${a.target_agent || "an agent"}`;
  return `${who} wants to run a command`;
}

/** approvalBannerText is the fleet banner for n pending approvals ("" for none). */
export function approvalBannerText(n) {
  if (!n) return "";
  return n === 1 ? "1 approval waiting" : `${n} approvals waiting`;
}

/** approvalConfirmTitle is the allow sheet's title for an approval kind. */
function approvalConfirmTitle(kind) {
  if (kind === "dispatch") return "Approve this dispatch?";
  if (kind === "cron") return "Approve this schedule?";
  return "Approve this command?";
}

/** approvalConfirm is the confirm sheet for answering an approval: everything that runs, in full. */
export function approvalConfirm(a, decision) {
  const allow = decision === "allow";
  const head = [approvalSummary(a), `window: ${a.window_id}${a.project ? ` (${a.project})` : ""}`];
  if (a.kind === "dispatch") {
    head.push(`target agent: ${a.target_agent || "(default)"}`);
    if (a.issue) head.push(`issue: #${a.issue}`);
  } else if (a.kind === "cron") {
    head.push(`scheduled agent: ${a.target_agent || "(unknown)"}`);
  }
  const lines = [head.join("\n")];
  if (a.prompt) lines.push(a.prompt);
  lines.push(a.command);
  return {
    title: allow ? approvalConfirmTitle(a.kind) : "Deny?",
    lines,
    confirmLabel: allow ? "Approve" : "Deny",
    danger: !allow,
  };
}

/**
 * resolvedText describes a resolved approval ({outcome, source, resolved_at,
 * opened}) in one line, e.g. "Approved from device-00bb · 2m ago".
 */
export function resolvedText(r, nowMs) {
  const ago = formatSince(r.resolved_at, nowMs);
  const when = ago ? ` · ${ago} ago` : "";
  const opened = Date.parse(r.opened);
  const done = Date.parse(r.resolved_at);
  let head;
  switch (r.outcome) {
    case "approved":
    case "denied": {
      const verb = r.outcome === "approved" ? "Approved" : "Denied";
      if (r.source === "laptop") head = `Already answered on the laptop: ${r.outcome}`;
      else head = r.source ? `${verb} from ${r.source}` : verb;
      break;
    }
    case "answered":
      head = r.source === "laptop" ? "Already answered on the laptop" : "Already answered";
      break;
    case "timed_out": {
      const secs = Number.isFinite(opened) && Number.isFinite(done) ? Math.max(0, Math.round((done - opened) / 1000)) : 0;
      head = secs >= 60 ? `Timed out after ${Math.floor(secs / 60)}m` : secs > 0 ? `Timed out after ${secs}s` : "Timed out";
      break;
    }
    default:
      head = "No longer waiting";
  }
  return head + when;
}

/** HUB_SESSION is the tmux hub session; its windows are never closable (server rule). */
const HUB_SESSION = "agentctl-hub";

/** SWIPE_SLOP is the movement in px after which a gesture's direction is locked. */
export const SWIPE_SLOP = 10;

/**
 * canSwipeClose reports whether a row offers swipe-to-close: the write feature
 * is on, the window is idle or done, and it is not a hub session. The server
 * still re-verifies the pane and may answer 409.
 */
export function canSwipeClose(win, session, features) {
  if (!win || !hasFeature(features, "write")) return false;
  if (session === HUB_SESSION) return false;
  const s = effectiveState(win);
  return s === "idle" || s === "done";
}

/** swipeAxis locks a gesture to "x" or "y" once it moves past the slop; "" until then. */
export function swipeAxis(dx, dy, slop = SWIPE_SLOP) {
  const ax = Math.abs(dx);
  const ay = Math.abs(dy);
  if (Math.max(ax, ay) < slop) return "";
  return ax > ay ? "x" : "y";
}

/** swipeOffset maps a drag to a row offset: leftward only, never past the row width. */
export function swipeOffset(dx, width) {
  return Math.max(-width, Math.min(0, dx));
}

/**
 * swipeCommit decides a release: commit when dragged past 38% of the row width
 * or flung left faster than 0.6 px/ms over at least 40px. dx is negative leftward.
 */
export function swipeCommit(dx, width, velocity = 0) {
  if (width <= 0 || dx >= 0) return false;
  const dist = -dx;
  if (dist >= width * 0.38) return true;
  return velocity < -0.6 && dist >= 40;
}
