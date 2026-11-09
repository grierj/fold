import type {
  AgeBand,
  AttemptMintResponse,
  AttemptSettleResponse,
  ErasureReasonPage,
  SoleHostedEventsResponse,
  SoleLedGroup,
  TeenConsent,
  TeenConsentsResponse,
  User,
  ReasonReviewBody,
} from "../types";
import { isErasureReport } from "../lib/erasure";
import { ApiError, api, path, type Id } from "./core";
import type { ProfileUpdate } from "./profile";

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
  update: (id: Id, data: ProfileUpdate & { role?: string; status?: "active" | "disabled"; successors?: Record<string, number> }) =>
    api<{ user: User }>(path`/admin/users/${id}`, { method: "PUT", body: data }),
  // Groups this person leads alone, with who may take over (DEC-36).
  soleLeaderGroups: (id: Id) => api<{ groups: SoleLedGroup[] }>(path`/admin/users/${id}/sole-leader-groups`),
  // Church-wide events this person hosts alone, with who may host them next (M7.4, R4-H).
  soleHostEvents: (id: Id) => api<SoleHostedEventsResponse>(path`/admin/users/${id}/sole-host-events`),
  // One audited page of the contact-request reasons deleting this person would
  // leave behind (T07B-2b). after is the previous page's next, or null for the
  // first page; a failed page is retried with the same cursor.
  erasureReasonsPage: (id: Id, after: string | null) =>
    api<ErasureReasonPage>(path`/admin/users/${id}/erasure-reasons/page`, { method: "POST", body: { after } }),
  // attemptId is the id minted for this press (mintAttempt); it travels in the
  // body only, never in the path. A 2xx whose body is not a deletion report (a
  // proxy's page, an empty body) is not a success: it throws a plain Error,
  // which the panel treats as "the server may have deleted" and settles the id.
  // reasonReview is the set-v1 envelope of the review (lib/reasonSetDigest.ts):
  // the count, the set digest and the admin's explicit choice for every reason
  // they were shown. It is sent only when the review found reasons; the server
  // ignores it for an account without.
  remove: async (
    id: Id,
    successors: Record<string, number> = {},
    hostSuccessors: Record<string, number> = {},
    attemptId?: string,
    reasonReview?: ReasonReviewBody,
  ) => {
    const res = await api<{ erasure?: unknown }>(path`/admin/users/${id}`, {
      method: "DELETE",
      body: { successors, hostSuccessors, ...(attemptId ? { attemptId } : {}), ...(reasonReview ? { reasonReview } : {}) },
    });
    if (!isErasureReport(res?.erasure)) throw new Error("unexpected answer to a delete");
    return { erasure: res.erasure };
  },
  // One id per press of Delete. The server mints and checks it; it reads no
  // database and grants nothing by itself (T07B-1b). The id is 80 lowercase hex
  // digits; anything else (a 2xx that is not the mint's JSON) throws, so a
  // DELETE is never sent without an id.
  mintAttempt: async (subjectUserId: number): Promise<AttemptMintResponse> => {
    const res = await api<{ attemptId?: unknown }>("/admin/operation-attempts", { method: "POST", body: { kind: "account_delete", subjectUserId } });
    if (typeof res?.attemptId !== "string" || !/^[0-9a-f]{80}$/.test(res.attemptId)) throw new ApiError(502, "unexpected answer to a mint", {});
    return { attemptId: res.attemptId };
  },
  settleAttempt: (attemptId: string, subjectUserId: number) =>
    api<AttemptSettleResponse>("/admin/operation-attempts/settle", {
      method: "POST",
      body: { attemptId, kind: "account_delete", subjectUserId },
    }),
  resetPassword: (id: Id, password: string) =>
    api<Record<string, never>>(path`/admin/users/${id}/password`, { method: "POST", body: { password } }),
  revokeCalendarFeed: (id: Id) => api<{ hadLink: boolean }>(path`/admin/users/${id}/calendar-feed`, { method: "DELETE" }),
  // Let Fold email a suppressed address again (M3.2c, DEC-113).
  clearEmailSuppression: (id: Id) => api<{ cleared: number }>(path`/admin/users/${id}/email-suppression/clear`, { method: "POST" }),
};

export const AdminTeenConsents = {
  list: (userId: Id) => api<TeenConsentsResponse>(path`/admin/users/${userId}/teen-consents`),
  change: (userId: Id, data: Pick<TeenConsent, "scope" | "status" | "evidenceText">) =>
    api<Record<string, never>>(path`/admin/users/${userId}/teen-consents`, { method: "POST", body: data }),
};
