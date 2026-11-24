// Artifacts tab: the list of agent-published artifacts and the viewer. The
// content is untrusted: it only ever loads inside a sandboxed iframe without
// allow-same-origin (an opaque origin, no cookie, no reach into /v1), from the
// raw_url the server gives: the direct /a/<id>/<n>/ for an artifact whose id
// is itself the capability, or a token URL minted per device (valid ~24h) for
// a legacy 10-character id.
import { ApiError, api, get, send } from "./api.js";
import { attachDrag } from "./drag.js";
import { attachSwipe } from "./swipe.js";
import {
  ARTIFACT_COLLAPSE_KEY,
  ARTIFACT_FEEDBACK_MAX,
  UNDO_MS,
  applyGroupOrder,
  artifactGroupMove,
  artifactRevBadge,
  artifactSubline,
  buildArtifactOrderRequest,
  buildGroupOrderRequest,
  createDeleteQueue,
  loadCollapsedSet,
  mergeArtifactOrder,
  moveKey,
  sameOrder,
  saveCollapsedSet,
  artifactCommentStatus,
  artifactCommentToast,
  artifactFeedbackFrom,
  artifactHash,
  artifactIcon,
  artifactOrigin,
  artifactRevLabel,
  artifactRevPill,
  artifactTokenStale,
  artifactWindowLive,
  artifactsForWindow,
  artifactsHash,
  groupArtifacts,
  formatAgo,
  windowHash,
} from "./logic.js";
import { actionToast, clear, confirmSheet, h, toast } from "./ui.js";

const POLL_MS = 30000;
// Burst of store events (an add is several fs events): one refetch per burst.
const REFRESH_DEBOUNCE_MS = 300;
const SANDBOX = "allow-scripts allow-forms allow-popups allow-modals allow-downloads";
const enc = encodeURIComponent;

export function mountArtifacts(root, ctx, route) {
  if (route.view === "viewer") return mountViewer(root, ctx, route);
  return mountList(root, ctx, route.filter);
}

const errText = (e) => (e instanceof Error ? e.message : String(e));

/** debounced returns fn wrapped so bursts collapse into one trailing call; .cancel() drops a pending one. */
function debounced(fn, ms) {
  let t = null;
  const wrapped = () => {
    clearTimeout(t);
    t = setTimeout(fn, ms);
  };
  wrapped.cancel = () => clearTimeout(t);
  return wrapped;
}

// ---- list ----

/** collapseKeyOf is the persisted fold key of a group; "Other" (no project) has an empty key. */
const collapseKeyOf = (g) => (g.key === "" ? "~other" : g.key);

/** clip shortens s to n characters for a toast. */
const clip = (s, n = 32) => (s.length > n ? `${s.slice(0, n - 1)}…` : s);

const reducedMotion = () => globalThis.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false;

/** animateFold hides or shows a group's rows, sliding the height unless motion is reduced. */
function animateFold(nodes, fold) {
  clearTimeout(nodes._fold);
  const reset = () => {
    nodes.style.height = nodes.style.overflow = nodes.style.transition = "";
    nodes.hidden = fold;
  };
  if (reducedMotion() || !nodes.isConnected) {
    reset();
    return;
  }
  nodes.style.overflow = "hidden";
  nodes.style.transition = "height .2s ease";
  if (fold) {
    nodes.style.height = `${nodes.offsetHeight}px`;
    requestAnimationFrame(() => {
      nodes.style.height = "0px";
    });
  } else {
    nodes.hidden = false;
    const full = nodes.scrollHeight;
    nodes.style.height = "0px";
    requestAnimationFrame(() => {
      nodes.style.height = `${full}px`;
    });
  }
  nodes._fold = setTimeout(reset, 220);
}

const deleteArtifact = async (id) => {
  try {
    await api("DELETE", `/v1/artifacts/${enc(id)}`, {});
  } catch (e) {
    // Already gone (deleted elsewhere): that is what was asked for.
    if (!(e instanceof ApiError && e.status === 404)) throw e;
  }
};

