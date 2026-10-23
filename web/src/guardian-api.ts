import { api } from "./api";
import type { GuardianEmailPreview, GuardianTeen } from "./types";

export type GuardianConsentScope = "account" | "sms" | "photos";

export const Guardians = {
  teens: () => api<{ teens: GuardianTeen[] }>("/me/guardian-teens"),
  claim: (email: string) => api<{ message: string }>("/me/guardian-claims", { method: "POST", body: { email } }),
  change: (linkId: number, scope: GuardianConsentScope, status: "granted" | "revoked") =>
    api<Record<string, never>>(`/me/guardian-links/${linkId}/consents`, { method: "POST", body: { scope, status } }),
  email: (linkId: number) => api<{ message: string }>(`/me/guardian-links/${linkId}/consent-email`, { method: "POST" }),
  preview: (token: string) => api<GuardianEmailPreview>(`/guardian-consent?token=${encodeURIComponent(token)}`, { anonymousOK: true }),
  useEmail: (token: string) => api<Record<string, never>>("/guardian-consent", { method: "POST", body: { token }, anonymousOK: true }),
};
