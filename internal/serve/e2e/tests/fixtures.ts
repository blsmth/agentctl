// Shared test fixtures: a reset dev server, the paired device's cookie, the
// control API, and a guard that fails any test whose page logged an error or
// had a same-origin request fail.
import { test as base, expect, type APIRequestContext, type Page } from "@playwright/test";

export { expect };

/** Action mirrors servetest.Action: one side effect the server performed. */
export type Action = {
  kind: "close" | "key" | "reply" | "queue" | "dispatch" | "approval" | "sweep" | "resume";
  session?: string;
  window?: string;
  key?: string;
  text?: string;
  agent?: string;
  id?: string;
  decision?: string;
  instructions?: string;
};

export type FixtureWindow = {
  id: string;
  index: number;
  agent: string;
  state: "idle" | "working" | "blocked";
  done?: boolean;
  mode?: string;
  parent?: string;
  activity?: string;
  pane?: "idle" | "working" | "dialog" | "trust";
  branch?: string;
  context_pct?: number;
  title?: string;
  issue?: number;
  model?: string;
  cost_usd?: number;
  pr_url?: string;
  scheduled_by?: string;
  recap?: string;
  recap_source?: "summary" | "task" | "title" | "activity";
  short_title?: string;
  prompt?: string;
  task?: string;
  short_kind?: string;
  pending_tool?: string;
  pending_detail?: string;
};

export type FixtureGroup = {
  session: string;
  project: string;
  dir: string;
  windows: FixtureWindow[];
  shells?: { id: string; index: number; dir: string }[];
};

export type SchedulesSpec = {
  jobs?: Record<string, unknown>[];
  runs?: Record<string, unknown>[];
  not_installed?: boolean;
  cron_disabled?: boolean;
  run?: Record<string, unknown>;
  /** templates are written as the project's prompt templates. */
  templates?: { name: string; prompt: string; agent?: string; description?: string; model?: string; effort?: string }[];
};

/** ClosedSpec mirrors servetest.ClosedSpec: one closed-ledger record. */
export type ClosedSpec = {
  session_id: string;
  agent: string;
  project: string;
  title: string;
  ago_minutes: number;
  pr_number?: number;
  pr_url?: string;
  /** gone leaves the project dir missing, so the record is not resumable. */
  gone?: boolean;
};

export type ArtifactSpec = {
  title: string;
  description?: string;
  kind?: string;
  pin?: boolean;
  project?: string;
  project_dir?: string;
  agent?: string;
  window_id?: string;
  session?: string;
  /** revisions are published in order; files are by relative path ("base64:" values are decoded). */
  revisions: { files: Record<string, string>; entry: string; note?: string }[];
};

/** Control wraps the dev server's /__test/ API. */
export class Control {
  constructor(private readonly request: APIRequestContext) {}

  private async call<T>(method: string, path: string, data?: unknown): Promise<T> {
    const res = await this.request.fetch(`/__test/${path}`, { method, data });
    expect(res.ok(), `${method} /__test/${path}: ${res.status()} ${await res.text()}`).toBeTruthy();
    return (await res.json()) as T;
  }

  reset() {
    return this.call("POST", "reset");
  }

  async session(): Promise<{ cookie: string; token: string }> {
    return this.call("GET", "session");
  }

  async actions(): Promise<Action[]> {
    return (await this.call<{ actions: Action[] }>("GET", "actions")).actions;
  }

  async fleet(): Promise<FixtureGroup[]> {
    return (await this.call<{ groups: FixtureGroup[] }>("GET", "fleet")).groups;
  }

  setFleet(groups: FixtureGroup[]) {
    return this.call("PUT", "fleet", { groups });
  }

  useFixture(name: string) {
    return this.call("PUT", "fleet", { fixture: name });
  }

  /** patchWindow changes fields of one fixture window; the bus publishes it within a refresh (50ms). */
  patchWindow(session: string, id: string, patch: Partial<Omit<FixtureWindow, "id">> & { pane_text?: string }) {
    return this.call("POST", "window", { session, id, ...patch });
  }

