// Small fetch client for /api/v1. Cookie session auth; the CSRF token is
// fetched once at boot from GET /api/v1/auth/csrf and sent as the
// X-CSRF-Token header on every unsafe request.

let csrfToken: string | null = null;
let onUnauthorized: (() => void) | null = null;

export function setOnUnauthorized(fn: (() => void) | null) {
  onUnauthorized = fn;
}

// An error response. `code` is the API's stable error code; show it with
// errorText (lib/errors.ts), never the message, which is the server's
// English for logs.
export class ApiError extends Error {
  status: number;
  code: string;
  data: Record<string, unknown>;
  constructor(status: number, message: string, data: Record<string, unknown> = {}) {
    super(message);
    this.status = status;
    this.data = data;
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
  const text = await res.text();
  let raw: unknown = {};
  try {
    raw = text ? (JSON.parse(text) as unknown) : {};
  } catch {
    // A proxy's HTML error page: keep the status, drop the body.
    raw = {};
  }
  if (res.status === 401) {
    if (onUnauthorized && !opts.anonymousOK) onUnauthorized();
    throw new ApiError(401, "unauthorized", { code: "signed_out", ...((raw ?? {}) as Record<string, unknown>) });
  }
  const data = raw as T;
  const apiErr = (raw as { error?: string } | null)?.error;
  if (!res.ok) {
    throw new ApiError(res.status, apiErr ?? `request failed (${res.status})`, (raw ?? {}) as Record<string, unknown>);
  }
  return data;
}

// CSV reads share cookie and signed-out handling with JSON reads, but must
// reject JSON/proxy errors before creating any downloadable blob.
export async function authenticatedCSV(path: string): Promise<Blob> {
  const res = await fetch(`/api/v1${path}`, {
    headers: { Accept: "text/csv" }, credentials: "include", cache: "no-store",
  });
  if (res.status === 401) {
    onUnauthorized?.();
    throw new ApiError(401, "unauthorized", { code: "signed_out" });
  }
  if (!res.ok) {
    const raw = await res.json().catch(() => ({})) as Record<string, unknown>;
    throw new ApiError(res.status, "CSV request failed", raw);
  }
  if (res.headers.get("Content-Type")?.split(";")[0].trim().toLowerCase() !== "text/csv") {
    throw new ApiError(500, "Expected CSV", { code: "server_error" });
  }
  return res.blob();
}

// ---- endpoint wrappers ----

import type {
  AccountRequest,
  PeopleSearchResponse,
  AgeBand,
  AttendanceMode,
  Attendee,
  HouseholdAttendee,
  RegistrationCommand,
  CalendarFeed,
  EventDetailResponse,
  EventHostRow,
  CalendarOccurrence,
  DepartureConfirmationResponse,
  DeparturePreview,
  EventOccurrence,
  EventSeries,
  GroupDetailResponse,
  GroupEventsResponse,
  GuestRegistration,
  GroupSummary,
  GuardianLink,
  GuardianNomination,
  Household,
  HouseholdPerson,
  Invitation,
  InvitationAcceptance,
  InvitationContext,
  HouseholdConversion,
  Member,
  Notification,
  NotificationFeed,
  NotificationFilter,
  OpenedDecision,
  RegistrationBatch,
  SeriesRegistrationRefusal,
  SeriesRegistrationResponse,
  SeriesRegistrationResult,
  SeriesRegistrationState,
  SoleLedGroup,
  User,
  TeenConsent,
  VideoProvider,
  VideoReveal,
  Visibility,
  VisibilityImpact,
} from "./types";

export const Auth = {
  me: () => api<{ user: User }>("/auth/me", { anonymousOK: true }),
  login: (email: string, password: string) =>
    api<{ user: User }>("/auth/login", { method: "POST", body: { email, password } }),
  logout: () => api<Record<string, never>>("/auth/logout", { method: "POST" }),
  // Password reset (M1.2): the response is uniform for known and unknown
  // addresses, so the message is shown as-is.
  requestPasswordReset: (email: string) =>
    api<{ message: string }>("/auth/password-reset", { method: "POST", body: { email } }),
  confirmPasswordReset: (token: string, password: string) =>
    api<{ user: User }>("/auth/password-reset/confirm", { method: "POST", body: { token, password } }),
};

export const People = {
  search: (query: string) => api<PeopleSearchResponse>(`/people/search?q=${encodeURIComponent(query)}`),
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
  get: (id: number | string, page?: { cursor?: string; limit?: number }) => {
    const params = new URLSearchParams();
    if (page?.cursor) params.set("cursor", page.cursor);
    if (page?.limit) params.set("limit", String(page.limit));
    return api<GroupDetailResponse>(`/groups/${id}${params.size ? `?${params}` : ""}`);
  },
  mine: () => api<{ groups: GroupSummary[] }>(`/me/groups`),
  create: (data: {
    name: string;
    description: string;
    tags: string[];
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
      location?: string;
      coverFocal?: { x: number; y: number };
      visibility?: Visibility;
      memberLimit?: number | null;
      // Keep or tighten each leader-opened event when the group becomes more private (DEC-48).
      openedEvents?: Record<string, OpenedDecision>;
    }
  ) => api<{ group: GroupSummary }>(`/groups/${id}`, { method: "PUT", body: data }),
  visibilityImpact: (id: number | string, visibility: Visibility) =>
    api<VisibilityImpact>(`/groups/${id}/visibility-impact?visibility=${encodeURIComponent(visibility)}`),
  join: (id: number | string) => api<unknown>(`/groups/${id}/join`, { method: "POST" }),
  departurePreview: (id: number | string) =>
    api<{ departure: DeparturePreview }>(`/groups/${id}/departure-preview`),
  confirmDeparture: (id: number | string, snapshot: string, keepOccurrenceIds: number[]) =>
    api<DepartureConfirmationResponse>(`/groups/${id}/leave`, { method: "POST", body: { snapshot, keepOccurrenceIds } }),
  memberDeparturePreview: (id: number | string, userId: number) =>
    api<{ departure: DeparturePreview }>(`/groups/${id}/members/${userId}/departure-preview`),
  confirmMemberDeparture: (id: number | string, userId: number, snapshot: string, keepOccurrenceIds: number[], note?: string) =>
    api<DepartureConfirmationResponse>(`/groups/${id}/members/${userId}/departure`, { method: "POST", body: { snapshot, keepOccurrenceIds, note } }),
  // Make a member a leader, or a leader a member again (audited; admins are notified).
  setRole: (id: number | string, userId: number, role: "leader" | "member") =>
    api<{ members: Member[] }>(`/groups/${id}/members/${userId}/role`, { method: "PUT", body: { role } }),
  // overLimit: add even though the group is full (an explicit, audited choice; DEC-39).
  addMember: (id: number | string, target: string | { selectionId: string }, overLimit = false) =>
    api<unknown>(`/groups/${id}/members`, { method: "POST", body: { ...(typeof target === "string" ? { email: target } : target), overLimit } }),
  requestDeletion: (id: number | string) =>
    api<{ group: GroupSummary }>(`/groups/${id}/deletion-request`, { method: "POST" }),
  cancelDeletion: (id: number | string) =>
    api<unknown>(`/groups/${id}/deletion-request`, { method: "DELETE" }),
  members: (id: number | string) => api<{ members: Member[] }>(`/groups/${id}/members`),
  events: (id: number | string, from?: string, to?: string, page?: { cursor?: string; limit?: number }) => {
    let p = `/groups/${id}/events`;
    const q = new URLSearchParams();
    if (from) q.set("from", from);
    if (to) q.set("to", to);
    if (page?.cursor) q.set("cursor", page.cursor);
    if (page?.limit !== undefined) q.set("limit", String(page.limit));
    const qs = q.toString();
    if (qs) p += `?${qs}`;
    return api<GroupEventsResponse>(p);
  },
  createEvent: (
    id: number | string,
    data: {
      title?: string;
      description?: string;
      startsLocal: string;
      durationMinutes: number;
      tzid: string;
      location?: string;
      recurrenceRule?: string;
      visibility?: Visibility;
      visibilityReason?: string;
      capacity?: number | null;
      waitlistEnabled?: boolean;
      guestsAllowed?: number | null;
      remoteAvailable?: boolean;
      remoteCapacity?: number | null;
      childcareAvailable?: boolean;
      childcareCapacity?: number | null;
      childcareAges?: string;
      childcareNotes?: string;
      videoProvider?: VideoProvider;
      videoUrl?: string;
      videoReveal?: VideoReveal;
    }
  ) => api<{ event: EventSeries }>(`/groups/${id}/events`, { method: "POST", body: data }),
};

export const Site = {
  get: () => api<{ name: string; timezone: string; selfSignup: "request" | "off" }>("/site"),
};

export interface AccountRequestInput {
  guardianEmail?: string;
  name: string;
  email: string;
  phone?: string;
  ageBand: AgeBand;
  birthDate?: string;
  message?: string;
  website?: string; // honeypot: people leave it blank
}

// Account requests (M1.3, DEC-4): self-signup waits for an admin; the
// answer is the same whether or not the email already has an account.
export const AccountRequests = {
  create: (data: AccountRequestInput) =>
    api<{ message: string }>("/account-requests", { method: "POST", body: data }),
  inviteFriend: (data: AccountRequestInput) =>
    api<{ message: string }>("/account-requests/invite-friend", { method: "POST", body: data }),
  confirm: (token: string, password: string) =>
    api<{ user: User; pendingConsent?: boolean }>("/account-requests/confirm", {
      method: "POST",
      body: { token, password },
    }),
};

export const AdminAccountRequests = {
  list: (status = "pending") =>
    api<{ requests: AccountRequest[] }>(`/admin/account-requests?status=${encodeURIComponent(status)}`),
  approve: (id: number, note?: string, fromNotification?: number, accountConsentEvidenceText?: string) =>
    api<{ request: AccountRequest; userId: number }>(`/admin/account-requests/${id}/approve`, {
      method: "POST",
      body: { note: note || undefined, fromNotification, accountConsentEvidenceText },
    }),
  reject: (id: number, note?: string) =>
    api<{ request: AccountRequest; userId: number }>(`/admin/account-requests/${id}/reject`, {
      method: "POST",
      body: { note: note || undefined },
    }),
  setSelfSignup: (mode: "request" | "off") =>
    api<{ selfSignup: "request" | "off" }>("/admin/self-signup", { method: "PUT", body: { mode } }),
};

export const Calendar = {
  list: (q: { from: string; to: string; tz: string; mine?: boolean; group?: string; deleted?: boolean; remote?: boolean; childcare?: boolean }) => {
    const p = new URLSearchParams({ from: q.from, to: q.to, tz: q.tz });
    if (q.mine) p.set("mine", "1");
    if (q.group) p.set("group", q.group);
    if (q.deleted) p.set("deleted", "1");
    if (q.remote) p.set("remote", "1");
    if (q.childcare) p.set("childcare", "1");
    return api<{ occurrences: CalendarOccurrence[]; truncated: boolean }>(`/calendar?${p}`);
  },
};

export const Events = {
  listOpen: () => api<{ events: EventOccurrence[] }>("/events"),
  get: (id: number | string) => api<EventDetailResponse>(`/events/${id}`),
  createChurch: (data: {
    title: string;
    description?: string;
    startsLocal: string;
    durationMinutes: number;
    tzid: string;
    location?: string;
    recurrenceRule?: string;
    visibility?: Visibility;
    hosts?: string[];
    capacity?: number | null;
    waitlistEnabled?: boolean;
    guestsAllowed?: number | null;
    remoteAvailable?: boolean;
    remoteCapacity?: number | null;
      childcareAvailable?: boolean;
      childcareCapacity?: number | null;
      childcareAges?: string;
      childcareNotes?: string;
    videoProvider?: VideoProvider;
    videoUrl?: string;
    videoReveal?: VideoReveal;
  }) => api<{ event: EventSeries }>("/events", { method: "POST", body: data }),
  setVisibility: (
    id: number,
    // confirm: the leader saw the preview of registrations this cancels.
    data: { visibility: Visibility; reason?: string; confirm?: boolean; resetOverrides?: boolean }
  ) => api<{ event: EventSeries }>(`/events/${id}/visibility`, { method: "PUT", body: data }),
  visibilityImpact: (id: number, visibility: Visibility, resetOverrides = false) =>
    api<VisibilityImpact>(`/events/${id}/visibility-impact?visibility=${encodeURIComponent(visibility)}&resetOverrides=${resetOverrides ? "1" : "0"}`),
  addHost: (id: number, email: string) =>
    api<{ hosts: EventHostRow[] }>(`/events/${id}/hosts`, { method: "POST", body: { email } }),
  removeHost: (id: number, userId: number) =>
    api<{ hosts: EventHostRow[] }>(`/events/${id}/hosts/${userId}`, { method: "DELETE" }),
  update: (
    id: number,
    data: {
      title?: string;
      description?: string;
      startsLocal?: string;
      durationMinutes?: number;
      tzid?: string;
      location?: string;
      recurrenceRule?: string;
      rruleUntil?: string;
      capacity?: number | null;
      waitlistEnabled?: boolean;
      guestsAllowed?: number | null;
      remoteAvailable?: boolean;
      remoteCapacity?: number | null;
      childcareAvailable?: boolean;
      childcareCapacity?: number | null;
      childcareAges?: string;
      childcareNotes?: string;
      videoProvider?: VideoProvider;
      videoUrl?: string;
      videoReveal?: VideoReveal;
    }
  ) => api<{ event: EventSeries }>(`/events/${id}`, { method: "PUT", body: data }),
  // Mint a fresh Jitsi room URL (M1.8); each call makes a new one.
  generateJitsi: () => api<{ url: string }>("/events/video/jitsi", { method: "POST", body: {} }),
  register: (id: number, data: RegistrationCommand) =>
    api<RegistrationBatch>(`/events/${id}/registrations`, { method: "PUT", body: data }),
  registerSeries: (id: number, data: {
    date?: string;
    scope: "all_future" | "this_and_following";
    status: "going" | "maybe" | "declined";
    guestCount?: number;
    attendanceMode?: AttendanceMode;
    childcareCount?: number;
    childcareAgesNote?: string;
  }) => api<SeriesRegistrationResponse & { results: (SeriesRegistrationResult & { before: SeriesRegistrationState; after: SeriesRegistrationState })[]; refusals: SeriesRegistrationRefusal[] }>(`/events/${id}/series-registrations`, { method: "POST", body: data }),
  undoSeriesRegistration: (id: number, token: string) =>
    api<SeriesRegistrationResponse & { results: (SeriesRegistrationResult & { before: SeriesRegistrationState; after: SeriesRegistrationState })[]; refusals: SeriesRegistrationRefusal[] }>(`/events/${id}/series-registrations/undo`, { method: "POST", body: { token } }),
  // Pruning and hand promotion (M1.5): leaders and hosts remove anyone,
  // promote one waitlisted row by hand, or add someone directly.
  removeRegistration: (id: number, date: string, registrationId: number, note?: string) =>
    api<unknown>(`/events/${id}/registrations/remove`, { method: "POST", body: { date, registrationId, note } }),
  promoteRegistration: (id: number, date: string, registrationId: number) =>
    api<unknown>(`/events/${id}/registrations/promote`, { method: "POST", body: { date, registrationId } }),
  addRegistration: (id: number, date: string, target: string | { selectionId: string }, overCapacity?: boolean, childcare?: { childcareCount: number; childcareAgesNote: string }) =>
    api<unknown>(`/events/${id}/registrations/add`, { method: "POST", body: { date, ...(typeof target === "string" ? { email: target } : target), overCapacity, ...childcare } }),
  restoreRegistration: (id: number, date: string, target: { userId: number } | { personId: number; registeredBy: number }, attendanceMode: AttendanceMode, overCapacity: boolean) =>
    api<unknown>(`/events/${id}/registrations/add`, { method: "POST", body: { date, ...target, attendanceMode, overCapacity } }),
  cancelOccurrence: (id: number, date: string) =>
    api<unknown>(`/events/${id}/cancel`, { method: "POST", body: { date } }),
  dateVisibilityImpact: (id: number, date: string, visibility: Visibility | "series") =>
    api<VisibilityImpact>(`/events/${id}/occurrences/${date}/visibility-impact?visibility=${encodeURIComponent(visibility)}`),
  updateOccurrence: (
    id: number,
    date: string,
    data: { startsLocal?: string; durationMinutes?: number; location?: string; title?: string; capacityOverride?: number | null; remoteCapacityOverride?: number | null; visibility?: Visibility | "series" | null; reason?: string; confirm?: boolean }
  ) => api<{ occurrence: EventOccurrence }>(`/events/${id}/occurrences/${date}`, { method: "PUT", body: data }),
  delete: (id: number) => api<unknown>(`/events/${id}`, { method: "DELETE" }),
};

// Guest registration (M1.4): a signed-out adult registers for a public
// event by name and email. The server answers the same way whether or
// not the address already has an account. Nothing is registered until
// the emailed link is confirmed on /guest/confirm.
export interface GuestRegistrationInput {
  name: string;
  email: string;
  date: string;
  over18: boolean;
  createAccount?: boolean;
  // The browser's zone, for the guest's emails (DEC-58).
  timezone?: string;
  // Honeypot. People never fill this in.
  website?: string;
}

export const Guests = {
  register: (eventId: number, data: GuestRegistrationInput) =>
    api<{ message: string }>(`/events/${eventId}/guest-registrations`, { method: "POST", body: data }),
  confirm: (token: string) =>
    api<{ registration: GuestRegistration; manageToken: string }>("/guest-registrations/confirm", {
      method: "POST",
      body: { token },
    }),
  getManage: (token: string) =>
    api<{ registration: GuestRegistration }>(`/guest-registrations/manage/${encodeURIComponent(token)}`),
  cancelManage: (token: string) =>
    api<{ registration: GuestRegistration }>(`/guest-registrations/manage/${encodeURIComponent(token)}/cancel`, {
      method: "POST",
    }),
};

// Invitations (M1.2, 5.10): admins and leaders invite; the person sets a
// password on acceptance and gets an active account. No approval step.
export const Invitations = {
  create: (data: {
    email: string;
    name?: string;
    phone?: string;
    groupId?: number | null;
    role?: "member" | "leader";
    accountType?: "adult" | "teen";
    overLimit?: boolean;
  }) => api<{ invitation: Invitation }>("/invitations", { method: "POST", body: data }),
  get: (token: string) =>
    api<InvitationContext>(`/invitations/${encodeURIComponent(token)}`),
  accept: (data: { token: string; name: string; password: string; ageBand: AgeBand; birthDate?: string }) =>
    api<InvitationAcceptance>(`/invitations/accept`, { method: "POST", body: data }),
};

export const Admin = {
  pending: () => api<{ groups: GroupSummary[] }>("/admin/pending"),
  // Tags are sent only when the admin edited them; left out, the leader's own stay.
  approve: (id: number | string, tags?: string[]) =>
    api<{ group: GroupSummary }>(`/admin/groups/${id}/approve`, { method: "POST", body: { tags } }),
  // The optional message goes to the leaders on their notice (DEC-96).
  reject: (id: number | string, message?: string) =>
    api<unknown>(`/admin/groups/${id}/reject`, { method: "POST", body: message ? { message } : undefined }),
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
    throw new ApiError(401, "unauthorized", { code: "signed_out" });
  }
  const raw = (await res.json().catch(() => ({}))) as { error?: string };
  if (!res.ok) {
    throw new ApiError(res.status, raw.error ?? `upload failed (${res.status})`, raw as Record<string, unknown>);
  }
  return raw as T;
}

