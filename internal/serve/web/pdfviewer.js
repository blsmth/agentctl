// The artifact PDF viewer: a full-screen overlay that fetches the revision's
// PDF from the Device route and draws its pages with PDF.js (bundled under
// /vendor/pdfjs, loaded on first use). iOS's own viewer shows only the first
// page of a PDF in an iframe, hence the pages are drawn here.
import { ApiError, getBlob } from "./api.js";
import {
  artifactPdfPath,
  pdfCanvasDensity,
  pdfClampZoom,
  pdfDelivery,
  pdfFileProblem,
  pdfFilename,
  pdfFitScale,
  pdfPageCountLabel,
  pdfPagesToDraw,
  pdfSharePayload,
} from "./logic.js";
import { h, toast } from "./ui.js";

const PDFJS_URL = "/vendor/pdfjs/pdf.min.mjs";
const PDFJS_WORKER = "/vendor/pdfjs/pdf.worker.min.mjs";

let pdfjsPromise = null;

/** loadPdfjs imports PDF.js once, the first time a viewer opens. */
function loadPdfjs() {
  pdfjsPromise ??= import(PDFJS_URL).then((m) => {
    m.GlobalWorkerOptions.workerSrc = PDFJS_WORKER;
    return m;
  });
  // A failed import (offline, first run) must be retried by the next open.
  pdfjsPromise.catch(() => (pdfjsPromise = null));
  return pdfjsPromise;
}

const errText = (e) => (e instanceof Error ? e.message : String(e));

/**
 * openPdfViewer shows revision rev of artifact id in the overlay and returns
 * {close}. onGone is called (after the overlay closes) when the server says the
 * artifact no longer exists.
 */
