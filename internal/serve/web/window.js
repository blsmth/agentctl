// Window detail: header, pane (reflowed or exact), state-aware actions.
import { ApiError, get, send, uploadImages } from "./api.js";
import { approvalCard, tickCountdowns } from "./approvals.js";
import { imagePicker, svgIcon } from "./attach.js";
import {
  artifactChipText,
  projectAgent,
  artifactsForWindow,
  artifactsHash,
  INTERRUPT_FOLLOWUP_MS,
  PANE_TAIL_LINES,
  canLoadEarlier,
  historyAddsLines,
  isDoubleTap,
  isTap,
  actionsWithQueue,
  approvalsForWindow,
  buildAnswerRequest,
  checkboxChoices,
  buildQueueRequest,
  buildCommandRequest,
  commandConfirm,
  commandError,
  commandSuggestions,
  isClaudeWindow,
  isSlashCommand,
  buildReplyRequest,
  buildUploadPath,
  dialogQuestion,
  displayState,
  waitingIndex,
  parentOf,
  childrenOf,
  rowRole,
  windowHash,
  rowHeadline,
  detailTitle,
  fullPrompt,
  exactRows,
  focusedOption,
  hangingIndent,
  headerChips,
  liveness,
  paneLinesForExact,
  paneModeOrDefault,
  safeHttpsUrl,
  scheduledLine,
  splitPane,
  stateLabel,
  headerLines,
  terminalHash,
  windowApiPath,
} from "./logic.js";
import { clear, confirmSheet, fitTermFont, h, toast } from "./ui.js";

const CHILD_GLYPH = { blocked: "⏸", working: "●", waiting: "◷", done: "✓", idle: "○" };
const CHILD_CAP = 5;
const PANE_REFRESH_MS = 2000;
const MODE_KEY = "agentctl.paneMode";
const STATUS_KEY = "agentctl.statusBarOpen";
const EXACT_HINT_KEY = "agentctl.exactHintSeen";
const EXACT_MIN_FONT = 8;
const EXACT_MAX_FONT = 14;
// The live feed counts as down (for the liveness chip) after this long.
const FEED_DOWN_MS = 15000;

const SEND_SVG =
  '<svg viewBox="0 0 24 24" width="22" height="22" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 19V5M5 12l7-7 7 7"/></svg>';
const CLOSE_SVG =
  '<svg viewBox="0 0 24 24" width="22" height="22" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true"><path d="M6 6l12 12M18 6L6 18"/></svg>';

function loadFlag(key) {
  try {
    return localStorage.getItem(key) === "1";
  } catch {
    return false;
  }
}

function saveFlag(key, on) {
  try {
    localStorage.setItem(key, on ? "1" : "0");
  } catch {
    // storage unavailable (private mode): the choice just is not remembered
  }
}

function loadMode() {
  try {
    return paneModeOrDefault(localStorage.getItem(MODE_KEY));
  } catch {
    return "reflow";
  }
}

function saveMode(mode) {
  try {
    localStorage.setItem(MODE_KEY, mode);
  } catch {
    // storage unavailable (private mode): the choice just is not remembered
  }
}

/** styleSpan builds a span for one styled run; text only via textContent. */
function styleSpan(run) {
  const span = document.createElement("span");
  span.textContent = run.t;
  let fg = run.fg;
  let bg = run.bg;
  if (run.inverse) {
    [fg, bg] = [bg ?? "#000000", fg ?? "#e5e5e5"];
  }
  if (fg) span.style.color = fg;
  if (bg) span.style.backgroundColor = bg;
  if (run.bold) span.style.fontWeight = "700";
  if (run.dim) span.style.opacity = "0.6";
  if (run.italic) span.style.fontStyle = "italic";
  if (run.underline) span.style.textDecoration = "underline";
  return span;
}

/** paneLine renders one reflowed line with a hanging indent for wrapped continuations. */
function paneLine(runs) {
  const line = document.createElement("div");
  line.className = "pane-line";
  if (runs.length === 0) {
    line.textContent = " ";
    return line;
  }
  const indent = hangingIndent(runs.map((r) => r.t).join(""));
  if (indent !== 2) {
    line.style.paddingLeft = `${indent}ch`;
    line.style.textIndent = `-${indent}ch`;
  }
  for (const r of runs) line.append(styleSpan(r));
  return line;
}

/**
 * reflowView renders the pane as wrapping HTML. When output changes while
 * the reader is scrolled up, a "New output" pill offers the way back down.
 */
