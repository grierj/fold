import type { AddRequestResponse, AddRequestsResponse, AttendanceMode } from "../types";
import { api, path, query, type Id } from "./core";

type AddScope = { contextType: "group"; contextId: number; overLimit?: boolean } | {
  contextType: "event"; contextId: number; occurrenceId: number; overCapacity?: boolean;
  attendanceMode?: AttendanceMode; guestCount?: number; childcareCount?: number; childcareAgesNote?: string;
};

export type AddRequestInput = AddScope & { selectionId: string; reason?: string };

export const Adds = {
  create: (body: AddRequestInput) => api<AddRequestResponse>("/add-requests", { method: "POST", body }),
  requests: (before?: number) => api<AddRequestsResponse>(`/add-requests${query({ before })}`),
  request: (id: Id) => api<AddRequestResponse>(path`/add-requests/${id}`),
  decide: (id: Id, party: "subject" | "admin", decision: "approved" | "denied") =>
    api<AddRequestResponse>(path`/add-requests/${id}/decision`, { method: "POST", body: { party, decision } }),
  withdraw: (id: Id) => api<AddRequestResponse>(path`/add-requests/${id}/withdraw`, { method: "POST" }),
  complete: (id: Id) => api<AddRequestResponse>(path`/add-requests/${id}/complete`, { method: "POST" }),
};