function emptyState(filter) {
  return h(
    "div",
    { class: "empty artifact-empty" },
    h("div", { class: "artifact-empty-art", "aria-hidden": "true" }, h("span", { class: "artifact-tile kind-html" }, artifactIcon("html")), h("span", { class: "artifact-tile kind-markdown" }, artifactIcon("markdown")), h("span", { class: "artifact-tile kind-image" }, artifactIcon("image"))),
    h("p", { class: "artifact-empty-title" }, filter ? "No artifacts from this window." : "Nothing published yet"),
    filter
      ? null
      : h("p", { class: "muted" }, "Artifacts are the reports, tables, charts and mockups your agents write for you. They show up here, and you get a push when one arrives."),
    filter ? null : h("p", { class: "muted" }, "An agent publishes one with:"),
    filter ? null : h("pre", { class: "sheet-body" }, "agentctl artifact add report.html --title 'Weekly report'"),
  );
}

function mountList(root, ctx, filter) {
  const write = ctx.hasFeature("write");
  const status = h("p", { class: "muted pad", hidden: true });
  const list = h("div", { class: "artifact-list", "aria-live": "polite" });
  const filterBar = h("div", { class: "artifact-filter", hidden: !filter });
  root.append(
    h("header", { class: "bar" }, h("h1", {}, "Artifacts")),
    filterBar,
    status,
    list,
  );
  let disposed = false;
  let data = null;
  let shownGroups = []; // the groups as last rendered, for the drag handlers

  const collapsed = loadCollapsedSet(ARTIFACT_COLLAPSE_KEY);

  // Renders are deferred while a swipe, drag or confirm sheet is live, because
  // render() rebuilds every row and would destroy the one being dragged.
  let busyCount = 0;
  let renderPending = false;
  let renders = 0; // exposed as data-renders on the list so tests can await a render
  const busy = (on) => {
    busyCount = Math.max(0, busyCount + (on ? 1 : -1));
    if (busyCount === 0 && renderPending) {
      renderPending = false;
      render();
    }
  };

  const seen = new Set(); // ids already drawn: only new rows animate in
  const fresh = new Set(); // ids that arrived while the page was open: highlighted once
  // Orders just dropped, shown until a fetch after the save confirms them:
  // project key -> { ids, at, confirmedAt }.
  const pendingOrder = new Map();
  let pendingGroups = null; // { keys, at }: the group order just dropped
  const removing = new Set(); // ids of confirmed (pinned) deletes in flight
  const undoToasts = new Map(); // id -> dismiss

  const queue = createDeleteQueue({
    commit: (a) => deleteArtifact(a.id),
    onSettle: (id, a, err) => {
      undoToasts.get(id)?.();
      undoToasts.delete(id);
      if (err) toast(`Could not delete ${clip(a.title)}: ${errText(err)}`, "error");
      else if (data) data = data.filter((x) => x.id !== id);
      if (!disposed) render();
    },
  });

  /** removeRow is a swipe's Delete: undoable for a few seconds, or confirmed first when pinned. */
  async function removeRow(a) {
    if (a.pinned) {
      const ok = await confirmSheet({
        title: `Delete ${a.title}?`,
        lines: ["This artifact is pinned. Deleting removes it and all its revisions from this machine."],
        confirmLabel: "Delete",
        danger: true,
      });
      if (!ok || disposed) return false;
      removing.add(a.id);
      try {
        await deleteArtifact(a.id);
        if (data) data = data.filter((x) => x.id !== a.id);
        return true;
      } catch (e) {
        toast(`Could not delete ${clip(a.title)}: ${errText(e)}`, "error");
        return false;
      } finally {
        removing.delete(a.id);
      }
    }
    queue.add(a.id, a);
    undoToasts.set(
      a.id,
      actionToast(`Deleted ${clip(a.title)}`, "Undo", () => {
        undoToasts.delete(a.id);
        if (queue.undo(a.id) && !disposed) render();
      }, UNDO_MS),
    );
    render();
    return true;
  }

  const hidden = (a) => queue.has(a.id) || removing.has(a.id);

  /** reorderItems moves artifact id within group g to slot, shows it at once and saves it. */
  async function reorderItems(g, id, slot) {
    const before = g.items.map((a) => a.id);
    const ids = moveKey(before, id, slot);
    if (ids.every((k, i) => k === before[i])) return;
    const prev = pendingOrder.get(g.key);
    const mine = { ids, at: Date.now(), confirmedAt: 0 };
    pendingOrder.set(g.key, mine);
    render();
    try {
      await send(buildArtifactOrderRequest(g.key, ids));
      mine.confirmedAt = Date.now();
      refresh();
    } catch (err) {
      if (pendingOrder.get(g.key) === mine) {
        if (prev) pendingOrder.set(g.key, prev);
        else pendingOrder.delete(g.key);
      }
      toast(err instanceof Error ? err.message : "Could not save the order", "error");
      if (!disposed) render();
    }
  }

  /** moveGroup moves a project group to slot (among the other groups) and saves the shared project order. */
  async function moveGroup(key, slot) {
    const keys = artifactGroupMove(shownGroups, key, slot);
    if (!keys) return;
    const prev = pendingGroups;
    const mine = { keys, at: Date.now() };
    pendingGroups = mine;
    render();
    try {
      await send(buildGroupOrderRequest(keys));
    } catch (err) {
      if (pendingGroups === mine) pendingGroups = prev;
      toast(err instanceof Error ? err.message : "Could not save the order", "error");
      if (!disposed) render();
    }
  }

  /** keyMoveRow is the keyboard path on a row: Alt+ArrowUp / Alt+ArrowDown. */
  function keyMoveRow(e, g, id) {
    if (!e.altKey || (e.key !== "ArrowUp" && e.key !== "ArrowDown")) return;
    e.preventDefault();
    const i = g.items.findIndex((a) => a.id === id);
    const to = e.key === "ArrowUp" ? i - 1 : i + 1;
    if (i < 0 || to < 0 || to >= g.items.length) return;
    reorderItems(g, id, to);
  }

  /** keyMoveGroup is the same on a group header. */
  function keyMoveGroup(e, g) {
    if (!e.altKey || (e.key !== "ArrowUp" && e.key !== "ArrowDown")) return;
    e.preventDefault();
    const movable = shownGroups.filter((x) => x.key !== "");
    const i = movable.findIndex((x) => x.key === g.key);
    const to = e.key === "ArrowUp" ? i - 1 : i + 1;
    if (i < 0 || to < 0 || to >= movable.length) return;
    moveGroup(g.key, to);
  }

  function row(a, g, i, nodes) {
    const sub = artifactSubline(a);
    const rev = artifactRevBadge(a);
    const ago = formatAgo(a.updated);
    const canMove = write && !filter && g.items.length > 1;
    const link = h(
      "a",
      {
        class: "win artifact-row",
        href: artifactHash(a.id),
        "data-id": a.id,
        onkeydown: canMove ? (e) => keyMoveRow(e, g, a.id) : null,
      },
      h("span", { class: `artifact-tile kind-${a.kind}`, role: "img", "aria-label": a.kind }, artifactIcon(a.kind)),
      h(
        "span",
        { class: "win-main" },
        h(
          "span",
          { class: "win-title" },
          h("span", { class: "agent" }, a.title),
          a.pinned ? h("span", { class: "artifact-pin", role: "img", "aria-label": "Pinned", title: "Pinned" }, "\u{1F4CC}") : null,
        ),
        sub ? h("span", { class: "activity" }, sub) : null,
        h(
          "span",
          { class: "win-chips" },
          a.agent ? h("span", { class: "tag agent-chip" }, a.agent) : null,
          ago ? h("span", { class: "tag time-chip" }, ago) : null,
          rev ? h("span", { class: "tag rev-badge" }, rev) : null,
        ),
      ),
      canMove ? h("span", { class: "artifact-grip", "aria-hidden": "true" }, "⠇⠇") : null,
    );
    const cls = ["artifact-node"];
    if (!seen.has(a.id)) cls.push("enter");
    if (fresh.delete(a.id)) cls.push("fresh");
    const node = h("div", { class: "node" }, link);
    let el = node;
    let dragger = null;
    if (write) {
      const action = h("button", { class: "swipe-action", type: "button", "aria-label": `Delete ${a.title}` }, "Delete");
      el = h("div", { class: "swipe" }, action, node);
      attachSwipe(el, node, link, action, { onBusy: busy, onClose: () => removeRow(a), dragging: () => dragger?.active() ?? false });
    }
    el.classList.add(...cls);
    el.style.setProperty("--i", String(Math.min(i, 10)));
    if (canMove) {
      dragger = attachDrag(el, link, {
        list: nodes,
        siblings: () => [...nodes.children].filter((x) => x !== el),
        mover: el,
        mid: (x) => {
          const r = x.getBoundingClientRect();
          return r.top + r.height / 2;
        },
        listClass: "reordering-rows",
        onBusy: busy,
        onDrop: (slot) => reorderItems(g, a.id, slot),
      });
    }
    return el;
  }

  function groupSection(g, groups) {
    const key = collapseKeyOf(g);
    const folded = collapsed.has(key);
    const n = g.items.length;
    const nodes = h("div", { class: "nodes", hidden: folded });
    g.items.forEach((a, i) => nodes.append(row(a, g, i, nodes)));
    const section = h("section", { class: `group artifact-group${folded ? " folded" : ""}`, "data-group": key });
    const toggle = h(
      "button",
      {
        class: "group-toggle",
        type: "button",
        "aria-expanded": String(!folded),
        "data-key": key,
        onclick: () => {
          const fold = !collapsed.has(key);
          if (fold) collapsed.add(key);
          else collapsed.delete(key);
          saveCollapsedSet(ARTIFACT_COLLAPSE_KEY, collapsed);
          section.classList.toggle("folded", fold);
          toggle.setAttribute("aria-expanded", String(!fold));
          animateFold(nodes, fold);
        },
        onkeydown: write && g.fleetKey ? (e) => keyMoveGroup(e, g) : null,
      },
      g.name,
    );
    const head = h(
      "div",
      { class: "group-head" },
      h("h2", {}, toggle),
      h("span", { class: "artifact-count", title: `${n} artifact${n === 1 ? "" : "s"}` }, String(n)),
    );
    section.append(head, nodes);
    if (write && g.fleetKey && groups.filter((x) => x.key !== "").length > 1) {
      attachDrag(section, head, {
        list,
        siblings: () => [...list.querySelectorAll(":scope > section.group")].filter((el) => el !== section),
        onBusy: busy,
        onDrop: (slot) => moveGroup(g.key, slot),
      });
    }
    return section;
  }

  function render() {
    if (disposed || !data) return;
    if (busyCount > 0) {
      renderPending = true;
      return;
    }
    list.dataset.renders = String(++renders);
    const focus = list.contains(document.activeElement) ? document.activeElement : null;
    const focusId = focus?.dataset?.id ?? "";
    const focusKey = focus?.classList.contains("group-toggle") ? focus.dataset.key : "";
    const visible = data.filter((a) => !hidden(a));
    const shown = filter ? artifactsForWindow(visible, filter.session, filter.windowId) : visible;
    clear(filterBar);
    if (filter) {
      filterBar.append(
        h("span", { class: "muted" }, `From window ${filter.windowId}`),
        h("a", { class: "btn small", href: artifactsHash() }, "Show all"),
      );
    }
    clear(list);
    status.hidden = true;
    shownGroups = [];
    if (shown.length === 0) {
      list.append(emptyState(filter));
      return;
    }
    const fleetGroups = ctx.store.fleet?.groups ?? [];
    let groups = groupArtifacts(shown, fleetGroups);
    const served = groups.map((g) => g.fleetKey).filter(Boolean);
    if (pendingGroups && (sameOrder(served, pendingGroups.keys) || Date.now() - pendingGroups.at > 10000)) pendingGroups = null;
    if (pendingGroups) groups = groupArtifacts(shown, applyGroupOrder(fleetGroups, pendingGroups.keys));
    groups = groups.map((g) => {
      const p = pendingOrder.get(g.key);
      return p ? { ...g, items: mergeArtifactOrder(g.items, p.ids) } : g;
    });
    shownGroups = groups;
    for (const g of groups) list.append(groupSection(g, groups));
    for (const a of shown) seen.add(a.id);
    // Forget folds of projects with no artifacts left.
    const live = new Set(groups.map(collapseKeyOf));
    const stale = [...collapsed].filter((k) => !live.has(k));
    if (stale.length > 0 && !filter) {
      stale.forEach((k) => collapsed.delete(k));
      saveCollapsedSet(ARTIFACT_COLLAPSE_KEY, collapsed);
    }
    // Rendering rebuilds the list; keep keyboard focus on the same row or header.
    if (focusId) [...list.querySelectorAll(".artifact-row")].find((el) => el.dataset.id === focusId)?.focus();
    else if (focusKey) [...list.querySelectorAll(".group-toggle")].find((el) => el.dataset.key === focusKey)?.focus();
  }

  async function load() {
    const startedAt = Date.now();
    try {
      const next = (await get("/v1/artifacts")).artifacts ?? [];
      if (disposed) return;
      if (data) for (const a of next) if (!seen.has(a.id) && !hidden(a)) fresh.add(a.id);
      data = next;
      for (const [k, p] of pendingOrder) if ((p.confirmedAt && startedAt >= p.confirmedAt) || Date.now() - p.at > 30000) pendingOrder.delete(k);
      render();
    } catch (e) {
      if (disposed) return;
      if (!data) {
        status.textContent = `Could not load artifacts: ${errText(e)}`;
        status.hidden = false;
      }
    }
  }

  const refresh = debounced(load, REFRESH_DEBOUNCE_MS);
  const unsub = ctx.store.subscribe((ev) => {
    if (ev?.type === "artifact") refresh();
    // The fleet feed also carries the saved group order, and a reconnect
    // may have dropped artifact events.
    if (ev?.type === "fleet") render();
    if (ev?.type === "status" && !ctx.store.downSince) refresh();
  });
  const timer = setInterval(() => !document.hidden && load(), POLL_MS);
  // Closing the tab sends what is waiting out its undo window.
  const onHide = () => queue.flush();
  addEventListener("pagehide", onHide);
  load();

  return () => {
    disposed = true;
    refresh.cancel();
    clearInterval(timer);
    unsub();
    removeEventListener("pagehide", onHide);
    // Leaving the page commits the deletes still waiting: undo needs this page.
    queue.flush();
    for (const dismiss of undoToasts.values()) dismiss();
    undoToasts.clear();
  };
}

