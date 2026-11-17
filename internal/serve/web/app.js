// App entry: feature detection, live store, hash router, service worker.
import { get } from "./api.js";
import { mountDispatch } from "./dispatch.js";
import { mountFleet } from "./fleet.js";
import { startLive } from "./live.js";
import { applyGone, applyWindow, hasFeature, parseRoute } from "./logic.js";
import { mountSettings } from "./settings.js";
import { h } from "./ui.js";
import { mountWindow } from "./window.js";

/** store holds the live fleet and notifies subscribers of every change. */
const store = {
  fleet: null,
  downSince: null,
  listeners: new Set(),
  subscribe(fn) {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  },
  emit(ev) {
    for (const fn of this.listeners) fn(ev);
  },
};

const ctx = { store, features: [], hasFeature: (name) => hasFeature(ctx.features, name) };
const root = document.getElementById("app");
let cleanup = () => {};

function route() {
  cleanup();
  root.replaceChildren();
  const r = parseRoute(location.hash);
  if (r.name === "window") cleanup = mountWindow(root, ctx, r);
  else if (r.name === "dispatch") cleanup = mountDispatch(root, ctx);
  else if (r.name === "settings") cleanup = mountSettings(root, ctx);
  else cleanup = mountFleet(root, ctx);
  document.querySelectorAll("nav a").forEach((a) => {
    a.classList.toggle("active", a.getAttribute("href") === (r.name === "fleet" ? "#/" : `#/${r.name}`));
  });
}

function startFeed() {
  startLive({
    events: ctx.hasFeature("events"),
    onFleet(body) {
      store.fleet = body;
      store.emit({ type: "fleet" });
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
  startFeed();
  addEventListener("hashchange", route);
  route();
}

main();
