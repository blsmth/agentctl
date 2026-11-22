// Live fleet feed: SSE with reconnect backoff, or 5s polling when the server
// does not advertise "events".
import { get } from "./api.js";
import { backoffDelay } from "./logic.js";

const POLL_MS = 5000;

const PRESENCE_MS = 20000;

/**
 * startPresence tells the server whether this device has the app in the
 * foreground, so it can skip web push while the user is looking at the app.
 * Visible: report on start, on becoming visible, and every 20s. Hidden or
 * pagehide: report once with keepalive. The server expires a visible report
 * after ~45s, which covers a frozen page that never sends the hidden one.
 * Returns a stop function. Best effort: failures are ignored.
 */
export function startPresence() {
  let timer = null;
  const report = (visible) => {
    fetch("/v1/presence", {
      method: "POST",
      headers: { "Content-Type": "application/json", "X-Agentctl-CSRF": "1" },
      body: JSON.stringify({ visible }),
      credentials: "same-origin",
      cache: "no-store",
      keepalive: true,
    }).catch(() => {});
  };
  const beat = () => {
    clearInterval(timer);
    timer = null;
    if (document.visibilityState === "visible") {
      report(true);
      timer = setInterval(() => report(true), PRESENCE_MS);
    }
  };
  const onVis = () => {
    if (document.visibilityState === "visible") beat();
    else {
      clearInterval(timer);
      timer = null;
      report(false);
    }
  };
  const onHide = () => {
    clearInterval(timer);
    timer = null;
    report(false);
  };
  document.addEventListener("visibilitychange", onVis);
  window.addEventListener("pagehide", onHide);
  window.addEventListener("pageshow", beat);
  beat();
  return () => {
    clearInterval(timer);
    document.removeEventListener("visibilitychange", onVis);
    window.removeEventListener("pagehide", onHide);
    window.removeEventListener("pageshow", beat);
  };
}

/**
 * startLive connects the feed and returns a stop function. Handlers:
 * onFleet(body), onWindow(win), onGone({session, window_id}),
 * onApproval(approval), onApprovalResolved({id, outcome, ...}),
 * onStatus(up: boolean).
 */
export function startLive({ events, onFleet, onWindow, onGone, onApproval, onApprovalResolved, onStatus }) {
  let stopped = false;
  let timer = null;
  let source = null;
  let attempt = 0;

  const later = (fn, ms) => {
    clearTimeout(timer);
    timer = setTimeout(fn, ms);
  };

  async function poll() {
    if (stopped) return;
    try {
      onFleet(await get("/v1/fleet"));
      onStatus(true);
    } catch {
      onStatus(false);
    }
    later(poll, POLL_MS);
  }

  function connect() {
    if (stopped) return;
    source = new EventSource("/v1/events", { withCredentials: true });
    const parse = (fn) => (e) => {
      try {
        fn(JSON.parse(e.data));
      } catch {
        // ignore a malformed frame
      }
    };
    source.addEventListener("fleet", (e) => {
      attempt = 0;
      onStatus(true);
      parse(onFleet)(e);
    });
    source.addEventListener("window", parse(onWindow));
    source.addEventListener("gone", parse(onGone));
    if (onApproval) source.addEventListener("approval", parse(onApproval));
    if (onApprovalResolved) source.addEventListener("approval_resolved", parse(onApprovalResolved));
    source.onerror = () => {
      source.close();
      onStatus(false);
      later(connect, backoffDelay(attempt++));
    };
  }

  if (events) connect();
  else poll();

  return () => {
    stopped = true;
    clearTimeout(timer);
    if (source) source.close();
  };
}