export interface ProfileUpdate {
  hideContactFromLeaders?: boolean;
  name?: string;
  displayName?: string;
  email?: string;
  currentPassword?: string;
  phone?: string;
  timezone?: string;
  avatarFocal?: { x: number; y: number };
}

export const Profile = {
  update: (data: ProfileUpdate) =>
    api<{ user: User; verificationSent?: boolean }>("/me", { method: "PUT", body: data }),
  // Verified email change (M1.2): the token is the credential.
  verifyEmail: (token: string) =>
    api<{ user: User }>("/me/email/verify", { method: "POST", body: { token } }),
  cancelEmailChange: (token: string) =>
    api<{ cancelled: true }>("/me/email/cancel", { method: "POST", body: { token }, anonymousOK: true }),
  changePassword: (current: string, password: string) =>
    api<Record<string, never>>("/me/password", { method: "POST", body: { current, password } }),
  uploadAvatar: (file: File) => apiUpload<{ avatarUrl: string }>("/me/avatar", "avatar", file),
  deleteAvatar: () => api<Record<string, never>>("/me/avatar", { method: "DELETE" }),
};

// The personal calendar feed link (M1.7): only create returns the URL,
// and only that once.
export const CalendarFeeds = {
  get: () => api<{ calendarFeed: CalendarFeed }>("/me/calendar-feed"),
  create: (scope: CalendarFeed["scope"]) =>
    api<{ calendarFeed: CalendarFeed }>("/me/calendar-feed", { method: "POST", body: { scope } }),
  setScope: (scope: CalendarFeed["scope"]) =>
    api<{ calendarFeed: CalendarFeed }>("/me/calendar-feed", { method: "PUT", body: { scope } }),
  revoke: () => api<{ calendarFeed: CalendarFeed }>("/me/calendar-feed", { method: "DELETE" }),
};

