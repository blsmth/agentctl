// Dispatch screen: agent + project + prompt, launch options, and a confirm sheet.
import { ApiError, get, send, uploadImages } from "./api.js";
import { imagePicker } from "./attach.js";
import {
  DISPATCH_UPLOAD_PATH, buildDispatchRequest, choiceLabel, confirmLines, defaultAgentFor, launchOptionsPath, modelPicker, windowHash,
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
  const submit = h("button", { class: "btn primary", disabled: true }, "Review & send");
  const launchBox = h("div", { class: "launch-opts" });
  const withOptions = ctx.hasFeature("launch-options");
  // Attaching a screenshot needs writes and the dispatch upload route.
  const att = ctx.hasFeature("write") && ctx.hasFeature("dispatch-upload") ? imagePicker() : null;
  root.append(
    h("header", { class: "bar" }, h("a", { class: "btn", href: "#/" }, "‹ Fleet"), h("h1", {}, "Dispatch")),
    h(
      "form",
      { class: "form", onsubmit: (e) => e.preventDefault() },
      h("label", {}, "Agent", agent),
      h("label", {}, "Project", project),
      withOptions ? launchBox : null,
      h("label", {}, "Prompt", prompt),
      withInstructions
        ? h("details", { class: "instructions" }, h("summary", {}, "Additional instructions"), instructions)
        : null,
      ...(att ? att.controls : []),
      ctx.hasFeature("write") ? null : h("p", { class: "muted" }, "Write actions are not enabled on this server."),
      submit,
    ),
  );

  // opts is the launch-options body for the current agent/project; choice is
  // what the pickers currently select. loadSeq drops stale responses.
  let opts = null;
  let choice = {};
  let loadSeq = 0;

  function renderOptions() {
    clear(launchBox);
    if (!opts) return;
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
              choice = { ...choice, backend: b.name, model: modelPicker(opts, b.name).def };
              renderOptions();
            },
          },
          b.name,
        ),
      );
    }
    const mp = modelPicker(opts, choice.backend);
    let model;
    if (mp.freeText) {
      model = h("input", { type: "text", value: choice.model ?? "", autocapitalize: "off", autocorrect: "off", spellcheck: "false" });
      model.addEventListener("input", () => (choice = { ...choice, model: model.value.trim() }));
    } else {
      model = h("select", { "aria-label": "Model" }, ...mp.choices.map((c) => h("option", { value: c.value }, choiceLabel(c))));
      model.value = choice.model ?? mp.def;
      model.addEventListener("change", () => (choice = { ...choice, model: model.value }));
    }
    const effort = h("select", { "aria-label": "Effort" }, ...(opts.efforts ?? []).map((e) => h("option", { value: e }, e)));
    effort.value = choice.effort;
    effort.addEventListener("change", () => (choice = { ...choice, effort: effort.value }));
    launchBox.append(
      h("div", {}, h("div", { class: "muted" }, "Backend"), backends),
      h("label", {}, "Model", model, mp.freeText ? h("span", { class: "muted" }, `${mp.error}; type a model name`) : null),
      h("label", {}, "Effort", effort),
    );
  }

  async function loadOptions() {
    if (!withOptions || !agent.value || !project.value) return;
    const seq = ++loadSeq;
    opts = null;
    renderOptions();
    try {
      const o = await get(launchOptionsPath(agent.value, project.value));
      if (seq !== loadSeq) return;
      opts = o;
      choice = { backend: o.defaults.backend, model: o.defaults.model, effort: o.defaults.effort };
      renderOptions();
    } catch (e) {
      if (seq === loadSeq) toast(`Could not load launch options: ${e.message}`, "error");
    }
  }
  // projects is the loaded /v1/projects list; userPickedAgent stops a later
  // project change from overriding a hand-picked agent.
  let projects = [];
  let userPickedAgent = false;
  let autoApplied = ""; // the agent applyDefaultAgent last selected, "" if none
  function applyDefaultAgent() {
    const names = [...agent.options].map((o) => o.value);
    const def = defaultAgentFor(projects, project.value, names, userPickedAgent);
    if (def) {
      agent.value = def;
      autoApplied = def;
    } else if (autoApplied && !userPickedAgent && agent.value === autoApplied && names.length) {
      // The previous project's default no longer applies: back to the first agent.
      agent.value = names[0];
      autoApplied = "";
    }
  }
  agent.addEventListener("change", () => {
    userPickedAgent = true;
    loadOptions();
  });
  project.addEventListener("change", () => {
    applyDefaultAgent();
    loadOptions();
  });

  Promise.all([get("/v1/agents"), get("/v1/projects")])
    .then(([a, p]) => {
      for (const x of a.agents ?? []) agent.append(h("option", { value: x.name }, x.name));
      for (const x of p.projects ?? []) project.append(h("option", { value: x.dir }, x.name));
      projects = p.projects ?? [];
      if (dir && projects.some((x) => x.dir === dir)) project.value = dir;
      applyDefaultAgent();
      submit.disabled = !ctx.hasFeature("write");
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
    const n = att ? att.count() : 0;
    const [agentName, dir, picked] = [agent.value, project.value, opts ? choice : {}];
    const confirmed = await confirmSheet({
      title: "Dispatch?",
      lines: confirmLines(buildDispatchRequest(agentName, dir, text, picked, [], extra).body, n),
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
      const res = await send(buildDispatchRequest(agentName, dir, text, picked, images, extra));
      toast("Dispatched");
      location.hash = res?.window_id ? windowHash(res.session, res.window_id) : "#/";
    } catch (e) {
      toast(e instanceof ApiError ? e.message : String(e), "error");
      submit.disabled = false;
    }
  });
  return () => {
    loadSeq++;
  };
}
