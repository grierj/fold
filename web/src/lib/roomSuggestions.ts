import type { RoomSuggestion, RoomSuggestions } from "../types";

// How a room picker groups its rooms when it has suggestions (M2.3, PLAN 9.3,
// DEC-110): the suggested rooms first, best first, each with its reason, then
// every other bookable room in the picker's usual order. A room is listed
// once, and every room stays pickable by hand (Rule 5). All three Room fields
// use this, so they can't drift.
export interface SuggestedRooms<R> {
  // "Suggested", or "Used by other kinds of events" when none matched the
  // group's kind (Rule 3).
  label: string;
  suggested: { room: R; reason: string }[];
  others: R[];
}

// Null when there is nothing to suggest among these rooms: the picker then
// lists its rooms as it always has.
export function suggestedRooms<R extends { id: number }>(rooms: R[], suggestions: RoomSuggestions | null | undefined): SuggestedRooms<R> | null {
  if (!suggestions || suggestions.rooms.length === 0) return null;
  const byId = new Map(rooms.map((r) => [r.id, r]));
  const seen = new Set<number>();
  const suggested: { room: R; reason: string }[] = [];
  const best: RoomSuggestion[] = suggestions.rooms;
  for (const s of best) {
    const room = byId.get(s.roomId);
    // A suggestion for a room this picker doesn't list can't be picked here.
    if (!room || seen.has(room.id)) continue;
    seen.add(room.id);
    suggested.push({ room, reason: s.reason });
  }
  if (suggested.length === 0) return null;
  return {
    label: suggestions.otherKinds ? "Used by other kinds of events" : "Suggested",
    suggested,
    others: rooms.filter((r) => !seen.has(r.id)),
  };
}
