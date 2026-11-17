// Window detail: header, read-only xterm pane, state-aware action bar.
import { ApiError, get, send } from "./api.js";
import {
  actionsFor,
  buildAnswerRequest,
  buildReplyRequest,
  effectiveState,
  formatSince,
  safeHttpsUrl,
  windowApiPath,
} from "./logic.js";
import { clear, confirmSheet, h, toast } from "./ui.js";

const PANE_REFRESH_MS = 2000;

export function mountWindow(root, ctx, { session, windowId }) {
  const base = windowApiPath(session, windowId);
  const canWrite = ctx.hasFeature("write");
  let win = null;
  let pane = null;
  let lastText = null;
  let lastActionsKey = null;
  let gone = false;

  const title = h("h1", {}, windowId);
  const sub = h("div", { class: "meta pad-x" });
  const termHost = h("div", { class: "term" });
  const actions = h("div", { class: "actions" });
  root.append(
    h("header", { class: "bar" }, h("a", { class: "btn", href: "#/" }, "‹ Fleet"), title),
    sub,
    termHost,
    actions,
  );

  const term = new window.Terminal({
    disableStdin: true,
    convertEol: true,
    fontSize: 12,
    scrollback: 2000,
    cursorBlink: false,
    cursorStyle: "underline",
    cursorInactiveStyle: "none",
  });
  const fit = new window.FitAddon.FitAddon();
  term.loadAddon(fit);
  term.open(termHost);
  const refit = () => {
    try {
      fit.fit();
    } catch {
      // host not laid out yet
    }
  };
  refit();
  window.addEventListener("resize", refit);

  function renderHeader() {
    if (!win) return;
    title.textContent = win.agent || windowId;
    clear(sub);
    const bits = [win.branch, win.mode, win.context_pct ? `${Math.round(win.context_pct)}% ctx` : "", effectiveState(win)];
    const since = formatSince(win.blocked_since || win.state_since, Date.now());
    if (since) bits.push(since);
    sub.append(bits.filter(Boolean).join(" · "));
    const pr = safeHttpsUrl(win.pr_url);
    if (pr) {
      sub.append(" · ", h("a", { href: pr, target: "_blank", rel: "noopener noreferrer" }, "PR"));
    }
  }

  // run confirms, sends, and on failure toasts and refreshes (409 = stale).
  async function run(confirm, req) {
    if (!(await confirmSheet(confirm))) return false;
    try {
      await send(req);
      toast("Sent");
    } catch (e) {
      toast(e instanceof ApiError ? e.message : String(e), "error");
    }
    await refresh();
    return true;
  }

  function answerButtons() {
    const dialog = pane.dialog;
    const shown = (key) => {
      let req;
      try {
        req = buildAnswerRequest(session, windowId, dialog, key);
      } catch (e) {
        toast(e.message, "error");
        return;
      }
      run(
        {
          title: key === "esc" ? "Send Escape?" : `Answer "${key}"?`,
          lines: [dialog.text],
          confirmLabel: "Send",
        },
        req,
      );
    };
    return [
      h("pre", { class: "dialog" }, dialog.text),
      h(
        "div",
        { class: "choices" },
        ...(dialog.options ?? []).map((o) =>
          h("button", { class: "btn", onclick: () => shown(o.key) }, `${o.key}. ${o.label}`),
        ),
        h("button", { class: "btn", onclick: () => shown("esc") }, "Esc"),
      ),
    ];
  }

  function replyBox() {
    const text = h("textarea", { rows: "3", placeholder: "Reply to the agent…", maxlength: "4000" });
    return [
      text,
      h(
        "div",
        { class: "row" },
        h(
          "button",
          {
            class: "btn primary",
            onclick: () => {
              const value = text.value.trim();
              if (value) run({ title: "Send reply?", lines: [value], confirmLabel: "Send" }, buildReplyRequest(session, windowId, value));
            },
          },
          "Reply",
        ),
        h(
          "button",
          {
            class: "btn danger",
            onclick: () =>
              run(
                { title: "Close window?", lines: [`${win?.agent ?? ""} ${windowId}`], confirmLabel: "Close", danger: true },
                { method: "POST", path: `${base}/close`, body: {} },
              ),
          },
          "Close window",
        ),
      ),
    ];
  }

  function interruptButton() {
    return [
      h(
        "button",
        {
          class: "btn danger",
          onclick: () =>
            run(
              { title: "Interrupt agent?", lines: ["Sends Escape to the running agent."], confirmLabel: "Interrupt", danger: true },
              { method: "POST", path: `${base}/interrupt`, body: {} },
            ),
        },
        "Interrupt",
      ),
    ];
  }

  function renderActions() {
    if (!win) return;
    if (!canWrite) {
      clear(actions);
      actions.append(h("p", { class: "muted" }, "Read-only: write actions are not enabled on this server."));
      return;
    }
    const kind = actionsFor(win, pane);
    // Do not rebuild while the user may be typing a reply: only when the kind or dialog changes.
    const key = `${kind}:${pane?.dialog?.hash ?? ""}`;
    if (key === lastActionsKey) return;
    lastActionsKey = key;
    clear(actions);
    if (kind === "answer") actions.append(...answerButtons());
    else if (kind === "reply") actions.append(...replyBox());
    else if (kind === "interrupt") actions.append(...interruptButton());
  }

  async function refreshPane() {
    try {
      pane = await get(`${base}/pane?lines=200`);
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) gone = true;
      return;
    }
    if (pane.text !== lastText) {
      lastText = pane.text;
      term.write(`\x1b[2J\x1b[3J\x1b[H${pane.text}`);
    }
  }

  async function refreshWindow() {
    try {
      win = await get(base);
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) {
        gone = true;
        title.textContent = "Window closed";
      }
    }
  }

  async function refresh() {
    await Promise.all([refreshWindow(), refreshPane()]);
    if (gone) return;
    renderHeader();
    renderActions();
  }

  refresh();
  const timer = setInterval(() => {
    if (!document.hidden && !gone) refresh();
  }, PANE_REFRESH_MS);
  const onVisible = () => !document.hidden && refresh();
  document.addEventListener("visibilitychange", onVisible);
  const unsub = ctx.store.subscribe((ev) => {
    if (ev?.type === "window" && ev.win.session === session && ev.win.window_id === windowId) refresh();
  });

  return () => {
    clearInterval(timer);
    unsub();
    document.removeEventListener("visibilitychange", onVisible);
    window.removeEventListener("resize", refit);
    term.dispose();
  };
}
