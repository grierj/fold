import type { AttendanceMode, EventOccurrence, RegistrationStatus, SeriesRegistrationState } from "../types";

// The viewer's own RSVP on one event date: the request body, the seat rules
// and the saved answer. One place, so a single-date, series and household RSVP
// ask the server the same way.

export type AnswerStatus = "going" | "maybe" | "declined";

export type RsvpAnswer = {
  status: AnswerStatus;
  attendanceMode: AttendanceMode;
  guestCount: number;
  childcare?: { childcareCount: number; childcareAgesNote: string };
};

// The fields every RSVP request carries; callers add `date` or `scope`.
// Guests and childcare only travel with "going". A first "going" answer on a
// date with childcare sends an explicit "none" so the server records a choice.
export function registerBody(instance: EventOccurrence, answer: RsvpAnswer) {
  const going = answer.status === "going";
  return {
    status: answer.status,
    guestCount: going ? answer.guestCount : 0,
    attendanceMode: instance.remoteAvailable ? answer.attendanceMode : undefined,
    ...(going && instance.childcareAvailable
      ? answer.childcare ?? (instance.myRegistration !== "going" ? { childcareCount: 0, childcareAgesNote: "" } : {})
      : {}),
  };
}

// Whether a person can pick remote: the event offers it and the date has seats.
export function offersRemote(instance: EventOccurrence): boolean {
  return instance.remoteAvailable && instance.remoteCapacity !== 0;
}

// Whether the in-person or remote pool has no seats left. A null cap is
// unlimited; a missing count counts as one seat left.
export function poolFull(instance: EventOccurrence, mode: AttendanceMode): boolean {
  return mode === "remote"
    ? instance.remoteCapacity !== null && (instance.remoteSpotsLeft ?? 1) <= 0
    : instance.capacity !== null && (instance.spotsLeft ?? 1) <= 0;
}

// What the viewer has saved for a date, with the defaults the form shows.
export type SavedAnswer = { status: RegistrationStatus | null; mode: AttendanceMode; guests: number; children: number; ages: string };

export function savedAnswer(instance: EventOccurrence): SavedAnswer {
  return {
    status: instance.myRegistration,
    mode: instance.myAttendanceMode ?? "in_person",
    guests: instance.myGuestCount ?? 0,
    children: instance.myChildcareCount ?? 0,
    ages: instance.myChildcareAgesNote ?? "",
  };
}

// The same, from a server result for a date. No status means no registration,
// so every detail is its default.
export function savedAnswerFromState(state: SeriesRegistrationState): SavedAnswer {
  const has = !!state.status;
  return {
    status: state.status ?? null,
    mode: has ? state.attendanceMode ?? "in_person" : "in_person",
    guests: has ? state.guestCount ?? 0 : 0,
    children: has ? state.childcareCount ?? 0 : 0,
    ages: has ? state.childcareAgesNote ?? "" : "",
  };
}
