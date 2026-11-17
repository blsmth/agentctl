// Dispatch screen: agent + project + prompt, with a confirm sheet.
import { ApiError, get, send } from "./api.js";
import { buildDispatchRequest, windowHash } from "./logic.js";
import { confirmSheet, h, toast } from "./ui.js";

export function mountDispatch(root, ctx) {
  const agent = h("select", { "aria-label": "Agent" });
  const project = h("select", { "aria-label": "Project" });
  const prompt = h("textarea", { rows: "8", placeholder: "What should the agent do?" });
  const submit = h("button", { class: "btn primary", disabled: true }, "Review & send");
  root.append(
    h("header", { class: "bar" }, h("a", { class: "btn", href: "#/" }, "‹ Fleet"), h("h1", {}, "Dispatch")),
    h(
      "form",
      { class: "form", onsubmit: (e) => e.preventDefault() },
      h("label", {}, "Agent", agent),
      h("label", {}, "Project", project),
      h("label", {}, "Prompt", prompt),
      ctx.hasFeature("write") ? null : h("p", { class: "muted" }, "Write actions are not enabled on this server."),
      submit,
    ),
  );

  Promise.all([get("/v1/agents"), get("/v1/projects")])
    .then(([a, p]) => {
      for (const x of a.agents ?? []) agent.append(h("option", { value: x.name }, x.name));
      for (const x of p.projects ?? []) project.append(h("option", { value: x.dir }, x.name));
      submit.disabled = !ctx.hasFeature("write");
    })
    .catch((e) => toast(`Could not load agents/projects: ${e.message}`, "error"));

  submit.addEventListener("click", async () => {
    const text = prompt.value.trim();
    if (!agent.value || !project.value || !text) {
      toast("Pick an agent and project, and write a prompt", "error");
      return;
    }
    const req = buildDispatchRequest(agent.value, project.value, text);
    const ok = await confirmSheet({
      title: "Dispatch?",
      lines: [`agent: ${req.body.agent}\nproject: ${req.body.project_dir}`, req.body.prompt],
      confirmLabel: "Dispatch",
    });
    if (!ok) return;
    submit.disabled = true;
    try {
      const res = await send(req);
      toast("Dispatched");
      location.hash = res?.window_id ? windowHash(res.session, res.window_id) : "#/";
    } catch (e) {
      toast(e instanceof ApiError ? e.message : String(e), "error");
      submit.disabled = false;
    }
  });
  return () => {};
}
