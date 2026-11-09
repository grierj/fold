import type {
  AttendanceMode,
  EventDate,
  EventDetailResponse,
  EventHostRow,
  EventInput,
  EventOccurrence,
  EventSeries,
  GuestConfirmation,
  GuestRegistration,
  NewEventInput,
  RegistrationBatch,
  RegistrationCommand,
  RegistrationResponse,
  SeriesRegistrationResponse,
  Visibility,
  VisibilityImpact,
} from "../types";
import { api, path, query, type Id } from "./core";

export const Events = {
  listOpen: () => api<{ events: EventOccurrence[] }>("/events"),
  get: (id: Id) => api<EventDetailResponse>(path`/events/${id}`),
  createChurch: (data: NewEventInput & { title: string }) => api<{ event: EventSeries }>("/events", { method: "POST", body: data }),
  setVisibility: (
    id: Id,
    // confirm: the leader saw the preview of registrations this cancels.
    data: { visibility: Visibility; reason?: string; confirm?: boolean; resetOverrides?: boolean }
  ) => api<{ event: EventSeries }>(path`/events/${id}/visibility`, { method: "PUT", body: data }),
  visibilityImpact: (id: Id, visibility: Visibility, resetOverrides = false) =>
    api<VisibilityImpact>(`${path`/events/${id}/visibility-impact`}${query({ visibility, resetOverrides: resetOverrides ? "1" : "0" })}`),
  addHost: (id: Id, email: string) => api<{ hosts: EventHostRow[] }>(path`/events/${id}/hosts`, { method: "POST", body: { email } }),
  removeHost: (id: Id, userId: Id) => api<{ hosts: EventHostRow[] }>(path`/events/${id}/hosts/${userId}`, { method: "DELETE" }),
  update: (id: Id, data: EventInput) => api<{ event: EventSeries }>(path`/events/${id}`, { method: "PUT", body: data }),
  generateJitsi: () => api<{ url: string }>("/events/video/jitsi", { method: "POST", body: {} }),
  register: (id: Id, data: RegistrationCommand) => api<RegistrationBatch>(path`/events/${id}/registrations`, { method: "PUT", body: data }),
  registerSeries: (id: Id, data: {
    date?: string;
    scope: "all_future" | "this_and_following";
    status: "going" | "maybe" | "declined";
    guestCount?: number;
    attendanceMode?: AttendanceMode;
    childcareCount?: number;
    childcareAgesNote?: string;
  }) => api<SeriesRegistrationResponse>(path`/events/${id}/series-registrations`, { method: "POST", body: data }),
  undoSeriesRegistration: (id: Id, token: string) =>
    api<SeriesRegistrationResponse>(path`/events/${id}/series-registrations/undo`, { method: "POST", body: { token } }),
  removeRegistration: (id: Id, date: string, registrationId: number, note?: string) =>
    api<unknown>(path`/events/${id}/registrations/remove`, { method: "POST", body: { date, registrationId, note } }),
  promoteRegistration: (id: Id, date: string, registrationId: number) =>
    api<unknown>(path`/events/${id}/registrations/promote`, { method: "POST", body: { date, registrationId } }),
  addRegistration: (id: Id, date: string, target: string | { selectionId: string }, overCapacity?: boolean, childcare?: { childcareCount: number; childcareAgesNote: string }) =>
    api<unknown>(path`/events/${id}/registrations/add`, { method: "POST", body: { date, ...(typeof target === "string" ? { email: target } : target), overCapacity, ...childcare } }),
  restoreRegistration: (id: Id, date: string, target: { userId: number } | { personId: number; registeredBy: number }, attendanceMode: AttendanceMode, overCapacity: boolean) =>
    api<unknown>(path`/events/${id}/registrations/add`, { method: "POST", body: { date, ...target, attendanceMode, overCapacity } }),
  cancelOccurrence: (id: Id, date: string) => api<unknown>(path`/events/${id}/cancel`, { method: "POST", body: { date } }),
  dateVisibilityImpact: (id: Id, date: string, visibility: Visibility | "series") =>
    api<VisibilityImpact>(`${path`/events/${id}/occurrences/${date}/visibility-impact`}${query({ visibility })}`),
  updateOccurrence: (
    id: Id,
    date: string,
    data: { startsLocal?: string; durationMinutes?: number; location?: string; title?: string; capacityOverride?: number | null; remoteCapacityOverride?: number | null; visibility?: Visibility | "series" | null; reason?: string; confirm?: boolean }
  ) => api<{ occurrence: EventOccurrence }>(path`/events/${id}/occurrences/${date}`, { method: "PUT", body: data }),
  delete: (id: Id) => api<unknown>(path`/events/${id}`, { method: "DELETE" }),
};

// One date of an event: /events/:id/dates/:date, where :date is the
// occurrence's original YYYY-MM-DD.
export const Dates = {
  get: (id: Id, date: string) => api<{ occurrence: EventDate }>(path`/events/${id}/dates/${date}`),
  // Care options are deliberately absent: the date DTO cannot describe them.
  register: (id: Id, date: string, answer: { status: "going" | "maybe"; attendanceMode?: AttendanceMode; guestCount?: number }) =>
    api<{ registration: RegistrationResponse }>(path`/events/${id}/dates/${date}/registration`, { method: "PUT", body: answer }),
  end: (id: Id, date: string, action: "decline" | "block") =>
    api<Record<string, never>>(path`/events/${id}/dates/${date}/${action}`, { method: "POST" }),
};

// Guest registration (M1.4): a signed-out adult registers for a public
// event by name and email. The server answers the same way whether or
// not the address already has an account. Nothing is registered until
// the emailed link is confirmed on /guest/confirm.
export interface GuestRegistrationInput {
  name: string;
  email: string;
  date: string;
  over18: boolean;
  createAccount?: boolean;
  // The browser's zone, for the guest's emails (DEC-58).
  timezone?: string;
  // Honeypot. People never fill this in.
  website?: string;
}

export const Guests = {
  register: (eventId: Id, data: GuestRegistrationInput) =>
    api<{ message: string }>(path`/events/${eventId}/guest-registrations`, { method: "POST", body: data }),
  confirm: (token: string) => api<GuestConfirmation>("/guest-registrations/confirm", { method: "POST", body: { token } }),
  getManage: (token: string) => api<{ registration: GuestRegistration }>(path`/guest-registrations/manage/${token}`),
  cancelManage: (token: string) =>
    api<{ registration: GuestRegistration }>(path`/guest-registrations/manage/${token}/cancel`, { method: "POST" }),
};
