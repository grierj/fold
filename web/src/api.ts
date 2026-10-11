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
  // anonymousOK: a 401 just means "not signed in" (the boot-time session
  // check), so don't send the visitor to /login; public pages stay public.
  anonymousOK?: boolean;
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
    if (onUnauthorized && !opts.anonymousOK) onUnauthorized();
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
  EventOccurrence,
  EventSeries,
  GroupDetailResponse,
  GroupSummary,
  Member,
  Notification,
  PendingReturn,
  RegistrationStatus,
  SoleLedGroup,
  User,
  Visibility,
} from "./types";

export const Auth = {
  me: () => api<{ user: User }>("/auth/me", { anonymousOK: true }),
  login: (email: string, password: string) =>
    api<{ user: User }>("/auth/login", { method: "POST", body: { email, password } }),
  logout: () => api<Record<string, never>>("/auth/logout", { method: "POST" }),
};

export const Groups = {
  list: (search = "", tags: string[] = []) => {
    const q = new URLSearchParams();
    if (search) q.set("search", search);
    for (const tag of tags) {
      if (tag) q.append("tag", tag);
    }
    const qs = q.toString();
    return api<{ groups: GroupSummary[] }>(`/groups${qs ? `?${qs}` : ""}`);
  },
  get: (id: number | string) => api<GroupDetailResponse>(`/groups/${id}`),
  mine: () => api<{ groups: GroupSummary[] }>(`/me/groups`),
  create: (data: {
    name: string;
    description: string;
    tags: string[];
    imageUrl: string;
    location: string;
    visibility: Visibility;
    memberLimit: number | null;
  }) => api<{ group: GroupSummary }>("/groups", { method: "POST", body: data }),
  update: (
    id: number | string,
    data: {
      name?: string;
      description?: string;
      tags?: string[];
      imageUrl?: string;
      location?: string;
      coverFocal?: { x: number; y: number };
      visibility?: Visibility;
      memberLimit?: number | null;
    }
  ) => api<{ group: GroupSummary }>(`/groups/${id}`, { method: "PUT", body: data }),
  join: (id: number | string) => api<unknown>(`/groups/${id}/join`, { method: "POST" }),
  leave: (id: number | string) => api<unknown>(`/groups/${id}/leave`, { method: "POST" }),
  // Make a member a leader, or a leader a member again (audited; admins are notified).
  setRole: (id: number | string, userId: number, role: "leader" | "member") =>
    api<{ members: Member[] }>(`/groups/${id}/members/${userId}/role`, { method: "PUT", body: { role } }),
  // overLimit: add even though the group is full (an explicit, audited choice; DEC-39).
  addMember: (id: number | string, email: string, overLimit = false) =>
    api<unknown>(`/groups/${id}/members`, { method: "POST", body: { email, overLimit } }),
  // Approve or decline a re-enabled account waiting to return to a full group.
  decideReturn: (id: number | string, userId: number, decision: "approve" | "decline") =>
    api<unknown>(`/groups/${id}/members/${userId}/return`, { method: "POST", body: { decision } }),
  // Leaders and admins remove someone (audited; DEC-35). The note stays in the audit log.
  removeMember: (id: number | string, userId: number, note?: string) =>
    api<{ members: Member[] }>(`/groups/${id}/members/${userId}`, { method: "DELETE", body: { note } }),
  requestDeletion: (id: number | string) =>
    api<{ group: GroupSummary }>(`/groups/${id}/deletion-request`, { method: "POST" }),
  cancelDeletion: (id: number | string) =>
    api<unknown>(`/groups/${id}/deletion-request`, { method: "DELETE" }),
  members: (id: number | string) => api<{ members: Member[] }>(`/groups/${id}/members`),
  events: (id: number | string, from?: string, to?: string) => {
    let p = `/groups/${id}/events`;
    const q = new URLSearchParams();
    if (from) q.set("from", from);
    if (to) q.set("to", to);
    const qs = q.toString();
    if (qs) p += `?${qs}`;
    return api<{ events: EventOccurrence[] }>(p);
  },
  createEvent: (
    id: number | string,
    data: {
      title?: string;
      startsLocal: string;
      durationMinutes: number;
      tzid: string;
      location?: string;
      recurrenceRule?: string;
    }
  ) => api<{ event: EventSeries }>(`/groups/${id}/events`, { method: "POST", body: data }),
};

export const Site = {
  get: () => api<{ name: string; timezone: string }>("/site"),
};

