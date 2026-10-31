import { api } from "./api";
import type { AudienceSummary, ComposeOptions, Message, MessageAudience, MessagePreview, MessageRecipients } from "./types";

// The composer's API (M3.4, docs/API.md "Messages"). Email only for now.
export interface MessageDraft {
  subject: string;
  bodyHtml: string;
  audience: MessageAudience;
}

export const Messages = {
  // eventId narrows it to that event: listed only when the viewer may write to it.
  options: (eventId?: number) => api<ComposeOptions>(`/messages/compose${eventId ? `?event=${eventId}` : ""}`),
  audiencePreview: (audience: MessageAudience) =>
    api<{ summary: AudienceSummary }>("/messages/audience-preview", { method: "POST", body: { audience } }),
  // category: one kind only, each with its own 200 (an admin's chips, M3.6b).
  list: (category?: "announcement" | "reminder") => api<{ messages: Message[] }>(`/messages${category ? `?category=${category}` : ""}`),
  get: (id: number) => api<{ message: Message }>(`/messages/${id}`),
  create: (draft: MessageDraft) => api<{ message: Message }>("/messages", { method: "POST", body: { channel: "email", ...draft } }),
  update: (id: number, draft: MessageDraft) =>
    api<{ message: Message }>(`/messages/${id}`, { method: "PUT", body: { channel: "email", ...draft } }),
  // scheduledAt absent: send now.
  schedule: (id: number, scheduledAt?: string) =>
    api<{ message: Message; summary: AudienceSummary }>(`/messages/${id}/schedule`, { method: "POST", body: scheduledAt ? { scheduledAt } : {} }),
  cancel: (id: number) => api<{ message: Message }>(`/messages/${id}/cancel`, { method: "POST" }),
  preview: (id: number) => api<{ preview: MessagePreview }>(`/messages/${id}/preview`),
  test: (id: number) => api<{ ok: boolean }>(`/messages/${id}/test`, { method: "POST" }),
  // Who got it (M3.5b), and "Try again" for the people it failed for (DEC-131).
  recipients: (id: number) => api<MessageRecipients>(`/messages/${id}/recipients`),
  retry: (id: number) => api<{ message: Message }>(`/messages/${id}/retry`, { method: "POST" }),
};

// The HTML part as an iframe shows it (served with its own sandboxing CSP).
export const previewHTMLPath = (id: number) => `/api/v1/messages/${id}/preview.html`;
