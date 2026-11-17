// agentctl bridge extension for pi.
//
// Written by agentctl into its runtime dir at every launch and loaded with
// `pi -e`; edits are overwritten. It forwards pi lifecycle events to
// `agentctl hook-<event> --harness pi` with a JSON payload on stdin, the same
// way Claude Code runs agentctl's hooks, so window state (working / idle /
// done), the bash gate and mailbox delivery work in pi windows.
//
// pi.exec() cannot write to a child's stdin, so hooks are spawned with
// node:child_process directly. No other dependencies.
// @ts-nocheck

import { spawn } from "node:child_process";

const BIN = process.env.AGENTCTL_BIN || "agentctl";

// hook-validate-bash may hold an approval popup open for up to 9 minutes
// before it denies on its own; the other hooks are quick.
const GATE_TIMEOUT_MS = 600_000;
const HOOK_TIMEOUT_MS = 60_000;

function enabled() {
  return process.env.AGENTCTL === "1";
}

type HookResult = { code: number; stdout: string; stderr: string };

function hook(name: string, payload: unknown, timeoutMs = HOOK_TIMEOUT_MS): Promise<HookResult> {
  return new Promise((resolve) => {
    let child;
    try {
      child = spawn(BIN, [name, "--harness", "pi"], { stdio: ["pipe", "pipe", "pipe"], env: process.env });
    } catch (err) {
      resolve({ code: -1, stdout: "", stderr: String(err) });
      return;
    }
    let stdout = "";
    let stderr = "";
    let done = false;
    const finish = (code: number) => {
      if (done) return;
      done = true;
      clearTimeout(timer);
      resolve({ code, stdout, stderr });
    };
    const timer = setTimeout(() => {
      try {
        child.kill("SIGTERM");
      } catch {}
      finish(-1);
    }, timeoutMs);
    child.stdout?.on("data", (d) => (stdout += d));
    child.stderr?.on("data", (d) => (stderr += d));
    child.on("error", (err) => {
      stderr += String(err);
      finish(-1);
    });
    child.on("close", (code) => finish(code ?? -1));
    child.stdin?.on("error", () => {});
    child.stdin?.end(JSON.stringify(payload));
  });
}

// output parses the last JSON line a hook printed.
function output(res: HookResult): Record<string, any> {
  const lines = res.stdout.trim().split("\n");
  try {
    return JSON.parse(lines[lines.length - 1] || "{}") ?? {};
  } catch {
    return {};
  }
}

function session(ctx) {
  let id = "";
  let file = "";
  try {
    id = ctx?.sessionManager?.getSessionId?.() ?? "";
  } catch {}
  try {
    file = ctx?.sessionManager?.getSessionFile?.() ?? "";
  } catch {}
  return { session_id: id, session_file: file, cwd: ctx?.cwd ?? process.cwd() };
}

function textOf(content): string {
  if (typeof content === "string") return content;
  if (!Array.isArray(content)) return "";
  return content
    .filter((b) => b?.type === "text" && typeof b.text === "string")
    .map((b) => b.text)
    .join("\n");
}

export default function (pi) {
  if (!enabled()) return;

  // An agentctl backend (config `backends`) arrives as a provider to
  // register before pi resolves --provider/--model. Its apiKey is a
  // "!agentctl backend-token <name>" command, never the key itself.
  const provider = process.env.AGENTCTL_PI_PROVIDER;
  if (provider) {
    try {
      const p = JSON.parse(provider);
      pi.registerProvider(p.name, p.config);
    } catch (err) {
      console.error(`agentctl: cannot register backend provider: ${err}`);
    }
  }

  let lastText = "";
  // Set when hook-check-done refused the stop and a continuation was sent,
  // so the next settle reports stop_hook_active (as Claude Code's Stop hook
  // does) and cannot loop.
  let continued = false;

  // inject delivers text a hook returned (agentctl mailbox notices) into
  // the conversation.
  const inject = (res: HookResult, ctx) => {
    const text = output(res).context;
    if (typeof text !== "string" || text === "") return;
    const idle = ctx?.isIdle?.() !== false;
    pi.sendMessage(
      { customType: "agentctl", content: text, display: true },
      { deliverAs: idle ? "nextTurn" : "steer" },
    );
  };

  pi.on("session_start", async (event, ctx) => {
    if (ctx?.mode !== "tui") return;
    const agent = process.env.AGENTCTL_AGENT_NAME;
    if (agent && event?.reason !== "reload") {
      // Tag the session with its agent so agentctl's session and cost views
      // can attribute it.
      let tagged = false;
      try {
        tagged = (ctx.sessionManager.getEntries?.() ?? []).some(
          (e) => e?.type === "custom" && e?.customType === "agentctl",
        );
      } catch {}
      if (!tagged) pi.appendEntry("agentctl", { agent });
    }
    await hook("hook-session-start", { ...session(ctx), reason: event?.reason ?? "" });
  });

  pi.on("input", (event) => {
    if (event?.source === "interactive") continued = false;
  });

  pi.on("agent_start", (_event, ctx) => {
    void hook("hook-user-prompt", session(ctx)).then((res) => inject(res, ctx));
  });

  pi.on("tool_call", async (event, ctx) => {
    if (event?.parentToolCallId) return;
    const payload = {
      ...session(ctx),
      tool_name: event.toolName,
      tool_call_id: event.toolCallId,
      input: event.input ?? {},
    };
    void hook("hook-pre-tool", payload);
    if (event.toolName !== "bash") return;
    const res = await hook("hook-validate-bash", payload, GATE_TIMEOUT_MS);
    const out = output(res);
    if (out.block === true || res.code === 2) {
      return { block: true, reason: out.reason || res.stderr.trim() || "Blocked by agentctl" };
    }
  });

  pi.on("tool_result", (event, ctx) => {
    if (event?.parentToolCallId) return;
    const name = event.isError ? "hook-post-tool-failure" : "hook-post-tool";
    void hook(name, {
      ...session(ctx),
      tool_name: event.toolName,
      tool_call_id: event.toolCallId,
      input: event.input ?? {},
      content: textOf(event.content).slice(0, 20000),
      is_error: !!event.isError,
    }).then((res) => inject(res, ctx));
  });

  pi.on("agent_end", (event) => {
    const msgs = Array.isArray(event?.messages) ? event.messages : [];
    for (let i = msgs.length - 1; i >= 0; i--) {
      if (msgs[i]?.role === "assistant") {
        lastText = textOf(msgs[i].content);
        break;
      }
    }
  });

  // agent_settled: pi will not continue on its own. This is the turn end
  // (Claude Code's Stop): idle and done detection run here.
  pi.on("agent_settled", async (_event, ctx) => {
    if (ctx?.isIdle?.() === false) return;
    const stopActive = continued;
    continued = false;
    const res = await hook("hook-check-done", {
      ...session(ctx),
      last_assistant_message: lastText,
      stop_hook_active: stopActive,
    });
    const reason = res.stderr.trim();
    if (res.code === 2 && reason !== "") {
      continued = true;
      pi.sendUserMessage(reason);
    }
  });

  pi.on("session_shutdown", async (event, ctx) => {
    if (event?.reason === "reload") return;
    await hook("hook-session-end", { ...session(ctx), reason: event?.reason ?? "" }, 15_000);
  });
}