// ---- viewer ----

function mountViewer(root, ctx, route) {
  const write = ctx.hasFeature("write");
  const canComment = write && ctx.hasFeature("artifact-comments");
  const id = route.id;
  let detail = null;
  let fetchedAt = 0;
  let disposed = false;
  let frameSrc = "";

  const title = h("h1", {}, "");
  const revSlot = h("span", { class: "artifact-revslot" });
  const more = h("button", { class: "btn icon", type: "button", "aria-label": "More actions", hidden: true, onclick: () => moreSheet() }, "⋯");
  const commentBtn = h("button", { class: "btn icon", type: "button", "aria-label": "Send feedback to the agent", hidden: true, onclick: () => composer("") }, "\u{1F4AC}");
  const frame = h("iframe", {
    class: "artifact-frame",
    sandbox: SANDBOX,
    title: "Artifact",
    referrerpolicy: "no-referrer",
    hidden: true,
  });
  const message = h("div", { class: "artifact-message", hidden: true });
  const view = h(
    "div",
    { class: "artifact-view" },
    h("header", { class: "bar artifact-bar" }, h("a", { class: "btn icon", href: "#/artifacts", "aria-label": "Back to artifacts" }, "‹"), title, revSlot, commentBtn, more),
    message,
    frame,
  );
  root.append(view);

  const revNumber = () => {
    if (!detail) return 0;
    return route.rev && detail.revs.some((r) => r.n === route.rev) ? route.rev : detail.latest;
  };
  const currentUrl = () => {
    const r = detail?.revs.find((x) => x.n === revNumber());
    return r?.raw_url || detail?.raw_url || "";
  };

  function showMessage(text, backLabel = "Back to artifacts") {
    more.hidden = true;
    commentBtn.hidden = true;
    clear(revSlot);
    frame.hidden = true;
    frameSrc = "";
    frame.removeAttribute("src");
    clear(message);
    message.append(h("p", {}, text), h("a", { class: "btn", href: "#/artifacts" }, backLabel));
    message.hidden = false;
  }

  function deleted() {
    title.textContent = "Artifact";
    showMessage("This artifact was deleted");
  }

  function setFrame() {
    const url = currentUrl();
    if (!url) return;
    // Assigning the same src would reload the document for nothing.
    if (url !== frameSrc) {
      frameSrc = url;
      frame.src = url;
    }
    frame.hidden = false;
    message.hidden = true;
  }

  async function togglePin() {
    try {
      detail = { ...detail, ...(await api("POST", `/v1/artifacts/${enc(id)}/pin`, { pinned: !detail.pinned })) };
      renderTools();
    } catch (e) {
      toast(`${detail.pinned ? "Unpin" : "Pin"} failed: ${errText(e)}`, "error");
    }
  }

  async function remove() {
    const ok = await confirmSheet({
      title: `Delete ${detail.title}?`,
      lines: ["Removes the artifact and all its revisions from this machine."],
      confirmLabel: "Delete",
      danger: true,
    });
    if (!ok || disposed) return;
    try {
      await api("DELETE", `/v1/artifacts/${enc(id)}`, {});
      toast("Artifact deleted");
      location.hash = "#/artifacts";
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) deleted();
      else toast(`Delete failed: ${errText(e)}`, "error");
    }
  }

  function renderTools() {
    title.textContent = detail.title;
    clear(revSlot);
    if (detail.revs.length > 1) {
      const n = revNumber();
      const sel = h(
        "select",
        {
          class: "artifact-revs",
          "aria-label": "Revision",
          onchange: () => {
            const v = Number(sel.value);
            location.hash = artifactHash(id, v === detail.latest ? null : v);
          },
        },
        ...[...detail.revs].reverse().map((r) => h("option", { value: r.n, selected: r.n === n }, artifactRevLabel(r))),
      );
      revSlot.append(h("span", { class: `artifact-pill${n === detail.latest ? "" : " old"}` }, artifactRevPill(n, detail.latest, detail.revs.length)), sel);
    }
    more.hidden = false;
    commentBtn.hidden = !canComment;
  }

  let composerEl = null;

  /**
   * composer is the feedback bottom sheet: the revision it applies to, a text
   * box and Send. prefill comes from the postMessage bridge: it only fills the
   * box; nothing is sent until Brendan taps Send. An open composer keeps what
   * he typed and gets the new text on a new line.
   */
  function composer(prefill) {
    if (!detail || !canComment) return;
    if (composerEl) {
      const ta = composerEl.querySelector("textarea");
      if (prefill) ta.value = (ta.value.trim() ? `${ta.value}\n${prefill}` : prefill).slice(0, ARTIFACT_FEEDBACK_MAX);
      ta.focus();
      return;
    }
    const rev = revNumber();
    let sending = false;
    const close = () => {
      removeEventListener("hashchange", close);
      composerEl = null;
      overlay.remove();
    };
    addEventListener("hashchange", close, { once: true });
    const ta = h("textarea", { rows: 5, maxlength: String(ARTIFACT_FEEDBACK_MAX), placeholder: "What should change?", "aria-label": "Feedback" });
    ta.value = prefill;
    const err = h("p", { class: "sheet-error", hidden: true });
    const send = h("button", { class: "btn primary", type: "button", onclick: submit }, "Send");
    async function submit() {
      const text = ta.value.trim();
      if (!text || sending) return;
      sending = true;
      send.disabled = true;
      err.hidden = true;
      try {
        const res = await api("POST", `/v1/artifacts/${enc(id)}/comments`, { text, rev });
        close();
        const [msg, kind] = artifactCommentToast(res);
        toast(msg, kind);
        load();
      } catch (e) {
        sending = false;
        send.disabled = false;
        err.textContent = `Could not send: ${errText(e)}`;
        err.hidden = false;
      }
    }
    const overlay = h(
      "div",
      { class: "overlay", onclick: (e) => e.target === overlay && close() },
      h(
        "div",
        { class: "sheet artifact-sheet", role: "dialog", "aria-modal": "true", "aria-label": "Send feedback" },
        h("h2", {}, "Feedback for the agent"),
        h("p", { class: "artifact-origin" }, `Applies to v${rev}${rev === detail.latest ? "" : " (not the latest)"}`),
        ta,
        err,
        h("div", { class: "row" }, h("button", { class: "btn", type: "button", onclick: close }, "Cancel"), send),
      ),
    );
    composerEl = overlay;
    document.body.append(overlay);
    ta.focus();
  }

  // The bridge: an artifact may propose feedback with
  // parent.postMessage({type: "agentctl:feedback", text}, "*"). Only the
  // iframe's own window counts, and it never sends: it opens the composer
  // prefilled so a person reviews the text and taps Send.
  const onMessage = (e) => {
    const text = artifactFeedbackFrom(e, frame.contentWindow);
    if (text) composer(text);
  };
  addEventListener("message", onMessage);

  /** moreSheet is the ⋯ bottom sheet: origin, open in a new tab, pin and delete. */
  function moreSheet() {
    const close = () => {
      removeEventListener("hashchange", close);
      overlay.remove();
    };
    addEventListener("hashchange", close, { once: true });
    const live = artifactWindowLive(detail, ctx.store.fleet?.groups);
    const origin = artifactOrigin(detail);
    const overlay = h(
      "div",
      { class: "overlay", onclick: (e) => e.target === overlay && close() },
      h(
        "div",
        { class: "sheet artifact-sheet", role: "dialog", "aria-modal": "true", "aria-label": "Artifact actions" },
        h("h2", {}, detail.title),
        origin ? h("p", { class: "artifact-origin" }, origin) : null,
        live ? h("a", { class: "btn", href: windowHash(detail.session, detail.window_id), onclick: close }, "Go to window ›") : null,
        h("a", { class: "btn", href: currentUrl(), target: "_blank", rel: "noopener noreferrer", onclick: close }, "Open in new tab ↗"),
        detail.comments?.length
          ? h(
              "div",
              { class: "artifact-comments" },
              h("h3", {}, `Feedback sent (${detail.comments.length})`),
              ...[...detail.comments].reverse().map((c) =>
                h(
                  "div",
                  { class: "artifact-comment" },
                  h("span", { class: "muted" }, `v${c.rev} \u00B7 ${formatAgo(c.at) || "now"} \u00B7 ${artifactCommentStatus(c)}`),
                  h("p", {}, c.text),
                ),
              ),
            )
          : null,
        write
          ? h("button", { class: "btn", type: "button", onclick: () => { close(); togglePin(); } }, detail.pinned ? "Unpin" : "Pin")
          : null,
        write ? h("button", { class: "btn danger", type: "button", onclick: () => { close(); remove(); } }, "Delete") : null,
        h("button", { class: "btn", type: "button", onclick: close }, "Close"),
      ),
    );
    document.body.append(overlay);
  }

  async function load() {
    try {
      const d = await get(`/v1/artifacts/${enc(id)}`);
      if (disposed) return;
      detail = d;
      fetchedAt = Date.now();
      renderTools();
      setFrame();
    } catch (e) {
      if (disposed) return;
      if (e instanceof ApiError && e.status === 404) deleted();
      else if (!detail) showMessage(`Could not load this artifact: ${errText(e)}`, "Back");
    }
  }

  // Tokens last ~24h; a viewer left open in the background gets a fresh URL
  // when it comes back and the one it has is more than an hour old.
  const onBack = () => {
    if (document.visibilityState !== "visible" || !detail) return;
    if (artifactTokenStale(fetchedAt)) load();
  };
  document.addEventListener("visibilitychange", onBack);
  addEventListener("focus", onBack);

  const refresh = debounced(load, REFRESH_DEBOUNCE_MS);
  const unsub = ctx.store.subscribe((ev) => {
    if (ev?.type === "artifact" && (!ev.change?.id || ev.change.id === id)) {
      if (ev.change?.action === "rm") deleted();
      else refresh();
    }
    if (ev?.type === "fleet" && detail) renderTools();
  });

  load();

  return () => {
    disposed = true;
    refresh.cancel();
    unsub();
    removeEventListener("message", onMessage);
    if (composerEl) composerEl.remove();
    document.removeEventListener("visibilitychange", onBack);
    removeEventListener("focus", onBack);
  };
}
