// Settings: server info, write mode, notifications.
import { ApiError, api, get } from "./api.js";
import { urlBase64ToBytes } from "./logic.js";
import { h, toast } from "./ui.js";

const isStandalone = () => window.matchMedia("(display-mode: standalone)").matches || navigator.standalone === true;
const pushSupported = () => "serviceWorker" in navigator && "PushManager" in window && "Notification" in window;

export function mountSettings(root, ctx) {
  const version = h("span", {}, "…");
  const pushBox = h("div", {});
  root.append(
    h("header", { class: "bar" }, h("a", { class: "btn", href: "#/" }, "‹ Fleet"), h("h1", {}, "Settings")),
    h(
      "div",
      { class: "form" },
      h("p", {}, "Server version: ", version),
      h("p", {}, "Write actions: ", ctx.hasFeature("write") ? "enabled" : "disabled (read-only)"),
      ctx.hasFeature("host") ? h("a", { class: "btn", href: "#/host" }, "Host status ›") : null,
      h("h2", {}, "Notifications"),
      pushBox,
    ),
  );
  get("/healthz").then((hz) => (version.textContent = hz.version || "unknown"), () => (version.textContent = "unreachable"));

  async function currentSubscription() {
    const reg = await navigator.serviceWorker.ready;
    return reg.pushManager.getSubscription();
  }

  async function subscribe() {
    if ((await Notification.requestPermission()) !== "granted") {
      toast("Notification permission was not granted", "error");
      return;
    }
    const { public_key: key } = await get("/v1/push/vapid");
    const reg = await navigator.serviceWorker.ready;
    const sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: urlBase64ToBytes(key) });
    await api("POST", "/v1/push/subscribe", sub.toJSON());
    toast("Notifications enabled");
  }

  async function unsubscribe() {
    const sub = await currentSubscription();
    if (sub) await sub.unsubscribe();
    await api("DELETE", "/v1/push/subscribe");
    toast("Notifications disabled");
  }

  const guarded = (fn) => async () => {
    try {
      await fn();
    } catch (e) {
      toast(e instanceof ApiError || e instanceof Error ? e.message : String(e), "error");
    }
    renderPush();
  };

  async function renderPush() {
    pushBox.replaceChildren();
    if (!ctx.hasFeature("push")) {
      pushBox.append(h("p", { class: "muted" }, "Push notifications are not available on this server."));
      return;
    }
    if (!isStandalone()) {
      pushBox.append(
        h("p", { class: "muted" }, "Add to Home Screen to enable notifications: tap Share, then “Add to Home Screen”, and open agentctl from there."),
      );
    }
    if (!pushSupported()) {
      pushBox.append(h("p", { class: "muted" }, "Open from the Home Screen icon to enable notifications (iOS requires it)."));
      return;
    }
    if (!isStandalone()) return;
    if (Notification.permission === "denied") {
      pushBox.append(h("p", { class: "muted" }, "Notifications are blocked in iOS Settings → Notifications → agentctl."));
    }
    const sub = await currentSubscription();
    let server = null;
    try {
      server = (await get("/v1/push/subscription")).subscribed;
    } catch {
      server = null;
    }
    let status = sub ? "subscribed on this device" : "not subscribed on this device";
    if (server === null) status += "; server state unknown";
    else if (server && !sub) status += "; server still has an old subscription (tap Enable to renew)";
    else if (!server && sub) status += "; server has no subscription (tap Enable to renew)";
    else if (server) status += "; server confirms";
    pushBox.append(h("p", {}, `Permission: ${Notification.permission}; ${status}`));
    if (sub && server !== false) {
      pushBox.append(
        h(
          "div",
          { class: "row" },
          h("button", { class: "btn", onclick: guarded(() => api("POST", "/v1/push/test", {}).then(() => toast("Test sent"))) }, "Send test push"),
          h("button", { class: "btn danger", onclick: guarded(unsubscribe) }, "Disable"),
        ),
      );
    } else {
      pushBox.append(h("button", { class: "btn primary", onclick: guarded(subscribe) }, "Enable notifications"));
    }
  }
  renderPush();
  return () => {};
}
