// Small fetch client for /api/v1. Cookie session auth; the CSRF token is
// fetched once at boot from GET /api/v1/auth/csrf and sent as the
// X-CSRF-Token header on every unsafe request.

let csrfToken: string | null = null;
let onUnauthorized: (() => void) | null = null;

export function setOnUnauthorized(fn: (() => void) | null) {
  onUnauthorized = fn;
}

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
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

interface RequestOptions {
  method?: string;
  body?: unknown;
}

export async function api<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const method = (opts.method ?? "GET").toUpperCase();
  const headers: Record<string, string> = { Accept: "application/json" };
  let body: string | undefined;
  if (opts.body !== undefined) {
    headers["Content-Type"] = "application/json";
    body = JSON.stringify(opts.body);
  }
  if (method !== "GET" && method !== "HEAD") {
    headers["X-CSRF-Token"] = await ensureCsrf();
  }
  const res = await fetch(`/api/v1${path}`, {
    method,
    headers,
    body,
    credentials: "include",
  });
  if (res.status === 401) {
    if (onUnauthorized) onUnauthorized();
    throw new ApiError(401, "unauthorized");
  }
  const text = await res.text();
  const raw = text ? (JSON.parse(text) as unknown) : {};
  const data = raw as T;
  const apiErr = (raw as { error?: string } | null)?.error;
  if (!res.ok) {
    throw new ApiError(res.status, apiErr ?? `request failed (${res.status})`);
  }
  return data;
}

// ---- endpoint wrappers ----

import type {
  GroupDetailResponse,
  GroupSummary,
  Meeting,
  MeetingInstance,
  Member,
  User,
} from "./types";

export const Auth = {
  me: () => api<{ user: User }>("/auth/me"),
  login: (username: string, password: string) =>
    api<{ user: User }>("/auth/login", { method: "POST", body: { username, password } }),
  logout: () => api<Record<string, never>>("/auth/logout", { method: "POST" }),
};

export const Groups = {
  list: (search = "", tag = "") =>
    api<{ groups: GroupSummary[] }>(
      `/groups?search=${encodeURIComponent(search)}&tag=${encodeURIComponent(tag)}`
    ),
  get: (id: number | string) => api<GroupDetailResponse>(`/groups/${id}`),
  mine: () => api<{ groups: GroupSummary[] }>(`/me/groups`),
  create: (data: {
    name: string;
    description: string;
    tags: string[];
    imageUrl: string;
    location: string;
  }) => api<{ group: GroupSummary }>("/groups", { method: "POST", body: data }),
  update: (
    id: number | string,
    data: { name: string; description: string; tags: string[]; imageUrl: string; location: string }
  ) => api<{ group: GroupSummary }>(`/groups/${id}`, { method: "PUT", body: data }),
  join: (id: number | string) => api<unknown>(`/groups/${id}/join`, { method: "POST" }),
  leave: (id: number | string) => api<unknown>(`/groups/${id}/leave`, { method: "POST" }),
  members: (id: number | string) => api<{ members: Member[] }>(`/groups/${id}/members`),
  meetings: (id: number | string, from?: string, to?: string) => {
    let p = `/groups/${id}/meetings`;
    const q = new URLSearchParams();
    if (from) q.set("from", from);
    if (to) q.set("to", to);
    const qs = q.toString();
    if (qs) p += `?${qs}`;
    return api<{ meetings: MeetingInstance[] }>(p);
  },
  createMeeting: (
    id: number | string,
    data: { startsAt: string; endsAt: string; location: string; recurrenceRule?: string }
  ) => api<{ meeting: Meeting }>(`/groups/${id}/meetings`, { method: "POST", body: data }),
};

export const Meetings = {
  rsvp: (id: number, status: "going" | "maybe" | "declined", date: string) =>
    api<unknown>(`/meetings/${id}/rsvp`, { method: "PUT", body: { status, date } }),
  cancelInstance: (id: number, date: string) =>
    api<unknown>(`/meetings/${id}/cancel`, { method: "POST", body: { date } }),
  delete: (id: number) => api<unknown>(`/meetings/${id}`, { method: "DELETE" }),
};

export const Admin = {
  pending: () => api<{ groups: GroupSummary[] }>("/admin/pending"),
  approve: (id: number | string, tags: string[]) =>
    api<{ group: GroupSummary }>(`/admin/groups/${id}/approve`, { method: "POST", body: { tags } }),
  reject: (id: number | string) => api<unknown>(`/admin/groups/${id}/reject`, { method: "POST" }),
};

// Multipart upload: the browser sets Content-Type with the boundary;
// the CSRF token still goes in the header.
async function apiUpload<T>(path: string, field: string, file: File): Promise<T> {
  const form = new FormData();
  form.append(field, file);
  const res = await fetch(`/api/v1${path}`, {
    method: "POST",
    headers: { "X-CSRF-Token": await ensureCsrf(), Accept: "application/json" },
    body: form,
    credentials: "include",
  });
  if (res.status === 401) {
    if (onUnauthorized) onUnauthorized();
    throw new ApiError(401, "unauthorized");
  }
  const raw = (await res.json().catch(() => ({}))) as { error?: string };
  if (!res.ok) {
    throw new ApiError(res.status, raw.error ?? `upload failed (${res.status})`);
  }
  return raw as T;
}

export interface ProfileUpdate {
  name?: string;
  displayName?: string;
  email?: string;
  phone?: string;
}

export const Profile = {
  update: (data: ProfileUpdate) => api<{ user: User }>("/me", { method: "PUT", body: data }),
  uploadAvatar: (file: File) => apiUpload<{ avatarUrl: string }>("/me/avatar", "avatar", file),
  deleteAvatar: () => api<Record<string, never>>("/me/avatar", { method: "DELETE" }),
};

export const GroupCover = {
  upload: (id: number | string, file: File) =>
    apiUpload<{ imageUrl: string }>(`/groups/${id}/cover`, "cover", file),
  remove: (id: number | string) =>
    api<Record<string, never>>(`/groups/${id}/cover`, { method: "DELETE" }),
};
