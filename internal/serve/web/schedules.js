// Schedules screen: list, job card and create/edit form for `agentctl cron`
// jobs. The server (internal/cronedit) owns validation, the schedule picker's
// model and the previews; this file only renders them.
import { ApiError, api, get } from "./api.js";
import { launchPicker } from "./dispatch.js";
import {
  advancedSummary, defaultAgentFor, dirName, fieldErrors, formClientErrors, formatAgo, formatClock, formatNextFire, newScheduleForm,
  previewBody, runResultLabel, scheduleBanners, scheduleBody, scheduleException, scheduleGlyph, scheduleHash, scheduleLastText,
  scheduleReview, scheduleScheduleText, scheduleWhen, tildePath, windowHash, worktreeFact,
} from "./logic.js";
import { attachSwipe } from "./swipe.js";
import { clear, confirmSheet, h, toast } from "./ui.js";

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

/** actionToast is a toast with one button (Undo), alive for ms. */
function actionToast(message, label, onClick, ms = 7000) {
  const host = document.getElementById("toasts");
  const t = h("div", { class: "toast info action", role: "status" }, message, " ");
  const btn = h("button", { class: "btn-text", type: "button", onclick: () => { t.remove(); onClick(); } }, label);
  t.append(btn);
  host.append(t);
  setTimeout(() => t.remove(), ms);
}

const errText = (e) => (e instanceof ApiError ? e.message : e instanceof Error ? e.message : String(e));

