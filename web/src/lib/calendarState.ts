import type { CalendarOccurrence } from "../types";
import { registrationStatusLabel } from "./status";

export type CalendarState = "going" | "maybe" | "waitlisted" | "declined" | "not_answered" | null;

// Where a calendar row leads: its exact-date page when the server scopes it to
// one date (invitation or kept date), else the event page.
export function occurrenceLink(occ: CalendarOccurrence): string {
  return occ.dateUrl || `/events/${occ.eventId}`;
}

export function calendarState(occ: CalendarOccurrence): CalendarState {
  if (occ.myStatus) return occ.myStatus;
  return occ.ownScope ? "not_answered" : null;
}

export function calendarStateLabel(occ: CalendarOccurrence): string | null {
  const state = calendarState(occ);
  return state ? registrationStatusLabel(state) : null;
}

// Maybe, waitlisted and unanswered rows are drawn quieter than the rest.
export function ghosted(state: CalendarState): boolean {
  return state === "maybe" || state === "waitlisted" || state === "not_answered";
}