export function openPdfViewer({ id, rev, title, onGone }) {
  const state = { ctl: null, doc: null, loading: null, file: null, closed: false, pushed: false, zoom: 1 };
  const pages = []; // {el, canvas, task, drawing, w, h}
  let visible = new Set();
  let observer = null;
  let resizeObserver = null;
  let elapsedTimer = 0;
  let relayoutTimer = 0;
  let baseW = 0; // page 1 width in points
  let baseH = 0;

  const countEl = h("span", { class: "pdfv-count muted" });
  const shareBtn = h("button", { class: "btn pdfv-share", type: "button", hidden: true, onclick: () => deliver() }, "Share");
  const closeBtn = h("button", { class: "btn pdfv-close", type: "button", "aria-label": "Close PDF", onclick: () => close() }, "Close");
  const bar = h(
    "header",
    { class: "pdfv-bar" },
    h("div", { class: "pdfv-heading" }, h("strong", { class: "pdfv-title" }, title || "PDF"), countEl),
    shareBtn,
    closeBtn,
  );
  const status = h("div", { class: "pdfv-status", role: "status" });
  const pagesEl = h("div", { class: "pdfv-pages" });
  const scroll = h("div", { class: "pdfv-scroll" }, pagesEl);
  const root = h(
    "div",
    { class: "pdfv", role: "dialog", "aria-modal": "true", "aria-label": `PDF: ${title || "artifact"}` },
    bar,
    h("div", { class: "pdfv-body" }, scroll, status),
  );

  // ---- status ----

  function showProgress() {
    const started = Date.now();
    const text = h("p", {}, "Rendering the PDF…");
    status.replaceChildren(h("div", { class: "pdfv-spinner", "aria-hidden": "true" }), text);
    status.hidden = false;
    status.setAttribute("aria-busy", "true");
    clearInterval(elapsedTimer);
    elapsedTimer = setInterval(() => {
      const s = Math.round((Date.now() - started) / 1000);
      text.textContent = s >= 8 ? `Rendering the PDF… ${s}s` : "Rendering the PDF…";
    }, 1000);
  }

  function hideStatus() {
    clearInterval(elapsedTimer);
    status.hidden = true;
    status.removeAttribute("aria-busy");
    status.replaceChildren();
  }

  function showError(message) {
    clearInterval(elapsedTimer);
    status.removeAttribute("aria-busy");
    status.replaceChildren(
      h("p", { class: "pdfv-error" }, message),
      h("button", { class: "btn pdfv-retry", type: "button", onclick: () => load() }, "Retry"),
    );
    status.hidden = false;
  }

  // ---- loading ----

  async function load() {
    state.ctl?.abort();
    const ctl = (state.ctl = new AbortController());
    freePages();
    pagesEl.replaceChildren();
    shareBtn.hidden = true;
    countEl.textContent = "";
    showProgress();
    const stale = () => state.closed || ctl.signal.aborted;
    try {
      // The import runs while the server renders.
      const lib = loadPdfjs();
      const { blob, disposition } = await getBlob(artifactPdfPath(id, rev), { signal: ctl.signal });
      if (stale()) return;
      // The File's type is what the server sent, so a non-PDF answer (a
      // captive portal's HTML) is caught here.
      const type = (blob.type || "").split(";")[0].trim().toLowerCase();
      const file = new File([blob], pdfFilename(disposition, `artifact-r${rev}.pdf`), { type });
      const problem = pdfFileProblem(file, new Uint8Array(await file.slice(0, 5).arrayBuffer()));
      if (problem) throw new Error(problem);
      const pdfjs = await lib;
      if (stale()) return;
      const loading = pdfjs.getDocument({ data: new Uint8Array(await file.arrayBuffer()) });
      state.loading = loading;
      const doc = await loading.promise;
      if (stale()) {
        loading.destroy();
        return;
      }
      state.file = file;
      state.doc = doc;
      await layout(doc);
      if (stale()) return;
      hideStatus();
      countEl.textContent = pdfPageCountLabel(doc.numPages);
      shareBtn.textContent = pdfDelivery(navigator, file) === "share" ? "Share" : "Download";
      shareBtn.hidden = false;
    } catch (e) {
      if (stale()) return;
      if (e instanceof ApiError && e.status === 404) {
        close();
        onGone?.();
        return;
      }
      showError(`Could not show the PDF: ${errText(e)}`);
    }
  }

  /** layout makes one placeholder per page, sized from page 1, and starts watching which are on screen. */
  async function layout(doc) {
    const first = await doc.getPage(1);
    const vp = first.getViewport({ scale: 1 });
    baseW = vp.width;
    baseH = vp.height;
    first.cleanup();
    for (let i = 0; i < doc.numPages; i++) {
      const el = h("div", { class: "pdfv-page", "data-page": String(i + 1) });
      pages.push({ el, canvas: null, task: null, drawing: false, w: baseW, h: baseH });
      pagesEl.append(el);
    }
    sizePages();
    observer = new IntersectionObserver(
      (entries) => {
        for (const e of entries) {
          const i = Number(e.target.dataset.page) - 1;
          if (e.isIntersecting) visible.add(i);
          else visible.delete(i);
        }
        sync();
      },
      // Draw a little before a page scrolls in.
      { root: scroll, rootMargin: "50% 0px" },
    );
    for (const p of pages) observer.observe(p.el);
  }

  /** availWidth is the CSS width a fitted page may use. */
  const availWidth = () => Math.max(0, scroll.clientWidth - 16);

  /** sizePages sets every placeholder to the fitted (and zoomed) size. */
  function sizePages() {
    const w = availWidth() * state.zoom;
    for (const p of pages) {
      p.el.style.width = `${w}px`;
      p.el.style.height = `${(w * p.h) / p.w}px`;
    }
  }

  // ---- drawing ----

  /** sync draws the pages near the screen and frees the others. */
  function sync() {
    if (!state.doc || state.closed) return;
    const want = pdfPagesToDraw(visible, pages.length, 1);
    pages.forEach((p, i) => {
      if (want.has(i)) draw(i);
      else free(p);
    });
  }

  async function draw(i) {
    const p = pages[i];
    if (p.canvas || p.drawing || !state.doc) return;
    p.drawing = true;
    const doc = state.doc;
    try {
      const page = await doc.getPage(i + 1);
      const natural = page.getViewport({ scale: 1 });
      if (natural.width !== p.w || natural.height !== p.h) {
        p.w = natural.width;
        p.h = natural.height;
        sizePages();
      }
      const cssW = availWidth() * state.zoom;
      const vp = page.getViewport({ scale: pdfFitScale(natural.width, cssW) });
      const density = pdfCanvasDensity(window.devicePixelRatio || 1, vp.width, vp.height);
      const canvas = h("canvas", { class: "pdfv-canvas" });
      canvas.width = Math.floor(vp.width * density);
      canvas.height = Math.floor(vp.height * density);
      const task = page.render({
        canvas,
        canvasContext: canvas.getContext("2d"),
        viewport: vp,
        transform: density === 1 ? null : [density, 0, 0, density, 0, 0],
        background: "#ffffff",
      });
      p.task = task;
      await task.promise;
      p.task = null;
      page.cleanup();
      if (state.closed || state.doc !== doc || !stillWanted(i)) {
        canvas.width = canvas.height = 0;
        return;
      }
      p.canvas = canvas;
      p.el.replaceChildren(canvas);
    } catch (e) {
      // A cancelled render is a freed page; anything else leaves the page blank.
      if (e?.name !== "RenderingCancelledException" && !state.closed) console.warn("pdf page", i + 1, e);
    } finally {
      p.drawing = false;
      p.task = null;
    }
  }

  /** want reports whether page i should still hold a canvas (the view may have moved on while it drew). */
  const stillWanted = (i) => pdfPagesToDraw(visible, pages.length, 1).has(i);

  /** free drops a page's canvas and cancels its render; the bitmap goes back to the browser at once. */
  function free(p) {
    p.task?.cancel();
    if (p.canvas) {
      // iOS keeps canvas memory until the element is collected: zero it.
      p.canvas.width = p.canvas.height = 0;
      p.canvas = null;
      p.el.replaceChildren();
    }
  }

  function freePages() {
    observer?.disconnect();
    observer = null;
    visible = new Set();
    for (const p of pages) free(p);
    pages.length = 0;
    // Destroying the loading task also stops the worker's work on the document.
    state.loading?.destroy();
    state.loading = null;
    state.doc = null;
  }

  /** relayout re-fits every page (rotation, window resize) and redraws the ones on screen. */
  function relayout() {
    clearTimeout(relayoutTimer);
    relayoutTimer = setTimeout(() => {
      if (!state.doc) return;
      sizePages();
      for (const p of pages) free(p);
      sync();
    }, 120);
  }

  // ---- zoom ----

  /**
   * setZoom applies zoom z (1 = fit to width) keeping the point (cx, cy), in
   * scroll-viewport pixels, where it is; pages are redrawn at the new size so
   * the text is sharp rather than a stretched bitmap.
   */
  function setZoom(z, cx = scroll.clientWidth / 2, cy = scroll.clientHeight / 2) {
    z = pdfClampZoom(z);
    if (z === state.zoom || !state.doc) return;
    const ratio = z / state.zoom;
    const x = scroll.scrollLeft + cx;
    const y = scroll.scrollTop + cy;
    state.zoom = z;
    pagesEl.style.transform = "";
    sizePages();
    scroll.scrollLeft = x * ratio - cx;
    scroll.scrollTop = y * ratio - cy;
    for (const p of pages) free(p);
    sync();
  }

  let pinch = null; // {d0, zoom0, cx, cy, live}
  const dist = (t) => Math.hypot(t[0].clientX - t[1].clientX, t[0].clientY - t[1].clientY);
  scroll.addEventListener(
    "touchstart",
    (e) => {
      if (e.touches.length !== 2) return;
      const r = scroll.getBoundingClientRect();
      pinch = {
        d0: dist(e.touches),
        zoom0: state.zoom,
        cx: (e.touches[0].clientX + e.touches[1].clientX) / 2 - r.left,
        cy: (e.touches[0].clientY + e.touches[1].clientY) / 2 - r.top,
        live: state.zoom,
      };
    },
    { passive: true },
  );
  scroll.addEventListener(
    "touchmove",
    (e) => {
      if (!pinch || e.touches.length !== 2) return;
      e.preventDefault();
      pinch.live = pdfClampZoom(pinch.zoom0 * (dist(e.touches) / pinch.d0));
      // While the fingers move, stretch what is drawn; the redraw comes on release.
      pagesEl.style.transformOrigin = `${scroll.scrollLeft + pinch.cx}px ${scroll.scrollTop + pinch.cy}px`;
      pagesEl.style.transform = `scale(${pinch.live / state.zoom})`;
    },
    { passive: false },
  );
  const endPinch = (e) => {
    if (!pinch || e.touches.length >= 2) return;
    const p = pinch;
    pinch = null;
    pagesEl.style.transform = "";
    setZoom(p.live, p.cx, p.cy);
  };
  scroll.addEventListener("touchend", endPinch);
  scroll.addEventListener("touchcancel", endPinch);
  // Ctrl/Cmd + wheel (and a trackpad pinch) on a laptop.
  let wheelTimer = 0;
  let wheelZoom = 0;
  scroll.addEventListener(
    "wheel",
    (e) => {
      if (!e.ctrlKey && !e.metaKey) return;
      e.preventDefault();
      wheelZoom = (wheelZoom || state.zoom) * Math.exp(-e.deltaY * 0.01);
      const r = scroll.getBoundingClientRect();
      const [cx, cy] = [e.clientX - r.left, e.clientY - r.top];
      clearTimeout(wheelTimer);
      wheelTimer = setTimeout(() => {
        const z = wheelZoom;
        wheelZoom = 0;
        setZoom(z, cx, cy);
      }, 150);
    },
    { passive: false },
  );

  // ---- share / download ----

  async function deliver() {
    const file = state.file;
    if (!file) return;
    if (pdfDelivery(navigator, file) === "share") {
      try {
        // Files only: a title next to them makes iOS share text.
        await navigator.share(pdfSharePayload(file));
      } catch (e) {
        if (e?.name === "AbortError") return; // dismissed the share sheet
        toast(`Could not share the PDF: ${errText(e)}`, "error");
      }
      return;
    }
    const url = URL.createObjectURL(file);
    const a = h("a", { href: url, download: file.name, hidden: true });
    document.body.append(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 60000);
  }

  // ---- open / close ----

  const onKey = (e) => {
    if (e.key === "Escape") close();
  };
  // The back gesture closes the viewer, not the artifact page behind it.
  const onPop = () => {
    state.pushed = false;
    teardown();
  };
  const onNav = () => teardown();

  function teardown() {
    if (state.closed) return;
    state.closed = true;
    root.remove();
    state.ctl?.abort();
    clearInterval(elapsedTimer);
    clearTimeout(relayoutTimer);
    clearTimeout(wheelTimer);
    resizeObserver?.disconnect();
    removeEventListener("keydown", onKey);
    removeEventListener("popstate", onPop);
    removeEventListener("hashchange", onNav);
    removeEventListener("orientationchange", relayout);
    freePages();
    state.file = null;
    document.body.classList.remove("pdfv-open");
  }

  function close() {
    if (state.closed) return;
    if (state.pushed && history.state?.pdfv) {
      // popstate runs teardown.
      state.pushed = false;
      history.back();
      // Safety net should popstate not arrive.
      setTimeout(teardown, 400);
      return;
    }
    teardown();
  }

  document.body.append(root);
  document.body.classList.add("pdfv-open");
  history.pushState({ ...(history.state ?? {}), pdfv: 1 }, "");
  state.pushed = true;
  addEventListener("keydown", onKey);
  addEventListener("popstate", onPop);
  addEventListener("hashchange", onNav);
  addEventListener("orientationchange", relayout);
  let lastW = 0;
  resizeObserver = new ResizeObserver(() => {
    const w = scroll.clientWidth;
    if (w === lastW) return;
    lastW = w;
    if (state.doc) relayout();
  });
  resizeObserver.observe(scroll);
  closeBtn.focus({ preventScroll: true });
  load();

  return { close };
}
