import type { NoticeResponse, SessionResponse, UserResponse } from "../types";
import { api } from "./core";

export const Auth = {
  me: () => api<SessionResponse>("/auth/me", { anonymousOK: true }),
  // A wrong password answers 401 invalid_credentials. It is not a sign-out, so
  // it never goes to the global handler.
  login: (email: string, password: string) =>
    api<SessionResponse>("/auth/login", { method: "POST", body: { email, password }, anonymousOK: true, rotatesCsrf: true }),
  // The logout request carries this browser's push endpoint, if it has
  // one, so the server deletes the device in the same request (M3.13b).
  logout: (pushEndpoint?: string) =>
    api<Record<string, never>>("/auth/logout", { method: "POST", body: pushEndpoint ? { pushEndpoint } : undefined, rotatesCsrf: true }),
  // Password reset (M1.2): the response is uniform for known and unknown
  // addresses, so the message is shown as-is.
  requestPasswordReset: (email: string) =>
    api<NoticeResponse>("/auth/password-reset", { method: "POST", body: { email } }),
  confirmPasswordReset: (token: string, password: string) =>
    api<UserResponse>("/auth/password-reset/confirm", { method: "POST", body: { token, password }, rotatesCsrf: true }),
};