export const Events = {
  update: (
    id: number,
    data: {
      title?: string;
      startsLocal?: string;
      durationMinutes?: number;
      tzid?: string;
      location?: string;
      recurrenceRule?: string;
      rruleUntil?: string;
    }
  ) => api<{ event: EventSeries }>(`/events/${id}`, { method: "PUT", body: data }),
  register: (id: number, status: RegistrationStatus, date: string) =>
    api<unknown>(`/events/${id}/registrations`, { method: "PUT", body: { status, date } }),
  cancelOccurrence: (id: number, date: string) =>
    api<unknown>(`/events/${id}/cancel`, { method: "POST", body: { date } }),
  updateOccurrence: (
    id: number,
    date: string,
    data: { startsLocal?: string; durationMinutes?: number; location?: string; title?: string }
  ) => api<{ occurrence: EventOccurrence }>(`/events/${id}/occurrences/${date}`, { method: "PUT", body: data }),
  delete: (id: number) => api<unknown>(`/events/${id}`, { method: "DELETE" }),
};

export const Admin = {
  pending: () => api<{ groups: GroupSummary[] }>("/admin/pending"),
  pendingReturns: () => api<{ returns: PendingReturn[] }>("/admin/pending-returns"),
  approve: (id: number | string, tags: string[]) =>
    api<{ group: GroupSummary }>(`/admin/groups/${id}/approve`, { method: "POST", body: { tags } }),
  reject: (id: number | string) => api<unknown>(`/admin/groups/${id}/reject`, { method: "POST" }),
  deletions: () => api<{ groups: GroupSummary[] }>("/admin/deletions"),
  rejectDeletion: (id: number | string) =>
    api<unknown>(`/admin/groups/${id}/reject-deletion`, { method: "POST" }),
  deleteGroup: (id: number | string) =>
    api<unknown>(`/admin/groups/${id}`, { method: "DELETE" }),
  setTimezone: (timezone: string) =>
    api<{ timezone: string }>("/admin/timezone", { method: "PUT", body: { timezone } }),
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
  avatarFocal?: { x: number; y: number };
}

export const Profile = {
  update: (data: ProfileUpdate) => api<{ user: User }>("/me", { method: "PUT", body: data }),
  changePassword: (current: string, password: string) =>
    api<Record<string, never>>("/me/password", { method: "POST", body: { current, password } }),
  uploadAvatar: (file: File) => apiUpload<{ avatarUrl: string }>("/me/avatar", "avatar", file),
  fetchAvatar: (url: string) =>
    api<{ avatarUrl: string }>("/me/avatar/url", { method: "POST", body: { url } }),
  deleteAvatar: () => api<Record<string, never>>("/me/avatar", { method: "DELETE" }),
};

export const GroupCover = {
  upload: (id: number | string, file: File) =>
    apiUpload<{ imageUrl: string }>(`/groups/${id}/cover`, "cover", file),
  remove: (id: number | string) =>
    api<Record<string, never>>(`/groups/${id}/cover`, { method: "DELETE" }),
};

export const Notifications = {
  list: () => api<{ notifications: Notification[] }>("/notifications"),
};

export interface AdminUserInput {
  email: string;
  name: string;
  // Optional: without one the account is invited and can't sign in yet.
  password?: string;
  role: "admin" | "member";
  displayName?: string;
  phone?: string;
}

export const AdminUsers = {
  list: () => api<{ users: User[] }>("/admin/users"),
  create: (data: AdminUserInput) => api<{ user: User }>("/admin/users", { method: "POST", body: data }),
  update: (
    id: number,
    data: ProfileUpdate & { role?: string; status?: "active" | "disabled"; successors?: Record<string, number> },
  ) =>
    api<{ user: User }>(`/admin/users/${id}`, { method: "PUT", body: data }),
  // Groups this person leads alone, with who may take over (DEC-36).
  soleLeaderGroups: (id: number) => api<{ groups: SoleLedGroup[] }>(`/admin/users/${id}/sole-leader-groups`),
  remove: (id: number, successors: Record<string, number> = {}) =>
    api<Record<string, never>>(`/admin/users/${id}`, { method: "DELETE", body: { successors } }),
  resetPassword: (id: number, password: string) =>
    api<Record<string, never>>(`/admin/users/${id}/password`, {
      method: "POST",
      body: { password },
    }),
};

export const AdminGroups = {
  list: () => api<{ groups: GroupSummary[] }>("/admin/groups"),
  addMember: (id: number | string, userId: number, role: "leader" | "member" = "member", overLimit = false) =>
    api<unknown>(`/admin/groups/${id}/members`, { method: "POST", body: { userId, role, overLimit } }),
  removeMember: (id: number | string, userId: number) =>
    api<unknown>(`/admin/groups/${id}/members/${userId}`, { method: "DELETE" }),
};
