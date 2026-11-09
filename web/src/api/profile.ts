import type { CalendarFeed, ProfileUpdateResponse, User } from "../types";
import { api, apiUpload } from "./core";

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
  update: (data: ProfileUpdate) => api<ProfileUpdateResponse>("/me", { method: "PUT", body: data }),
  // Verified email change (M1.2): the token is the credential.
  verifyEmail: (token: string) => api<{ user: User }>("/me/email/verify", { method: "POST", body: { token } }),
  cancelEmailChange: (token: string) => api<{ cancelled: true }>("/me/email/cancel", { method: "POST", body: { token }, anonymousOK: true }),
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