function reflowView({ loadHistory } = {}) {
  const scroller = h("div", { class: "pane-reflow" });
  const pill = h("button", { type: "button", class: "new-output", hidden: true }, "New output ↓");
  const el = h("div", { class: "pane-reflow-wrap" }, scroller, pill);
  const atBottom = () => scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 24;

  // Double tap (touch) or dblclick (desktop) toggles a full-screen pane: the
  // same elements get a class, so polling, scroll and the pill keep working.
  let expanded = false;
  const closeBtn = h(
    "button",
    { type: "button", class: "icon-btn pane-expand-close", "aria-label": "Close full screen", hidden: true, onclick: () => setExpanded(false) },
    svgIcon(CLOSE_SVG),
  );
  el.append(closeBtn);
  function setExpanded(on) {
    if (on === expanded) return;
    const follow = atBottom();
    const top = scroller.scrollTop;
    expanded = on;
    el.classList.toggle("expanded", on);
    closeBtn.hidden = !on;
    document.body.classList.toggle("pane-expanded", on);
    if (on) document.addEventListener("keydown", onKey);
    else document.removeEventListener("keydown", onKey);
    if (follow) scroller.scrollTop = scroller.scrollHeight;
    else scroller.scrollTop = top;
  }
  function onKey(e) {
    if (e.key === "Escape") setExpanded(false);
  }
  const isControl = (t) => t instanceof Element && !!t.closest("button, a, input, textarea");
  let touchStart = null;
  let prevTap = null;
  let lastTouchToggle = 0;
  scroller.addEventListener(
    "touchstart",
    (e) => {
      const t = e.touches[0];
      touchStart = e.touches.length === 1 && t ? { t: e.timeStamp, x: t.clientX, y: t.clientY } : null;
      if (!touchStart) prevTap = null;
    },
    { passive: true },
  );
  scroller.addEventListener("touchend", (e) => {
    const t = e.changedTouches[0];
    const start = touchStart;
    touchStart = null;
    if (!start || !t || isControl(e.target)) {
      prevTap = null;
      return;
    }
    const end = { t: e.timeStamp, x: t.clientX, y: t.clientY };
    if (!isTap(start, end) || !(window.getSelection()?.isCollapsed ?? true)) {
      prevTap = null;
      return;
    }
    if (isDoubleTap(prevTap, end)) {
      prevTap = null;
      lastTouchToggle = Date.now();
      e.preventDefault();
      setExpanded(!expanded);
    } else {
      prevTap = end;
    }
  });
  scroller.addEventListener("dblclick", (e) => {
    // A touch double tap may also synthesize dblclick: it already toggled.
    if (Date.now() - lastTouchToggle < 600 || isControl(e.target)) return;
    setExpanded(!expanded);
  });
  // History: "Load earlier" at the top fetches the whole scrollback once.
  // While it is shown and the reader is scrolled up, polls do not replace it;
  // reaching the bottom again drops it for the live tail.
  let history = false;
  let loading = false;
  let lastPane = null;
  let exhausted = false;
  const earlier = h("button", { type: "button", class: "load-earlier", hidden: true }, "Load earlier");
  const syncEarlier = () => {
    earlier.hidden = history || loading || exhausted || scroller.scrollTop > 24 || !canLoadEarlier(lastPane?.text);
  };
  function leaveHistory() {
    history = false;
    if (lastPane) render(lastPane, true);
    syncEarlier();
  }
  scroller.addEventListener("scroll", () => {
    if (atBottom()) {
      pill.hidden = true;
      if (history) leaveHistory();
    }
    syncEarlier();
  });
  pill.addEventListener("click", () => {
    if (history) leaveHistory();
    scroller.scrollTop = scroller.scrollHeight;
    pill.hidden = true;
  });
  earlier.addEventListener("click", async () => {
    if (!loadHistory || loading) return;
    loading = true;
    earlier.textContent = "Loading…";
    try {
      const full = await loadHistory();
      if (!historyAddsLines(lastPane?.text, full.text)) {
        exhausted = true;
        return;
      }
      const before = scroller.scrollHeight - scroller.scrollTop;
      history = true;
      render(full, false);
      // Keep the line that was at the top where it was: the tail is unchanged
      // at the bottom, so hold the distance from the bottom.
      scroller.scrollTop = scroller.scrollHeight - before;
    } catch {
      toast("Could not load earlier output", "error");
    } finally {
      loading = false;
      earlier.textContent = "Load earlier";
      syncEarlier();
    }
  });
  el.append(earlier);

  let statusOpen = loadFlag(STATUS_KEY);
  let statusLines = null;
  let rendered = false;
  // statusBar is Claude Code's chrome below the prompt box: one muted row,
  // tap to show the raw lines.
  function statusBar() {
    const wrap = document.createElement("div");
    wrap.className = "pane-status";
    const toggle = document.createElement("button");
    toggle.type = "button";
    toggle.className = "pane-status-toggle";
    toggle.setAttribute("aria-expanded", String(statusOpen));
    toggle.textContent = statusOpen ? "Claude Code status ▾" : "Claude Code status ▸";
    toggle.addEventListener("click", () => {
      statusOpen = !statusOpen;
      saveFlag(STATUS_KEY, statusOpen);
      wrap.replaceWith(statusBar());
    });
    wrap.append(toggle);
    if (statusOpen) {
      const pre = document.createElement("pre");
      pre.className = "pane-status-raw";
      pre.textContent = statusLines.join("\n");
      wrap.append(pre);
    }
    return wrap;
  }
  return {
    el,
    update(pane) {
      lastPane = pane;
      if (history && !atBottom()) {
        pill.hidden = false;
        return;
      }
      if (history) history = false;
      render(pane, true);
    },
    destroy() {
      setExpanded(false);
    },
  };
  function render(pane, live) {
    const follow = live ? atBottom() : false;
    const frag = document.createDocumentFragment();
    const split = splitPane(pane.text);
    statusLines = split.status ? split.status.lines : null;
    for (const b of split.body) {
      if (b.rule !== undefined) {
        const rule = document.createElement("div");
        rule.className = "pane-rule";
        if (b.rule) {
          const label = document.createElement("span");
          label.textContent = b.rule;
          rule.append(label);
        }
        frag.append(rule);
      } else {
        frag.append(paneLine(b.runs));
      }
    }
    if (split.body.length === 0 && split.prompt?.empty) {
      frag.append(h("div", { class: "pane-empty" }, "At an empty prompt."));
    }
    if (statusLines) frag.append(statusBar());
    scroller.replaceChildren(frag);
    if (follow) scroller.scrollTop = scroller.scrollHeight;
    else if (live && rendered) pill.hidden = false;
    rendered = true;
    syncEarlier();
  }
}

