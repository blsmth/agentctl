// Fleet screen: project groups ordered by attention.
import {
  approvalBannerText,
  applyGroupOrder,
  approvalHash,
  autoscrollDelta,
  bucketLabel,
  buildGroupOrderRequest,
  buildShellRequest,
  canShell,
  terminalHash,
  collapseKey,
  dispatchHash,
  descendants,
  dropIndex,
  effectiveState,
  formatClock,
  formatSince,
  groupCounts,
  groupKey,
  groupKeys,
  LONG_PRESS_MS,
  LONG_PRESS_SLOP,
  moveKey,
  rollup,
  agentChip,
  rowDetailLine,
  rowHeadline,
  rowChips,
  rowTitle,
  scheduledChip,
  sameOrder,
  sortGroups,
  treeLines,
  treeRows,
  canSwipeClose,
  canSweep,
  runSweep,
  visibleTree,
  windowApiPath,
  windowHash,
  windowKey,
} from "./logic.js";
import { send, ApiError } from "./api.js";
import { attachSwipe } from "./swipe.js";
import { clear, confirmSheet, h, newSheet, toast } from "./ui.js";

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
  // Window ID, launch mode and branch are internal; the detail view has them.
  const chips = rowChips(win);
  const detail = rowDetailLine(win);
  const link = h(
    "a",
    { class: `win ${state}${entry.depth > 0 ? " child" : ""}`, style: entry.depth > 0 ? `--depth:${entry.depth}` : null, href: windowHash(win.session, win.window_id) },
    ...treeSpans(entry, expanded),
    h("span", { class: "glyph", "aria-label": state }, GLYPH[state]),
    h(
      "span",
      { class: "win-main" },
      h("span", { class: "win-title" }, h("span", { class: "agent" }, rowHeadline(win))),
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
      since ? h("span", {}, since) : null,
      win.context_pct ? h("span", { class: "meta" }, `${Math.round(win.context_pct)}% ctx`) : null,
    ),
  );
  const node = h("div", { class: "node" }, link, toggle);
  if (!swipe || !swipe.can(win)) return node;
  const action = h("button", { class: "swipe-action", type: "button", "aria-label": `Close ${rowTitle(win)}` }, "Close");
  const wrap = h("div", { class: `swipe${swipe.gone(win) ? " swipe-gone" : ""}` }, action, node);
  attachSwipe(wrap, node, link, action, {
    onBusy: swipe.busy,
    onClose: () => swipe.close(win),
  });
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
  return h("div", { class: "node bucket" }, expanded ? stubSpan(entry) : null, h("span", { class: "bucket-label" }, bucketLabel(entry.count ?? 0, entry.kind)), toggle);
}

/**
 * attachGroupDrag lets a project group be reordered by holding its header
 * (long press, or a held mouse button) and dragging. A short tap is left to
 * the header's own click (fold); a touch that starts scrolling before the
 * hold completes is cancelled by the browser (pointercancel) or by the slop
 * check, so the page scrolls as usual. Pointer Events only; while a drag is
 * live the page is kept from scrolling under the finger and autoscrolls near
 * the top and bottom edges.
 *
 * onBusy(true|false) brackets the whole gesture so renders are held, and
 * onDrop(index) is called with the target slot among the other groups' order
 * (index into the list with this group removed).
 */
