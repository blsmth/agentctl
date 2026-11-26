// Schedules screen: list, job card and create/edit form for `agentctl cron`
// jobs. The server (internal/cronedit) owns validation, the schedule picker's
// model and the previews; this file only renders them.
import { ApiError, api, get } from "./api.js";
import { launchPicker } from "./dispatch.js";
import {
  advancedSummary, cardLastFailure, cardStatus, defaultAgentFor, dirName, fieldErrors, formClientErrors, formatAgo, formatClock, formatNextFire,
  newScheduleForm, previewBody, runResultCard, runResultLabel, scheduleAgentText, scheduleBanners, scheduleBody, scheduleException, scheduleGlyph, schedulePromptSource,
  scheduleHash, scheduleLastText, scheduleReview, scheduleRowActivity, scheduleScheduleText, scheduleWhen, templateLabel, templatesPath,
  TIMEOUT_PRESETS, tildePath, timeoutIsPreset, usageOptions, windowHash, worktreeFact,
} from "./logic.js";
import { attachSwipe } from "./swipe.js";
import { actionToast, clear, confirmSheet, h, toast } from "./ui.js";

const POLL_MS = 20000;
const SLOW_SAVE_MS = 4000;
// Preview is a write route (rate limited, audited): wait for a typing pause.
const PREVIEW_DEBOUNCE_MS = 1000;
const enc = encodeURIComponent;

export function mountSchedules(root, ctx, route) {
  const write = ctx.hasFeature("write");
  const view = route.view ?? "list";
  if (view === "list") return mountList(root, ctx, write);
  if (view === "new") return mountForm(root, ctx, { job: "", copy: false });
  if (view === "edit" || view === "copy") return mountForm(root, ctx, { job: route.job, copy: view === "copy" });
  return mountCard(root, ctx, route.job, write);
}

// ---- shared bits ----

const post = (path, body) => api("POST", path, body ?? {});

function header(title, back) {
  return h("header", { class: "bar" }, h("a", { class: "btn", href: back.href }, back.label), h("h1", {}, title));
}

const errText = (e) => (e instanceof ApiError ? e.message : e instanceof Error ? e.message : String(e));

/** runNow confirms, runs a job with plain `cron run` semantics and reports fired or the skip reason. */
async function runNow(st, after) {
  const name = st.name;
  const ok = await confirmSheet({
    title: `Run ${name} now?`,
    lines: [`Runs ${scheduleAgentText(st.job)} in ${tildePath(st.job.dir)} unattended.\nOverlap, the concurrency limit and the usage gate still apply; nothing is forced.`],
    confirmLabel: "Run now",
  });
  if (!ok) return;
  toast(`Running ${name}…`);
  try {
    const r = await post(`/v1/schedules/${enc(name)}/run`);
    if (r.fired) toast(`${name} started`);
    else toast(`${name} did not run: ${runResultLabel(r.entry) || r.reason || r.result}`, "error");
  } catch (e) {
    toast(`Run failed: ${errText(e)}`, "error");
  }
  after?.();
}

async function setPaused(st, pause, after) {
  const name = st.name;
  try {
    const r = await post(`/v1/schedules/${enc(name)}/${pause ? "pause" : "resume"}`);
    if (pause) actionToast(`${name} paused`, "Undo", () => setPaused({ ...st }, false, after));
    else toast(`${name} resumed${r?.next_fire ? ` · next ${formatNextFire(r.next_fire)}` : ""}`);
  } catch (e) {
    toast(`${pause ? "Pause" : "Resume"} failed: ${errText(e)}`, "error");
  }
  after?.();
}

async function deleteJob(st) {
  const ok = await confirmSheet({
    title: `Delete ${st.name}?`,
    lines: ["The job stops firing. Its run history is kept, and a window it is running now is left alone."],
    confirmLabel: "Delete",
    danger: true,
  });
  if (!ok) return false;
  try {
    await post(`/v1/schedules/${enc(st.name)}/delete`);
    toast(`${st.name} deleted`);
    return true;
  } catch (e) {
    toast(`Delete failed: ${errText(e)}`, "error");
    return false;
  }
}

// ---- list ----