/**
 * exactView is the read-only xterm at the window's real width. The font is
 * scaled so the full width fits (down to EXACT_MIN_FONT, then the view pans
 * sideways); cols are never changed by fitting. Claude Code's footer is
 * dropped and rows are cut to what fits, so no blank band is left.
 */
function exactView() {
  const host = h("div", { class: "pane-exact-host" });
  const el = h("div", { class: "pane-exact" }, host);
  const term = new window.Terminal({
    disableStdin: true,
    convertEol: true,
    fontSize: 12,
    scrollback: 0,
    cursorBlink: false,
    cursorStyle: "underline",
    cursorInactiveStyle: "none",
  });
  term.open(host);

  const cellHeight = () => {
    const screen = host.querySelector(".xterm-screen");
    const hgt = screen ? screen.getBoundingClientRect().height : 0;
    return hgt > 0 && term.rows > 0 ? hgt / term.rows : 0;
  };

  function draw(lines, cols, rows, then) {
    if (term.cols !== cols || term.rows !== rows) term.resize(cols, rows);
    term.write(`\x1b[2J\x1b[3J\x1b[H${lines.join("\n")}`, then);
  }

  return {
    el,
    /** setBackground matches the terminal to the pane it sits in. */
    setBackground(color) {
      if (color) term.options.theme = { ...term.options.theme, background: color };
    },
    update(pane) {
      const lines = paneLinesForExact(pane.text);
      const cols = pane.cols > 0 ? pane.cols : 80;
      const paneRows = pane.rows > 0 ? pane.rows : 24;
      const rowsFor = () => exactRows(lines.length, paneRows, el.clientHeight - 4, cellHeight());
      draw(lines, cols, rowsFor(), () => {
        fitTermFont(term, host, el.clientWidth - 4, EXACT_MIN_FONT, EXACT_MAX_FONT);
        // The font (and so the cell height) may have changed: one more pass.
        const rows = rowsFor();
        if (rows !== term.rows) draw(lines, cols, rows);
      });
    },
    destroy() {
      term.dispose();
    },
  };
}

