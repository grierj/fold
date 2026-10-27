import type { EventOccurrence, RegistrationResponse, SeriesRegistrationState } from "../types";

export type ActorRsvpResult = { date: string; after: SeriesRegistrationState };

export type SeriesFeedbackSignal = {
  eventId: number;
  date: string;
  revision: number;
  actorResults: ActorRsvpResult[];
};

export function actorResultFromRegistration(registration: RegistrationResponse): ActorRsvpResult {
  return {
    date: registration.date,
    after: {
      registrationId: registration.registrationId,
      status: registration.status,
      guestCount: registration.guestCount,
      attendanceMode: registration.attendanceMode,
      childcareCount: registration.childcareCount,
      childcareAgesNote: registration.childcareAgesNote,
      ...(registration.waitlistPosition == null ? {} : { waitlistPosition: registration.waitlistPosition }),
    },
  };
}

export function applyActorRsvpResults(instance: EventOccurrence, eventId: number, actorResults: ActorRsvpResult[]): EventOccurrence {
  if (instance.eventId !== eventId) return instance;
  const result = actorResults.find((row) => row.date === instance.date);
  if (!result) return instance;
  const { after } = result;
  const hasRegistration = after.status !== undefined;
  return {
    ...instance,
    myRegistration: after.status ?? null,
    myAttendanceMode: hasRegistration ? after.attendanceMode ?? "in_person" : undefined,
    myGuestCount: hasRegistration ? after.guestCount ?? 0 : undefined,
    myChildcareCount: hasRegistration ? after.childcareCount ?? 0 : undefined,
    myChildcareAgesNote: hasRegistration ? after.childcareAgesNote ?? "" : undefined,
    myWaitlistPosition: after.status === "waitlisted" ? after.waitlistPosition : undefined,
  };
}
