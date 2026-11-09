import type { AgeBand, Invitation, InvitationAcceptance, InvitationContext } from "../types";
import { api, path } from "./core";

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
  get: (token: string) => api<InvitationContext>(path`/invitations/${token}`),
  accept: (data: { token: string; name: string; password: string; ageBand: AgeBand; birthDate?: string }) =>
    api<InvitationAcceptance>("/invitations/accept", { method: "POST", body: data, rotatesCsrf: true }),
};
