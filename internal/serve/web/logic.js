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

/** parseRoute turns location.hash into {name, ...params}. */
export function parseRoute(hash) {
  const parts = (hash || "").replace(/^#\/?/, "").split("/").filter(Boolean);
  try {
    if (parts[0] === "w" && parts.length >= 3) {
      return { name: "window", session: decodeURIComponent(parts[1]), windowId: decodeURIComponent(parts[2]) };
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
