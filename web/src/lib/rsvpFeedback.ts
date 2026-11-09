import type { EventOccurrence, RegistrationResponse, SeriesRegistrationState } from "../types";
import { savedAnswerFromState } from "./registration";

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
  const saved = savedAnswerFromState(after);
  const hasRegistration = saved.status !== null;
  return {
    ...instance,
    myRegistration: saved.status,
    myAttendanceMode: hasRegistration ? saved.mode : undefined,
    myGuestCount: hasRegistration ? saved.guests : undefined,
    myChildcareCount: hasRegistration ? saved.children : undefined,
    myChildcareAgesNote: hasRegistration ? saved.ages : undefined,
    myWaitlistPosition: saved.status === "waitlisted" ? after.waitlistPosition : undefined,
  };
}
