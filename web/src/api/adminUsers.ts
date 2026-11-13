import type {
  AgeBand,
  AttemptKind,
  AttemptMintResponse,
  AttemptSettleResponse,
  CalendarFeedRevokedResponse,
  EmailSuppressionClearedResponse,
  ErasureReasonPage,
  LegacyReasonDecision,
  LegacyReasonDiscovery,
  LegacyReasonProbe,
  LegacyReasonSaveResponse,
  ReasonReviewBody,
  SoleHostedEventsResponse,
  SoleLedGroupsResponse,
  TeenConsent,
  TeenConsentsResponse,
  UserResponse,
  UsersResponse,
} from "../types";
import { isErasureReport } from "../lib/erasure";
import { ApiError, api, path, query, type Id } from "./core";
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

// The subject of an attempt id travels as a JSON number. A deleted account's id
// reaches the client as a decimal string (the legacy list), so a string must be
// a canonical id that survives the conversion; anything else is never sent, as
// a rounded number would name another account.
function attemptSubject(id: Id): number {
  const n = typeof id === "number" ? id : /^[1-9][0-9]*$/.test(id) ? Number(id) : NaN;
  if (!Number.isSafeInteger(n) || n < 1) throw new Error("not a usable account id");
  return n;
}

// A read whose 2xx body is not the expected object is not an answer: api()
// reads an empty or non-JSON body as {}, and "found: undefined" or "no
// accounts" must never pass for a result.
function unexpectedAnswer(what: string): never {
  throw new ApiError(502, `unexpected answer to ${what}`, {});
}

const isCursor = (v: unknown) => v === null || typeof v === "string";
const isCount = (v: unknown) => typeof v === "number" && Number.isSafeInteger(v) && v >= 0;

