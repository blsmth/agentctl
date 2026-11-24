// Fleet screen: project groups ordered by attention.
import {
  approvalBannerText,
  applyGroupOrder,
  approvalHash,
  bucketLabel,
  buildAnswerRequest,
  isTrustBlocked,
  PANE_TAIL_LINES,
  TRUST_KEY_TRUST,
  trustDialogOf,
  trustFolder,
  buildGroupOrderRequest,
  buildShellRequest,
  canShell,
  terminalHash,
  collapseKey,
  dispatchHash,
  descendants,
  displayState,
  waitingLabel,
  formatClock,
  formatSince,
  groupCounts,
  groupKey,
  groupKeys,
  loadCollapsedSet,
  moveKey,
  rollup,
  rowDetailLine,
  rowHeadline,
  rowChips,
  rowRole,
  rowTitle,
  sameOrder,
  shellApiPath,
  shellRows,
  sortGroups,
  treeLines,
  treeRows,
  canSwipeClose,
  canSweep,
  runSweep,
  saveCollapsedSet,
  visibleTree,
  windowApiPath,
  windowHash,
  windowKey,
} from "./logic.js";
import { get, send, ApiError } from "./api.js";
import { attachDrag } from "./drag.js";
import { attachSwipe } from "./swipe.js";
import { trustCard } from "./trust.js";
import { clear, confirmSheet, h, newSheet, toast } from "./ui.js";

/** closeError is the message to toast for a failed close. */
function closeError(err) {
  return err instanceof ApiError || err instanceof Error ? err.message : "Close failed";
}

const GLYPH = { blocked: "⏸", working: "●", waiting: "◷", done: "✓", idle: "○" };

const STORE_KEY = "agentctl.fleet.collapsed";

const loadCollapsed = () => loadCollapsedSet(STORE_KEY);
const saveCollapsed = (set) => saveCollapsedSet(STORE_KEY, set);

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

function windowRow(entry, toggle, expanded, swipe, trust) {
  const win = entry.win;
  const state = displayState(win);
  const since = formatSince(win.blocked_since || win.state_since, Date.now());
  // Window ID, launch mode and branch are internal; the detail view has them.
  const chips = rowChips(win);
  const role = rowRole(win);
  const detail = rowDetailLine(win);
  const link = h(
    "a",
    { class: `win ${state}${entry.depth > 0 ? " child" : ""}`, style: entry.depth > 0 ? `--depth:${entry.depth}` : null, href: windowHash(win.session, win.window_id) },
    ...treeSpans(entry, expanded),
    h("span", { class: "glyph", "aria-label": state }, GLYPH[state]),
    h(
      "span",
      { class: "win-main" },
      h("span", { class: "win-title" }, role ? h("span", { class: "tag kind-chip", title: "Role of this sub-agent" }, role) : null, h("span", { class: "agent" }, rowHeadline(win))),
      detail ? h("span", { class: detail === win.recap ? "activity recap" : "activity" }, detail) : null,
      chips.length
        ? h(
            "span",
            { class: "win-chips" },
            ...chips.map((c) => h("span", { class: `tag ${c.kind}`, title: c.kind === "sched-chip" ? "Started by agentctl cron" : null }, c.text)),
          )
        : null,
    ),
    h(
      "span",
      { class: "win-side" },
      state === "waiting" ? h("span", { class: "waiting-on" }, waitingLabel(win.waiting_on.length)) : null,
      since ? h("span", {}, since) : null,
      win.context_pct ? h("span", { class: "meta" }, `${Math.round(win.context_pct)}% ctx`) : null,
    ),
  );
  const node = h("div", { class: "node" }, link, toggle);
  const card = trust && isTrustBlocked(win) ? trustCard(trustFolder(win), (key) => trust(win, key), "row-trust") : null;
  const row = swipeRow(win, node, link, swipe);
  return card ? h("div", { class: "trust-row" }, row, card) : row;
}

