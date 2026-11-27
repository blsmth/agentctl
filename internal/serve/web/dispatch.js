// Dispatch screen: agent + project + prompt, launch options, and a confirm sheet.
import { ApiError, get, send, uploadImages } from "./api.js";
import { imagePicker } from "./attach.js";
import {
  DISPATCH_UPLOAD_PATH, buildDispatchRequest, choiceLabel, confirmLines, agentGroups, defaultAgentFor, launchOptionsPath, modelPicker, templateFill, templateLabel, templatesPath, windowHash,
} from "./logic.js";
import { clear, confirmSheet, h, toast } from "./ui.js";

export function mountDispatch(root, ctx, { dir } = {}) {
  const agent = h("select", { "aria-label": "Agent" });
  const project = h("select", { "aria-label": "Project" });
  const prompt = h("textarea", { rows: "8", placeholder: "What should the agent do?" });
  // Standing guidance for the agent's system prompt; only offered when the
  // server accepts the field (an older serve would reject it as unknown).
  const withInstructions = ctx.hasFeature("dispatch-instructions");
  const instructions = h("textarea", {
    rows: "4",
    placeholder: "Extra guidance for this agent (added to its system prompt)",
    "aria-label": "Additional instructions",
  });
  // Per-dispatch opt-out of the orchestrator's code review; only offered for
  // orchestrator agents (workers have nobody to review) and never remembered.
  const withSkipReview = ctx.hasFeature("dispatch-skip-review");
  const skipReview = h("input", { type: "checkbox", "aria-label": "Skip code review" });
  const skipReviewRow = h("label", { class: "skip-review", hidden: true }, skipReview, " Skip code review");
  const orchestrators = new Set();
  const syncSkipReview = () => {
    const show = withSkipReview && orchestrators.has(agent.value);
    skipReviewRow.hidden = !show;
    if (!show) skipReview.checked = false;
  };
  // Prompt templates: fills agent, model, project and prompt, all still
  // editable. The chosen name goes along as display-only metadata.
  const withTemplates = ctx.hasFeature("dispatch-templates");
  const template = h("select", { "aria-label": "Template" });
  const templateRow = h("label", { hidden: true }, "Template", template);
  let templates = [];
  const submit = h("button", { class: "btn primary", disabled: true }, "Review & send");
  const launchBox = h("div", { class: "launch-opts" });
  const withOptions = ctx.hasFeature("launch-options");
  // Attaching a screenshot needs writes and the dispatch upload route.
  const att = ctx.hasFeature("write") && ctx.hasFeature("dispatch-upload") ? imagePicker() : null;
  if (att) att.bindPaste(prompt);
  root.append(
    h("header", { class: "bar" }, h("a", { class: "btn", href: "#/" }, "‹ Fleet"), h("h1", {}, "Dispatch")),
    h(
      "form",
      { class: "form", onsubmit: (e) => e.preventDefault() },
      h("label", {}, "Project", project),
      h("label", {}, "Agent", agent),
      withOptions ? launchBox : null,
      withTemplates ? templateRow : null,
      h("label", {}, "Prompt", prompt),
      withInstructions
        ? h("details", { class: "instructions" }, h("summary", {}, "Additional instructions"), instructions)
        : null,
      withSkipReview ? skipReviewRow : null,
      ...(att ? att.controls : []),
      ctx.hasFeature("write") ? null : h("p", { class: "muted" }, "Write actions are not enabled on this server."),
      submit,
    ),
  );

  const picker = launchPicker(launchBox, { onError: (e) => toast(`Could not load launch options: ${e.message}`, "error") });
  const loadOptions = () => (withOptions ? picker.load(agent.value, project.value) : undefined);
  // projects is the loaded /v1/projects list; userPickedAgent stops a later
  // project change from overriding a hand-picked agent.
  let projects = [];
  let allAgents = [];
  // fillAgents lists the agents for the selected project: its own first, then
  // global ones.
  function fillAgents() {
    const keep = agent.value;
    // A pick survives a project change only when it is global or belongs to
    // the new project; another project's agent is not listed at all.
    clear(agent);
    const opt = (x) => h("option", { value: x.name }, x.name);
    for (const g of agentGroups(allAgents, project.value)) {
      if (g.label) agent.append(h("optgroup", { label: g.label }, ...g.agents.map(opt)));
      else for (const x of g.agents) agent.append(opt(x));
    }
    const names = [...agent.options].map((o) => o.value);
    if (names.includes(keep)) agent.value = keep;
  }
  let userPickedAgent = false;
  let autoApplied = ""; // the agent applyDefaultAgent last selected, "" if none
  function applyDefaultAgent() {
    const names = [...agent.options].map((o) => o.value);
    const def = defaultAgentFor(projects, project.value, names, userPickedAgent);
    if (def) {
      agent.value = def;
      autoApplied = def;
    } else if (!userPickedAgent) {
      // Project agents lead the list but are never auto-selected: fall back
      // to the first global agent, as the flat list did.
      const g = allAgents.find((x) => x.scope !== "project" && names.includes(x.name));
      if (g) agent.value = g.name;
      else if (names.length) agent.value = names[0];
      autoApplied = "";
    }
  }
  agent.addEventListener("change", () => {
    userPickedAgent = true;
    syncSkipReview();
    loadOptions();
  });
  project.addEventListener("change", () => {
    fillAgents();
    applyDefaultAgent();
    loadOptions();
    loadTemplates();
  });

  // loadTemplates lists the templates visible from the project; the picker is
  // hidden when there are none or the list cannot be read.
  let templateSeq = 0;
  async function loadTemplates() {
    if (!withTemplates) return;
    const seq = ++templateSeq;
    const keep = template.value;
    try {
      const res = await get(templatesPath(project.value));
      if (seq !== templateSeq) return;
      templates = res.templates ?? [];
    } catch {
      if (seq !== templateSeq) return;
      templates = [];
    }
    clear(template);
    template.append(h("option", { value: "" }, "None"));
    for (const t of templates) template.append(h("option", { value: t.name }, templateLabel(t)));
    template.value = templates.some((t) => t.name === keep) ? keep : "";
    templateRow.hidden = templates.length === 0;
  }
  template.addEventListener("change", () => {
    const t = templates.find((x) => x.name === template.value);
    if (!t) return;
    const fill = templateFill(t, projects, [...agent.options].map((o) => o.value));
    if (fill.agent) {
      agent.value = fill.agent;
      userPickedAgent = true;
    }
    if (fill.project) project.value = fill.project;
    prompt.value = fill.prompt;
    syncSkipReview();
    if (withOptions) picker.load(agent.value, project.value, fill.preset);
    if (fill.project) loadTemplates();
  });

  Promise.all([get("/v1/agents"), get("/v1/projects")])
    .then(([a, p]) => {
      allAgents = a.agents ?? [];
      for (const x of allAgents) if (x.orchestrator) orchestrators.add(x.name);
      for (const x of p.projects ?? []) project.append(h("option", { value: x.dir }, x.name));
      projects = p.projects ?? [];
      if (dir && projects.some((x) => x.dir === dir)) project.value = dir;
      fillAgents();
      applyDefaultAgent();
      syncSkipReview();
      submit.disabled = !ctx.hasFeature("write");
      loadTemplates();
      return loadOptions();
    })
    .catch((e) => toast(`Could not load agents/projects: ${e.message}`, "error"));

  submit.addEventListener("click", async () => {
    const text = prompt.value.trim();
    if (!agent.value || !project.value || !text) {
      toast("Pick an agent and project, and write a prompt", "error");
      return;
    }
    const extra = withInstructions ? instructions.value.trim() : "";
    const skip = withSkipReview && orchestrators.has(agent.value) && skipReview.checked;
    const n = att ? att.count() : 0;
    const tplName = withTemplates ? template.value : "";
    const [agentName, dir, picked] = [agent.value, project.value, picker.options() ? picker.choice() : {}];
    const confirmed = await confirmSheet({
      title: "Dispatch?",
      lines: confirmLines(buildDispatchRequest(agentName, dir, text, picked, [], extra, skip, tplName).body, n),
      confirmLabel: "Dispatch",
    });
    if (!confirmed) return;
    submit.disabled = true;
    // Upload only now, to the project actually being dispatched to, so a
    // cancelled sheet stores nothing. A failed upload dispatches nothing.
    let images = [];
    if (n) {
      try {
        images = await uploadImages(DISPATCH_UPLOAD_PATH, att.files(), { project_dir: dir });
      } catch (e) {
        toast(`Image upload failed: ${e instanceof ApiError ? e.message : String(e)}`, "error");
        submit.disabled = false;
        return;
      }
    }
    try {
      const res = await send(buildDispatchRequest(agentName, dir, text, picked, images, extra, skip, tplName));
      toast("Dispatched");
      location.hash = res?.window_id ? windowHash(res.session, res.window_id) : "#/";
    } catch (e) {
      toast(e instanceof ApiError ? e.message : String(e), "error");
      submit.disabled = false;
    }
  });
  return () => picker.stop();
}

