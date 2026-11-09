import type {
  GuardianEmailPreview,
  GuardianLinkResponse,
  GuardianLinksResponse,
  GuardianTeensResponse,
  NoticeResponse,
} from "../types";
import { api, path, query, type Id } from "./core";

export type GuardianConsentScope = "account" | "sms" | "photos";

export const Guardians = {
  teens: () => api<GuardianTeensResponse>("/me/guardian-teens"),
  claim: (email: string) => api<NoticeResponse>("/me/guardian-claims", { method: "POST", body: { email } }),
  change: (linkId: Id, scope: GuardianConsentScope, status: "granted" | "revoked") =>
    api<Record<string, never>>(path`/me/guardian-links/${linkId}/consents`, { method: "POST", body: { scope, status } }),
  email: (linkId: Id) => api<NoticeResponse>(path`/me/guardian-links/${linkId}/consent-email`, { method: "POST" }),
  preview: (token: string) => api<GuardianEmailPreview>(`/guardian-consent${query({ token })}`, { anonymousOK: true }),
  useEmail: (token: string) => api<Record<string, never>>("/guardian-consent", { method: "POST", body: { token }, anonymousOK: true }),
};

export const AdminGuardianLinks = {
  list: (status = "pending") => api<GuardianLinksResponse>(`/admin/guardian-links${query({ status })}`),
  confirm: (id: Id, checkNote: string) =>
    api<GuardianLinkResponse>(path`/admin/guardian-links/${id}/confirm`, { method: "POST", body: { checkNote } }),
  reject: (id: Id, note?: string) =>
    api<GuardianLinkResponse>(path`/admin/guardian-links/${id}/reject`, { method: "POST", body: { note: note || undefined } }),
};
