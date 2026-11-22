// App entry: feature detection, live store, hash router, service worker.
import { get } from "./api.js";
import { mountApprovals } from "./approvals.js";
import { mountDispatch } from "./dispatch.js";
import { mountFleet } from "./fleet.js";
import { mountHost } from "./host.js";
import { startLive, startPresence } from "./live.js";
import {
  applyGone,
  applyWindow,
  hasFeature,
  parseRoute,
  removeApproval,
  upsertApproval,
  withReceived,
} from "./logic.js";
import { mountSchedules } from "./schedules.js";
import { mountSettings } from "./settings.js";
import { mountTerminal } from "./terminal.js";
import { h } from "./ui.js";
import { mountWindow } from "./window.js";

/** store holds the live fleet and notifies subscribers of every change. */
const store = {
  fleet: null,
  downSince: null,
  fleetAt: null,
  approvals: [],
  recentApprovals: [],
  listeners: new Set(),
  subscribe(fn) {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  },
  emit(ev) {
    for (const fn of this.listeners) fn(ev);
  },
};

const ctx = {
  store,
  features: [],
  hasFeature: (name) => hasFeature(ctx.features, name),
  refreshApprovals: () => refreshApprovals(),
};

const APPROVALS_POLL_MS = 15000;

function setApprovals(list) {
  store.approvals = list;
  renderNavBadge();
  store.emit({ type: "approvals" });
}

/** refreshApprovals refetches the pending list (after reconnects and answers). */
async function refreshApprovals() {
  if (!ctx.hasFeature("approvals")) return;
  try {
    const body = await get("/v1/approvals");
    const now = Date.now();
    store.recentApprovals = body.recent ?? [];
    setApprovals((body.approvals ?? []).map((a) => withReceived(a, now)));
  } catch {
    // keep the last known list; the next event or poll retries
  }
}

function renderNavBadge() {
  const badge = document.getElementById("nav-approvals-badge");
  if (!badge) return;
  const n = store.approvals.length;
  badge.textContent = n > 0 ? String(n) : "";
  badge.hidden = n === 0;
}
const root = document.getElementById("app");
let cleanup = () => {};

function route() {
  cleanup();
  root.replaceChildren();
  // Sheets live on document.body, outside #app; none may outlive its view.
  document.querySelectorAll(".overlay").forEach((o) => o.remove());
  const r = parseRoute(location.hash);
  if (r.name === "window") cleanup = mountWindow(root, ctx, r);
  else if (r.name === "terminal" && ctx.hasFeature("terminal")) cleanup = mountTerminal(root, ctx, r);
  else if (r.name === "approvals" && ctx.hasFeature("approvals")) cleanup = mountApprovals(root, ctx, r);
  else if (r.name === "dispatch") cleanup = mountDispatch(root, ctx, { dir: r.dir });
  else if (r.name === "schedules" && ctx.hasFeature("schedules")) cleanup = mountSchedules(root, ctx, r);
  else if (r.name === "host") cleanup = mountHost(root, ctx);
  else if (r.name === "settings") cleanup = mountSettings(root, ctx);
  else cleanup = mountFleet(root, ctx);
  document.querySelectorAll("nav a").forEach((a) => {
    a.classList.toggle("active", a.getAttribute("href") === (r.name === "fleet" ? "#/" : `#/${r.name}`));
  });
}

function startFeed() {
  if (ctx.hasFeature("push")) startPresence();
  startLive({
    events: ctx.hasFeature("events"),
    onFleet(body) {
      store.fleet = body;
      store.fleetAt = Date.now();
      store.emit({ type: "fleet" });
      // A fleet event starts every (re)connected stream: events may have
      // been missed while it was down.
      refreshApprovals();
    },
    onApproval(a) {
      setApprovals(upsertApproval(store.approvals, withReceived(a, Date.now())));
    },
    onApprovalResolved(r) {
      setApprovals(removeApproval(store.approvals, r.id));
      refreshApprovals(); // pick up the new history entry
    },
    onWindow(win) {
      if (store.fleet) store.fleet = applyWindow(store.fleet, win);
      store.emit({ type: "window", win });
    },
    onGone(g) {
      if (store.fleet) store.fleet = applyGone(store.fleet, g.session, g.window_id);
      store.emit({ type: "gone" });
    },
    onStatus(up) {
      if (up) store.downSince = null;
      else if (!store.downSince) store.downSince = Date.now();
      store.emit({ type: "status" });
    },
  });
}

async function main() {
  if ("serviceWorker" in navigator) {
    navigator.serviceWorker.register("/sw.js", { scope: "/" }).catch(() => {});
    navigator.serviceWorker.addEventListener("message", (e) => {
      if (e.data?.type === "navigate" && typeof e.data.url === "string") location.assign(e.data.url);
    });
  }
  try {
    ctx.features = (await get("/healthz")).features ?? [];
  } catch {
    ctx.features = [];
  }
  if (!ctx.hasFeature("fleet")) {
    root.append(h("p", { class: "muted pad" }, "This server does not provide the fleet API yet."));
    return;
  }
  document.getElementById("nav-dispatch").hidden = !ctx.hasFeature("write");
  document.getElementById("nav-schedules").hidden = !ctx.hasFeature("schedules");
  document.getElementById("nav-approvals").hidden = !ctx.hasFeature("approvals");
  startFeed();
  if (ctx.hasFeature("approvals")) {
    refreshApprovals();
    setInterval(() => !document.hidden && refreshApprovals(), APPROVALS_POLL_MS);
  }
  addEventListener("hashchange", route);
  route();
}

main();