export function mountWindow(root, ctx, { session, windowId }) {
  const base = windowApiPath(session, windowId);
  const canWrite = ctx.hasFeature("write");
  const canQueue = canWrite && ctx.hasFeature("queue");
  const canCommand = canWrite && ctx.hasFeature("command");
  const canUpload = canWrite && ctx.hasFeature("upload");
  let win = null;
  let waiting = new Map();
  let pane = null;
  let lastText = null;
  let lastActionsKey = null;
  let gone = false;
  // Liveness: when the pane last changed, and since when fetches fail.
  let lastChangeAt = 0;
  let fetchFailingSince = 0;
  // Set by a successful interrupt; the reply box that follows gets focus.
  let interruptedAt = 0;

  const title = h("h1", { class: "win-h1" }, windowId);
  const statePill = h("span", { class: "state-pill" }, h("span", { class: "dot" }), h("span", { class: "state-text" }));
  const where = h("span", { class: "win-where" });
  // Tap the task line to show the full initial prompt (only when it has more).
  const task = h("div", { class: "win-task", hidden: true, onclick: () => {
      if (!taskFull) return;
      task.classList.toggle("open");
      paintTask();
    } });
  let taskCollapsed = "";
  let taskFull = "";
  // paintTask shows the full prompt while open, else the short line. The open
  // class survives polls, and the text is only rewritten when it changed so the
  // scroll position of a long prompt is kept.
  function paintTask() {
    const shown = task.classList.contains("open") && taskFull ? taskFull : taskCollapsed;
    if (task.textContent !== shown) task.textContent = shown;
  }
  const scheduled = h("div", { class: "win-scheduled", hidden: true });
  // Tap the recap to show it in full; it is clamped to two lines.
  const recap = h("div", { class: "win-recap", hidden: true, onclick: () => recap.classList.toggle("open") });
  const chips = h("div", { class: "chips", hidden: true });
  // "◆ N artifacts" when agents published from this window; opens the list filtered to it.
  const artifactChip = h("a", { class: "chip artifact-chip", href: artifactsHash({ session, windowId }), hidden: true });
  let childrenOpen = false;
  const waitingKids = h("div", { class: "win-waiting", hidden: true });
  const head = h("div", { class: "win-head" }, h("div", { class: "win-state" }, statePill, where), task, recap, scheduled, chips, artifactChip, waitingKids);

  const liveChip = h("span", { class: "live-chip", role: "status" });
  const modeBtns = {
    reflow: h("button", { class: "seg", onclick: () => setMode("reflow") }, "Reflow"),
    exact: h("button", { class: "seg", onclick: () => setMode("exact") }, "Exact"),
  };
  const liveSeg = ctx.hasFeature("terminal")
    ? h("a", { class: "seg", href: terminalHash(session, windowId) }, "Live")
    : null;
  const paneBar = h("div", { class: "pane-bar" }, liveChip, h("div", { class: "segmented" }, modeBtns.reflow, modeBtns.exact, liveSeg));
  const exactHint = h(
    "div",
    { class: "exact-hint", hidden: true },
    h("p", {}, "Exact shows the terminal at its real width. Pan sideways for long lines."),
    h(
      "button",
      {
        type: "button",
        class: "btn-text",
        onclick: () => {
          saveFlag(EXACT_HINT_KEY, true);
          exactHint.hidden = true;
        },
      },
      "Got it",
    ),
  );
  const paneHost = h("div", { class: "pane" });
  const answerHost = h("div", { class: "answer-host" });
  const actions = h("div", { class: "actions" });
  const goneHost = h(
    "div",
    { class: "gone", hidden: true },
    h("p", { class: "muted" }, "This window was closed."),
    h("a", { class: "btn", href: "#/" }, "Back to Fleet"),
  );
  const closeBtn = h(
    "button",
    {
      type: "button",
      class: "icon-btn close-window",
      "aria-label": "Close window",
      hidden: !canWrite,
      onclick: () =>
        run(
          { title: "Close window?", lines: [win?.label || win?.agent || windowId], confirmLabel: "Close", danger: true },
          { method: "POST", path: `${base}/close`, body: {} },
          () => {
            // Leave for Fleet; the hashchange tears this view down. Until then
            // refresh() is a no-op so the closed window is not re-fetched.
            disposed = true;
            location.hash = "#/";
          },
        ),
    },
    svgIcon(CLOSE_SVG),
  );
  const approvalHost = h("div", { class: "window-approvals" });
  let approvalCountdowns = [];
  let approvalKey = null;
  root.append(
    h(
      "div",
      { class: "window-view" },
      h("header", { class: "bar" }, h("a", { class: "back-link", href: "#/" }, "‹ Fleet"), title, closeBtn),
      head,
      approvalHost,
      answerHost,
      paneBar,
      exactHint,
      paneHost,
      goneHost,
      actions,
    ),
  );

  let mode = loadMode();
  let view = null;
  // Set when the view is leaving (after a close, or on teardown): refreshes
  // become no-ops so nothing renders or toasts against a dead view.
  let disposed = false;

  function mountView() {
    view?.destroy();
    clear(paneHost);
    view = mode === "exact" ? exactView() : reflowView({ loadHistory: () => get(`${base}/pane?lines=all`) });
    paneHost.append(view.el);
    view.setBackground?.(getComputedStyle(paneHost).backgroundColor);
    for (const [m, btn] of Object.entries(modeBtns)) btn.setAttribute("aria-pressed", String(m === mode));
    exactHint.hidden = gone || mode !== "exact" || loadFlag(EXACT_HINT_KEY);
    if (pane) view.update(pane);
  }

  function setMode(next) {
    if (next === mode) return;
    mode = next;
    saveMode(mode);
    mountView();
  }
  mountView();

  function renderHeader() {
    if (!win) return;
    // The fetched detail can lag the fleet feed; a newer title or role from the feed wins.
    const live = ctx.store.fleet?.groups?.find((g) => g.session === session)?.windows?.find((x) => x.window_id === windowId);
    const dt = detailTitle(live ? { ...win, title: live.title, role: live.role } : win, windowId);
    clear(title);
    if (dt.role) title.append(h("span", { class: "tag kind-chip", title: "Role of this sub-agent" }, dt.role));
    title.append(h("span", { class: "h1-text" }, dt.text));
    waiting = waitingIndex(ctx.store.fleet?.groups);
    const st = displayState(win, waiting);
    statePill.className = `state-pill ${st}`;
    statePill.querySelector(".state-text").textContent = stateLabel(win, Date.now(), waiting);
    clear(waitingKids);
    const groups = ctx.store.fleet?.groups;
    const link = (k, cls) => {
      const ks = displayState(k, waiting);
      const role = rowRole(k);
      return h(
        "a",
        { class: `waiting-child ${cls} ${ks}`, href: windowHash(k.session, k.window_id) },
        cls === "parent-link" ? h("span", { class: "rel-up", title: "Parent", "aria-label": "Parent" }, "↑") : null,
        h("span", { class: "glyph", "aria-label": ks }, CHILD_GLYPH[ks]),
        role ? h("span", { class: "tag kind-chip", title: "Role of this sub-agent" }, role) : null,
        h("span", { class: "link-text" }, rowHeadline(k)),
      );
    };
    const par = parentOf(groups, win);
    if (par) waitingKids.append(link(par, "parent-link"));
    const kids = childrenOf(groups, win);
    if (kids.length > 0) {
      const shown = childrenOpen ? kids : kids.slice(0, CHILD_CAP);
      waitingKids.append(h("div", { class: "rel-label" }, st === "waiting" ? "Waiting on" : "Sub-agents"));
      for (const k of shown) waitingKids.append(link(k, "child-link"));
      if (kids.length > CHILD_CAP) {
        waitingKids.append(
          h(
            "button",
            {
              type: "button",
              class: "rel-more",
              onclick: () => {
                childrenOpen = !childrenOpen;
                renderHeader();
              },
            },
            childrenOpen ? "Show fewer" : `${kids.length - CHILD_CAP} more`,
          ),
        );
      }
    }
    waitingKids.hidden = waitingKids.childElementCount === 0;
    where.textContent = [projectAgent(win.project, dt.agent), win.branch].filter(Boolean).join(" · ");
    const { task: line, recap: summary } = headerLines(win);
    taskCollapsed = line;
    taskFull = fullPrompt(win, line);
    task.classList.toggle("expandable", !!taskFull);
    if (!taskFull) task.classList.remove("open");
    paintTask();
    task.hidden = !line;
    recap.textContent = summary;
    recap.hidden = !summary;
    const sched = scheduledLine(win);
    scheduled.textContent = sched;
    scheduled.hidden = !sched;
    clear(chips);
    for (const c of headerChips(win, safeHttpsUrl)) {
      chips.append(
        c.href
          ? h("a", { class: `chip ${c.kind}`, href: c.href, target: "_blank", rel: "noopener noreferrer" }, c.text)
          : h("span", { class: `chip ${c.kind}` }, c.text),
      );
    }
    chips.hidden = chips.childElementCount === 0;
  }

  function offlineSince() {
    const down = ctx.store.downSince;
    const feedDown = down && Date.now() - down > FEED_DOWN_MS ? down : 0;
    if (fetchFailingSince && feedDown) return Math.min(fetchFailingSince, feedDown);
    return fetchFailingSince || feedDown;
  }

  function renderLiveness() {
    if (gone) {
      liveChip.textContent = "";
      return;
    }
    const l = liveness({ lastChangeAt, offlineSince: offlineSince() }, Date.now());
    liveChip.textContent = l.text;
    liveChip.className = `live-chip ${l.kind}`;
  }

  function showGone() {
    title.textContent = "Window closed";
    clear(actions);
    clear(answerHost);
    lastActionsKey = null;
    closeBtn.hidden = true;
    paneBar.hidden = true;
    exactHint.hidden = true;
    goneHost.hidden = false;
  }

  // run confirms (skipped when confirm is null; reply and queue sends pass null), sends, and on failure toasts; it always refreshes (a 409
  // means the view was stale). onOk runs before that refresh. Returns
  // whether the request succeeded.
  async function run(confirm, req, onOk) {
    if (confirm && !(await confirmSheet(confirm))) return false;
    let ok = false;
    try {
      // req may be a function, for requests that first upload attachments.
      await send(typeof req === "function" ? await req() : req);
      ok = true;
      toast("Sent");
      onOk?.();
    } catch (e) {
      toast(e instanceof ApiError ? e.message : String(e), "error");
    }
    await refresh();
    return ok;
  }

  // Local tick state for a multi-select question, seeded from the pane's
  // [x] marks and kept across polls while the same dialog (hash) is up.
  let multiSel = { hash: "", keys: new Set() };

  // True while an answer request is in flight: further taps are ignored and
  // the choice buttons are disabled, so one tap cannot send two keys.
  let answering = false;
  async function sendAnswer(confirm, req) {
    if (answering) return;
    answering = true;
    const btns = [...document.querySelectorAll(".answer button.choice")];
    if (!confirm) btns.forEach((b) => (b.disabled = true));
    try {
      await run(confirm, req);
    } finally {
      answering = false;
      btns.forEach((b) => (b.disabled = false));
    }
  }

  function multiAnswer(dialog, answer) {
    const choices = checkboxChoices(dialog);
    if (multiSel.hash !== dialog.hash) {
      multiSel = { hash: dialog.hash, keys: new Set(choices.filter((c) => c.checked).map((c) => c.key)) };
    }
    const submit = () => {
      let req;
      try {
        req = buildAnswerRequest(session, windowId, dialog, "submit", [...multiSel.keys]);
      } catch (e) {
        toast(e.message, "error");
        return;
      }
      sendAnswer(null, req);
    };
    return [
      h(
        "div",
        { class: "choices" },
        ...choices.map((c) => {
          const box = h("input", { type: "checkbox" });
          box.checked = multiSel.keys.has(c.key);
          box.onchange = () => (box.checked ? multiSel.keys.add(c.key) : multiSel.keys.delete(c.key));
          return h("label", { class: "btn choice" }, box, ` ${c.label}`);
        }),
        dialog.submit
          ? h("button", { class: "btn choice primary", onclick: submit }, dialog.submit.label || "Submit")
          : h("p", { class: "note" }, "Cannot find the submit row; finish this question on the terminal."),
        h("button", { class: "btn choice", onclick: () => answer("esc") }, "Dismiss (Esc)"),
      ),
    ];
  }

  function answerSection() {
    const dialog = pane.dialog;
    const recommended = focusedOption(pane.text, dialog) || dialog.options?.[0]?.key || "";
    const answer = (key) => {
      let req;
      try {
        req = buildAnswerRequest(session, windowId, dialog, key);
      } catch (e) {
        toast(e.message, "error");
        return;
      }
      // Dismiss cancels a whole AskUserQuestion set, so it still confirms.
      sendAnswer(
        key === "esc" ? { title: "Dismiss the dialog (Esc)?", lines: [dialog.text], confirmLabel: "Send" } : null,
        req,
      );
    };
    if (dialog.multi) {
      return h("section", { class: "answer" }, h("p", { class: "question" }, dialogQuestion(dialog)), ...multiAnswer(dialog, answer));
    }
    return h(
      "section",
      { class: "answer" },
      h("p", { class: "question" }, dialogQuestion(dialog)),
      h(
        "div",
        { class: "choices" },
        ...(dialog.options ?? []).map((o) =>
          h(
            "button",
            { class: `btn choice${o.key === recommended ? " primary" : ""}`, onclick: () => answer(o.key) },
            `${o.key}. ${o.label}`,
          ),
        ),
        h("button", { class: "btn choice", onclick: () => answer("esc") }, "Dismiss (Esc)"),
      ),
    );
  }

  // attachments is the composer's image picker (attach.js); null when the
  // server cannot take uploads.
  function attachments({ icon = false } = {}) {
    if (!canUpload) return null;
    const picker = imagePicker({ icon });
    return {
      controls: picker.controls,
      trigger: picker.trigger,
      count: picker.count,
      // upload stores the files and returns their IDs.
      upload: () => (picker.count() ? uploadImages(buildUploadPath(session, windowId), picker.files()) : Promise.resolve([])),
      clear: picker.clear,
    };
  }

  // composer is the chat-style row shared by the reply and queue boxes:
  // thumbnails above one row of camera button, auto-growing textarea and a
  // round send button. Tapping send sends at once, with no confirm sheet;
  // while a send (including the image upload) is in flight further taps are
  // ignored and the button is disabled. buildRequest turns the trimmed text
  // and uploaded image IDs into the request. On success the text and images
  // are cleared; on failure they are kept so nothing typed is lost.
  // With commands set (Claude windows, reply box), a text starting with "/"
  // turns the send button into "Run command", lists matching built-ins to
  // tap, and sends through the command action instead of reply.
  function composer({ placeholder, sendLabel, buildRequest, commands = false }) {
    const att = attachments({ icon: true });
    const text = h("textarea", {
      rows: "1",
      class: "composer-text",
      placeholder,
      maxlength: "4000",
      oninput: () => {
        grow(text);
        refreshCommandMode();
      },
    });
    let sending = false;
    const suggest = h("div", { class: "cmd-suggest", hidden: "" });
    function refreshCommandMode() {
      if (!commands) return;
      const cmd = isSlashCommand(text.value);
      send.classList.toggle("command", cmd);
      send.setAttribute("aria-label", cmd ? "Run command" : sendLabel);
      send.title = cmd ? "Run command" : "";
      clear(suggest);
      const list = commandSuggestions(text.value);
      suggest.hidden = list.length === 0;
      for (const c of list) {
        suggest.append(
          h(
            "button",
            {
              type: "button",
              class: "cmd-suggest-item",
              onclick: () => {
                text.value = `${c.name} `;
                grow(text);
                text.focus();
                refreshCommandMode();
              },
            },
            h("b", {}, c.name),
            h("span", { class: "muted" }, ` ${c.hint}`),
          ),
        );
      }
    }
    const send = h(
      "button",
      {
        type: "button",
        class: "icon-btn send",
        "aria-label": sendLabel,
        onclick: async () => {
          if (sending) return;
          const value = text.value.trim();
          const n = att ? att.count() : 0;
          if (!value && !n) return;
          const cmd = commands && isSlashCommand(value);
          if (cmd) {
            const bad = n ? "Commands cannot have images." : commandError(value);
            if (bad) {
              toast(bad, "error");
              return;
            }
          }
          sending = true;
          send.disabled = true;
          try {
            await run(
              cmd ? commandConfirm(value) : null,
              cmd ? buildCommandRequest(session, windowId, value) : async () => buildRequest(value, att ? await att.upload() : []),
              () => {
                text.value = "";
                text.style.height = "";
                att?.clear();
                refreshCommandMode();
              },
            );
          } finally {
            sending = false;
            send.disabled = false;
          }
        },
      },
      svgIcon(SEND_SVG),
    );
    const row = h("div", { class: "composer" }, ...(att ? att.trigger : []), text, send);
    return { text, nodes: [...(att ? att.controls : []), suggest, row] };
  }

  function replyBox() {
    return composer({
      placeholder: "Reply to the agent…",
      sendLabel: "Send reply",
      buildRequest: (value, ids) => buildReplyRequest(session, windowId, value, ids),
      commands: canCommand && isClaudeWindow(win),
    });
  }

  function grow(el) {
    el.style.height = "auto";
    el.style.height = `${el.scrollHeight}px`;
  }

  function interruptButton() {
    return h(
      "button",
      {
        class: "btn small danger",
        onclick: () =>
          run(
            { title: "Interrupt agent?", lines: ["Sends Escape to the running agent."], confirmLabel: "Interrupt", danger: true },
            { method: "POST", path: `${base}/interrupt`, body: {} },
            () => {
              interruptedAt = Date.now();
            },
          ),
      },
      "Interrupt",
    );
  }

  function queueBox() {
    const box = composer({
      placeholder: "Queue a message for the agent…",
      sendLabel: "Queue message",
      buildRequest: (value, ids) => buildQueueRequest(session, windowId, value, ids),
    });
    return [h("div", { class: "row end" }, interruptButton()), ...box.nodes];
  }

  function renderActions() {
    if (!win || gone) return;
    if (!canWrite) {
      clear(actions);
      actions.append(h("p", { class: "muted" }, "Read-only: write actions are not enabled on this server."));
      return;
    }
    const kind = actionsWithQueue(win, pane, canQueue);
    // Do not rebuild while the user may be typing: only when the kind or dialog changes.
    const key = `${kind}:${pane?.dialog?.hash ?? ""}`;
    if (key === lastActionsKey) return;
    lastActionsKey = key;
    clear(actions);
    clear(answerHost);
    if (kind === "answer") answerHost.append(answerSection());
    else if (kind === "reply") {
      const { text, nodes } = replyBox();
      actions.append(...nodes);
      if (interruptedAt && Date.now() - interruptedAt < INTERRUPT_FOLLOWUP_MS) {
        text.focus();
        toast("Interrupted. Tell it what to do next.");
      }
    } else if (kind === "queue") actions.append(...queueBox());
    else if (kind === "interrupt") actions.append(h("div", { class: "row end" }, interruptButton()));
    if (kind !== "interrupt" && kind !== "queue") interruptedAt = 0;
  }

  // fetchFailed records a failed fetch: 404 means the window is gone, anything
  // else (network, 5xx) that the laptop cannot be reached right now.
  function fetchFailed(e) {
    if (e instanceof ApiError && e.status === 404) gone = true;
    else if (!fetchFailingSince) fetchFailingSince = Date.now();
  }

  async function refreshPane() {
    try {
      pane = await get(`${base}/pane?lines=${PANE_TAIL_LINES}`);
    } catch (e) {
      fetchFailed(e);
      return false;
    }
    const key = `${pane.cols}x${pane.rows}\n${pane.text}`;
    if (key !== lastText) {
      if (lastText !== null) lastChangeAt = Date.now();
      lastText = key;
      view.update(pane);
    }
    return true;
  }

  async function refreshWindow() {
    try {
      win = await get(base);
    } catch (e) {
      fetchFailed(e);
      return false;
    }
    // Until the pane is seen to change, the window's last recorded activity
    // is the best guess at when it last did.
    const recorded = Date.parse(win.detail?.last_activity ?? "");
    if (!Number.isNaN(recorded) && recorded > lastChangeAt) lastChangeAt = Math.min(recorded, Date.now());
    return true;
  }

  async function refresh() {
    if (gone || disposed) return;
    const [okWin, okPane] = await Promise.all([refreshWindow(), refreshPane()]);
    if (disposed) return;
    if (gone) {
      showGone();
      return;
    }
    if (okWin && okPane) fetchFailingSince = 0;
    renderHeader();
    renderActions();
    renderLiveness();
  }

  // The window's own pending approval, shown in full above the pane.
  function renderApprovals() {
    const mine = approvalsForWindow(ctx.store.approvals, session, windowId);
    const key = mine.map((a) => a.id).join(",");
    if (key === approvalKey) return;
    approvalKey = key;
    clear(approvalHost);
    approvalCountdowns = [];
    for (const a of mine) approvalHost.append(approvalCard(ctx, a, approvalCountdowns));
  }
  renderApprovals();
  // One 1s tick: approval countdowns, the liveness chip and the state age.
  const tick = setInterval(() => {
    tickCountdowns(approvalCountdowns);
    if (document.hidden || gone) return;
    renderLiveness();
    if (win) statePill.querySelector(".state-text").textContent = stateLabel(win, Date.now(), waiting);
  }, 1000);

  refresh();
  const timer = setInterval(() => {
    if (!document.hidden && !gone) refresh();
  }, PANE_REFRESH_MS);
  const onVisible = () => !document.hidden && refresh();
  document.addEventListener("visibilitychange", onVisible);
  async function loadArtifactChip() {
    if (!ctx.hasFeature("artifacts")) return;
    try {
      const n = artifactsForWindow((await get("/v1/artifacts")).artifacts, session, windowId).length;
      if (disposed) return;
      artifactChip.textContent = artifactChipText(n);
      artifactChip.hidden = n === 0;
    } catch {
      // the chip is a convenience; the next artifact event retries
    }
  }
  loadArtifactChip();

  const unsub = ctx.store.subscribe((ev) => {
    if (ev?.type === "artifact") loadArtifactChip();
    if (ev?.type === "window" && ev.win.session === session && ev.win.window_id === windowId) refresh();
    if (ev?.type === "gone") refresh();
    if (ev?.type === "fleet" || ev?.type === "window" || ev?.type === "gone") renderHeader();
    if (ev?.type === "status") {
      renderLiveness();
      // Back online: fetch at once rather than on the next poll.
      if (!ctx.store.downSince) refresh();
    }
    if (ev?.type === "approvals") renderApprovals();
  });

  return () => {
    disposed = true;
    clearInterval(timer);
    clearInterval(tick);
    unsub();
    document.removeEventListener("visibilitychange", onVisible);
    view.destroy();
  };
}
