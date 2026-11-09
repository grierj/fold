// The one fetch client for /api/v1. Cookie session auth; the CSRF token is
// fetched from GET /api/v1/auth/csrf and sent as X-CSRF-Token on every unsafe
// request. Every request, whatever it reads back (JSON, a file, a 304), goes
// through send() so a 401, an unreadable body and the CSRF rules behave the
// same everywhere.

let csrfToken: string | null = null;
let onUnauthorized: (() => void) | null = null;

// Called once before the first render (main.tsx), so no request can run
// before the sign-out handler is known. Tests call it too, with null to clear.
export function configureApi(config: { onUnauthorized: (() => void) | null }) {
  onUnauthorized = config.onUnauthorized;
}

// A resource id in a URL path: a number from a loaded row, or the string a
// route handed over. path() encodes either the same way.
export type Id = number | string;

// path`/groups/${id}/members/${userId}` encodes every interpolated value as a
// path segment, so an id or token with "/", "?" or "#" in it stays one segment.
export function path(strings: TemplateStringsArray, ...values: Array<string | number>): string {
  return strings.reduce((out, s, i) => out + s + (i < values.length ? encodeURIComponent(String(values[i])) : ""), "");
}

type QueryValue = string | number | boolean | null | undefined | readonly string[];

// query({ a: "x y", tag: ["p", "q"] }) is "?a=x%20y&tag=p&tag=q". Absent, null,
// empty and false values are left out; true is sent as "1". Returns "" when
// nothing is left, so it appends to any path.
export function query(params: Record<string, QueryValue>): string {
  const parts: string[] = [];
  const add = (key: string, value: string | number | boolean) => {
    parts.push(`${encodeURIComponent(key)}=${encodeURIComponent(value === true ? "1" : String(value))}`);
  };
  for (const [key, value] of Object.entries(params)) {
    if (Array.isArray(value)) {
      for (const item of value) if (item) add(key, item);
    } else if (value !== undefined && value !== null && value !== "" && value !== false) {
      add(key, value as string | number | boolean);
    }
  }
  return parts.length ? `?${parts.join("&")}` : "";
}

// An error response. `code` is the API's stable error code; show it with
// errorText (lib/errors.ts), never the message, which is the server's
// English for logs.
export class ApiError extends Error {
  status: number;
  code: string;
  data: Record<string, unknown>;
  // True when the failure status came with a body that is not a JSON object (a
  // gateway's page, an empty body): the server did not say this, so a caller
  // that must know what happened (the delete panel) treats it as uncertain.
  unreadable: boolean;
  constructor(status: number, message: string, data: Record<string, unknown> = {}, unreadable = false) {
    super(message);
    this.status = status;
    this.data = data;
    this.unreadable = unreadable;
    this.code = typeof data.code === "string" ? data.code : status >= 500 ? "server_error" : "invalid";
  }
}

export async function ensureCsrf(): Promise<string> {
  if (csrfToken) return csrfToken;
  const res = await fetch("/api/v1/auth/csrf", { credentials: "include" });
  if (!res.ok) throw new ApiError(res.status, "failed to fetch CSRF token");
  const data = await res.json();
  csrfToken = data.csrfToken;
  return csrfToken as string;
}

interface SendOptions {
  method?: string;
  headers?: Record<string, string>;
  body?: BodyInit;
  cache?: RequestCache;
  anonymousOK?: boolean;
  rotatesCsrf?: boolean;
  // A 304 is an answer, not a failure (the badge's ETag poll).
  notModifiedOK?: boolean;
}

// Reads a body without throwing: `data` is the JSON value (or {} when there is
// none), `readable` is true only for a non-empty JSON object.
async function readBody(res: Response): Promise<{ data: unknown; readable: boolean }> {
  const text = await res.text();
  if (!text) return { data: {}, readable: false };
  try {
    const data = JSON.parse(text) as unknown;
    return { data, readable: typeof data === "object" && data !== null && !Array.isArray(data) };
  } catch {
    // A proxy's HTML error page: keep the status, drop the body.
    return { data: {}, readable: false };
  }
}