/** runNow confirms, runs a job with plain `cron run` semantics and reports fired or the skip reason. */
async function runNow(st, after) {
  const name = st.name;
  const ok = await confirmSheet({
    title: `Run ${name} now?`,
    lines: [`Runs ${st.job.agent} in ${tildePath(st.job.dir)} unattended.\nOverlap, the concurrency limit and the usage gate still apply; nothing is forced.`],
    confirmLabel: "Run now",
  });
  if (!ok) return;
  toast(`Running ${name}…`);
  try {
    const r = await post(`/v1/schedules/${enc(name)}/run`);
    if (r.fired) toast(`${name} started${r.window_id ? ` in ${r.window_id}` : ""}`);
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
        h("span", { class: "activity" }, `${st.job.agent} · ${dirName(st.job.dir)}`),
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
    const facts = [
      ["Schedule", `${scheduleScheduleText(st)}${tz ? ` · ${tz}` : ""}`],
      ["Status", st.enabled ? (st.running ? "running" : "enabled") : "paused"],
      ["Next", st.enabled ? (fires.length ? fires.join(", ") : scheduleWhen(st)) : "paused"],
      ["Project", tildePath(st.job.dir)],
      ["Agent", st.job.agent],
      ["Worktree", worktreeFact(st.job)],
    ];
    if (st.invalid) facts.push(["Problem", st.invalid]);
    body.append(
      h("div", { class: "sched-facts" }, ...facts.map(([k, v]) => h("div", {}, h("span", { class: "meta" }, `${k} `), v))),
      h("pre", { class: "sheet-body sched-prompt" }, st.job.prompt || st.prompt_resolved || (st.job.prompt_key ? `(context key ${st.job.prompt_key})` : "")),
      write
        ? h("div", { class: "row sched-actions" },
            h("a", { class: "btn", href: scheduleHash(name, "edit") }, "Edit"),
            h("a", { class: "btn", href: scheduleHash(name, "copy") }, "Duplicate"),
            h("button", { class: "btn", type: "button", onclick: () => setPaused(st, st.enabled, load) }, st.enabled ? "Pause" : "Resume"),
            h("button", { class: "btn", type: "button", onclick: () => runNow(st, load) }, "Run now"),
            h("button", { class: "btn danger", type: "button", onclick: async () => { if (await deleteJob(st)) location.hash = "#/schedules"; } }, "Delete"))
        : null,
      h("h2", {}, "Recent runs"),
      st.runs?.length
        ? h("ul", { class: "sched-runs" }, ...st.runs.map((e) => {
            const w = e.window ? live.get(e.window) : null;
            const when = `${formatAgo(e.ts)} · ${formatClock(new Date(e.ts).getTime())}`;
            return h("li", {}, h("span", { class: "meta" }, `${when} `), runResultLabel(e), e.manual ? " (manual)" : "",
              w ? [" ", h("a", { href: windowHash(w.session, w.window_id) }, "open window")] : null);
          }))
        : h("p", { class: "muted" }, "No runs yet."),
    );
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

function mountForm(root, ctx, { job, copy }) {
  const withOptions = ctx.hasFeature("launch-options");
  const editing = Boolean(job) && !copy;
  let f = newScheduleForm();
  let gone = false;
  let previewSeq = 0;
  let timer = 0;
  let preview = null; // the latest valid-or-not preview response
  let serverErrors = {};
  const touched = new Set();
  let saving = false;
  let showAll = false;

  const title = editing ? `Edit ${job}` : copy ? `Duplicate ${job}` : "New schedule";
  const form = h("form", { class: "form sched-form", onsubmit: (e) => e.preventDefault() });
  root.append(header(title, { href: editing ? scheduleHash(job) : "#/schedules", label: "‹ Back" }), form);
  form.append(h("p", { class: "muted" }, "Loading…"));

  const name = h("input", { type: "text", "aria-label": "Name", autocapitalize: "off", autocorrect: "off", spellcheck: "false", maxlength: "48" });
  const project = h("select", { "aria-label": "Project" });
  const agent = h("select", { "aria-label": "Agent" });
  const prompt = h("textarea", { rows: "8", "aria-label": "Prompt", placeholder: "What should the agent do on every run?" });
  const schedBox = h("div", { class: "sched-picker" });
  const previewLine = h("p", { class: "sched-preview muted", "aria-live": "polite" });
  const advBox = h("div", { class: "sched-adv" });
  const launchBox = h("div", { class: "launch-opts" });
  const summary = h("span", { class: "muted sched-adv-summary" });
  const submit = h("button", { class: "btn primary", type: "button", disabled: true }, editing ? "Review changes" : "Review & schedule");
  const savingNote = h("p", { class: "muted", hidden: true, role: "status" });
  const picker = withOptions
    ? launchPicker(launchBox, {
        onError: (e) => toast(`Could not load launch options: ${e.message}`, "error"),
        onChange: (c, opts) => {
          const d = opts?.defaults ?? {};
          // Only a choice that differs from the server default is stored.
          f.backend = c.backend === d.backend ? "" : c.backend;
          f.model = c.model === d.model ? "" : c.model;
          f.effort = c.effort === d.effort ? "" : c.effort;
          changed();
        },
      })
    : null;

  // The launch pins the form holds; a reload keeps them so the picker shows what Save sends.
  const launchPreset = () => ({ backend: f.backend, model: f.model, effort: f.effort });
  const errors = () => ({ ...serverErrors, ...formClientErrors(f) });
  const fieldError = (field) => (showAll || touched.has(field) ? errors()[field] : "");
  const ready = () => !saving && preview?.valid && Object.keys(formClientErrors(f)).length === 0;

  function changed() {
    summary.textContent = advancedSummary(f);
    submit.disabled = true;
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
    submit.disabled = !ready();
  }

  const slots = {};
  function slot(field) {
    slots[field] ??= h("span", { class: "field-error", role: "alert" });
    return slots[field];
  }
  function renderErrors() {
    for (const [field, el] of Object.entries(slots)) {
      el.textContent = fieldError(field) ?? "";
      el.hidden = !el.textContent;
    }
  }
  const touch = (field) => () => {
    touched.add(field);
    renderErrors();
  };

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

  // ---- schedule picker ----
  function renderSchedule() {
    clear(schedBox);
    const seg = h("div", { class: "sched-seg", role: "group", "aria-label": "Schedule type" },
      ...MODES.map(([m, label]) => h("button", { type: "button", class: "btn", "aria-pressed": String(f.mode === m),
        onclick: () => { f.mode = m; if (m === "custom" && !f.raw) f.raw = "0 9 * * *"; renderSchedule(); touched.add("schedule"); changed(); } }, label)));
    schedBox.append(seg);
    if (f.mode === "hourly") {
      const minute = h("input", { type: "number", min: "0", max: "59", inputmode: "numeric", value: String(f.minute), "aria-label": "Minute past the hour" });
      minute.addEventListener("input", () => { f.minute = Number(minute.value) || 0; changed(); });
      schedBox.append(h("label", {}, "Minute past the hour", minute));
    } else if (f.mode === "custom") {
      const raw = h("input", { type: "text", value: f.raw, "aria-label": "Cron expression", autocapitalize: "off", autocorrect: "off", spellcheck: "false", placeholder: "30 7 * * 1-5" });
      raw.addEventListener("input", () => { f.raw = raw.value; touched.add("schedule"); changed(); });
      schedBox.append(h("label", {}, "Cron expression", raw));
    } else {
      const time = h("input", { type: "time", value: `${String(f.hour).padStart(2, "0")}:${String(f.minute).padStart(2, "0")}`, "aria-label": "Time (local)" });
      time.addEventListener("input", () => {
        const [hh, mm] = time.value.split(":").map(Number);
        if (Number.isFinite(hh) && Number.isFinite(mm)) { f.hour = hh; f.minute = mm; changed(); }
      });
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
      schedBox.append(h("label", {}, "Time (local)", time));
    }
    schedBox.append(slot("schedule"));
    renderErrors();
  }

  // ---- advanced ----
  function renderAdvanced() {
    clear(advBox);
    const sel = (key, opts, label) => {
      const s = h("select", { "aria-label": label }, ...opts.map(([v, l]) => h("option", { value: v }, l)));
      s.value = f[key];
      s.addEventListener("change", () => { f[key] = s.value; changed(); });
      return h("label", {}, label, s);
    };
    const text = (key, label, placeholder = "") => {
      const i = h("input", { type: "text", value: f[key], "aria-label": label, placeholder, autocapitalize: "off", autocorrect: "off", spellcheck: "false" });
      bind(i, key);
      return h("label", {}, label, i, slot(key));
    };
    const num = (key, label) => {
      const i = h("input", { type: "number", min: "1", max: "100", inputmode: "numeric", value: f[key], "aria-label": label, placeholder: "default" });
      i.addEventListener("input", () => { f[key] = i.value; changed(); });
      return h("label", {}, label, i);
    };
    const wt = h("input", { type: "checkbox", checked: f.worktree, "aria-label": "Use a new worktree for each run" });
    wt.addEventListener("change", () => { f.worktree = wt.checked; changed(); });
    advBox.append(
      h("label", { class: "sched-check" }, wt, " New worktree for each run"),
      withOptions ? launchBox : h("div", {}, text("backend", "Backend"), text("model", "Model"), text("effort", "Effort")),
      text("account", "Account"),
      text("timeout", "Timeout (e.g. 30m)", "none"),
      sel("overlap", [["skip", "Skip if still running"], ["allow", "Allow overlapping runs"]], "Overlap"),
      sel("catchup", [["latest", "Run the latest missed fire"], ["none", "Skip missed fires"]], "Catch-up"),
      num("gate5h", "Skip when 5h usage is above (%)"),
      num("gate7d", "Skip when 7d usage is above (%)"),
    );
  }

  function build(projects, agents) {
    clear(form);
    for (const x of projects) project.append(h("option", { value: x.dir }, x.name));
    if (f.dir && !projects.some((x) => x.dir === f.dir)) project.append(h("option", { value: f.dir }, tildePath(f.dir)));
    for (const x of agents) agent.append(h("option", { value: x.name }, x.name));
    if (f.agent && !agents.some((x) => x.name === f.agent)) agent.append(h("option", { value: f.agent }, f.agent));
    name.value = f.name;
    name.disabled = editing;
    prompt.value = f.prompt;
    project.value = f.dir;
    agent.value = f.agent;
    bind(name, "name");
    bind(prompt, "prompt");
    let userAgent = Boolean(f.agent);
    project.addEventListener("change", () => {
      f.dir = project.value;
      touched.add("dir");
      const def = defaultAgentFor(projects, f.dir, [...agent.options].map((o) => o.value), userAgent);
      if (def) { agent.value = def; f.agent = def; }
      picker?.load(f.agent, f.dir, launchPreset());
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
    form.append(
      h("label", {}, "Name", name, editing ? h("span", { class: "muted" }, "The name cannot change after creation.") : null, slot("name")),
      h("label", {}, "Project", project, slot("dir")),
      h("label", {}, "Agent", agent, slot("agent")),
      h("label", {}, "Prompt",
        f.promptKey ? h("span", { class: "muted" }, `From context key ${f.promptKey}. Editing the text saves it as an inline prompt.`) : null,
        prompt, slot("prompt")),
      h("div", { class: "sched-field" }, h("div", { class: "muted" }, "Schedule"), schedBox, previewLine),
      h("details", { class: "instructions sched-advanced" }, h("summary", {}, "Advanced ", summary), advBox),
      ctx.hasFeature("write") ? null : h("p", { class: "muted" }, "Write actions are not enabled on this server."),
      savingNote,
      submit,
    );
    summary.textContent = advancedSummary(f);
    renderErrors();
  }

  async function init() {
    try {
      const [a, p] = await Promise.all([get("/v1/agents"), get("/v1/projects")]);
      const projects = p.projects ?? [];
      const agents = a.agents ?? [];
      if (job) {
        const st = await get(`/v1/schedules/${enc(job)}`);
        f = newScheduleForm({ job: st.job, simple: st.simple ?? null, promptText: st.prompt_resolved ?? "", copy });
      } else {
        const dir = projects[0]?.dir ?? "";
        f = newScheduleForm();
        f.dir = dir;
        f.agent = defaultAgentFor(projects, dir, agents.map((x) => x.name), false) || agents[0]?.name || "";
      }
      if (gone) return;
      build(projects, agents);
      picker?.load(f.agent, f.dir, launchPreset());
      runPreview();
    } catch (e) {
      clear(form);
      form.append(h("p", { class: "muted" }, `Could not load the form: ${errText(e)}`));
    }
  }

  submit.addEventListener("click", async () => {
    showAll = true;
    renderErrors();
    if (!ready()) return;
    await runPreview();
    if (!ready()) return;
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
      if (!gone) submit.disabled = !ready();
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