export const AdminUsers = {
  list: () => api<UsersResponse>("/admin/users"),
  create: (data: AdminUserInput) => api<UserResponse>("/admin/users", { method: "POST", body: data }),
  update: (id: Id, data: ProfileUpdate & { role?: string; status?: "active" | "disabled"; successors?: Record<string, number> }) =>
    api<UserResponse>(path`/admin/users/${id}`, { method: "PUT", body: data }),
  // Groups this person leads alone, with who may take over (DEC-36).
  soleLeaderGroups: (id: Id) => api<SoleLedGroupsResponse>(path`/admin/users/${id}/sole-leader-groups`),
  // Church-wide events this person hosts alone, with who may host them next (M7.4, R4-H).
  soleHostEvents: (id: Id) => api<SoleHostedEventsResponse>(path`/admin/users/${id}/sole-host-events`),
  // One audited page of the contact-request reasons deleting this person would
  // leave behind (T07B-2b). after is the previous page's next, or null for the
  // first page; a failed page is retried with the same cursor.
  erasureReasonsPage: (id: Id, after: string | null) =>
    api<ErasureReasonPage>(path`/admin/users/${id}/erasure-reasons/page`, { method: "POST", body: { after } }),
  // The deleted accounts whose contact-request reasons nobody decided
  // (T07B-3a): one request-atomic unit of request ids and account references,
  // never text. after is the previous unit's next, or absent for the first; a
  // failed read is retried with the same cursor, and an empty unit with a next
  // proves nothing: walk until next is null. An account can continue on a later
  // unit, so merge by userId.
  legacyReasons: async (after?: string | null): Promise<LegacyReasonDiscovery> => {
    const res = await api<Partial<LegacyReasonDiscovery>>(`/admin/erasure-reasons/legacy${query({ after })}`);
    if (!Array.isArray(res?.accounts) || !isCursor(res.next)) unexpectedAnswer("a deleted-accounts list");
    return res as LegacyReasonDiscovery;
  },
  // Whether the first unit holds anything: found false proves only that the
  // first unit had none, not that no reasons remain.
  legacyReasonsProbe: async (): Promise<LegacyReasonProbe> => {
    const res = await api<Partial<LegacyReasonProbe>>("/admin/erasure-reasons/legacy/probe");
    if (typeof res?.found !== "boolean") unexpectedAnswer("a deleted-accounts probe");
    return { found: res.found };
  },
  // One audited page of a deleted account's undecided reasons (up to 100, with
  // the text). Cursors work as in erasureReasonsPage; each item's reviewHash
  // goes back unchanged with the choice in saveLegacyReasons. userId is the
  // decimal string the list gave.
  legacyReasonsPage: async (userId: Id, after: string | null): Promise<ErasureReasonPage> => {
    const res = await api<Partial<ErasureReasonPage>>(path`/admin/erasure-reasons/legacy/${userId}/page`, { method: "POST", body: { after } });
    if (!Array.isArray(res?.items) || !isCursor(res.next)) unexpectedAnswer("a deleted account's reasons");
    return res as ErasureReasonPage;
  },
  // Decide 1 to 100 of a deleted account's reasons, each an explicit keep or
  // remove; the server has no default, bulk or subset rule beyond that.
  // attemptId is a legacy_reason_save id minted for this press with
  // mintAttempt(userId, "legacy_reason_save"); like a deletion's it travels in
  // the body only. A 2xx whose body is not the two counts is not a success: it
  // throws a plain Error, which the caller treats as "the server may have
  // saved" and settles the id, as for remove().
  saveLegacyReasons: async (userId: Id, attemptId: string, decisions: LegacyReasonDecision[]): Promise<LegacyReasonSaveResponse> => {
    const res = await api<Partial<LegacyReasonSaveResponse>>(path`/admin/erasure-reasons/legacy/${userId}`, { method: "PUT", body: { attemptId, decisions } });
    if (!isCount(res?.kept) || !isCount(res?.removed)) throw new Error("unexpected answer to a save");
    return { kept: res.kept as number, removed: res.removed as number };
  },
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
  // One id per press of Delete, or of Save for a deleted account's reasons
  // (kind "legacy_reason_save", subject the deleted account). The server mints
  // and checks it; it reads no database and grants nothing by itself (T07B-1b).
  // An id opens only for the kind it was minted for. The id is 80 lowercase hex
  // digits; anything else (a 2xx that is not the mint's JSON) throws, so a
  // DELETE or PUT is never sent without an id.
  mintAttempt: async (subjectUserId: Id, kind: AttemptKind = "account_delete"): Promise<AttemptMintResponse> => {
    const res = await api<{ attemptId?: unknown }>("/admin/operation-attempts", { method: "POST", body: { kind, subjectUserId: attemptSubject(subjectUserId) } });
    if (typeof res?.attemptId !== "string" || !/^[0-9a-f]{80}$/.test(res.attemptId)) throw new ApiError(502, "unexpected answer to a mint", {});
    return { attemptId: res.attemptId };
  },
  // Settle with the kind and subject the id was minted with. For a save,
  // not_applied never carries subjectDeleted: the subject is deleted by definition.
  settleAttempt: async (attemptId: string, subjectUserId: Id, kind: AttemptKind = "account_delete") =>
    api<AttemptSettleResponse>("/admin/operation-attempts/settle", {
      method: "POST",
      body: { attemptId, kind, subjectUserId: attemptSubject(subjectUserId) },
    }),
  resetPassword: (id: Id, password: string) =>
    api<Record<string, never>>(path`/admin/users/${id}/password`, { method: "POST", body: { password } }),
  revokeCalendarFeed: (id: Id) => api<CalendarFeedRevokedResponse>(path`/admin/users/${id}/calendar-feed`, { method: "DELETE" }),
  // Let Fold email a suppressed address again (M3.2c, DEC-113).
  clearEmailSuppression: (id: Id) => api<EmailSuppressionClearedResponse>(path`/admin/users/${id}/email-suppression/clear`, { method: "POST" }),
};

export const AdminTeenConsents = {
  list: (userId: Id) => api<TeenConsentsResponse>(path`/admin/users/${userId}/teen-consents`),
  change: (userId: Id, data: Pick<TeenConsent, "scope" | "status" | "evidenceText">) =>
    api<Record<string, never>>(path`/admin/users/${userId}/teen-consents`, { method: "POST", body: data }),
};