function attachGroupDrag(section, head, { list, onBusy, onDrop }) {
  let press = null; // { id, x, y, timer, buttons }
  let drag = null; // { id, grab, y, slot, raf }

  const others = () => [...list.querySelectorAll(":scope > section.group")].filter((el) => el !== section);

  const place = () => {
    // Follow the pointer: measure the header's natural spot, then offset it.
    head.style.transform = "";
    const natural = head.getBoundingClientRect().top;
    head.style.transform = `translateY(${drag.y - drag.grab - natural}px)`;
    const rest = others();
    const mids = rest.map((el) => {
      const r = el.getBoundingClientRect();
      return r.top + Math.min(r.height, 56) / 2; // collapsed while dragging: ~ the header
    });
    drag.slot = dropIndex(mids, drag.y);
    rest.forEach((el, i) => {
      el.classList.toggle("drop-before", i === drag.slot);
      el.classList.toggle("drop-after", i === rest.length - 1 && drag.slot === rest.length);
    });
  };

  const frame = () => {
    if (!drag) return;
    const dy = autoscrollDelta(drag.y, window.innerHeight);
    if (dy !== 0) {
      window.scrollBy(0, dy);
      place();
    }
    drag.raf = requestAnimationFrame(frame);
  };

  const blockScroll = (e) => e.preventDefault();

  // The press holds renders from pointerdown, so the list is not rebuilt
  // under a finger that is still deciding between tap, scroll and drag. A
  // tap's release is delayed past its click, which a render would swallow.
  // A mouse has no implicit pointer capture, so a press released off the
  // header never reaches it. While a press is pending, watch the window too.
  const onWinMove = (e) => {
    if (press && e.pointerId === press.id) press.buttons = e.buttons;
  };
  const onWinEnd = (e) => {
    if (press && e.pointerId === press.id) clearPress();
  };
  const unwatchPress = () => {
    window.removeEventListener("pointermove", onWinMove);
    window.removeEventListener("pointerup", onWinEnd);
    window.removeEventListener("pointercancel", onWinEnd);
  };

  const clearPress = (delayMs = 0) => {
    if (!press) return;
    clearTimeout(press.timer);
    press = null;
    unwatchPress();
    if (delayMs > 0) setTimeout(() => onBusy(false), delayMs);
    else onBusy(false);
  };

  const start = () => {
    const p = press;
    press = null;
    unwatchPress();
    // The press already holds renders (busy). Still, never drag a header
    // that has left the page, or one whose pointer was already released.
    if (!p || !head.isConnected || p.buttons === 0) {
      onBusy(false);
      return;
    }
    drag = { id: p.id, grab: p.y - head.getBoundingClientRect().top, y: p.y, slot: 0, raf: 0 };
    try {
      head.setPointerCapture?.(p.id);
    } catch {
      // No active pointer to capture (it lifted already): drop the gesture.
      drag = null;
      onBusy(false);
      return;
    }
    // touch-action only governs a gesture before it starts scrolling, so also
    // veto touchmove (iOS) while the drag is live.
    head.classList.add("dragging");
    window.addEventListener("touchmove", blockScroll, { passive: false });
    list.classList.add("reordering");
    section.classList.add("lifted");
    navigator.vibrate?.(12);
    place();
    drag.raf = requestAnimationFrame(frame);
  };

  const finish = (drop) => {
    if (!drag) return;
    const d = drag;
    drag = null;
    cancelAnimationFrame(d.raf);
    window.removeEventListener("touchmove", blockScroll);
    head.releasePointerCapture?.(d.id);
    head.style.transform = "";
    head.classList.remove("dragging");
    list.classList.remove("reordering");
    section.classList.remove("lifted");
    others().forEach((el) => el.classList.remove("drop-before", "drop-after"));
    // The click that follows the release must not fold the group.
    const stop = (e) => {
      e.preventDefault();
      e.stopPropagation();
    };
    head.addEventListener("click", stop, { capture: true, once: true });
    setTimeout(() => head.removeEventListener("click", stop, { capture: true }), 400);
    if (drop) onDrop(d.slot);
    onBusy(false);
  };

  head.addEventListener("pointerdown", (e) => {
    if (press || drag || (e.pointerType === "mouse" && e.button !== 0) || e.target.closest(".group-add, .group-sweep")) return;
    onBusy(true);
    press = { id: e.pointerId, x: e.clientX, y: e.clientY, timer: setTimeout(start, LONG_PRESS_MS), buttons: e.buttons };
    window.addEventListener("pointermove", onWinMove);
    window.addEventListener("pointerup", onWinEnd);
    window.addEventListener("pointercancel", onWinEnd);
  });
  head.addEventListener("pointermove", (e) => {
    if (press && e.pointerId === press.id && Math.hypot(e.clientX - press.x, e.clientY - press.y) > LONG_PRESS_SLOP) clearPress();
    if (!drag || e.pointerId !== drag.id) return;
    e.preventDefault();
    drag.y = e.clientY;
    place();
  });
  head.addEventListener("pointerup", (e) => {
    if (press && e.pointerId === press.id) clearPress(150);
    if (drag && e.pointerId === drag.id) finish(true);
  });
  head.addEventListener("pointercancel", (e) => {
    if (press && e.pointerId === press.id) clearPress();
    if (drag && e.pointerId === drag.id) finish(false);
  });
  // A long press on Android opens a context menu; it would end the drag.
  head.addEventListener("contextmenu", (e) => {
    if (press || drag) e.preventDefault();
  });
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
  let renders = 0; // exposed as data-renders on the list so tests can await a render
  // The order just dropped, shown until the server's fleet reports it (or it
  // is refused): { keys, at }.
  let pending = null;
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
      return e.bucket ? bucketRow(e, toggle, expanded) : windowRow(e, toggle, expanded, swipe);
    });
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
        attachGroupDrag(section, head, { list, onBusy: swipe.busy, onDrop: (slot) => reorder(g.key, slot) });
      }
      list.append(section);
    }
    // Rendering rebuilds the list; keep keyboard focus on the same header.
    if (focusKey) [...list.querySelectorAll(".group-toggle")].find((b) => b.dataset.key === focusKey)?.focus();
    const present = new Set(groups.flatMap((g) => g.windows.map(windowKey)));
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
