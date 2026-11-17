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
 * sortGroups orders project groups by their most urgent window, keeping the
 * server's order among equals, and sorts the windows inside each group.
 */
export function sortGroups(groups) {
  const decorated = groups.map((g, i) => {
    const windows = sortWindows(g.windows ?? []);
    return { group: { ...g, windows, rollup: rollup(windows) }, i };
  });
  decorated.sort((a, b) => stateRank(a.group.rollup) - stateRank(b.group.rollup) || a.i - b.i);
  return decorated.map((d) => d.group);
}

/** isDispatched mirrors herd.Meta.Dispatched: launched by another agent. */
export function isDispatched(win) {
  return Boolean(win.mode) && win.mode !== "bare";
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
 * internal/tui/herd_tree.go treeRows (shared cases: internal/tui/testdata/
 * tree_cases.json). A window nests under win.parent when that is another
 * window of the group (and not itself). Non-dispatched windows without such a
 * parent are roots. Dispatched windows with no usable parent, plus windows
 * stuck in a parent cycle, go under a trailing {label, bucket:true} entry.
 * Roots and children are in attention order; every window is emitted once.
 * Entries are {win, depth, last, ancestorsLast[]} or {label, bucket:true, depth:0}.
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

  rows.forEach((r, i) => {
    if (!isDispatched(r) && !hasParent[i]) walk(i, 0, [], true);
  });

  // Bucket: walk every top-level entry as non-last, then fix up the final one.
  const head = out.length;
  out.push({ bucket: true, depth: 0 });
  let lastTop = -1;
  for (let pass = 0; pass < 2; pass++) {
    rows.forEach((r, i) => {
      if (visited[i] || (pass === 0 && (hasParent[i] || !isDispatched(r)))) return;
      lastTop = out.length;
      walk(i, 1, [], false);
    });
  }
  if (lastTop < 0) return out.slice(0, head);
  out[lastTop].last = true;
  for (let j = lastTop + 1; j < out.length; j++) out[j].ancestorsLast[0] = true;
  out[head].label = `dispatched (${out.length - head - 1})`;
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
  return `${session}\u0000${entry.bucket ? "bucket" : entry.win.window_id}`;
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
  if (parts[0] === "dispatch") return { name: "dispatch" };
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

export function buildReplyRequest(session, windowId, text) {
  return { method: "POST", path: `${windowApiPath(session, windowId)}/reply`, body: { text } };
}

export function buildDispatchRequest(agent, projectDir, prompt) {
  return { method: "POST", path: "/v1/dispatch", body: { agent, project_dir: projectDir, prompt } };
}

/** actionsFor decides which action-bar sections a window shows. */
export function actionsFor(win, pane) {
  if (pane && pane.verdict === "awaiting_input" && pane.dialog) return "answer";
  const s = effectiveState(win);
  if (s === "working") return "interrupt";
  if (s === "idle" || s === "done") return "reply";
  return "none";
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