/** swipeRow wraps a row node in its swipe-to-close shell when the row can be closed. */
function swipeRow(win, node, link, swipe) {
  if (!swipe || !swipe.can(win)) return node;
  const action = h("button", { class: "swipe-action", type: "button", "aria-label": `Close ${rowTitle(win)}` }, "Close");
  const wrap = h("div", { class: `swipe${swipe.gone(win) ? " swipe-gone" : ""}` }, action, node);
  attachSwipe(wrap, node, link, action, {
    onBusy: swipe.busy,
    onClose: () => swipe.close(win),
  });
  return wrap;
}

/** shellRow is a compact, muted "$ shell" row: no state, never counted. Tapping opens its terminal; swiping closes it at once, with no confirm. */
function shellRow(sh, swipe) {
  const link = h("a", { class: "win shell-row", href: terminalHash(sh.session, sh.window_id) }, h("span", { class: "shell-label" }, sh.label));
  const node = h("div", { class: "node" }, link);
  if (!swipe) return node;
  const action = h("button", { class: "swipe-action", type: "button", "aria-label": `Close ${sh.label}` }, "Close");
  const wrap = h("div", { class: `swipe${swipe.gone(sh) ? " swipe-gone" : ""}` }, action, node);
  attachSwipe(wrap, node, link, action, { onBusy: swipe.busy, onClose: () => swipe.closeShell(sh) });
  return wrap;
}

/** groupSummary is a folded project's per-state counts, as on the hub's group header. */
function groupSummary(windows, onToggle) {
  const counts = groupCounts(windows);
  const label = counts.map((c) => `${c.n} ${c.state}`).join(", ");
  return h(
    "button",
    { class: "group-counts", type: "button", "aria-label": `Expand: ${label}`, onclick: onToggle },
    ...counts.map((c) => h("span", { class: `count ${c.state}` }, h("span", { class: "glyph", "aria-hidden": "true" }, GLYPH[c.state]), String(c.n))),
  );
}

function bucketRow(entry, toggle, expanded) {
  return h("div", { class: "node bucket" }, expanded ? stubSpan(entry) : null, h("span", { class: "bucket-label" }, bucketLabel(entry.count ?? 0, entry.kind)), entry.kind === "scheduled" ? h("a", { class: "bucket-link", href: "#/schedules" }, "Schedules ›") : null, toggle);
}

