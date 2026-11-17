// Service worker: web push + a small app-shell cache. It never touches /v1/.
const CACHE = "agentctl-shell-v1";
const SHELL = [
  "/", "/app.css", "/app.js", "/api.js", "/live.js", "/logic.js", "/ui.js",
  "/fleet.js", "/window.js", "/dispatch.js", "/settings.js",
  "/vendor/xterm.js", "/vendor/xterm.css", "/vendor/addon-fit.js",
  "/icons/icon-192.png", "/icons/icon-512.png",
];

self.addEventListener("install", (event) => {
  event.waitUntil(
    caches.open(CACHE).then((c) => Promise.allSettled(SHELL.map((u) => c.add(u)))).then(() => self.skipWaiting()),
  );
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  );
});

// Network first, cache as the offline fallback, for shell assets only.
self.addEventListener("fetch", (event) => {
  const req = event.request;
  const url = new URL(req.url);
  if (req.method !== "GET" || url.origin !== self.location.origin) return;
  if (url.pathname.startsWith("/v1/") || url.pathname === "/healthz" || url.pathname === "/pair") return;
  if (!SHELL.includes(url.pathname)) return;
  event.respondWith(
    fetch(req)
      .then((res) => {
        if (res.ok) {
          const copy = res.clone();
          caches.open(CACHE).then((c) => c.put(req, copy));
        }
        return res;
      })
      .catch(() => caches.match(req)),
  );
});

self.addEventListener("push", (event) => {
  let data = {};
  try {
    data = event.data ? event.data.json() : {};
  } catch {
    data = { body: event.data ? event.data.text() : "" };
  }
  event.waitUntil(
    self.registration.showNotification(data.title || "agentctl", {
      body: data.body || "",
      tag: data.tag || undefined,
      icon: "/icons/icon-192.png",
      data: { url: data.url || "/" },
    }),
  );
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const url = new URL(event.notification.data?.url || "/", self.location.origin).href;
  event.waitUntil(
    self.clients.matchAll({ type: "window", includeUncontrolled: true }).then((list) => {
      const existing = list.find((c) => new URL(c.url).origin === self.location.origin);
      if (existing) {
        existing.postMessage({ type: "navigate", url });
        return existing.focus();
      }
      return self.clients.openWindow(url);
    }),
  );
});