  /**
   * seedSchedules replaces the cron jobs and runs the API sees. "{dir}" in a
   * job's dir becomes a real project directory, and a fleet group with that
   * dir is set so the project picker offers it. Returns the dir.
   */
  async seedSchedules(spec: SchedulesSpec = {}): Promise<string> {
    const { dir } = await this.call<{ dir: string }>("PUT", "schedules", {});
    const jobs = (spec.jobs ?? []).map((j) => ({ ...j, dir: String(j.dir ?? "{dir}").replace("{dir}", dir) }));
    await this.call("PUT", "schedules", { ...spec, jobs });
    await this.setFleet([
      { session: "agentctl-sched", project: "sched", dir, windows: [{ id: "@90", index: 1, agent: "go-engineer", state: "idle" }] },
    ]);
    return dir;
  }

  /** scheduleRuns are the job names run-now fired since the last seed. */
  async scheduleRuns(): Promise<string[]> {
    return (await this.call<{ runs: string[] }>("GET", "schedules")).runs;
  }

  /** seedArtifacts replaces the artifact store; returns the new ids in order. */
  async seedArtifacts(artifacts: ArtifactSpec[]): Promise<string[]> {
    return (await this.call<{ ids: string[] }>("PUT", "artifacts", { artifacts })).ids;
  }

  /** seedClosed replaces the closed ledger /v1/closed lists. */
  seedClosed(closed: ClosedSpec[]) {
    return this.call("PUT", "closed", { closed });
  }

  async openApproval(spec: Record<string, unknown>): Promise<string> {
    return (await this.call<{ id: string }>("POST", "approvals", spec)).id;
  }

  /** waitForActions polls until the recorded actions satisfy pred, and returns them. */
  async waitForActions(pred: (a: Action[]) => boolean, timeout = 5000): Promise<Action[]> {
    let last: Action[] = [];
    await expect
      .poll(async () => {
        last = await this.actions();
        return pred(last);
      }, { timeout })
      .toBe(true);
    return last;
  }
}

/** Guard records page errors; a test may allow specific expected ones. */
export class Guard {
  readonly problems: string[] = [];
  private readonly allowed: RegExp[] = [];

  /** allow ignores problems matching re (e.g. a 409 the test provokes). */
  allow(re: RegExp) {
    this.allowed.push(re);
  }

  add(p: string) {
    this.problems.push(p);
  }

  unexpected(): string[] {
    return this.problems.filter((p) => !this.allowed.some((re) => re.test(p)));
  }

  attach(page: Page, origin: string) {
    page.on("console", (m) => {
      if (m.type() === "error") this.add(`console error: ${m.text()}`);
    });
    page.on("pageerror", (e) => this.add(`page error: ${e.message}`));
    page.on("requestfailed", (r) => {
      // The SSE stream is aborted when a test ends or the page navigates.
      if (new URL(r.url()).pathname === "/v1/events") return;
      if (r.url().startsWith(origin)) this.add(`request failed: ${r.method()} ${r.url()} (${r.failure()?.errorText})`);
    });
    page.on("response", (r) => {
      if (r.url().startsWith(origin) && r.status() >= 400) {
        this.add(`HTTP ${r.status()}: ${r.request().method()} ${new URL(r.url()).pathname}`);
      }
    });
  }
}

type Fixtures = { control: Control; guard: Guard };

export const test = base.extend<Fixtures>({
  // Auto, and a dependency of context, so the reset lands before any page
  // (including one opened in beforeEach) loads.
  control: [
    async ({ request }, use) => {
      const control = new Control(request);
      await control.reset();
      await use(control);
    },
    { auto: true },
  ],
  // The paired device's cookie, seeded the way pairing leaves it (minus
  // Secure: the dev server is plain HTTP on loopback).
  context: async ({ context, control, baseURL }, use) => {
    const { cookie, token } = await control.session();
    await context.addCookies([{ name: cookie, value: token, url: baseURL!, httpOnly: true, sameSite: "Strict" }]);
    await use(context);
  },
  guard: [
    async ({ page, baseURL }, use, testInfo) => {
      const guard = new Guard();
      guard.attach(page, baseURL!);
      await use(guard);
      if (testInfo.status === testInfo.expectedStatus) {
        expect(guard.unexpected(), "page errors and failed same-origin requests").toEqual([]);
      }
    },
    { auto: true },
  ],
});

/** openFleet loads the app and waits for the fixture fleet to render. */
export async function openFleet(page: Page) {
  await page.goto("/");
  await expect(page.locator("header.bar h1")).toHaveText("Fleet");
  await expect(page.locator(".fleet section.group").first()).toBeVisible();
}

/** row is a fleet row by window ID. */
export function row(page: Page, windowId: string) {
  return page.locator(`.fleet a.win[href$="/${encodeURIComponent(windowId)}"]`);
}
