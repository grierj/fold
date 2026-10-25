import { api } from "./api";
import type { ContactGrant, ContactRequest, ContactReveal } from "./types";

type ContactScope = Pick<ContactRequest, "subjectUserId" | "contextType" | "fields"> & { contextId: number };

export const Contacts = {
  create: (body: ContactScope & { reason?: string }) => api<{ request: ContactRequest }>("/contact-requests", { method: "POST", body }),
  requests: (before?: number) => api<{ requests: ContactRequest[] }>(`/contact-requests${before === undefined ? "" : `?before=${before}`}`),
  request: (id: number) => api<{ request: ContactRequest }>(`/contact-requests/${id}`),
  decide: (id: number, party: "subject" | "admin", decision: "approved" | "denied") => api<{ request: ContactRequest }>(`/contact-requests/${id}/decision`, { method: "POST", body: { party, decision } }),
  withdraw: (id: number) => api<Record<string, never>>(`/contact-requests/${id}/withdraw`, { method: "POST" }),
  grants: (before?: number) => api<{ grants: ContactGrant[] }>(`/contact-grants${before === undefined ? "" : `?before=${before}`}`),
  revoke: (id: number) => api<Record<string, never>>(`/contact-grants/${id}/revoke`, { method: "POST" }),
  reveal: (id: number, body: ContactScope) => api<ContactReveal>(`/contact-grants/${id}/reveal`, { method: "POST", body }),
};
