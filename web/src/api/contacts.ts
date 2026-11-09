import type { ContactGrant, ContactRequest, ContactReveal } from "../types";
import { api, path, query, type Id } from "./core";

type ContactScope = Pick<ContactRequest, "subjectUserId" | "contextType" | "fields"> & { contextId: number };

export const Contacts = {
  create: (body: ContactScope & { reason?: string }) => api<{ request: ContactRequest }>("/contact-requests", { method: "POST", body }),
  requests: (before?: number) => api<{ requests: ContactRequest[] }>(`/contact-requests${query({ before })}`),
  request: (id: Id) => api<{ request: ContactRequest }>(path`/contact-requests/${id}`),
  decide: (id: Id, party: "subject" | "admin", decision: "approved" | "denied") =>
    api<{ request: ContactRequest }>(path`/contact-requests/${id}/decision`, { method: "POST", body: { party, decision } }),
  withdraw: (id: Id) => api<Record<string, never>>(path`/contact-requests/${id}/withdraw`, { method: "POST" }),
  grants: (before?: number) => api<{ grants: ContactGrant[] }>(`/contact-grants${query({ before })}`),
  revoke: (id: Id) => api<Record<string, never>>(path`/contact-grants/${id}/revoke`, { method: "POST" }),
  reveal: (id: Id, body: ContactScope) => api<ContactReveal>(path`/contact-grants/${id}/reveal`, { method: "POST", body }),
};
