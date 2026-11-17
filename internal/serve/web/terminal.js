// Live terminal: a full-screen xterm.js attached to the window over a
// WebSocket. Binary frames are raw terminal bytes both ways; text frames are
// JSON control messages. The remote side is never resized from here: the
// server sends the window's real size and the font is scaled to fit it.
import {
  backoffDelay,
  KEY_BAR,
  keySequence,
  parseControl,
  terminalSocketUrl,
  windowHash,
} from "./logic.js";
import { fitTermFont, h } from "./ui.js";

const PING_MS = 20000;
const BASE_FONT = 10;
const MIN_FONT = 4;
const MAX_FONT = 24;

export function mountTerminal(root, ctx, { session, windowId }) {
  const encoder = new TextEncoder();
  let ws = null;
  let attempt = 0;
  let stopped = false;
  let retryTimer = null;
  let readOnly = true;
  let size = null;
  let userZoomed = false;

  const status = h("span", { class: "term-status" }, "Connecting…");
  const badge = h("span", { class: "badge", hidden: true }, "read-only");
  const title = h("h1", {}, windowId);
  const win = ctx.store.fleet?.groups
    ?.find((g) => g.session === session)
    ?.windows?.find((w) => w.window_id === windowId);
  if (win?.agent) title.textContent = win.agent;
  const host = h("div", { class: "term-host" });
  const viewport = h("div", { class: "term-viewport" }, host);
  const keybar = h("div", { class: "keybar", hidden: true });
  const screen = h(
    "div",
    { class: "term-screen" },
    h("header", { class: "bar" }, h("a", { class: "btn", href: windowHash(session, windowId) }, "‹ Back"), title, badge),
    h("div", { class: "term-statusline" }, status),
    viewport,
    keybar,
  );
  root.append(screen);
  document.body.classList.add("term-open");

  const term = new window.Terminal({
    cols: 80,
    rows: 24,
    fontSize: BASE_FONT,
    scrollback: 1000,
    cursorBlink: false,
    disableStdin: true,
  });
  term.open(host);

  const setStatus = (text) => {
    status.textContent = text;
  };

  // fitWidth scales the font so the window's columns fill the screen width.
  function fitWidth() {
    if (!size || userZoomed) return;
    fitTermFont(term, host, viewport.clientWidth - 4, MIN_FONT, MAX_FONT);
  }

  function applySize(m) {
    size = m;
    if (term.cols !== m.cols || term.rows !== m.rows) term.resize(m.cols, m.rows);
    setReadOnly(m.readOnly);
    requestAnimationFrame(fitWidth);
  }

  function setReadOnly(ro) {
    readOnly = ro;
    badge.hidden = !ro;
    keybar.hidden = ro;
    term.options.disableStdin = ro;
    // No soft keyboard for a terminal that cannot type.
    if (term.textarea) term.textarea.inputMode = ro ? "none" : "text";
  }

  function send(data) {
    if (readOnly || !ws || ws.readyState !== WebSocket.OPEN) return;
    ws.send(encoder.encode(data));
  }
  const dataSub = term.onData(send);

  for (const key of KEY_BAR) {
    keybar.append(
      h(
        "button",
        {
          class: "key",
          type: "button",
          // Keep focus (and the soft keyboard) on xterm's textarea.
          onpointerdown: (e) => e.preventDefault(),
          onclick: () => send(keySequence(key, term.modes.applicationCursorKeysMode)),
        },
        key.label,
      ),
    );
  }

  function connect() {
    if (stopped) return;
    setStatus(attempt === 0 ? "Connecting…" : "Reconnecting…");
    const sock = new WebSocket(terminalSocketUrl(location, session, windowId));
    ws = sock;
    sock.binaryType = "arraybuffer";
    sock.onopen = () => {
      attempt = 0;
      // tmux redraws the whole screen for a new client.
      term.reset();
      setStatus("Live");
    };
    sock.onmessage = (e) => {
      if (typeof e.data === "string") {
        const m = parseControl(e.data);
        if (m?.type === "size") applySize(m);
        return;
      }
      term.write(new Uint8Array(e.data));
    };
    sock.onclose = (e) => {
      if (stopped || ws !== sock) return;
      if (e.code === 1001 && e.reason === "window closed") {
        setStatus("Window closed.");
        return;
      }
      const delay = backoffDelay(attempt++);
      const why = e.reason ? `: ${e.reason}` : "";
      setStatus(`Disconnected${why}. Retrying in ${Math.round(delay / 1000)}s…`);
      retryTimer = setTimeout(connect, delay);
    };
  }

  const pinger = setInterval(() => {
    if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: "ping" }));
  }, PING_MS);

  // Pinch to zoom the font; the viewport scrolls (pans) when wider than the screen.
  let pinch = null;
  const dist = (t) => Math.hypot(t[0].clientX - t[1].clientX, t[0].clientY - t[1].clientY);
  const onTouchStart = (e) => {
    if (e.touches.length === 2) pinch = { d: dist(e.touches), font: term.options.fontSize };
  };
  const onTouchMove = (e) => {
    if (!pinch || e.touches.length !== 2) return;
    e.preventDefault();
    const f = Math.max(MIN_FONT, Math.min(MAX_FONT, Math.round(pinch.font * (dist(e.touches) / pinch.d) * 10) / 10));
    if (f !== term.options.fontSize) {
      userZoomed = true;
      term.options.fontSize = f;
    }
  };
  const onTouchEnd = (e) => {
    if (e.touches.length < 2) pinch = null;
  };
  viewport.addEventListener("touchstart", onTouchStart, { passive: true });
  viewport.addEventListener("touchmove", onTouchMove, { passive: false });
  viewport.addEventListener("touchend", onTouchEnd, { passive: true });
  // Double-tap the status line to return to fit-to-width.
  status.addEventListener("dblclick", () => {
    userZoomed = false;
    fitWidth();
  });

  // Keep the key bar above the soft keyboard.
  const vv = window.visualViewport;
  const placeKeybar = () => {
    if (!vv) return;
    const covered = Math.max(0, window.innerHeight - (vv.height + vv.offsetTop));
    keybar.style.bottom = `${covered}px`;
  };
  vv?.addEventListener("resize", placeKeybar);
  vv?.addEventListener("scroll", placeKeybar);
  window.addEventListener("resize", fitWidth);

  connect();

  return () => {
    stopped = true;
    clearTimeout(retryTimer);
    clearInterval(pinger);
    if (ws) ws.close(1000, "closed");
    dataSub.dispose();
    vv?.removeEventListener("resize", placeKeybar);
    vv?.removeEventListener("scroll", placeKeybar);
    window.removeEventListener("resize", fitWidth);
    term.dispose();
    document.body.classList.remove("term-open");
  };
}