function mountList(root, ctx, write) {
  const banners = h("div", { class: "sched-banners" });
  const list = h("div", { class: "sched-list", "aria-live": "polite" });
  const status = h("p", { class: "muted pad", hidden: true });
  root.append(
    h("header", { class: "bar" }, h("a", { class: "btn", href: "#/" }, "‹ Fleet"), h("h1", {}, "Schedules"),
      write ? h("a", { class: "btn small", href: "#/schedules/new" }, "+ New") : null),
    banners, status, list,
  );
  let held = 0;
  let stale = false;
  let data = null;
  let gone = false;

  const busy = (on) => {
    held = Math.max(0, held + (on ? 1 : -1));
    if (!held && stale) render();
  };

  function row(st) {
    const g = scheduleGlyph(st);
    const exc = scheduleException(st);
    const last = scheduleLastText(st);
    const link = h(
      "a",
      { class: `win sched-row ${g.kind}`, href: scheduleHash(st.name) },
      h("span", { class: `glyph sched-glyph ${g.kind}`, "aria-label": g.kind }, g.glyph),
      h(
        "span",
        { class: "win-main" },
        h("span", { class: "win-title" }, h("span", { class: "agent" }, st.name), h("span", { class: "sched-words" }, scheduleScheduleText(st))),
        h("span", { class: "activity" }, scheduleRowActivity(st.job, dirName(st.job.dir))),
        exc ? h("span", { class: "win-chips" }, h("span", { class: "tag sched-exc" }, exc)) : null,
        last ? h("span", { class: "activity" }, last) : null,
      ),
      h("span", { class: "win-side" }, h("span", { class: "sched-when" }, scheduleWhen(st))),
    );
    const node = h("div", { class: "node" }, link,
      write ? h("button", { class: "btn small sched-run", type: "button", "aria-label": `Run ${st.name} now`, onclick: () => runNow(st, load) }, "▶") : null);
    if (!write) return node;
    const label = st.enabled ? "Pause" : "Resume";
    const action = h("button", { class: "swipe-action", type: "button", "aria-label": `${label} ${st.name}` }, label);
    const wrap = h("div", { class: "swipe", "data-job": st.name }, action, node);
    attachSwipe(wrap, node, link, action, {
      onBusy: busy,
      onClose: async () => {
        await setPaused(st, st.enabled, load);
        return false;
      },
    });
    return wrap;
  }

  function render() {
    if (held) {
      stale = true;
      return;
    }
    stale = false;
    clear(banners);
    clear(list);
    if (!data) return;
    for (const b of scheduleBanners(data)) banners.append(h("p", { class: "banner warn", role: "alert" }, b));
    if (data.jobs.length === 0) {
      list.append(h("p", { class: "muted pad sched-empty" }, "No scheduled jobs yet.", write ? " Tap + New to run an agent on a schedule." : ""));
      return;
    }
    for (const st of data.jobs) list.append(row(st));
  }

  async function load() {
    try {
      data = await get("/v1/schedules");
      status.hidden = true;
    } catch (e) {
      if (!data) {
        status.hidden = false;
        status.textContent = `Could not load schedules: ${errText(e)}`;
      }
    }
    if (!gone) render();
  }

  load();
  const timer = setInterval(() => !document.hidden && load(), POLL_MS);
  return () => {
    gone = true;
    clearInterval(timer);
  };
}

// ---- job card ----

