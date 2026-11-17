// Fleet screen: project groups ordered by attention.
import {
  approvalBannerText,
  approvalHash,
  collapseKey,
  dispatchHash,
  descendants,
  effectiveState,
  formatClock,
  formatSince,
  rollup,
  sortGroups,
  treeLines,
  treeRows,
  canSwipeClose,
  visibleTree,
  windowApiPath,
  windowHash,
  windowKey,
} from "./logic.js";
import { send, ApiError } from "./api.js";
import { attachSwipe } from "./swipe.js";
import { clear, confirmSheet, h, toast } from "./ui.js";

/** closeError is the message to toast for a failed close. */
function closeError(err) {
  return err instanceof ApiError || err instanceof Error ? err.message : "Close failed";
}

const GLYPH = { blocked: "⏸", working: "●", done: "✓", idle: "○" };

const STORE_KEY = "agentctl.fleet.collapsed";

function loadCollapsed() {
  try {
    const v = JSON.parse(localStorage.getItem(STORE_KEY) ?? "[]");
    return new Set(Array.isArray(v) ? v.filter((k) => typeof k === "string") : []);
  } catch {
    return new Set();
  }
}

function saveCollapsed(set) {
  try {
    localStorage.setItem(STORE_KEY, JSON.stringify([...set]));
  } catch {
    // Storage may be unavailable (private mode); collapse then lasts until reload.
  }
}

/** disclosure is the collapse toggle: child count, plus the most urgent child glyph when collapsed. */
function disclosure(kids, isCollapsed, onToggle) {
  const urgent = rollup(kids.filter((k) => k.win).map((k) => k.win));
  const attrs = {
    class: `disclose${isCollapsed ? " collapsed" : ""}${isCollapsed ? ` u-${urgent}` : ""}`,
    type: "button",
    "aria-expanded": String(!isCollapsed),
    "aria-label": `${isCollapsed ? "Expand" : "Collapse"} ${kids.filter((k) => k.win).length} nested`,
    onclick: onToggle,
  };
  return h(
    "button",
    attrs,
    h("span", { class: "chev", "aria-hidden": "true" }, isCollapsed ? "▸" : "▾"),
    h("span", {}, String(kids.filter((k) => k.win).length)),
    isCollapsed ? h("span", { class: "glyph", "aria-hidden": "true" }, GLYPH[urgent]) : null,
  );
}

/** treeSpans emits one aria-hidden span per connector level; CSS draws the lines. */
function stubSpan(entry) {
  return h("span", { class: "tl stub", style: `--lvl:${entry.depth}`, "aria-hidden": "true" });
}

function treeSpans(entry, expanded) {
  const stub = expanded ? [stubSpan(entry)] : [];
  if (entry.depth === 0) return stub;
  const { through, last } = treeLines(entry);
  const spans = through.map((cont, k) =>
    h("span", { class: `tl${cont ? " through" : ""}`, style: `--lvl:${k}`, "aria-hidden": "true" }),
  );
  spans.push(h("span", { class: `tl own${last ? " last" : ""}`, style: `--lvl:${through.length}`, "aria-hidden": "true" }));
  return [...spans, ...stub];
}

function windowRow(entry, toggle, expanded, swipe) {
  const win = entry.win;
  const state = effectiveState(win);
  const since = formatSince(win.blocked_since || win.state_since, Date.now());
  // Children omit the branch; it is in the detail view.
  const meta = [win.window_id, win.mode, entry.depth > 0 ? "" : win.branch].filter(Boolean).join(" · ");
  const link = h(
    "a",
    { class: `win ${state}${entry.depth > 0 ? " child" : ""}`, style: entry.depth > 0 ? `--depth:${entry.depth}` : null, href: windowHash(win.session, win.window_id) },
    ...treeSpans(entry, expanded),
    h("span", { class: "glyph", "aria-label": state }, GLYPH[state]),
    h(
      "span",
      { class: "win-main" },
      h("span", { class: "win-title" }, h("span", { class: "agent" }, win.agent || "(bare)"), h("span", { class: "meta" }, ` ${meta}`)),
      win.activity ? h("span", { class: "activity" }, win.activity) : null,
    ),
    h(
      "span",
      { class: "win-side" },
      since ? h("span", {}, since) : null,
      win.context_pct ? h("span", { class: "meta" }, `${Math.round(win.context_pct)}%`) : null,
    ),
  );
  const node = h("div", { class: "node" }, link, toggle);
  if (!swipe || !swipe.can(win)) return node;
  const wrap = h("div", { class: `swipe${swipe.gone(win) ? " swipe-gone" : ""}` }, h("span", { class: "swipe-action", "aria-hidden": "true" }, "Close"), node);
  attachSwipe(wrap, node, link, {
    onBusy: swipe.busy,
    onCommit: () => swipe.close(win),
  });
  return wrap;
}