/**
 * launchPicker renders the backend/model/effort pickers of the launch-options
 * body for an agent in a project into box, shared by the Dispatch and
 * Schedules screens. load(agent, project, preset) fetches the options; preset
 * ({backend, model, effort}, any may be empty) overrides the server defaults
 * for the fields it sets. onChange(choice, opts) fires on every user change.
 * A stale response (a newer load, or stop()) is dropped.
 */
export function launchPicker(box, { onError = () => {}, onChange = () => {}, schedule = false } = {}) {
  let opts = null;
  let choice = {};
  let loadSeq = 0;
  const changed = (c) => {
    choice = c;
    onChange(choice, opts);
  };
  const UNKNOWN = " (unknown)";

  // The schedule form names the resolved defaults and keeps a stored value
  // the lists no longer offer (marked unknown) instead of dropping it.
  const modelLabel = (c) => {
    if (!schedule) return choiceLabel(c);
    if (c.value === "inherit") return "agent default";
    return c.default ? `agent default (${c.value})` : choiceLabel(c);
  };
  const effortLabel = (e) => {
    if (!schedule) return e;
    const d = opts?.defaults?.effort;
    if (e === "inherit") return d && d !== "inherit" ? "inherit" : "default";
    return e === d ? `default (${e})` : e;
  };

  function render() {
    clear(box);
    if (!opts) return;
    const names = (opts.backends ?? []).map((b) => b.name);
    const unknownBackend = schedule && choice.backend && !names.includes(choice.backend);
    const backends = h("div", { class: "seg", role: "group", "aria-label": "Backend" });
    for (const b of opts.backends ?? []) {
      backends.append(
        h(
          "button",
          {
            type: "button",
            class: "btn",
            "aria-pressed": String(choice.backend === b.name),
            onclick: () => {
              changed({ ...choice, backend: b.name, model: modelPicker(opts, b.name).def });
              render();
            },
          },
          b.name,
        ),
      );
    }
    if (unknownBackend) backends.append(h("button", { type: "button", class: "btn", "aria-pressed": "true" }, `${choice.backend}${UNKNOWN}`));
    const mp = modelPicker(opts, choice.backend);
    let model;
    if (mp.freeText) {
      model = h("input", { type: "text", value: choice.model ?? "", autocapitalize: "off", autocorrect: "off", spellcheck: "false" });
      model.addEventListener("input", () => changed({ ...choice, model: model.value.trim() }));
    } else {
      const cur = choice.model ?? mp.def;
      const known = mp.choices.some((c) => c.value === cur);
      model = h("select", { "aria-label": "Model" }, ...mp.choices.map((c) => h("option", { value: c.value }, modelLabel(c))),
        schedule && cur && !known ? h("option", { value: cur, class: "unknown" }, `${cur}${UNKNOWN}`) : null);
      model.value = cur;
      model.addEventListener("change", () => changed({ ...choice, model: model.value }));
    }
    const efforts = opts.efforts ?? [];
    const effort = h("select", { "aria-label": "Effort" }, ...efforts.map((e) => h("option", { value: e }, effortLabel(e))),
      schedule && choice.effort && !efforts.includes(choice.effort) ? h("option", { value: choice.effort, class: "unknown" }, `${choice.effort}${UNKNOWN}`) : null);
    effort.value = choice.effort;
    effort.addEventListener("change", () => changed({ ...choice, effort: effort.value }));
    const rows = [];
    // One backend: nothing to choose (a stored unknown one still shows).
    if (!schedule || names.length > 1 || unknownBackend) rows.push(h("div", {}, h("div", { class: "muted" }, "Backend"), backends));
    rows.push(
      h("label", {}, "Model", model, mp.freeText ? h("span", { class: "muted" }, `${mp.error}; type a model name`) : null),
      h("label", {}, "Effort", effort),
    );
    const accounts = opts.accounts ?? [];
    if (schedule && accounts.length) {
      const cur = choice.account ?? "";
      const account = h("select", { "aria-label": "Account" },
        h("option", { value: "" }, `default (${opts.defaults?.account || "none"})`),
        ...accounts.map((a) => h("option", { value: a }, a)),
        cur && !accounts.includes(cur) ? h("option", { value: cur, class: "unknown" }, `${cur}${UNKNOWN}`) : null);
      account.value = cur;
      account.addEventListener("change", () => changed({ ...choice, account: account.value }));
      rows.push(h("label", {}, "Account", account));
    }
    box.append(...rows);
  }

  return {
    async load(agent, project, preset = {}) {
      if (!agent || !project) return;
      const seq = ++loadSeq;
      opts = null;
      render();
      try {
        const o = await get(launchOptionsPath(agent, project));
        if (seq !== loadSeq) return;
        opts = o;
        choice = { backend: o.defaults.backend, model: o.defaults.model, effort: o.defaults.effort, account: "" };
        for (const k of ["backend", "model", "effort", "account"]) if (preset[k]) choice[k] = preset[k];
        render();
      } catch (e) {
        if (seq === loadSeq) onError(e);
      }
    },
    choice: () => choice,
    options: () => opts,
    stop() {
      loadSeq++;
    },
  };
}