function mountCard(root, ctx, name, write) {
  const body = h("div", { class: "sched-card pad" });
  root.append(header(name, { href: "#/schedules", label: "‹ Schedules" }), body);
  let gone = false;

  async function load() {
    let st;
    try {
      st = await get(`/v1/schedules/${enc(name)}`);
    } catch (e) {
      if (!gone) clear(body), body.append(h("p", { class: "muted" }, e instanceof ApiError && e.status === 404 ? "No such job." : `Could not load: ${errText(e)}`));
      return;
    }
    let fires = [];
    let tz = "";
    if (write) {
      try {
        const p = await post("/v1/schedules/preview", { job: st.job, original: st.name });
        fires = p.next_fires_text ?? [];
        tz = p.time_zone ?? "";
      } catch {
        // the card is useful without the preview
      }
    }
    if (!gone) render(st, fires, tz);
  }

  function render(st, fires, tz) {
    clear(body);
    const live = new Map((st.windows ?? []).map((w) => [w.window_id, w]));
    const next = fires[0] ?? formatNextFire(st.next_fire);
    const lead = cardStatus(st, next);
    const failure = cardLastFailure(st);
    const adv = advancedSummary(newScheduleForm({ job: st.job }));
    const facts = [
      ["Schedule", `${scheduleScheduleText(st)}${tz ? ` · ${tz}` : ""}`],
      ["Project", tildePath(st.job.dir)],
      ["Agent", scheduleAgentText(st.job, st.template_resolved?.agent ?? "")],
      ["Worktree", worktreeFact(st.job)],
    ];
    if (st.job.mode === "improve") facts.splice(2, 0, ["Mode", "Fleet improve (unattended)"]);
    if (st.job.template) facts.splice(3, 0, ["Template", `${st.job.template}${st.template_resolved ? ` (${st.template_resolved.scope})` : ""}`]);
    if (fires.length > 1 && st.enabled) facts.push(["Next fires", fires.join(", ")]);
    if (adv) facts.push(["Advanced", adv]);
    const promptFrom = schedulePromptSource(st.job);
    const unpinned = Boolean(st.job.prompt_key && !st.job.prompt);
    // Native append stringifies null: build the list, then drop the empty slots.
    body.append(...[
      h("p", { class: `sched-status ${lead.kind}` }, h("span", { class: `glyph sched-glyph ${lead.kind}`, "aria-hidden": "true" }, lead.glyph), " ", lead.text),
      failure ? h("p", { class: "sched-fail", role: "status" }, failure) : null,
      st.invalid ? h("p", { class: "sched-fail" }, `Problem: ${st.invalid}`) : null,
      h("div", { class: "sched-facts" }, ...facts.map(([k, v]) => h("div", {}, h("span", { class: "meta" }, `${k} `), v))),
      st.job.template && st.template_error ? h("p", { class: "sched-fail" }, `Template: ${st.template_error}`) : null,
      promptFrom ? h("p", { class: unpinned ? "sched-fail" : "meta" }, `Prompt: ${promptFrom}`) : null,
      h("pre", { class: "sheet-body sched-prompt" }, st.job.mode === "improve"
        ? [st.improve_summary, st.job.prompt ? `Theme: ${st.job.prompt}` : ""].filter(Boolean).join("\n\n")
        : st.job.template
        ? (st.template_resolved?.prompt ?? `(template ${st.job.template}, read at every run)`)
        : st.job.prompt || st.prompt_resolved || (st.job.prompt_key ? `(context key ${st.job.prompt_key})` : "")),
      write
        ? h("div", { class: "row sched-actions" },
            h("a", { class: "btn", href: scheduleHash(name, "edit") }, "Edit"),
            h("a", { class: "btn", href: scheduleHash(name, "copy") }, "Duplicate"),
            h("button", { class: "btn", type: "button", onclick: () => setPaused(st, st.enabled, load) }, st.enabled ? "Pause" : "Resume"),
            h("button", { class: "btn", type: "button", onclick: () => runNow(st, load) }, "Run now"))
        : null,
      write
        ? h("details", { class: "sched-more" },
            h("summary", { "aria-label": "More actions" }, "More"),
            h("button", { class: "btn danger", type: "button", onclick: async () => { if (await deleteJob(st)) location.hash = "#/schedules"; } }, "Delete"))
        : null,
      h("h2", {}, "Recent runs"),
      st.runs?.length
        ? h("ul", { class: "sched-runs" }, ...st.runs.map((e) => {
            const w = e.window ? live.get(e.window) : null;
            const when = `${formatAgo(e.ts)} · ${formatClock(new Date(e.ts).getTime())}`;
            return h("li", {}, h("span", { class: "meta" }, `${when} `), e.label ? `${e.label} ` : null, e.handle ? h("span", { class: "tag handle-chip" }, e.handle) : null, e.handle ? " " : null, runResultCard(e), e.manual ? " (manual)" : "",
              w ? [" ", h("a", { href: windowHash(w.session, w.window_id) }, "open window")] : null);
          }))
        : h("p", { class: "muted" }, "No runs yet."),
    ].filter(Boolean));
  }

  load();
  return () => {
    gone = true;
  };
}

// ---- form ----

const MODES = [["hourly", "Hourly"], ["daily", "Daily"], ["weekdays", "Weekdays"], ["days", "Days…"], ["custom", "Custom"]];
const DAY_CHIPS = [["M", 1], ["T", 2], ["W", 3], ["T", 4], ["F", 5], ["S", 6], ["S", 0]];
const DAY_NAMES = { 0: "Sunday", 1: "Monday", 2: "Tuesday", 3: "Wednesday", 4: "Thursday", 5: "Friday", 6: "Saturday" };
const pad2 = (n) => String(n).padStart(2, "0");
const range = (n) => Array.from({ length: n }, (_, i) => pad2(i));
const OVERLAP = [["skip", "Skip if running"], ["allow", "Allow overlap"]];
const CATCHUP = [["latest", "Run latest missed"], ["none", "Skip missed"]];
// Server field names that have their own slot under another name.
const SLOT_ALIAS = { skip_if_usage_above: "usage" };