export function mountFleet(root, ctx) {
  const banner = h("div", { class: "banner", hidden: true });
  const approvalBanner = h("a", { class: "banner approval-banner", hidden: true, href: approvalHash("") });
  const list = h("div", { class: "fleet" });
  root.append(
    h("header", { class: "bar" }, h("h1", {}, "Fleet"), ctx.hasFeature("write") ? h("a", { class: "btn", href: "#/dispatch" }, "Dispatch") : null, h("a", { class: "btn icon", href: "#/settings", "aria-label": "Settings", title: "Settings" }, "⚙︎")),
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
  let renders = 0; // exposed as data-renders on the list so tests can await a render
  // The order just dropped, shown until the server's fleet reports it (or it
  // is refused): { keys, at }.
  let pending = null;
  const closed = new Map(); // windowKey -> ms; rows closed, awaiting the fleet event
  const swipe = {
    can: (win) => canSwipeClose(win, win.session, ctx.features),
    closeShell: async (sh) => {
      try {
        await send({ method: "POST", path: shellApiPath(sh.session, sh.window_id), body: {} });
      } catch (err) {
        toast(closeError(err), "error");
        return false;
      }
      closed.set(windowKey(sh), Date.now());
      return true;
    },
    gone: (win) => closed.has(windowKey(win)),
    busy: (on) => {
      busyCount = Math.max(0, busyCount + (on ? 1 : -1));
      if (busyCount === 0 && renderPending) {
        renderPending = false;
        render();
      }
    },
    close: async (win) => {
      let res;
      try {
        res = await send({ method: "POST", path: `${windowApiPath(win.session, win.window_id)}/close`, body: {} });
      } catch (err) {
        toast(closeError(err), "error");
        return false;
      }
      const now = Date.now();
      closed.set(windowKey(win), now);
      // Sub-sessions the server closed first go away with their parent.
      for (const k of res?.closed_children ?? []) closed.set(windowKey({ session: k.session, window_id: k.window_id }), now);
      return true;
    },
  };

  /**
   * trust answers a trust-blocked row's folder-trust dialog from the list:
   * it reads the pane for the dialog (and its hash) first, so the server can
   * verify the answer is for the screen the window shows now. Renders wait
   * while it runs. Null without write access: the row shows no card.
   */
  const trust = ctx.hasFeature("write")
    ? async (win, key) => {
        swipe.busy(true);
        try {
          const pane = await get(`${windowApiPath(win.session, win.window_id)}/pane?lines=${PANE_TAIL_LINES}`);
          const dialog = trustDialogOf(pane, trustFolder(win));
          await send(buildAnswerRequest(win.session, win.window_id, dialog, key));
          toast(key === TRUST_KEY_TRUST ? "Folder trusted" : "Session exited");
        } catch (err) {
          toast(closeError(err), "error");
        } finally {
          swipe.busy(false);
        }
      }
    : null;

  // Sessions with a sweep preview or apply in flight: their button is
  // disabled, so a double tap never starts a second sweep.
  const sweeping = new Set();

  /** sweep previews a sweep of group g, confirms it in a sheet and applies it. */
  async function sweep(g) {
    if (sweeping.has(g.session)) return;
    sweeping.add(g.session);
    render();
    try {
      await runSweep(g, {
        send,
        confirm: (sheet) => confirmSheet({ ...sheet, danger: true }),
        toast,
      });
    } finally {
      sweeping.delete(g.session);
      render();
    }
  }

  /** sweepButton is a group header's sweep action. */
  function sweepButton(g, name) {
    const busy = sweeping.has(g.session);
    return h(
      "button",
      {
        class: `group-sweep${busy ? " busy" : ""}`,
        type: "button",
        disabled: busy,
        "aria-busy": busy ? "true" : null,
        "aria-label": busy ? `Sweeping ${name}…` : `Sweep ${name}`,
        title: busy ? "Sweeping…" : "Sweep finished windows",
        onclick: (e) => {
          e.stopPropagation();
          sweep(g);
        },
      },
      busy ? "…" : "🧹",
    );
  }

  /** displayedKeys are the keyed groups as listed now (the pending order applies). */
  function displayedKeys() {
    const served = sortGroups(ctx.store.fleet?.groups ?? []);
    return groupKeys(pending ? applyGroupOrder(served, pending.keys) : served);
  }

  /**
   * reorder moves the group with key to slot (an index among the other
   * groups), shows the result at once and saves it. A refused save puts the
   * previous order back.
   */
  async function reorder(key, slot) {
    const before = displayedKeys();
    const keys = moveKey(before, key, slot);
    if (keys.every((k, i) => k === before[i])) return;
    const prev = pending;
    pending = { keys, at: Date.now() };
    render();
    try {
      await send(buildGroupOrderRequest(keys));
    } catch (err) {
      pending = prev;
      toast(err instanceof ApiError || err instanceof Error ? err.message : "Could not save the order", "error");
      render();
    }
  }

  /** keyMove is the keyboard path: Alt+ArrowUp / Alt+ArrowDown on a group header. */
  function keyMove(e, key) {
    if (!e.altKey || (e.key !== "ArrowUp" && e.key !== "ArrowDown")) return;
    e.preventDefault();
    const keys = displayedKeys();
    const i = keys.indexOf(key);
    const to = e.key === "ArrowUp" ? i - 1 : i + 1;
    if (i < 0 || to < 0 || to >= keys.length) return;
    reorder(key, to);
  }

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
      return e.bucket ? bucketRow(e, toggle, expanded) : windowRow(e, toggle, expanded, swipe, trust);
    });
    for (const sh of shellRows(g, ctx.features)) nodes.push(shellRow(sh, ctx.hasFeature("write") ? swipe : null));
    return { nodes, live };
  }

  function render() {
    if (busyCount > 0) {
      renderPending = true;
      return;
    }
    list.dataset.renders = String(++renders);
    renderApprovalBanner();
    const { fleet, downSince } = ctx.store;
    if (downSince && Date.now() - downSince > 15000) {
      banner.textContent = `laptop unreachable since ${formatClock(downSince)}`;
      banner.hidden = false;
    } else {
      banner.hidden = true;
    }
    const focusKey = list.contains(document.activeElement) ? (document.activeElement.dataset?.key ?? "") : "";
    clear(list);
    if (!fleet) {
      list.append(h("p", { class: "muted pad" }, "Loading…"));
      return;
    }
    const served = sortGroups(fleet.groups ?? []);
    if (pending && (sameOrder(groupKeys(served), pending.keys) || Date.now() - pending.at > 10000)) pending = null;
    const groups = pending ? applyGroupOrder(served, pending.keys) : served;
    if (groups.length === 0) list.append(h("p", { class: "muted pad" }, "No agents running."));
    const live = new Set();
    for (const g of groups) {
      // Window keys stay live while the group is folded, so nested folds survive.
      const { nodes, live: keys } = groupNodes(g);
      keys.forEach((k) => live.add(k));
      const gk = groupKey(g.session);
      live.add(gk);
      const folded = collapsed.has(gk);
      const name = g.project || g.session;
      const toggle = () => {
        if (folded) collapsed.delete(gk);
        else collapsed.add(gk);
        saveCollapsed(collapsed);
        render();
      };
      const head = h(
        "div",
        { class: "group-head" },
        // Heading text stays the bare name; the chevron is drawn by CSS.
        h(
          "h2",
          {},
          h("button", { class: "group-toggle", type: "button", "aria-expanded": String(!folded), "data-key": g.key || null, onclick: toggle, onkeydown: g.key && ctx.hasFeature("write") ? (e) => keyMove(e, g.key) : null }, name),
        ),
        folded ? groupSummary(g.windows, toggle) : null,
        canSweep(g, ctx.features) ? sweepButton(g, name) : null,
        g.dir && ctx.hasFeature("write")
          ? addButton(g, name)
          : null,
      );
      const section = h("section", { class: `group ${g.rollup}${folded ? " folded" : ""}` }, head, ...(folded ? [] : nodes));
      // Only keyed project groups reorder, and only with the write feature
      // (the route is a write); a lone group has nowhere to go.
      if (g.key && groups.length > 1 && ctx.hasFeature("write")) {
        attachDrag(section, head, {
          list,
          siblings: () => [...list.querySelectorAll(":scope > section.group")].filter((el) => el !== section),
          ignore: ".group-add, .group-sweep",
          onBusy: swipe.busy,
          onDrop: (slot) => reorder(g.key, slot),
        });
      }
      list.append(section);
    }
    // Rendering rebuilds the list; keep keyboard focus on the same header.
    if (focusKey) [...list.querySelectorAll(".group-toggle")].find((b) => b.dataset.key === focusKey)?.focus();
    const present = new Set(groups.flatMap((g) => [...g.windows.map(windowKey), ...(g.shells ?? []).map((s) => windowKey({ session: g.session, window_id: s.window_id }))]));
    for (const [k, t] of closed) if (!present.has(k) || Date.now() - t > 15000) closed.delete(k);
    // Forget collapsed keys whose window or bucket is gone.
    const stale = [...collapsed].filter((k) => !live.has(k));
    if (stale.length > 0) {
      stale.forEach((k) => collapsed.delete(k));
      saveCollapsed(collapsed);
    }
  }

  // addButton is the group's "+": a plain Dispatch link, or a Shell | Agent
  // sheet when the server can open shells.
  function addButton(g, name) {
    if (!canShell(ctx.features)) {
      return h("a", { class: "group-add", href: dispatchHash(g.dir), "aria-label": `Dispatch to ${name}` }, "+");
    }
    return h(
      "button",
      {
        class: "group-add",
        type: "button",
        "aria-label": `New in ${name}`,
        onclick: () =>
          newSheet({
            title: `New in ${name}`,
            agentHref: dispatchHash(g.dir),
            onShell: async () => {
              const res = await send(buildShellRequest(g.dir));
              location.hash = terminalHash(res.session, res.window_id);
            },
          }),
      },
      "+",
    );
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
