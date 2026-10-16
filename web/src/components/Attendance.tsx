import { useId, useState } from "react";
import { Button } from "./ui/Button";
import type { EventOccurrence } from "../types";

// Attendance for one date (UI makeover PR 6, DEC-63): counts first, then
// an expand that lists names, shown only when the server says this viewer
// may see them (attendeesVisible; the rules live in api_event_access.go).
// "Can't go" is an answer, not a list: those people are never counted or
// named here (Grier's call), they just don't appear among attendees.
export function Attendance({ instance }: { instance: EventOccurrence }) {
  const [open, setOpen] = useState(false);
  const listId = useId();
  const going = instance.registrations.going ?? 0;
  const maybe = instance.registrations.maybe ?? 0;
  const coming = instance.attendeesVisible
    ? (instance.attendees ?? []).filter((a) => a.status === "going" || a.status === "maybe")
    : [];
  return (
    <div className="mt-1 text-sm text-text-muted">
      <div>
        {going === 0 && maybe === 0 ? "No one yet" : `${going} going${maybe ? ` · ${maybe} maybe` : ""}`}
        {instance.capacity !== null && ` · ${instance.spotsLeft} of ${instance.capacity} spots left`}
      </div>
      {coming.length > 0 && (
        <>
          <Button variant="ghost" layout="-ml-3" aria-expanded={open} aria-controls={listId} onClick={() => setOpen((v) => !v)}>
            {open ? "Hide names" : "Show who's coming"}
          </Button>
          {open && (
            <ul id={listId} className="space-y-0.5">
              {coming.map((a) => (
                <li key={a.userId}>
                  {a.name}
                  {a.status === "maybe" && " (maybe)"}
                  {a.guestCount > 0 && ` +${a.guestCount}`}
                </li>
              ))}
            </ul>
          )}
        </>
      )}
    </div>
  );
}
