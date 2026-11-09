import type { Attendee, HouseholdAttendee } from "../types";

// Mixed wire rows become an exclusive presentation identity at the API
// boundary. Invalid both/neither identities never become action targets.
export function attendeeSubject(row: Attendee): { kind: "user"; userId: number } | { kind: "household"; person: HouseholdAttendee } | null {
  if (row.userId !== undefined && !row.householdPerson) return { kind: "user", userId: row.userId };
  if (row.userId === undefined && row.householdPerson) return { kind: "household", person: row.householdPerson };
  return null;
}