function objectOrEmpty(data: unknown): Record<string, unknown> {
  return typeof data === "object" && data !== null && !Array.isArray(data) ? (data as Record<string, unknown>) : {};
}

// send is the only place that calls fetch for the API. It returns a response
// whose status is acceptable and whose body is unread; every failure is an
// ApiError (a thrown fetch error, such as a network failure, passes through).
async function send(url: string, opts: SendOptions = {}): Promise<Response> {
  const method = (opts.method ?? "GET").toUpperCase();
  const headers: Record<string, string> = { ...opts.headers };
  if (method !== "GET" && method !== "HEAD") {
    headers["X-CSRF-Token"] = await ensureCsrf();
  }
  const res = await fetch(`/api/v1${url}`, {
    method,
    headers,
    body: opts.body,
    credentials: "include",
    ...(opts.cache ? { cache: opts.cache } : {}),
  });
  // The server rotates the CSRF token when a session starts or ends, so the
  // next unsafe request fetches a fresh one (M7.2 slice b).
  if (opts.rotatesCsrf) csrfToken = null;
  if (res.status === 401) {
    if (onUnauthorized && !opts.anonymousOK) onUnauthorized();
    throw new ApiError(401, "unauthorized", { code: "signed_out", ...objectOrEmpty((await readBody(res)).data) });
  }
  if (res.status === 304 && opts.notModifiedOK) return res;
  if (!res.ok) {
    const { data, readable } = await readBody(res);
    const body = objectOrEmpty(data);
    const message = typeof body.error === "string" ? body.error : `request failed (${res.status})`;
    throw new ApiError(res.status, message, body, !readable);
  }
  return res;
}

export interface RequestOptions {
  method?: string;
  body?: unknown;
  // anonymousOK: a 401 just means "not signed in" (the boot-time session
  // check), so don't send the visitor to /login; public pages stay public.
  anonymousOK?: boolean;
  // rotatesCsrf: this request starts or ends a session (sign-in, sign-out,
  // a token that signs someone in).
  rotatesCsrf?: boolean;
}

// A JSON request. A success body that is not JSON reads as {}.
export async function api<T>(url: string, opts: RequestOptions = {}): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" };
  let body: string | undefined;
  if (opts.body !== undefined) {
    headers["Content-Type"] = "application/json";
    body = JSON.stringify(opts.body);
  }
  const res = await send(url, { method: opts.method, headers, body, anonymousOK: opts.anonymousOK, rotatesCsrf: opts.rotatesCsrf });
  return (await readBody(res)).data as T;
}

// Multipart upload: the browser sets Content-Type with the boundary.
export async function apiUpload<T>(url: string, field: string, file: File): Promise<T> {
  const form = new FormData();
  form.append(field, file);
  const res = await send(url, { method: "POST", headers: { Accept: "application/json" }, body: form });
  return (await readBody(res)).data as T;
}

// CSV reads reject JSON/proxy answers before any downloadable blob exists.
export async function apiCSV(url: string): Promise<Blob> {
  const res = await send(url, { headers: { Accept: "text/csv" }, cache: "no-store" });
  if (res.headers.get("Content-Type")?.split(";")[0].trim().toLowerCase() !== "text/csv") {
    throw new ApiError(500, "Expected CSV", { code: "server_error" });
  }
  return res.blob();
}

// A conditional JSON read: null means the server said 304, nothing changed.
export async function apiIfChanged<T>(url: string, etag: string): Promise<{ data: T; etag: string } | null> {
  const res = await send(url, {
    headers: { Accept: "application/json", ...(etag ? { "If-None-Match": etag } : {}) },
    notModifiedOK: true,
  });
  if (res.status === 304) return null;
  return { data: (await readBody(res)).data as T, etag: res.headers.get("ETag") ?? "" };
}
