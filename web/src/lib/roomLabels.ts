import type { RoomPickerItem, SeriesBooking, SeriesRoom } from "../types";
import { roomName } from "./place";
import { count } from "./plural";

// The words the room pickers use, in one place. e2e/helpers.ts `pickRoom`
// reads the option label back (" — " separators, "(N seats)"), so a change to
// the shape of a label changes that helper with it.

// The select value for "no room".
export const NO_ROOM = "none";

// An admin's reason for double-booking may be this long.
// Mirrors maxOverlapReason in api_room_bookings.go.
export const OVERLAP_REASON_MAX = 500;

// " (80 seats)", or nothing for a room with no size.
function seatsSuffix(room: { capacity: number | null }): string {
  return room.capacity === null ? "" : ` (${count(room.capacity, "seat")})`;
}

// A room as the single-date picker words it: "Fellowship Hall · Main (80 seats)",
// then what a leader should know before choosing it, then why it was suggested,
// if it was (last, so a phone that clips the option still shows the busy note).
export function roomOptionLabel(room: RoomPickerItem, reason?: string): string {
  return `${roomName(room)}${seatsSuffix(room)}${room.busy ? " — in use at this time" : ""}${room.tooSmall ? " — may be too small" : ""}${reason ? ` — ${reason}` : ""}`;
}

// The same for the whole-series picker: how many of the dates something else
// holds the room on. `dates` is how many dates the booking would cover.
export function seriesRoomLabel(room: SeriesRoom, dates: number, reason?: string): string {
  let busy = "";
  if (room.busyDates > 0) busy = dates <= 1 ? " — in use at this time" : ` — in use on ${room.busyDates} of ${dates} dates`;
  return `${roomName(room)}${seatsSuffix(room)}${busy}${room.tooSmall ? " — may be too small" : ""}${reason ? ` — ${reason}` : ""}`;
}

// What picking a room will do to the dates it is busy on, said before it is
// booked. `where` is the page that lists those dates afterwards.
export function busyDatesNote(room: SeriesRoom, dates: number, where: "edit" | "create"): string | null {
  if (room.busyDates <= 0) return null;
  const name = roomName(room);
  const listed = where === "edit" ? "the busy dates will be listed under “Dates that need a room”." : "the event page will list the busy dates under “Dates that need a room”.";
  if (dates <= 1) {
    return where === "edit"
      ? `${name} is in use at this time, so the date will need a room; ${listed}`
      : `${name} is in use at this time, so the event will start without a room. The event page will ask you to pick one.`;
  }
  if (room.busyDates >= dates) {
    return where === "edit"
      ? `${name} is in use on all ${dates} dates, so none can be booked yet; ${listed}`
      : `${name} is in use on all ${dates} dates, so none can be booked yet. The event page will list them under “Dates that need a room”.`;
  }
  return `${name} is in use on ${room.busyDates} of ${dates} dates. It will be booked on the other ${dates - room.busyDates}, and ${listed}`;
}

// What a series booking did, in words. `over` is how many of the booked dates
// an admin double-booked.
export function seriesBookingMessage(res: SeriesBooking, over = 0): string {
  const dates = (n: number) => count(n, "date");
  const parts: string[] = [];
  if (res.defaultRoom === null) parts.push("Series room released.");
  else if (res.booked > 0) {
    parts.push(over > 0
      ? `Booked for ${dates(res.booked)}, ${over} of them over another booking.`
      : `Booked for ${dates(res.booked)}.`);
  }
  if (res.defaultRoom !== null && res.pending > 0) parts.push(`Sent to an admin for approval: ${dates(res.pending)}.`);
  const n = res.lost.length;
  if (n > 0) {
    parts.push(`${dates(n)} ${n === 1 ? "is" : "are"} busy; pick another room for ${n === 1 ? "it" : "them"} under “Dates that need a room” on the event page.`);
  }
  if (res.kept > 0) {
    parts.push(`${dates(res.kept)} ${res.kept === 1 ? "keeps" : "keep"} the room picked for ${res.kept === 1 ? "it" : "them"}.`);
  }
  if (parts.length === 0) parts.push("There are no upcoming dates to book.");
  return parts.join(" ");
}
