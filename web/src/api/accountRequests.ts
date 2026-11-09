import type { AccountRequest, AccountRequestConfirmation, AccountRequestDecision, AgeBand } from "../types";
import { api, path, query, type Id } from "./core";

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
  create: (data: AccountRequestInput) => api<{ message: string }>("/account-requests", { method: "POST", body: data }),
  inviteFriend: (data: AccountRequestInput) => api<{ message: string }>("/account-requests/invite-friend", { method: "POST", body: data }),
  confirm: (token: string, password: string) =>
    api<AccountRequestConfirmation>("/account-requests/confirm", { method: "POST", body: { token, password }, rotatesCsrf: true }),
};

export const AdminAccountRequests = {
  list: (status = "pending") => api<{ requests: AccountRequest[] }>(`/admin/account-requests${query({ status })}`),
  // fromNotification: the inline Approve on a clean adult request (DEC-44).
  approve: (id: Id, note?: string, fromNotification?: number, accountConsentEvidenceText?: string) =>
    api<AccountRequestDecision>(path`/admin/account-requests/${id}/approve`, {
      method: "POST",
      body: { note: note || undefined, fromNotification, accountConsentEvidenceText },
    }),
  reject: (id: Id, note?: string) =>
    api<AccountRequestDecision>(path`/admin/account-requests/${id}/reject`, { method: "POST", body: { note: note || undefined } }),
  setSelfSignup: (mode: "request" | "off") =>
    api<{ selfSignup: "request" | "off" }>("/admin/self-signup", { method: "PUT", body: { mode } }),
};
