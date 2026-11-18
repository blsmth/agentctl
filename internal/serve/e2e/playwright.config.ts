// Browser tests for the phone web app. The Go dev server
// (internal/serve/servetest/devserver) runs the real serve handler over a
// fixture fleet; see docs/development.md ("Browser tests").
import { defineConfig, devices } from "@playwright/test";

const PORT = Number(process.env.E2E_PORT ?? 8797);
const baseURL = `http://127.0.0.1:${PORT}`;

export default defineConfig({
  testDir: "./tests",
  // One dev server, shared state (fixture, recorded actions): run serially.
  fullyParallel: false,
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  timeout: 30_000,
  expect: { timeout: 5_000 },
  reporter: [["list"], ["html", { open: "never" }]],
  use: {
    baseURL,
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  projects: [
    {
      // WebKit is the engine iOS Safari uses; the descriptor sets the iPhone
      // viewport, user agent, isMobile and hasTouch.
      name: "webkit-iphone",
      use: { ...devices["iPhone 15"] },
    },
  ],
  webServer: {
    command: `go run ../servetest/devserver -addr 127.0.0.1:${PORT}`,
    url: `${baseURL}/healthz`,
    reuseExistingServer: !process.env.CI,
    timeout: 180_000,
    stdout: "pipe",
    stderr: "pipe",
  },
});