function bucketRow(entry, toggle, expanded) {
  return h("div", { class: "node bucket" }, expanded ? stubSpan(entry) : null, h("span", { class: "bucket-label" }, entry.label), toggle);
}

export function mountFleet(root, ctx) {
  const banner = h("div", { class: "banner", hidden: true });
  const approvalBanner = h("a", { class: "banner approval-banner", hidden: true, href: approvalHash("") });
  const list = h("div", { class: "fleet" });
  root.append(
    h("header", { class: "bar" }, h("h1", {}, "Fleet"), ctx.hasFeature("write") ? h("a", { class: "btn", href: "#/dispatch" }, "Dispatch") : null),
    approvalBanner,
    banner,
    list,
  );

  function renderApprovalBanner() {
    const pending = ctx.store.approvals ?? [];
    const text = approvalBannerText(pending.length);
    approvalBanner.textContent = text;
    approvalBanner.hidden = !text;
    approvalBanner.setAttribute("href", approvalHash(pending.length === 1 ? pending[0].id : ""));
  }

  const collapsed = loadCollapsed();

  // Renders are deferred while a swipe or confirm sheet is live, because
  // render() rebuilds every row and would destroy the one being dragged.
  let busyCount = 0;
  let renderPending = false;
  const closed = new Map(); // windowKey -> ms; rows closed, awaiting the fleet event
  const swipe = {
    can: (win) => canSwipeClose(win, win.session, ctx.features),
    gone: (win) => closed.has(windowKey(win)),
    busy: (on) => {
      busyCount = Math.max(0, busyCount + (on ? 1 : -1));
      if (busyCount === 0 && renderPending) {
        renderPending = false;
        render();
      }
    },
    close: async (win) => {
      const ok = await confirmSheet({ title: "Close window?", lines: [`${win.agent ?? ""} ${win.window_id}`], confirmLabel: "Close", danger: true });
      if (!ok) return false;
      try {
        await send({ method: "POST", path: `${windowApiPath(win.session, win.window_id)}/close`, body: {} });
      } catch (err) {
        toast(closeError(err), "error");
        return false;
      }
      closed.set(windowKey(win), Date.now());
      return true;
    },
  };

  function groupNodes(g) {
    const entries = treeRows(g.windows);
    const live = new Set(entries.map((e) => collapseKey(g.session, e)));
    const flip = (key) => () => {
      if (collapsed.has(key)) collapsed.delete(key);
      else collapsed.add(key);
      saveCollapsed(collapsed);
      render();
    };
    const nodes = visibleTree(entries, g.session, collapsed).map((e) => {
      const i = entries.indexOf(e);
      const kids = descendants(entries, i);
      const key = collapseKey(g.session, e);
      const toggle = kids.length > 0 ? disclosure(kids, collapsed.has(key), flip(key)) : null;
      const expanded = kids.length > 0 && !collapsed.has(key);
      return e.bucket ? bucketRow(e, toggle, expanded) : windowRow(e, toggle, expanded, swipe);
    });
    return { nodes, live };
  }

  function render() {
    if (busyCount > 0) {
      renderPending = true;
      return;
    }
    renderApprovalBanner();
    const { fleet, downSince } = ctx.store;
    if (downSince && Date.now() - downSince > 15000) {
      banner.textContent = `laptop unreachable since ${formatClock(downSince)}`;
      banner.hidden = false;
    } else {
      banner.hidden = true;
    }
    clear(list);
    if (!fleet) {
      list.append(h("p", { class: "muted pad" }, "Loading…"));
      return;
    }
    const groups = sortGroups(fleet.groups ?? []);
    if (groups.length === 0) list.append(h("p", { class: "muted pad" }, "No agents running."));
    const live = new Set();
    for (const g of groups) {
      const { nodes, live: keys } = groupNodes(g);
      keys.forEach((k) => live.add(k));
      list.append(
        h(
          "section",
          { class: `group ${g.rollup}` },
          h(
            "div",
            { class: "group-head" },
            h("h2", {}, g.project || g.session),
            g.dir && ctx.hasFeature("write")
              ? h("a", { class: "group-add", href: dispatchHash(g.dir), "aria-label": `Dispatch to ${g.project || g.session}` }, "+")
              : null,
          ),
          ...nodes,
        ),
      );
    }
    const present = new Set(groups.flatMap((g) => g.windows.map(windowKey)));
    for (const [k, t] of closed) if (!present.has(k) || Date.now() - t > 15000) closed.delete(k);
    // Forget collapsed keys whose window or bucket is gone.
    const stale = [...collapsed].filter((k) => !live.has(k));
    if (stale.length > 0) {
      stale.forEach((k) => collapsed.delete(k));
      saveCollapsed(collapsed);
    }
  }

  render();
  const unsub = ctx.store.subscribe(render);
  // Refresh "time in state" and the unreachable banner without a server event.
  const tick = setInterval(render, 5000);
  return () => {
    unsub();
    clearInterval(tick);
  };
}
