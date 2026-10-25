import { api } from "./api";
import type { AttendanceMode, EventDate, RegistrationResponse } from "./types";

function datePath(id: string, date: string) {
  return `/events/${encodeURIComponent(id)}/dates/${encodeURIComponent(date)}`;
}

export const Dates = {
  get: (id: string, date: string) => api<{ occurrence: EventDate }>(datePath(id, date)),
  // Care options are deliberately absent: the date DTO cannot describe them.
  register: (id: string, date: string, answer: { status: "going" | "maybe"; attendanceMode?: AttendanceMode; guestCount?: number }) =>
    api<{ registration: RegistrationResponse }>(`${datePath(id, date)}/registration`, { method: "PUT", body: answer }),
  end: (id: string, date: string, action: "decline" | "block") =>
    api<Record<string, never>>(`${datePath(id, date)}/${action}`, { method: "POST" }),
};
