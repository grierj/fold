import { useLoad } from "../../lib/useLoad";
import { useAuth } from "../../auth";
import { Messages } from "../../api/messages";

// Where a page sends a leader to write to its people (M3.4c). Every entry
// point uses these words and paths, so a group, an event and a date read
// the same (AGENTS.md, Consistency).
export const EMAIL_MEMBERS = "Email members";
export const EMAIL_GOING = "Email people going";

export function composePath(to: { group: number } | { event: number } | { occurrence: number }): string {
  const [key, id] = Object.entries(to)[0];
  return `/messages/new?${key}=${id}`;
}

// useCanEmailEvent: the viewer may write to this event's people, for a page
// whose own data says nothing about managing it (the date page). It asks
// only people who can compose at all, once per event, and only about that
// event (GET /messages/compose?event=).
export function useCanEmailEvent(eventId: number | undefined): boolean {
  const { canCompose } = useAuth();
  // A failed read means no link: the composer itself decides what the person may do.
  const load = useLoad(async () => (await Messages.options(eventId!)).events.some((e) => e.id === eventId), [canCompose, eventId], { enabled: canCompose && !!eventId });
  const can = load.data === true;
  return can;
}
