import { useId, useState } from "react";
import { Button } from "./ui/Button";
import type { EventOccurrence } from "../types";

// Attendance for one date (UI makeover PR 6, DEC-63): counts first, then
// an expand that lists names, shown only when the server says this viewer
// may see them (attendeesVisible; the rules live in api_event_access.go).
// "Can't go" is never among attendees. Only the event's leaders and admins
// get a declined count and names from the server (DEC-64); for them they
// show after the attendees, under "Can't go".
export function Attendance({ instance }: { instance: EventOccurrence }) {
  const [open, setOpen] = useState(false);
  const listId = useId();
  const going = instance.registrations.going ?? 0;
  const maybe = instance.registrations.maybe ?? 0;
  const declined = instance.registrations.declined; // undefined unless you run the event
  const cant = (instance.attendees ?? []).filter((a) => a.status === "declined");
  const coming = instance.attendeesVisible
    ? (instance.attendees ?? []).filter((a) => a.status === "going" || a.status === "maybe")
    : [];
  return (
    <div className="mt-1 text-sm text-text-muted">
      <div>
        {going === 0 && maybe === 0 ? "No one yet" : `${going} going${maybe ? ` · ${maybe} maybe` : ""}`}
        {declined ? ` · ${declined} can't go` : ""}
        {instance.capacity !== null && ` · ${instance.spotsLeft} of ${instance.capacity} spots left`}
        {instance.remoteAvailable &&
          instance.remoteCapacity !== null &&
          ` · ${instance.remoteSpotsLeft} of ${instance.remoteCapacity} remote spots left`}
      </div>
      {instance.remoteAvailable && <div>Remote attendance available</div>}
      {coming.length + cant.length > 0 && (
        <>
          <Button variant="link" aria-expanded={open} aria-controls={listId} onClick={() => setOpen((v) => !v)}>
            {open ? "Hide names" : "Show who's coming"}
          </Button>
          {open && (
            <ul id={listId} className="space-y-0.5">
              {coming.map((a) => (
                <li key={a.userId}>
                  {a.name}
                  {a.status === "maybe" && " (maybe)"}
                  {a.attendanceMode === "remote" && " (remote)"}
                  {a.guestCount > 0 && ` +${a.guestCount}`}
                </li>
              ))}
              {cant.length > 0 && <li className="pt-1 font-medium">Can&apos;t go</li>}
              {cant.map((a) => <li key={a.userId}>{a.name}</li>)}
            </ul>
          )}
        </>
      )}
    </div>
  );
}
