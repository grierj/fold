import type {
  CoverUploadResponse,
  DepartureConfirmationResponse,
  DeparturePreviewResponse,
  EventSeriesResponse,
  GroupDetailResponse,
  GroupListResponse,
  GroupResponse,
  MembersResponse,
  NewEventInput,
  OpenedDecision,
  Visibility,
  VisibilityImpact,
} from "../types";
import { api, apiUpload, path, query, type Id } from "./core";

export interface GroupInput {
  name: string;
  description: string;
  tags: string[];
  location: string;
  visibility: Visibility;
  memberLimit: number | null;
}

export interface GroupUpdate extends Partial<GroupInput> {
  coverFocal?: { x: number; y: number };
  // Keep or tighten each leader-opened event when the group becomes more private (DEC-48).
  openedEvents?: Record<string, OpenedDecision>;
}

export const Groups = {
  list: (search = "", tags: string[] = []) => api<GroupListResponse>(`/groups${query({ search, tag: tags })}`),
  get: (id: Id, page?: { cursor?: string; limit?: number }) =>
    api<GroupDetailResponse>(`${path`/groups/${id}`}${query({ cursor: page?.cursor, limit: page?.limit })}`),
  mine: () => api<GroupListResponse>("/me/groups"),
  create: (data: GroupInput) => api<GroupResponse>("/groups", { method: "POST", body: data }),
  update: (id: Id, data: GroupUpdate) => api<GroupResponse>(path`/groups/${id}`, { method: "PUT", body: data }),
  visibilityImpact: (id: Id, visibility: Visibility) =>
    api<VisibilityImpact>(`${path`/groups/${id}/visibility-impact`}${query({ visibility })}`),
  join: (id: Id) => api<unknown>(path`/groups/${id}/join`, { method: "POST" }),
  departurePreview: (id: Id) => api<DeparturePreviewResponse>(path`/groups/${id}/departure-preview`),
  confirmDeparture: (id: Id, snapshot: string, keepOccurrenceIds: number[]) =>
    api<DepartureConfirmationResponse>(path`/groups/${id}/leave`, { method: "POST", body: { snapshot, keepOccurrenceIds } }),
  memberDeparturePreview: (id: Id, userId: Id) =>
    api<DeparturePreviewResponse>(path`/groups/${id}/members/${userId}/departure-preview`),
  confirmMemberDeparture: (id: Id, userId: Id, snapshot: string, keepOccurrenceIds: number[], note?: string) =>
    api<DepartureConfirmationResponse>(path`/groups/${id}/members/${userId}/departure`, { method: "POST", body: { snapshot, keepOccurrenceIds, note } }),
  // Make a member a leader, or a leader a member again (audited; admins are notified).
  setRole: (id: Id, userId: Id, role: "leader" | "member") =>
    api<MembersResponse>(path`/groups/${id}/members/${userId}/role`, { method: "PUT", body: { role } }),
  // overLimit: add even though the group is full (an explicit, audited choice; DEC-39).
  addMember: (id: Id, target: string | { selectionId: string }, overLimit = false) =>
    api<unknown>(path`/groups/${id}/members`, { method: "POST", body: { ...(typeof target === "string" ? { email: target } : target), overLimit } }),
  requestDeletion: (id: Id) => api<GroupResponse>(path`/groups/${id}/deletion-request`, { method: "POST" }),
  cancelDeletion: (id: Id) => api<unknown>(path`/groups/${id}/deletion-request`, { method: "DELETE" }),
  members: (id: Id) => api<MembersResponse>(path`/groups/${id}/members`),
  createEvent: (id: Id, data: NewEventInput) => api<EventSeriesResponse>(path`/groups/${id}/events`, { method: "POST", body: data }),
};

export const GroupCover = {
  upload: (id: Id, file: File) => apiUpload<CoverUploadResponse>(path`/groups/${id}/cover`, "cover", file),
  remove: (id: Id) => api<Record<string, never>>(path`/groups/${id}/cover`, { method: "DELETE" }),
};
