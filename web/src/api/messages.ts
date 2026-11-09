import type {
  AudienceSummaryResponse,
  ComposeOptions,
  MessageAudience,
  MessageListResponse,
  MessagePreviewResponse,
  MessageReadResponse,
  MessageRecipients,
  MessageResponse,
  MessageScheduledResponse,
  MessageTestResponse,
} from "../types";
import { api, path, query, type Id } from "./core";

// The composer's API (M3.4, docs/API.md "Messages"). Email only for now;
// pushAlert adds the companion push (M3.13d2), never instead of the email.
interface MessageDraft {
  subject: string;
  bodyHtml: string;
  audience: MessageAudience;
  pushAlert: boolean;
}

export const Messages = {
  // eventId narrows it to that event: listed only when the viewer may write to it.
  options: (eventId?: Id) => api<ComposeOptions>(`/messages/compose${query({ event: eventId })}`),
  audiencePreview: (audience: MessageAudience) =>
    api<AudienceSummaryResponse>("/messages/audience-preview", { method: "POST", body: { audience } }),
  // Each page rechecks author/category access. Omitted limit keeps the legacy 200.
  list: (category?: "announcement" | "reminder", options?: { cursor?: string; search?: string; status?: string; limit?: number }) =>
    api<MessageListResponse>(`/messages${query({ category, ...options })}`),
  get: (id: Id) => api<MessageResponse>(path`/messages/${id}`),
  create: (draft: MessageDraft) => api<MessageResponse>("/messages", { method: "POST", body: { channel: "email", ...draft } }),
  update: (id: Id, draft: MessageDraft) =>
    api<MessageResponse>(path`/messages/${id}`, { method: "PUT", body: { channel: "email", ...draft } }),
  // scheduledAt absent: send now.
  schedule: (id: Id, scheduledAt?: string) =>
    api<MessageScheduledResponse>(path`/messages/${id}/schedule`, { method: "POST", body: scheduledAt ? { scheduledAt } : {} }),
  cancel: (id: Id) => api<MessageResponse>(path`/messages/${id}/cancel`, { method: "POST" }),
  preview: (id: Id) => api<MessagePreviewResponse>(path`/messages/${id}/preview`),
  test: (id: Id) => api<MessageTestResponse>(path`/messages/${id}/test`, { method: "POST" }),
  // Who got it (M3.5b), and "Try again" for the people it failed for (DEC-131).
  recipients: (id: Id) => api<MessageRecipients>(path`/messages/${id}/recipients`),
  retry: (id: Id) => api<MessageResponse>(path`/messages/${id}/retry`, { method: "POST" }),
  // An announcement as its reader got it: the page a push opens (M3.13d2).
  read: (id: Id) => api<MessageReadResponse>(path`/messages/${id}/read`),
};

// The HTML part as an iframe shows it (served with its own sandboxing CSP).
export const previewHTMLPath = (id: Id) => `/api/v1${path`/messages/${id}/preview.html`}`;
