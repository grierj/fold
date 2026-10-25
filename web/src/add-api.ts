import { api } from "./api";
import type { AddRequest, AttendanceMode } from "./types";

type AddScope = { contextType: "group"; contextId: number; overLimit?: boolean } | {
  contextType: "event"; contextId: number; occurrenceId: number; overCapacity?: boolean;
  attendanceMode?: AttendanceMode; guestCount?: number; childcareCount?: number; childcareAgesNote?: string;
};

export type AddRequestInput = AddScope & { selectionId: string; reason?: string };

export const Adds = {
  create: (body: AddRequestInput) => api<{ request: AddRequest }>("/add-requests", { method: "POST", body }),
  requests: (before?: number) => api<{ requests: AddRequest[] }>(`/add-requests${before === undefined ? "" : `?before=${before}`}`),
  request: (id: number) => api<{ request: AddRequest }>(`/add-requests/${id}`),
  decide: (id: number, party: "subject" | "admin", decision: "approved" | "denied") => api<{ request: AddRequest }>(`/add-requests/${id}/decision`, { method: "POST", body: { party, decision } }),
  withdraw: (id: number) => api<{ request: AddRequest }>(`/add-requests/${id}/withdraw`, { method: "POST" }),
  complete: (id: number) => api<{ request: AddRequest }>(`/add-requests/${id}/complete`, { method: "POST" }),
};
