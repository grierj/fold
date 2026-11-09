import { childcareBody, childcareProblem, emptyChildcare, isChildcareCapacityError, type ChildcareValue } from "./ChildcareField";
import { DEFAULT_REMINDER_MINUTES } from "./ReminderField";
import { isRemoteError, emptyRemote, remoteBody, remoteProblem, type RemoteValue } from "./RemoteField";
import { emptySeats, isSeatsError, seatsBody, seatsProblem, type SeatsValue } from "./SeatsField";
import { emptyVideo, isVideoError, videoBody, videoProblem, type VideoValue } from "./VideoField";

// The values behind the More options section of every event form: one object,
// one check and one wire body, so a new option touches this file and
// EventOptions, not each form.
export interface EventOptionValues {
  seats: SeatsValue;
  video: VideoValue;
  remote: RemoteValue;
  childcare: ChildcareValue;
  reminder: number;
  reminderPush: boolean;
}

export const emptyEventOptions = (): EventOptionValues => ({
  seats: emptySeats,
  video: emptyVideo,
  remote: emptyRemote,
  childcare: emptyChildcare,
  reminder: DEFAULT_REMINDER_MINUTES,
  reminderPush: true,
});

export type OptionField = "seats" | "video" | "childcare" | "remote";
export type OptionErrors = Partial<Record<OptionField, string>>;
export interface OptionProblem {
  field: OptionField;
  message: string;
}

// The first problem in the order the section reads: seats, video, childcare,
// remote. The server checks again.
export function validateEventOptions(v: EventOptionValues): OptionProblem | null {
  const checks: [OptionField, string | null][] = [
    ["seats", seatsProblem(v.seats)],
    ["video", videoProblem(v.video)],
    ["childcare", childcareProblem(v.childcare)],
    ["remote", remoteProblem(v.remote)],
  ];
  const hit = checks.find(([, message]) => message);
  return hit ? { field: hit[0], message: hit[1]! } : null;
}

// Which option a server error is about, or null when it is about none of them.
export function optionFieldOfError(err: unknown): OptionField | null {
  if (isSeatsError(err)) return "seats";
  if (isChildcareCapacityError(err)) return "childcare";
  if (isRemoteError(err)) return "remote";
  if (isVideoError(err)) return "video";
  return null;
}

export function eventOptionsBody(v: EventOptionValues) {
  return {
    ...videoBody(v.video),
    ...seatsBody(v.seats),
    ...remoteBody(v.remote),
    ...childcareBody(v.childcare),
    reminderEmailMinutes: v.reminder,
    reminderPush: v.reminderPush,
  };
}