const SOURCES = [["prompt", "Prompt"], ["template", "Template"]];

function mountForm(root, ctx, { job, copy }) {
  const withOptions = ctx.hasFeature("launch-options");
  // Prompt templates (GET /v1/templates) as the prompt source.
  const withTemplates = ctx.hasFeature("dispatch-templates");
  let templates = [];
  let templatesSeq = 0;
  const editing = Boolean(job) && !copy;
  let f = newScheduleForm();
  let gone = false;
  let previewSeq = 0;
  let timer = 0;
  let dirty = true; // the form changed since the last preview
  let preview = null; // the latest valid-or-not preview response
  let serverErrors = {};
  let globalGate = { five_hour: "", seven_day: "" };
  let timeoutCustom = false;
  const touched = new Set();
  let saving = false;
  let showAll = false;

  const title = editing ? `Edit ${job}` : copy ? `Duplicate ${job}` : "New schedule";
  const form = h("form", { class: "form sched-form", onsubmit: (e) => e.preventDefault(), novalidate: true });
  root.append(header(title, { href: editing ? scheduleHash(job) : "#/schedules", label: "‹ Back" }), form);
  form.append(h("p", { class: "muted" }, "Loading…"));

  const name = h("input", { type: "text", "aria-label": "Name", autocapitalize: "off", autocorrect: "off", spellcheck: "false", maxlength: "48" });
  const project = h("select", { "aria-label": "Project" });
  const agent = h("select", { "aria-label": "Agent" });
  const prompt = h("textarea", { rows: "8", "aria-label": "Prompt", placeholder: "What should the agent do on every run?" });
  const promptBox = h("div", { class: "sched-prompt-src" });
  const templateSel = h("select", { "aria-label": "Template" });
  // The agent select's "from the template" choice (value ""), shown only for a template job.
  const templateAgentOpt = h("option", { value: "" }, "From the template");
  const schedBox = h("div", { class: "sched-picker" });
  const previewLine = h("p", { class: "sched-preview muted", "aria-live": "polite" });
  const advBox = h("div", { class: "sched-adv" });
  const launchBox = h("div", { class: "launch-opts" });
  const summary = h("span", { class: "muted sched-adv-summary" });
  // Always enabled: a tap on a form with a problem shows every error.
  const submit = h("button", { class: "btn primary", type: "button" }, editing ? "Review changes" : "Review & schedule");
  const savingNote = h("p", { class: "muted", hidden: true, role: "status" });
  const picker = withOptions
    ? launchPicker(launchBox, {
        schedule: true,
        onError: (e) => toast(`Could not load launch options: ${e.message}`, "error"),
        onChange: (c, opts) => {
          const d = opts?.defaults ?? {};
          // Only a choice that differs from the server default is stored.
          f.backend = c.backend === d.backend ? "" : c.backend;
          f.model = c.model === d.model ? "" : c.model;
          f.effort = c.effort === d.effort ? "" : c.effort;
          f.account = c.account ?? "";
          changed();
        },
      })
    : null;

  // The launch pins the form holds; a reload keeps them so the picker shows what Save sends.
  const launchPreset = () => ({ backend: f.backend, model: f.model, effort: f.effort, account: f.account });
  const errors = () => {
    const out = {};
    for (const [k, v] of Object.entries({ ...serverErrors, ...formClientErrors(f) })) out[SLOT_ALIAS[k] ?? k] = v;
    return out;
  };
  const fieldError = (field) => (showAll || touched.has(field) ? errors()[field] : "");
  const blocked = () => !preview?.valid || Object.keys(formClientErrors(f)).length > 0;

  function changed() {
    dirty = true;
    summary.textContent = advancedSummary(f);
    clearTimeout(timer);
    timer = setTimeout(runPreview, PREVIEW_DEBOUNCE_MS);
    renderErrors();
  }

  async function runPreview() {
    timer = 0;
    const seq = ++previewSeq;
    try {
      const p = await post("/v1/schedules/preview", previewBody(f));
      if (seq !== previewSeq || gone) return;
      preview = p;
      dirty = false;
      serverErrors = fieldErrors(p.errors);
      previewLine.textContent = p.valid || p.words || p.next_fires_text?.length
        ? [p.words || p.schedule, p.time_zone ? `${p.time_zone} time` : "", p.next_fires_text?.length ? `next ${p.next_fires_text.join(", ")}` : ""].filter(Boolean).join(" · ")
        : "";
    } catch (e) {
      if (seq !== previewSeq || gone) return;
      preview = null;
      previewLine.textContent = `Could not preview: ${errText(e)}`;
    }
    renderErrors();
  }

  const slots = {};
  function slot(field) {
    slots[field] ??= h("span", { class: "field-error", role: "alert", hidden: true });
    return slots[field];
  }
  // Errors for a field with no slot of its own end up here, above the button.
  const general = h("p", { class: "field-error", role: "alert", hidden: true });
  function renderErrors() {
    const all = errors();
    for (const [field, el] of Object.entries(slots)) {
      el.textContent = fieldError(field) ?? "";
      el.hidden = !el.textContent;
    }
    const orphans = Object.entries(all).filter(([k]) => !slots[k] && (showAll || touched.has(k))).map(([, v]) => v);
    general.textContent = orphans.join(" · ");
    general.hidden = !orphans.length;
  }

  /** revealFirstError scrolls to (and focuses) the first visible error. */
  function revealFirstError() {
    const el = [...form.querySelectorAll(".field-error")].find((e) => !e.hidden);
    if (!el) return;
    const det = el.closest("details");
    if (det) det.open = true;
    el.scrollIntoView({ block: "center", behavior: "smooth" });
    el.parentElement?.querySelector("input:not([disabled]), select, textarea, button")?.focus({ preventScroll: true });
  }

  // Leaving a text field previews at once (a pause already would after 1s).
  function previewNow() {
    clearTimeout(timer);
    runPreview();
  }

  function bind(el, key, field) {
    el.addEventListener("blur", () => {
      if (!gone && timer) previewNow();
    });
    el.addEventListener("input", () => {
      f[key] = el.value;
      touched.add(field ?? key);
      changed();
    });
  }

  /** selectField is a native select in a label; values are strings. */
  function selectField(label, opts, cur, onPick, aria = label) {
    const s = h("select", { "aria-label": aria }, ...opts.map(([v, l]) => h("option", { value: v }, l)));
    s.value = cur;
    s.addEventListener("change", () => onPick(s.value));
    return label ? h("label", {}, label, s) : s;
  }

  /** segmented is a one-row control of mutually exclusive choices; a stored value not in opts is kept, marked unknown. */
  function segmented(label, opts, key) {
    const all = opts.some(([v]) => v === f[key]) ? opts : [...opts, [f[key], `${f[key]} (unknown)`]];
    const btns = all.map(([v, l]) => h("button", { type: "button", class: "btn", "aria-pressed": String(f[key] === v),
      onclick: () => {
        f[key] = v;
        btns.forEach((b, i) => b.setAttribute("aria-pressed", String(all[i][0] === v)));
        touched.add(key);
        changed();
      } }, l));
    return h("div", { class: "sched-field" }, h("div", { class: "muted" }, label), h("div", { class: "sched-seg2", role: "group", "aria-label": label }, ...btns), slot(key));
  }

  // ---- schedule picker ----
  function renderSchedule() {
    clear(schedBox);
    const seg = h("div", { class: "sched-seg", role: "group", "aria-label": "Schedule type" },
      ...MODES.map(([m, label]) => h("button", { type: "button", class: "btn", "aria-pressed": String(f.mode === m),
        onclick: () => { f.mode = m; if (m === "custom" && !f.raw) f.raw = "0 9 * * *"; renderSchedule(); touched.add("schedule"); changed(); } }, label)));
    schedBox.append(seg);
    const minuteSel = (label, aria = label) => selectField(label, range(60).map((v) => [String(Number(v)), v]), String(f.minute), (v) => { f.minute = Number(v); changed(); }, aria);
    if (f.mode === "hourly") {
      schedBox.append(minuteSel("Minute past the hour"));
    } else if (f.mode === "custom") {
      const raw = h("input", { type: "text", value: f.raw, "aria-label": "Cron expression", autocapitalize: "off", autocorrect: "off", spellcheck: "false", placeholder: "30 7 * * 1-5" });
      raw.addEventListener("input", () => { f.raw = raw.value; touched.add("schedule"); changed(); });
      schedBox.append(h("label", {}, "Cron expression", raw));
    } else {
      if (f.mode === "days") {
        const chips = h("div", { class: "day-chips", role: "group", "aria-label": "Days" },
          ...DAY_CHIPS.map(([l, d]) => h("button", { type: "button", class: "day-chip", "aria-label": DAY_NAMES[d], "aria-pressed": String(f.days.includes(d)),
            onclick: (e) => {
              f.days = f.days.includes(d) ? f.days.filter((x) => x !== d) : [...f.days, d];
              e.currentTarget.setAttribute("aria-pressed", String(f.days.includes(d)));
              touched.add("schedule");
              changed();
            } }, l)));
        schedBox.append(chips);
      }
      // 24h: two native selects, so the device's 12h clock never shows.
      schedBox.append(h("div", { class: "sched-time", role: "group", "aria-label": "Time (local, 24h)" },
        h("span", { class: "muted" }, "Time (local, 24h)"),
        h("div", { class: "sched-time-sel" },
          selectField("", range(24).map((v) => [String(Number(v)), v]), String(f.hour), (v) => { f.hour = Number(v); changed(); }, "Hour"),
          h("span", {}, ":"),
          minuteSel("", "Minute"))));
    }
    schedBox.append(slot("schedule"));
    renderErrors();
  }

  // ---- advanced ----
  function renderAdvanced() {
    clear(advBox);
    const text = (key, label, placeholder = "") => {
      const i = h("input", { type: "text", value: f[key], "aria-label": label, placeholder, autocapitalize: "off", autocorrect: "off", spellcheck: "false" });
      bind(i, key);
      return h("label", {}, label, i, slot(key));
    };
    const wt = h("input", { type: "checkbox", checked: f.worktree, "aria-label": "Use a new worktree for each run" });
    wt.addEventListener("change", () => { f.worktree = wt.checked; changed(); });

    // Timeout: presets, or custom… which reveals a text box. A stored value
    // that is not a preset opens in custom.
    timeoutCustom = !timeoutIsPreset(f.timeout);
    const custom = h("input", { type: "text", value: f.timeout, "aria-label": "Custom timeout", placeholder: "e.g. 90m", autocapitalize: "off", autocorrect: "off", spellcheck: "false", hidden: !timeoutCustom });
    bind(custom, "timeout");
    const tsel = selectField("Timeout", [...TIMEOUT_PRESETS.map((v) => [v, v || "none"]), ["__custom", "custom…"]], timeoutCustom ? "__custom" : f.timeout, (v) => {
      timeoutCustom = v === "__custom";
      custom.hidden = !timeoutCustom;
      f.timeout = timeoutCustom ? custom.value.trim() : v;
      if (timeoutCustom) custom.focus();
      touched.add("timeout");
      changed();
    });
    tsel.append(custom, slot("timeout"));

    const gate = (key, label) => selectField("", usageOptions(globalGate[key === "gate5h" ? "five_hour" : "seven_day"], f[key]), f[key], (v) => { f[key] = v; touched.add("usage"); changed(); }, label);
    advBox.append(
      h("label", { class: "sched-check" }, wt, " New worktree for each run"),
      withOptions
        ? launchBox
        : h("div", {}, text("backend", "Backend"), text("model", "Model"), text("effort", "Effort"), text("account", "Account")),
      tsel,
      segmented("Overlap", OVERLAP, "overlap"),
      segmented("Catch-up", CATCHUP, "catchup"),
      h("div", { class: "sched-field" }, h("div", { class: "muted" }, "Skip above"),
        h("div", { class: "sched-gate" }, h("span", { class: "muted" }, "5h"), gate("gate5h", "Skip above, 5h usage"), h("span", { class: "muted" }, "7d"), gate("gate7d", "Skip above, 7d usage")),
        slot("usage")),
      // model/effort/backend/account errors from the server show under the launch controls
      slot("backend"), slot("model"), slot("effort"), slot("account"),
    );
  }

  /** loadTemplates lists the templates the job's project sees, keeping the choice. */
  async function loadTemplates() {
    if (!withTemplates) return;
    const seq = ++templatesSeq;
    try {
      const res = await get(templatesPath(f.dir));
      if (seq !== templatesSeq || gone) return;
      templates = res.templates ?? [];
    } catch (e) {
      if (seq !== templatesSeq || gone) return;
      templates = [];
      toast(`Could not load templates: ${errText(e)}`, "error");
    }
    syncTemplate();
    renderPromptBlock();
  }

  /** syncTemplate copies the chosen template's prompt and agent into the form for display. */
  function syncTemplate() {
    const t = templates.find((x) => x.name === f.template);
    f.templateText = t?.prompt ?? (f.template ? f.templateText : "");
    f.templateAgent = t?.agent ?? "";
    templateAgentOpt.textContent = f.templateAgent ? `From the template (${f.templateAgent})` : "From the template";
  }

  function renderTemplateOptions() {
    clear(templateSel);
    templateSel.append(h("option", { value: "" }, templates.length ? "Pick a template…" : "No templates in this project"));
    for (const t of templates) templateSel.append(h("option", { value: t.name }, templateLabel(t)));
    if (f.template && !templates.some((t) => t.name === f.template)) templateSel.append(h("option", { value: f.template }, `${f.template} (not found)`));
    templateSel.value = f.template;
  }

  function setSource(src) {
    f.source = src;
    templateAgentOpt.hidden = src !== "template";
    if (src !== "template" && !f.agent && f.base?.mode !== "improve") {
      // A prompt job needs an agent of its own.
      const first = [...agent.options].find((o) => o.value);
      if (first) { agent.value = first.value; f.agent = first.value; }
    }
    touched.add("prompt");
    renderPromptBlock();
    changed();
  }

  function renderPromptBlock() {
    clear(promptBox);
    if (withTemplates) {
      const btns = SOURCES.map(([v, l]) => h("button", { type: "button", class: "btn", "aria-pressed": String(f.source === v), onclick: () => setSource(v) }, l));
      promptBox.append(h("div", { class: "sched-field" }, h("div", { class: "muted" }, "Prompt source"),
        h("div", { class: "sched-seg2", role: "group", "aria-label": "Prompt source" }, ...btns)));
    }
    if (f.source === "template") {
      renderTemplateOptions();
      promptBox.append(h("label", {}, "Template", templateSel, slot("template")),
        f.templateText ? h("pre", { class: "sheet-body sched-prompt", "aria-label": "Template prompt" }, f.templateText) : null,
        h("p", { class: "muted" }, "Read from the project at every run; the agent, model and effort you set here win over the template's."));
      return;
    }
    promptBox.append(promptBlock());
  }

  function promptBlock() {
    if (f.promptKey) {
      return h("div", { class: "sched-field" }, h("div", { class: "muted" }, "Prompt"),
        h("p", { class: "sched-keyline" }, `from context key ${f.promptKey}`),
        f.promptKeyText ? h("pre", { class: "sheet-body sched-prompt" }, f.promptKeyText) : null, slot("prompt"));
    }
    return h("label", {}, "Prompt", prompt, slot("prompt"));
  }

  function build(projects, agents) {
    clear(form);
    for (const x of projects) project.append(h("option", { value: x.dir }, x.name));
    if (f.dir && !projects.some((x) => x.dir === f.dir)) project.append(h("option", { value: f.dir }, `${tildePath(f.dir)} (unknown)`));
    templateAgentOpt.hidden = f.source !== "template";
    agent.append(templateAgentOpt);
    for (const x of agents) agent.append(h("option", { value: x.name }, x.name));
    if (f.agent && !agents.some((x) => x.name === f.agent)) agent.append(h("option", { value: f.agent }, `${f.agent} (unknown)`));
    name.value = f.name;
    name.disabled = editing;
    prompt.value = f.prompt;
    project.value = f.dir;
    agent.value = f.agent;
    bind(name, "name");
    bind(prompt, "prompt");
    templateSel.addEventListener("change", () => {
      f.template = templateSel.value;
      touched.add("template");
      syncTemplate();
      renderPromptBlock();
      changed();
    });
    let userAgent = Boolean(f.agent);
    project.addEventListener("change", () => {
      f.dir = project.value;
      touched.add("dir");
      const def = defaultAgentFor(projects, f.dir, [...agent.options].map((o) => o.value), userAgent);
      if (def && !(f.source === "template" && !userAgent)) { agent.value = def; f.agent = def; }
      picker?.load(f.agent, f.dir, launchPreset());
      loadTemplates();
      changed();
    });
    agent.addEventListener("change", () => {
      userAgent = true;
      f.agent = agent.value;
      touched.add("agent");
      picker?.load(f.agent, f.dir, launchPreset());
      changed();
    });
    renderSchedule();
    renderAdvanced();
    form.append(...[
      h("label", {}, "Name", name, editing ? h("span", { class: "muted" }, "The name cannot change after creation.") : null, slot("name")),
      h("label", {}, "Project", project, slot("dir")),
      h("label", {}, "Agent", agent, slot("agent")),
      promptBox,
      h("div", { class: "sched-field" }, h("div", { class: "muted" }, "Schedule"), schedBox, previewLine),
      h("details", { class: "instructions sched-advanced" }, h("summary", {}, h("span", { class: "sched-adv-title" }, "Advanced"), summary), advBox),
      ctx.hasFeature("write") ? null : h("p", { class: "muted" }, "Write actions are not enabled on this server."),
      general,
      savingNote,
      submit,
    ].filter(Boolean));
    summary.textContent = advancedSummary(f);
    renderPromptBlock();
    renderErrors();
  }

  async function init() {
    try {
      const [a, p] = await Promise.all([get("/v1/agents"), get("/v1/projects")]);
      const projects = p.projects ?? [];
      const agents = a.agents ?? [];
      // The global usage thresholds name the "global (N%)" choices; the form works without them.
      get("/v1/schedules")
        .then((o) => {
          const g = o?.throttles?.skip_if_usage_above ?? {};
          globalGate = { five_hour: g.five_hour ?? "", seven_day: g.seven_day ?? "" };
          if (!gone && form.contains(advBox)) renderAdvanced();
        })
        .catch(() => {});
      if (job) {
        const st = await get(`/v1/schedules/${enc(job)}`);
        f = newScheduleForm({ job: st.job, simple: st.simple ?? null, promptText: st.prompt_resolved ?? "", copy, words: st.words ?? "",
          templateText: st.template_resolved?.prompt ?? "" });
      } else {
        const dir = projects[0]?.dir ?? "";
        f = newScheduleForm();
        f.dir = dir;
        f.agent = defaultAgentFor(projects, dir, agents.map((x) => x.name), false) || agents[0]?.name || "";
      }
      if (gone) return;
      build(projects, agents);
      picker?.load(f.agent, f.dir, launchPreset());
      loadTemplates();
      runPreview();
    } catch (e) {
      clear(form);
      form.append(h("p", { class: "muted" }, `Could not load the form: ${errText(e)}`));
    }
  }

  submit.addEventListener("click", async () => {
    if (saving) return;
    showAll = true;
    renderErrors();
    if (timer || dirty || !preview) {
      clearTimeout(timer);
      await runPreview();
    }
    renderErrors();
    if (blocked()) {
      if (!preview) toast("Could not preview the schedule; try again", "error");
      revealFirstError();
      return;
    }
    const ok = await confirmSheet({
      title: editing ? "Save changes?" : "Schedule it?",
      lines: [...scheduleReview(preview, f), copy ? "Saved paused: resume it from the list when you are ready." : ""].filter(Boolean),
      confirmLabel: editing ? "Save changes" : "Schedule it",
    });
    if (ok) await save();
  });

  async function save() {
    saving = true;
    submit.disabled = true;
    const slow = setTimeout(() => {
      savingNote.hidden = false;
      savingNote.textContent = "Still saving: the scheduler may be busy starting a run. This can take a minute.";
    }, SLOW_SAVE_MS);
    const body = scheduleBody(f);
    const target = editing ? job : body.job.name;
    try {
      if (editing) await api("PUT", `/v1/schedules/${enc(job)}`, body);
      else await post("/v1/schedules", body);
      toast(editing ? "Changes saved" : copy ? "Saved paused" : "Scheduled");
      location.hash = scheduleHash(target);
    } catch (e) {
      if (e instanceof ApiError && e.status === 422) {
        serverErrors = fieldErrors(e.fields);
        showAll = true;
        renderErrors();
        revealFirstError();
        toast(e.message, "error");
      } else if (e instanceof ApiError) {
        toast(e.message, "error");
      } else {
        // The connection dropped mid-save: the write may have landed.
        await confirmAfterDrop(target);
      }
    } finally {
      clearTimeout(slow);
      savingNote.hidden = true;
      saving = false;
      if (!gone) submit.disabled = false;
    }
  }

  async function confirmAfterDrop(target) {
    try {
      await get(`/v1/schedules/${enc(target)}`);
      toast("Connection dropped; check that the job has what you saved", "error");
      location.hash = scheduleHash(target);
    } catch {
      toast("Could not save: no connection. Try again.", "error");
    }
  }

  init();
  return () => {
    gone = true;
    clearTimeout(timer);
    picker?.stop();
  };
}
