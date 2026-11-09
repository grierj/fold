import type {
  AdminRoomsResponse,
  DateRoomsResponse,
  MatchLocationResponse,
  RoomsCheckResponse,
  Room,
  RoomFeed,
  RoomBooking,
  RoomRequest,
  RoomRequestConfirmation,
  SeriesBooking,
  SeriesRoomsResponse,
  TypedLocation,
} from "../types";
import { api, path, type Id } from "./core";

// Rooms (M2.1a). Everything answers 404 while rooms are off. On update, absent
// fields keep their value and a null capacity clears the limit.
export interface RoomInput {
  name?: string;
  building?: string;
  capacity?: number | null;
  features?: string;
  notes?: string;
  active?: boolean;
  restricted?: boolean;
  defaultSetupMinutes?: number;
  defaultTeardownMinutes?: number;
}

export const AdminRooms = {
  list: () => api<AdminRoomsResponse>("/admin/rooms"),
  create: (body: RoomInput) => api<{ room: Room }>("/admin/rooms", { method: "POST", body }),
  update: (id: Id, body: RoomInput) => api<{ room: Room }>(path`/admin/rooms/${id}`, { method: "PUT", body }),
  setEnabled: (enabled: boolean) => api<{ roomsEnabled: boolean }>("/admin/settings/rooms", { method: "PUT", body: { enabled } }),
  // Whether leaders' and hosts' bookings wait for an admin (M2.1d, DEC-102).
  setApproval: (enabled: boolean) => api<{ needApproval: boolean }>("/admin/settings/room-approval", { method: "PUT", body: { enabled } }),
  // Typed places on upcoming dates without a room, and booking one place's dates into a room (M2.1e).
  locations: () => api<{ locations: TypedLocation[] }>("/admin/rooms/locations"),
  matchLocation: (location: string, roomId: number) =>
    api<MatchLocationResponse>("/admin/rooms/locations/match", { method: "POST", body: { location, roomId } }),
  // A room's calendar-feed link (M2.2b): makeFeed returns the link once and
  // turns any old one off.
  getFeed: (id: Id) => api<{ feed: RoomFeed }>(path`/admin/rooms/${id}/feed`),
  makeFeed: (id: Id) => api<{ feed: RoomFeed }>(path`/admin/rooms/${id}/feed`, { method: "POST" }),
  revokeFeed: (id: Id) => api<{ feed: RoomFeed }>(path`/admin/rooms/${id}/feed`, { method: "DELETE" }),
};

// Open room requests (M2.1d, DEC-106): one per booking action, a date or a whole
// series. Confirming makes the free dates confirmed; busy dates are
// double-booked with overlapReason or go back to the leader (`lost`).
export const RoomRequests = {
  list: () => api<{ requests: RoomRequest[] }>("/admin/room-requests"),
  confirm: (id: Id, overlapReason?: string) =>
    api<RoomRequestConfirmation>(path`/admin/room-requests/${id}/confirm`, { method: "POST", body: overlapReason ? { overlapReason } : {} }),
  reject: (id: Id, reason?: string) =>
    api<Record<string, never>>(path`/admin/room-requests/${id}/reject`, { method: "POST", body: reason ? { reason } : {} }),
};

// Booking a room for one date (M2.1b). `:date` is the occurrence's original
// YYYY-MM-DD. Every route answers 404 while rooms are off. Only an admin may
// send overlapReason (to book over a conflict) or the two padding overrides.
export interface DateRoomInput {
  roomId: number | null;
  overlapReason?: string;
  setupMinutes?: number;
  teardownMinutes?: number;
}

export const DateRooms = {
  list: (eventId: Id, date: string) => api<DateRoomsResponse>(path`/events/${eventId}/dates/${date}/rooms`),
  set: (eventId: Id, date: string, body: DateRoomInput) =>
    api<{ booking: RoomBooking | null }>(path`/events/${eventId}/dates/${date}/room`, { method: "PUT", body }),
};

// A room for the whole series (M2.1c). Booking it books every scheduled
// future date; dates the room is busy on come back in `lost` and in the
// event's needsRoom list (DEC-103). roomId null stops the series room. Only
// an admin's overlapReason double-books busy dates instead.
export const SeriesRooms = {
  list: (eventId: Id) => api<SeriesRoomsResponse>(path`/events/${eventId}/rooms`),
  set: (eventId: Id, body: { roomId: number | null; overlapReason?: string }) =>
    api<SeriesBooking>(path`/events/${eventId}/room`, { method: "PUT", body }),
  // Rooms for an event not yet created: the create form sends its date
  // fields (and groupId for a group event, and the visibility chosen, which
  // room suggestions use) and books with roomId on create.
  check: (body: Record<string, unknown>) => api<RoomsCheckResponse>("/rooms/check", { method: "POST", body }),
};
