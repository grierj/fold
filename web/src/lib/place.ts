import type { OccurrenceRoom } from "../types";

// "Fellowship Hall · Main": the room's name, then its building when it has one.
export function roomName(room: Pick<OccurrenceRoom, "name" | "building">): string {
  return room.building ? `${room.name} · ${room.building}` : room.name;
}

// A date's place (M2.1b, PLAN 14.5 "Where rooms show"). A confirmed room
// leads and a typed location follows as a detail ("Fellowship Hall · Main ·
// north door"); without a room it is the typed location as it always was.
// Every page that names a date's place goes through here.
export function placeLine(room: OccurrenceRoom | null | undefined, location: string | null | undefined): string {
  if (!room) return location ?? "";
  const typed = (location ?? "").trim();
  return typed ? `${roomName(room)} · ${typed}` : roomName(room);
}
