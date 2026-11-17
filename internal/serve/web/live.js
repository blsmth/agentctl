// Live fleet feed: SSE with reconnect backoff, or 5s polling when the server
// does not advertise "events".
import { get } from "./api.js";
import { backoffDelay } from "./logic.js";

const POLL_MS = 5000;

/**
 * startLive connects the feed and returns a stop function. Handlers:
 * onFleet(body), onWindow(win), onGone({session, window_id}),
 * onStatus(up: boolean).
 */
export function startLive({ events, onFleet, onWindow, onGone, onStatus }) {
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
