// Thin fetch wrapper. Every request carries the device cookie (same-origin);
// every non-GET request carries the CSRF header.

export class ApiError extends Error {
  constructor(status, code, message, fields) {
    super(message);
    this.status = status;
    this.code = code;
    // fields are a 422's per-field errors ([{field, message}]).
    this.fields = fields ?? [];
  }
}

/**
 * onPhoneOff handles a 503 phone_off: phone access was switched off on the
 * laptop. Reloading lands on the server's "phone access is off" page, which
 * retries by itself and brings the app back once it is on again.
 */
function onPhoneOff(status, data) {
  if (status === 503 && data?.code === "phone_off" && typeof location !== "undefined") location.reload();
}

/**
 * checkPhoneOff asks /healthz (no cookie needed) whether phone access is off
 * and, if so, reloads into the "phone access is off" page. For callers that
 * cannot read a status, such as an EventSource error.
 */
export async function checkPhoneOff() {
  try {
    const res = await fetch("/healthz", { credentials: "same-origin", cache: "no-store" });
    const data = await res.json();
    if (data?.phone_off) onPhoneOff(503, { code: "phone_off" });
  } catch {
    // offline or not JSON: the normal reconnect handles it
  }
}

/** api performs a JSON request and returns the parsed body (null for 204). keepalive survives page unload. */
export async function api(method, path, body, { keepalive = false } = {}) {
  const headers = { Accept: "application/json" };
  if (method !== "GET") headers["X-Agentctl-CSRF"] = "1";
  const init = { method, headers, credentials: "same-origin", cache: "no-store" };
  // keepalive lets the request outlive the page (a delete flushed on unload).
  if (keepalive) init.keepalive = true;
  if (body !== undefined) {
    headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(body);
  }
  const res = await fetch(path, init);
  if (res.status === 204) return null;
  let data = null;
  try {
    data = await res.json();
  } catch {
    // non-JSON body: fall through to the status check
  }
  if (!res.ok) {
    onPhoneOff(res.status, data);
    throw new ApiError(res.status, data?.code ?? "http_error", data?.error ?? `HTTP ${res.status}`, data?.fields);
  }
  return data;
}

export const get = (path) => api("GET", path);

/** send runs a request built by logic.js (method, path, body). */
export const send = (req) => api(req.method, req.path, req.body);

/**
 * uploadImages POSTs files as multipart "image" parts and returns the stored
 * image IDs. fields are text parts sent first (the dispatch upload's project_dir).
 */
export async function uploadImages(path, files, fields = {}) {
  const form = new FormData();
  for (const [k, v] of Object.entries(fields)) form.append(k, v);
  for (const f of files) form.append("image", f, "image");
  const res = await fetch(path, {
    method: "POST",
    headers: { Accept: "application/json", "X-Agentctl-CSRF": "1" },
    body: form,
    credentials: "same-origin",
    cache: "no-store",
  });
  let data = null;
  try {
    data = await res.json();
  } catch {
    // non-JSON body: fall through to the status check
  }
  if (!res.ok) {
    onPhoneOff(res.status, data);
    throw new ApiError(res.status, data?.code ?? "http_error", data?.error ?? `HTTP ${res.status}`);
  }
  return data.images;
}

/**
 * getBlob GETs a binary response (the artifact PDF) with the device cookie and
 * returns {blob, disposition}. A failure throws ApiError with the server's
 * JSON message, like api. signal aborts the request.
 */
export async function getBlob(path, { signal } = {}) {
  const res = await fetch(path, { credentials: "same-origin", cache: "no-store", signal });
  if (!res.ok) {
    let data = null;
    try {
      data = await res.json();
    } catch {
      // non-JSON body: fall through
    }
    onPhoneOff(res.status, data);
    throw new ApiError(res.status, data?.code ?? "http_error", data?.error ?? `HTTP ${res.status}`);
  }
  return { blob: await res.blob(), disposition: res.headers.get("Content-Disposition") ?? "" };
}