export const GroupCover = {
  upload: (id: number | string, file: File) =>
    apiUpload<{ imageUrl: string }>(`/groups/${id}/cover`, "cover", file),
  remove: (id: number | string) =>
    api<Record<string, never>>(`/groups/${id}/cover`, { method: "DELETE" }),
};

export interface Badge {
  unread: number;
  waiting: number;
}

export const Notifications = {
  list: (filter: NotificationFilter = "all", before = "") =>
    api<NotificationFeed>(
      `/notifications?filter=${filter}${before ? `&before=${encodeURIComponent(before)}` : ""}`
    ),
  // badge sends the last ETag; null means "unchanged" (304).
  badge: async (etag: string): Promise<{ badge: Badge; etag: string } | null> => {
    const res = await fetch("/api/v1/notifications/badge", {
      headers: { Accept: "application/json", ...(etag ? { "If-None-Match": etag } : {}) },
      credentials: "include",
    });
    if (res.status === 304) return null;
    if (!res.ok) throw new ApiError(res.status, "could not load notifications");
    return { badge: (await res.json()) as Badge, etag: res.headers.get("ETag") ?? "" };
  },
  seen: (ids: number[]) => api<unknown>("/notifications/seen", { method: "POST", body: { ids } }),
  read: (id: number, read = true) =>
    api<{ notification: Notification }>(`/notifications/${id}/read`, { method: read ? "POST" : "DELETE" }),
  readAll: () => api<unknown>("/notifications/read-all", { method: "POST" }),
  archive: (id: number, archived = true) =>
    api<{ notification: Notification }>(`/notifications/${id}/archive`, { method: archived ? "POST" : "DELETE" }),
  // Inline Approve on a pending group (the only allow-listed action, 6.6).
  approveGroup: (groupId: number, notificationId: number) =>
    api<unknown>(`/admin/groups/${groupId}/approve`, {
      method: "POST",
      body: { fromNotification: notificationId },
    }),
  // Inline Approve on a clean adult account request (DEC-44).
  approveAccountRequest: (requestId: number, notificationId: number) =>
    api<unknown>(`/admin/account-requests/${requestId}/approve`, {
      method: "POST",
      body: { fromNotification: notificationId },
    }),
};

