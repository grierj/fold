import type {
  Badge,
  EmailKind,
  EmailSettingsLinkResponse,
  Notification,
  NotificationFeed,
  NotificationFilter,
  NotificationSettings as NotificationSettingsData,
  PushConfig,
  PushSettings,
  PushSubscribeResponse,
  PushTestResponse,
} from "../types";
import { api, apiIfChanged, path, query, type Id } from "./core";

export const Notifications = {
  list: (filter: NotificationFilter = "all", before = "") => api<NotificationFeed>(`/notifications${query({ filter, before })}`),
  // badge sends the last ETag; null means "unchanged" (304).
  badge: async (etag: string): Promise<{ badge: Badge; etag: string } | null> => {
    const got = await apiIfChanged<Badge>("/notifications/badge", etag);
    return got && { badge: got.data, etag: got.etag };
  },
  read: (id: Id, read = true) => api<{ notification: Notification }>(path`/notifications/${id}/read`, { method: read ? "POST" : "DELETE" }),
  readAll: () => api<unknown>("/notifications/read-all", { method: "POST" }),
  archive: (id: Id, archived = true) =>
    api<{ notification: Notification }>(path`/notifications/${id}/archive`, { method: archived ? "POST" : "DELETE" }),
};

// One email switch (DEC-97, DEC-115): a kind of email, or with no category
// every optional kind at once.
export interface EmailSettingsChange {
  category?: EmailKind["category"];
  off: boolean;
}

// Me > Notifications: the signed-in email switches.
export const NotificationSettings = {
  get: () => api<{ settings: NotificationSettingsData }>("/me/notification-settings"),
  update: (change: EmailSettingsChange) =>
    api<{ settings: NotificationSettingsData }>("/me/notification-settings", { method: "PUT", body: change }),
  // One Push switch (DEC-122): an optional kind on every device.
  updatePush: (category: EmailKind["category"], off: boolean) =>
    api<{ settings: NotificationSettingsData }>("/me/notification-settings", { method: "PUT", body: { channel: "push", category, off } }),
};

// Notifications on this device (M3.13b). A device is named by its endpoint,
// which only this browser and the server know; no response carries one.
export const Push = {
  config: () => api<PushConfig>("/push/config"),
  // Past 30 saves an hour, saved is false with the line to show (DEC-129).
  save: (subscription: PushSubscriptionJSON) => api<PushSubscribeResponse>("/push/subscriptions", { method: "PUT", body: subscription }),
  remove: (endpoint: string) => api<Record<string, never>>("/push/subscriptions", { method: "DELETE", body: { endpoint } }),
  turnOffEverywhere: () => api<{ push: PushSettings }>("/push/turn-off", { method: "POST" }),
  test: (endpoint: string) => api<PushTestResponse>("/push/test", { method: "POST", body: { endpoint } }),
};

// The email settings page an email's link opens, without signing in (M3.3b).
// The token in the link is the authority.
export const EmailSettingsLink = {
  get: (token: string) => api<EmailSettingsLinkResponse>(path`/email-settings/${token}`),
  update: (token: string, change: EmailSettingsChange) =>
    api<{ emailSettings: NotificationSettingsData }>(path`/email-settings/${token}`, { method: "PUT", body: change }),
};
