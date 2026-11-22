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

/** api performs a JSON request and returns the parsed body (null for 204). */
export async function api(method, path, body) {
  const headers = { Accept: "application/json" };
  if (method !== "GET") headers["X-Agentctl-CSRF"] = "1";
  const init = { method, headers, credentials: "same-origin", cache: "no-store" };
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
  if (!res.ok) throw new ApiError(res.status, data?.code ?? "http_error", data?.error ?? `HTTP ${res.status}`);
  return data.images;
}