export interface AdminUserInput {
  ageBand: AgeBand;
  birthDate?: string;
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
  revokeCalendarFeed: (id: number) =>
    api<{ hadLink: boolean }>(`/admin/users/${id}/calendar-feed`, { method: "DELETE" }),
};

export const AdminTeenConsents = {
  list: (userId: number) => api<{ consents: TeenConsent[]; administrativelyDisabled: boolean }>(`/admin/users/${userId}/teen-consents`),
  change: (userId: number, data: Pick<TeenConsent, "scope" | "status" | "evidenceText">) =>
    api<Record<string, never>>(`/admin/users/${userId}/teen-consents`, { method: "POST", body: data }),
};

export const AdminGroups = {
  list: () => api<{ groups: GroupSummary[] }>("/admin/groups"),
  addMember: (id: number | string, target: number | { selectionId: string }, role: "leader" | "member" = "member", overLimit = false) =>
    api<unknown>(`/admin/groups/${id}/members`, { method: "POST", body: { ...(typeof target === "number" ? { userId: target } : target), role, overLimit } }),
};

export interface HouseholdPersonInput {
  firstName: string;
  lastName?: string;
  kind: HouseholdPerson["kind"];
  birthDate?: string;
  careNotes?: string;
  photoConsent?: boolean;
}

// GET never creates: it answers {household: null} until the household
// exists, and always for teen accounts (Warden, PR 106).
export const Households = {
  convert: (id: number, email: string) => api<{ conversion: HouseholdConversion }>(`/me/household/people/${id}/convert`, { method: "POST", body: { email } }),
  resendConversion: (id: number) => api<{ conversion: HouseholdConversion }>(`/me/household/people/${id}/convert/resend`, { method: "POST" }),
  cancelConversion: (id: number) => api<Record<string, never>>(`/me/household/people/${id}/convert`, { method: "DELETE" }),
  mine: () => api<{ household: Household | null }>("/me/household"),
  update: (data: { name?: string; address?: string }) =>
    api<{ household: Household }>("/me/household", { method: "PUT", body: data }),
  addPerson: (data: HouseholdPersonInput) =>
    api<{ person: HouseholdPerson }>("/me/household/people", { method: "POST", body: data }),
  updatePerson: (id: number, data: HouseholdPersonInput) =>
    api<{ person: HouseholdPerson }>(`/me/household/people/${id}`, { method: "PUT", body: data }),
  removePerson: (id: number) => api<Record<string, never>>(`/me/household/people/${id}`, { method: "DELETE" }),
};

export const AdminGuardianLinks = {
  list: (status = "pending") =>
    api<{ guardianLinks: GuardianLink[]; nominations: GuardianNomination[] }>(`/admin/guardian-links?status=${encodeURIComponent(status)}`),
  confirm: (id: number, checkNote: string) =>
    api<{ guardianLink: GuardianLink }>(`/admin/guardian-links/${id}/confirm`, { method: "POST", body: { checkNote } }),
  reject: (id: number, note?: string) =>
    api<{ guardianLink: GuardianLink }>(`/admin/guardian-links/${id}/reject`, { method: "POST", body: { note: note || undefined } }),
};

// Mixed wire rows become an exclusive presentation identity at the API
// boundary. Invalid both/neither identities never become action targets.
export function attendeeSubject(row: Attendee): { kind: "user"; userId: number } | { kind: "household"; person: HouseholdAttendee } | null {
  if (row.userId !== undefined && !row.householdPerson) return { kind: "user", userId: row.userId };
  if (row.userId === undefined && row.householdPerson) return { kind: "household", person: row.householdPerson };
  return null;
}
